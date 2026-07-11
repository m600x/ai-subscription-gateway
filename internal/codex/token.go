package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/m600x/ai-substation/internal/config"
)

// TokenManager holds the ChatGPT OAuth credentials and refreshes the
// short-lived access token in-memory (nothing is persisted, so the process
// stays stateless -- on restart it re-derives everything from the refresh
// token). It is safe for concurrent use.
type TokenManager struct {
	cfg  *config.Config
	http *http.Client

	mu      sync.Mutex
	access  string
	account string
	refresh string
	exp     time.Time
}

// NewTokenManager seeds the manager from config. It does not perform any
// network call; use Prime to validate/refresh at startup.
func NewTokenManager(cfg *config.Config, httpClient *http.Client) *TokenManager {
	tm := &TokenManager{
		cfg:     cfg,
		http:    httpClient,
		access:  cfg.OpenAIAccessToken,
		account: cfg.OpenAIAccountID,
		refresh: cfg.OpenAIRefreshToken,
	}
	if tm.access != "" {
		tm.exp = tokenExpiry(tm.access)
		if tm.account == "" {
			// The access token isn't the id_token, so this is best-effort;
			// a refresh will fill it in when a refresh token is present.
			tm.account = accountIDFromIDToken(tm.access)
		}
	}
	return tm
}

// Prime validates the credentials at startup, refreshing if possible so a bad
// refresh token fails fast with an actionable message.
func (tm *TokenManager) Prime(ctx context.Context) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.refresh != "" {
		return tm.refreshLocked(ctx)
	}
	if tm.access == "" {
		return fmt.Errorf("no OpenAI credentials: set OPENAI_REFRESH_TOKEN")
	}
	if tm.account == "" {
		return fmt.Errorf("OPENAI_ACCESS_TOKEN given without a derivable account id; set OPENAI_ACCOUNT_ID or use OPENAI_REFRESH_TOKEN")
	}
	return nil
}

// Token returns a fresh access token and account id, refreshing when the
// current access token is within 5 minutes of expiry.
func (tm *TokenManager) Token(ctx context.Context) (access, account string, err error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.needsRefreshLocked() {
		if err := tm.refreshLocked(ctx); err != nil {
			// If we still hold a usable token, serve it and let a 401 drive a
			// forced refresh; otherwise surface the error.
			if tm.access == "" {
				return "", "", err
			}
			slog.Warn("openai token refresh failed; using cached token", "err", err)
		}
	}
	return tm.access, tm.account, nil
}

// ForceRefresh refreshes unconditionally (used after an upstream 401).
func (tm *TokenManager) ForceRefresh(ctx context.Context) (access, account string, err error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.refresh == "" {
		return tm.access, tm.account, fmt.Errorf("cannot refresh: no OPENAI_REFRESH_TOKEN")
	}
	if err := tm.refreshLocked(ctx); err != nil {
		return "", "", err
	}
	return tm.access, tm.account, nil
}

func (tm *TokenManager) needsRefreshLocked() bool {
	if tm.access == "" {
		return true
	}
	if tm.refresh == "" {
		return false // static token; nothing to refresh with
	}
	if tm.exp.IsZero() {
		return false
	}
	return time.Now().Add(5 * time.Minute).After(tm.exp)
}

// refreshLocked performs the OAuth refresh. Caller holds tm.mu.
func (tm *TokenManager) refreshLocked(ctx context.Context) error {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tm.refresh},
		"client_id":     {tm.cfg.OpenAIClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		tm.cfg.OpenAIAuthIssuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := tm.http.Do(req)
	if err != nil {
		return fmt.Errorf("openai token refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		slog.Error("OPENAI token refresh failed: the refresh token is expired or revoked -- re-run 'server login' and update OPENAI_REFRESH_TOKEN",
			"status", resp.StatusCode)
		return &Error{Status: resp.StatusCode, Type: "auth_error", Message: "openai token refresh failed: " + string(raw)}
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return fmt.Errorf("parsing token refresh response: %w", err)
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("token refresh response missing access_token")
	}
	tm.access = tr.AccessToken
	if tr.RefreshToken != "" {
		tm.refresh = tr.RefreshToken
	}
	if tr.IDToken != "" {
		if id := accountIDFromIDToken(tr.IDToken); id != "" {
			tm.account = id
		}
	}
	tm.exp = tokenExpiry(tm.access)
	return nil
}
