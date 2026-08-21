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