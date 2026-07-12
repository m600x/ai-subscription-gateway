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
