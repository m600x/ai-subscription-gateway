// Package server wires HTTP routes to the enabled providers and the model
// registry. It routes each chat request to the provider that owns the named
// model, aggregates /v1/models across enabled providers, and owns the shared
// SSE plumbing (keepalives, the trailing [DONE] marker).
package server

import (
	"log/slog"
	"net/http"

	"github.com/m600x/ai-substation/internal/config"
	"github.com/m600x/ai-substation/internal/provider"
	"github.com/m600x/ai-substation/internal/registry"
)

// Server holds dependencies and the route mux.
type Server struct {
	cfg       *config.Config
	reg       *registry.Registry
	providers map[string]provider.Provider
	enabled   map[string]bool
	mux       *http.ServeMux
}

// New builds the router. providers maps a provider name
// (registry.ProviderAnthropic / ProviderOpenAI) to its implementation;
// enabled marks which providers are configured.
func New(cfg *config.Config, reg *registry.Registry, providers map[string]provider.Provider, enabled map[string]bool) *Server {
	s := &Server{cfg: cfg, reg: reg, providers: providers, enabled: enabled, mux: http.NewServeMux()}
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
