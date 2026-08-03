package llm

// Role represents a message participant.
type Role string

const (
	RoleSystem      Role = "system"
	RoleUser        Role = "user"
	RoleAssistant   Role = "assistant"
	RoleToolResult  Role = "tool" // OpenAI-compatible role for tool result messages
)

// ContentBlockType describes the kind of content in a block.
type ContentBlockType string

const (
	ContentText      ContentBlockType = "text"
	ContentThinking  ContentBlockType = "thinking"
	ContentToolCall  ContentBlockType = "tool_call"
	ContentImage     ContentBlockType = "image"
)

// ContentBlock is a single piece of content within a Message.
type ContentBlock struct {
	Type ContentBlockType

	// Text / thinking content
	Text      string
	Redacted  bool   // thinking: whether the content was redacted
	Signature string // thinking: verification signature

	// Tool call fields
	ToolCallID   string
	ToolName     string
	ToolArguments string // JSON string

	// Image fields
	Data     string // base64-encoded
	MIMEType string
}

// Message is a unified message exchanged with the LLM.
type Message struct {
	Role       Role
	Content    []ContentBlock
	Usage      *Usage
	Cost       *Cost
	StopReason string // stop / length / tool_calls / error

	// For tool_result messages
	ToolCallID string
	ToolName   string
	IsError    bool
}

// ToolChoice controls whether and which tool the LLM must call.
type ToolChoice interface {
	toolChoiceMarker()
	// Code returns a provider-agnostic choice code: "auto", "required", "none", or "function".
	Code() string
}

var (
	ToolChoiceAuto     = toolChoiceAuto{}
	ToolChoiceRequired = toolChoiceRequired{}
	ToolChoiceNone     = toolChoiceNone{}
)

type toolChoiceAuto struct{}
func (toolChoiceAuto) toolChoiceMarker()     {}
func (toolChoiceAuto) Code() string          { return "auto" }

type toolChoiceRequired struct{}
func (toolChoiceRequired) toolChoiceMarker()  {}
func (toolChoiceRequired) Code() string       { return "required" }

type toolChoiceNone struct{}
func (toolChoiceNone) toolChoiceMarker()      {}
func (toolChoiceNone) Code() string           { return "none" }

// ToolChoiceNamed forces the LLM to call a specific tool.
type ToolChoiceNamed struct{ Name string }
func (ToolChoiceNamed) toolChoiceMarker()     {}
func (t ToolChoiceNamed) Code() string        { return "function" }

// Usage records token counts for a completion.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	TotalTokens      int
	CacheCreationTokens *int
	CacheReadTokens     *int
}

// Cost records the monetary cost of a completion (USD).
type Cost struct {
	Total  float64
	Input  float64
	Output float64
	Cache  float64
}

// ModelInfo describes an LLM model known by a provider.
type ModelInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Provider      string   `json:"provider"`
	ContextWindow int      `json:"context_window"`
	MaxOutput     int      `json:"max_output"`
	InputTypes    []string `json:"input_types"`
	OutputTypes   []string `json:"output_types"`
	Reasoning     bool     `json:"reasoning"`
	ToolCalling   bool     `json:"tool_calling"`
	Pricing       Pricing  `json:"pricing"`
}

// Pricing for a model.
type Pricing struct {
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
}

// ToolDef describes a tool for the LLM API — just the schema, no worker
// routing metadata. core/worker.Tool embeds this to add Target and Inline.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
