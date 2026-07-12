package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/config"
)

// Client talks to the ChatGPT Codex Responses API with a subscription OAuth
// token pair.
type Client struct {
	cfg            *config.Config
	http           *http.Client
	tok            *TokenManager
	installationID string
}

// New builds a Client with a pooled transport and its own token manager.
func New(cfg *config.Config) *Client {
	httpClient := &http.Client{
		Timeout: cfg.RequestTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	return &Client{
		cfg:            cfg,
		http:           httpClient,
		tok:            NewTokenManager(cfg, &http.Client{Timeout: 30 * time.Second}),
		installationID: newUUID(),
	}
}

// Prime validates credentials at startup (see TokenManager.Prime).
func (c *Client) Prime(ctx context.Context) error { return c.tok.Prime(ctx) }

func (c *Client) headers(req *http.Request, access, account, sessionID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("ChatGPT-Account-ID", account)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", c.cfg.OpenAIOriginator)
	req.Header.Set("session-id", sessionID)
	req.Header.Set("x-codex-installation-id", c.installationID)
	if c.cfg.OpenAIUserAgent != "" {
		req.Header.Set("User-Agent", c.cfg.OpenAIUserAgent)
	}
}

func (c *Client) newRequest(ctx context.Context, body []byte, access, account, sessionID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.OpenAIBaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.headers(req, access, account, sessionID)
	return req, nil
}

// stream POSTs the Responses payload and returns the raw SSE body. On a 401 it
// forces a token refresh and retries once. The caller must close the body.
func (c *Client) stream(ctx context.Context, payload responsesRequest) (io.ReadCloser, error) {
	sessionID := newUUID()
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	access, account, err := c.tok.Token(ctx)
	if err != nil {
		return nil, &Error{Status: http.StatusUnauthorized, Type: "auth_error", Message: err.Error()}
	}

	resp, err := c.do(ctx, body, access, account, sessionID)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		access, account, rerr := c.tok.ForceRefresh(ctx)
		if rerr != nil {
			return nil, &Error{Status: http.StatusUnauthorized, Type: "auth_error", Message: rerr.Error()}
		}
		resp, err = c.do(ctx, body, access, account, sessionID)
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, parseError(resp.StatusCode, raw)
	}
	return resp.Body, nil
}

func (c *Client) do(ctx context.Context, body []byte, access, account, sessionID string) (*http.Response, error) {
	req, err := c.newRequest(ctx, body, access, account, sessionID)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Type: "upstream_error", Message: err.Error()}
	}
	return resp, nil
}

func parseError(status int, raw []byte) *Error {
	var body struct {
		Error apiError `json:"error"`
		// Some upstream errors put a bare "detail"/"message" at the top level.
		Detail  string `json:"detail"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)
	msg := body.Error.Message
	if msg == "" {
		msg = body.Detail
	}
	if msg == "" {
		msg = body.Message
	}
	if msg == "" {
		msg = string(raw)
	}
	typ := body.Error.Type
	if typ == "" {
		typ = "upstream_error"
	}
	return &Error{Status: status, Type: typ, Message: msg}
}
