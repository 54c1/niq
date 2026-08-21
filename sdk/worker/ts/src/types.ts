/**
 * RequestType classifies a delivery request sent from a worker to the bus.
 *
 * These are the API-names in the `type` field of the HTTP publish payload —
 * never change the serialized strings without changing the Go transport.
 */
export type RequestType = "send" | "broadcast";

/** Message type sent from a worker to the bus (worker → bus direction). */
export type MessageType = RequestType | "heartbeat" | "disconnect";

/** Lifecycle stage of an event. Mirrors `core/event.EventStatus`. */
export type EventStatus = "created" | "routed" | "delivered";

/**
 * Subscription declaration: an event of a type, optionally from a specific
 * source worker. Mirrors `core/event.EventPattern`.
 *
 * `type` supports exact match, `"*"` (any), and `"Prefix.*"` (prefix wildcard —
 * also matches the bare prefix, e.g. `"github.*"` matches `"github"` and
 * `"github.issue.new"`). An empty `type` matches nothing (fails closed).
 */
export interface EventPattern {
  type: string;
  source_id?: string;
}

/**
 * The core data unit of the niq event bus. Field names are the wire names
 * (`snake_case`) and must match `core/event.Event` exactly.
 *
 * Note on authorship: the bus overwrites `worker_id` on receipt (anti-spoofing).
 * The worker MUST supply `id` and `timestamp` — the bus does not fill them.
 * Use {@link createEvent} to get these right.
 */
export interface Event {
  id: string;
  type: string;
  status: EventStatus;
  payload: Record<string, unknown>;
  worker_id: string;
  target_worker_id?: string;
  trace_id?: string;
  specversion?: string;
  dataschema?: string;
  timestamp: number;
  recipients?: string[];
}

/**
 * A delivery request from a worker to the bus. Mirrors `core/bus.Request`.
 * Serialized over HTTP as the publish body; `MessageType` is the wire `type`.
 */
export interface WorkerMessage {
  type: MessageType;
  events: Event[];
  targets?: string[];
  trace_id?: string;
}

/** Publish detail: which event types a worker may publish. */
export interface Identity {
  worker_id: string;
  credential: string;
  publish_allow?: string[];
  subscribe_allow?: EventPattern[];
}