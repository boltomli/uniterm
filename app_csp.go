package main

import "net/http"

// contentSecurityPolicy is the production CSP stamped on every asset the
// WebView loads (F-09b). The app renders untrusted text (AI messages, update
// notes, terminal output); the HTML sanitizer is regex-based, so a policy
// layer exists to make any bypass non-executable.
//
// Rationale for each relaxation:
//   - script-src 'self' + exactly one sha256: the single inline script is
//     the pre-paint theme/platform bootstrap in frontend/index.html. The
//     hash is asserted against that file by
//     TestContentSecurityPolicyHashesInlineThemeScript — regenerate it
//     (sha256 of the script element's text) when the bootstrap changes.
//     Wails does NOT inject a runtime script: the bundled @wailsio/runtime
//     module talks to the host through fetch(window.location.origin +
//     "/wails/runtime"), i.e. 'self'.
//   - style-src 'unsafe-inline': Element Plus and the components set inline
//     style attributes throughout.
//   - connect-src adds ws://127.0.0.1/localhost (any port): the VNC and
//     SPICE tabs connect to the Go-managed loopback proxies
//     (backend/session/{vnc,spice}_proxy.go).
//   - worker/blob allowances cover bundled dynamic imports
//     (@novnc/novnc, spice-html5) and Vite's asset URLs.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'sha256-grnC/M1y7+r9gmG+whHoc1WfLuvqVtKQhMcfr4QglG0='; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"connect-src 'self' ws://127.0.0.1:* ws://localhost:*; " +
	"worker-src 'self' blob:; " +
	"font-src 'self' data:; " +
	"media-src 'self' blob: data:; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// cspMiddleware stamps the production CSP (plus MIME sniffing protection) on
// every asset-server response. It runs for the embedded production build
// only — `wails3 dev` serves through Vite and is intentionally untouched.
func cspMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
