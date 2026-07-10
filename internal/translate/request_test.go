package translate

import (
	"testing"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/config"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/openai"
)

func testCfg() *config.Config {
	return &config.Config{
		SpoofSystemPrompt:       "You are Claude Code, Anthropic's official CLI for Claude.",
		DefaultModel:            "claude-sonnet-5",
		DefaultMaxTokens:        8192,
		ThinkingModels:          []string{"claude-fable-5", "claude-opus-4-8", "claude-sonnet-5"},
		ThinkingAlwaysOnModels:  []string{"claude-fable-5"},
		ThinkingDefaultOnModels: []string{"claude-sonnet-5"},
		ThinkingDisplay:         "summarized",
	}
}

func TestSpoofIsFirstSystemBlock(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{
			{Role: "system", Content: "You are a pirate."},
			{Role: "user", Content: "hi"},
		},
	}
	mr := BuildMessagesRequest(req, cfg)

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
		t.Errorf("default model not applied; got %q", mr.Model)
	}
}

func TestCoalesceConsecutiveRoles(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{
			{Role: "user", Content: "a"},
			{Role: "user", Content: "b"},
			{Role: "assistant", Content: "c"},
		},
	}
	mr := BuildMessagesRequest(req, cfg)

	if len(mr.Messages) != 2 {
		t.Fatalf("want 2 coalesced messages, got %d", len(mr.Messages))
	}
	if mr.Messages[0].Content != "a\n\nb" {
		t.Errorf("consecutive user messages not merged; got %q", mr.Messages[0].Content)
	}
}

func TestClientMaxTokensHonored(t *testing.T) {
	cfg := testCfg()
	mt := 100
	req := openai.ChatCompletionRequest{
		MaxTokens: &mt,
		Messages:  []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)
	if mr.MaxTokens != 100 {
		t.Errorf("client max_tokens not honored; got %d", mr.MaxTokens)
	}
}

func TestThinkingVariantEnablesAdaptiveAndDropsSampling(t *testing.T) {
	cfg := testCfg()
	temp := 0.7
	req := openai.ChatCompletionRequest{
		Model:       "claude-sonnet-5-thinking",
		Temperature: &temp,
		Messages:    []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)

	if mr.Model != "claude-sonnet-5" {
		t.Errorf("variant suffix not stripped for upstream; got %q", mr.Model)
	}
	if mr.Thinking == nil || mr.Thinking.Type != "adaptive" || mr.Thinking.Display != "summarized" {
		t.Errorf("want adaptive thinking with summarized display; got %+v", mr.Thinking)
	}
	if mr.OutputConfig == nil || mr.OutputConfig.Effort != "high" {
		t.Errorf("-thinking variant should default to high effort; got %+v", mr.OutputConfig)
	}
	if mr.Temperature != nil {
		t.Error("temperature must be dropped when thinking is active")
	}
	if mr.MaxTokens < 4*cfg.DefaultMaxTokens {
		t.Errorf("max_tokens (%d) should leave headroom for thinking", mr.MaxTokens)
	}
}

func TestEffortLadderPassesThrough(t *testing.T) {
	cfg := testCfg()
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		req := openai.ChatCompletionRequest{
			Model:           "claude-opus-4-8",
			ReasoningEffort: effort,
			Messages:        []openai.ChatMessage{{Role: "user", Content: "hi"}},
		}
		mr := BuildMessagesRequest(req, cfg)
		if mr.Thinking == nil || mr.Thinking.Type != "adaptive" {
			t.Errorf("effort %q: want adaptive thinking; got %+v", effort, mr.Thinking)
		}
		if mr.OutputConfig == nil || mr.OutputConfig.Effort != effort {
			t.Errorf("effort %q not passed through; got %+v", effort, mr.OutputConfig)
		}
	}
}

func TestReasoningEffortOverridesVariant(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Model:           "claude-sonnet-5-thinking",
		ReasoningEffort: "xhigh",
		Messages:        []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)
	if mr.OutputConfig == nil || mr.OutputConfig.Effort != "xhigh" {
		t.Errorf("explicit reasoning_effort should win; got %+v", mr.OutputConfig)
	}
}

func TestOffDisablesThinkingOnDefaultOnModel(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Model:           "claude-sonnet-5",
		ReasoningEffort: "off",
		Messages:        []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)
	// Sonnet 5 thinks by default -> "off" must send an explicit disable.
	if mr.Thinking == nil || mr.Thinking.Type != "disabled" {
		t.Errorf("off on a default-on model should send thinking disabled; got %+v", mr.Thinking)
	}
	if mr.OutputConfig != nil {
		t.Errorf("off must not send an effort; got %+v", mr.OutputConfig)
	}
}

func TestOffIgnoredOnAlwaysOnModel(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Model:           "claude-fable-5",
		ReasoningEffort: "off",
		Messages:        []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)
	// Fable rejects thinking.type=disabled -> send nothing.
	if mr.Thinking != nil {
		t.Errorf("off on an always-on model must omit the thinking config; got %+v", mr.Thinking)
	}
}

func TestOffOnRegularModelSendsNothing(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Model:           "claude-opus-4-8",
		ReasoningEffort: "off",
		Messages:        []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)
	// Opus doesn't think unless asked -> no config needed to stay off.
	if mr.Thinking != nil || mr.OutputConfig != nil {
		t.Errorf("off on an off-by-default model should send nothing; got %+v %+v", mr.Thinking, mr.OutputConfig)
	}
}

func TestWebSearchToolAddedWhenEnabled(t *testing.T) {
	cfg := testCfg()
	cfg.EnableWebSearch = true
	req := openai.ChatCompletionRequest{Messages: []openai.ChatMessage{{Role: "user", Content: "hi"}}}
	mr := BuildMessagesRequest(req, cfg)
	if len(mr.Tools) != 1 || mr.Tools[0].Name != "web_search" {
		t.Errorf("web_search tool not added; got %+v", mr.Tools)
	}
}
