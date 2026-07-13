package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/openai"
	"github.com/m600x/ai-subscription-gateway/internal/provider"
	"github.com/m600x/ai-subscription-gateway/internal/registry"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "invalid or missing API key", "invalid_request_error", "")
		return
	}
	now := time.Now().Unix()
	list := openai.ModelList{Object: "list"}
	for _, m := range s.reg.Public(s.enabled) {
		var reasoning *openai.ModelReasoning
		if len(m.Reasoning.Efforts) > 0 {
			reasoning = &openai.ModelReasoning{
				Efforts: m.Reasoning.Efforts,
				Default: m.Reasoning.Default,
				Mode:    m.Reasoning.Mode,
			}
		}
		list.Data = append(list.Data, openai.Model{ID: m.ID, Object: "model", Created: now, OwnedBy: m.Provider, Reasoning: reasoning})
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "invalid or missing API key", "invalid_request_error", "")
		return
	}
	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages must not be empty", "invalid_request_error", "")
		return
	}

	modelID := req.Model
	if modelID == "" {
		modelID = s.cfg.DefaultModel
	}
	m, prov, ok := s.resolve(w, modelID)
	if !ok {
		return
	}

	if req.Stream {
		s.streamCompletion(w, r, req, m, prov)
		return
	}

	resp, err := prov.Complete(r.Context(), req, m)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// resolve maps a model id to its registry entry and enabled provider, writing
// an error response and returning ok=false when the model is unknown or its
// provider is not configured.
func (s *Server) resolve(w http.ResponseWriter, modelID string) (registry.Model, provider.Provider, bool) {
	if modelID == "" {
		writeError(w, http.StatusBadRequest, "no model specified and no default configured", "invalid_request_error", "")
		return registry.Model{}, nil, false
	}
	m, found := s.reg.Lookup(modelID)
	if !found {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("model %q is not in the registry", modelID), "invalid_request_error", "model_not_found")
		return registry.Model{}, nil, false
	}
	if !s.enabled[m.Provider] {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("model %q requires the %s provider, which is not configured", modelID, m.Provider),
			"invalid_request_error", "provider_not_configured")
		return registry.Model{}, nil, false
	}
	prov, ok := s.providers[m.Provider]
	if !ok || prov == nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Sprintf("no provider wired for %q", m.Provider), "internal_error", "")
		return registry.Model{}, nil, false
	}
	return m, prov, true
}

func (s *Server) streamCompletion(w http.ResponseWriter, r *http.Request, req openai.ChatCompletionRequest, m registry.Model, prov provider.Provider) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported", "internal_error", "")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// The 200 status is deferred to the first write so an upstream error that
	// arrives before any output can still surface as a real HTTP error.
	sse := newSSEWriter(w, flusher.Flush, func() { w.WriteHeader(http.StatusOK) })

	// Keepalive comments during silent stretches so intermediaries don't buffer
	// or time out the stream.
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				sse.writeComment(": keepalive")
			}
		}
	}()

	err := prov.Stream(r.Context(), req, m, sse)
	close(done)
	if err != nil {
		if !sse.Started() {
			// Nothing sent yet: a real HTTP error status is still possible.
			writeUpstreamError(w, err)
			return
		}
		slog.Warn("stream ended with error", "err", err)
	}
	sse.writeRaw("data: [DONE]\n\n")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg, typ, code string) {
	writeJSON(w, status, openai.ErrorResponse{Error: openai.ErrorBody{Message: msg, Type: typ, Code: code}})
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	var he provider.HTTPError
	if errors.As(err, &he) {
		status := he.HTTPStatus()
		if status == 0 {
			status = http.StatusBadGateway
		}
		typ := he.ErrType()
		if typ == "" {
			typ = "upstream_error"
		}
		writeError(w, status, he.Error(), typ, "")
		return
	}
	writeError(w, http.StatusBadGateway, err.Error(), "upstream_error", "")
}
