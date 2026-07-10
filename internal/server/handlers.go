package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/anthropic"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/openai"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/translate"
)

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

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
	for _, m := range s.cfg.AdvertisedModels() {
		list.Data = append(list.Data, openai.Model{ID: m, Object: "model", Created: now, OwnedBy: "anthropic"})
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

	model := req.Model
	if model == "" {
		model = s.cfg.DefaultModel
	}
	mr := translate.BuildMessagesRequest(req, s.cfg)
	id := newID()

	if req.Stream {
		s.streamCompletion(w, r, mr, id, model)
		return
	}

	resp, err := s.client.CreateMessage(r.Context(), mr)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, translate.BuildChatCompletion(resp, id, model))
}

func (s *Server) streamCompletion(w http.ResponseWriter, r *http.Request, mr anthropic.MessagesRequest, id, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported", "internal_error", "")
		return
	}

	body, err := s.client.StreamMessage(r.Context(), mr)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sse := translate.NewSSEWriter(w, flusher.Flush)

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
				sse.WriteComment(": keepalive")
			}
		}
	}()
	defer close(done)

	if err := translate.StreamResponse(body, sse, id, model, s.cfg); err != nil {
		slog.Warn("stream ended with error", "err", err)
	}
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
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		status := ae.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		typ := ae.Type
		if typ == "" {
			typ = "upstream_error"
		}
		writeError(w, status, ae.Message, typ, "")
		return
	}
	writeError(w, http.StatusBadGateway, err.Error(), "upstream_error", "")
}
