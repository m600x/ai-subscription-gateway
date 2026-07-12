package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/m600x/ai-subscription-gateway/internal/config"
	"github.com/m600x/ai-subscription-gateway/internal/openai"
	"github.com/m600x/ai-subscription-gateway/internal/registry"
)

func codexModel() registry.Model {
	return registry.Model{
		ID: "gpt-5-codex", Provider: "openai", UpstreamID: "gpt-5-codex",
		Reasoning: registry.Reasoning{Efforts: []string{"minimal", "low", "medium", "high"}, Default: "medium"},
	}
}

type capture struct{ chunks []openai.ChatCompletion }

func (c *capture) Send(cc openai.ChatCompletion) error {
	c.chunks = append(c.chunks, cc)
	return nil
}

func TestResolveEffort(t *testing.T) {
	m := codexModel()
	cases := map[string]string{
		"high":    "high",
		"HIGH":    "high",
		"":        "medium", // default
		"max":     "medium", // not in ladder -> default
		"off":     "minimal",
		"minimal": "minimal",
	}
	for in, want := range cases {
		if got := resolveEffort(in, m); got != want {
			t.Errorf("resolveEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveEffortDisableFallsBackToLowest(t *testing.T) {
	// A GPT-5.6-style model whose ladder has no minimal/none: an "off" request
	// should resolve to the lowest advertised level rather than the default.
	m := registry.Model{
		ID: "gpt-5.6-sol", Provider: "openai", UpstreamID: "gpt-5.6-sol",
		Reasoning: registry.Reasoning{Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Default: "medium"},
	}
	for _, in := range []string{"off", "none", "minimal"} {
		if got := resolveEffort(in, m); got != "low" {
			t.Errorf("resolveEffort(%q) = %q, want low", in, got)
		}
	}
	if got := resolveEffort("max", m); got != "max" {
		t.Errorf("resolveEffort(max) = %q, want max", got)
	}
}

func TestBuildInstructions(t *testing.T) {
	cfg := &config.Config{OpenAIBaseInstructions: "BASE"}
	msgs := []openai.ChatMessage{
		{Role: "system", Content: openai.Content{Text: "be terse"}},
		{Role: "user", Content: openai.Content{Text: "hi"}},
	}
	got := buildInstructions(msgs, cfg)
	if got != "BASE\n\nbe terse" {
		t.Errorf("instructions = %q", got)
	}
}

func TestBuildInputConvertsRolesToolsImages(t *testing.T) {
	msgs := []openai.ChatMessage{
		{Role: "system", Content: openai.Content{Text: "ignored here"}},
		{Role: "user", Content: openai.Content{Parts: []openai.ContentPart{
			{Type: "text", Text: "look"},
			{Type: "image_url", ImageURL: &openai.ImageURL{URL: "data:image/png;base64,AAAA"}},
		}}},
		{Role: "assistant", ToolCalls: []openai.ToolCall{
			{ID: "call_1", Type: "function", Function: openai.ToolCallFunction{Name: "f", Arguments: `{"a":1}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: openai.Content{Text: "result"}},
	}
	items := buildInput(msgs)

	// system dropped; user message; function_call; function_call_output.
	if len(items) != 3 {
		t.Fatalf("items = %d (%+v)", len(items), items)
	}
	if items[0].Type != "message" || items[0].Role != "user" || len(items[0].Content) != 2 {
		t.Errorf("user item = %+v", items[0])
	}
	if items[0].Content[0].Type != "input_text" || items[0].Content[1].Type != "input_image" {
		t.Errorf("content parts wrong kinds: %+v", items[0].Content)
	}
	if items[1].Type != "function_call" || items[1].CallID != "call_1" || items[1].Name != "f" {
		t.Errorf("function_call item = %+v", items[1])
	}
	if items[2].Type != "function_call_output" || items[2].CallID != "call_1" || items[2].Output != "result" {
		t.Errorf("function_call_output item = %+v", items[2])
	}
}

func TestBuildToolsDefaultsParameters(t *testing.T) {
	tools := []openai.Tool{{Type: "function", Function: openai.FunctionDef{Name: "f"}}}
	out := buildTools(tools)
	if len(out) != 1 || out[0].Name != "f" {
		t.Fatalf("tools = %+v", out)
	}
	if !json.Valid(out[0].Parameters) {
		t.Errorf("default parameters not valid JSON: %s", out[0].Parameters)
	}
}

func TestBuildRequestShape(t *testing.T) {
	req := openai.ChatCompletionRequest{
		Model:           "gpt-5-codex",
		ReasoningEffort: "high",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi", Parts: []openai.ContentPart{{Type: "text", Text: "hi"}}}}},
	}
	r := buildRequest(req, codexModel(), &config.Config{}, "sess")
	if r.Model != "gpt-5-codex" || !r.Stream || r.Store {
		t.Errorf("bad base payload: %+v", r)
	}
	if r.Reasoning == nil || r.Reasoning.Effort != "high" || r.Reasoning.Summary != "auto" {
		t.Errorf("reasoning = %+v", r.Reasoning)
	}
	if len(r.Include) != 1 || r.Include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %+v", r.Include)
	}
	if r.PromptCacheKey != "sess" {
		t.Errorf("prompt_cache_key = %q", r.PromptCacheKey)
	}
}

func TestStreamResponseTextReasoningUsage(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"pondering"}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		``,
		`data: {"type":"response.output_text.delta","delta":" world"}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":3}}}}`,
		``,
	}, "\n")

	var cap capture
	if err := streamResponse(strings.NewReader(input), &cap, "id", "gpt-5-codex"); err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	var content, reasoning, finish string
	var usage *openai.Usage
	for _, c := range cap.chunks {
		ch := c.Choices[0]
		if ch.Delta != nil {
			content += ch.Delta.Content
			reasoning += ch.Delta.ReasoningContent
		}
		if ch.FinishReason != nil {
			finish = *ch.FinishReason
		}
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if content != "Hello world" {
		t.Errorf("content = %q", content)
	}
	if reasoning != "pondering" {
		t.Errorf("reasoning = %q", reasoning)
	}
	if finish != "stop" {
		t.Errorf("finish = %q", finish)
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 5 || usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 4 {
		t.Errorf("cached tokens missing: %+v", usage.PromptTokensDetails)
	}
	if usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Errorf("reasoning tokens missing: %+v", usage.CompletionTokensDetails)
	}
}

func TestStreamResponseToolCall(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"paris\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		``,
	}, "\n")

	var cap capture
	if err := streamResponse(strings.NewReader(input), &cap, "id", "gpt-5-codex"); err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	var name, args, finish string
	for _, c := range cap.chunks {
		ch := c.Choices[0]
		if ch.Delta != nil && len(ch.Delta.ToolCalls) > 0 {
			name = ch.Delta.ToolCalls[0].Function.Name
			args = ch.Delta.ToolCalls[0].Function.Arguments
		}
		if ch.FinishReason != nil {
			finish = *ch.FinishReason
		}
	}
	if name != "get_weather" || args != `{"city":"paris"}` {
		t.Errorf("tool call name=%q args=%q", name, args)
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q, want tool_calls", finish)
	}
}
