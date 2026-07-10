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
	if m.Content != "hi" {
		t.Errorf("got %q", m.Content)
	}
}

func TestContentArrayParts(t *testing.T) {
	// OpenAI multimodal form: text parts are concatenated, images ignored.
	raw := `{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"b"}]}`
	var m ChatMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content != "ab" {
		t.Errorf("got %q, want %q", m.Content, "ab")
	}
}

func TestContentNull(t *testing.T) {
	var m ChatMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":null}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content != "" {
		t.Errorf("got %q, want empty", m.Content)
	}
}
