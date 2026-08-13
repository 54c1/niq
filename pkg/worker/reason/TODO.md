# TODO

- [ ] Design a mechanism for reason worker to declare special events in `publishReady()`'s `publishes` field.
  - Current: `reason.*` events are internal system conventions, not declared in `worker.ready`.
  - Goal: If a reason worker subclass or extension needs to expose special events for other workers to consume, declare them via `publishes`.

- [ ] Consider whether `timer.reminder` deserves its own `handleReminder` path, or if it should be unified with the generic input handling.
  - Current: `handleReminder` converts the event to messages, then calls `setImmediateReasoning(InterruptCauseReminder)`. The only difference from `handleInput`'s default path is: (1) it doesn't cancel in-flight reasoning, (2) it uses `InterruptCauseReminder` instead of `InterruptCauseInput` for the park cause.
  - Observation: The `InterruptCauseReminder` distinction is only used in `parkReason`/`appendLateResult` message text — the LLM would naturally understand the reminder context from the event text itself.
  - Proposal: Treat `timer.reminder` (and future reminder-like events) as regular input events. The LLM sees the reminder text as a message and understands it naturally. No need for a dedicated handler or interrupt cause — the `input_mode` field already provides the "append vs interrupt" semantics.
  - Action: Possibly remove `handleReminder` and let `timer.reminder` fall through to `handleInput`, or handle it as `input_mode: "append"`. Remove `InterruptCauseReminder` if no longer needed.