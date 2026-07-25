package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/m600x/ai-subscription-gateway/internal/config"
	"github.com/m600x/ai-subscription-gateway/internal/openai"
	"github.com/m600x/ai-subscription-gateway/internal/registry"
)

func testCfg() *config.Config {
	return &config.Config{
		SpoofSystemPrompt: "You are Claude Code, Anthropic's official CLI for Claude.",
		DefaultMaxTokens:  8192,
		ThinkingDisplay:   "summarized",
	}
}

var fullLadder = []string{"off", "low", "medium", "high", "xhigh", "max"}

func modelSonnet() registry.Model {
	return registry.Model{ID: "claude-sonnet-5", Provider: "anthropic", UpstreamID: "claude-sonnet-5",
		Reasoning: registry.Reasoning{Efforts: fullLadder, Default: "high", Mode: registry.ModeDefaultOn}, DefaultMaxTokens: 8192}
}
func modelOpus() registry.Model {
	return registry.Model{ID: "claude-opus-4-8", Provider: "anthropic", UpstreamID: "claude-opus-4-8",
		Reasoning: registry.Reasoning{Efforts: fullLadder, Default: "high", Mode: registry.ModeOptIn}, DefaultMaxTokens: 8192}
}
func modelFable() registry.Model {
	return registry.Model{ID: "claude-fable-5", Provider: "anthropic", UpstreamID: "claude-fable-5",
		Reasoning: registry.Reasoning{Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Default: "high", Mode: registry.ModeAlwaysOn}, DefaultMaxTokens: 8192}
}

func TestSpoofIsFirstSystemBlock(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{
			{Role: "system", Content: openai.Content{Text: "You are a pirate."}},
			{Role: "user", Content: openai.Content{Text: "hi"}},
		},
	}
	mr := BuildMessagesRequest(req, modelSonnet(), cfg)

	if len(mr.System) != 2 {
		t.Fatalf("want 2 system blocks, got %d", len(mr.System))
	}
	if mr.System[0].Text != cfg.SpoofSystemPrompt {
		t.Errorf("first system block must be exactly the spoof; got %q", mr.System[0].Text)
	}
	if mr.System[1].Text != "You are a pirate." {
		t.Errorf("user system prompt not preserved; got %q", mr.System[1].Text)
	}
	if mr.MaxTokens != 8192 {
		t.Errorf("default max_tokens not injected; got %d", mr.MaxTokens)
	}
	if mr.Model != "claude-sonnet-5" {
		t.Errorf("upstream model not applied; got %q", mr.Model)
	}
}

func TestCoalesceConsecutiveRoles(t *testing.T) {
	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{
			{Role: "user", Content: openai.Content{Text: "a"}},
			{Role: "user", Content: openai.Content{Text: "b"}},
			{Role: "assistant", Content: openai.Content{Text: "c"}},
		},
	}
	mr := BuildMessagesRequest(req, modelSonnet(), testCfg())

	if len(mr.Messages) != 2 {
		t.Fatalf("want 2 coalesced messages, got %d", len(mr.Messages))
	}
	if len(mr.Messages[0].Content) != 1 || mr.Messages[0].Content[0].Text != "a\n\nb" {
		t.Errorf("consecutive user messages not merged into one text block; got %+v", mr.Messages[0].Content)
	}
}

func TestClientMaxTokensHonored(t *testing.T) {
	mt := 100
	req := openai.ChatCompletionRequest{
		MaxTokens: &mt,
		Messages:  []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}},
	}
	mr := BuildMessagesRequest(req, modelSonnet(), testCfg())
	if mr.MaxTokens != 100 {
		t.Errorf("client max_tokens not honored; got %d", mr.MaxTokens)
	}
}

func TestEffortEnablesAdaptiveAndDropsSampling(t *testing.T) {
	cfg := testCfg()
	temp := 0.7
	req := openai.ChatCompletionRequest{
		Model:           "claude-sonnet-5",
		ReasoningEffort: "high",
		Temperature:     &temp,
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}},
	}
	mr := BuildMessagesRequest(req, modelSonnet(), cfg)

	if mr.Thinking == nil || mr.Thinking.Type != "adaptive" || mr.Thinking.Display != "summarized" {
		t.Errorf("want adaptive thinking with summarized display; got %+v", mr.Thinking)
	}
	if mr.OutputConfig == nil || mr.OutputConfig.Effort != "high" {
		t.Errorf("effort not passed through; got %+v", mr.OutputConfig)
	}
	if mr.Temperature != nil {
		t.Error("temperature must be dropped when thinking is active")
	}
	if mr.MaxTokens < 4*cfg.DefaultMaxTokens {
		t.Errorf("max_tokens (%d) should leave headroom for thinking", mr.MaxTokens)
	}
}

func TestEffortLadderPassesThrough(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		req := openai.ChatCompletionRequest{
			Model:           "claude-opus-4-8",
			ReasoningEffort: effort,
			Messages:        []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}},
		}
		mr := BuildMessagesRequest(req, modelOpus(), testCfg())
		if mr.Thinking == nil || mr.Thinking.Type != "adaptive" {
			t.Errorf("effort %q: want adaptive thinking; got %+v", effort, mr.Thinking)
		}
		if mr.OutputConfig == nil || mr.OutputConfig.Effort != effort {
			t.Errorf("effort %q not passed through; got %+v", effort, mr.OutputConfig)
		}
	}
}

func TestEffortNotInLadderIsIgnored(t *testing.T) {
	// A model whose ladder omits an effort must ignore that request value.
	m := modelOpus()
	m.Reasoning.Efforts = []string{"low", "medium", "high"}
	req := openai.ChatCompletionRequest{
		Model:           "claude-opus-4-8",
		ReasoningEffort: "xhigh",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}},
	}
	mr := BuildMessagesRequest(req, m, testCfg())
	if mr.Thinking != nil || mr.OutputConfig != nil {
		t.Errorf("unsupported effort must be ignored; got %+v %+v", mr.Thinking, mr.OutputConfig)
	}
}

func TestOffDisablesThinkingOnDefaultOnModel(t *testing.T) {
	req := openai.ChatCompletionRequest{
		Model:           "claude-sonnet-5",
		ReasoningEffort: "off",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}},
	}
	mr := BuildMessagesRequest(req, modelSonnet(), testCfg())
	// Sonnet 5 thinks by default -> "off" must send an explicit disable.
	if mr.Thinking == nil || mr.Thinking.Type != "disabled" {
		t.Errorf("off on a default-on model should send thinking disabled; got %+v", mr.Thinking)
	}
	if mr.OutputConfig != nil {
		t.Errorf("off must not send an effort; got %+v", mr.OutputConfig)
	}
}

func TestOffIgnoredOnAlwaysOnModel(t *testing.T) {
	req := openai.ChatCompletionRequest{
		Model:           "claude-fable-5",
		ReasoningEffort: "off",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}},
	}
	mr := BuildMessagesRequest(req, modelFable(), testCfg())
	// Fable rejects thinking.type=disabled -> send nothing.
	if mr.Thinking != nil {
		t.Errorf("off on an always-on model must omit the thinking config; got %+v", mr.Thinking)
	}
}

func TestOffOnOptInModelSendsNothing(t *testing.T) {
	req := openai.ChatCompletionRequest{
		Model:           "claude-opus-4-8",
		ReasoningEffort: "off",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}},
	}
	mr := BuildMessagesRequest(req, modelOpus(), testCfg())
	// Opus doesn't think unless asked -> no config needed to stay off.
	if mr.Thinking != nil || mr.OutputConfig != nil {
		t.Errorf("off on an opt-in model should send nothing; got %+v %+v", mr.Thinking, mr.OutputConfig)
	}
}

func TestBuildChatCompletion(t *testing.T) {
	resp := &MessagesResponse{
		ID:    "msg_1",
		Model: "claude-sonnet-5",
		Role:  "assistant",
		Content: []ContentBlock{
			{Type: "thinking", Thinking: "hmm"}, // must be ignored in the text
			{Type: "text", Text: "Hello "},
			{Type: "text", Text: "world"},
		},
		StopReason: "max_tokens",
		Usage:      Usage{InputTokens: 10, OutputTokens: 5},
	}
	cc := BuildChatCompletion(resp, "chatcmpl-1", "claude-sonnet-5")
	if cc.Object != "chat.completion" || cc.ID != "chatcmpl-1" || cc.Model != "claude-sonnet-5" {
		t.Errorf("envelope = %+v", cc)
	}
	if len(cc.Choices) != 1 || cc.Choices[0].Message == nil {
		t.Fatalf("choices = %+v", cc.Choices)
	}
	if got := cc.Choices[0].Message.Content; got != "Hello world" {
		t.Errorf("content = %q, want concatenated text only", got)
	}
	if fr := cc.Choices[0].FinishReason; fr == nil || *fr != "length" {
		t.Errorf("finish_reason = %v, want length (max_tokens)", fr)
	}
	if cc.Usage == nil || cc.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v", cc.Usage)
	}
}

func TestErrorImplementsHTTPError(t *testing.T) {
	e := &Error{Status: 401, Type: "authentication_error", Message: "bad token"}
	if e.HTTPStatus() != 401 || e.ErrType() != "authentication_error" {
		t.Errorf("HTTPError methods = %d/%q", e.HTTPStatus(), e.ErrType())
	}
	if !strings.Contains(e.Error(), "401") || !strings.Contains(e.Error(), "bad token") {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestWebSearchToolAddedWhenEnabled(t *testing.T) {
	cfg := testCfg()
	cfg.EnableWebSearch = true
	req := openai.ChatCompletionRequest{Messages: []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}}}
	mr := BuildMessagesRequest(req, modelSonnet(), cfg)
	if len(mr.Tools) != 1 || mr.Tools[0].Name != "web_search" {
		t.Errorf("web_search tool not added; got %+v", mr.Tools)
	}
	if mr.ToolChoice != nil {
		t.Errorf("web_search alone must not emit a tool_choice; got %+v", mr.ToolChoice)
	}
}

func TestClientToolsForwarded(t *testing.T) {
	cfg := testCfg()
	cfg.EnableWebSearch = true
	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}},
		Tools: []openai.Tool{
			{Type: "function", Function: openai.FunctionDef{Name: "get_weather", Description: "Weather lookup",
				Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)}},
			{Type: "function", Function: openai.FunctionDef{Name: "no_params"}},
			{Type: "web_search_preview"}, // non-function entry: skipped
		},
	}
	mr := BuildMessagesRequest(req, modelSonnet(), cfg)
	if len(mr.Tools) != 3 {
		t.Fatalf("want 2 client tools + web_search, got %+v", mr.Tools)
	}
	if mr.Tools[0].Name != "get_weather" || mr.Tools[0].Description != "Weather lookup" || mr.Tools[0].Type != "" {
		t.Errorf("client tool mangled: %+v", mr.Tools[0])
	}
	if !strings.Contains(string(mr.Tools[0].InputSchema), `"city"`) {
		t.Errorf("input_schema not forwarded: %s", mr.Tools[0].InputSchema)
	}
	if string(mr.Tools[1].InputSchema) != `{"type":"object","properties":{}}` {
		t.Errorf("missing parameters must default to an empty object schema: %s", mr.Tools[1].InputSchema)
	}
	if mr.Tools[2].Name != "web_search" || mr.Tools[2].Type == "" {
		t.Errorf("web_search server tool must be appended after client tools: %+v", mr.Tools[2])
	}
}

func TestToolChoiceMapping(t *testing.T) {
	noParallel := false
	cases := []struct {
		name     string
		raw      string
		parallel *bool
		want     *ToolChoice
	}{
		{"unset", "", nil, nil},
		{"auto", `"auto"`, nil, nil},
		{"none", `"none"`, nil, &ToolChoice{Type: "none"}},
		{"required", `"required"`, nil, &ToolChoice{Type: "any"}},
		{"named", `{"type":"function","function":{"name":"get_weather"}}`, nil, &ToolChoice{Type: "tool", Name: "get_weather"}},
		{"noparallel", `"auto"`, &noParallel, &ToolChoice{Type: "auto", DisableParallelToolUse: true}},
	}
	for _, c := range cases {
		var raw json.RawMessage
		if c.raw != "" {
			raw = json.RawMessage(c.raw)
		}
		got := mapToolChoice(raw, c.parallel)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestAssistantToolCallsBecomeToolUse(t *testing.T) {
	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{
			{Role: "user", Content: openai.Content{Text: "weather?"}},
			{Role: "assistant", Content: openai.Content{Text: "Let me check."}, ToolCalls: []openai.ToolCall{
				{ID: "call_1", Type: "function", Function: openai.ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
				{ID: "call_2", Type: "function", Function: openai.ToolCallFunction{Name: "get_time", Arguments: `not json`}},
			}},
		},
	}
	mr := BuildMessagesRequest(req, modelSonnet(), testCfg())
	if len(mr.Messages) != 2 {
		t.Fatalf("messages = %+v", mr.Messages)
	}
	blocks := mr.Messages[1].Content
	if len(blocks) != 3 || blocks[0].Type != "text" || blocks[1].Type != "tool_use" || blocks[2].Type != "tool_use" {
		t.Fatalf("assistant blocks = %+v", blocks)
	}
	if blocks[1].ID != "call_1" || blocks[1].Name != "get_weather" || string(blocks[1].Input) != `{"city":"Paris"}` {
		t.Errorf("tool_use block = %+v", blocks[1])
	}
	if string(blocks[2].Input) != `{}` {
		t.Errorf("unparseable arguments must degrade to {}; got %s", blocks[2].Input)
	}
}

func TestToolResultsGroupIntoOneUserTurn(t *testing.T) {
	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{
			{Role: "user", Content: openai.Content{Text: "weather?"}},
			{Role: "assistant", ToolCalls: []openai.ToolCall{
				{ID: "call_1", Type: "function", Function: openai.ToolCallFunction{Name: "get_weather", Arguments: `{}`}},
				{ID: "call_2", Type: "function", Function: openai.ToolCallFunction{Name: "get_time", Arguments: `{}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: openai.Content{Text: "sunny"}},
			{Role: "tool", ToolCallID: "call_2", Content: openai.Content{Text: "noon"}},
			{Role: "user", Content: openai.Content{Text: "thanks, summarize"}},
		},
	}
	mr := BuildMessagesRequest(req, modelSonnet(), testCfg())
	if len(mr.Messages) != 3 {
		t.Fatalf("want user/assistant/user, got %d: %+v", len(mr.Messages), mr.Messages)
	}
	if got := mr.Messages[1].Content; len(got) != 2 || got[0].Type != "tool_use" || got[1].Type != "tool_use" {
		t.Fatalf("assistant turn = %+v", got)
	}
	last := mr.Messages[2]
	if last.Role != "user" || len(last.Content) != 3 {
		t.Fatalf("merged user turn = %+v", last)
	}
	if last.Content[0].Type != "tool_result" || last.Content[0].ToolUseID != "call_1" || last.Content[0].Content != "sunny" {
		t.Errorf("first tool_result = %+v", last.Content[0])
	}
	if last.Content[1].Type != "tool_result" || last.Content[1].ToolUseID != "call_2" {
		t.Errorf("second tool_result = %+v", last.Content[1])
	}
	if last.Content[2].Type != "text" || last.Content[2].Text != "thanks, summarize" {
		t.Errorf("text must trail the tool_result blocks; got %+v", last.Content[2])
	}
}

func TestForcedToolChoiceKeepsThinking(t *testing.T) {
	// Verified live 2026-07-25: adaptive thinking coexists with tool_choice
	// any/tool (the old budget_tokens-era incompatibility does not apply).
	temp := 0.5
	req := openai.ChatCompletionRequest{
		ReasoningEffort: "high",
		Temperature:     &temp,
		ToolChoice:      json.RawMessage(`"required"`),
		Tools:           []openai.Tool{{Type: "function", Function: openai.FunctionDef{Name: "get_weather"}}},
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}},
	}
	mr := BuildMessagesRequest(req, modelSonnet(), testCfg())
	if mr.ToolChoice == nil || mr.ToolChoice.Type != "any" {
		t.Fatalf("tool_choice = %+v, want any", mr.ToolChoice)
	}
	if mr.Thinking == nil || mr.Thinking.Type != "adaptive" || mr.OutputConfig == nil || mr.OutputConfig.Effort != "high" {
		t.Errorf("forced tool choice must keep thinking; got %+v %+v", mr.Thinking, mr.OutputConfig)
	}
	if mr.Temperature != nil {
		t.Error("temperature must still be dropped while thinking is active")
	}
}

func TestBuildChatCompletionToolUse(t *testing.T) {
	resp := &MessagesResponse{
		Content: []ContentBlock{
			{Type: "text", Text: "Checking."},
			{Type: "tool_use", ID: "toolu_1", Name: "get_weather", Input: json.RawMessage(`{"city":"Paris"}`)},
		},
		StopReason: "tool_use",
		Usage:      Usage{InputTokens: 10, OutputTokens: 5},
	}
	cc := BuildChatCompletion(resp, "chatcmpl-1", "claude-sonnet-5")
	msg := cc.Choices[0].Message
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "toolu_1" || msg.ToolCalls[0].Function.Name != "get_weather" ||
		msg.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Errorf("tool_calls = %+v", msg.ToolCalls)
	}
	if fr := cc.Choices[0].FinishReason; fr == nil || *fr != "tool_calls" {
		t.Errorf("finish_reason = %v, want tool_calls", fr)
	}
	if msg.Content != "Checking." {
		t.Errorf("content = %q", msg.Content)
	}
}
