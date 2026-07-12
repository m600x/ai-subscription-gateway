package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m600x/ai-subscription-gateway/internal/config"
	"github.com/m600x/ai-subscription-gateway/internal/openai"
	"github.com/m600x/ai-subscription-gateway/internal/provider"
	"github.com/m600x/ai-subscription-gateway/internal/registry"
)

// fakeHTTPErr implements provider.HTTPError.
type fakeHTTPErr struct {
	status int
	typ    string
	msg    string
}

func (e *fakeHTTPErr) Error() string   { return e.msg }
func (e *fakeHTTPErr) HTTPStatus() int { return e.status }
func (e *fakeHTTPErr) ErrType() string { return e.typ }

// fakeProvider is a controllable provider.Provider for exercising the server's
// routing and error-mapping without a real upstream.
type fakeProvider struct {
	completeErr error
	streamErr   error // returned before any chunk is sent
	chunks      []openai.ChatCompletion
}

func (f *fakeProvider) Name() string { return registry.ProviderOpenAI }

func (f *fakeProvider) Complete(_ context.Context, _ openai.ChatCompletionRequest, _ registry.Model) (openai.ChatCompletion, error) {
	if f.completeErr != nil {
		return openai.ChatCompletion{}, f.completeErr
	}
	stop := "stop"
	return openai.ChatCompletion{
		ID: "x", Object: "chat.completion",
		Choices: []openai.Choice{{Message: &openai.RespMessage{Role: "assistant", Content: "hi"}, FinishReason: &stop}},
	}, nil
}

func (f *fakeProvider) Stream(_ context.Context, _ openai.ChatCompletionRequest, _ registry.Model, sink provider.ChunkSink) error {
	if f.streamErr != nil {
		return f.streamErr
	}
	for _, c := range f.chunks {
		if err := sink.Send(c); err != nil {
			return err
		}
	}
	return nil
}

// serverWithProvider wires the OpenAI slot to prov and routes gpt-5-codex to it.
func serverWithProvider(t *testing.T, prov provider.Provider) *httptest.Server {
	t.Helper()
	cfg := &config.Config{ClientAPIKey: "ck", DefaultModel: "gpt-5-codex", RequestTimeout: 5 * time.Second}
	reg := testRegistry(t)
	providers := map[string]provider.Provider{registry.ProviderOpenAI: prov}
	enabled := map[string]bool{registry.ProviderOpenAI: true}
	return httptest.NewServer(New(cfg, reg, providers, enabled))
}

func TestUpstreamHTTPErrorMapsStatusAndType(t *testing.T) {
	ts := serverWithProvider(t, &fakeProvider{completeErr: &fakeHTTPErr{status: 429, typ: "rate_limit_error", msg: "slow down"}})
	defer ts.Close()

	resp := post(t, ts.URL+"/v1/chat/completions", "ck",
		`{"model":"gpt-5-codex","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Fatalf("status = %d, want 429 (upstream status passed through)", resp.StatusCode)
	}
	var er openai.ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&er)
	if er.Error.Message != "slow down" || er.Error.Type != "rate_limit_error" {
		t.Errorf("error body = %+v", er.Error)
	}
}

func TestUpstreamNonHTTPErrorIsBadGateway(t *testing.T) {
	ts := serverWithProvider(t, &fakeProvider{completeErr: errors.New("boom")})
	defer ts.Close()

	resp := post(t, ts.URL+"/v1/chat/completions", "ck",
		`{"model":"gpt-5-codex","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Fatalf("status = %d, want 502 (opaque upstream error)", resp.StatusCode)
	}
	var er openai.ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&er)
	if er.Error.Type != "upstream_error" {
		t.Errorf("type = %q, want upstream_error", er.Error.Type)
	}
}

func TestStreamErrorBeforeFirstChunkSetsStatus(t *testing.T) {
	// An upstream error that arrives before any chunk should surface as a real
	// HTTP status, not a 200 stream.
	ts := serverWithProvider(t, &fakeProvider{streamErr: &fakeHTTPErr{status: 503, typ: "overloaded_error", msg: "try later"}})
	defer ts.Close()

	resp := post(t, ts.URL+"/v1/chat/completions", "ck",
		`{"model":"gpt-5-codex","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestStreamSuccessThroughProvider(t *testing.T) {
	role := "assistant"
	stop := "stop"
	chunks := []openai.ChatCompletion{
		{Object: "chat.completion.chunk", Choices: []openai.Choice{{Delta: &openai.Delta{Role: role}}}},
		{Object: "chat.completion.chunk", Choices: []openai.Choice{{Delta: &openai.Delta{Content: "hello"}}}},
		{Object: "chat.completion.chunk", Choices: []openai.Choice{{Delta: &openai.Delta{}, FinishReason: &stop}}},
	}
	ts := serverWithProvider(t, &fakeProvider{chunks: chunks})
	defer ts.Close()

	resp := post(t, ts.URL+"/v1/chat/completions", "ck",
		`{"model":"gpt-5-codex","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	out := string(b)
	for _, want := range []string{`"content":"hello"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q\n%s", want, out)
		}
	}
}
