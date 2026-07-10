package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authorized reports whether the request carries the correct client API key as
// a Bearer token. Comparison is constant-time.
func (s *Server) authorized(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.ClientAPIKey)) == 1
}
