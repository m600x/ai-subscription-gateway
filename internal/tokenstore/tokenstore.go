// Package tokenstore optionally persists provider credentials to disk so they
// survive a restart when the server runs with STATELESS=false.
//
// The file holds the long-lived Anthropic token and the (rotating) OpenAI
// refresh token. In stateless mode (the default) this package is never used:
// tokens live only in memory and come from the environment. In non-stateless
// mode the store is seeded from the environment on first run and then updated
// in place whenever the OpenAI refresh token rotates, so a short restart can
// resume with the latest token instead of a possibly-stale env value.
package tokenstore

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
)

// Tokens is the on-disk shape of the token file.
type Tokens struct {
	AnthropicOAuthToken string `json:"anthropic_oauth_token,omitempty"`
	OpenAIRefreshToken  string `json:"openai_refresh_token,omitempty"`
}

// Store reads and writes the token file. It is safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
	t    Tokens
}

// Open reads the token file at path. A missing file is not an error (the store
// starts empty and the file is created on the first write).
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if e := json.Unmarshal(b, &s.t); e != nil {
			slog.Warn("token file is unreadable; ignoring it", "path", path, "err", e)
		}
	case os.IsNotExist(err):
		// first run: nothing to load
	default:
		return nil, err
	}
	return s, nil
}

// Tokens returns a snapshot of the current tokens.
func (s *Store) Tokens() Tokens {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.t
}

// Resolved is the effective credential set after applying file-vs-env
// precedence.
type Resolved struct {
	AnthropicOAuthToken string
	// OpenAIRefreshToken is the token to use first. The persisted file wins
	// over the environment: across the gateway's life the OpenAI refresh token
	// rotates and the latest value is written here, so on a restart the env
	// value may be obsolete.
	OpenAIRefreshToken string
	// OpenAIRefreshFallback is the env-provided refresh token, tried only if a
	// refresh with the primary (file) token fails -- lets a deliberate
	// re-login (new env value) take effect even when a stale file exists.
	OpenAIRefreshFallback string
}

// Resolve merges env-provided credentials with the persisted file:
//
//   - OpenAI refresh token: the file wins (it may have rotated); the env value
//     is kept as a fallback.
//   - Anthropic token: the env wins (the user rotates it via the env); the file
//     is used only to backfill when the env is unset.
func (s *Store) Resolve(envAnthropic, envOpenAIRefresh string) Resolved {
	t := s.Tokens()
	r := Resolved{
		AnthropicOAuthToken:   envAnthropic,
		OpenAIRefreshToken:    envOpenAIRefresh,
		OpenAIRefreshFallback: envOpenAIRefresh,
	}
	if r.AnthropicOAuthToken == "" && t.AnthropicOAuthToken != "" {
		r.AnthropicOAuthToken = t.AnthropicOAuthToken
	}
	if t.OpenAIRefreshToken != "" {
		r.OpenAIRefreshToken = t.OpenAIRefreshToken
	}
	return r
}

// Seed sets the initial token values and writes the file. Empty arguments
// leave the corresponding field unchanged (so an already-persisted, freshly
// rotated OpenAI refresh token is not clobbered by a stale env value).
func (s *Store) Seed(anthropic, openaiRefresh string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if anthropic != "" {
		s.t.AnthropicOAuthToken = anthropic
	}
	if openaiRefresh != "" {
		s.t.OpenAIRefreshToken = openaiRefresh
	}
	return s.writeLocked()
}

// SetOpenAIRefresh persists a rotated OpenAI refresh token. It is intended to
// be wired as the token manager's on-rotation callback; write errors are
// logged rather than propagated so a persistence hiccup never breaks a
// request.
func (s *Store) SetOpenAIRefresh(tok string) {
	if tok == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.t.OpenAIRefreshToken == tok {
		return
	}
	s.t.OpenAIRefreshToken = tok
	if err := s.writeLocked(); err != nil {
		slog.Error("failed to persist rotated OpenAI refresh token", "path", s.path, "err", err)
	}
}

// writeLocked writes the token file atomically (temp file + rename), 0600.
// Caller holds s.mu.
func (s *Store) writeLocked() error {
	b, err := json.MarshalIndent(s.t, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
