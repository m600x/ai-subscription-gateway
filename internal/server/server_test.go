package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/anthropic"
	"github.com/m600x/ai-subscription-gateway/internal/config"
	"github.com/m600x/ai-subscription-gateway/internal/openai"
	"github.com/m600x/ai-subscription-gateway/internal/provider"
	"github.com/m600x/ai-subscription-gateway/internal/registry"
)

const spoof = "You are Claude Code, Anthropic's official CLI for Claude."

const testModelsJSON = `{
  "models": [
    {"id":"claude-sonnet-5","provider":"anthropic","upstream_id":"claude-sonnet-5",
     "reasoning":{"efforts":["off","low","medium","high","xhigh","max"],"default":"high","mode":"default-on"},
     "pricing":{"currency":"USD","unit":"per_million_tokens","input":3.0,"output":15.0,"cache_read":0.3,"cache_write":3.75},
     "context_window":1000000,"default_max_tokens":8192},
    {"id":"claude-opus-4-8","provider":"anthropic","upstream_id":"claude-opus-4-8",
     "reasoning":{"efforts":["off","low","medium","high"],"default":"high","mode":"opt-in"},"default_max_tokens":8192},
    {"id":"gpt-5-codex","provider":"openai","upstream_id":"gpt-5-codex",
     "reasoning":{"efforts":["low","medium","high"],"default":"medium"}}
  ]
}`

type captured struct {
	mu     sync.Mutex
	system []string
}

// mockUpstream fakes the Anthropic Messages API.
func mockUpstream(cap *captured) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Stream bool `json:"stream"`
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		_ = json.Unmarshal(raw, &body)
		cap.mu.Lock()
		cap.system = nil
		for _, s := range body.System {
			cap.system = append(cap.system, s.Text)
		}
		cap.mu.Unlock()

		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n")
			io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n")
			io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
			io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
			return
		}
		io.WriteString(w, `{"id":"m","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"Hi there"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`)
	}))
}

func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(testModelsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// newTestServer wires an anthropic-only server (OpenAI provider disabled)
// pointed at the mock upstream.
func newTestServer(t *testing.T, upstreamURL string) *httptest.Server {
	cfg := &config.Config{
		ClientAPIKey:      "ck",
		OAuthToken:        "tok",
		AnthropicBaseURL:  upstreamURL,
		AnthropicVersion:  "2023-06-01",
		SpoofSystemPrompt: spoof,
		DefaultModel:      "claude-sonnet-5",
		DefaultMaxTokens:  1024,
		RequestTimeout:    5 * time.Second,
	}
	reg := testRegistry(t)
	providers := map[string]provider.Provider{registry.ProviderAnthropic: anthropic.NewProvider(cfg)}
	enabled := map[string]bool{registry.ProviderAnthropic: true, registry.ProviderOpenAI: false}
	return httptest.NewServer(New(cfg, reg, providers, enabled))
}

func post(t *testing.T, url, key, bodyJSON string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(bodyJSON))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHealthNoAuth(t *testing.T) {
	up := mockUpstream(&captured{})
	defer up.Close()
	ts := newTestServer(t, up.URL)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
}

func TestModelsListsOnlyEnabledProviders(t *testing.T) {
	up := mockUpstream(&captured{})
	defer up.Close()
	ts := newTestServer(t, up.URL)
	defer ts.Close()

	// No key -> 401.
	resp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unauth models status = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer ck")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var list openai.ModelList
	json.NewDecoder(resp2.Body).Decode(&list)
	// OpenAI is disabled, so gpt-5-codex must not appear.
	if len(list.Data) != 2 || list.Data[0].ID != "claude-sonnet-5" || list.Data[1].ID != "claude-opus-4-8" {
		t.Errorf("models = %+v, want the two anthropic models only", list.Data)
	}
	for _, m := range list.Data {
		if m.OwnedBy != "anthropic" {
			t.Errorf("model %q owned_by = %q", m.ID, m.OwnedBy)
		}
	}

	// The reasoning ladder from the registry is exposed as a vendor extension.
	sonnet := list.Data[0]
	if sonnet.Reasoning == nil {
		t.Fatalf("model %q has no reasoning block", sonnet.ID)
	}
	if got := strings.Join(sonnet.Reasoning.Efforts, ","); got != "off,low,medium,high,xhigh,max" {
		t.Errorf("%q efforts = %q", sonnet.ID, got)
	}
	if sonnet.Reasoning.Default != "high" || sonnet.Reasoning.Mode != "default-on" {
		t.Errorf("%q reasoning = %+v, want default high mode default-on", sonnet.ID, sonnet.Reasoning)
	}
	opus := list.Data[1]
	if opus.Reasoning == nil || opus.Reasoning.Mode != "opt-in" {
		t.Errorf("%q reasoning = %+v, want mode opt-in", opus.ID, opus.Reasoning)
	}

	// Pricing is exposed as a vendor extension when the registry declares it.
	if p := sonnet.Pricing; p == nil {
		t.Fatalf("model %q has no pricing block", sonnet.ID)
	} else if p.Currency != "USD" || p.Unit != "per_million_tokens" ||
		p.Input != 3.0 || p.Output != 15.0 || p.CacheRead != 0.3 || p.CacheWrite != 3.75 {
		t.Errorf("%q pricing = %+v", sonnet.ID, p)
	}
	if opus.Pricing != nil {
		t.Errorf("%q pricing = %+v, want none (not declared in registry)", opus.ID, opus.Pricing)
	}

	// context_window is exposed as a vendor extension when the registry
	// declares it, and omitted (zero) otherwise.
	if sonnet.ContextWindow != 1000000 {
		t.Errorf("%q context_window = %d, want 1000000", sonnet.ID, sonnet.ContextWindow)
	}
	if opus.ContextWindow != 0 {
		t.Errorf("%q context_window = %d, want 0 (not declared in registry)", opus.ID, opus.ContextWindow)
	}
}

func TestChatNonStreamAndSpoof(t *testing.T) {
	cap := &captured{}
	up := mockUpstream(cap)
	defer up.Close()
	ts := newTestServer(t, up.URL)
	defer ts.Close()

	// Wrong key -> 401.
	bad := post(t, ts.URL+"/v1/chat/completions", "nope", `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`)
	bad.Body.Close()
	if bad.StatusCode != 401 {
		t.Fatalf("wrong-key status = %d, want 401", bad.StatusCode)
	}

	resp := post(t, ts.URL+"/v1/chat/completions", "ck",
		`{"model":"claude-sonnet-5","messages":[{"role":"system","content":"be a pirate"},{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("chat status = %d", resp.StatusCode)
	}
	var cc openai.ChatCompletion
	if err := json.NewDecoder(resp.Body).Decode(&cc); err != nil {
		t.Fatal(err)
	}
	if cc.Object != "chat.completion" || len(cc.Choices) != 1 || cc.Choices[0].Message.Content != "Hi there" {
		t.Errorf("completion = %+v", cc)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.system) != 2 {
		t.Fatalf("upstream system blocks = %d, want 2 (%v)", len(cap.system), cap.system)
	}
	if cap.system[0] != spoof {
		t.Errorf("first system block = %q, want exact spoof", cap.system[0])
	}
	if cap.system[1] != "be a pirate" {
		t.Errorf("second system block = %q", cap.system[1])
	}
}

func TestChatStreaming(t *testing.T) {
	up := mockUpstream(&captured{})
	defer up.Close()
	ts := newTestServer(t, up.URL)
	defer ts.Close()

	resp := post(t, ts.URL+"/v1/chat/completions", "ck",
		`{"model":"claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := string(b)
	for _, want := range []string{
		`"object":"chat.completion.chunk"`,
		`"role":"assistant"`,
		`"content":"Hi"`,
		`"finish_reason":"stop"`,
		"data: [DONE]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestUnknownModelRejected(t *testing.T) {
	up := mockUpstream(&captured{})
	defer up.Close()
	ts := newTestServer(t, up.URL)
	defer ts.Close()

	resp := post(t, ts.URL+"/v1/chat/completions", "ck",
		`{"model":"does-not-exist","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("unknown model status = %d, want 400", resp.StatusCode)
	}
	var er openai.ErrorResponse
	json.NewDecoder(resp.Body).Decode(&er)
	if er.Error.Code != "model_not_found" {
		t.Errorf("error code = %q, want model_not_found", er.Error.Code)
	}
}

func TestDisabledProviderModelRejected(t *testing.T) {
	up := mockUpstream(&captured{})
	defer up.Close()
	ts := newTestServer(t, up.URL)
	defer ts.Close()

	// gpt-5-codex is in the registry but the OpenAI provider is disabled.
	resp := post(t, ts.URL+"/v1/chat/completions", "ck",
		`{"model":"gpt-5-codex","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("disabled-provider model status = %d, want 400", resp.StatusCode)
	}
	var er openai.ErrorResponse
	json.NewDecoder(resp.Body).Decode(&er)
	if er.Error.Code != "provider_not_configured" {
		t.Errorf("error code = %q, want provider_not_configured", er.Error.Code)
	}
}
