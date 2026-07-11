// Package config loads and validates runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/m600x/ai-substation/internal/registry"
)

// Config holds all runtime settings. Secrets (ClientAPIKey, the provider
// credentials) come from the environment; everything else has a sane default.
//
// The wrapper fronts one or both subscription backends. A provider is enabled
// only when its credentials are present; if neither is, startup fails. The
// advertised models and their reasoning efforts live in the models config
// (models.json), not here -- see the registry package.
type Config struct {
	Port         int
	ClientAPIKey string

	// ModelsConfigPath is the path to models.json (the model registry).
	ModelsConfigPath string
	// DefaultModel is used when a request omits the model. Empty means "first
	// model in the registry whose provider is enabled".
	DefaultModel     string
	DefaultMaxTokens int

	// --- Anthropic (Claude subscription) ---
	OAuthToken        string
	AnthropicBaseURL  string
	AnthropicVersion  string
	AnthropicBeta     string
	SpoofSystemPrompt string
	UserAgent         string
	EnableWebSearch   bool
	// ThinkingDisplay is the thinking.display mode. "summarized" streams
	// readable thinking_delta events (surfaced as reasoning_content);
	// "omitted" returns empty thinking blocks.
	ThinkingDisplay string

	// --- OpenAI (ChatGPT/Codex subscription) ---
	OpenAIRefreshToken     string
	OpenAIAccessToken      string
	OpenAIAccountID        string
	OpenAIBaseURL          string
	OpenAIAuthIssuer       string
	OpenAIClientID         string
	OpenAIOriginator       string
	OpenAIUserAgent        string
	OpenAIBaseInstructions string

	// --- shared ---
	RequestTimeout time.Duration
	MaxRetries     int
	LogLevel       string
}

// Default endpoints / identifiers.
const (
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	defaultOpenAIBaseURL    = "https://chatgpt.com/backend-api/codex"
	defaultOpenAIAuthIssuer = "https://auth.openai.com"
	// OpenAIDefaultClientID is the public OAuth client id used by the Codex CLI.
	OpenAIDefaultClientID   = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultOpenAIOriginator = "codex_cli_rs"
)

// Load reads configuration from environment variables and validates it.
func Load() (*Config, error) {
	c := &Config{
		Port:             envInt("PORT", 8000),
		ClientAPIKey:     os.Getenv("CLIENT_API_KEY"),
		ModelsConfigPath: envStr("MODELS_CONFIG", "models.json"),
		DefaultModel:     os.Getenv("DEFAULT_MODEL"),
		DefaultMaxTokens: envInt("DEFAULT_MAX_TOKENS", 8192),

		OAuthToken:        os.Getenv("ANTHROPIC_OAUTH_TOKEN"),
		AnthropicBaseURL:  strings.TrimRight(envStr("ANTHROPIC_BASE_URL", defaultAnthropicBaseURL), "/"),
		AnthropicVersion:  envStr("ANTHROPIC_VERSION", "2023-06-01"),
		AnthropicBeta:     envStr("ANTHROPIC_BETA", "oauth-2025-04-20"),
		SpoofSystemPrompt: envStr("SPOOF_SYSTEM_PROMPT", "You are Claude Code, Anthropic's official CLI for Claude."),
		UserAgent:         envStr("USER_AGENT", "claude-cli/1.0.0 (external, cli)"),
		EnableWebSearch:   envBool("ENABLE_WEB_SEARCH", false),
		ThinkingDisplay:   envStr("THINKING_DISPLAY", "summarized"),

		OpenAIRefreshToken:     os.Getenv("OPENAI_REFRESH_TOKEN"),
		OpenAIAccessToken:      os.Getenv("OPENAI_ACCESS_TOKEN"),
		OpenAIAccountID:        os.Getenv("OPENAI_ACCOUNT_ID"),
		OpenAIBaseURL:          strings.TrimRight(envStr("OPENAI_BASE_URL", defaultOpenAIBaseURL), "/"),
		OpenAIAuthIssuer:       strings.TrimRight(envStr("OPENAI_AUTH_ISSUER", defaultOpenAIAuthIssuer), "/"),
		OpenAIClientID:         envStr("OPENAI_CLIENT_ID", OpenAIDefaultClientID),
		OpenAIOriginator:       envStr("OPENAI_ORIGINATOR", defaultOpenAIOriginator),
		OpenAIUserAgent:        envStr("OPENAI_USER_AGENT", "codex_cli_rs/0.1.0 (external; wrapper)"),
		OpenAIBaseInstructions: os.Getenv("OPENAI_BASE_INSTRUCTIONS"),

		RequestTimeout: time.Duration(envInt("REQUEST_TIMEOUT_SECONDS", 600)) * time.Second,
		MaxRetries:     envInt("MAX_RETRIES", 2),
		LogLevel:       envStr("LOG_LEVEL", "info"),
	}

	if c.ClientAPIKey == "" {
		return nil, fmt.Errorf("CLIENT_API_KEY is required")
	}
	if !c.AnthropicEnabled() && !c.OpenAIEnabled() {
		return nil, fmt.Errorf("no provider configured: set ANTHROPIC_OAUTH_TOKEN and/or OPENAI_REFRESH_TOKEN")
	}
	if c.AnthropicEnabled() && c.SpoofSystemPrompt == "" {
		return nil, fmt.Errorf("SPOOF_SYSTEM_PROMPT must not be empty (it is the Anthropic auth gate)")
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	return c, nil
}

// AnthropicEnabled reports whether the Anthropic (Claude) provider is configured.
func (c *Config) AnthropicEnabled() bool { return c.OAuthToken != "" }

// OpenAIEnabled reports whether the OpenAI (Codex) provider is configured.
func (c *Config) OpenAIEnabled() bool {
	return c.OpenAIRefreshToken != "" || c.OpenAIAccessToken != ""
}

// EnabledProviders is the provider-enable map used by the registry and server.
func (c *Config) EnabledProviders() map[string]bool {
	return map[string]bool{
		registry.ProviderAnthropic: c.AnthropicEnabled(),
		registry.ProviderOpenAI:    c.OpenAIEnabled(),
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	return def
}
