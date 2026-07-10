package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/anthropic"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/config"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/openai"
)

const spoof = "You are Claude Code, Anthropic's official CLI for Claude."

type captured struct {
	mu     sync.Mutex
	system []string
}

// mockUpstream fakes the Anthropic Messages API. It records the system blocks
// it received and returns either JSON or SSE depending on the stream flag.
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

func newTestServer(upstreamURL string) *httptest.Server {
	cfg := &config.Config{
		ClientAPIKey:      "ck",
		OAuthToken:        "tok",
		AnthropicBaseURL:  upstreamURL,
		AnthropicVersion:  "2023-06-01",
		SpoofSystemPrompt: spoof,
		Models:            []string{"claude-sonnet-5", "claude-opus-4-8"},
		DefaultModel:      "claude-sonnet-5",
		DefaultMaxTokens:  1024,
		RequestTimeout:    5 * time.Second,
	}
	return httptest.NewServer(New(cfg, anthropic.New(cfg)))
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
	ts := newTestServer(up.URL)
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

func TestModelsAuth(t *testing.T) {
	up := mockUpstream(&captured{})
	defer up.Close()
	ts := newTestServer(up.URL)
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

	// With key -> list.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer ck")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("models status = %d", resp2.StatusCode)
	}
	var list openai.ModelList
	json.NewDecoder(resp2.Body).Decode(&list)
	if len(list.Data) != 2 || list.Data[0].ID != "claude-sonnet-5" {
		t.Errorf("models = %+v", list.Data)
	}
}

func TestChatNonStreamAndSpoof(t *testing.T) {
	cap := &captured{}
	up := mockUpstream(cap)
	defer up.Close()
	ts := newTestServer(up.URL)
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

	// The upstream must have received the exact spoof as the first system block,
	// with the user's system prompt appended second.
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
	ts := newTestServer(up.URL)
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
