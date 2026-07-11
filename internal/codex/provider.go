package codex

import (
	"context"
	"strings"
	"time"

	"github.com/m600x/ai-substation/internal/config"
	"github.com/m600x/ai-substation/internal/openai"
	"github.com/m600x/ai-substation/internal/provider"
	"github.com/m600x/ai-substation/internal/registry"
)

// Provider adapts the ChatGPT Codex Responses API to provider.Provider.
type Provider struct {
	client *Client
	cfg    *config.Config
}

// NewProvider builds the OpenAI Codex provider.
func NewProvider(cfg *config.Config) *Provider {
	return &Provider{client: New(cfg), cfg: cfg}
}

// Prime validates the OpenAI credentials at startup.
func (p *Provider) Prime(ctx context.Context) error { return p.client.Prime(ctx) }

// Name implements provider.Provider.
func (p *Provider) Name() string { return registry.ProviderOpenAI }

func displayModel(req openai.ChatCompletionRequest, m registry.Model) string {
	if req.Model != "" {
		return req.Model
	}
	return m.ID
}

// Stream implements provider.Provider.
func (p *Provider) Stream(ctx context.Context, req openai.ChatCompletionRequest, m registry.Model, sink provider.ChunkSink) error {
	payload := buildRequest(req, m, p.cfg, newUUID())
	body, err := p.client.stream(ctx, payload)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	return streamResponse(body, sink, provider.NewID(), displayModel(req, m))
}

// Complete implements provider.Provider. The Codex backend is stream-only, so
// this drives the stream and aggregates it into a single chat.completion.
func (p *Provider) Complete(ctx context.Context, req openai.ChatCompletionRequest, m registry.Model) (openai.ChatCompletion, error) {
	payload := buildRequest(req, m, p.cfg, newUUID())
	body, err := p.client.stream(ctx, payload)
	if err != nil {
		return openai.ChatCompletion{}, err
	}
	defer func() { _ = body.Close() }()

	id := provider.NewID()
	model := displayModel(req, m)
	acc := &accumulator{tools: map[int]*openai.ToolCall{}}
	if err := streamResponse(body, acc, id, model); err != nil {
		return openai.ChatCompletion{}, err
	}

	msg := &openai.RespMessage{Role: "assistant", Content: acc.content.String()}
	for _, idx := range acc.order {
		msg.ToolCalls = append(msg.ToolCalls, *acc.tools[idx])
	}
	finish := acc.finish
	if finish == "" {
		finish = "stop"
	}
	return openai.ChatCompletion{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openai.Choice{{Index: 0, Message: msg, FinishReason: &finish}},
		Usage:   acc.usage,
	}, nil
}

// accumulator is a ChunkSink that folds streamed deltas into a whole message
// (used by Complete for the non-streaming response).
type accumulator struct {
	content strings.Builder
	tools   map[int]*openai.ToolCall
	order   []int
	finish  string
	usage   *openai.Usage
}

func (a *accumulator) Send(c openai.ChatCompletion) error {
	if len(c.Choices) == 0 {
		return nil
	}
	ch := c.Choices[0]
	if ch.Delta != nil {
		a.content.WriteString(ch.Delta.Content)
		for _, td := range ch.Delta.ToolCalls {
			tc, ok := a.tools[td.Index]
			if !ok {
				tc = &openai.ToolCall{Type: "function"}
				a.tools[td.Index] = tc
				a.order = append(a.order, td.Index)
			}
			if td.ID != "" {
				tc.ID = td.ID
			}
			if td.Type != "" {
				tc.Type = td.Type
			}
			if td.Function.Name != "" {
				tc.Function.Name = td.Function.Name
			}
			tc.Function.Arguments += td.Function.Arguments
		}
	}
	if ch.FinishReason != nil {
		a.finish = *ch.FinishReason
	}
	if c.Usage != nil {
		a.usage = c.Usage
	}
	return nil
}

var _ provider.Provider = (*Provider)(nil)
