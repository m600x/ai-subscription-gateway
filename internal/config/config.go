// Package config loads and validates runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings. Secrets (ClientAPIKey, OAuthToken) come
// from the environment; everything else has a sane default.
type Config struct {
	Port              int
	ClientAPIKey      string
	OAuthToken        string
	AnthropicBaseURL  string
	AnthropicVersion  string
	AnthropicBeta     string
	SpoofSystemPrompt string
	UserAgent         string
	Models            []string
	DefaultModel      string
	DefaultMaxTokens  int
	EnableWebSearch   bool
	MaxThinkingTokens int
	// ThinkingModels are the base models that accept an explicit thinking
	// budget. They get a "-thinking" alias in /v1/models (OpenRouter-style
	// variant id) and honor reasoning_effort. Models not listed here (e.g.
	// Fable, whose thinking is always silent server-side) are advertised
	// as-is.
	ThinkingModels     []string
	ThinkingBudgetLow  int
	ThinkingBudgetMed  int
	ThinkingBudgetHigh int
	RequestTimeout     time.Duration
	MaxRetries         int
	LogLevel           string
}

// Load reads configuration from environment variables and validates it.
func Load() (*Config, error) {
	c := &Config{
		Port:              envInt("PORT", 8000),
		ClientAPIKey:      os.Getenv("CLIENT_API_KEY"),
		OAuthToken:        os.Getenv("ANTHROPIC_OAUTH_TOKEN"),
		AnthropicBaseURL:  strings.TrimRight(envStr("ANTHROPIC_BASE_URL", "https://api.anthropic.com"), "/"),
		AnthropicVersion:  envStr("ANTHROPIC_VERSION", "2023-06-01"),
		AnthropicBeta:     envStr("ANTHROPIC_BETA", "oauth-2025-04-20"),
		SpoofSystemPrompt: envStr("SPOOF_SYSTEM_PROMPT", "You are Claude Code, Anthropic's official CLI for Claude."),
		UserAgent:         envStr("USER_AGENT", "claude-cli/1.0.0 (external, cli)"),
		Models:            envList("MODELS", []string{"claude-fable-5", "claude-opus-4-8", "claude-sonnet-5"}),
		DefaultModel:      envStr("DEFAULT_MODEL", "claude-sonnet-5"),
		DefaultMaxTokens:  envInt("DEFAULT_MAX_TOKENS", 8192),
		EnableWebSearch:   envBool("ENABLE_WEB_SEARCH", false),
		MaxThinkingTokens: envInt("MAX_THINKING_TOKENS", 0),
		ThinkingModels: envList("THINKING_MODELS",
			[]string{"claude-opus-4-8", "claude-sonnet-5"}),
		ThinkingBudgetLow:  envInt("THINKING_BUDGET_LOW", 2048),
		ThinkingBudgetMed:  envInt("THINKING_BUDGET_MEDIUM", 8192),
		ThinkingBudgetHigh: envInt("THINKING_BUDGET_HIGH", 16384),
		RequestTimeout:     time.Duration(envInt("REQUEST_TIMEOUT_SECONDS", 600)) * time.Second,
		MaxRetries:         envInt("MAX_RETRIES", 2),
		LogLevel:           envStr("LOG_LEVEL", "info"),
	}

	if c.ClientAPIKey == "" {
		return nil, fmt.Errorf("CLIENT_API_KEY is required")
	}
	if c.OAuthToken == "" {
		return nil, fmt.Errorf("ANTHROPIC_OAUTH_TOKEN is required")
	}
	if c.SpoofSystemPrompt == "" {
		return nil, fmt.Errorf("SPOOF_SYSTEM_PROMPT must not be empty (it is the auth gate)")
	}
	if c.DefaultModel == "" && len(c.Models) > 0 {
		c.DefaultModel = c.Models[0]
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	return c, nil
}

// IsThinkingModel reports whether the base model accepts an explicit thinking
// budget (and therefore gets a -thinking alias and honors reasoning_effort).
func (c *Config) IsThinkingModel(model string) bool {
	for _, m := range c.ThinkingModels {
		if m == model {
			return true
		}
	}
	return false
}

// AdvertisedModels returns the /v1/models list: every configured model, plus
// a "-thinking" alias for each thinking-capable one.
func (c *Config) AdvertisedModels() []string {
	out := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		out = append(out, m)
		if c.IsThinkingModel(m) {
			out = append(out, m+"-thinking")
		}
	}
	return out
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

func envList(key string, def []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}
