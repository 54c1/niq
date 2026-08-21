# reason — the domain-agnostic reasoning base

`reason` is niq's domain-agnostic reasoning mechanism, shared by every
reason-family worker (the generic reason worker today, a coding worker later).
It consumes events from the bus, calls the LLM, and publishes outcomes as
events. An embedding worker composes its own subscriptions, tools, event
conversion and goals (programs) onto `BaseReasonWorker`.

## Architecture

```
watch() — the event-fetch goroutine
  │
  ├─ sleep point: <-busCh (block wait, zero CPU)
  ├─ process(evt) — dispatch a single event (see process.go)
  │     each handler translates the event into a transcript input + scheduling
  └─ tryReason() — the decision gate
        └─ needReason && !isReasoning → go reason() (own goroutine)
              │
              ├─ LLM call (seconds, does not block the event loop)
              ├─ tool calls → insert placeholders → dispatch to target → return
              └─ text response → publish → return
                    └─ tryReason() — pick up overlapping events
```

Division between watch and process:

- **watch.go** — the event loop goroutine + the `tryReason` decision gate.
- **process.go** — `process` dispatch + its handlers + event→transcript-input
  translation.

## Files

| File | Responsibility |
|---|---|
| `worker.go` | `BaseReasonWorker` — the generic reasoning body (embeds `worker.BaseWorker`) + `Config` + `NewBaseReasonWorker` + lifecycle (`Start`/`Stop`/`Snapshot`/`Restore`/`Messages`) + `broadcastReady` |
| `reason.go` | the reasoning round: prepareReasoning / consumeStream / finishReasoning + lifecycle broadcasts (reason.start/end/response/thinking) + tool dispatch handleToolCalls + ErrorContextLength compaction retry |
| `watch.go` | the event loop `watch` + the `tryReason` decision gate |
| `process.go` | event dispatch `process` + handlers (abort/timeout/reminder/tool-result/input) + event→transcript-input translation |
| `tools.go` | tools: the `ToolProvider` interface + default `BuiltinTools` + tool-name encoding + worker.ready tool discovery |
| `budget.go` | context budget: token ledger (Usage snapshot), soft/hard thresholds (remind/direct compact), compaction orchestration (project→summarize→apply), incremental merge, projection (strips image/thinking, truncates) |
| `systemprompt.go` | renders the system prompt from programs |
| `tooltracker.go` | `ToolCallTracker` state machine (tracks Pending/Parked), map only |
| `transcript/` | the transcript subpackage: `Transcript` interface + sealed input variants + the tool-pairing invariant base + the default accumulate implementation |

## Core ideas

### The transcript is the workbench notes

The only cross-round state of a reason node is the transcript — workers (single
reasoning rounds) take turns at the workbench, each reading the notes the
previous one left, reasoning, and writing back. The transcript is not the
worker's core state; it is a projection of facts plus working memory,
held in `transcript/`. Render is an identity projection of the transcript;
the `[system]` prompt comes from the worker's programs (`systemprompt.go`),
not the transcript.

### Event dispatch

All events go through `process()`. The three input levels (`input_mode`) are a
spectrum of increasing intrusion, not a "default vs non-default":

| level | behavior | sets needReason? |
|---|---|---|
| append (level 1) | leave a message; only trigger a round when idle | when idle |
| schedule (level 2) | record cause + schedule next round, don't interrupt in-flight | yes |
| interrupt (level 3) | interrupt in-flight + schedule + park (hiw usually takes this) | yes |

Other events are dispatched by `process`:

| event | effect | sets needReason? |
|---|---|---|
| worker.discover | reply worker.ready | no |
| worker.abort | park + recall + record | no |
| timer.timeout | park leftover tools (cause=timeout) + schedule | yes |
| timer.reminder | treat as input (schedule level) | yes |
| worker.ready/gone | learn/forget the worker's tools | no |
| tool.completed/failed/rejected | resolve or park-late + update transcript | when resolved |
| tool.requested | route built-in tools (via ToolProvider) | no |
| worker.input | handle per the three input_mode levels | depends on level |

### Tools and ToolProvider

A built-in tool is one this worker both calls and answers itself — a tool call
routed back to the same worker. Tools come from a `ToolProvider`:
`ToolDefinitions()` (the schemas the LLM sees) and `HandleToolCall(tc)` (how a
call is served). The default `BuiltinTools` provides four: `send_message`,
`list_workers`, `context.compress`, `context.rotate`. An embedding worker can
supply its own provider via `Config.BuiltinTools` (embed `BuiltinTools` to keep
the defaults and add its own). Tools backed by other workers are discovered
separately from `worker.ready`, and are not built-ins.

Tool names are encoded unambiguously: built-ins keep a bare name (inner `.`
→ `_`); external-worker tools become `provider__name`. `__` is the separator;
worker IDs and tool names must not contain `__`.

### Tool-call lifecycle (placeholders)

The LLM API forbids inserting a non-tool_result message between
`assistant(tool_calls)` and its `tool_result`. When reason dispatches tool
calls it immediately inserts `tool_result(pending)` placeholders; a result
replaces it in place, parking replaces it in place with an explanation, and a
late result is appended as a user message (a second tool_result for the same
call_id would violate the pairing invariant). This pairing invariant lives in
the `transcript/` subpackage; the worker translates its lifecycle into sealed
BuilderInput variants handed to the transcript.

`ToolCallTracker` (tooltracker.go) records only calls still being awaited:
- **Pending** — active, the reasoner is still waiting.
- **Parked** — no longer awaited, kept in case a late result arrives
  (`ParkCause` distinguishes input/timeout/abort/reminder).

### Context budget and compaction

After each streamed round the worker records usage from `Usage` (latest
Input+Output snapshot, never accumulated) and compares it against
`ContextWindow`:

- ≥ soft line (0.85) → inject a reminder; the LLM calls `context.compress`.
- ≥ hard line (0.97) → the system compacts directly (no LLM input).
- opening the stream hits `ErrorContextLength` → compact once, then retry.

Compaction = summarize (the projected transcript) → `transcript.Compact(digest,
keepTail)`. The summarizer prompt is overridable via `compact_directive`
(program/config); a built-in fallback exists, and when a digest is already
present the merge is incremental (early goals/constraints survive). Projection
comes first: strips image/thinking, truncates oversized tool results; the cut
point aligns to pairing boundaries. `context.rotate` is `Compact` with
keepTail=2 (turn the page, keeping this call's placeholder so the result stays
visible); the fresh episode starts from the digest. Compaction runs outside the
lock (snapshot → summarize → apply); keepTail is re-evaluated at apply time so
concurrent appends survive; single-flight (`isCompacting`) prevents doubles.

### Tool-call timeout

reason recognizes the timeout tool by a bare-name contract: if any worker
provides a tool whose bare name is `set_tool_timeout`, reason treats it as
this round's tool-call timeout (provider-agnostic — the timer worker need not
be named "timer"). If no such tool exists the feature is disabled. It records
`activeTimeoutProvider` at set time and cancels that provider on completion.

### Abort recall

On abort, reason sends directed `tool.cancel` events to each target worker —
a best-effort recall. Parked calls stay in the tracker; a late result is
surfaced to the LLM as a late-result message carrying its cause.

## Design principles

1. **process does not call the LLM.** Event handling is microsecond-level: it
   only routes and updates state; reasoning is started from tryReason.
2. **Event side and reasoning side are decoupled.** Events only change state
   and set needReason; the reasoning side consumes needReason and never
   reverse-infers a cause.
3. **reason is async.** reason() runs on its own goroutine so the watch loop
   stays responsive; `cancelReason()` can genuinely interrupt in-flight
   reasoning.
4. **reason does not recurse.** One result per reasoning; chaining happens
   through tryReason re-entry (isReasoning guarantees at most one round).
5. **Concurrency is lock + snapshot.** All shared fields are read/written under
   `w.mu`; transcripts are passive, the caller holds the lock.
6. **No internal queue.** process consumes at microsecond level; the bus
   channel drains in real time.
7. **Focus on one worker's attention.** Transcript = workbench notes; tools =
   own call-and-answer built-ins plus discovered external workers; programs are
   the educable source of behavior.