package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("CLIENT_API_KEY", "ck")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "tok")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 8000 {
		t.Errorf("Port default = %d, want 8000", c.Port)
	}
	if c.DefaultModel != "claude-sonnet-5" {
		t.Errorf("DefaultModel = %q", c.DefaultModel)
	}
	if len(c.Models) != 3 {
		t.Errorf("Models default len = %d, want 3", len(c.Models))
	}
	if c.AnthropicBeta != "oauth-2025-04-20" {
		t.Errorf("AnthropicBeta = %q", c.AnthropicBeta)
	}
	if c.SpoofSystemPrompt == "" {
		t.Error("SpoofSystemPrompt must have a default")
	}
	if c.MaxRetries != 2 {
		t.Errorf("MaxRetries default = %d, want 2", c.MaxRetries)
	}
}

func TestThinkingModelRegistry(t *testing.T) {
	c := &Config{
		Models:         []string{"claude-fable-5", "claude-sonnet-5"},
		ThinkingModels: []string{"claude-sonnet-5"},
	}
	if !c.IsThinkingModel("claude-sonnet-5") {
		t.Error("sonnet must be thinking-capable")
	}
	if c.IsThinkingModel("claude-fable-5") {
		t.Error("fable is not in ThinkingModels here")
	}
}

func TestLoadRequiresSecrets(t *testing.T) {
	t.Setenv("CLIENT_API_KEY", "")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when required secrets are missing")
	}

	t.Setenv("CLIENT_API_KEY", "ck")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when ANTHROPIC_OAUTH_TOKEN is missing")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("CLIENT_API_KEY", "ck")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "tok")
	t.Setenv("PORT", "9000")
	t.Setenv("MODELS", "a, b ,c")
	t.Setenv("ENABLE_WEB_SEARCH", "true")
	t.Setenv("ANTHROPIC_BASE_URL", "https://example.test/")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 9000 {
		t.Errorf("Port = %d", c.Port)
	}
	if len(c.Models) != 3 || c.Models[1] != "b" {
		t.Errorf("Models = %#v (whitespace not trimmed?)", c.Models)
	}
	if !c.EnableWebSearch {
		t.Error("EnableWebSearch not parsed")
	}
	if c.AnthropicBaseURL != "https://example.test" {
		t.Errorf("trailing slash not trimmed: %q", c.AnthropicBaseURL)
	}
}
