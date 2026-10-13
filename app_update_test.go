package main

import (
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/update"
)

// TestDownloadUpdateTakesNoFrontendAssets pins the binding signature: the
// webview must not be able to choose WHAT gets installed. The asset list is
// derived from the backend's own CheckForUpdate result instead of a
// JS-supplied URL + optional hash. (Compile-time check: assigning the method
// to func() error fails if a parameter is ever reintroduced.)
func TestDownloadUpdateTakesNoFrontendAssets(t *testing.T) {
	var download func() error = (&App{}).DownloadUpdate
	if download == nil {
		t.Fatal("DownloadUpdate must be callable without arguments")
	}
}

// withoutDevBuild flips the production guard off for one test so the real
// DownloadUpdate body runs.
func withoutDevBuild(t *testing.T) {
	t.Helper()
	prev := devBuild
	devBuild = false
	t.Cleanup(func() { devBuild = prev })
}

// TestDownloadUpdateRequiresPriorCheck: with no cached check result there is
// no trusted asset list, so the download must refuse instead of accepting
// whatever the caller would have supplied.
func TestDownloadUpdateRequiresPriorCheck(t *testing.T) {
	withoutDevBuild(t)
	a := &App{}
	err := a.DownloadUpdate()
	if err == nil {
		t.Fatal("DownloadUpdate without a prior CheckForUpdate must fail")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Fatalf("error should tell the caller to check first, got: %v", err)
	}
}

// TestDownloadUpdateUsesCachedCheckAssets: the download is driven by the
// backend-cached CheckForUpdate assets, and those still go through the
// release-source allowlist — a poisoned cache entry cannot install anything.
func TestDownloadUpdateUsesCachedCheckAssets(t *testing.T) {
	withoutDevBuild(t)
	a := &App{
		lastUpdateInfo: &update.UpdateInfo{
			HasUpdate: true,
			Latest:    "v9.9.9",
			Assets: []update.UpdateAsset{{
				Name:   "uniterm-linux-amd64-v9.9.9.tar.gz",
				URL:    "https://evil.example.com/uniterm-linux-amd64-v9.9.9.tar.gz",
				SHA256: strings.Repeat("a", 64),
			}},
		},
	}
	err := a.DownloadUpdate()
	if err == nil {
		t.Fatal("DownloadUpdate must refuse an asset outside the release sources")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "host") {
		t.Fatalf("error should name the rejected host, got: %v", err)
	}
}
