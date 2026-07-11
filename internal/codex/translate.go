package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/m600x/ai-substation/internal/config"
	"github.com/m600x/ai-substation/internal/openai"
	"github.com/m600x/ai-substation/internal/provider"
	"github.com/m600x/ai-substation/internal/registry"
)

// buildRequest maps an OpenAI chat-completions request onto a Responses API
// payload. The Codex backend is stream-only, so stream is always true;
// non-streaming callers aggregate the stream themselves.
func buildRequest(req openai.ChatCompletionRequest, m registry.Model, cfg *config.Config, sessionKey string) responsesRequest {
	out := responsesRequest{
		Model:          m.UpstreamID,
		Input:          buildInput(req.Messages),
		Tools:          buildTools(req.Tools),
		Store:          false,
		Stream:         true,
		PromptCacheKey: sessionKey,
		Reasoning:      &reasoning{Effort: resolveEffort(req.ReasoningEffort, m), Summary: "auto"},
		Include:        []string{"reasoning.encrypted_content"},
	}
	if len(req.ToolChoice) > 0 {
		out.ToolChoice = req.ToolChoice
	}
	if req.ParallelToolCalls != nil {
		out.ParallelToolCalls = *req.ParallelToolCalls
	}
	if instr := buildInstructions(req.Messages, cfg); instr != "" {
		out.Instructions = instr
	}
	return out
}

// resolveEffort clamps the client's reasoning_effort to the model's ladder,
// falling back to the model's default when unset/unsupported. A "disable"
// request (off/none/minimal) resolves to the lowest reasoning level the model
// actually advertises, since Codex reasoning models can't be fully disabled.
func resolveEffort(effort string, m registry.Model) string {
	e := strings.ToLower(strings.TrimSpace(effort))
	if e == "off" || e == "none" || e == "minimal" {
		for _, cand := range []string{"minimal", "none", "low"} {
			if m.AllowsEffort(cand) {
				return cand
			}
		}
		e = "" // nothing low enough advertised; use the default
	}
	if e != "" && m.AllowsEffort(e) {
		return e
	}
	if m.Reasoning.Default != "" {
		return m.Reasoning.Default
	}
	return "medium"
}

// buildInstructions concatenates the client's system/developer messages,
// prefixed by the optional configured base instructions.
func buildInstructions(msgs []openai.ChatMessage, cfg *config.Config) string {
	var parts []string
	if strings.TrimSpace(cfg.OpenAIBaseInstructions) != "" {
		parts = append(parts, cfg.OpenAIBaseInstructions)
	}
	for _, m := range msgs {
		if m.Role == "system" || m.Role == "developer" {
			if s := strings.TrimSpace(m.Content.String()); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// buildInput converts chat messages into Responses input items. System and
// developer messages are handled separately (buildInstructions) and skipped
// here.
func buildInput(msgs []openai.ChatMessage) []inputItem {
	items := make([]inputItem, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			continue
		case "tool":
			if m.ToolCallID != "" {
				items = append(items, inputItem{
					Type:   "function_call_output",
					CallID: m.ToolCallID,
					Output: m.Content.String(),
				})
			}
			continue
		}

		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if tc.Type != "" && tc.Type != "function" {
					continue
				}
				items = append(items, inputItem{
					Type:      "function_call",
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
					CallID:    tc.ID,
				})
			}
		}

		parts := contentParts(m)
		if len(parts) == 0 {
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "assistant"
		}
		items = append(items, inputItem{Type: "message", Role: role, Content: parts})
	}
	return items
}

func contentParts(m openai.ChatMessage) []contentPart {
	textKind := "input_text"
	if m.Role == "assistant" {
		textKind = "output_text"
	}
	var parts []contentPart
	for _, p := range m.Content.Parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				parts = append(parts, contentPart{Type: textKind, Text: p.Text})
			}
		case "image_url":
			if p.ImageURL != nil && p.ImageURL.URL != "" {
				parts = append(parts, contentPart{Type: "input_image", ImageURL: p.ImageURL.URL})
			}
		}
	}
	return parts
}

func buildTools(tools []openai.Tool) []responsesTool {
	out := make([]responsesTool, 0, len(tools))
	for _, t := range tools {
		if t.Type != "function" || t.Function.Name == "" {
			continue
		}
		params := t.Function.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, responsesTool{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Strict:      false,
			Parameters:  params,
		})
	}
	return out
}

// --- stream translation ---

// streamResponse reads the Responses SSE stream and emits OpenAI chunks via
// sink. It emits every chat.completion.chunk including the final finish/usage
// chunk; the server owns the trailing "data: [DONE]" marker.
func streamResponse(r io.Reader, sink provider.ChunkSink, id, model string) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	roleSent := false
	finish := "stop"
	toolIndex := map[string]int{}
	nextToolIdx := 0
	var usg *usage

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
		final := mkChunk(&openai.Delta{}, &finish)
		if usg != nil {
			final.Usage = mapUsage(*usg)
		}
		_ = sink.Send(final)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		if ev.Response != nil && ev.Response.Usage != nil {
			usg = ev.Response.Usage
		}

		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				if err := sendRole(); err != nil {
					return err
				}
				if err := sink.Send(mkChunk(&openai.Delta{Content: ev.Delta}, nil)); err != nil {
					return err
				}
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if ev.Delta != "" {
				if err := sendRole(); err != nil {
					return err
				}
				if err := sink.Send(mkChunk(&openai.Delta{ReasoningContent: ev.Delta}, nil)); err != nil {
					return err
				}
			}
		case "response.output_item.done":
			if ev.Item == nil {
				continue
			}
			if ev.Item.Type == "function_call" || ev.Item.Type == "web_search_call" {
				callID := ev.Item.CallID
				if callID == "" {
					callID = ev.Item.ID
				}
				name := ev.Item.Name
				if name == "" && ev.Item.Type == "web_search_call" {
					name = "web_search"
				}
				idx, ok := toolIndex[callID]
				if !ok {
					idx = nextToolIdx
					toolIndex[callID] = idx
					nextToolIdx++
				}
				if err := sendRole(); err != nil {
					return err
				}
				delta := &openai.Delta{ToolCalls: []openai.ToolCallDelta{{
					Index: idx,
					ID:    callID,
					Type:  "function",
					Function: openai.ToolCallFunction{
						Name:      name,
						Arguments: argString(ev.Item.Arguments),
					},
				}}}
				if err := sink.Send(mkChunk(delta, nil)); err != nil {
					return err
				}
				finish = "tool_calls"
			}
		case "response.completed":
			finishAndClose()
			return nil
		case "response.failed", "error":
			msg := "upstream stream error"
			switch {
			case ev.Error != nil && ev.Error.Message != "":
				msg = ev.Error.Message
			case ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "":
				msg = ev.Response.Error.Message
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
	finishAndClose()
	return nil
}

// mapUsage converts Responses usage onto the OpenAI usage shape.
func mapUsage(u usage) *openai.Usage {
	out := &openai.Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = u.InputTokens + u.OutputTokens
	}
	if u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens > 0 {
		out.PromptTokensDetails = &openai.PromptTokensDetails{CachedTokens: u.InputTokensDetails.CachedTokens}
	}
	if u.OutputTokensDetails != nil && u.OutputTokensDetails.ReasoningTokens > 0 {
		out.CompletionTokensDetails = &openai.CompletionTokensDetails{ReasoningTokens: u.OutputTokensDetails.ReasoningTokens}
	}
	return out
}
