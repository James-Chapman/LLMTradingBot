package kraken

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// recordedRequest captures one request the fake Kraken server received.
type recordedRequest struct {
	Path    string
	Query   url.Values
	Body    map[string]any
	Headers http.Header
}

// fakeKraken is an httptest server that replays queued JSON responses per path.
type fakeKraken struct {
	mu        sync.Mutex
	responses map[string][]string
	requests  []recordedRequest
	server    *httptest.Server
}

// newFakeKraken starts a fake Kraken REST server that is closed with the test.
func newFakeKraken(t *testing.T) *fakeKraken {
	t.Helper()
	f := &fakeKraken{responses: map[string][]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

// on queues response bodies for path; the last body repeats once the queue drains.
func (f *fakeKraken) on(path string, bodies ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[path] = append(f.responses[path], bodies...)
}

// handle records the request and writes the next queued response for its path.
func (f *fakeKraken) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)

	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{Path: r.URL.Path, Query: r.URL.Query(), Body: body, Headers: r.Header.Clone()})
	queue := f.responses[r.URL.Path]
	response := `{"error":["EGeneral:Unknown method"]}`
	if len(queue) > 0 {
		response = queue[0]
		if len(queue) > 1 {
			f.responses[r.URL.Path] = queue[1:]
		}
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, response)
}

// calls returns the recorded requests for path.
func (f *fakeKraken) calls(path string) []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedRequest
	for _, req := range f.requests {
		if req.Path == path {
			out = append(out, req)
		}
	}
	return out
}

// totalRequests returns how many requests the server has received.
func (f *fakeKraken) totalRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}
