package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/config"
)

// loginPort is the fixed local callback port the OAuth client expects.
const loginPort = 1455

// pkce holds a PKCE verifier/challenge pair.
type pkce struct {
	verifier  string
	challenge string
}

func generatePKCE() (pkce, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return pkce{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return pkce{verifier: verifier, challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// authorizeURL builds the OAuth authorize URL for the Codex simplified flow.
func authorizeURL(issuer, clientID, redirect, originator, challenge, state string) string {
	params := url.Values{
		"response_type":              {"code"},
		"client_id":                  {clientID},
		"redirect_uri":               {redirect},
		"scope":                      {"openid profile email offline_access"},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
		"originator":                 {originator},
	}
	return issuer + "/oauth/authorize?" + params.Encode()
}

// exchangeCode swaps an authorization code for tokens.
func exchangeCode(ctx context.Context, httpClient *http.Client, issuer, clientID, redirect, verifier, code string) (tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return tokenResponse{}, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(raw))
	}
	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return tokenResponse{}, err
	}
	return tr, nil
}

// LoginResult is the outcome of a successful login.
type LoginResult struct {
	RefreshToken string
	AccessToken  string
	AccountID    string
}

const loginSuccessHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Login successful</title></head>
<body style="font-family:system-ui,sans-serif;max-width:640px;margin:80px auto">
<h1>Login successful</h1><p>You can close this window and return to the terminal.</p></body></html>`

// Login runs the interactive browser OAuth (PKCE) flow and returns the tokens.
// It mirrors `claude setup-token`: complete the flow in the browser, then set
// the printed refresh token as OPENAI_REFRESH_TOKEN.
func Login(ctx context.Context, cfg *config.Config) (LoginResult, error) {
	return login(ctx, cfg, loginPort, func(u string) {
		fmt.Println("Open this URL in your browser to authorize:\n\n  " + u + "\n")
		_ = openBrowser(u)
	})
}

// login is the testable core of Login. port is the local callback port (0 lets
// the OS pick one, used by tests); open is invoked with the authorize URL (the
// production hook opens a browser; a test can drive the callback instead).
func login(ctx context.Context, cfg *config.Config, port int, open func(url string)) (LoginResult, error) {
	pk, err := generatePKCE()
	if err != nil {
		return LoginResult{}, err
	}
	state := randomHex(32)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return LoginResult{}, fmt.Errorf("cannot bind local callback port %d (is another login running?): %w", port, err)
	}
	redirect := fmt.Sprintf("http://localhost:%d/auth/callback", ln.Addr().(*net.TCPAddr).Port)

	type result struct {
		tr  tokenResponse
		err error
	}
	resultCh := make(chan result, 1)
	httpClient := &http.Client{Timeout: 30 * time.Second}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("state mismatch on callback")}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("no authorization code in callback")}
			return
		}
		tr, err := exchangeCode(r.Context(), httpClient, cfg.OpenAIAuthIssuer, cfg.OpenAIClientID, redirect, pk.verifier, code)
		if err != nil {
			http.Error(w, "token exchange failed", http.StatusInternalServerError)
			resultCh <- result{err: err}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, loginSuccessHTML)
		resultCh <- result{tr: tr}
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	open(authorizeURL(cfg.OpenAIAuthIssuer, cfg.OpenAIClientID, redirect, cfg.OpenAIOriginator, pk.challenge, state))

	select {
	case <-ctx.Done():
		return LoginResult{}, ctx.Err()
	case res := <-resultCh:
		if res.err != nil {
			return LoginResult{}, res.err
		}
		return LoginResult{
			RefreshToken: res.tr.RefreshToken,
			AccessToken:  res.tr.AccessToken,
			AccountID:    accountIDFromIDToken(res.tr.IDToken),
		}, nil
	}
}

func openBrowser(u string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{u}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", u}
	default:
		cmd = "xdg-open"
		args = []string{u}
	}
	return exec.Command(cmd, args...).Start()
}
