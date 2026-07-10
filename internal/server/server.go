// Package server wires HTTP routes to the Anthropic client and translation layer.
package server

import (
	"log/slog"
	"net/http"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/anthropic"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/config"
)

// Server holds dependencies and the route mux.
type Server struct {
	cfg    *config.Config
	client *anthropic.Client
	mux    *http.ServeMux
}

// New builds the router.
func New(cfg *config.Config, client *anthropic.Client) *Server {
	s := &Server{cfg: cfg, client: client, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	return s
}

// ServeHTTP implements http.Handler with panic recovery.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic recovered", "err", rec, "path", r.URL.Path)
			writeError(w, http.StatusInternalServerError, "internal error", "internal_error", "")
		}
	}()
	s.mux.ServeHTTP(w, r)
}
