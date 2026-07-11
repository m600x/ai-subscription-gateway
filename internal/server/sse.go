package server

import (
	"encoding/json"
	"io"
	"sync"

	"github.com/m600x/ai-substation/internal/openai"
)

// sseWriter serializes writes to the client's event stream so real content
// chunks and background keepalives never interleave mid-line. It implements
// provider.ChunkSink.
//
// The HTTP status line is deferred until the first write (via onStart, run
// once under the lock): a streaming response commits to 200 as soon as any
// body byte is sent, so holding it back lets an upstream error that arrives
// before any output still surface as a real HTTP error status.
type sseWriter struct {
	w       io.Writer
	flush   func()
	onStart func()
	mu      sync.Mutex
	started bool
}

// newSSEWriter wraps an io.Writer and a flush callback (e.g. http.Flusher.Flush).
// onStart runs exactly once, immediately before the first byte is written.
func newSSEWriter(w io.Writer, flush, onStart func()) *sseWriter {
	return &sseWriter{w: w, flush: flush, onStart: onStart}
}

// begin marks the stream started, running onStart once. Caller holds s.mu.
func (s *sseWriter) begin() {
	if s.started {
		return
	}
	s.started = true
	if s.onStart != nil {
		s.onStart()
	}
}

// Started reports whether any byte has been written yet.
func (s *sseWriter) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// Send marshals a chunk and writes it as an SSE data line. Implements
// provider.ChunkSink.
func (s *sseWriter) Send(c openai.ChatCompletion) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.begin()
	if _, err := io.WriteString(s.w, "data: "+string(b)+"\n\n"); err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *sseWriter) writeRaw(str string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.begin()
	_, _ = io.WriteString(s.w, str)
	s.flush()
}

// writeComment emits an SSE comment line (used for keepalives). Safe to call
// concurrently with a streaming loop.
func (s *sseWriter) writeComment(text string) {
	s.writeRaw(text + "\n\n")
}
