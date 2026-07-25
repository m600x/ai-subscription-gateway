package anthropic

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/config"
	"github.com/m600x/ai-subscription-gateway/internal/openai"
	"github.com/m600x/ai-subscription-gateway/internal/registry"
)

// BuildMessagesRequest maps an OpenAI request to an Anthropic MessagesRequest.
//
// The system prompt is assembled as an array whose FIRST block is exactly the
// configured spoof string ("You are Claude Code, Anthropic's official CLI for
// Claude."). This is mandatory: the subscription OAuth token is rejected
// (disguised HTTP 429) unless that exact block leads the system prompt. Any
// system messages the client sent are appended as subsequent blocks, so the
// user's own instructions still apply.
//
// Reasoning behavior is driven by the registry model m: its effort ladder
// (m.Reasoning.Efforts) gates which reasoning_effort values are honored, and
// its thinking mode (m.Reasoning.Mode) decides how an explicit "off" is
// treated.
func BuildMessagesRequest(req openai.ChatCompletionRequest, m registry.Model, cfg *config.Config) MessagesRequest {
	system := []SystemBlock{{Type: "text", Text: cfg.SpoofSystemPrompt}}
	var msgs []Message

	for _, mm := range req.Messages {
		switch mm.Role {
		case "system", "developer":
			if strings.TrimSpace(mm.Content.String()) != "" {
				system = append(system, SystemBlock{Type: "text", Text: mm.Content.String()})
			}
		case "tool":
			// Tool output continues the exchange as a tool_result block in a
			// user turn; coalesce groups consecutive results into one turn.
			if mm.ToolCallID == "" {
				continue
			}
			msgs = append(msgs, Message{Role: "user", Content: []ContentBlock{{
				Type: "tool_result", ToolUseID: mm.ToolCallID, Content: mm.Content.String(),
			}}})
		case "user", "assistant":
			if blocks := messageBlocks(mm); len(blocks) > 0 {
				msgs = append(msgs, Message{Role: mm.Role, Content: blocks})
			}
		}
	}
	msgs = coalesce(msgs)

	baseMax := cfg.DefaultMaxTokens
	if m.DefaultMaxTokens > 0 {
		baseMax = m.DefaultMaxTokens
	}
	maxTokens := baseMax
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		maxTokens = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	out := MessagesRequest{
		Model:     m.UpstreamID,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  msgs,
		Stream:    req.Stream,
	}

	clientTools := buildTools(req.Tools)
	out.Tools = clientTools
	if cfg.EnableWebSearch {
		out.Tools = append(out.Tools, Tool{Type: "web_search_20250305", Name: "web_search"})
	}
	if len(clientTools) > 0 {
		out.ToolChoice = mapToolChoice(req.ToolChoice, req.ParallelToolCalls)
	}

	thinking := false
	switch effort := resolveEffort(req.ReasoningEffort, m); effort {
	case "":
		// Default: no thinking config. Default-on models (Sonnet 5) and
		// always-on models (Fable 5) still think adaptively at their own
		// default effort; the rest stay fast.
	case "off":
		// Only default-on models need an explicit disable. Always-on models
		// reject one (so "off" is ignored) and opt-in models are already off.
		if m.Reasoning.Mode == registry.ModeDefaultOn {
			out.Thinking = &Thinking{Type: "disabled"}
		}
	default:
		thinking = true
		out.Thinking = &Thinking{Type: "adaptive", Display: cfg.ThinkingDisplay}
		out.OutputConfig = &OutputConfig{Effort: effort}
		// max_tokens caps thinking + response combined; leave headroom so
		// high-effort thinking cannot starve the visible answer.
		if out.MaxTokens < 4*baseMax {
			out.MaxTokens = 4 * baseMax
		}
	}

	// temperature/top_p are incompatible with active thinking.
	if !thinking {
		out.Temperature = req.Temperature
		out.TopP = req.TopP
	}

	return out
}

// messageBlocks converts a user/assistant message into content blocks: the
// text (if any) first, then the assistant's tool calls as tool_use blocks.
func messageBlocks(mm openai.ChatMessage) []ContentBlock {
	var blocks []ContentBlock
	if txt := mm.Content.String(); txt != "" {
		blocks = append(blocks, ContentBlock{Type: "text", Text: txt})
	}
	if mm.Role == "assistant" {
		for _, tc := range mm.ToolCalls {
			if tc.Type != "" && tc.Type != "function" {
				continue
			}
			blocks = append(blocks, ContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: toolInput(tc.Function.Arguments),
			})
		}
	}
	return blocks
}

// toolInput parses an OpenAI arguments string into the JSON object tool_use
// requires; anything unparseable degrades to {} rather than an error.
func toolInput(args string) json.RawMessage {
	s := strings.TrimSpace(args)
	var obj map[string]json.RawMessage
	if s == "" || json.Unmarshal([]byte(s), &obj) != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

// buildTools maps client function tools onto Anthropic tool definitions.
func buildTools(tools []openai.Tool) []Tool {
	var out []Tool
	for _, t := range tools {
		if t.Type != "function" || t.Function.Name == "" {
			continue
		}
		schema := t.Function.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}
	return out
}

// mapToolChoice converts the OpenAI tool_choice (string or object form) plus
// parallel_tool_calls into the Anthropic tool_choice object. Returns nil when
// nothing needs to be sent (auto with parallel calls allowed).
func mapToolChoice(raw json.RawMessage, parallel *bool) *ToolChoice {
	tc := &ToolChoice{Type: "auto"}
	if parallel != nil && !*parallel {
		tc.DisableParallelToolUse = true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "none":
			tc.Type = "none"
		case "required":
			tc.Type = "any"
		}
	} else if len(raw) > 0 {
		var obj struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &obj) == nil && obj.Function.Name != "" {
			tc.Type = "tool"
			tc.Name = obj.Function.Name
		}
	}
	if tc.Type == "auto" && !tc.DisableParallelToolUse {
		return nil
	}
	return tc
}

// normalizeEffort maps a reasoning_effort value onto the Anthropic effort
// ladder (low|medium|high|xhigh|max), "off" for an explicit disable, or ""
// when unspecified/unrecognized.
func normalizeEffort(effort string) string {
	switch e := strings.ToLower(strings.TrimSpace(effort)); e {
	case "minimal", "none", "off":
		return "off"
	case "low", "medium", "high", "xhigh", "max":
		return e
	case "extra-high", "extra_high", "xtra-high":
		return "xhigh"
	default:
		return ""
	}
}

// resolveEffort decides the effort for a request from the client's
// reasoning_effort, validated against the model's ladder. An effort the model
// does not advertise resolves to "" (no thinking config sent).
func resolveEffort(effort string, m registry.Model) string {
	e := normalizeEffort(effort)
	if e == "" || e == "off" {
		return e
	}
	if !m.AllowsEffort(e) {
		return ""
	}
	return e
}

// coalesce merges consecutive same-role messages (Anthropic requires
// alternating roles).
func coalesce(msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = mergeBlocks(out[n-1].Content, m.Content)
			continue
		}
		out = append(out, m)
	}
	return out
}

// mergeBlocks combines the blocks of two same-role turns. tool_result blocks
// move to the front (the API requires results to lead the user turn that
// answers a tool_use) and blocks that end up as adjacent text merge with a
// paragraph break, preserving the pre-block wire shape for plain text.
func mergeBlocks(a, b []ContentBlock) []ContentBlock {
	all := make([]ContentBlock, 0, len(a)+len(b))
	all = append(all, a...)
	all = append(all, b...)

	out := make([]ContentBlock, 0, len(all))
	for _, blk := range all {
		if blk.Type == "tool_result" {
			out = append(out, blk)
		}
	}
	for _, blk := range all {
		if blk.Type == "tool_result" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Type == "text" && blk.Type == "text" {
			out[n-1].Text += "\n\n" + blk.Text
			continue
		}
		out = append(out, blk)
	}
	return out
}

func mapStopReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

// BuildUsage maps Anthropic usage (incl. cache reads and thinking tokens)
// onto the OpenAI usage shape. Prompt tokens include cache reads/writes so
// the total reflects what was actually processed.
func BuildUsage(u Usage) *openai.Usage {
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	out := &openai.Usage{
		PromptTokens:     prompt,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      prompt + u.OutputTokens,
	}
	if u.CacheReadInputTokens > 0 {
		out.PromptTokensDetails = &openai.PromptTokensDetails{CachedTokens: u.CacheReadInputTokens}
	}
	if u.OutputTokensDetails != nil && u.OutputTokensDetails.ThinkingTokens > 0 {
		out.CompletionTokensDetails = &openai.CompletionTokensDetails{
			ReasoningTokens: u.OutputTokensDetails.ThinkingTokens,
		}
	}
	return out
}

// BuildChatCompletion maps a non-streaming Anthropic response to an OpenAI
// chat.completion.
func BuildChatCompletion(resp *MessagesResponse, id, model string) openai.ChatCompletion {
	var sb strings.Builder
	var toolCalls []openai.ToolCall
	for _, c := range resp.Content {
		switch c.Type {
		case "text":
			sb.WriteString(c.Text)
		case "tool_use":
			args := "{}"
			if len(c.Input) > 0 {
				args = string(c.Input)
			}
			toolCalls = append(toolCalls, openai.ToolCall{
				ID:       c.ID,
				Type:     "function",
				Function: openai.ToolCallFunction{Name: c.Name, Arguments: args},
			})
		}
	}
	finish := mapStopReason(resp.StopReason)
	return openai.ChatCompletion{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openai.Choice{{
			Index:        0,
			Message:      &openai.RespMessage{Role: "assistant", Content: sb.String(), ToolCalls: toolCalls},
			FinishReason: &finish,
		}},
		Usage: BuildUsage(resp.Usage),
	}
}
