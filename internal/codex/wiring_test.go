package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/m600x/ai-subscription-gateway/internal/config"
	"github.com/m600x/ai-subscription-gateway/internal/openai"
	"github.com/m600x/ai-subscription-gateway/internal/provider"
)

// TestProviderUseRefreshTokensAndPersist exercises the non-stateless wiring:
// UseRefreshTokens overrides the seed token and SetPersist receives the rotated
// token after Prime refreshes.
func TestProviderUseRefreshTokensAndPersist(t *testing.T) {
	srv := oauthServer(map[string]bool{"persisted-primary": true}, "rotated-1")
	defer srv.Close()

	cfg := &config.Config{OpenAIAuthIssuer: srv.URL, OpenAIClientID: "cid"}
	p := NewProvider(cfg)

	var persisted string
	p.UseRefreshTokens("persisted-primary", "env-fallback")
	p.SetPersist(func(r string) { persisted = r })

	if err := p.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if persisted != "rotated-1" {
		t.Errorf("persist got %q, want rotated-1", persisted)
	}
}

func TestProviderSurfacesUpstreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"unknown model"}}`)
			return
		}
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewProvider(staticCfg(srv.URL)) // static token -> no refresh needed
	_, err := p.Complete(context.Background(), openai.ChatCompletionRequest{
		Messages: []openai.ChatMessage{{Role: "user", Content: openai.Content{Text: "hi", Parts: []openai.ContentPart{{Type: "text", Text: "hi"}}}}},
	}, codexModel())
	if err == nil {
		t.Fatal("expected an upstream error")
	}
	var he provider.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("error should implement provider.HTTPError; got %T", err)
	}
	if he.HTTPStatus() != 400 {
		t.Errorf("status = %d, want 400", he.HTTPStatus())
	}
	if he.ErrType() != "invalid_request_error" || !strings.Contains(he.Error(), "unknown model") {
		t.Errorf("error = %q / type %q", he.Error(), he.ErrType())
	}
}

func TestParseErrorFallbacks(t *testing.T) {
	// error object shape
	e := parseError(400, []byte(`{"error":{"type":"bad","message":"m1"}}`))
	if e.Status != 400 || e.Type != "bad" || e.Message != "m1" {
		t.Errorf("structured = %+v", e)
	}
	// bare detail, no error object -> falls back to detail, default type
	e = parseError(503, []byte(`{"detail":"overloaded"}`))
	if e.Message != "overloaded" || e.Type != "upstream_error" {
		t.Errorf("detail fallback = %+v", e)
	}
	// non-JSON body -> raw text preserved
	e = parseError(500, []byte(`gateway boom`))
	if !strings.Contains(e.Message, "gateway boom") {
		t.Errorf("raw fallback = %+v", e)
	}
}
