package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// F-09b: the CSP must cover every directive the threat model needs.
func TestContentSecurityPolicyDirectives(t *testing.T) {
	required := []string{
		"default-src 'self'",
		"script-src 'self'",
		"'unsafe-inline'", // styles (Element Plus inline attributes)
		"img-src 'self' data: blob:",
		"connect-src 'self'",
		"ws://127.0.0.1:*", // VNC/SPICE loopback proxies
		"ws://localhost:*",
		"object-src 'none'",
		"base-uri 'self'",
		"frame-ancestors 'none'",
	}
	for _, d := range required {
		if !strings.Contains(contentSecurityPolicy, d) {
			t.Errorf("CSP is missing %q", d)
		}
	}
	// The only script relaxation besides 'self' must be a hash — never
	// 'unsafe-inline'/'unsafe-eval', which would defeat the policy.
	scriptSrc := ""
	for _, part := range strings.Split(contentSecurityPolicy, ";") {
		if strings.HasPrefix(strings.TrimSpace(part), "script-src") {
			scriptSrc = part
		}
	}
	if scriptSrc == "" {
		t.Fatal("no script-src directive")
	}
	// 'wasm-unsafe-eval' is allowed (IronRDP WASM engine compilation; it
	// does not permit JavaScript eval). Everything else with "unsafe" in
	// the name — 'unsafe-inline', bare 'unsafe-eval' — must stay out.
	cleaned := strings.ReplaceAll(scriptSrc, "'wasm-unsafe-eval'", "")
	if strings.Contains(cleaned, "unsafe") {
		t.Fatalf("script-src must not carry unsafe allowances: %q", scriptSrc)
	}
}

// The inline theme bootstrap in index.html is the only inline script; the
// CSP hash must match the document actually served (the embedded
// frontend/dist/index.html) exactly or the pre-paint theme logic stops
// running. The policy derives the hash from that document at init, so this
// pins the derivation rather than a hand-copied constant.
func TestContentSecurityPolicyHashesInlineThemeScript(t *testing.T) {
	if servedIndexHTML == "" {
		t.Fatal("embedded frontend/dist/index.html is empty — build the frontend")
	}
	want := inlineScriptHash(servedIndexHTML)
	if want == "" {
		t.Fatal("served index.html has no inline script; the policy would carry no hash")
	}
	if !strings.Contains(contentSecurityPolicy, want) {
		t.Fatalf("CSP does not allow the served inline script %s\nCSP: %s", want, contentSecurityPolicy)
	}
	// Exactly one hash, in script-src, and nothing else that allows inline
	// execution.
	if n := strings.Count(contentSecurityPolicy, "'sha256-"); n != 1 {
		t.Fatalf("CSP carries %d script hashes, want exactly 1: %s", n, contentSecurityPolicy)
	}

	// The served document must be the built copy of the source template:
	// Vite copies the inline script verbatim, so apart from line endings
	// the two must agree — otherwise a source edit could ship without the
	// hash following it (or vice versa).
	src, err := os.ReadFile("frontend/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	stripEOL := func(s string) string { return strings.ReplaceAll(s, "\r", "") }
	if got, wantSrc := stripEOL(firstInlineScript(servedIndexHTML)), stripEOL(firstInlineScript(string(src))); got != wantSrc {
		t.Fatalf("inline script in frontend/dist/index.html differs from frontend/index.html — rebuild the frontend\n--- dist ---\n%s\n--- src ---\n%s", got, wantSrc)
	}

	// Endings-independence: the derivation must accept the same script with
	// LF or CRLF and produce the hash for exactly those bytes — the policy
	// follows whatever the build shipped.
	lf := strings.ReplaceAll(servedIndexHTML, "\r\n", "\n")
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	if inlineScriptHash(lf) == inlineScriptHash(crlf) && strings.Contains(firstInlineScript(lf), "\n") {
		t.Fatal("hash is insensitive to line endings — it is not hashing the script bytes")
	}
	if inlineScriptHash(strings.Replace(servedIndexHTML, "<script>", "<script src=\"x\">", 1)) != "" {
		t.Fatal("an external first script must yield no inline allowance")
	}
	if inlineScriptHash("<html><head></head></html>") != "" {
		t.Fatal("a document without scripts must yield no inline allowance")
	}
	if !strings.Contains(buildContentSecurityPolicy(""), "script-src 'self' 'wasm-unsafe-eval';") {
		t.Fatalf("empty hash must not widen script-src: %s", buildContentSecurityPolicy(""))
	}
}

// firstInlineScript returns the body of the first <script>…</script> in html.
func firstInlineScript(html string) string {
	start := strings.Index(html, "<script>")
	end := strings.Index(html, "</script>")
	if start < 0 || end < start {
		return ""
	}
	return html[start+len("<script>") : end]
}

func TestCSPMiddlewareSetsHeaders(t *testing.T) {
	var gotCSP, gotNosniff string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCSP = w.Header().Get("Content-Security-Policy")
		gotNosniff = w.Header().Get("X-Content-Type-Options")
		w.WriteHeader(http.StatusNoContent)
	})
	h := cspMiddleware(next)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if gotCSP != contentSecurityPolicy {
		t.Fatalf("CSP header = %q", gotCSP)
	}
	if gotNosniff != "nosniff" {
		t.Fatalf("nosniff header = %q", gotNosniff)
	}
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
}
