# @niq-ai/niq-sdk (sdk/niq)

**Placeholder — no implementation yet.**

This is the reserved language layer for the **"integrate the whole niq"** SDK
family: embedding / orchestrating a full niq (swarm assembly, lifecycle,
Program management) inside a host program, distinct from [@niq-ai/niq-worker](../worker/ts)
which only connects a single Worker over the bus.

Per the `sdk/<role>/<lang>` layout, `ts/` is the TypeScript language layer.
It is scaffolded but intentionally has no content yet — the scope is not yet
defined (this is the "机动" / integration muscle that comes later).

Compare: `sdk/worker/ts` → "write *a* Worker". `sdk/niq/ts` → "run/embed *the
niq*" (future).