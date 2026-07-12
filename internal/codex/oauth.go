package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/config"
)

// LoginResult is the outcome of a successful login.
type LoginResult struct {
	RefreshToken string
	AccessToken  string
	AccountID    string
}

// flexInt is an int that also accepts a JSON string ("5") or number (5), since
// the device-auth endpoint returns interval as a quoted string. Unparseable
// values decode to 0 (the caller applies a default).
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		*f = flexInt(n)
	}
	return nil
}

// deviceCodeResp is the response from the device-authorization "usercode" call.
type deviceCodeResp struct {
	DeviceAuthID string  `json:"device_auth_id"`
	UserCode     string  `json:"user_code"`
	UserCodeAlt  string  `json:"usercode"` // some responses use this spelling
	Interval     flexInt `json:"interval"`
}

func (d deviceCodeResp) code() string {
	if d.UserCode != "" {
		return d.UserCode
	}
	return d.UserCodeAlt
}

// devicePollResp is the response once the user has authorized the device.
type devicePollResp struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

// Login runs the headless device-code OAuth flow and returns the tokens. It
// prints a verification URL and a short code; the user enters the code on any
// device/browser, and the command polls until authorized. There is no local
// callback server and no same-host browser requirement, so it works in
// containers and over SSH.
func Login(ctx context.Context, cfg *config.Config) (LoginResult, error) {
	sleep := func(d time.Duration) {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
	prompt := func(verifyURL, userCode string) {
		fmt.Printf("\nTo authorize, open this URL on any device and enter the code:\n\n"+
			"  URL:  %s\n  Code: %s\n\nWaiting for authorization…\n", verifyURL, userCode)
	}
	return deviceLogin(ctx, cfg, &http.Client{Timeout: 30 * time.Second}, sleep, prompt)
}

// deviceLogin is the testable core of Login. sleep waits between polls (a test
// passes a no-op / cancelling stub); prompt surfaces the verification URL+code.
func deviceLogin(ctx context.Context, cfg *config.Config, httpClient *http.Client,
	sleep func(time.Duration), prompt func(verifyURL, userCode string)) (LoginResult, error) {
	issuer := strings.TrimRight(cfg.OpenAIAuthIssuer, "/")

	dc, err := requestDeviceCode(ctx, httpClient, issuer, cfg.OpenAIClientID, cfg.OpenAIOriginator)
	if err != nil {
		return LoginResult{}, err
	}
	if dc.DeviceAuthID == "" || dc.code() == "" {
		return LoginResult{}, fmt.Errorf("device authorization response missing device_auth_id/user_code")
	}
	prompt(issuer+"/codex/device", dc.code())

	interval := time.Duration(dc.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}

	for {
		if err := ctx.Err(); err != nil {
			return LoginResult{}, err
		}
		sleep(interval)
		if err := ctx.Err(); err != nil {
			return LoginResult{}, err
		}

		poll, status, err := pollDeviceToken(ctx, httpClient, issuer, dc.DeviceAuthID, dc.code(), cfg.OpenAIOriginator)
		if err != nil {
			return LoginResult{}, err
		}
		switch {
		case status == http.StatusForbidden || status == http.StatusNotFound:
			continue // not authorized yet
		case status >= 400:
			return LoginResult{}, fmt.Errorf("device authorization failed with status %d", status)
		}
		if poll.AuthorizationCode == "" || poll.CodeVerifier == "" {
			return LoginResult{}, fmt.Errorf("device token response missing authorization_code/code_verifier")
		}

		tr, err := exchangeCode(ctx, httpClient, issuer, cfg.OpenAIClientID,
			issuer+"/deviceauth/callback", poll.CodeVerifier, poll.AuthorizationCode)
		if err != nil {
			return LoginResult{}, err
		}
		return LoginResult{
			RefreshToken: tr.RefreshToken,
			AccessToken:  tr.AccessToken,
			AccountID:    accountIDFromIDToken(tr.IDToken),
		}, nil
	}
}

func requestDeviceCode(ctx context.Context, httpClient *http.Client, issuer, clientID, originator string) (deviceCodeResp, error) {
	body, _ := json.Marshal(map[string]string{"client_id": clientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		issuer+"/api/accounts/deviceauth/usercode", bytes.NewReader(body))
	if err != nil {
		return deviceCodeResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", originator+"/device-login")

	resp, err := httpClient.Do(req)
	if err != nil {
		return deviceCodeResp{}, fmt.Errorf("device code request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return deviceCodeResp{}, fmt.Errorf("device code request failed (%d): %s", resp.StatusCode, string(raw))
	}
	var dc deviceCodeResp
	if err := json.Unmarshal(raw, &dc); err != nil {
		return deviceCodeResp{}, fmt.Errorf("parsing device code response: %w", err)
	}
	return dc, nil
}

// pollDeviceToken polls once. The HTTP status is returned so the caller can
// distinguish "pending" (403/404) from success/failure; err is only for
// transport-level failures.
func pollDeviceToken(ctx context.Context, httpClient *http.Client, issuer, deviceAuthID, userCode, originator string) (devicePollResp, int, error) {
	body, _ := json.Marshal(map[string]string{"device_auth_id": deviceAuthID, "user_code": userCode})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		issuer+"/api/accounts/deviceauth/token", bytes.NewReader(body))
	if err != nil {
		return devicePollResp{}, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", originator+"/device-login")

	resp, err := httpClient.Do(req)
	if err != nil {
		return devicePollResp{}, 0, fmt.Errorf("device token poll: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return devicePollResp{}, resp.StatusCode, nil
	}
	var pr devicePollResp
	if err := json.Unmarshal(raw, &pr); err != nil {
		return devicePollResp{}, resp.StatusCode, fmt.Errorf("parsing device token response: %w", err)
	}
	return pr, resp.StatusCode, nil
}

// exchangeCode swaps an authorization code (from the device flow) for tokens.
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
