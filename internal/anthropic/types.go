// Package anthropic is a thin client for the Anthropic Messages API using a
// subscription OAuth token. It handles the Claude Code identity requirement
// (the exact spoof system block is assembled by the translate package) and
// exposes streaming and non-streaming calls.
package anthropic

import "encoding/json"

// SystemBlock is one entry in the Messages API `system` array.
type SystemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Message is a single conversation turn. Content always uses the
// array-of-blocks form (the API also accepts a plain string; one marshal path
// is simpler and tool_use/tool_result require blocks anyway).
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// Thinking configures thinking mode. Current models use adaptive thinking
// ({type: "adaptive"}) guided by OutputConfig.Effort; display "summarized"
// makes thinking text stream as readable thinking_delta events.
type Thinking struct {
	Type    string `json:"type"`
	Display string `json:"display,omitempty"`
}

// OutputConfig carries the effort parameter (low|medium|high|xhigh|max).
type OutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// Tool declares either a server-side tool (Type+Name, e.g. web_search) or a
// client function tool (Name+Description+InputSchema, no Type).
type Tool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ToolChoice steers tool selection: auto|any|tool|none, with Name naming the
// forced tool for type "tool". DisableParallelToolUse maps from OpenAI's
// parallel_tool_calls=false.
type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

// MessagesRequest is the POST /v1/messages body.
type MessagesRequest struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	System       []SystemBlock `json:"system,omitempty"`
	Messages     []Message     `json:"messages"`
	Stream       bool          `json:"stream,omitempty"`
	Thinking     *Thinking     `json:"thinking,omitempty"`
	OutputConfig *OutputConfig `json:"output_config,omitempty"`
	Tools        []Tool        `json:"tools,omitempty"`
	ToolChoice   *ToolChoice   `json:"tool_choice,omitempty"`
	Temperature  *float64      `json:"temperature,omitempty"`
	TopP         *float64      `json:"top_p,omitempty"`
}

// OutputTokensDetails breaks down output tokens (thinking vs text).
type OutputTokensDetails struct {
	ThinkingTokens int `json:"thinking_tokens"`
}

// Usage reports token counts, including cache and thinking details.
type Usage struct {
	InputTokens              int                  `json:"input_tokens"`
	OutputTokens             int                  `json:"output_tokens"`
	CacheReadInputTokens     int                  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int                  `json:"cache_creation_input_tokens"`
	OutputTokensDetails      *OutputTokensDetails `json:"output_tokens_details,omitempty"`
}

// ContentBlock is one content block: a request message block, a block of a
// non-streaming response, or a stream block header. Fields are a union across
// block types (text, thinking, tool_use, tool_result, server_tool_use);
// unused ones stay empty. ToolUseID/Content belong to tool_result blocks
// (request-only); ID/Input belong to tool_use blocks.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

// MessagesResponse is the non-streaming response body.
type MessagesResponse struct {
	ID         string         `json:"id"`
	Model      string         `json:"model"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// StreamDelta is the `delta` field across SSE event types. PartialJSON
// carries input_json_delta fragments of a tool_use block's input.
type StreamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// StreamEvent is a decoded SSE `data:` payload from the Messages API.
type StreamEvent struct {
	Type         string            `json:"type"`
	Message      *MessagesResponse `json:"message,omitempty"`
	Index        int               `json:"index,omitempty"`
	ContentBlock *ContentBlock     `json:"content_block,omitempty"`
	Delta        *StreamDelta      `json:"delta,omitempty"`
	Usage        *Usage            `json:"usage,omitempty"`
	Error        *APIErrorBody     `json:"error,omitempty"`
}

// APIErrorBody is the `error` object in an Anthropic error response.
type APIErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
