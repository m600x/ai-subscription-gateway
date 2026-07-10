package translate

import (
	"strings"
	"time"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/anthropic"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/openai"
)

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
func BuildUsage(u anthropic.Usage) *openai.Usage {
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
func BuildChatCompletion(resp *anthropic.MessagesResponse, id, model string) openai.ChatCompletion {
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
