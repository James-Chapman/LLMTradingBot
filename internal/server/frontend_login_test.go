package server

import "testing"

// TestFrontendLogin covers how the pages and the service worker behave when
// the bot asks for a login (it listens on a non-loopback address).
func TestFrontendLogin(t *testing.T) {
	core := appScripts(t)["core.js"]
	approvals := readFrontend(t, "approvals.html")
	login := readFrontend(t, "login.html")
	sw := readFrontend(t, "sw.js")

	t.Run("GIVEN an expired or missing session WHEN an API call answers 401 THEN the page goes to the login form", func(t *testing.T) {
		guards := map[string]string{
			"core.js":   between(t, core, "// Sends the browser to the login form", "// ── Formatting"),
			"approvals": between(t, approvals, "// Sends the browser to the login form", "</script>"),
		}
		for name, guard := range guards {
			mustContain(t, name, guard, "response.status === 401", "window.location.assign('/login')")
		}
		// core.js is the first dashboard module, so the guard wraps fetch before any view calls it.
		mustAppearBefore(t, "core.js", core, "window.fetch = async", "window.Dashboard = {")
	})

	t.Run("GIVEN the login page WHEN it loads without a session THEN it needs nothing but the public stylesheet", func(t *testing.T) {
		mustContain(t, "login", login, `<link rel="stylesheet" href="/static/styles.css">`, `type="password"`, "fetch('/api/login'",
			"errorBox.textContent = message")
		mustNotContain(t, "login", login, "/static/app/", "<script src=", "innerHTML")
	})

	t.Run("GIVEN a request redirected to the login form WHEN the service worker handles it THEN the answer is not cached", func(t *testing.T) {
		// A cached redirect would later be served as the dashboard itself.
		mustContain(t, "sw", sw, "function cacheable(response)", "response.ok && !response.redirected")
		mustContain(t, "networkFirst", between(t, sw, "async function networkFirst(request) {", "async function staleWhileRevalidate("), "cacheable(response)")
		mustContain(t, "staleWhileRevalidate", between(t, sw, "async function staleWhileRevalidate(request) {", "// ── Helpers"), "cacheable(res)")
		mustNotContain(t, "sw", sw, "if (response.ok) cache.put", "if (res.ok) cache.put")
	})
}
