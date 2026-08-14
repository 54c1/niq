# niq

> **niq runs programs that haven't been written yet.**

niq is an **event-driven, decentralized agent runtime**. It is not a single "agent" — it is a collection of **Workers** (a Worker Swarm) that collaborate over an event bus; together they form a complete agent.

niq has exactly three core concepts:

- **Worker** — a unit that can do things. It subscribes to events, processes them, and publishes new events. Every capability (reasoning, tool execution, safety guards, lifecycle) is a Worker; there is only one extension concept.
- **Program** — the source of a Worker's capability (natural-language Prompts, or formalized DSL Scripts).
- **Event** — the only communication language between Workers, delivered over the event bus.

Architectural tenets: **everything is a Worker, the control plane lives in the data plane, the concept count stays at 3, and the protocol outlives implementations.**

> **Status: early, fast-moving development.** The design is still evolving; APIs and behavior may change without notice.

## Project layout

```
core/         interfaces & types (contracts, not implementations)
pkg/          implementations (workers, services, bus, transports)
internal/     swarm assembly, WebUI
cmd/          CLI entry point
doc/          design docs & dev notes
```

## Quick start

### Prerequisites

- Go 1.22+
- A model API key (for the reason worker), set in `~/.zshenv`:

  ```sh
  export OPENAI_API_KEY=sk-xxxx
  ```

### Run

```sh
# Start with the built-in "dev" preset (WebUI listens on :19763 by default)
cd niq && go run ./cmd/niq/

# Or build first
make build
./bin/niq

# Start from a custom YAML config
./bin/niq swarm --config path/to/config.yaml

# Override the WebUI address
./bin/niq swarm --webui :19763
```

Once started, open <http://localhost:19763> for the WebUI — chat, inspect events, and view workers.

### Build & test

```sh
cd niq && go build ./... && go vet ./...
cd niq && go test ./pkg/service/eventbus/ -count=1
```

### Data directory

Runtime data lives under `~/.niq/`:

```
~/.niq/
  ├── id/identities.json    # worker identity registry (owned by the bus)
  ├── programs/             # Program storage
  └── niq.log               # run log
```