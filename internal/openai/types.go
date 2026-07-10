// Package openai defines the OpenAI-compatible request/response types the
// wrapper exposes to clients (Open WebUI, OpenAI SDKs, curl).
package openai

import (
	"encoding/json"
	"strings"
)

// Content decodes an OpenAI message content that may be either a plain string
// or an array of content parts (multimodal). Text parts are concatenated;
// non-text parts (images) are ignored in v1.
type Content string

// UnmarshalJSON accepts both the string and the array-of-parts forms.
func (c *Content) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*c = Content(s)
		return nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		*c = Content(sb.String())
		return nil
	}
	*c = ""
	return nil
}

// ChatMessage is one message in a chat completion request.
type ChatMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// ChatCompletionRequest is the POST /v1/chat/completions body.
type ChatCompletionRequest struct {
	Model               string        `json:"model"`
	Messages            []ChatMessage `json:"messages"`
	Stream              bool          `json:"stream"`
	MaxTokens           *int          `json:"max_tokens"`
	MaxCompletionTokens *int          `json:"max_completion_tokens"`
	Temperature         *float64      `json:"temperature"`
	TopP                *float64      `json:"top_p"`
	// ReasoningEffort is OpenAI's low|medium|high (also minimal). Open WebUI
	// sends it as an advanced per-model param; the wrapper maps it to an
	// Anthropic thinking budget for thinking-capable models.
	ReasoningEffort string `json:"reasoning_effort"`
}

// RespMessage is the assistant message in a non-streaming completion.
type RespMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Delta is the incremental payload in a streaming chunk. reasoning_content is
// rendered by Open WebUI (and others) as a collapsible reasoning section.
type Delta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// Choice is one completion choice (message for non-stream, delta for stream).
type Choice struct {
	Index        int          `json:"index"`
	Message      *RespMessage `json:"message,omitempty"`
	Delta        *Delta       `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason"`
}

// Usage reports token counts in OpenAI shape.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
