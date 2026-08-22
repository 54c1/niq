// AccumulateTranscript: the default transcript implementation. A flat
// transcript of llm.Messages; digest messages may appear among them after
// Compact. Passive and lock-free by contract: the caller serializes access.
package transcript

import (
	"encoding/json"
	"fmt"
	"sync"

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

// AccumulateTranscript owns the working transcript. It is concurrency-safe on
// its own: every method locks internally, and meta edits (BeginEdit..CommitEdit)
// run their computation without holding the lock while buffering concurrent
// Apply calls.
type AccumulateTranscript struct {
	mu sync.Mutex

	messages     []llm.Message
	editing      bool          // a meta edit is in progress
	pendingInput []llm.Message // Apply inputs buffered during the edit
}

// NewAccumulateTranscript creates an empty transcript.
func NewAccumulateTranscript() *AccumulateTranscript {
	return &AccumulateTranscript{}
}

// Apply folds one lifecycle fact into the transcript. If an edit is in
// progress, the input is buffered (merged on CommitEdit), so it is neither
// lost nor torn by the edit's overwrite.
func (b *AccumulateTranscript) Apply(input BuilderInput) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.applyLocked(input)
}

func (b *AccumulateTranscript) applyLocked(input BuilderInput) {
	if b.editing {
		switch in := input.(type) {
		case InputEvent:
			b.pendingInput = append(b.pendingInput, in.Messages...)
		default:
			// Non-InputEvent mutations (tool results, placeholders, outputs)
			// are part of the editing turn; keep them in the main transcript
			// buffer to be merged on commit via the tail. We still capture
			// them so commit keeps them consistent: append to a generic tail.
			// InputEvent is the only external-input variant; the others are
			// worker lifecycle that we conservatively buffer as raw messages.
			b.pendingInput = append(b.pendingInput, extractMessages(input)...)
		}
		return
	}
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

// extractMessages flattens any BuilderInput into its composer llm.Messages, for
// buffering during an edit.
func extractMessages(input BuilderInput) []llm.Message {
	switch in := input.(type) {
	case InputEvent:
		return in.Messages
	case AssistantOutput:
		return []llm.Message{in.Message}
	case PartialOutput:
		return []llm.Message{in.Message}
	case ToolPlaceholders:
		var out []llm.Message
		for _, call := range in.Calls {
			out = append(out, placeholderMessage(call))
		}
		return out
	case ToolResult:
		return []llm.Message{toolResultMessage(in.CallID, in.Name, in.Text, in.IsErr)}
	case ToolParked:
		return []llm.Message{toolResultMessage(in.CallID, in.Name, parkReason(in.Cause), false)}
	case LateResult:
		if in.Text != "" {
			return []llm.Message{lateResultMessage(in.CallID, in.Name, in.Text, in.Cause)}
		}
	}
	return nil
}

// Render returns the transcript for the next LLM round. The returned slice
// must not be mutated.
func (b *AccumulateTranscript) Render() []llm.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.messages
}

// BeginEdit starts a meta edit: marks the transcript as editing and returns a
// snapshot. The lock is released before returning, so the caller can compute
// off-transcript (e.g. an LLM summary); Apply calls during the edit are
// buffered.
func (b *AccumulateTranscript) BeginEdit() []llm.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.editing = true
	return b.messages
}

// CommitEdit applies a meta edit: replaces all but the last keepTail messages
// with a digest (alignment-corrected), then merges the Apply inputs buffered
// during the edit. No-op if no edit is in progress.
func (b *AccumulateTranscript) CommitEdit(digest string, keepTail int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.editing {
		return
	}
	b.editing = false

	n := len(b.messages)
	if n > keepTail {
		cut := alignCutToPairing(b.messages, n-keepTail)
		b.messages = append([]llm.Message{digestMessage(digest)}, b.messages[cut:]...)
	}
	if len(b.pendingInput) > 0 {
		b.messages = append(b.messages, b.pendingInput...)
		b.pendingInput = nil
	}
}

// AbortEdit cancels a meta edit without applying it: clears the editing state
// and leaves buffered inputs unmerged (the main transcript stays as it was;
// buffered inputs are preserved here so a later commit does not lose them, and
// they are appended by the next successful commit). No-op if no edit is in
// progress.
func (b *AccumulateTranscript) AbortEdit() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.editing {
		return
	}
	b.editing = false
	// Keep pendingInput as-is; a later commit will merge it. This preserves
	// inputs received during an aborted edit rather than dropping them.
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
