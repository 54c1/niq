package tui

 import (
 	"log"
 	tea "charm.land/bubbletea/v2"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.textInput.SetWidth(msg.Width - 2)
		m.viewPort.SetWidth(msg.Width)
 		m.updateViewportHeight()

	case tea.KeyPressMsg:
		if m.showSuggest {
			switch msg.String() {
			case "esc":
				m.hideSuggestions()
				return m, nil
			case "up", "down", "ctrl+p", "ctrl+n":
				m.suggest, cmd = m.suggest.Update(msg)
				return m, cmd
			case "enter":
				if m.handleSuggestionSelect() {
					return m, nil
				}
				m.finishInput()
				return m, nil
			}
		}

		switch msg.String() {
		case "enter":
			m.finishInput()
			return m, nil
		case "ctrl+c":
			return m, tea.Quit
		}

 	if m.textInput.Value() == "" && msg.Text == "/" {
 		log.Printf("[ui] slash command detected: value=%q, text=%q", m.textInput.Value(), msg.Text)
 		m.textInput.SetValue("/")
			m.rebuildSuggestions()
			return m, nil
		}

	case ResponseMsg:
		m.appendResponse(msg.Event)
		return m, nil

	case WorkerListMsg:
		m.workers = msg.Workers
		m.rebuildSuggestions()
		return m, nil
	}

 	// Let the viewport handle scroll keys (pgup/pgdown, up/down, etc.).
 	var vpCmd tea.Cmd
 	m.viewPort, vpCmd = m.viewPort.Update(msg)
 
	prevVal := m.textInput.Value()
	m.textInput, cmd = m.textInput.Update(msg)
	if m.textInput.Value() != prevVal {
		m.rebuildSuggestions()
	}

 	return m, tea.Batch(cmd, vpCmd)
}
