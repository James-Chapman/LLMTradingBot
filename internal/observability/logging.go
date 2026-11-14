// Package observability provides structured JSON logging and the dashboard
// activity log.
package observability

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// llmLoggerNames are the component names whose records are also written to
// llm.log so LLM traffic can be tailed separately.
var llmLoggerNames = map[string]bool{"llm_client": true, "llm_analyser": true}

var (
	mu       sync.RWMutex
	mainH    slog.Handler = slog.NewJSONHandler(os.Stdout, nil)
	llmH     slog.Handler // nil until SetupLogging opens llm.log
	closers  []io.Closer
	levelVar = new(slog.LevelVar)
)

// SetupLogging routes JSON logs to stdout plus logFile, and LLM component logs
// additionally to llm.log beside logFile. Call once at startup; the returned
// function closes the files.
func SetupLogging(level, logFile string) (func(), error) {
	levelVar.Set(ParseLevel(level))
	opts := &slog.HandlerOptions{Level: levelVar}

	mainFile, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	llmFile, err := os.OpenFile(filepath.Join(filepath.Dir(logFile), "llm.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		mainFile.Close()
		return nil, err
	}

	mu.Lock()
	mainH = slog.NewJSONHandler(io.MultiWriter(os.Stdout, mainFile), opts)
	llmH = slog.NewJSONHandler(llmFile, opts)
	closers = []io.Closer{mainFile, llmFile}
	mu.Unlock()

	return func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range closers {
			c.Close()
		}
		closers = nil
	}, nil
}

// SetLevel changes the minimum level of every logger (DEBUG/INFO/WARNING/ERROR).
// The bot starts logging at INFO and switches once its saved settings load.
func SetLevel(level string) { levelVar.Set(ParseLevel(level)) }

// ParseLevel converts DEBUG/INFO/WARNING/ERROR to a slog level (INFO default).
func ParseLevel(level string) slog.Level {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR", "CRITICAL":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Logger returns a component logger tagged logger="trading_bot.<name>".
// The handler is resolved per record, so loggers created before SetupLogging
// still write to the configured destinations.
func Logger(name string) *slog.Logger {
	return slog.New(&routingHandler{llm: llmLoggerNames[name]}).With("logger", "trading_bot."+name)
}

// routingHandler forwards to the current global handlers at log time.
type routingHandler struct {
	llm bool
	// steps are the WithAttrs/WithGroup calls in the order they were made;
	// they are replayed on the live handlers so groups nest and attributes
	// added before a group stay outside it, as slog.Handler requires.
	steps []handlerStep
}

// handlerStep is one WithAttrs (attrs set) or WithGroup (group set) call.
type handlerStep struct {
	attrs []slog.Attr
	group string
}

// current returns the live handlers this logger should write to.
func (h *routingHandler) current() []slog.Handler {
	mu.RLock()
	defer mu.RUnlock()
	hs := []slog.Handler{mainH}
	if h.llm && llmH != nil {
		hs = append(hs, llmH)
	}
	for i, inner := range hs {
		for _, step := range h.steps {
			if step.group != "" {
				inner = inner.WithGroup(step.group)
			} else {
				inner = inner.WithAttrs(step.attrs)
			}
		}
		hs[i] = inner
	}
	return hs
}

// Enabled reports whether level passes the configured minimum.
func (h *routingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= levelVar.Level()
}

// Handle writes the record to every destination handler.
func (h *routingHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, inner := range h.current() {
		if err := inner.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// WithAttrs returns a handler carrying extra attributes.
func (h *routingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.with(handlerStep{attrs: append([]slog.Attr{}, attrs...)})
}

// WithGroup returns a handler that nests subsequent attributes in a group
// inside any group opened before.
func (h *routingHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.with(handlerStep{group: name})
}

// with returns a copy of h with one more step; the step list is copied so
// sibling loggers never share it.
func (h *routingHandler) with(step handlerStep) *routingHandler {
	clone := *h
	clone.steps = append(append([]handlerStep{}, h.steps...), step)
	return &clone
}
