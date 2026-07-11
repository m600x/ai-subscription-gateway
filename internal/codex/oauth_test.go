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
