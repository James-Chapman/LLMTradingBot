package llm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// fakeBackend is a scriptable Backend for switching tests.
type fakeBackend struct {
	mu         sync.Mutex
	configured bool
	available  bool
	canAttempt bool
	probeOK    bool
	reply      *Reply
	name       string
	probeCalls int
	chatCalls  int
}

// newFakeBackend returns a configured backend with the given availability.
func newFakeBackend(name string, available bool) *fakeBackend {
	return &fakeBackend{configured: true, available: available, canAttempt: true, name: name}
}

// Available reports the scripted availability.
func (f *fakeBackend) Available() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

// CanAttempt reports whether the scripted circuit allows a call.
func (f *fakeBackend) CanAttempt() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.canAttempt
}

// IsConfigured reports the scripted configuration state.
func (f *fakeBackend) IsConfigured() bool {
	return f.configured
}

// ModelName returns the scripted display name.
func (f *fakeBackend) ModelName() string {
	return f.name
}

// Probe applies the scripted probe outcome to availability.
func (f *fakeBackend) Probe(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probeCalls++
	f.available = f.probeOK
	f.canAttempt = f.probeOK
	return f.probeOK
}

// Chat returns the scripted reply, or ErrUnavailable when none is set.
func (f *fakeBackend) Chat(context.Context, []Message, bool) (Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatCalls++
	if f.reply == nil {
		return Reply{}, ErrUnavailable
	}
	return *f.reply, nil
}

// jsonReply builds a Reply carrying a single sentiment field.
func jsonReply(sentiment float64) *Reply {
	return &Reply{JSON: map[string]any{"sentiment": sentiment}}
}

// TestSwitchingClientProbe covers backend selection at startup.
func TestSwitchingClientProbe(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN LM Studio is reachable WHEN probed THEN LM Studio is active", func(t *testing.T) {
		primary, fallback := newFakeBackend("m (LM Studio)", false), newFakeBackend("r (Remote)", false)
		primary.probeOK = true
		sw := NewSwitchingClient(primary, fallback)

		if !sw.Probe(ctx) || sw.ActiveBackend() != BackendLMStudio || !sw.Available() {
			t.Errorf("active = %s, want lmstudio", sw.ActiveBackend())
		}
		if fallback.probeCalls != 0 {
			t.Error("the remote fallback should not be probed while LM Studio is up")
		}
	})

	t.Run("GIVEN LM Studio is unreachable WHEN probed THEN the remote fallback is active", func(t *testing.T) {
		primary, fallback := newFakeBackend("m", false), newFakeBackend("r", false)
		fallback.probeOK = true
		sw := NewSwitchingClient(primary, fallback)

		if !sw.Probe(ctx) || sw.ActiveBackend() != BackendRemote {
			t.Errorf("active = %s, want remote", sw.ActiveBackend())
		}
	})

	t.Run("GIVEN neither backend is reachable WHEN probed THEN the client is unavailable", func(t *testing.T) {
		sw := NewSwitchingClient(newFakeBackend("m", false), newFakeBackend("r", false))

		if sw.Probe(ctx) || sw.Available() || sw.ActiveBackend() != BackendNone {
			t.Errorf("active = %s, want none", sw.ActiveBackend())
		}
	})

	t.Run("GIVEN the remote fallback is not configured WHEN LM Studio is down THEN it is never probed", func(t *testing.T) {
		primary, fallback := newFakeBackend("m", false), newFakeBackend("", false)
		fallback.configured = false
		fallback.probeOK = true
		sw := NewSwitchingClient(primary, fallback)

		if sw.Probe(ctx) || fallback.probeCalls != 0 {
			t.Error("an unconfigured fallback must not be probed")
		}
	})
}

// TestSwitchingClientChat covers per-call routing between backends.
func TestSwitchingClientChat(t *testing.T) {
	ctx := context.Background()
	msgs := []Message{{Role: "user", Content: "analyse"}}

	t.Run("GIVEN LM Studio is available WHEN chat is called THEN LM Studio is used", func(t *testing.T) {
		primary, fallback := newFakeBackend("m", true), newFakeBackend("r", true)
		primary.reply, fallback.reply = jsonReply(0.5), jsonReply(-0.1)
		sw := NewSwitchingClient(primary, fallback)

		reply, err := sw.Chat(ctx, msgs, true)

		if err != nil || reply.JSON["sentiment"] != 0.5 {
			t.Fatalf("reply = %+v err = %v, want LM Studio reply", reply, err)
		}
		if primary.chatCalls != 1 || fallback.chatCalls != 0 {
			t.Errorf("calls primary=%d fallback=%d, want 1/0", primary.chatCalls, fallback.chatCalls)
		}
	})

	t.Run("GIVEN LM Studio fails a call WHEN chat is called THEN the remote fallback answers", func(t *testing.T) {
		primary, fallback := newFakeBackend("m", true), newFakeBackend("r", true)
		fallback.reply = jsonReply(0.2)
		sw := NewSwitchingClient(primary, fallback)

		reply, err := sw.Chat(ctx, msgs, true)

		if err != nil || reply.JSON["sentiment"] != 0.2 || fallback.chatCalls != 1 {
			t.Errorf("reply = %+v err = %v, want fallback reply", reply, err)
		}
	})

	t.Run("GIVEN only the remote can attempt WHEN chat is called THEN the remote is used", func(t *testing.T) {
		primary, fallback := newFakeBackend("m", false), newFakeBackend("r", true)
		primary.canAttempt = false
		fallback.reply = jsonReply(0.3)
		sw := NewSwitchingClient(primary, fallback)

		reply, err := sw.Chat(ctx, msgs, true)

		if err != nil || reply.JSON["sentiment"] != 0.3 || primary.chatCalls != 0 {
			t.Errorf("reply = %+v err = %v primaryCalls = %d", reply, err, primary.chatCalls)
		}
	})

	t.Run("GIVEN no backend can attempt WHEN chat is called THEN ErrUnavailable is returned", func(t *testing.T) {
		primary, fallback := newFakeBackend("m", false), newFakeBackend("r", false)
		primary.canAttempt, fallback.canAttempt = false, false
		sw := NewSwitchingClient(primary, fallback)

		if _, err := sw.Chat(ctx, msgs, true); err == nil {
			t.Error("expected an error when no backend can attempt")
		}
		if sw.CanAttempt() {
			t.Error("CanAttempt must be false when neither backend can attempt")
		}
	})
}

// TestSwitchingClientRecheck covers periodic re-probing of LM Studio.
func TestSwitchingClientRecheck(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN LM Studio was down WHEN recheck finds it online THEN LM Studio is active again", func(t *testing.T) {
		primary, fallback := newFakeBackend("m", false), newFakeBackend("r", true)
		primary.probeOK = true
		sw := NewSwitchingClient(primary, fallback)

		sw.RecheckPrimary(ctx)

		if sw.ActiveBackend() != BackendLMStudio {
			t.Errorf("active = %s, want lmstudio", sw.ActiveBackend())
		}
	})

	t.Run("GIVEN LM Studio was up WHEN recheck finds it gone THEN the remote fallback is probed and active", func(t *testing.T) {
		primary, fallback := newFakeBackend("m", true), newFakeBackend("r", false)
		fallback.probeOK = true
		sw := NewSwitchingClient(primary, fallback)

		sw.RecheckPrimary(ctx)

		if primary.Available() || !fallback.Available() || sw.ActiveBackend() != BackendRemote {
			t.Errorf("active = %s, want remote", sw.ActiveBackend())
		}
	})

	t.Run("GIVEN LM Studio is not configured WHEN recheck runs THEN nothing is probed", func(t *testing.T) {
		primary, fallback := newFakeBackend("", false), newFakeBackend("r", true)
		primary.configured = false
		sw := NewSwitchingClient(primary, fallback)

		sw.RecheckPrimary(ctx)

		if primary.probeCalls != 0 || fallback.probeCalls != 0 || !fallback.Available() {
			t.Error("recheck must be a no-op when LM Studio is not configured")
		}
	})
}

// TestSwitchingClientModelName covers the dashboard model label.
func TestSwitchingClientModelName(t *testing.T) {
	t.Run("GIVEN LM Studio is active WHEN the model name is read THEN it is the LM Studio label", func(t *testing.T) {
		sw := NewSwitchingClient(newFakeBackend("m (LM Studio)", true), newFakeBackend("r (Remote)", false))

		if !strings.Contains(sw.ModelName(), "LM Studio") {
			t.Errorf("model name = %q, want LM Studio label", sw.ModelName())
		}
	})

	t.Run("GIVEN only the remote is active WHEN the model name is read THEN it is the remote label", func(t *testing.T) {
		sw := NewSwitchingClient(newFakeBackend("m (LM Studio)", false), newFakeBackend("r (Remote)", true))

		if !strings.Contains(sw.ModelName(), "Remote") {
			t.Errorf("model name = %q, want Remote label", sw.ModelName())
		}
	})

	t.Run("GIVEN nothing is active WHEN the model name is read THEN a configured name is still shown", func(t *testing.T) {
		sw := NewSwitchingClient(newFakeBackend("", false), newFakeBackend("r (Remote)", false))

		if sw.ModelName() != "r (Remote)" {
			t.Errorf("model name = %q, want the configured fallback name", sw.ModelName())
		}
	})
}

// TestSwitchingClientOverHTTP exercises real clients against fake LM Studio and remote servers.
func TestSwitchingClientOverHTTP(t *testing.T) {
	ctx := context.Background()
	msgs := []Message{{Role: "user", Content: "analyse"}}

	// build wires a SwitchingClient from settings pointing at the given URLs.
	build := func(lmURL, remoteURL string, clock *fakeClock) *SwitchingClient {
		cfg := config.Defaults()
		cfg.LMStudioBaseURL = lmURL
		cfg.LMStudioModel = "local-model"
		cfg.OpenAIBaseURL = remoteURL
		cfg.OpenAIModel = testModel
		return NewSwitchingClientFromSettings(cfg, WithClock(clock.Now))
	}

	t.Run("GIVEN LM Studio is down WHEN probed and chatting THEN the remote fallback is used", func(t *testing.T) {
		lm := newFakeOpenAIServer(t, `{"sentiment": 0.9}`)
		lmURL := lm.URL + "/v1"
		lm.Close()
		remote := newFakeOpenAIServer(t, `{"sentiment": -0.2}`)
		sw := build(lmURL, remote.URL+"/v1", newFakeClock())

		if !sw.Probe(ctx) || sw.ActiveBackend() != BackendRemote {
			t.Fatalf("active = %s, want remote", sw.ActiveBackend())
		}
		reply, err := sw.Chat(ctx, msgs, true)
		if err != nil || reply.JSON["sentiment"] != -0.2 {
			t.Errorf("reply = %+v err = %v, want the remote reply", reply, err)
		}
	})

	t.Run("GIVEN LM Studio recovers WHEN RecheckPrimary runs THEN LM Studio is reclaimed", func(t *testing.T) {
		clock := newFakeClock()
		lm := newFakeOpenAIServer(t, `{"sentiment": 0.9}`)
		lm.status = 503
		remote := newFakeOpenAIServer(t, `{"sentiment": -0.2}`)
		sw := build(lm.URL+"/v1", remote.URL+"/v1", clock)

		sw.Probe(ctx)
		if sw.ActiveBackend() != BackendRemote {
			t.Fatalf("active = %s, want remote while LM Studio is down", sw.ActiveBackend())
		}

		lm.mu.Lock()
		lm.status = 200
		lm.mu.Unlock()
		clock.Advance(int((time.Minute).Seconds()))
		sw.RecheckPrimary(ctx)

		if sw.ActiveBackend() != BackendLMStudio {
			t.Fatalf("active = %s, want lmstudio after recovery", sw.ActiveBackend())
		}
		reply, err := sw.Chat(ctx, msgs, true)
		if err != nil || reply.JSON["sentiment"] != 0.9 {
			t.Errorf("reply = %+v err = %v, want the LM Studio reply", reply, err)
		}
	})
}
