package codex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/m600x/ai-subscription-gateway/internal/config"
)

func TestGeneratePKCE(t *testing.T) {
	pk, err := generatePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if pk.verifier == "" || pk.challenge == "" {
		t.Fatal("empty pkce")
	}
	sum := sha256.Sum256([]byte(pk.verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if pk.challenge != want {
		t.Errorf("challenge is not S256(verifier)")
	}
}

func TestAuthorizeURL(t *testing.T) {
	raw := authorizeURL("https://auth.openai.com", "cid", "http://localhost:1455/auth/callback", "codex_cli_rs", "chal", "st8")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	checks := map[string]string{
		"response_type":             "code",
		"client_id":                 "cid",
		"code_challenge":            "chal",
		"code_challenge_method":     "S256",
		"state":                     "st8",
		"originator":                "codex_cli_rs",
		"codex_cli_simplified_flow": "true",
	}
	for k, want := range checks {
		if q.Get(k) != want {
			t.Errorf("param %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if !strings.Contains(q.Get("scope"), "offline_access") {
		t.Errorf("scope missing offline_access: %q", q.Get("scope"))
	}
}

func TestExchangeCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/oauth/token") {
			http.Error(w, "not found", 404)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		if form.Get("grant_type") != "authorization_code" || form.Get("code") != "the-code" {
			t.Errorf("unexpected form: %v", form)
		}
		io.WriteString(w, `{"access_token":"at","id_token":"it","refresh_token":"rt","expires_in":3600}`)
	}))
	defer srv.Close()

	tr, err := exchangeCode(context.Background(), srv.Client(), srv.URL, "cid", "http://localhost/cb", "verifier", "the-code")
	if err != nil {
		t.Fatal(err)
	}
	if tr.AccessToken != "at" || tr.RefreshToken != "rt" || tr.IDToken != "it" {
		t.Errorf("token response = %+v", tr)
	}
}

// authCodeIssuer mocks the OAuth token endpoint for the authorization_code
// grant, returning the given refresh token and an id_token carrying acc_1.
func authCodeIssuer(t *testing.T, refresh string) *httptest.Server {
	t.Helper()
	idTok := makeJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_1"},
	})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "authorization_code" || r.FormValue("code") == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"bad_request"}`)
			return
		}
		io.WriteString(w, `{"access_token":"at","id_token":"`+idTok+`","refresh_token":"`+refresh+`","expires_in":3600}`)
	}))
}

// fakeBrowser returns an `open` hook that reads the authorize URL and drives
// the local callback with the given code and (optionally overridden) state.
func fakeBrowser(code, stateOverride string) func(string) {
	return func(authURL string) {
		u, err := url.Parse(authURL)
		if err != nil {
			return
		}
		q := u.Query()
		state := q.Get("state")
		if stateOverride != "" {
			state = stateOverride
		}
		resp, err := http.Get(q.Get("redirect_uri") + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
		if err == nil {
			_ = resp.Body.Close()
		}
	}
}

func TestLoginSuccess(t *testing.T) {
	issuer := authCodeIssuer(t, "rt-login")
	defer issuer.Close()
	cfg := &config.Config{OpenAIAuthIssuer: issuer.URL, OpenAIClientID: "cid", OpenAIOriginator: "codex_cli_rs"}

	res, err := login(context.Background(), cfg, 0, fakeBrowser("the-code", ""))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if res.RefreshToken != "rt-login" {
		t.Errorf("RefreshToken = %q, want rt-login", res.RefreshToken)
	}
	if res.AccountID != "acc_1" {
		t.Errorf("AccountID = %q, want acc_1 (from id_token)", res.AccountID)
	}
}

func TestLoginStateMismatch(t *testing.T) {
	issuer := authCodeIssuer(t, "rt")
	defer issuer.Close()
	cfg := &config.Config{OpenAIAuthIssuer: issuer.URL, OpenAIClientID: "cid"}

	_, err := login(context.Background(), cfg, 0, fakeBrowser("the-code", "WRONG-STATE"))
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("want a state-mismatch error, got %v", err)
	}
}

func TestLoginMissingCode(t *testing.T) {
	issuer := authCodeIssuer(t, "rt")
	defer issuer.Close()
	cfg := &config.Config{OpenAIAuthIssuer: issuer.URL, OpenAIClientID: "cid"}

	_, err := login(context.Background(), cfg, 0, fakeBrowser("", ""))
	if err == nil || !strings.Contains(err.Error(), "authorization code") {
		t.Fatalf("want a missing-code error, got %v", err)
	}
}

func TestLoginContextCanceled(t *testing.T) {
	cfg := &config.Config{OpenAIAuthIssuer: "http://127.0.0.1:9", OpenAIClientID: "cid"}
	ctx, cancel := context.WithCancel(context.Background())
	// The "browser" never completes the callback; cancel the context instead.
	_, err := login(ctx, cfg, 0, func(string) { cancel() })
	if err == nil {
		t.Fatal("expected a context-cancellation error")
	}
}
