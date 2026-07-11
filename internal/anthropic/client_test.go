package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m600x/ai-substation/internal/config"
)

func testConfig(url string) *config.Config {
	return &config.Config{
		OAuthToken:       "tok",
		AnthropicBaseURL: url,
		AnthropicVersion: "2023-06-01",
		AnthropicBeta:    "oauth-2025-04-20",
		UserAgent:        "claude-cli/1.0.0 (external, cli)",
		RequestTimeout:   5 * time.Second,
		MaxRetries:       2,
	}
}

func TestCreateMessageSendsHeadersAndParses(t *testing.T) {
	var auth, ver, beta, ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		ver = r.Header.Get("anthropic-version")
		beta = r.Header.Get("anthropic-beta")
		ua = r.Header.Get("User-Agent")
		io.WriteString(w, `{"id":"m","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer srv.Close()

	c := New(testConfig(srv.URL))
	resp, err := c.CreateMessage(context.Background(), MessagesRequest{
		Model: "claude-sonnet-5", MaxTokens: 16,
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q", auth)
	}
	if ver != "2023-06-01" {
		t.Errorf("anthropic-version = %q", ver)
	}
	if beta != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q", beta)
	}
	if ua != "claude-cli/1.0.0 (external, cli)" {
		t.Errorf("User-Agent = %q", ua)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "hello" {
		t.Errorf("content = %+v", resp.Content)
	}
	if resp.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestCreateMessageTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"type":"authentication_error","message":"Invalid bearer token"}}`)
	}))
	defer srv.Close()

	c := New(testConfig(srv.URL))
	_, err := c.CreateMessage(context.Background(), MessagesRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *Error
	if !errors.As(err, &ae) {
		t.Fatalf("want *Error, got %T", err)
	}
	if ae.Status != 401 || ae.Type != "authentication_error" {
		t.Errorf("got %+v", ae)
	}
}

func TestRetryOn500ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"type":"api_error","message":"boom"}}`)
			return
		}
		io.WriteString(w, `{"id":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxRetries = 3
	c := New(cfg)
	resp, err := c.CreateMessage(context.Background(), MessagesRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text != "ok" {
		t.Errorf("content = %+v", resp.Content)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("upstream calls = %d, want 3 (2 retries)", got)
	}
}

func TestStreamMessageReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	c := New(testConfig(srv.URL))
	body, err := c.StreamMessage(context.Background(), MessagesRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	b, _ := io.ReadAll(body)
	if !strings.Contains(string(b), "message_stop") {
		t.Errorf("stream body = %q", string(b))
	}
}
