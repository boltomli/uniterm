package session

import "testing"

// sessionCwds used to be write-only: entries outlived their sessions for the
// life of the app. Closing a session must drop its cached cwd.
func TestSessionManagerCloseForgetsRecordedCwd(t *testing.T) {
	sm := NewSessionManager()
	const id = "cid-close"
	sm.Add(NewSSHSession(id))

	recordSessionCwd(id, "/home/u/project")
	if got := GetSessionCwd(id); got != "/home/u/project" {
		t.Fatalf("setup: GetSessionCwd = %q", got)
	}

	if err := sm.Close(id); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := GetSessionCwd(id); got != "" {
		t.Fatalf("cwd survived session close: %q", got)
	}
}

// Startup eviction (Init) is the fallback for sessions that disappeared while
// the app was not running the Close path: their cwds go too, while live
// sessions keep theirs.
func TestSessionManagerInitForgetsCwdsOfEvictedSessions(t *testing.T) {
	sm := NewSessionManager()

	const dead, live = "cid-dead", "cid-live"
	deadSession := NewSSHSession(dead)
	deadSession.setStatus(StatusDisconnected)
	sm.Add(deadSession)
	recordSessionCwd(dead, "/stale/path")

	liveSession := NewSSHSession(live)
	liveSession.setStatus(StatusConnected)
	sm.Add(liveSession)
	recordSessionCwd(live, "/live/path")

	sm.Init()

	if got := GetSessionCwd(dead); got != "" {
		t.Fatalf("evicted session kept its cwd: %q", got)
	}
	if got := GetSessionCwd(live); got != "/live/path" {
		t.Fatalf("live session lost its cwd: %q", got)
	}
}
