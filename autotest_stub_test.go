package main

import "testing"

// TestAutotestHookIsInertWithoutBuildTag: the local update e2e hook must not
// exist in a normal (untagged) build. It used to ship in every release and
// was gated only by environment variables, so UNITERM_UPDATE_AUTOTEST=1 plus
// a loopback API base could hide the window and drive check → download →
// apply → os.Exit without going through DownloadUpdate's guards. With
// //go:build autotest on autotest_update.go, the stub must report the hook
// as disabled no matter what the environment says.
func TestAutotestHookIsInertWithoutBuildTag(t *testing.T) {
	t.Setenv("UNITERM_UPDATE_AUTOTEST", "1")
	t.Setenv("UNITERM_UPDATE_API_BASE", "http://127.0.0.1:9")

	if autotestEnabled() {
		t.Fatal("autotestEnabled() must be false in a build without -tags autotest")
	}
}
