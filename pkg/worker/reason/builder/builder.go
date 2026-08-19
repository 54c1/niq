// Package builder holds the reason worker's context construction core.
//
// The builder is the reason worker's attention organ: a passive state machine
// that folds lifecycle facts (BuilderInput) into a working transcript and
// renders it into LLM messages per reasoning round.
//
// Design invariants (see doc/design/reason_worker/context-builder.md):
//   - The transcript is a projection of the fact layer (the event store), not
//     the worker's identity. Operations transform the projection; facts remain.
//   - No goroutines, no locks, no I/O: the caller (reason worker) holds its
//     mutex around every call. Builders are passive data structures owning
//     invariants (tool_call/tool_result pairing), nothing more.
//   - Inputs are sealed, data-only variants: the builder knows nothing about
//     events, the bus, or the reason worker's internals. The worker translates
//     its lifecycle into these variants.
//   - Render is an identity projection of the state: [system] is the worker's
//     domain (programs), the builder renders messages verbatim. Prefix
//     stability (prompt cache) holds because messages only append, and digest
//     changes happen only via Compact.
package builder

import "github.com/54c1/niq/core/llm"

// ContextBuilder is the reason worker's context construction core.
// It is an internal organ of the reasoning node, deliberately NOT a worker:
// everything here is re-derivable from the event stream (workers are for
// non-regenerable capabilities; projections live where they are consumed).
//
// The interface is evolving: single implementation (Accumulate) for now.
type ContextBuilder interface {
	// Apply folds one lifecycle fact into the transcript. The single write
	// path; call order is transcript order. Not a query: it returns nothing.
	Apply(input BuilderInput)

	// Render returns the message list for the next LLM round. A pure,
	// identity projection of the state (see package doc). The returned slice
	// must be treated as read-only.
	Render() []llm.Message

	// Compact applies a pre-computed digest to the transcript: everything
	// before the last keepTail messages (evaluated at apply time) is replaced
	// by a digest message. The cut is alignment-corrected so the tail never
	// starts with orphan tool_results. The digest text is produced by the
	// caller (the reason worker's summarizer); the builder stays free of
	// LLM I/O.
	Compact(digest string, keepTail int)

	// State returns the serializable snapshot (a cache of the projection).
	State() ([]byte, error)

	// Restore rehydrates from a State blob.
	Restore(state []byte) error
}

// BuilderInput is a sealed lifecycle fact translated by the reason worker.
// Exactly one variant is carried per Apply call; Apply order is history.
type BuilderInput interface{ isBuilderInput() }

// InputEvent carries externally-sourced messages (user input, reminders,
// timeout notices, abort records) into the transcript.
type InputEvent struct {
	Messages []llm.Message
}

// AssistantOutput records a completed reasoning round's final message.
type AssistantOutput struct {
	Message llm.Message
}

// PartialOutput records content preserved from an interrupted round.
type PartialOutput struct {
	Message llm.Message
}

// ToolPlaceholders inserts [pending] tool_result entries for dispatched
// tool calls, preserving transcript ordering. Replaced in place later.
type ToolPlaceholders struct {
	Calls []llm.ContentBlock
}

// ToolResult replaces the placeholder for a resolved tool call.
type ToolResult struct {
	CallID string
	Name   string
	Text   string
	IsErr  bool
}

// ToolParked replaces the placeholder for a parked (no longer awaited) call.
type ToolParked struct {
	CallID string
	Name   string
	Cause  string // why the call was parked (timeout / input / abort / reminder)
}

// LateResult appends a contextualized user message for a late-arriving
// result on a parked call (a second tool_result for the same call_id would
// violate the pairing invariant).
type LateResult struct {
	CallID string
	Name   string
	Text   string
	Cause  string
}

func (InputEvent) isBuilderInput()       {}
func (AssistantOutput) isBuilderInput()  {}
func (PartialOutput) isBuilderInput()    {}
func (ToolPlaceholders) isBuilderInput() {}
func (ToolResult) isBuilderInput()       {}
func (ToolParked) isBuilderInput()       {}
func (LateResult) isBuilderInput()       {}
