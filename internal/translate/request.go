// Package translate converts between the OpenAI chat-completions shape and the
// Anthropic Messages API, including the Claude Code identity requirement.
package translate

import (
	"strings"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/anthropic"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/config"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/openai"
)

// BuildMessagesRequest maps an OpenAI request to an Anthropic MessagesRequest.
//
// The system prompt is assembled as an array whose FIRST block is exactly the
// configured spoof string ("You are Claude Code, Anthropic's official CLI for
// Claude."). This is mandatory: the subscription OAuth token is rejected
// (disguised HTTP 429) unless that exact block leads the system prompt. Any
// system messages the client sent are appended as subsequent blocks, so the
// user's own instructions still apply.
func BuildMessagesRequest(req openai.ChatCompletionRequest, cfg *config.Config) anthropic.MessagesRequest {
	system := []anthropic.SystemBlock{{Type: "text", Text: cfg.SpoofSystemPrompt}}
	var msgs []anthropic.Message

	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if strings.TrimSpace(string(m.Content)) != "" {
				system = append(system, anthropic.SystemBlock{Type: "text", Text: string(m.Content)})
			}
		case "user", "assistant":
			msgs = append(msgs, anthropic.Message{Role: m.Role, Content: string(m.Content)})
		}
	}
	msgs = coalesce(msgs)

	maxTokens := cfg.DefaultMaxTokens
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		maxTokens = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	model := req.Model
	if model == "" {
		model = cfg.DefaultModel
	}

	out := anthropic.MessagesRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  msgs,
		Stream:    req.Stream,
	}

	if cfg.MaxThinkingTokens > 0 {
		out.Thinking = &anthropic.Thinking{Type: "enabled", BudgetTokens: cfg.MaxThinkingTokens}
		// max_tokens must exceed the thinking budget.
		if out.MaxTokens <= cfg.MaxThinkingTokens {
			out.MaxTokens = cfg.MaxThinkingTokens + cfg.DefaultMaxTokens
		}
		// temperature/top_p are incompatible with extended thinking -> leave unset.
	} else {
		out.Temperature = req.Temperature
		out.TopP = req.TopP
	}

	if cfg.EnableWebSearch {
		out.Tools = []anthropic.Tool{{Type: "web_search_20250305", Name: "web_search"}}
	}

	return out
}

// coalesce merges consecutive same-role messages (Anthropic requires
// alternating roles).
func coalesce(msgs []anthropic.Message) []anthropic.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]anthropic.Message, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = out[n-1].Content + "\n\n" + m.Content
			continue
		}
		out = append(out, m)
	}
	return out
}
