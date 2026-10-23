//go:build !android || server

package utils

// EnsureTempDir is a no-op off Android: TMPDIR is already sane there.
func EnsureTempDir() {}
