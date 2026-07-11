// Package openai defines the OpenAI-compatible request/response types the
// wrapper exposes to clients (Open WebUI, OpenAI SDKs, curl).
package openai

import (
	"encoding/json"
	"strings"
)

// ImageURL is the image_url content part payload.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// ContentPart is one element of a multimodal message content array.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// Content decodes an OpenAI message content that may be either a plain string
// or an array of content parts (multimodal). It retains BOTH a flattened Text
// (concatenated text parts -- used by the text-only Anthropic path) and the
// structured Parts (used by the OpenAI/Codex path, which forwards images).
type Content struct {
	Text  string
	Parts []ContentPart
}

// UnmarshalJSON accepts both the string and the array-of-parts forms.
func (c *Content) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		c.Text = s
		c.Parts = []ContentPart{{Type: "text", Text: s}}
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(b, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		c.Text = sb.String()
		c.Parts = parts
		return nil
	}
	c.Text = ""
	c.Parts = nil
	return nil
}

// String returns the flattened text content.
func (c Content) String() string { return c.Text }

// ToolCallFunction is the function payload of a tool call.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall is an assistant tool/function call.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallDelta is a streaming tool-call fragment (carries an index so the
// client can assemble calls across chunks).
type ToolCallDelta struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function"`
}

// FunctionDef is a function tool definition supplied by the client.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Tool is one entry in the request's tools array.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// ChatMessage is one message in a chat completion request.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    Content    `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// StreamOptions carries the OpenAI stream_options object.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ChatCompletionRequest is the POST /v1/chat/completions body.
type ChatCompletionRequest struct {
	Model               string          `json:"model"`
	Messages            []ChatMessage   `json:"messages"`
	Stream              bool            `json:"stream"`
	StreamOptions       *StreamOptions  `json:"stream_options,omitempty"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	Tools               []Tool          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	// ReasoningEffort is OpenAI's low|medium|high (also minimal/off). Open WebUI
	// sends it as an advanced per-model param; the wrapper maps it onto each
	// provider's reasoning/thinking controls.
	ReasoningEffort string `json:"reasoning_effort"`
}

// RespMessage is the assistant message in a non-streaming completion.
type RespMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Delta is the incremental payload in a streaming chunk. reasoning_content is
// rendered by Open WebUI (and others) as a collapsible reasoning section.
type Delta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`
}

// Choice is one completion choice (message for non-stream, delta for stream).
type Choice struct {
	Index        int          `json:"index"`
	Message      *RespMessage `json:"message,omitempty"`
	Delta        *Delta       `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason"`
}

// PromptTokensDetails is the OpenAI prompt token breakdown.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// CompletionTokensDetails is the OpenAI completion token breakdown.
type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// Usage reports token counts in OpenAI shape, including the standard
// details objects (cached prompt tokens, reasoning tokens).
type Usage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

// ChatCompletion is used for both the non-streaming response (object
// "chat.completion") and each streaming chunk (object "chat.completion.chunk").
type ChatCompletion struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Model is one entry in the /v1/models list.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelList is the /v1/models response.
type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// ErrorBody / ErrorResponse are the OpenAI error envelope.
type ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// ErrorResponse wraps an ErrorBody.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}
