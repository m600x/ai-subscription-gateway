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
	base, variant := splitModelVariant(model)

	out := anthropic.MessagesRequest{
		Model:    base,
		System:   system,
		Messages: msgs,
		Stream:   req.Stream,
	}

	// A "-max" variant lifts the output ceiling regardless of thinking.
	if variant == "max" && maxTokens < cfg.MaxOutputTokens {
		maxTokens = cfg.MaxOutputTokens
	}
	out.MaxTokens = maxTokens

	if budget := resolveThinkingBudget(base, variant, req.ReasoningEffort, cfg); budget > 0 {
		out.Thinking = &anthropic.Thinking{Type: "enabled", BudgetTokens: budget}
		// max_tokens must exceed the thinking budget.
		if out.MaxTokens <= budget {
			out.MaxTokens = budget + cfg.DefaultMaxTokens
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

// splitModelVariant strips a "-thinking" / "-max" suffix, returning the base
// model id and the variant ("thinking", "max", or "").
func splitModelVariant(model string) (base, variant string) {
	switch {
	case strings.HasSuffix(model, "-max"):
		return strings.TrimSuffix(model, "-max"), "max"
	case strings.HasSuffix(model, "-thinking"):
		return strings.TrimSuffix(model, "-thinking"), "thinking"
	default:
		return model, ""
	}
}

// budgetForEffort maps an OpenAI reasoning_effort value to a thinking budget.
// Returns -1 when unspecified/unrecognized (caller falls back to a variant or
// global default) and 0 for an explicit "off".
func budgetForEffort(effort string, cfg *config.Config) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "":
		return -1
	case "minimal", "none", "off":
		return 0
	case "low":
		return cfg.ThinkingBudgetLow
	case "medium":
		return cfg.ThinkingBudgetMed
	case "high", "max":
		return cfg.ThinkingBudgetHigh
	default:
		return -1
	}
}

// resolveThinkingBudget decides the extended-thinking budget for a request.
// Precedence: an explicit reasoning_effort always wins; otherwise the model
// variant sets a default (-max -> high, -thinking -> medium); a plain model
// falls back to the global MaxThinkingTokens default. Models that can't take an
// explicit budget (e.g. Fable's silent thinking) always resolve to 0.
func resolveThinkingBudget(base, variant, effort string, cfg *config.Config) int {
	if !cfg.IsThinkingModel(base) {
		return 0
	}
	if eb := budgetForEffort(effort, cfg); eb >= 0 {
		return eb
	}
	switch variant {
	case "max":
		return cfg.ThinkingBudgetHigh
	case "thinking":
		return cfg.ThinkingBudgetMed
	default:
		return cfg.MaxThinkingTokens
	}
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
