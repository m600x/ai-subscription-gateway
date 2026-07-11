// Package codex is the OpenAI Codex provider. It fronts the ChatGPT Codex
// Responses API (chatgpt.com/backend-api/codex/responses) using a ChatGPT
// subscription OAuth token pair (short-lived access token + refresh token),
// translating between the OpenAI chat-completions shape and the Responses API.
package codex

import "encoding/json"

// --- Responses API request ---

// contentPart is one element of a Responses input message content array.
type contentPart struct {
	Type     string `json:"type"` // input_text | output_text | input_image
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// inputItem is a Responses `input` element. It is polymorphic on Type:
// "message", "function_call", or "function_call_output".
type inputItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role,omitempty"`
	Content []contentPart `json:"content,omitempty"`
	// function_call
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	// function_call_output
	Output string `json:"output,omitempty"`
}

// responsesTool declares a client function tool.
type responsesTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Strict      bool            `json:"strict"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// reasoning is the Responses reasoning control.
type reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// responsesRequest is the POST /responses body.
type responsesRequest struct {
	Model             string          `json:"model"`
	Input             []inputItem     `json:"input"`
	Tools             []responsesTool `json:"tools"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls bool            `json:"parallel_tool_calls"`
	Store             bool            `json:"store"`
	Stream            bool            `json:"stream"`
	PromptCacheKey    string          `json:"prompt_cache_key,omitempty"`
	Instructions      string          `json:"instructions,omitempty"`
	Include           []string        `json:"include,omitempty"`
	Reasoning         *reasoning      `json:"reasoning,omitempty"`
}

// --- Responses API stream events ---

type tokenDetails struct {
	CachedTokens    int `json:"cached_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
}

type usage struct {
	InputTokens         int           `json:"input_tokens"`
	OutputTokens        int           `json:"output_tokens"`
	TotalTokens         int           `json:"total_tokens"`
	InputTokensDetails  *tokenDetails `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *tokenDetails `json:"output_tokens_details,omitempty"`
}

type apiError struct {
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// outputItem is the `item` on response.output_item.done events.
type outputItem struct {
	Type      string          `json:"type"` // function_call | web_search_call | message | reasoning
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// responseObject is the `response` envelope on lifecycle events.
type responseObject struct {
	ID     string    `json:"id,omitempty"`
	Status string    `json:"status,omitempty"`
	Usage  *usage    `json:"usage,omitempty"`
	Error  *apiError `json:"error,omitempty"`
}

// streamEvent is a decoded Responses SSE `data:` payload.
type streamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta,omitempty"`
	Item     *outputItem     `json:"item,omitempty"`
	Response *responseObject `json:"response,omitempty"`
	Error    *apiError       `json:"error,omitempty"`
}

// tokenResponse is the OAuth token endpoint response (login + refresh).
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}
