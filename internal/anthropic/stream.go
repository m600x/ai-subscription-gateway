package anthropic

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/m600x/ai-substation/internal/config"
	"github.com/m600x/ai-substation/internal/openai"
	"github.com/m600x/ai-substation/internal/provider"
)

// StreamResponse reads the Anthropic SSE stream from r and writes translated
// OpenAI chunks via sink until the stream ends. It emits every
// chat.completion.chunk including the final finish/usage chunk; the caller
// (server) owns the trailing "data: [DONE]" marker.
func StreamResponse(r io.Reader, sink provider.ChunkSink, id, model string, cfg *config.Config) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	roleSent := false
	finish := "stop"
	var usage Usage

	// mergeUsage keeps the most complete counts seen so far: message_start
	// carries input/cache tokens, the final message_delta carries the
	// authoritative output tokens (incl. thinking breakdown).
	mergeUsage := func(u *Usage) {
		if u == nil {
			return
		}
		if u.InputTokens > 0 {
			usage.InputTokens = u.InputTokens
		}
		if u.CacheReadInputTokens > 0 {
			usage.CacheReadInputTokens = u.CacheReadInputTokens
		}
		if u.CacheCreationInputTokens > 0 {
			usage.CacheCreationInputTokens = u.CacheCreationInputTokens
		}
		if u.OutputTokens > 0 {
			usage.OutputTokens = u.OutputTokens
		}
		if u.OutputTokensDetails != nil {
			usage.OutputTokensDetails = u.OutputTokensDetails
		}
	}

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
		return sink.Send(mkChunk(&openai.Delta{Role: "assistant"}, nil))
	}

	finishAndClose := func() {
		_ = sendRole()
		// Final chunk carries usage (OpenAI stream_options.include_usage
		// convention); Open WebUI picks it up for its token-usage popover.
		final := mkChunk(&openai.Delta{}, &finish)
		if usage.InputTokens > 0 || usage.OutputTokens > 0 {
			final.Usage = BuildUsage(usage)
		}
		_ = sink.Send(final)
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
		var ev StreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				mergeUsage(&ev.Message.Usage)
			}
			if err := sendRole(); err != nil {
				return err
			}
		case "content_block_start":
			if cfg.EnableWebSearch && ev.ContentBlock != nil && ev.ContentBlock.Type == "server_tool_use" {
				_ = sendRole()
				// Italic status on its own paragraph: the surrounding blank
				// lines keep the model's answer out of the status styling
				// (a "> " blockquote would swallow the following text).
				_ = sink.Send(mkChunk(&openai.Delta{Content: "\n\n*searching the web…*\n\n"}, nil))
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
					if err := sink.Send(mkChunk(&openai.Delta{Content: ev.Delta.Text}, nil)); err != nil {
						return err
					}
				}
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					if err := sendRole(); err != nil {
						return err
					}
					if err := sink.Send(mkChunk(&openai.Delta{ReasoningContent: ev.Delta.Thinking}, nil)); err != nil {
						return err
					}
				}
			}
		case "message_delta":
			mergeUsage(ev.Usage)
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
			_ = sink.Send(mkChunk(&openai.Delta{Content: "\n[error] " + msg}, nil))
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
