package tui

import (
	"context"

	"github.com/54c1/niq/core/store"
)

func (m *Model) switchView(workerID string) {
	if workerID == "*" {
		m.viewWorker = ""
		m.lastTarget = ""
	} else {
		m.viewWorker = workerID
		m.lastTarget = workerID
	}
	m.lines = nil
	m.lastOutputBy = workerID
	m.seenEvents = make(map[string]bool)

	label := workerID
	if workerID == "*" {
		label = "all"
	}
	m.lines = append(m.lines, delimStyle.Render("─── viewing: "+label+" ───"))

	// Replay events from store before enabling real-time display.
	if m.store != nil {
		events, err := m.store.List(context.Background(), workerID, store.QueryOpts{Limit: 50, Desc: false})
		if err == nil {
			for i := 0; i < len(events); i++ {
				m.appendResponseLocked(events[i])
			}
		}
	}

	m.showEvents = true
 	m.updateViewportHeight()
}
