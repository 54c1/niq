package tui

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/54c1/niq/core/event"
)

func (m *Model) finishInput() {
	line := strings.TrimSpace(m.textInput.Value())
	if line == "" {
		return
	}
	if !m.handleSlashCommand(line) {
		m.publishMessage(line)
	}
	m.textInput.Reset()
	m.hideSuggestions()
}

func (m *Model) handleSlashCommand(line string) bool {
	if !strings.HasPrefix(line, "/") {
		return false
	}
	parts := strings.SplitN(line[1:], " ", 2)
	cmd := parts[0]
	var args string
	if len(parts) > 1 {
		args = parts[1]
	}

	switch cmd {
	case "all":
		m.switchView("*")
		return true

	case "quiet":
		m.hideToolChain = !m.hideToolChain
		m.lines = append(m.lines, delimStyle.Render(fmt.Sprintf("─── tool chain %s ───", map[bool]string{true: "hidden", false: "shown"}[m.hideToolChain])))
		return true

	case "target":
		if args == "" {
			return false
		}
		workerParts := strings.SplitN(args, " ", 2)
		target := workerParts[0]
		if !m.isWorker(target) {
			return false
		}
		if len(workerParts) == 1 {
			m.switchView(target)
			return true
		}
		m.showEvents = true
		m.lastTarget = target
		m.publishTargeted(target, workerParts[1])
		return true

	default:
		if !m.isWorker(cmd) {
			return false
		}
		m.showEvents = true
		if args == "" {
			m.switchView(cmd)
			return true
		}
		m.lastTarget = cmd
		m.publishTargeted(cmd, args)
		return true
	}
}

func (m *Model) isWorker(id string) bool {
	for _, w := range m.workers {
		if w == id {
			return true
		}
	}
	return false
}

func (m *Model) publishMessage(line string) {
	if line == m.lastSent && time.Since(m.lastSentAt) < 2000*time.Millisecond {
		return
	}
	m.lastSent = line
	m.lastSentAt = time.Now()
	log.Printf("[hiw] publishInput: %q", line)

	if m.lastTarget != "" {
		m.publishTargeted(m.lastTarget, line)
	} else {
		evt := event.New("worker.input", "hiw", map[string]any{"text": line})
		_ = m.bus.Publish(evt)
	}
}

func (m *Model) publishTargeted(target, text string) {
	log.Printf("[hiw] publishInput: /target %s %q", target, text)
	evt := event.New("worker.input", "hiw", map[string]any{
		"text": text,
	})
	evt.TargetWorkerID = target
	_ = m.bus.Publish(evt)
}
