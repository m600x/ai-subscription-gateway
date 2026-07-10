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
		Usage: &openai.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
}
