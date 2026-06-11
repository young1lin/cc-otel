package db

import "time"

// GeminiAPIRequest represents a single Gemini CLI API response record.
// Fields map 1:1 to the gemini_cli.api_response OTLP event attributes.
type GeminiAPIRequest struct {
	ID              int64     `json:"id"`
	Timestamp       time.Time `json:"timestamp"`
	SessionID       string    `json:"session_id"`
	Model           string    `json:"model"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	CacheReadTokens int64     `json:"cache_read_tokens"`
	ThoughtsTokens  int64     `json:"thoughts_tokens"`
	ToolTokens      int64     `json:"tool_tokens"`
	TotalTokens     int64     `json:"total_tokens"`
	DurationMs      int64     `json:"duration_ms"`
	CostUSD         float64   `json:"cost_usd"`
	HTTPStatusCode  int64     `json:"http_status_code"`
	PromptID        string    `json:"prompt_id"`
	EventName       string    `json:"event_name"`
	ServiceName     string    `json:"service_name"`
	ServiceVersion  string    `json:"service_version"`
}
