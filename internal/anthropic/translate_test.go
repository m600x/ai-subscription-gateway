package anthropic

import (
	"testing"

	"github.com/m600x/ai-substation/internal/config"
	"github.com/m600x/ai-substation/internal/openai"
	"github.com/m600x/ai-substation/internal/registry"
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
	if mr.Messages[0].Content != "a\n\nb" {
		t.Errorf("consecutive user messages not merged; got %q", mr.Messages[0].Content)
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

func TestWebSearchToolAddedWhenEnabled(t *testing.T) {
	cfg := testCfg()
	cfg.EnableWebSearch = true
	req := openai.ChatCompletionRequest{Messages: []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi"}}}}
	mr := BuildMessagesRequest(req, modelSonnet(), cfg)
	if len(mr.Tools) != 1 || mr.Tools[0].Name != "web_search" {
		t.Errorf("web_search tool not added; got %+v", mr.Tools)
	}
}
