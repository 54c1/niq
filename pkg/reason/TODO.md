# TODO

- [ ] Design a mechanism for reason worker to declare special events in the
  `worker.ready` "publishes" field.
  - Current: `reason.*` events are internal system conventions, not declared
    in `worker.ready`.
  - Goal: If a reason-family worker needs to expose special events for other
    workers to consume, declare them via `publishes`.

- [ ] Revisit whether `timer.reminder` deserves its own `handleReminder` path,
  or should go through `handleInput`'s `schedule` level.
  - Current: `handleReminder` converts the event and calls `scheduleInput`
    directly (level 2 — does not interrupt, parks on next round). `handleInput`
    now also offers a `"schedule"` input_mode that does the same thing.
  - The two paths now behave identically; the only reason to keep
    `handleReminder` separate is clarity of intent. Consider folding it into
    `handleInput` (e.g. `timer.reminder` arrives with `input_mode: "schedule"`).

- [ ] Persist runtime program updates in Snapshot/Restore (meta-capability).
  - Two semantics for "reference a program":
    1. transient -- the program is loaded via a query/injection tool into the
       current transcript (consulted this round). We support this today.
    2. permanent -- the worker decides it should always follow this program, so
       it should be promoted into the system prompt and survive restarts.
  - Today reason's `Snapshot()` only persists the transcript; `programs` come
    from Config (spawn params), restored by host's `WorkerStore` from the
    original WorkerConfig. A runtime-added program would be lost on restart.
  - Assignment: generalize `Snapshot` to include a runtime program overlay
    (programs added/mutated while running), and merge it with the
    Config-derived programs on `Restore`. Static programs stay in Config
    (host-persisted); only the overlay is carried by reason's snapshot. This is
    the "durable meta-edit" counterpart to the meta-operation work (worker.update).
  - See doc/blogs/meta-capability.md for the conceptual framing.

- [ ] Derive meta-capability tools from discovered worker.update support, not
  reason-internal IsMetaTool builtins.
  - Once a worker self-updates via worker.update, the update capability is a
    bus contract it declares ("I handle worker.update"). So reason should not
    hard-code its meta tools (context.compress / context.rotate) as builtins
    with an IsMetaTool flag; instead it should discover which workers declare
    worker.update support and derive the LLM-visible meta tools from that.
  - Open tension: a worker.update's payload internally groups a *set* of
    operations (op: compress/rotate/set_provider/...)--one event, many meta
    tools underneath. Bridging "one event" with "many LLM-visible tools" means
    a worker.update declaration should enumerate its supported ops, and a layer
    expands each op into a tool definition and routes a call back into a
    worker.update{op}.
  - Direction to explore: worker declares worker.update + its ops; reason (or a
    generic layer) materializes those ops as tools for the LLM; a call becomes a
    worker.update event. This removes reason's IsMetaTool hardcoding and makes
    meta-capability protocol-driven and extensible to any worker that offers it.
  - Preferred alternative: avoid payload parsing/expansion by encoding the ops in
    the event type itself: `worker.update.compress`, `worker.update.rotate`, ...
    Each op is its own (dotted, sub-type) event; a worker declares the
    `worker.update.*` subset it supports, reason subscribes those and turns them
    into LLM-visible tools, and a call is just broadcasting that event type. No
    `op` field to parse, no manual expansion -- the event name already carries
    the intent and the audit trail. This matches niq's existing dotted event
    naming (timer.timeout / timer.reminder) and drops the IsMetaTool builtins
    entirely.