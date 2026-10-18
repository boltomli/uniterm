package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/store"
)

// newURLTestApp builds an App whose settings store carries the F-07 opt-in.
func newURLTestApp(t *testing.T, allowInsecure bool) *App {
	t.Helper()
	ss, err := store.NewSettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("settings store: %v", err)
	}
	st, err := ss.Load()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	st.AI.AllowInsecureHTTP = &allowInsecure
	if err := ss.Save(st); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	return &App{settingsStore: ss}
}

// F-07: the endpoint transport policy — https everywhere, plain http only on
// loopback (local Ollama/LM Studio) or with the explicit settings opt-in.
func TestValidateLLMBaseURL(t *testing.T) {
	deny := newURLTestApp(t, false)
	allow := newURLTestApp(t, true)

	cases := []struct {
		name    string
		app     *App
		url     string
		wantErr string // "" = must pass
	}{
		{"https is always allowed", deny, "https://api.openai.com/v1", ""},
		{"loopback ipv4 http allowed", deny, "http://127.0.0.1:11434", ""},
		{"localhost http allowed", deny, "http://localhost:11434/v1", ""},
		{"loopback ipv6 http allowed", deny, "http://[::1]:1234", ""},
		{"routable http refused", deny, "http://example.com/v1", "insecure"},
		{"lan http refused", deny, "http://192.168.1.10:8000", "insecure"},
		{"file scheme refused", deny, "file:///etc/passwd", "scheme"},
		{"javascript scheme refused", deny, "javascript:alert(1)", "scheme"},
		{"missing scheme refused", deny, "api.openai.com", "https://"},
		{"empty refused", deny, "", "empty"},
		{"opt-in unlocks plain http", allow, "http://llm-gateway.corp.local:8080", ""},
		{"opt-in does not widen schemes", allow, "ftp://example.com", "scheme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.app.validateLLMBaseURL(tc.url)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validate(%q) = %v, want nil", tc.url, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate(%q) = nil, want error containing %q", tc.url, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
			// The error must never echo userinfo/query credentials.
			if strings.Contains(err.Error(), "key=") || strings.Contains(err.Error(), "sk-") {
				t.Fatalf("error leaks credentials: %v", err)
			}
		})
	}

	// E2E: ChatCompletion refuses before dialing anything.
	if _, err := deny.ChatCompletion("", "sk-secret", "http://evil.example/v1", "m", "{}", "openai", "", "", ""); err == nil {
		t.Fatal("ChatCompletion must refuse an insecure endpoint")
	} else if !strings.Contains(err.Error(), "insecure") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// F-07: FetchModels must bound the response body — a hostile or broken
// endpoint streaming forever used to be read with io.ReadAll and could
// exhaust memory.
func TestFetchModelsRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := make([]byte, 1<<20)
		for i := 0; i < 6; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	a := &App{} // loopback http needs no opt-in
	_, err := a.FetchModels("", "key", srv.URL, "openai", "")
	if err == nil {
		t.Fatal("oversized models response must return an error, not OOM")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A normal-sized models response still parses end to end (regression guard
// for the LimitReader change).
func TestFetchModelsParsesNormalResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1","object":"model"},{"id":"m2","object":"model"}]}`))
	}))
	defer srv.Close()

	a := &App{}
	models, err := a.FetchModels("", "key", srv.URL, "openai", "")
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "m1" {
		t.Fatalf("models = %+v", models)
	}
}
