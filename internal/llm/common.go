// Package llm talks to OpenAI-compatible chat-completion servers (LM Studio
// as the primary backend, a remote endpoint as fallback) and builds the
// trading prompts used by the strategies.
package llm

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	initialRetryDelay = 30 * time.Second
	maxRetryDelay     = 5 * time.Minute
)

// utcNow returns the current time in UTC; it is the default breaker clock.
func utcNow() time.Time {
	return time.Now().UTC()
}

// MessagesToPrompt renders chat messages as a readable plain-text transcript.
func MessagesToPrompt(messages []Message, appendJSONInstruction bool) string {
	parts := make([]string, 0, len(messages)+1)
	for _, msg := range messages {
		role := strings.ToLower(msg.Role)
		if role == "" {
			role = "user"
		}
		switch role {
		case "system":
			parts = append(parts, "System: "+msg.Content)
		case "user":
			parts = append(parts, "User: "+msg.Content)
		case "assistant":
			parts = append(parts, "Assistant: "+msg.Content)
		default:
			parts = append(parts, strings.ToUpper(role[:1])+role[1:]+": "+msg.Content)
		}
	}
	if appendJSONInstruction {
		parts = append(parts, "Respond with valid JSON only.")
	}
	return strings.Join(parts, "\n")
}

// errNotObject reports model output that parsed but was not a JSON object.
var errNotObject = errors.New("LLM JSON response was not an object")

// LoadsModelJSON parses model output as a JSON object, tolerating Markdown
// code fences, surrounding prose and missing commas between fields.
func LoadsModelJSON(content string) (map[string]any, error) {
	var firstErr error
	stripped := StripJSONCodeFence(content)
	attempts := []string{content, stripped, RepairMissingFieldCommas(stripped)}
	for _, candidate := range attempts {
		var loaded any
		if err := json.Unmarshal([]byte(candidate), &loaded); err == nil {
			if obj, ok := loaded.(map[string]any); ok {
				return obj, nil
			}
		} else if firstErr == nil {
			firstErr = err
		}
		if extracted, ok := ExtractFirstJSONObject(candidate); ok {
			return extracted, nil
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, errNotObject
}

// StripJSONCodeFence removes a surrounding Markdown code fence from content.
func StripJSONCodeFence(content string) string {
	stripped := strings.TrimSpace(content)
	if !strings.HasPrefix(stripped, "```") {
		return stripped
	}
	lines := splitLines(stripped)
	if len(lines) >= 3 && strings.HasPrefix(lines[0], "```") && strings.TrimSpace(lines[len(lines)-1]) == "```" {
		return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	}
	return stripped
}

// ExtractFirstJSONObject returns the first complete JSON object embedded in
// content, ignoring any prose before or after it.
func ExtractFirstJSONObject(content string) (map[string]any, bool) {
	for i := 0; i < len(content); i++ {
		if content[i] != '{' {
			continue
		}
		// Decode a single value starting here; trailing text is ignored.
		var loaded any
		if err := json.NewDecoder(strings.NewReader(content[i:])).Decode(&loaded); err != nil {
			continue
		}
		if obj, ok := loaded.(map[string]any); ok {
			return obj, true
		}
	}
	return nil, false
}

// RepairMissingFieldCommas inserts commas between newline-separated object
// fields where a model forgot them.
func RepairMissingFieldCommas(content string) string {
	lines := splitLines(content)
	for i, line := range lines {
		next := ""
		if i+1 < len(lines) {
			next = lines[i+1]
		}
		if lineNeedsFieldComma(line, next) {
			lines[i] = strings.TrimRight(line, " \t") + ","
		}
	}
	return strings.Join(lines, "\n")
}

// scalarAtEnd matches a whole JSON scalar literal (used anchored at each suffix).
var scalarAtEnd = regexp.MustCompile(`^(?:true|false|null|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)$`)

// lineNeedsFieldComma reports whether a missing comma can be inferred safely
// between a value line and the following "key": line.
func lineNeedsFieldComma(line, next string) bool {
	stripped := strings.TrimSpace(line)
	nextStripped := strings.TrimSpace(next)
	if stripped == "" || strings.HasSuffix(stripped, ",") || strings.HasSuffix(stripped, "{") || strings.HasSuffix(stripped, "[") {
		return false
	}
	if !strings.HasPrefix(nextStripped, `"`) || !strings.Contains(nextStripped, `":`) {
		return false
	}
	if strings.HasSuffix(stripped, `"`) || strings.HasSuffix(stripped, "}") || strings.HasSuffix(stripped, "]") {
		return true
	}
	return endsWithScalar(stripped)
}

// endsWithScalar emulates Python's `(?<![A-Za-z])(?:true|...|number)$`
// search: some suffix must be a scalar literal not preceded by a letter.
func endsWithScalar(s string) bool {
	for i := 0; i < len(s); i++ {
		if i > 0 && isASCIILetter(s[i-1]) {
			continue
		}
		if scalarAtEnd.MatchString(s[i:]) {
			return true
		}
	}
	return false
}

// isASCIILetter reports whether b is in [A-Za-z].
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// splitLines splits text on \n, \r\n or \r line endings.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// CircuitState is the state of a CircuitBreaker.
type CircuitState string

// Circuit breaker states.
const (
	StateClosed   CircuitState = "closed"
	StateOpen     CircuitState = "open"
	StateHalfOpen CircuitState = "half_open"
)

// CircuitBreaker is an exponential-backoff breaker shared by LLM clients.
// Embed it by value in a client and call initCircuitBreaker once; it is safe
// for concurrent use and must not be copied after initialisation.
type CircuitBreaker struct {
	mu           sync.Mutex
	clock        func() time.Time
	onFailure    func(reason string, retryDelay time.Duration)
	available    bool
	lastFailure  time.Time
	nextRetryAt  time.Time
	failureCount int
	retryDelay   time.Duration
	state        CircuitState
}

// initCircuitBreaker resets the breaker with an injectable clock and an
// optional failure hook (used for logging).
func (cb *CircuitBreaker) initCircuitBreaker(clock func() time.Time, onFailure func(string, time.Duration)) {
	if clock == nil {
		clock = utcNow
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.clock = clock
	cb.onFailure = onFailure
	cb.available = false
	cb.lastFailure = time.Time{}
	cb.nextRetryAt = time.Time{}
	cb.failureCount = 0
	cb.retryDelay = initialRetryDelay
	cb.state = StateClosed
}

// Available reports whether the last call or probe succeeded.
func (cb *CircuitBreaker) Available() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.available
}

// CircuitState returns closed, open or half_open.
func (cb *CircuitBreaker) CircuitState() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// RetryDelaySeconds returns the current retry delay in whole seconds.
func (cb *CircuitBreaker) RetryDelaySeconds() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return int(cb.retryDelay / time.Second)
}

// CanAttempt reports whether a call may be attempted now, without changing state.
func (cb *CircuitBreaker) CanAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.retryDue()
}

// retryDue reports whether the circuit is closed or its retry time has
// arrived. The caller must hold cb.mu.
func (cb *CircuitBreaker) retryDue() bool {
	if cb.state == StateClosed {
		return true
	}
	return !cb.nextRetryAt.IsZero() && !cb.clock().Before(cb.nextRetryAt)
}

// shouldAttempt reports whether a call may proceed, moving an open circuit to
// half-open once the retry time arrives.
func (cb *CircuitBreaker) shouldAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == StateClosed {
		return true
	}
	if cb.retryDue() {
		cb.state = StateHalfOpen
		return true
	}
	return false
}

// markSuccess closes the circuit and marks the client available.
func (cb *CircuitBreaker) markSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.available = true
	cb.lastFailure = time.Time{}
	cb.nextRetryAt = time.Time{}
	cb.failureCount = 0
	cb.retryDelay = initialRetryDelay
	cb.state = StateClosed
}

// markFailed opens the circuit and doubles the retry delay (capped at 5 min).
func (cb *CircuitBreaker) markFailed(reason string) {
	cb.mu.Lock()
	// 30s * 2^failures, capped; the shift is bounded to avoid overflow.
	delay := initialRetryDelay << min(cb.failureCount, 10)
	cb.retryDelay = min(delay, maxRetryDelay)
	cb.failureCount++
	cb.available = false
	cb.lastFailure = cb.clock()
	cb.nextRetryAt = cb.lastFailure.Add(cb.retryDelay)
	cb.state = StateOpen
	onFailure, retryDelay := cb.onFailure, cb.retryDelay
	cb.mu.Unlock()

	// Call the hook outside the lock so it may safely read breaker state.
	if onFailure != nil {
		onFailure(reason, retryDelay)
	}
}
