package llm

import (
	"context"
	"errors"
)

// Backend identifiers reported by SwitchingClient.ActiveBackend.
const (
	BackendLMStudio = "lmstudio"
	BackendRemote   = "remote"
	BackendNone     = "none"
)

// Sentinel errors returned by Chat.
var (
	// ErrUnavailable means no request was sent: the client is not configured
	// or its circuit breaker is open.
	ErrUnavailable = errors.New("llm: client unavailable")
	// ErrInvalidJSON means the model answered but the content was not a JSON
	// object; it does not trip the circuit breaker.
	ErrInvalidJSON = errors.New("llm: model returned non-JSON content")
)

// Message is one OpenAI-style chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Reply is a chat result: JSON is set when JSON was requested, Text otherwise.
type Reply struct {
	JSON map[string]any
	Text string
}

// Client is the interface the analyser depends on (port of the Python
// LLMClientProtocol). Implementations must be safe for concurrent use.
type Client interface {
	// Available reports whether the last call or probe succeeded.
	Available() bool
	// CanAttempt reports whether a call may be made now (closed or half-open circuit).
	CanAttempt() bool
	// Probe checks connectivity and updates availability.
	Probe(ctx context.Context) bool
	// Chat sends messages; with expectJSON the reply content is parsed as a JSON object.
	Chat(ctx context.Context, messages []Message, expectJSON bool) (Reply, error)
	// ModelName is a display name for the dashboard, e.g. "qwen2.5 (LM Studio)".
	ModelName() string
}

// Backend is a Client that can report whether it is configured at all; the
// SwitchingClient routes between two of these.
type Backend interface {
	Client
	// IsConfigured reports whether enough settings exist to attempt calls.
	IsConfigured() bool
}
