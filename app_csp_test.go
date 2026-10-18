package main

import (
	"crypto/sha256"
	"encoding/base64"
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
	if strings.Contains(scriptSrc, "unsafe") {
		t.Fatalf("script-src must not carry unsafe allowances: %q", scriptSrc)
	}
}

// The inline theme bootstrap in index.html is the only inline script; the
// CSP hash must match it exactly or the pre-paint theme logic stops running.
func TestContentSecurityPolicyHashesInlineThemeScript(t *testing.T) {
	html, err := os.ReadFile("frontend/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	start := strings.Index(string(html), "<script>")
	end := strings.Index(string(html), "</script>")
	if start < 0 || end < start {
		t.Fatal("inline theme script not found in index.html")
	}
	script := string(html)[start+len("<script>") : end]

	sum := sha256.Sum256([]byte(script))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(contentSecurityPolicy, want) {
		t.Fatalf("CSP hash %s does not match the index.html inline script %s — regenerate the constant", want,
			"'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
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
