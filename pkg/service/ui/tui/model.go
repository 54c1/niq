// Package ui provides human interface implementations for the Human Interface Worker.
// model.go: types, styles, constructors.
package tui

 import (
 	"log"
 	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
 	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/store"
)

// ResponseMsg wraps a display event.
type ResponseMsg struct{ Event event.Event }

// WorkerListMsg carries the full sorted list of visible worker IDs.
type WorkerListMsg struct{ Workers []string }

type suggestPhase int

const (
	suggestNone   suggestPhase = iota
	suggestCmd
	suggestWorker
)

// Model is the bubbletea Model for the Human Interface Worker TUI.
type Model struct {
 	textInput textarea.Model
	suggest   list.Model
	lines     []string
	width     int
	height    int

	showSuggest  bool
	suggestPhase suggestPhase
	workers      []string

	lastTarget string
	lastSentAt time.Time // unix nano, for debounce
	lastSent   string
	bus        corebus.EventBusClient

	store        store.EventStore
	viewWorker   string
	showEvents   bool
	lastOutputBy string
	seenEvents   map[string]bool
	toolChainStart int
	hideToolChain  bool
 	viewPort       viewport.Model
}

// ── Slash commands ──

type slashCommand struct {
	name string
	desc string
}

func (c slashCommand) FilterValue() string { return c.name }
func (c slashCommand) Title() string       { return c.name }
func (c slashCommand) Description() string { return c.desc }

var slashCommands = []slashCommand{
	{name: "target", desc: "send message to a specific worker"},
	{name: "all", desc: "switch to global view"},
	{name: "quiet", desc: "toggle hiding tool call chains"},
}

type workerItem string

func (w workerItem) FilterValue() string { return string(w) }
func (w workerItem) Title() string       { return string(w) }
func (w workerItem) Description() string { return "" }

// ── Styles ──

var (
	promptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	workerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	delimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
 	logoStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#3C78B4")).Bold(true)
	toolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	faintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	thinkStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#787878"))
	replyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
)

// NewModel creates a new TUI model.
func NewModel(bus corebus.EventBusClient, store store.EventStore) *Model {
	ti := textarea.New()
 	ti.Prompt = ""
 	ti.Placeholder = "Type a message (/ for commands)..."
 	ti.SetWidth(80)
 	ti.SetHeight(3)
 	ti.ShowLineNumbers = false
 	styles := ti.Styles()
 	styles.Focused.Base = lipgloss.NewStyle()
 	styles.Focused.EndOfBuffer = lipgloss.NewStyle()
 	styles.Blurred.Base = lipgloss.NewStyle()
 	styles.Blurred.EndOfBuffer = lipgloss.NewStyle()
 	ti.SetStyles(styles)
 	ti.Focus()
 	ti.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter"))

 	dl := list.NewDefaultDelegate()
	dl.ShowDescription = false
	dl.SetSpacing(0)
	sl := list.New([]list.Item{}, dl, 80, 6)
	sl.SetShowTitle(false)
	sl.SetShowStatusBar(false)
	sl.SetShowHelp(false)
	sl.SetShowPagination(false)
	sl.SetFilteringEnabled(false)
	sl.DisableQuitKeybindings()
	sl.KeyMap.CursorUp = key.NewBinding(key.WithKeys("up", "k", "ctrl+p"))
	sl.KeyMap.CursorDown = key.NewBinding(key.WithKeys("down", "j", "ctrl+n"))

 	m := &Model{
 		textInput:  ti,
 		suggest:    sl,
 		lastTarget: "niq",
		showEvents: true,
		bus:        bus,
		store:      store,
	seenEvents: make(map[string]bool),
	width:      80,
	height:     24,
 	viewPort:   viewport.New(viewport.WithWidth(80), viewport.WithHeight(17)),
	}
 
 	// Add ctrl+y (up) and ctrl+e (down) to viewport scroll keys.
 	m.viewPort.KeyMap.Up = key.NewBinding(key.WithKeys("up", "ctrl+y"))
 	m.viewPort.KeyMap.Down = key.NewBinding(key.WithKeys("down", "ctrl+e"))
 	m.viewPort.SoftWrap = true
 	m.viewPort.MouseWheelEnabled = true
	return m
}
 
 func (m *Model) updateViewportHeight() {
 	// Fixed overhead: logo(1) + \n(1) + \n(1) + textarea(3) = 6
 	overhead := 5
	if m.lastTarget != "" {
 		overhead += 2
	}
	if m.showSuggest {
 		overhead += 1 + len(m.suggest.Items())
	}
 	m.viewPort.SetHeight(max(5, m.height - overhead))
 	log.Printf("[ui] viewport: V=%d, h=%d, overhead=%d, target=%q, suggest=%v",
 		max(5, m.height-overhead), m.height, overhead, m.lastTarget, m.showSuggest)
 }
 
func (m Model) renderLogo() string {
 	text := " niq "
 	left := (m.width - 5) / 2
 	right := m.width - 5 - left
 	return logoStyle.Render(strings.Repeat("─", left) + text + strings.Repeat("─", right))
 }

// Init sends the initial textinput blink command.
func (m Model) Init() tea.Cmd {
 	return func() tea.Msg { return tea.ClearScreen() }
}
 
func (m *Model) rebuildViewport() {
 	var b strings.Builder
 	for _, l := range m.wrapLines(m.lines) {
 		b.WriteString(l)
 		b.WriteString("\n")
 	}
 	m.viewPort.SetContent(b.String())
 	m.viewPort.GotoBottom()
}
