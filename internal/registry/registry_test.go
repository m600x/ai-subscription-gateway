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

func TestParseRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"not json":      `{nope`,
		"no models key": `{"foo":1}`,
		"empty models":  `{"models":[]}`,
		"empty id":      `{"models":[{"id":"","provider":"openai"}]}`,
		"bad provider":  `{"models":[{"id":"x","provider":"aws"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Errorf("expected Parse to reject %s", name)
			}
		})
	}
}

func TestParseAcceptsMinimalCompleteModel(t *testing.T) {
	reg, err := Parse([]byte(`{"models":[{"id":"gpt-5.6-sol","provider":"openai","reasoning":{"efforts":["low","high"],"default":"low"}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if reg.Len() != 1 {
		t.Fatalf("len = %d", reg.Len())
	}
	if m, ok := reg.Lookup("gpt-5.6-sol"); !ok || m.UpstreamID != "gpt-5.6-sol" {
		t.Errorf("lookup/upstream default failed: %+v ok=%v", m, ok)
	}
}

func TestResolvePrefersInlineWhenValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	_ = os.WriteFile(path, []byte(sample), 0o600) // file has sonnet + gpt-5-codex
	inline := `{"models":[{"id":"only-inline","provider":"openai","reasoning":{"efforts":["low"],"default":"low"}}]}`

	reg, src, err := Resolve(inline, path)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src.Name != "MODELS env" || src.Warning != nil {
		t.Errorf("source = %+v, want MODELS env, no warning", src)
	}
	if _, ok := reg.Lookup("only-inline"); !ok {
		t.Error("inline registry not used")
	}
	if _, ok := reg.Lookup("claude-sonnet-5"); ok {
		t.Error("file registry should have been ignored")
	}
}

func TestResolveFallsBackOnInvalidInline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	_ = os.WriteFile(path, []byte(sample), 0o600)

	reg, src, err := Resolve(`{"models":[{"id":"x","provider":"aws"}]}`, path)
	if err != nil {
		t.Fatalf("Resolve should fall back, not fail: %v", err)
	}
	if src.Name != path {
		t.Errorf("source = %q, want the file path (fallback)", src.Name)
	}
	if src.Warning == nil {
		t.Error("expected a warning explaining why inline was ignored")
	}
	if _, ok := reg.Lookup("claude-sonnet-5"); !ok {
		t.Error("should have fallen back to the file registry")
	}
}

func TestResolveEmptyInlineUsesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	_ = os.WriteFile(path, []byte(sample), 0o600)
	reg, src, err := Resolve("   ", path)
	if err != nil {
		t.Fatal(err)
	}
	if src.Name != path || src.Warning != nil {
		t.Errorf("blank inline should quietly use the file; got %+v", src)
	}
	if reg.Len() != 2 {
		t.Errorf("len = %d, want 2", reg.Len())
	}
}
