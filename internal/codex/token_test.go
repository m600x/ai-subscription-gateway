package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"testing"

	"github.com/m600x/ai-subscription-gateway/internal/config"
)

// oauthServer returns an httptest server that mimics the OpenAI token endpoint.
// It accepts the refresh tokens in `good` and rotates each to newRefresh;
// anything else gets a 401.
func oauthServer(good map[string]bool, newRefresh string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		rt := r.FormValue("refresh_token")
		if !good[rt] {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		access := makeJWT(map[string]any{
			"exp":                         time.Now().Add(time.Hour).Unix(),
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_1"},
		})
		idtok := makeJWT(map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_1"},
		})
		io.WriteString(w, `{"access_token":"`+access+`","id_token":"`+idtok+`","refresh_token":"`+newRefresh+`","expires_in":3600}`)
	}))
}

func tmConfig(issuer string) *config.Config {
	return &config.Config{OpenAIAuthIssuer: issuer, OpenAIClientID: "cid"}
}

func TestPersistHookFiresWithRotatedToken(t *testing.T) {
	srv := oauthServer(map[string]bool{"rt-1": true}, "rt-rotated")
	defer srv.Close()

	cfg := tmConfig(srv.URL)
	cfg.OpenAIRefreshToken = "rt-1"
	tm := NewTokenManager(cfg, srv.Client())

	var persisted string
	tm.persist = func(r string) { persisted = r }

	if err := tm.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if persisted != "rt-rotated" {
		t.Errorf("persist got %q, want rt-rotated", persisted)
	}
}

func TestRefreshFallsBackToEnvToken(t *testing.T) {
	// The primary (persisted) token is stale; the fallback (env) still works.
	srv := oauthServer(map[string]bool{"env-good": true}, "env-rotated")
	defer srv.Close()

	cfg := tmConfig(srv.URL)
	tm := NewTokenManager(cfg, srv.Client())
	tm.refresh = "stale-persisted"
	tm.fallback = "env-good"

	access, account, err := tm.ForceRefresh(context.Background())
	if err != nil {
		t.Fatalf("ForceRefresh should succeed via fallback: %v", err)
	}
	if access == "" || account != "acc_1" {
		t.Errorf("access/account = %q/%q", access, account)
	}
}

func TestRefreshFailsWhenBothTokensBad(t *testing.T) {
	srv := oauthServer(map[string]bool{}, "")
	defer srv.Close()

	cfg := tmConfig(srv.URL)
	tm := NewTokenManager(cfg, srv.Client())
	tm.refresh = "bad1"
	tm.fallback = "bad2"

	if _, _, err := tm.ForceRefresh(context.Background()); err == nil {
		t.Fatal("expected error when both primary and fallback fail")
	}
}
