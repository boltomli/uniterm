package mcp

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Use a real TCP listener: SDK localhost protection depends on LocalAddrContextKey,
// which is not populated by a plain httptest recorder.
func TestRemoteAccess(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(strconv.FormatBool(remote), func(t *testing.T) {
			srv := NewServer(Env{})
			token, hash := GenerateToken()
			srv.SetTokens(map[string]TokenInfo{hash: {Name: "remote-agent"}})
			if err := srv.Start(0, remote); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(srv.Stop)
			ip := srv.ln.Addr().(*net.TCPAddr).IP
			if remote && !ip.IsUnspecified() || !remote && !ip.IsLoopback() {
				t.Fatalf("remote=%v: unexpected listener %v", remote, srv.ln.Addr())
			}

			endpoint := "http://127.0.0.1:" + strconv.Itoa(srv.Port()) + "/mcp"
			client := &http.Client{Timeout: 5 * time.Second}
			remoteStatus := http.StatusForbidden
			if remote {
				remoteStatus = http.StatusOK
			}
			for _, tc := range []struct {
				name, method, host, origin, token string
				status                            int
			}{
				{"local", "POST", "127.0.0.1", "", token, http.StatusOK},
				{"forwarded-ip", "POST", "172.16.35.152:61207", "", token, remoteStatus},
				{"forwarded-hostname", "POST", "dev.example:61207", "", token, remoteStatus},
				{"missing-token", "POST", "172.16.35.152:61207", "", "", http.StatusUnauthorized},
				{"invalid-token", "POST", "172.16.35.152:61207", "", "wrong", http.StatusUnauthorized},
				{"same-origin", "POST", "127.0.0.1", "http://127.0.0.1", token, http.StatusOK},
				{"remote-same-origin", "POST", "dev.example:61207", "http://dev.example:61207", token, remoteStatus},
				{"cross-origin-post", "POST", "dev.example:61207", "https://evil.example", token, http.StatusForbidden},
				{"cross-origin-get", "GET", "dev.example:61207", "https://evil.example", token, http.StatusForbidden},
				{"null-origin", "POST", "127.0.0.1", "null", token, http.StatusForbidden},
				{"malformed-origin", "POST", "127.0.0.1", "://", token, http.StatusForbidden},
				{"origin-with-path", "POST", "127.0.0.1", "http://127.0.0.1/other", token, http.StatusForbidden},
				{"origin-with-credentials", "POST", "127.0.0.1", "http://user@127.0.0.1", token, http.StatusForbidden},
				{"wrong-scheme", "POST", "127.0.0.1", "https://127.0.0.1", token, http.StatusForbidden},
			} {
				t.Run(tc.name, func(t *testing.T) {
					body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
					req, err := http.NewRequest(tc.method, endpoint, strings.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					req.Host = tc.host
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Accept", "application/json, text/event-stream")
					if tc.origin != "" {
						req.Header.Set("Origin", tc.origin)
					}
					if tc.token != "" {
						req.Header.Set("Authorization", "Bearer "+tc.token)
					}
					resp, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					data, _ := io.ReadAll(resp.Body)
					if resp.StatusCode != tc.status {
						t.Fatalf("status=%d, want %d: %s", resp.StatusCode, tc.status, data)
					}
					if tc.status == http.StatusOK && !strings.Contains(string(data), `"protocolVersion"`) {
						t.Fatalf("missing initialization result: %s", data)
					}
				})
			}
		})
	}
}

func TestRemoteAccessCanBeDisabled(t *testing.T) {
	srv := NewServer(Env{})
	if err := srv.Start(0, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	port := srv.Port()
	if err := srv.Start(port, false); err != nil {
		t.Fatal(err)
	}
	if !srv.ln.Addr().(*net.TCPAddr).IP.IsLoopback() {
		t.Fatalf("remote access remains enabled: %v", srv.ln.Addr())
	}
}

func TestInvalidOriginHeaders(t *testing.T) {
	for _, origins := range [][]string{{""}, {"http://localhost", "http://localhost"}, {"http://localhost http://evil.example"}} {
		r, err := http.NewRequest("GET", "http://localhost/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, origin := range origins {
			r.Header.Add("Origin", origin)
		}
		if validRequestOrigin(r) {
			t.Fatalf("accepted invalid Origin headers: %q", origins)
		}
	}
}
