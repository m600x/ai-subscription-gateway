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
