// AccumulateTranscript: the default transcript implementation. A flat
// transcript of llm.Messages; digest messages may appear among them after
// Compact. Passive and lock-free by contract: the caller serializes access.
package transcript

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

// AccumulateTranscript owns the working transcript. It is passive: the
// caller serializes all access.
type AccumulateTranscript struct {
	messages []llm.Message
}

// NewAccumulateTranscript creates an empty transcript.
func NewAccumulateTranscript() *AccumulateTranscript {
	return &AccumulateTranscript{}
}

// Apply folds one lifecycle fact into the transcript.
func (b *AccumulateTranscript) Apply(input BuilderInput) {
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
func (b *AccumulateTranscript) Render() []llm.Message {
	return b.messages
}

// Compact applies a pre-computed digest: the transcript becomes
// [digest] + last keepTail messages (evaluated at apply time, so concurrent
// appends during summarization are preserved). No-op when there is nothing
// beyond the tail to compact. The cut point is alignment-corrected: it never
// falls between an assistant(tool_calls) message and its tool_result
// messages (the pairing invariant would be violated on the tail side).
func (b *AccumulateTranscript) Compact(digest string, keepTail int) {
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
}

// State serializes the projection cache.
func (b *AccumulateTranscript) State() ([]byte, error) {
	return json.Marshal(accumulateState{Messages: b.messages})
}

// Restore rehydrates the transcript from a State blob.
func (b *AccumulateTranscript) Restore(state []byte) error {
	var s accumulateState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("transcript restore: %w", err)
	}
	b.messages = s.Messages
	return nil
}
