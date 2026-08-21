import type { Event, WorkerMessage } from "./types.js";
import type { WorkerSideChannel } from "./channel.js";
import { parseEventStream } from "./sse.js";

/** Minimal wire request body sent to `POST /publish`. */
interface PublishWire {
  worker_id: string;
  credential: string;
  type: "send" | "broadcast";
  events: Event[];
  targets?: string[];
  trace_id?: string;
}

export interface HTTPWorkerClientOptions {
  /** Base URL of the niq bus, e.g. `http://localhost:8080`. Trailing slash optional. */
  baseURL: string;
  /** Worker identity registered on the bus (control plane created it). */
  workerID: string;
  /** Credential for this worker, verified at connect/publish time. */
  credential: string;
  /** Injectable fetch (e.g. for tests / proxies). Defaults to global fetch. */
  fetchImpl?: typeof fetch;
}

/**
 * A WorkerSideChannel over the niq HTTP transport.
 *
 * The protocol — mirroring the Go `httptrans` package:
 * - Connect: open `GET /events?worker_id=&credential=` as an SSE stream.
 * - Send/Broadcast: `POST /publish` with a {@link WorkerMessage} body.
 *
 * Every request carries its own `worker_id` + `credential`; there is no session
 * token. The SSE stream acts as the connection anchor: the bus requires it to
 * be open before it accepts publishes, and only one live connection per worker
 * is kept. Closing the SSE stream marks the worker offline (identity is
 * retained and can reconnect).
 *
 * Identity registration is NOT part of this SDK — it is a control-plane
 * operation (registryapi) performed before the worker connects.
 */
export class HTTPWorkerClient implements WorkerSideChannel {
  readonly id: string;

  private readonly baseURL: string;
  private readonly workerID: string;
  private readonly credential: string;
  private readonly fetchImpl: typeof fetch;

  private es?: ReadableStream<Uint8Array>;
  private controller?: AbortController;
  private connected = false;
  private closed = false;

  constructor(opts: HTTPWorkerClientOptions) {
    this.id = opts.workerID;
    this.baseURL = opts.baseURL.replace(/\/+$/, "");
    this.workerID = opts.workerID;
    this.credential = opts.credential;
    this.fetchImpl = opts.fetchImpl ?? fetch;
  }

  async connect(): Promise<void> {
    if (this.connected) return;
    if (this.closed) throw new Error("niq: client is closed");

    const url = `${this.baseURL}/events?worker_id=${encodeURIComponent(
      this.workerID,
    )}&credential=${encodeURIComponent(this.credential)}`;

    const controller = new AbortController();
    let resp: Response;
    try {
      resp = await this.fetchImpl(url, { signal: controller.signal });
    } catch (err) {
      throw new Error(`niq: SSE connect failed: ${(err as Error).message}`);
    }
    if (!resp.ok || !resp.body) {
      throw new Error(`niq: SSE connect failed with status ${resp.status}`);
    }
    this.es = resp.body;
    this.controller = controller;
    this.connected = true;
  }

  async send(event: Event, ...targets: string[]): Promise<void> {
    this.assertOpen();
    if (targets.length === 0) {
      throw new Error("niq: send requires at least one target; use broadcast() for undirected delivery");
    }
    await this.publish("send", event, targets);
  }

  async broadcast(event: Event): Promise<void> {
    this.assertOpen();
    await this.publish("broadcast", event, undefined);
  }

  events(): AsyncIterable<Event> {
    if (!this.connected) throw new Error("niq: not connected");
    const stream = this.es;
    if (!stream) throw new Error("niq: no event stream");
    return parseEventStream(stream);
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.connected = false;
    this.controller?.abort();
  }

  private assertOpen(): void {
    if (!this.connected) throw new Error(`niq: worker ${this.workerID} not connected`);
  }

  private async publish(
    type: "send" | "broadcast",
    event: Event,
    targets: string[] | undefined,
  ): Promise<void> {
    // Stamp authorship. The bus overwrites it anyway (anti-spoofing), but this
    // is done at the edge so a caller cannot accidentally attribute to another.
    const stamped: Event = { ...event, worker_id: this.workerID };

    const body: PublishWire = {
      worker_id: this.workerID,
      credential: this.credential,
      type,
      events: [stamped],
      ...(targets ? { targets } : {}),
    };

    let resp: Response;
    try {
      resp = await this.fetchImpl(`${this.baseURL}/publish`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
    } catch (err) {
      throw new Error(`niq: publish failed: ${(err as Error).message}`);
    }
    if (!resp.ok) {
      const detail = await resp.text().catch(() => "");
      throw new Error(`niq: publish failed (${resp.status}): ${detail}`);
    }
  }
}