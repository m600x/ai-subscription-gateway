package translate

import (
	"strings"
	"testing"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/config"
)

func TestStreamResponseMapping(t *testing.T) {
	input := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var out strings.Builder
	sse := NewSSEWriter(&out, func() {})
	if err := StreamResponse(strings.NewReader(input), sse, "chatcmpl-x", "claude-sonnet-5", &config.Config{}); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		`"object":"chat.completion.chunk"`,
		`"role":"assistant"`,
		`"reasoning_content":"hmm"`,
		`"content":"Hello"`,
		`"content":" world"`,
		`"finish_reason":"stop"`,
		"data: [DONE]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, got)
		}
	}
}

func TestStreamResponseLengthFinish(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m"}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var out strings.Builder
	sse := NewSSEWriter(&out, func() {})
	if err := StreamResponse(strings.NewReader(input), sse, "id", "m", &config.Config{}); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if !strings.Contains(out.String(), `"finish_reason":"length"`) {
		t.Errorf("max_tokens stop_reason should map to finish_reason=length\n%s", out.String())
	}
}
