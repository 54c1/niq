package tui

import (
 	"fmt"
 	"strings"
 
 	tea "charm.land/bubbletea/v2"
 )

func (m Model) View() tea.View {
	var b strings.Builder

	// Logo.
 	b.WriteString(m.renderLogo())
 	b.WriteString("\n")

 	// Content: viewport (internal scrolling, independent of terminal scrollback).
 	b.WriteString(m.viewPort.View())
 	b.WriteString("\n")
 
 	// Target info row (shown when a specific worker is targeted).
	if m.lastTarget != "" {
		b.WriteString(delimStyle.Render(fmt.Sprintf("» %s", m.lastTarget)))
		b.WriteString("\n")
 		b.WriteString(delimStyle.Render(strings.Repeat("─", m.width)))
 		b.WriteString("\n")
	}

	b.WriteString(m.textInput.View())
 
 	if m.showSuggest && len(m.suggest.Items()) > 0 {
		m.suggest.SetWidth(m.width - 2)
		b.WriteString("\n")
		b.WriteString(m.suggest.View())
	}

 	v := tea.NewView(b.String())
 	v.AltScreen = true
 	return v
}

func (m *Model) wrapLines(lines []string) []string {
	if m.width <= 0 {
		return lines
	}
	var out []string
	for _, l := range lines {
		if strings.Contains(l, "\x1b[") {
			out = append(out, l)
			continue
		}
		for len(l) > m.width {
			out = append(out, l[:m.width])
			l = l[m.width:]
		}
		out = append(out, l)
	}
	return out
}
