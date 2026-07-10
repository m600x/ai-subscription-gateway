package translate

import (
	"testing"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/config"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/openai"
)

func testCfg() *config.Config {
	return &config.Config{
		SpoofSystemPrompt: "You are Claude Code, Anthropic's official CLI for Claude.",
		DefaultModel:      "claude-sonnet-5",
		DefaultMaxTokens:  8192,
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

func TestWebSearchToolAddedWhenEnabled(t *testing.T) {
	cfg := testCfg()
	cfg.EnableWebSearch = true
	req := openai.ChatCompletionRequest{Messages: []openai.ChatMessage{{Role: "user", Content: "hi"}}}
	mr := BuildMessagesRequest(req, cfg)
	if len(mr.Tools) != 1 || mr.Tools[0].Name != "web_search" {
		t.Errorf("web_search tool not added; got %+v", mr.Tools)
	}
}
