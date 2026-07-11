package anthropic

import (
	"context"

	"github.com/m600x/ai-substation/internal/config"
	"github.com/m600x/ai-substation/internal/openai"
	"github.com/m600x/ai-substation/internal/provider"
	"github.com/m600x/ai-substation/internal/registry"
)

// Provider adapts the Anthropic Messages API to the provider.Provider
// interface.
type Provider struct {
	client *Client
	cfg    *config.Config
}

// NewProvider builds the Anthropic provider.
func NewProvider(cfg *config.Config) *Provider {
	return &Provider{client: New(cfg), cfg: cfg}
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return registry.ProviderAnthropic }

func displayModel(req openai.ChatCompletionRequest, m registry.Model) string {
	if req.Model != "" {
		return req.Model
	}
	return m.ID
}

// Complete implements provider.Provider (non-streaming).
func (p *Provider) Complete(ctx context.Context, req openai.ChatCompletionRequest, m registry.Model) (openai.ChatCompletion, error) {
	mr := BuildMessagesRequest(req, m, p.cfg)
	resp, err := p.client.CreateMessage(ctx, mr)
	if err != nil {
		return openai.ChatCompletion{}, err
	}
	return BuildChatCompletion(resp, provider.NewID(), displayModel(req, m)), nil
}

// Stream implements provider.Provider (streaming).
func (p *Provider) Stream(ctx context.Context, req openai.ChatCompletionRequest, m registry.Model, sink provider.ChunkSink) error {
	mr := BuildMessagesRequest(req, m, p.cfg)
	body, err := p.client.StreamMessage(ctx, mr)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	return StreamResponse(body, sink, provider.NewID(), displayModel(req, m), p.cfg)
}

var _ provider.Provider = (*Provider)(nil)
