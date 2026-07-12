package tokenstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMissingIsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := s.Tokens(); got.AnthropicOAuthToken != "" || got.OpenAIRefreshToken != "" {
		t.Errorf("missing file should yield empty tokens; got %+v", got)
	}
}

func TestSeedWritesAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := Open(path)
	if err := s.Seed("sk-ant-x", "rt-1"); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// File exists, 0600, valid JSON with both fields.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %v, want 0600", perm)
	}
	var on Tokens
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &on); err != nil {
		t.Fatalf("file is not valid JSON: %v", err)
	}
	if on.AnthropicOAuthToken != "sk-ant-x" || on.OpenAIRefreshToken != "rt-1" {
		t.Errorf("round-trip = %+v", on)
	}

	// Re-open reads the persisted values.
	s2, _ := Open(path)
	if s2.Tokens() != on {
		t.Errorf("re-open = %+v, want %+v", s2.Tokens(), on)
	}
}

func TestSeedEmptyLeavesFieldUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := Open(path)
	_ = s.Seed("sk-ant-x", "rt-1")
	// A later seed with an empty OpenAI value (e.g. env unset) must not wipe the
	// already-persisted, rotated token.
	_ = s.Seed("sk-ant-x", "")
	if got := s.Tokens().OpenAIRefreshToken; got != "rt-1" {
		t.Errorf("OpenAI refresh clobbered: %q", got)
	}
}

func TestResolveOpenAIFileWinsOverEnv(t *testing.T) {
	// The core scenario: the gateway was launched with an env token, rotated it
	// over its life (persisting the latest to the file), then restarted with the
	// same (now stale) env still set. The file token must win.
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := Open(path)
	_ = s.Seed("sk-ant", "rt-fresh-from-file")

	r := s.Resolve("sk-ant", "rt-stale-env")
	if r.OpenAIRefreshToken != "rt-fresh-from-file" {
		t.Errorf("OpenAIRefreshToken = %q, want the file token to win", r.OpenAIRefreshToken)
	}
	if r.OpenAIRefreshFallback != "rt-stale-env" {
		t.Errorf("fallback = %q, want the env token preserved as fallback", r.OpenAIRefreshFallback)
	}
}

func TestResolveOpenAIUsesEnvWhenFileEmpty(t *testing.T) {
	// First launch: no file yet -> env is used and becomes the fallback.
	s, _ := Open(filepath.Join(t.TempDir(), "tokens.json"))
	r := s.Resolve("", "rt-env")
	if r.OpenAIRefreshToken != "rt-env" || r.OpenAIRefreshFallback != "rt-env" {
		t.Errorf("resolved = %+v, want env token as primary and fallback", r)
	}
}

func TestResolveOpenAIFromFileWhenEnvUnset(t *testing.T) {
	// Restart where the env token was dropped entirely: the file still enables
	// OpenAI (non-empty primary), with no fallback.
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := Open(path)
	_ = s.Seed("", "rt-only-in-file")

	r := s.Resolve("", "")
	if r.OpenAIRefreshToken != "rt-only-in-file" {
		t.Errorf("OpenAIRefreshToken = %q, want the file token", r.OpenAIRefreshToken)
	}
	if r.OpenAIRefreshFallback != "" {
		t.Errorf("fallback = %q, want empty", r.OpenAIRefreshFallback)
	}
}

func TestResolveAnthropicEnvWinsFileBackfills(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := Open(path)
	_ = s.Seed("sk-ant-file", "rt")

	// Env set -> env wins (the user rotates the long-lived token via env).
	if got := s.Resolve("sk-ant-env", "rt").AnthropicOAuthToken; got != "sk-ant-env" {
		t.Errorf("anthropic = %q, want env to win", got)
	}
	// Env unset -> file backfills.
	if got := s.Resolve("", "rt").AnthropicOAuthToken; got != "sk-ant-file" {
		t.Errorf("anthropic = %q, want file backfill", got)
	}
}

func TestSetOpenAIRefreshPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := Open(path)
	_ = s.Seed("sk-ant-x", "rt-1")

	s.SetOpenAIRefresh("rt-2") // simulate a rotation
	if got := s.Tokens().OpenAIRefreshToken; got != "rt-2" {
		t.Errorf("in-memory = %q, want rt-2", got)
	}
	s2, _ := Open(path)
	if got := s2.Tokens().OpenAIRefreshToken; got != "rt-2" {
		t.Errorf("persisted = %q, want rt-2", got)
	}
	// Anthropic token preserved across the rotation write.
	if got := s2.Tokens().AnthropicOAuthToken; got != "sk-ant-x" {
		t.Errorf("anthropic token lost: %q", got)
	}
}
