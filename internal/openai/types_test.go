package openai

import (
	"encoding/json"
	"testing"
)

func TestContentString(t *testing.T) {
	var m ChatMessage
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hi"}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content.String() != "hi" {
		t.Errorf("got %q", m.Content.String())
	}
	if len(m.Content.Parts) != 1 || m.Content.Parts[0].Type != "text" {
		t.Errorf("string content should yield one text part; got %+v", m.Content.Parts)
	}
}

func TestContentArrayParts(t *testing.T) {
	// OpenAI multimodal form: text is flattened for the text-only path, and the
	// structured parts (incl. images) are retained for the OpenAI path.
	raw := `{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"b"}]}`
	var m ChatMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content.String() != "ab" {
		t.Errorf("flattened text = %q, want %q", m.Content.String(), "ab")
	}
	if len(m.Content.Parts) != 3 {
		t.Fatalf("want 3 retained parts, got %d", len(m.Content.Parts))
	}
	if m.Content.Parts[1].Type != "image_url" || m.Content.Parts[1].ImageURL == nil || m.Content.Parts[1].ImageURL.URL != "x" {
		t.Errorf("image part not preserved; got %+v", m.Content.Parts[1])
	}
}

func TestContentNull(t *testing.T) {
	var m ChatMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":null}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content.String() != "" {
		t.Errorf("got %q, want empty", m.Content.String())
	}
}

func TestToolCallsDecode(t *testing.T) {
	raw := `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"paris\"}"}}]}`
	var m ChatMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool calls not decoded; got %+v", m.ToolCalls)
	}
}
