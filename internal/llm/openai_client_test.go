package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

const testModel = "gpt-4o-mini"

// fakeOpenAIServer is a scriptable OpenAI-compatible HTTP server.
type fakeOpenAIServer struct {
	*httptest.Server
	mu          sync.Mutex
	content     string         // assistant message content returned by chat
	rawResponse map[string]any // when set, returned verbatim instead of a choices payload
	status      int            // HTTP status for chat-completions (default 200)
	models      []string       // ids returned by GET /models
	chatHits    atomic.Int32
	modelHits   atomic.Int32
	lastPayload map[string]any
	lastAuth    string
}

// newFakeOpenAIServer starts a fake server that answers chat with content.
func newFakeOpenAIServer(t *testing.T, content string) *fakeOpenAIServer {
	t.Helper()
	f := &fakeOpenAIServer{content: content, status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

// handle serves /v1/models and /v1/chat/completions.
func (f *fakeOpenAIServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
		f.modelHits.Add(1)
		data := make([]map[string]any, 0, len(f.models))
		for _, id := range f.models {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/completions"):
		f.chatHits.Add(1)
		body, _ := io.ReadAll(r.Body)
		f.lastPayload = map[string]any{}
		_ = json.Unmarshal(body, &f.lastPayload)
		f.lastAuth = r.Header.Get("Authorization")
		if f.status != http.StatusOK {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		if f.rawResponse != nil {
			_ = json.NewEncoder(w).Encode(f.rawResponse)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": f.content}}},
		})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// setContent changes the assistant content returned by chat.
func (f *fakeOpenAIServer) setContent(content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.content = content
}

// auth returns the last Authorization header received.
func (f *fakeOpenAIServer) auth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAuth
}

// payload returns the last chat-completions request body.
func (f *fakeOpenAIServer) payload() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPayload
}

// newTestClient builds a remote-style client pointed at baseURL.
func newTestClient(baseURL, apiKey, model string, clock func() time.Time) *OpenAICompatClient {
	return NewOpenAICompatClient(ClientConfig{
		Backend: BackendRemote,
		Label:   "Remote",
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		Timeout: 5 * time.Second,
		Clock:   clock,
	})
}

// TestOpenAICompatClientCircuit covers circuit breaker behaviour on the client.
func TestOpenAICompatClientCircuit(t *testing.T) {
	t.Run("GIVEN a failure WHEN the cooldown is active THEN the client does not attempt", func(t *testing.T) {
		clock := newFakeClock()
		client := newTestClient("http://localhost:1234/v1", "", testModel, clock.Now)

		client.markFailed("connection refused")

		if client.CircuitState() != StateOpen || client.CanAttempt() {
			t.Fatal("expected an open circuit that blocks attempts")
		}
		clock.Advance(29)
		if client.CanAttempt() {
			t.Fatal("expected attempts to stay blocked before the delay elapses")
		}
		clock.Advance(1)
		if !client.CanAttempt() || !client.shouldAttempt() || client.CircuitState() != StateHalfOpen {
			t.Fatal("expected a half-open attempt once the delay elapses")
		}
	})

	t.Run("GIVEN no base URL or model WHEN IsConfigured is checked THEN it is false", func(t *testing.T) {
		if newTestClient("", "", "", nil).IsConfigured() {
			t.Error("expected an unconfigured client")
		}
	})

	t.Run("GIVEN a base URL and model WHEN IsConfigured is checked THEN it is true", func(t *testing.T) {
		if !newTestClient("http://localhost:1234/v1", "key", testModel, nil).IsConfigured() {
			t.Error("expected a configured client")
		}
	})
}

// TestOpenAICompatClientChat covers chat-completions requests and replies.
func TestOpenAICompatClientChat(t *testing.T) {
	ctx := context.Background()
	msgs := []Message{{Role: "system", Content: "Return JSON."}, {Role: "user", Content: "analyse"}}

	t.Run("GIVEN the server returns valid JSON WHEN chat is called THEN a parsed object is returned", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, `{"sentiment": 0.4, "confidence_scale": 1.1, "reasoning": "bullish"}`)
		client := newTestClient(srv.URL+"/v1", "key", testModel, nil)

		reply, err := client.Chat(ctx, msgs, true)

		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if reply.JSON["sentiment"] != 0.4 {
			t.Errorf("sentiment = %v, want 0.4", reply.JSON["sentiment"])
		}
		if !client.Available() {
			t.Error("expected the client to be available after a successful call")
		}
	})

	t.Run("GIVEN a chat request WHEN sent THEN a chat-completions payload with model and messages is posted", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, `{"sentiment": 0.1}`)
		client := newTestClient(srv.URL+"/v1/", "key", testModel, nil)

		if _, err := client.Chat(ctx, msgs, true); err != nil {
			t.Fatalf("unexpected error %v", err)
		}

		payload := srv.payload()
		if payload["model"] != testModel {
			t.Errorf("model = %v, want %s", payload["model"], testModel)
		}
		sent, _ := payload["messages"].([]any)
		if len(sent) != 2 {
			t.Fatalf("messages = %v, want 2 entries", payload["messages"])
		}
		first, _ := sent[0].(map[string]any)
		if first["role"] != "system" || first["content"] != "Return JSON." {
			t.Errorf("first message = %v, want the system prompt", first)
		}
		if srv.auth() != "Bearer key" {
			t.Errorf("Authorization = %q, want bearer token", srv.auth())
		}
	})

	t.Run("GIVEN no API key WHEN chat is sent THEN no Authorization header is sent", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, `{"sentiment": 0.1}`)
		client := newTestClient(srv.URL+"/v1", "", testModel, nil)

		if _, err := client.Chat(ctx, msgs, true); err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if srv.auth() != "" {
			t.Errorf("Authorization = %q, want none", srv.auth())
		}
	})

	t.Run("GIVEN the server returns non-JSON WHEN chat expects JSON THEN an error is returned and the circuit stays closed", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, "I cannot answer that in JSON")
		client := newTestClient(srv.URL+"/v1", "key", testModel, nil)

		_, err := client.Chat(ctx, msgs, true)

		if !errors.Is(err, ErrInvalidJSON) {
			t.Fatalf("err = %v, want ErrInvalidJSON", err)
		}
		if !client.Available() || client.CircuitState() != StateClosed {
			t.Error("a JSON parse failure must not trip the circuit breaker")
		}
	})

	t.Run("GIVEN plain text is requested WHEN chat is called THEN the raw text is returned", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, "hello there")
		client := newTestClient(srv.URL+"/v1", "", testModel, nil)

		reply, err := client.Chat(ctx, msgs, false)

		if err != nil || reply.Text != "hello there" {
			t.Errorf("reply = %+v err = %v, want raw text", reply, err)
		}
	})

	t.Run("GIVEN the server is unreachable WHEN chat is called THEN an error is returned and the circuit opens", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, "{}")
		url := srv.URL + "/v1"
		srv.Close()
		client := newTestClient(url, "key", testModel, nil)
		client.markSuccess()

		_, err := client.Chat(ctx, msgs, true)

		if err == nil {
			t.Fatal("expected a network error")
		}
		if client.Available() || client.CircuitState() != StateOpen {
			t.Error("expected the circuit to open after a network error")
		}
	})

	t.Run("GIVEN the server returns HTTP 500 WHEN chat is called THEN the circuit opens", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, "{}")
		srv.status = http.StatusInternalServerError
		client := newTestClient(srv.URL+"/v1", "", testModel, nil)

		if _, err := client.Chat(ctx, msgs, true); err == nil {
			t.Fatal("expected an HTTP error")
		}
		if client.CircuitState() != StateOpen {
			t.Error("expected the circuit to open after an HTTP error")
		}
	})

	t.Run("GIVEN the server is slower than the timeout WHEN chat is called THEN it fails and the circuit opens", func(t *testing.T) {
		release := make(chan struct{})
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(slow.Close)
		t.Cleanup(func() { close(release) })
		client := NewOpenAICompatClient(ClientConfig{
			Backend: BackendRemote, Label: "Remote", BaseURL: slow.URL, Model: testModel,
			Timeout: 50 * time.Millisecond,
		})

		if _, err := client.Chat(ctx, msgs, true); err == nil {
			t.Fatal("expected a timeout error")
		}
		if client.CircuitState() != StateOpen {
			t.Error("expected the circuit to open after a timeout")
		}
	})

	t.Run("GIVEN the caller cancels during a request WHEN chat is called THEN it fails without opening the circuit", func(t *testing.T) {
		release := make(chan struct{})
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(slow.Close)
		t.Cleanup(func() { close(release) })
		client := newTestClient(slow.URL, "", testModel, newFakeClock().Now)
		callerCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()

		_, err := client.Chat(callerCtx, msgs, true)

		if err == nil {
			t.Fatal("expected an error when the caller's context ends")
		}
		if client.CircuitState() != StateClosed || !client.CanAttempt() {
			t.Errorf("circuit = %s, want closed: a caller cancellation is not a backend failure", client.CircuitState())
		}
	})

	t.Run("GIVEN an already cancelled caller context WHEN chat is called THEN the circuit stays closed", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, `{"sentiment": 0.1}`)
		client := newTestClient(srv.URL+"/v1", "", testModel, newFakeClock().Now)
		callerCtx, cancel := context.WithCancel(ctx)
		cancel()

		_, err := client.Chat(callerCtx, msgs, true)

		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if client.CircuitState() != StateClosed {
			t.Errorf("circuit = %s, want closed", client.CircuitState())
		}
	})

	t.Run("GIVEN an open circuit WHEN chat is called THEN no request is sent", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, "{}")
		client := newTestClient(srv.URL+"/v1", "", testModel, newFakeClock().Now)
		client.markFailed("timeout")

		_, err := client.Chat(ctx, msgs, true)

		if !errors.Is(err, ErrUnavailable) || srv.chatHits.Load() != 0 {
			t.Errorf("err = %v hits = %d, want ErrUnavailable and no request", err, srv.chatHits.Load())
		}
	})
}

// TestOpenAICompatClientProbe covers availability probing.
func TestOpenAICompatClientProbe(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN the client is not configured WHEN probed THEN it is unavailable without a network call", func(t *testing.T) {
		client := newTestClient("", "", "", nil)

		if client.Probe(ctx) || client.Available() {
			t.Error("expected an unconfigured probe to fail")
		}
	})

	t.Run("GIVEN a healthy server WHEN probed THEN the client becomes available", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, "p")
		client := newTestClient(srv.URL+"/v1", "key", testModel, nil)

		if !client.Probe(ctx) || !client.Available() || client.CircuitState() != StateClosed {
			t.Error("expected a successful probe to close the circuit")
		}
		if srv.payload()["max_tokens"] != 1.0 {
			t.Errorf("probe max_tokens = %v, want 1", srv.payload()["max_tokens"])
		}
	})

	t.Run("GIVEN HTTP 200 without choices WHEN probed THEN the client stays unavailable", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, "")
		srv.rawResponse = map[string]any{"error": "model not found"}
		client := newTestClient(srv.URL+"/v1", "key", testModel, nil)

		if client.Probe(ctx) || client.Available() || client.CircuitState() != StateOpen {
			t.Error("expected a probe without choices to open the circuit")
		}
	})
}

// TestLMStudioClient covers the LM Studio constructor and model auto-selection.
func TestLMStudioClient(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN no LMSTUDIO_MODEL WHEN probed THEN the first served model is selected and used", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, `{"ok": true}`)
		srv.models = []string{"qwen2.5-7b-instruct", "llama-3.2-3b"}
		cfg := config.Defaults()
		cfg.LMStudioBaseURL = srv.URL + "/v1"
		cfg.LMStudioModel = ""
		client := NewLMStudioClient(cfg)

		if !client.IsConfigured() {
			t.Fatal("LM Studio must count as configured without a model when auto-selecting")
		}
		if !client.Probe(ctx) {
			t.Fatal("expected the probe to succeed")
		}
		if client.Model() != "qwen2.5-7b-instruct" {
			t.Errorf("model = %q, want the first served model", client.Model())
		}
		if !strings.Contains(client.ModelName(), "qwen2.5-7b-instruct") || !strings.Contains(client.ModelName(), "LM Studio") {
			t.Errorf("model name = %q, want the model id and LM Studio label", client.ModelName())
		}
		if _, err := client.Chat(ctx, []Message{{Role: "user", Content: "hi"}}, true); err != nil {
			t.Fatalf("unexpected chat error %v", err)
		}
		if srv.payload()["model"] != "qwen2.5-7b-instruct" {
			t.Errorf("chat model = %v, want the auto-selected model", srv.payload()["model"])
		}
	})

	t.Run("GIVEN LM Studio serves no models WHEN probed THEN the client is unavailable", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, `{}`)
		cfg := config.Defaults()
		cfg.LMStudioBaseURL = srv.URL + "/v1"
		client := NewLMStudioClient(cfg)

		if client.Probe(ctx) || client.Available() {
			t.Error("expected the probe to fail with no models")
		}
		if srv.chatHits.Load() != 0 {
			t.Error("no chat request should be sent without a model")
		}
	})

	t.Run("GIVEN LMSTUDIO_MODEL is set WHEN probed THEN the configured model is used without listing", func(t *testing.T) {
		srv := newFakeOpenAIServer(t, "p")
		srv.models = []string{"other-model"}
		cfg := config.Defaults()
		cfg.LMStudioBaseURL = srv.URL + "/v1"
		cfg.LMStudioModel = "my-model"
		client := NewLMStudioClient(cfg)

		if !client.Probe(ctx) || client.Model() != "my-model" || srv.modelHits.Load() != 0 {
			t.Errorf("model = %q modelHits = %d, want configured model and no listing", client.Model(), srv.modelHits.Load())
		}
	})

	t.Run("GIVEN default settings WHEN clients are built THEN LM Studio is configured and remote is disabled", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.OpenAIBaseURL = ""
		cfg.OpenAIModel = "gpt-4o-mini"

		lm := NewLMStudioClient(cfg)
		remote := NewRemoteClient(cfg)

		if !lm.IsConfigured() || lm.Backend() != BackendLMStudio {
			t.Error("expected LM Studio to be configured from defaults")
		}
		if remote.IsConfigured() || remote.Backend() != BackendRemote {
			t.Error("expected the remote fallback to be disabled without OPENAI_BASE_URL")
		}
	})
}
