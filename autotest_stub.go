//go:build !autotest

// Inert counterparts of the local update e2e hook in autotest_update.go,
// which compiles only with -tags autotest. Keeping that file out of normal
// builds means no environment variable can reach the check → download →
// apply → os.Exit pipeline that bypasses DownloadUpdate's guards.

package main

// autotestEnabled reports whether the local update e2e hook may run. It is
// always false in a build without -tags autotest, so main never hides the
// window for a test run nor starts the hook.
func autotestEnabled() bool { return false }

// autotestUpdate is the unreachable counterpart of the -tags autotest hook;
// main only calls it behind autotestEnabled(), which is false here.
func (a *App) autotestUpdate() {}
