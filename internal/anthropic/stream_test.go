package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/m600x/ai-substation/internal/config"
	"github.com/m600x/ai-substation/internal/openai"
)

// captureSink records emitted chunks as the server's SSE writer would frame
// them, so the string assertions match on-the-wire output (minus [DONE], which
// is the server's responsibility).
type captureSink struct{ b strings.Builder }

func (s *captureSink) Send(c openai.ChatCompletion) error {
	j, _ := json.Marshal(c)
	s.b.WriteString("data: " + string(j) + "\n\n")
	return nil
}
func (s *captureSink) String() string { return s.b.String() }

func TestStreamResponseMapping(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1"}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var sink captureSink
	if err := StreamResponse(strings.NewReader(input), &sink, "chatcmpl-x", "claude-sonnet-5", &config.Config{}); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	got := sink.String()
	for _, want := range []string{
		`"object":"chat.completion.chunk"`,
		`"role":"assistant"`,
		`"reasoning_content":"hmm"`,
		`"content":"Hello"`,
		`"content":" world"`,
		`"finish_reason":"stop"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, got)
		}
	}
}

func TestStreamResponseWebSearchStatus(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m"}}`,
		``,
		`data: {"type":"content_block_start","content_block":{"type":"server_tool_use","name":"web_search"}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Answer"}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var sink captureSink
	cfg := &config.Config{EnableWebSearch: true}
	if err := StreamResponse(strings.NewReader(input), &sink, "id", "m", cfg); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	got := sink.String()
	if !strings.Contains(got, `"content":"\n\n*searching the web…*\n\n"`) {
		t.Errorf("web search status should be italic with paragraph breaks\n%s", got)
	}
}

func TestStreamResponseFinalChunkCarriesUsage(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m","usage":{"input_tokens":45,"cache_read_input_tokens":10,"output_tokens":2}}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":45,"output_tokens":475,"output_tokens_details":{"thinking_tokens":126}}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var sink captureSink
	if err := StreamResponse(strings.NewReader(input), &sink, "id", "m", &config.Config{}); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	got := sink.String()
	for _, want := range []string{
		`"prompt_tokens":55`,
		`"completion_tokens":475`,
		`"total_tokens":530`,
		`"cached_tokens":10`,
		`"reasoning_tokens":126`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("final chunk missing %s\n%s", want, got)
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

	var sink captureSink
	if err := StreamResponse(strings.NewReader(input), &sink, "id", "m", &config.Config{}); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if !strings.Contains(sink.String(), `"finish_reason":"length"`) {
		t.Errorf("max_tokens stop_reason should map to finish_reason=length\n%s", sink.String())
	}
}
