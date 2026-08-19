// AccumulateBuilder: the default context builder. A flat transcript of
// llm.Messages plus a cursor; digest messages may appear among them after
// Compact. Passive and lock-free by contract: the caller serializes access.
package builder

import (
	"encoding/json"
	"fmt"

	"github.com/54c1/niq/core/llm"
)

// digestMessage wraps a compacted transcript summary as the head message of
// the new projection. User role: it must read as system-provided context to
// the model without violating any pairing invariant. The [context digest]
// prefix marks the message so update-mode summarization can detect a carried
// digest.
func digestMessage(digest string) llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: "[context digest] " + digest}},
	}
}

// AccumulateBuilder owns the working transcript. It is passive: the caller
// serializes all access (the reason worker holds its mutex).
type AccumulateBuilder struct {
	messages []llm.Message
	cursor   string // last seen event id; rebuild credential (step 4 wires it)
}

// NewAccumulate creates an empty builder.
func NewAccumulate() *AccumulateBuilder {
	return &AccumulateBuilder{}
}

// Apply folds one lifecycle fact into the transcript.
func (b *AccumulateBuilder) Apply(input BuilderInput) {
	switch in := input.(type) {
	case InputEvent:
		b.messages = append(b.messages, in.Messages...)
	case AssistantOutput:
		b.messages = append(b.messages, in.Message)
	case PartialOutput:
		b.messages = append(b.messages, in.Message)
	case ToolPlaceholders:
		for _, call := range in.Calls {
			b.messages = append(b.messages, placeholderMessage(call))
		}
	case ToolResult:
		b.messages = replacePlaceholder(b.messages, in.CallID,
			toolResultMessage(in.CallID, in.Name, in.Text, in.IsErr))
	case ToolParked:
		b.messages = replacePlaceholder(b.messages, in.CallID,
			toolResultMessage(in.CallID, in.Name, parkReason(in.Cause), false))
	case LateResult:
		if in.Text != "" {
			b.messages = append(b.messages, lateResultMessage(in.CallID, in.Name, in.Text, in.Cause))
		}
	default:
		// Unknown variants are ignored: the sealed algebra grows at the
		// interface, old snapshots stay readable.
	}
}

// Render returns the transcript for the next LLM round. Identity projection:
// callers must not mutate the returned slice.
func (b *AccumulateBuilder) Render() []llm.Message {
	return b.messages
}

// Compact applies a pre-computed digest: the transcript becomes
// [digest] + last keepTail messages (evaluated at apply time, so concurrent
// appends during summarization are preserved). No-op when there is nothing
// beyond the tail to compact. The cut point is alignment-corrected: it never
// falls between an assistant(tool_calls) message and its tool_result
// messages (the pairing invariant would be violated on the tail side).
func (b *AccumulateBuilder) Compact(digest string, keepTail int) {
	n := len(b.messages)
	if n <= keepTail {
		return
	}
	cut := n - keepTail
	cut = alignCutToPairing(b.messages, cut)
	kept := append([]llm.Message{digestMessage(digest)}, b.messages[cut:]...)
	b.messages = kept
}

// alignCutToPairing moves a cut point forward past tool_result messages that
// belong to an assistant tool_calls message left of the cut: a tail starting
// with orphan tool_results would violate the pairing invariant. The tail
// shrinks (more history is compacted) - never grows.
func alignCutToPairing(msgs []llm.Message, cut int) int {
	for cut < len(msgs) && msgs[cut].Role == llm.RoleToolResult {
		cut++
	}
	return cut
}

// accumulateState is the serializable projection cache. The field set must
// only grow (older blobs stay readable).
type accumulateState struct {
	Messages []llm.Message `json:"messages"`
	Cursor   string        `json:"cursor,omitempty"`
}

// State serializes the projection cache.
func (b *AccumulateBuilder) State() ([]byte, error) {
	return json.Marshal(accumulateState{Messages: b.messages, Cursor: b.cursor})
}

// Restore rehydrates the transcript from a State blob.
func (b *AccumulateBuilder) Restore(state []byte) error {
	var s accumulateState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("builder restore: %w", err)
	}
	b.messages = s.Messages
	b.cursor = s.Cursor
	return nil
}
