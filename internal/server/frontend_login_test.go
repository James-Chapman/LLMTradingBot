package server

import "testing"

// TestFrontendLogin covers how the pages and the service worker behave when
// the bot asks for a login (it listens on a non-loopback address).
func TestFrontendLogin(t *testing.T) {
	index := readFrontend(t, "index.html")
	approvals := readFrontend(t, "approvals.html")
	sw := readFrontend(t, "sw.js")

	t.Run("GIVEN an expired or missing session WHEN an API call answers 401 THEN the page goes to the login form", func(t *testing.T) {
		for name, page := range map[string]string{"index": index, "approvals": approvals} {
			guard := between(t, page, "// Sends the browser to the login form", "</script>")
			mustContain(t, name, guard, "response.status === 401", "window.location.assign('/login')")
		}
	})

	t.Run("GIVEN a request redirected to the login form WHEN the service worker handles it THEN the answer is not cached", func(t *testing.T) {
		// A cached redirect would later be served as the dashboard itself.
		mustContain(t, "sw", sw, "function cacheable(response)", "response.ok && !response.redirected")
		mustContain(t, "networkFirst", between(t, sw, "async function networkFirst(request) {", "async function staleWhileRevalidate("), "cacheable(response)")
		mustContain(t, "staleWhileRevalidate", between(t, sw, "async function staleWhileRevalidate(request) {", "// ── Helpers"), "cacheable(res)")
		mustNotContain(t, "sw", sw, "if (response.ok) cache.put", "if (res.ok) cache.put")
	})
}
