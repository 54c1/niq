package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"

	"github.com/54c1/niq/core/event"
)

func (m *Model) appendResponse(evt event.Event) {
	defer m.rebuildViewport()

	if !m.showEvents {
		return
	}
	if evt.ID != "" {
		if m.seenEvents[evt.ID] {
			return
		}
		m.seenEvents[evt.ID] = true
	}
	if m.viewWorker != "" && evt.WorkerId != m.viewWorker && evt.TargetWorkerID != m.viewWorker {
		return
	}

	// Render worker.message as a prompt-styled user message.
	if evt.Type == "worker.input" {
		text, _ := evt.Payload["text"].(string)
		target := evt.TargetWorkerID
		if target != "" {
			m.lines = append(m.lines, promptStyle.Render("> to "+target+": ")+text)
		} else {
			m.lines = append(m.lines, promptStyle.Render("> ")+text)
		}
		m.toolChainStart = len(m.lines) // keep input line, hide subsequent tool events
		return
	}

	// Suppress noisy lifecycle events.
	if evt.Type == "reason.start" || evt.Type == "reason.end" {
		return
	}

	// Tool call events: compact style.
	if isToolEvent(evt.Type) {
		m.lines = append(m.lines, m.formatToolEvent(evt))
		return
	}

	// Render reason.thinking as dimmed text (no markdown rendering).
	if evt.Type == "reason.thinking" {
		m.lines = append(m.lines, delimStyle.Render("------thinking"))
		texts, ok := evt.Payload["content"].([]any)
		if ok {
			for _, t := range texts {
				if s, ok := t.(string); ok {
					for _, line := range strings.Split(s, "\n") {
						m.lines = append(m.lines, thinkStyle.Render(line))
					}
				}
			}
		}
		return
	}

	texts, ok := evt.Payload["content"].([]any)
	if !ok {
		m.lines = append(m.lines, m.formatEventLine(evt))
		return
	}

	// Join all text blocks and render as Markdown with glamour.
	var raw strings.Builder
	for _, t := range texts {
		if s, ok := t.(string); ok {
			raw.WriteString(s)
			raw.WriteString("\n")
		}
	}
	content := raw.String()
	if content == "" {
		return
	}

	// Collapse tool chain into final response if quiet mode is on.
	if m.hideToolChain && m.toolChainStart > 0 {
		m.lines = m.lines[:m.toolChainStart]
		m.toolChainStart = 0
	}

	rendered := m.renderMarkdown(content)
	delim := delimStyle.Render("------")
	m.lines = append(m.lines, delim)
	for _, line := range strings.Split(strings.TrimSpace(rendered), "\n") {
		m.lines = append(m.lines, line)
	}
	m.lines = append(m.lines, delim)
}

func (m *Model) renderMarkdown(s string) string {
	w := m.width
	if w < 40 {
		w = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(w-4),
	)
	if err != nil {
		return s
	}
	out, err := r.Render(s)
	if err != nil {
		return s
	}
	return out
}

func (m *Model) appendResponseLocked(evt event.Event) {
	saved := m.showEvents
	m.showEvents = true
	m.appendResponse(evt)
	m.showEvents = saved
}

func isToolEvent(typ string) bool {
	return typ == "tool.requested" || typ == "tool.completed" || typ == "tool.failed" || typ == "tool.rejected"
}

func (m *Model) formatToolEvent(evt event.Event) string {
	name, _ := evt.Payload["name"].(string)
	from := evt.WorkerId
	to := evt.TargetWorkerID
	if to == "" {
		to = "*"
	}

	// Pad label plain text for alignment (ANSI codes in style don't count).
	padded := evt.Type
	for len(padded) < 20 {
		padded += " "
	}
	label := toolStyle.Render(padded)
	flow := fmt.Sprintf("%s ▸ %s · %s", from, to, name)

	switch evt.Type {
	case "tool.requested":
		m.lastOutputBy = from
		return fmt.Sprintf("%-20s %s", label, flow)
	case "tool.completed":
		result, _ := evt.Payload["result"].(string)
		m.lastOutputBy = from
		return fmt.Sprintf("%-20s %s  %s", label, flow, truncate(result, 100))
	case "tool.failed":
		err, _ := evt.Payload["error"].(string)
		m.lastOutputBy = from
		return fmt.Sprintf("%-20s %s  %s", label, flow, err)
	case "tool.rejected":
		reason, _ := evt.Payload["reason"].(string)
		m.lastOutputBy = from
		return fmt.Sprintf("%-20s %s  rejected: %s", label, flow, reason)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (m *Model) formatEventLine(evt event.Event) string {
	var parts []string
	skip := map[string]bool{"call_id": true, "worker_id": true}
	for k, v := range evt.Payload {
		if skip[k] {
			continue
		}
		val := fmt.Sprintf("%v", v)
		if len(val) > 80 {
			val = val[:80] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, val))
	}
	target := evt.TargetWorkerID
	if target == "" {
		target = "*"
	}
	out := fmt.Sprintf("%s %s → %s", delimStyle.Render(evt.Type), evt.WorkerId, target)
	if len(parts) > 0 {
		out += "  " + faintStyle.Render(strings.Join(parts, ", "))
	}
	return out
}
