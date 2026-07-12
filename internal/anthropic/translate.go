package anthropic

import (
	"strings"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/config"
	"github.com/m600x/ai-subscription-gateway/internal/openai"
	"github.com/m600x/ai-subscription-gateway/internal/registry"
)

// BuildMessagesRequest maps an OpenAI request to an Anthropic MessagesRequest.
//
// The system prompt is assembled as an array whose FIRST block is exactly the
// configured spoof string ("You are Claude Code, Anthropic's official CLI for
// Claude."). This is mandatory: the subscription OAuth token is rejected
// (disguised HTTP 429) unless that exact block leads the system prompt. Any
// system messages the client sent are appended as subsequent blocks, so the
// user's own instructions still apply.
//
// Reasoning behavior is driven by the registry model m: its effort ladder
// (m.Reasoning.Efforts) gates which reasoning_effort values are honored, and
// its thinking mode (m.Reasoning.Mode) decides how an explicit "off" is
// treated.
func BuildMessagesRequest(req openai.ChatCompletionRequest, m registry.Model, cfg *config.Config) MessagesRequest {
	system := []SystemBlock{{Type: "text", Text: cfg.SpoofSystemPrompt}}
	var msgs []Message

	for _, mm := range req.Messages {
		switch mm.Role {
		case "system", "developer":
			if strings.TrimSpace(mm.Content.String()) != "" {
				system = append(system, SystemBlock{Type: "text", Text: mm.Content.String()})
			}
		case "user", "assistant":
			msgs = append(msgs, Message{Role: mm.Role, Content: mm.Content.String()})
		}
	}
	msgs = coalesce(msgs)

	baseMax := cfg.DefaultMaxTokens
	if m.DefaultMaxTokens > 0 {
		baseMax = m.DefaultMaxTokens
	}
	maxTokens := baseMax
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		maxTokens = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	out := MessagesRequest{
		Model:     m.UpstreamID,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  msgs,
		Stream:    req.Stream,
	}

	thinking := false
	switch effort := resolveEffort(req.ReasoningEffort, m); effort {
	case "":
		// Default: no thinking config. Default-on models (Sonnet 5) and
		// always-on models (Fable 5) still think adaptively at their own
		// default effort; the rest stay fast.
	case "off":
		// Only default-on models need an explicit disable. Always-on models
		// reject one (so "off" is ignored) and opt-in models are already off.
		if m.Reasoning.Mode == registry.ModeDefaultOn {
			out.Thinking = &Thinking{Type: "disabled"}
		}
	default:
		thinking = true
		out.Thinking = &Thinking{Type: "adaptive", Display: cfg.ThinkingDisplay}
		out.OutputConfig = &OutputConfig{Effort: effort}
		// max_tokens caps thinking + response combined; leave headroom so
		// high-effort thinking cannot starve the visible answer.
		if out.MaxTokens < 4*baseMax {
			out.MaxTokens = 4 * baseMax
		}
	}

	// temperature/top_p are incompatible with active thinking.
	if !thinking {
		out.Temperature = req.Temperature
		out.TopP = req.TopP
	}

	if cfg.EnableWebSearch {
		out.Tools = []Tool{{Type: "web_search_20250305", Name: "web_search"}}
	}

	return out
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

// resolveEffort decides the effort for a request from the client's
// reasoning_effort, validated against the model's ladder. An effort the model
// does not advertise resolves to "" (no thinking config sent).
func resolveEffort(effort string, m registry.Model) string {
	e := normalizeEffort(effort)
	if e == "" || e == "off" {
		return e
	}
	if !m.AllowsEffort(e) {
		return ""
	}
	return e
}

// coalesce merges consecutive same-role messages (Anthropic requires
// alternating roles).
func coalesce(msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = out[n-1].Content + "\n\n" + m.Content
			continue
		}
		out = append(out, m)
	}
	return out
}

func mapStopReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	default:
		return "stop"
	}
}

// BuildUsage maps Anthropic usage (incl. cache reads and thinking tokens)
// onto the OpenAI usage shape. Prompt tokens include cache reads/writes so
// the total reflects what was actually processed.
func BuildUsage(u Usage) *openai.Usage {
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	out := &openai.Usage{
		PromptTokens:     prompt,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      prompt + u.OutputTokens,
	}
	if u.CacheReadInputTokens > 0 {
		out.PromptTokensDetails = &openai.PromptTokensDetails{CachedTokens: u.CacheReadInputTokens}
	}
	if u.OutputTokensDetails != nil && u.OutputTokensDetails.ThinkingTokens > 0 {
		out.CompletionTokensDetails = &openai.CompletionTokensDetails{
			ReasoningTokens: u.OutputTokensDetails.ThinkingTokens,
		}
	}
	return out
}

// BuildChatCompletion maps a non-streaming Anthropic response to an OpenAI
// chat.completion.
func BuildChatCompletion(resp *MessagesResponse, id, model string) openai.ChatCompletion {
	var sb strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	finish := mapStopReason(resp.StopReason)
	return openai.ChatCompletion{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openai.Choice{{
			Index:        0,
			Message:      &openai.RespMessage{Role: "assistant", Content: sb.String()},
			FinishReason: &finish,
		}},
		Usage: BuildUsage(resp.Usage),
	}
}
