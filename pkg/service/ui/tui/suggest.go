package tui

import (
	"strings"

	"charm.land/bubbles/v2/list"
)

func (m *Model) rebuildSuggestions() {
 	defer m.updateViewportHeight()
 	v := m.textInput.Value()
	if !strings.HasPrefix(v, "/") {
		m.hideSuggestions()
		return
	}

	text := strings.TrimPrefix(v, "/")
	spaceIdx := strings.Index(text, " ")

	if spaceIdx < 0 {
		// Phase 1: choosing a slash command.
		m.setSuggestPhase(suggestCmd)
		filter := text
		items := make([]list.Item, 0)
		for _, cmd := range slashCommands {
			if filter == "" || strings.Contains(cmd.name, filter) {
				items = append(items, cmd)
			}
		}
		if len(items) == 0 {
			m.hideSuggestions()
			return
		}
		m.showSuggest = true
		m.suggest.SetItems(items)
		m.suggest.Select(0)
		if m.width > 0 {
			m.suggest.SetWidth(m.width - 2)
		}
		return
	}

	// Phase 2: command already chosen.
	cmdName := text[:spaceIdx]
	if cmdName != "target" {
		m.hideSuggestions()
		return
	}

	m.setSuggestPhase(suggestWorker)
	after := strings.TrimSpace(text[spaceIdx:])

	if afterIdx := strings.Index(after, " "); afterIdx >= 0 {
		m.hideSuggestions()
		return
	}

	items := make([]list.Item, 0)
	for _, w := range m.workers {
		if after == "" || strings.Contains(w, after) {
			items = append(items, workerItem(w))
		}
	}
	if len(items) == 0 {
		m.hideSuggestions()
		return
	}
	m.showSuggest = true
	m.suggest.SetItems(items)
	m.suggest.Select(0)
	if m.width > 0 {
		m.suggest.SetWidth(m.width - 2)
	}
}

func (m *Model) handleSuggestionSelect() bool {
	switch m.suggestPhase {
	case suggestCmd:
		if sel, ok := m.suggest.SelectedItem().(slashCommand); ok {
			if sel.name == "all" {
				m.textInput.SetValue("/all")
				m.finishInput()
				return true
			}
			if sel.name == "quiet" {
				m.textInput.SetValue("/quiet")
				m.finishInput()
				return true
			}
			m.textInput.SetValue("/" + sel.name + " ")
			m.textInput.CursorEnd()
			m.rebuildSuggestions()
			return true
		}
	case suggestWorker:
		if sel, ok := m.suggest.SelectedItem().(workerItem); ok {
			v := m.textInput.Value()
			lastSpace := strings.LastIndex(v, " ")
			m.textInput.SetValue(v[:lastSpace+1] + string(sel) + " ")
			m.textInput.CursorEnd()
			m.hideSuggestions()
			return true
		}
	}
	return false
}

func (m *Model) setSuggestPhase(phase suggestPhase) {
	if m.suggestPhase != phase || !m.showSuggest {
		m.suggestPhase = phase
	}
}

func (m *Model) hideSuggestions() {
 	m.showSuggest = false
 	m.suggestPhase = suggestNone
 	m.suggest.SetItems(nil)
 	m.updateViewportHeight()
}
