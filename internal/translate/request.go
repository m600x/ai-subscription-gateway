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
		Model:     base,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  msgs,
		Stream:    req.Stream,
	}

	thinking := false
	switch effort := resolveEffort(base, variant, req.ReasoningEffort, cfg); effort {
	case "":
		// Default: no thinking config. Default-on models (Sonnet 5) and
		// always-on models (Fable 5) still think adaptively at their own
		// default effort; the rest stay fast.
	case "off":
		// Always-on models reject an explicit disable -- omit the config
		// and let the model do its (unavoidable) silent thinking.
		if !cfg.IsThinkingAlwaysOn(base) && cfg.IsThinkingDefaultOn(base) {
			out.Thinking = &anthropic.Thinking{Type: "disabled"}
		}
	default:
		thinking = true
		out.Thinking = &anthropic.Thinking{Type: "adaptive", Display: cfg.ThinkingDisplay}
		out.OutputConfig = &anthropic.OutputConfig{Effort: effort}
		// max_tokens caps thinking + response combined; leave headroom so
		// high-effort thinking cannot starve the visible answer.
		if out.MaxTokens < 4*cfg.DefaultMaxTokens {
			out.MaxTokens = 4 * cfg.DefaultMaxTokens
		}
	}

	// temperature/top_p are incompatible with active thinking.
	if !thinking {
		out.Temperature = req.Temperature
		out.TopP = req.TopP
	}

	if cfg.EnableWebSearch {
		out.Tools = []anthropic.Tool{{Type: "web_search_20250305", Name: "web_search"}}
	}

	return out
}

// splitModelVariant strips a "-thinking" suffix, returning the base model id
// and the variant ("thinking" or "").
func splitModelVariant(model string) (base, variant string) {
	if strings.HasSuffix(model, "-thinking") {
		return strings.TrimSuffix(model, "-thinking"), "thinking"
	}
	return model, ""
}

// normalizeEffort maps a reasoning_effort value onto the Anthropic effort
// ladder (low|medium|high|xhigh|max), "off" for an explicit disable, or ""
// when unspecified/unrecognized.
func normalizeEffort(effort string) string {
	switch e := strings.ToLower(strings.TrimSpace(effort)); e {
	case "minimal", "none", "off":
		return "off"
	case "low", "medium", "high", "xhigh", "max":
		return e
	case "extra-high", "extra_high", "xtra-high":
		return "xhigh"
	default:
		return ""
	}
}

// resolveEffort decides the effort for a request. Precedence: an explicit
// reasoning_effort always wins; otherwise a -thinking variant enables
// adaptive thinking at the API default effort (high). Non-thinking models
// always resolve to "" (no thinking config sent).
func resolveEffort(base, variant, effort string, cfg *config.Config) string {
	if !cfg.IsThinkingModel(base) {
		return ""
	}
	if e := normalizeEffort(effort); e != "" {
		return e
	}
	if variant == "thinking" {
		return "high"
	}
	return ""
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
