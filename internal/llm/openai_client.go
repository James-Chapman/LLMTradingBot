package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

// clientLogger also writes to llm.log (see observability.llmLoggerNames).
var clientLogger = observability.Logger("llm_client")

const (
	defaultTimeout   = 30 * time.Second
	maxProbeTimeout  = 10 * time.Second
	maxResponseBytes = 10 << 20
)

// ClientConfig configures an OpenAICompatClient.
type ClientConfig struct {
	Backend         string        // BackendLMStudio or BackendRemote
	Label           string        // display label, e.g. "LM Studio"
	BaseURL         string        // e.g. http://localhost:1234/v1
	APIKey          string        // optional; sent as a Bearer token when set
	Model           string        // model id; may be empty when AutoSelectModel is set
	AutoSelectModel bool          // pick the first id from GET /models on probe
	Timeout         time.Duration // per chat request; probes use min(Timeout, 10s)
	Clock           func() time.Time
	HTTPClient      *http.Client
}

// Option adjusts a ClientConfig built from settings.
type Option func(*ClientConfig)

// WithClock injects the circuit breaker clock (used by tests).
func WithClock(clock func() time.Time) Option {
	return func(c *ClientConfig) { c.Clock = clock }
}

// WithHTTPClient injects the HTTP client used for requests.
func WithHTTPClient(client *http.Client) Option {
	return func(c *ClientConfig) { c.HTTPClient = client }
}

// OpenAICompatClient calls any OpenAI-compatible chat-completions endpoint
// (LM Studio, OpenAI, llama.cpp server, ...). It is safe for concurrent use.
type OpenAICompatClient struct {
	CircuitBreaker
	backend    string
	label      string
	baseURL    string
	apiKey     string
	autoSelect bool
	timeout    time.Duration
	httpClient *http.Client

	modelMu sync.RWMutex
	model   string
}

// NewOpenAICompatClient builds a client from an explicit configuration.
func NewOpenAICompatClient(cfg ClientConfig) *OpenAICompatClient {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	c := &OpenAICompatClient{
		backend:    cfg.Backend,
		label:      cfg.Label,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		autoSelect: cfg.AutoSelectModel,
		timeout:    cfg.Timeout,
		httpClient: cfg.HTTPClient,
		model:      cfg.Model,
	}
	c.initCircuitBreaker(cfg.Clock, c.logFailure)
	return c
}

// NewLMStudioClient builds the primary LM Studio client from settings. When
// LMSTUDIO_MODEL is empty the first model served by /v1/models is used.
func NewLMStudioClient(s config.Settings, opts ...Option) *OpenAICompatClient {
	cfg := ClientConfig{
		Backend:         BackendLMStudio,
		Label:           "LM Studio",
		BaseURL:         s.LMStudioBaseURL,
		APIKey:          s.LMStudioAPIKey,
		Model:           s.LMStudioModel,
		AutoSelectModel: s.LMStudioModel == "",
		Timeout:         time.Duration(s.LMStudioTimeout) * time.Second,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return NewOpenAICompatClient(cfg)
}

// NewRemoteClient builds the remote OpenAI-compatible fallback client from
// settings. It is unconfigured (never used) when OPENAI_BASE_URL is empty.
func NewRemoteClient(s config.Settings, opts ...Option) *OpenAICompatClient {
	cfg := ClientConfig{
		Backend: BackendRemote,
		Label:   "Remote",
		BaseURL: s.OpenAIBaseURL,
		APIKey:  s.OpenAIAPIKey,
		Model:   s.OpenAIModel,
		Timeout: time.Duration(s.OpenAITimeout) * time.Second,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return NewOpenAICompatClient(cfg)
}

// Backend returns the backend identifier (lmstudio or remote).
func (c *OpenAICompatClient) Backend() string {
	return c.backend
}

// BaseURL returns the endpoint base URL without a trailing slash.
func (c *OpenAICompatClient) BaseURL() string {
	return c.baseURL
}

// Model returns the configured or auto-selected model id.
func (c *OpenAICompatClient) Model() string {
	c.modelMu.RLock()
	defer c.modelMu.RUnlock()
	return c.model
}

// setModel records the model id chosen by auto-selection.
func (c *OpenAICompatClient) setModel(model string) {
	c.modelMu.Lock()
	defer c.modelMu.Unlock()
	c.model = model
}

// IsConfigured reports whether a base URL and a model (or auto-selection) exist.
func (c *OpenAICompatClient) IsConfigured() bool {
	return c.baseURL != "" && (c.autoSelect || c.Model() != "")
}

// ModelName returns "<model> (<label>)" for display, or "" when no model is known.
func (c *OpenAICompatClient) ModelName() string {
	model := c.Model()
	if model == "" {
		return ""
	}
	return fmt.Sprintf("%s (%s)", model, c.label)
}

// logFailure logs a tripped circuit: connection problems at warning level,
// other errors (e.g. HTTP 400) at info to avoid log noise.
func (c *OpenAICompatClient) logFailure(reason string, retryDelay time.Duration) {
	lower := strings.ToLower(reason)
	if strings.Contains(lower, "connection") || strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline") {
		clientLogger.Warn(c.label+" client unavailable", "reason", reason, "retry_in", retryDelay.String())
		return
	}
	clientLogger.Info(c.label+" client failed (non-critical); circuit tripped", "reason", reason)
}

// Probe resolves the model (auto-selecting on LM Studio when needed) and sends
// a one-token request, requiring a "choices" field in the response.
func (c *OpenAICompatClient) Probe(ctx context.Context) bool {
	if !c.IsConfigured() {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, min(c.timeout, maxProbeTimeout))
	defer cancel()

	clientLogger.Debug("Probing "+c.label+" endpoint for availability", "url", c.baseURL)
	model, err := c.resolveModel(ctx)
	if err != nil {
		c.markFailed(err.Error())
		return false
	}
	data, err := c.postChat(ctx, chatRequest{
		Model:     model,
		Messages:  []Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	})
	if err != nil {
		c.markFailed(err.Error())
		return false
	}
	if _, ok := data["choices"]; !ok {
		c.markFailed("unexpected response (no 'choices'): " + truncate(fmt.Sprint(data), 120))
		return false
	}
	c.markSuccess()
	clientLogger.Info(c.label+" client available", "url", c.baseURL, "model", model)
	return true
}

// Chat posts messages to /chat/completions. With expectJSON the content is
// parsed tolerantly into an object; a parse failure returns ErrInvalidJSON
// without tripping the circuit. Transport and HTTP errors trip the circuit.
func (c *OpenAICompatClient) Chat(ctx context.Context, messages []Message, expectJSON bool) (Reply, error) {
	if !c.shouldAttempt() || !c.IsConfigured() {
		return Reply{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	model := c.Model()
	if model == "" {
		resolved, err := c.resolveModel(ctx)
		if err != nil {
			c.markFailed(err.Error())
			return Reply{}, err
		}
		model = resolved
	}

	// Only build the prompt transcript when debug logging is enabled.
	if clientLogger.Enabled(ctx, slog.LevelDebug) {
		clientLogger.Debug("Sending LLM request", "backend", c.backend, "prompt", MessagesToPrompt(messages, false))
	}
	data, err := c.postChat(ctx, chatRequest{Model: model, Messages: messages})
	if err != nil {
		c.markFailed(err.Error())
		return Reply{}, err
	}
	clientLogger.Debug("Received LLM response", "backend", c.backend, "response", data)

	content, err := firstChoiceContent(data)
	if err != nil {
		c.markFailed(err.Error())
		return Reply{}, err
	}
	c.markSuccess()
	if !expectJSON {
		return Reply{Text: content}, nil
	}
	parsed, err := LoadsModelJSON(content)
	if err != nil {
		clientLogger.Warn(c.label+" returned non-JSON", "error", err.Error(), "content", truncate(content, 200))
		return Reply{}, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	return Reply{JSON: parsed}, nil
}

// chatRequest is the chat-completions request body. response_format is not
// sent: the Python client relied on the system prompt plus tolerant parsing,
// and LM Studio rejects {"type":"json_object"}.
type chatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

// resolveModel returns the model to use, re-listing /models on LM Studio when
// auto-selecting so a model swapped in LM Studio is picked up.
func (c *OpenAICompatClient) resolveModel(ctx context.Context) (string, error) {
	if !c.autoSelect {
		return c.Model(), nil
	}
	ids, err := c.listModels(ctx)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("%s at %s serves no models; load one or set LMSTUDIO_MODEL", c.label, c.baseURL)
	}
	if previous := c.Model(); previous != ids[0] {
		c.setModel(ids[0])
		clientLogger.Info(c.label+" model auto-selected", "model", ids[0], "previous", previous, "available", ids)
	}
	return ids[0], nil
}

// listModels returns the model ids served by GET /models.
func (c *OpenAICompatClient) listModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode /models response: %w", err)
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// postChat sends a chat-completions request and decodes the JSON body.
func (c *OpenAICompatClient) postChat(ctx context.Context, payload chatRequest) (map[string]any, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode chat response: %w", err)
	}
	return data, nil
}

// setHeaders adds the JSON content type and the optional bearer token.
func (c *OpenAICompatClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// do executes req and returns the body, treating non-2xx statuses as errors.
func (c *OpenAICompatClient) do(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, req.URL.Path, truncate(string(body), 200))
	}
	return body, nil
}

// firstChoiceContent extracts choices[0].message.content from a response.
func firstChoiceContent(data map[string]any) (string, error) {
	choices, _ := data["choices"].([]any)
	if len(choices) == 0 {
		return "", errors.New("unexpected response: no choices")
	}
	choice, _ := choices[0].(map[string]any)
	message, ok := choice["message"].(map[string]any)
	if !ok {
		return "", errors.New("unexpected response: choice has no message")
	}
	switch content := message["content"].(type) {
	case string:
		return content, nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("unexpected response: content is %T", content)
	}
}

// truncate shortens s to at most n runes.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
