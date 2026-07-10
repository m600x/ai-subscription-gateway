package translate

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/m600x/claude-subscription-openai-wrapper/internal/anthropic"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/config"
	"github.com/m600x/claude-subscription-openai-wrapper/internal/openai"
)

// SSEWriter serializes writes to the client's event stream so real content
// chunks and background keepalives never interleave mid-line.
type SSEWriter struct {
	w     io.Writer
	flush func()
	mu    sync.Mutex
}

// NewSSEWriter wraps an io.Writer and a flush callback (e.g. http.Flusher.Flush).
func NewSSEWriter(w io.Writer, flush func()) *SSEWriter {
	return &SSEWriter{w: w, flush: flush}
}

func (s *SSEWriter) writeChunk(c openai.ChatCompletion) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.w, "data: "+string(b)+"\n\n"); err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *SSEWriter) writeRaw(str string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = io.WriteString(s.w, str)
	s.flush()
}

// WriteComment emits an SSE comment line (used for keepalives). Safe to call
// concurrently with the streaming loop.
func (s *SSEWriter) WriteComment(text string) {
	s.writeRaw(text + "\n\n")
}

// StreamResponse reads the Anthropic SSE stream from r and writes translated
// OpenAI chunks via sse until the stream ends.
func StreamResponse(r io.Reader, sse *SSEWriter, id, model string, cfg *config.Config) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	roleSent := false
	finish := "stop"

	mkChunk := func(d *openai.Delta, fr *string) openai.ChatCompletion {
		return openai.ChatCompletion{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []openai.Choice{{Index: 0, Delta: d, FinishReason: fr}},
		}
	}

	sendRole := func() error {
		if roleSent {
			return nil
		}
		roleSent = true
		return sse.writeChunk(mkChunk(&openai.Delta{Role: "assistant"}, nil))
	}

	finishAndClose := func() {
		_ = sendRole()
		_ = sse.writeChunk(mkChunk(&openai.Delta{}, &finish))
		sse.writeRaw("data: [DONE]\n\n")
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var ev anthropic.StreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if err := sendRole(); err != nil {
				return err
			}
		case "content_block_start":
			if cfg.EnableWebSearch && ev.ContentBlock != nil && ev.ContentBlock.Type == "server_tool_use" {
				_ = sendRole()
				_ = sse.writeChunk(mkChunk(&openai.Delta{Content: "\n> searching the web…\n"}, nil))
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					if err := sendRole(); err != nil {
						return err
					}
					if err := sse.writeChunk(mkChunk(&openai.Delta{Content: ev.Delta.Text}, nil)); err != nil {
						return err
					}
				}
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					if err := sendRole(); err != nil {
						return err
					}
					if err := sse.writeChunk(mkChunk(&openai.Delta{ReasoningContent: ev.Delta.Thinking}, nil)); err != nil {
						return err
					}
				}
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				finish = mapStopReason(ev.Delta.StopReason)
			}
		case "message_stop":
			finishAndClose()
			return nil
		case "error":
			msg := "upstream stream error"
			if ev.Error != nil && ev.Error.Message != "" {
				msg = ev.Error.Message
			}
			_ = sendRole()
			_ = sse.writeChunk(mkChunk(&openai.Delta{Content: "\n[error] " + msg}, nil))
			finishAndClose()
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// Stream ended without an explicit message_stop; close cleanly.
	finishAndClose()
	return nil
}
