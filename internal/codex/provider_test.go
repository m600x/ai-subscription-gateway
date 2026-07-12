package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/config"
	"github.com/m600x/ai-subscription-gateway/internal/openai"
)

const responsesSSE = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi there\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n"

func futureAccessToken() string {
	return makeJWT(map[string]any{
		"exp":                         time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_1"},
	})
}

// staticCfg uses a long-lived access token so no refresh is attempted.
func staticCfg(baseURL string) *config.Config {
	return &config.Config{
		OpenAIBaseURL:     baseURL,
		OpenAIAuthIssuer:  baseURL,
		OpenAIClientID:    "cid",
		OpenAIAccessToken: futureAccessToken(),
		OpenAIAccountID:   "acc_1",
		OpenAIOriginator:  "codex_cli_rs",
		RequestTimeout:    5 * time.Second,
	}
}

func TestProviderCompleteAggregates(t *testing.T) {
	var gotAuth, gotAccount, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		gotBeta = r.Header.Get("OpenAI-Beta")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, responsesSSE)
	}))
	defer srv.Close()

	p := NewProvider(staticCfg(srv.URL))
	cc, err := p.Complete(context.Background(), openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi", Parts: []openai.ContentPart{{Type: "text", Text: "hi"}}}}},
	}, codexModel())
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Choices) != 1 || cc.Choices[0].Message.Content != "Hi there" {
		t.Errorf("completion = %+v", cc)
	}
	if cc.Usage == nil || cc.Usage.TotalTokens != 10 {
		t.Errorf("usage = %+v", cc.Usage)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccount != "acc_1" {
		t.Errorf("account header = %q", gotAccount)
	}
	if gotBeta != "responses=experimental" {
		t.Errorf("OpenAI-Beta = %q", gotBeta)
	}
}

func TestProviderStreamEmitsChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, responsesSSE)
	}))
	defer srv.Close()

	p := NewProvider(staticCfg(srv.URL))
	var cap capture
	err := p.Stream(context.Background(), openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi", Parts: []openai.ContentPart{{Type: "text", Text: "hi"}}}}},
	}, codexModel(), &cap)
	if err != nil {
		t.Fatal(err)
	}
	var content string
	for _, c := range cap.chunks {
		if c.Choices[0].Delta != nil {
			content += c.Choices[0].Delta.Content
		}
	}
	if content != "Hi there" {
		t.Errorf("streamed content = %q", content)
	}
}

func TestClient401ForcesRefreshAndRetries(t *testing.T) {
	var responsesCalls, tokenCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			atomic.AddInt32(&tokenCalls, 1)
			io.WriteString(w, `{"access_token":"`+futureAccessToken()+`","id_token":"`+makeJWT(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_1"}})+`","refresh_token":"rt2","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, "/responses"):
			if atomic.AddInt32(&responsesCalls, 1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":{"type":"auth","message":"expired"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, responsesSSE)
		default:
			http.Error(w, "nope", 404)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		OpenAIBaseURL:      srv.URL,
		OpenAIAuthIssuer:   srv.URL,
		OpenAIClientID:     "cid",
		OpenAIRefreshToken: "rt1",
		OpenAIAccessToken:  futureAccessToken(), // valid, so first call is not preemptively refreshed
		OpenAIAccountID:    "acc_1",
		RequestTimeout:     5 * time.Second,
	}
	p := NewProvider(cfg)
	cc, err := p.Complete(context.Background(), openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi", Parts: []openai.ContentPart{{Type: "text", Text: "hi"}}}}},
	}, codexModel())
	if err != nil {
		t.Fatalf("Complete after 401 retry: %v", err)
	}
	if cc.Choices[0].Message.Content != "Hi there" {
		t.Errorf("content = %q", cc.Choices[0].Message.Content)
	}
	if atomic.LoadInt32(&responsesCalls) != 2 {
		t.Errorf("responses calls = %d, want 2 (401 then retry)", responsesCalls)
	}
	if atomic.LoadInt32(&tokenCalls) != 1 {
		t.Errorf("token refresh calls = %d, want 1", tokenCalls)
	}
}

func TestPrimeRefreshFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	defer srv.Close()

	cfg := &config.Config{
		OpenAIBaseURL:      srv.URL,
		OpenAIAuthIssuer:   srv.URL,
		OpenAIClientID:     "cid",
		OpenAIRefreshToken: "bad",
		RequestTimeout:     5 * time.Second,
	}
	p := NewProvider(cfg)
	if err := p.Prime(context.Background()); err == nil {
		t.Fatal("expected Prime to fail with a bad refresh token")
	}
}
