package reason

import (
	"strings"
	"testing"

	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/pkg/reason/transcript"
)

// TestSeedMessagesAppliedAtConstruction verifies the spawner's handover brief
// becomes the transcript's first message (goal goes to Programs instead -
// tested on the swarm side).
func TestSeedMessagesAppliedAtConstruction(t *testing.T) {
	seed := []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: "[handover brief from spawner]\nroot cause found (trace=t_abc)"}},
	}}
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel(), SeedMessages: seed})

	msgs := w.transcript.Render()
	if len(msgs) != 1 {
		t.Fatalf("seed not applied, got %d messages", len(msgs))
	}
	if !strings.Contains(msgs[0].Content[0].Text, "trace=t_abc") {
		t.Fatalf("brief content lost: %q", msgs[0].Content[0].Text)
	}

	// Normal Apply continues after the seed.
	w.transcript.Apply(transcript.InputEvent{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "go"}}},
	}})
	if got := len(w.transcript.Render()); got != 2 {
		t.Fatalf("expected seed + 1, got %d", got)
	}
}

// TestSeedAbsentForFreshWorker verifies no seed leaves the transcript empty.
func TestSeedAbsentForFreshWorker(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if got := len(w.transcript.Render()); got != 0 {
		t.Fatalf("fresh worker should be empty, got %d", got)
	}
}
