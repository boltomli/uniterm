package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain sandboxes os.UserConfigDir (APPDATA on Windows, XDG_CONFIG_HOME
// on Linux) for the whole package run.
//
// Regression guard for a real incident: SetDataDir → store.WriteBootstrap
// resolves the REAL user config dir to persist the data-dir pointer, and a
// test reaching that path used to overwrite the developer's live pointer
// with a t.TempDir() location (2026-10-13: %APPDATA%\uniTerm\
// bootstrap.json\bootstrap.json ended up pointing at ...\Temp\
// TestSetDataDir_MigrateToSelfRefuses… — every non-portable exe then
// started in FirstRun with the real data dir out of reach). The current
// SetDataDir refusal order keeps tests away from WriteBootstrap; this
// sandbox makes that a property of the harness instead of a property of
// the code under test.
//
// The full package suite passes with the redirect in place (verified both
// with an external APPDATA override and with this TestMain); tests that
// need a specific location still t.Setenv on top of it.
func TestMain(m *testing.M) {
	sandbox, err := os.MkdirTemp("", "uniterm-test-config")
	if err != nil {
		panic("cannot create config sandbox: " + err.Error())
	}
	os.Setenv("APPDATA", sandbox)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(sandbox, "xdg"))
	code := m.Run()
	os.RemoveAll(sandbox)
	os.Exit(code)
}
