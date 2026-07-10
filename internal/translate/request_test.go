package translate

import (
	"testing"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/config"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/openai"
)

func testCfg() *config.Config {
	return &config.Config{
		SpoofSystemPrompt:  "You are Claude Code, Anthropic's official CLI for Claude.",
		DefaultModel:       "claude-sonnet-5",
		DefaultMaxTokens:   8192,
		ThinkingModels:     []string{"claude-opus-4-8", "claude-sonnet-5"},
		ThinkingBudgetLow:  2048,
		ThinkingBudgetMed:  8192,
		ThinkingBudgetHigh: 16384,
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

func TestThinkingVariantSetsBudgetAndDropsSampling(t *testing.T) {
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
	if mr.Thinking == nil || mr.Thinking.BudgetTokens != cfg.ThinkingBudgetMed {
		t.Errorf("thinking budget = %+v, want %d", mr.Thinking, cfg.ThinkingBudgetMed)
	}
	if mr.Temperature != nil {
		t.Error("temperature must be dropped when extended thinking is enabled")
	}
	if mr.MaxTokens <= mr.Thinking.BudgetTokens {
		t.Errorf("max_tokens (%d) must exceed thinking budget (%d)", mr.MaxTokens, mr.Thinking.BudgetTokens)
	}
}

func TestReasoningEffortOverridesVariant(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Model:           "claude-sonnet-5-thinking",
		ReasoningEffort: "low",
		Messages:        []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)
	if mr.Thinking == nil || mr.Thinking.BudgetTokens != cfg.ThinkingBudgetLow {
		t.Errorf("explicit reasoning_effort should win; got %+v", mr.Thinking)
	}
}

func TestReasoningEffortOnPlainThinkingModel(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Model:           "claude-sonnet-5",
		ReasoningEffort: "high",
		Messages:        []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)
	if mr.Thinking == nil || mr.Thinking.BudgetTokens != cfg.ThinkingBudgetHigh {
		t.Errorf("reasoning_effort must apply to a plain thinking-capable model; got %+v", mr.Thinking)
	}
}

func TestNonThinkingModelIgnoresThinkingSignals(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Model:           "claude-fable-5-thinking",
		ReasoningEffort: "high",
		Messages:        []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)
	if mr.Model != "claude-fable-5" {
		t.Errorf("suffix should still be stripped; got %q", mr.Model)
	}
	if mr.Thinking != nil {
		t.Errorf("fable is not thinking-capable; thinking must stay unset, got %+v", mr.Thinking)
	}
}

func TestMinimalEffortDisablesThinking(t *testing.T) {
	cfg := testCfg()
	req := openai.ChatCompletionRequest{
		Model:           "claude-sonnet-5-thinking",
		ReasoningEffort: "minimal",
		Messages:        []openai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	mr := BuildMessagesRequest(req, cfg)
	if mr.Thinking != nil {
		t.Errorf("minimal effort should disable thinking even on a -thinking alias; got %+v", mr.Thinking)
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
