package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/config"
)

// Client talks to the Anthropic Messages API with a subscription OAuth token.
type Client struct {
	http *http.Client
	cfg  *config.Config
}

// New builds a Client with a pooled HTTP transport.
func New(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: cfg.RequestTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// Error is a typed upstream failure.
type Error struct {
	Status  int
	Type    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("anthropic %d %s: %s", e.Status, e.Type, e.Message)
}

// HTTPStatus implements provider.HTTPError.
func (e *Error) HTTPStatus() int { return e.Status }

// ErrType implements provider.HTTPError.
func (e *Error) ErrType() string { return e.Type }

func (c *Client) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.AnthropicBaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.OAuthToken)
	req.Header.Set("anthropic-version", c.cfg.AnthropicVersion)
	if c.cfg.AnthropicBeta != "" {
		req.Header.Set("anthropic-beta", c.cfg.AnthropicBeta)
	}
	if c.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", c.cfg.UserAgent)
	}
	return req, nil
}

func (c *Client) parseError(status int, raw []byte) *Error {
	var body struct {
		Error APIErrorBody `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	if status == http.StatusUnauthorized {
		slog.Error("ANTHROPIC 401: OAuth token expired or revoked -- regenerate with 'claude setup-token' and update the ANTHROPIC_OAUTH_TOKEN secret")
	}
	return &Error{Status: status, Type: body.Error.Type, Message: body.Error.Message}
}

// do issues the request with retries on 429 / 5xx. On success the caller owns
// the response body and must close it.
func (c *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		req, err := c.newRequest(ctx, body)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			lastErr = c.parseError(resp.StatusCode, raw)
		default:
			return resp, nil
		}
		if attempt < c.cfg.MaxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
	}
	return nil, lastErr
}

func backoff(attempt int) time.Duration {
	return time.Duration(attempt+1) * 400 * time.Millisecond
}

// CreateMessage performs a non-streaming request.
func (c *Client) CreateMessage(ctx context.Context, mr MessagesRequest) (*MessagesResponse, error) {
	mr.Stream = false
	body, err := json.Marshal(mr)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp.StatusCode, raw)
	}
	var out MessagesResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StreamMessage opens a streaming request. The caller must close the returned
// reader.
func (c *Client) StreamMessage(ctx context.Context, mr MessagesRequest) (io.ReadCloser, error) {
	mr.Stream = true
	body, err := json.Marshal(mr)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, c.parseError(resp.StatusCode, raw)
	}
	return resp.Body, nil
}
