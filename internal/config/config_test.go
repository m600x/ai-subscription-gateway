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
	if c.ModelsConfigPath != "models.json" {
		t.Errorf("ModelsConfigPath default = %q", c.ModelsConfigPath)
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
	if !c.AnthropicEnabled() || c.OpenAIEnabled() {
		t.Errorf("expected anthropic-only enablement; anthropic=%v openai=%v", c.AnthropicEnabled(), c.OpenAIEnabled())
	}
	if c.OpenAIBaseURL != "https://chatgpt.com/backend-api/codex" {
		t.Errorf("OpenAIBaseURL default = %q", c.OpenAIBaseURL)
	}
}

func TestProviderEnablement(t *testing.T) {
	cases := []struct {
		name          string
		anthropicTok  string
		openaiRefresh string
		wantErr       bool
		wantAnthropic bool
		wantOpenAI    bool
	}{
		{name: "neither", wantErr: true},
		{name: "anthropic only", anthropicTok: "tok", wantAnthropic: true},
		{name: "openai only", openaiRefresh: "rt", wantOpenAI: true},
		{name: "both", anthropicTok: "tok", openaiRefresh: "rt", wantAnthropic: true, wantOpenAI: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLIENT_API_KEY", "ck")
			t.Setenv("ANTHROPIC_OAUTH_TOKEN", tc.anthropicTok)
			t.Setenv("OPENAI_REFRESH_TOKEN", tc.openaiRefresh)

			c, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error when no provider is configured")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.AnthropicEnabled() != tc.wantAnthropic {
				t.Errorf("AnthropicEnabled = %v, want %v", c.AnthropicEnabled(), tc.wantAnthropic)
			}
			if c.OpenAIEnabled() != tc.wantOpenAI {
				t.Errorf("OpenAIEnabled = %v, want %v", c.OpenAIEnabled(), tc.wantOpenAI)
			}
		})
	}
}

func TestLoadRequiresClientKey(t *testing.T) {
	t.Setenv("CLIENT_API_KEY", "")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "tok")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when CLIENT_API_KEY is missing")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("CLIENT_API_KEY", "ck")
	t.Setenv("OPENAI_REFRESH_TOKEN", "rt")
	t.Setenv("PORT", "9000")
	t.Setenv("ENABLE_WEB_SEARCH", "true")
	t.Setenv("ANTHROPIC_BASE_URL", "https://example.test/")
	t.Setenv("OPENAI_BASE_URL", "https://codex.test/")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 9000 {
		t.Errorf("Port = %d", c.Port)
	}
	if !c.EnableWebSearch {
		t.Error("EnableWebSearch not parsed")
	}
	if c.AnthropicBaseURL != "https://example.test" {
		t.Errorf("trailing slash not trimmed: %q", c.AnthropicBaseURL)
	}
	if c.OpenAIBaseURL != "https://codex.test" {
		t.Errorf("openai base trailing slash not trimmed: %q", c.OpenAIBaseURL)
	}
}
