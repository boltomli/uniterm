package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"net/http"
	"strings"
)

// contentSecurityPolicy is the production CSP stamped on every asset the
// WebView loads (F-09b). The app renders untrusted text (AI messages, update
// notes, terminal output); the HTML sanitizer is regex-based, so a policy
// layer exists to make any bypass non-executable.
//
// Rationale for each relaxation:
//   - script-src 'self' + exactly one sha256: the single inline script is
//     the pre-paint theme/platform bootstrap in frontend/index.html. The hash
//     is computed at init from the embedded frontend/dist/index.html — the
//     exact document indexNoCache serves — by inlineScriptHash, so it can
//     never drift from the served bytes. (A hand-maintained constant silently
//     depended on the checkout's line endings: Vite copies the inline script
//     verbatim, so an LF checkout produced a different hash than a CRLF one
//     and the policy blocked the theme bootstrap on one of the two.)
//     TestContentSecurityPolicyHashesInlineThemeScript pins this.
//     Wails does NOT inject a runtime script: the bundled @wailsio/runtime
//     module talks to the host through fetch(window.location.origin +
//     "/wails/runtime"), i.e. 'self'. The theme itself is passed to the
//     bootstrap through a <meta> element (see indexNoCache) for the same
//     reason — a generated second script would have no stable hash.
//   - style-src 'unsafe-inline': Element Plus and the components set inline
//     style attributes throughout.
//   - connect-src adds ws://127.0.0.1/localhost (any port): the VNC and
//     SPICE tabs connect to the Go-managed loopback proxies
//     (backend/session/{vnc,spice}_proxy.go).
//   - worker/blob allowances cover bundled dynamic imports
//     (@novnc/novnc, spice-html5) and Vite's asset URLs.
//   - script-src 'wasm-unsafe-eval': the RDP tab runs the IronRDP engine as
//     a WASM module (@ys-ll/ironrdp-wasm); CSP3 requires this source to
//     permit WebAssembly compilation. It enables wasm compilation only —
//     NOT JavaScript eval — so it does not weaken the JS policy.
var contentSecurityPolicy = buildContentSecurityPolicy(inlineScriptHash(servedIndexHTML))

// servedIndexHTML is the root document the asset server hands to the WebView
// (indexNoCache rewrites a copy of it at request time; the inline script is
// untouched by that rewrite).
//
//go:embed frontend/dist/index.html
var servedIndexHTML string

// inlineScriptHash returns the CSP sha256 source expression (including the
// surrounding single quotes) for the first inline <script> in html, or ""
// when the document has no inline script. An empty result yields a policy
// with NO inline-script allowance — the fail-closed direction: the theme
// bootstrap would stop running (a visible regression caught by tests) rather
// than the policy widening.
func inlineScriptHash(html string) string {
	start := strings.Index(html, "<script")
	if start < 0 {
		return ""
	}
	rest := html[start:]
	tagEnd := strings.Index(rest, ">")
	end := strings.Index(rest, "</script>")
	if tagEnd < 0 || end < tagEnd {
		return ""
	}
	if strings.Contains(rest[:tagEnd], "src=") {
		return "" // first script is external; there is no inline script to allow
	}
	script := rest[tagEnd+1 : end]
	sum := sha256.Sum256([]byte(script))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// buildContentSecurityPolicy assembles the policy with the given inline
// script hash ("" = no inline script allowed).
func buildContentSecurityPolicy(scriptHash string) string {
	scriptSrc := "script-src 'self' 'wasm-unsafe-eval'"
	if scriptHash != "" {
		scriptSrc += " " + scriptHash
	}
	return "default-src 'self'; " +
		scriptSrc + "; " +
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
}

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
