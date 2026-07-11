package registry

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `{
  "models": [
    {"id":"claude-sonnet-5","provider":"anthropic","upstream_id":"claude-sonnet-5",
     "reasoning":{"efforts":["off","low","high"],"default":"high","mode":"default-on"},"default_max_tokens":8192},
    {"id":"gpt-5-codex","provider":"openai","upstream_id":"gpt-5-codex","aliases":["gpt5-codex"],
     "reasoning":{"efforts":["low","medium","high"],"default":"medium"}}
  ]
}`

func load(t *testing.T, body string) *Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return reg
}

func TestLookupByIDAndAlias(t *testing.T) {
	reg := load(t, sample)

	if m, ok := reg.Lookup("claude-sonnet-5"); !ok || m.Provider != ProviderAnthropic {
		t.Errorf("lookup sonnet: ok=%v m=%+v", ok, m)
	}
	// Alias, case-insensitive.
	if m, ok := reg.Lookup("GPT5-CODEX"); !ok || m.ID != "gpt-5-codex" {
		t.Errorf("alias lookup failed: ok=%v m=%+v", ok, m)
	}
	if _, ok := reg.Lookup("nope"); ok {
		t.Error("unknown model should not resolve")
	}
}

func TestAllowsEffort(t *testing.T) {
	reg := load(t, sample)
	m, _ := reg.Lookup("gpt-5-codex")
	if !m.AllowsEffort("HIGH") {
		t.Error("high should be allowed (case-insensitive)")
	}
	if m.AllowsEffort("max") {
		t.Error("max is not in the gpt-5-codex ladder")
	}
}

func TestPublicFiltersByEnabled(t *testing.T) {
	reg := load(t, sample)

	only := reg.Public(map[string]bool{ProviderAnthropic: true, ProviderOpenAI: false})
	if len(only) != 1 || only[0].ID != "claude-sonnet-5" {
		t.Errorf("anthropic-only public = %+v", only)
	}
	both := reg.Public(map[string]bool{ProviderAnthropic: true, ProviderOpenAI: true})
	if len(both) != 2 {
		t.Errorf("both public len = %d, want 2", len(both))
	}
}

func TestFirstEnabled(t *testing.T) {
	reg := load(t, sample)
	m, ok := reg.First(map[string]bool{ProviderOpenAI: true})
	if !ok || m.ID != "gpt-5-codex" {
		t.Errorf("First(openai) = %+v ok=%v", m, ok)
	}
	if _, ok := reg.First(map[string]bool{}); ok {
		t.Error("First with nothing enabled should be false")
	}
}

func TestLoadRejectsBadProvider(t *testing.T) {
	bad := `{"models":[{"id":"x","provider":"aws","reasoning":{"efforts":["low"],"default":"low"}}]}`
	path := filepath.Join(t.TempDir(), "m.json")
	_ = os.WriteFile(path, []byte(bad), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("expected error for unknown provider")
	}
}
