//go:build windows

package session

import (
	"os"
	"strings"
	"testing"
)

// readRdpSessionSource loads rdp_session.go so the guards below can pin the
// security-relevant parts of the connect path that no in-process unit test
// can reach: they execute on the COM STA thread inside Connect, against a
// real ActiveX control.
func readRdpSessionSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("rdp_session.go")
	if err != nil {
		t.Fatalf("read rdp_session.go: %v", err)
	}
	return string(src)
}

// TestRdpConnectPathHasNoGlobalSecurityOverrides pins the P1 fix: the RDP
// connect path must not write the registry (HKCU/HKLM AuthenticationLevel
// override, redirection-warning policy, with UAC elevation as fallback) and
// must not auto-dismiss the certificate/redirection warning dialogs. Those
// warnings belong to the user — an impersonating server must be visible.
func TestRdpConnectPathHasNoGlobalSecurityOverrides(t *testing.T) {
	src := readRdpSessionSource(t)
	forbidden := []string{
		"setAuthLevelOverride",       // writes AuthenticationLevelOverride=0 for ALL mstsc use
		"elevateRegWrite",            // UAC prompt to weaken the HKLM policy key
		"autoDismissSecurityDialogs", // 50 ms clicker that confirms cert warnings
		"AuthenticationLevelOverride",
		"ShowRedirectionWarningDialog",
		"RedirectionWarningDialogVersion",
		"registry.CreateKey",
		"registry.OpenKey",
		"registry.SET_VALUE",
		"registry.LOCAL_MACHINE",
		`"AuthenticationLevel", 0`,  // never turn server certificate verification off
		`"RedirectClipboard", true`, // must come from buildRdpSettings, never hardcoded
		`"RedirectDrives", true`,
	}
	for _, s := range forbidden {
		if strings.Contains(src, s) {
			t.Errorf("rdp_session.go must not contain %q: global security overrides and auto-confirmed warnings were removed (security.md P1)", s)
		}
	}
	required := []string{
		"buildRdpSettings(config)", // security-relevant settings come from the tested pure function
	}
	for _, s := range required {
		if !strings.Contains(src, s) {
			t.Errorf("rdp_session.go must contain %q so the connect path uses the tested settings builder", s)
		}
	}
}

// TestBuildRdpSettings pins the security-relevant settings the ActiveX
// control receives: server certificate verification is never switched off
// (NLA on or off — the stored password goes through ClearTextPassword on the
// NLA-off path, so an impersonator must still be rejected), and local drive
// / clipboard redirection is a per-connection tri-state flag: an absent
// field (connections saved before the fields existed) means clipboard ON —
// pasting local text into the remote desktop keeps working — and drives
// OFF; an explicit value, including an opt-out, always wins.
func TestBuildRdpSettings(t *testing.T) {
	tests := []struct {
		name       string
		config     ConnectionConfig
		wantNLA    bool
		wantClip   bool
		wantDrives bool
	}{
		{
			name:     "NLA off keeps server authentication enabled (auth level >= 1)",
			config:   ConnectionConfig{RdpEnableNLA: false},
			wantClip: true,
		},
		{
			name:     "legacy connection with no redirect fields defaults to clipboard on, drives off",
			config:   ConnectionConfig{},
			wantClip: true,
		},
		{
			name:     "NLA on verifies the server and shares only the clipboard by default",
			config:   ConnectionConfig{RdpEnableNLA: true},
			wantNLA:  true,
			wantClip: true,
		},
		{
			name: "explicit opt-out keeps the clipboard local",
			config: ConnectionConfig{
				RdpRedirectClipboard: boolPtr(false),
			},
		},
		{
			name: "explicit opt-in enables clipboard only",
			config: ConnectionConfig{
				RdpRedirectClipboard: boolPtr(true),
			},
			wantClip: true,
		},
		{
			name: "explicit opt-in enables drives only, clipboard stays at its default on",
			config: ConnectionConfig{
				RdpRedirectDrives: boolPtr(true),
			},
			wantClip:   true,
			wantDrives: true,
		},
		{
			name: "explicit opt-out of drives while clipboard stays default on",
			config: ConnectionConfig{
				RdpRedirectDrives: boolPtr(false),
			},
			wantClip: true,
		},
		{
			name: "explicit opt-in with NLA off still never disables server auth",
			config: ConnectionConfig{
				RdpEnableNLA:         false,
				RdpRedirectClipboard: boolPtr(true),
				RdpRedirectDrives:    boolPtr(true),
			},
			wantClip:   true,
			wantDrives: true,
		},
		{
			name: "explicit values win: everything on",
			config: ConnectionConfig{
				RdpEnableNLA:         true,
				RdpRedirectClipboard: boolPtr(true),
				RdpRedirectDrives:    boolPtr(true),
			},
			wantNLA:    true,
			wantClip:   true,
			wantDrives: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRdpSettings(tc.config)
			if got.AuthLevel < 1 {
				t.Errorf("AuthLevel = %d, must be >= 1 so the server certificate is verified", got.AuthLevel)
			}
			if got.EnableCredSSP != tc.wantNLA {
				t.Errorf("EnableCredSSP = %v, want %v", got.EnableCredSSP, tc.wantNLA)
			}
			if got.RedirectClipboard != tc.wantClip {
				t.Errorf("RedirectClipboard = %v, want %v", got.RedirectClipboard, tc.wantClip)
			}
			if got.RedirectDrives != tc.wantDrives {
				t.Errorf("RedirectDrives = %v, want %v", got.RedirectDrives, tc.wantDrives)
			}
		})
	}
}

// boolPtr is a test helper for the tri-state redirect flags.
func boolPtr(v bool) *bool { return &v }

// TestRdpSnapshotUsesFastPNG pins the Low perf fix: Snapshot encodes on the
// Wails/UI thread, so it must use png.BestSpeed rather than the default
// (slowest) compression level.
func TestRdpSnapshotUsesFastPNG(t *testing.T) {
	src := readRdpSessionSource(t)
	if !strings.Contains(src, "png.BestSpeed") {
		t.Error("rdp_session.go Snapshot must encode with png.BestSpeed")
	}
}
