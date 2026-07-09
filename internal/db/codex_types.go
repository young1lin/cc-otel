package db

import "time"

// CodexAPIRequest mirrors APIRequest but is sourced from Codex CLI telemetry.
// Token data arrives via codex.sse_event(kind=response.completed); network
// data arrives via codex.api_request. The two are correlated post-hoc by the
// repository's UpdateCodexAPIRequestTokens.
type CodexAPIRequest struct {
	ID                  int64     `json:"id"`
	Timestamp           time.Time `json:"timestamp"`
	SessionID           string    `json:"session_id"`
	UserID              string    `json:"user_id"`
	ConversationID      string    `json:"conversation_id"`
	Model               string    `json:"model"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	ReasoningTokens     int64     `json:"reasoning_tokens"`
	TotalTokens         int64     `json:"total_tokens"`
	CostUSD             float64   `json:"cost_usd"`
	DurationMs          int64     `json:"duration_ms"`
	TTFTMs              int64     `json:"ttft_ms"`
	HTTPStatus          int64     `json:"http_status"`
	Endpoint            string    `json:"endpoint"`
	EventName           string    `json:"event_name"`
	EventSequence       int64     `json:"event_sequence"`
	TerminalType        string    `json:"terminal_type"`
	ServiceName         string    `json:"service_name"`
	ServiceVersion      string    `json:"service_version"`
	HostArch            string    `json:"host_arch"`
	OSType              string    `json:"os_type"`
	OSVersion           string    `json:"os_version"`
	ErrorMessage        string    `json:"error_message"`
}

// CodexTokenUpdate is the payload for UpdateCodexAPIRequestTokens.
//
// CostUSD is computed by the receiver via the local pricing registry
// (Codex never reports cost_usd itself). Pass 0 to leave cost untouched on
// both the row and the daily aggregate.
type CodexTokenUpdate struct {
	SessionID       string
	Model           string
	Timestamp       time.Time
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	ReasoningTokens int64
	TotalTokens     int64
	CostUSD         float64
	DurationMs      int64
}

// CodexEvent is a parsed Codex log record. Carries fields used by all
// secondary inserts; unrelated fields are zero.
type CodexEvent struct {
	Timestamp      time.Time
	SessionID      string
	ConversationID string
	EventName      string // codex.user_prompt / codex.tool_decision / ...
	EventKind      string // for codex.sse_event sub-types
	EventSequence  int64
	Model          string
	DurationMs     int64
	ErrorMessage   string

	// user_prompt
	PromptText   string
	PromptLength int64

	// tool_decision / tool_result
	ToolName string
	CallID   string
	Decision string
	Source   string
	Success  int

	// tool_result
	ArgumentsLength int64
	OutputLength    int64
	ToolOrigin      string
	MCPServer       string

	TerminalType   string
	ServiceName    string
	ServiceVersion string

	RawAttrsJSON string
}
