package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/config"
)

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

	tr, err := exchangeCode(context.Background(), srv.Client(), srv.URL, "cid", srv.URL+"/deviceauth/callback", "verifier", "the-code")
	if err != nil {
		t.Fatal(err)
	}
	if tr.AccessToken != "at" || tr.RefreshToken != "rt" || tr.IDToken != "it" {
		t.Errorf("token response = %+v", tr)
	}
}

// deviceIssuer mocks the device-code endpoints. tokenStatus is served by the
// poll endpoint for the first (pendingRounds) polls (e.g. 404), then it returns
// the authorization code + verifier; /oauth/token returns the tokens.
func deviceIssuer(t *testing.T, pendingRounds int32, refresh string) (*httptest.Server, *int32) {
	t.Helper()
	var polls int32
	idTok := makeJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_1"},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/accounts/deviceauth/usercode"):
			// interval as a quoted string, matching the real endpoint.
			io.WriteString(w, `{"device_auth_id":"dev-1","user_code":"WXYZ-1234","interval":"0"}`)
		case strings.HasSuffix(r.URL.Path, "/api/accounts/deviceauth/token"):
			if atomic.AddInt32(&polls, 1) <= pendingRounds {
				w.WriteHeader(http.StatusNotFound) // not authorized yet
				return
			}
			io.WriteString(w, `{"authorization_code":"auth-code","code_verifier":"verifier-xyz"}`)
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			if form.Get("code") != "auth-code" || form.Get("code_verifier") != "verifier-xyz" {
				t.Errorf("exchange form = %v", form)
			}
			io.WriteString(w, `{"access_token":"at","id_token":"`+idTok+`","refresh_token":"`+refresh+`"}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	return srv, &polls
}

func noSleep(time.Duration) {}
func noPrompt(_, _ string)  {}

func TestDeviceLoginSuccessAfterPolling(t *testing.T) {
	srv, polls := deviceIssuer(t, 2, "rt-device") // pending twice, then authorized
	defer srv.Close()
	cfg := &config.Config{OpenAIAuthIssuer: srv.URL, OpenAIClientID: "cid", OpenAIOriginator: "codex_cli_rs"}

	var gotURL, gotCode string
	res, err := deviceLogin(context.Background(), cfg, srv.Client(), noSleep,
		func(u, c string) { gotURL, gotCode = u, c })
	if err != nil {
		t.Fatalf("deviceLogin: %v", err)
	}
	if res.RefreshToken != "rt-device" || res.AccountID != "acc_1" {
		t.Errorf("result = %+v", res)
	}
	if gotURL != srv.URL+"/codex/device" || gotCode != "WXYZ-1234" {
		t.Errorf("prompt url=%q code=%q", gotURL, gotCode)
	}
	if atomic.LoadInt32(polls) != 3 {
		t.Errorf("polls = %d, want 3 (2 pending + 1 success)", *polls)
	}
}

func TestDeviceLoginUsercodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()
	cfg := &config.Config{OpenAIAuthIssuer: srv.URL, OpenAIClientID: "cid"}

	if _, err := deviceLogin(context.Background(), cfg, srv.Client(), noSleep, noPrompt); err == nil {
		t.Fatal("expected an error when the usercode request fails")
	}
}

func TestDeviceLoginContextCanceled(t *testing.T) {
	// usercode succeeds; the token endpoint stays pending; the sleep cancels the
	// context so the poll loop exits with the context error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/usercode") {
			io.WriteString(w, `{"device_auth_id":"d","user_code":"C","interval":0}`)
			return
		}
		w.WriteHeader(http.StatusNotFound) // always pending
	}))
	defer srv.Close()
	cfg := &config.Config{OpenAIAuthIssuer: srv.URL, OpenAIClientID: "cid"}

	ctx, cancel := context.WithCancel(context.Background())
	_, err := deviceLogin(ctx, cfg, srv.Client(), func(time.Duration) { cancel() }, noPrompt)
	if err == nil {
		t.Fatal("expected a context-cancellation error")
	}
}
