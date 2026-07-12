// Package provider defines the backend abstraction the server routes to. Each
// subscription backend (Anthropic Messages API, OpenAI Codex Responses API)
// implements Provider; the server picks one per request based on the model the
// client named (resolved via the registry).
package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/m600x/ai-subscription-gateway/internal/openai"
	"github.com/m600x/ai-subscription-gateway/internal/registry"
)

// NewID returns a fresh OpenAI-style completion id ("chatcmpl-…").
func NewID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

// Provider is a chat backend. Implementations translate the OpenAI-shaped
// request into their upstream API and back.
type Provider interface {
	// Name is the provider key (registry.ProviderAnthropic / ProviderOpenAI).
	Name() string
	// Complete performs a non-streaming completion.
	Complete(ctx context.Context, req openai.ChatCompletionRequest, m registry.Model) (openai.ChatCompletion, error)
	// Stream performs a streaming completion, emitting OpenAI chunks via sink.
	// The provider emits every chat.completion.chunk including the final
	// finish/usage chunk; the server owns keepalives and the trailing
	// "data: [DONE]" marker.
	Stream(ctx context.Context, req openai.ChatCompletionRequest, m registry.Model, sink ChunkSink) error
}

// ChunkSink receives translated OpenAI streaming chunks. The server's SSE
// writer implements it (marshal + "data: …\n\n" + flush).
type ChunkSink interface {
	Send(openai.ChatCompletion) error
}

// HTTPError is an upstream failure carrying an HTTP status and error type so
// the server can map it back onto an OpenAI error envelope. Both provider
// error types implement it.
type HTTPError interface {
	error
	HTTPStatus() int
	ErrType() string
}
