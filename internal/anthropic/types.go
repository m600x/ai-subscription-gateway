// Package anthropic is a thin client for the Anthropic Messages API using a
// subscription OAuth token. It handles the Claude Code identity requirement
// (the exact spoof system block is assembled by the translate package) and
// exposes streaming and non-streaming calls.
package anthropic

// SystemBlock is one entry in the Messages API `system` array.
type SystemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Message is a single conversation turn (text-only in v1).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

// Tool declares a server-side tool (e.g. web_search).
type Tool struct {
	Type string `json:"type"`
	Name string `json:"name"`
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
	Temperature  *float64      `json:"temperature,omitempty"`
	TopP         *float64      `json:"top_p,omitempty"`
}

// Usage reports token counts.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ContentBlock is one block of a non-streaming response (or a stream block header).
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	Name     string `json:"name,omitempty"`
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

// StreamDelta is the `delta` field across SSE event types.
type StreamDelta struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	Thinking   string `json:"thinking,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
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
