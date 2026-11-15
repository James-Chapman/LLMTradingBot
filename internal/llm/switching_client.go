package llm

import (
	"context"
	"errors"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// SwitchingClient routes calls to the preferred LM Studio backend and falls
// back to a remote OpenAI-compatible endpoint when LM Studio is unavailable.
//
// Note the roles are swapped versus the Python SwitchingLLMClient, which
// preferred the remote OpenAI endpoint and fell back to a local Transformers
// model (unloading it while OpenAI was active). In Go the local LM Studio
// server is preferred; nothing needs unloading because LM Studio manages its
// own model memory.
//
// Call RecheckPrimary periodically (e.g. on each news fetch) so LM Studio is
// reclaimed when it comes back online.
type SwitchingClient struct {
	primary  Backend
	fallback Backend
}

// NewSwitchingClient routes between primary (LM Studio) and fallback (remote).
func NewSwitchingClient(primary, fallback Backend) *SwitchingClient {
	return &SwitchingClient{primary: primary, fallback: fallback}
}

// NewSwitchingClientFromSettings builds LM Studio and remote clients from
// settings; the remote fallback is disabled when OPENAI_BASE_URL is empty.
func NewSwitchingClientFromSettings(s config.Settings, opts ...Option) *SwitchingClient {
	return NewSwitchingClient(NewLMStudioClient(s, opts...), NewRemoteClient(s, opts...))
}

// usable reports whether a backend is configured and can take a call now.
func usable(b Backend) bool {
	return b.IsConfigured() && b.CanAttempt()
}

// Available reports whether at least one backend is marked available.
func (s *SwitchingClient) Available() bool {
	return s.primary.Available() || s.fallback.Available()
}

// CanAttempt reports whether at least one configured backend can take a call now.
func (s *SwitchingClient) CanAttempt() bool {
	return usable(s.primary) || usable(s.fallback)
}

// ModelName returns the active backend's model name, or a configured one so
// the dashboard is not blank when nothing is available.
func (s *SwitchingClient) ModelName() string {
	if s.primary.Available() {
		return s.primary.ModelName()
	}
	if s.fallback.Available() {
		return s.fallback.ModelName()
	}
	if name := s.primary.ModelName(); name != "" {
		return name
	}
	return s.fallback.ModelName()
}

// ActiveBackend returns "lmstudio", "remote" or "none" for dashboard display.
func (s *SwitchingClient) ActiveBackend() string {
	if s.primary.Available() {
		return BackendLMStudio
	}
	if s.fallback.Available() {
		return BackendRemote
	}
	return BackendNone
}

// Probe tries LM Studio first and the remote fallback second.
func (s *SwitchingClient) Probe(ctx context.Context) bool {
	if s.primary.IsConfigured() && s.primary.Probe(ctx) {
		clientLogger.Info("LLM backend: LM Studio active", "model", s.primary.ModelName())
		return true
	}
	if s.fallback.IsConfigured() && s.fallback.Probe(ctx) {
		clientLogger.Info("LLM backend: remote fallback active", "model", s.fallback.ModelName())
		return true
	}
	return false
}

// Chat prefers LM Studio and silently falls back to the remote endpoint for
// any individual call that LM Studio cannot answer. When the caller's context
// has ended there is no point retrying on the fallback, so the error returns.
func (s *SwitchingClient) Chat(ctx context.Context, messages []Message, expectJSON bool) (Reply, error) {
	var primaryErr error
	if usable(s.primary) {
		reply, err := s.primary.Chat(ctx, messages, expectJSON)
		if err == nil {
			return reply, nil
		}
		if ctx.Err() != nil {
			return Reply{}, err
		}
		primaryErr = err
	}
	if usable(s.fallback) {
		return s.fallback.Chat(ctx, messages, expectJSON)
	}
	if primaryErr != nil {
		return Reply{}, primaryErr
	}
	return Reply{}, errors.Join(ErrUnavailable, errors.New("no LLM backend can attempt a call"))
}

// RecheckPrimary re-probes LM Studio and logs a switch when its availability
// changes; when it disappears the remote fallback is probed so it is ready.
func (s *SwitchingClient) RecheckPrimary(ctx context.Context) {
	if !s.primary.IsConfigured() {
		return
	}
	wasAvailable := s.primary.Available()
	ok := s.primary.Probe(ctx)
	switch {
	case ok && !wasAvailable:
		clientLogger.Info("LM Studio back online — switching to LM Studio", "model", s.primary.ModelName())
	case !ok && wasAvailable:
		clientLogger.Info("LM Studio unavailable — switching to remote fallback")
		if s.fallback.IsConfigured() {
			s.fallback.Probe(ctx)
		}
	}
}

// Compile-time checks that the clients satisfy the interfaces.
var (
	_ Backend = (*OpenAICompatClient)(nil)
	_ Client  = (*SwitchingClient)(nil)
)
