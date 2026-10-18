//go:build !windows

package session

import (
	"testing"
)

func TestEnsureTerminalEnv(t *testing.T) {
	t.Run("injects default TERM when absent", func(t *testing.T) {
		env := ensureTerminalEnv([]string{"PATH=/bin"}, "")
		if !hasEnvKV(env, "TERM="+DefaultTermType) {
			t.Fatalf("env = %v, want TERM=%s", env, DefaultTermType)
		}
	})

	t.Run("keeps inherited TERM when no explicit choice", func(t *testing.T) {
		env := ensureTerminalEnv([]string{"TERM=vt220"}, "")
		if !hasEnvKV(env, "TERM=vt220") {
			t.Fatalf("env = %v, want inherited TERM=vt220 kept", env)
		}
	})

	t.Run("explicit choice overrides inherited TERM", func(t *testing.T) {
		env := ensureTerminalEnv([]string{"TERM=vt220"}, "vt100")
		if hasEnvKV(env, "TERM=vt220") || !hasEnvKV(env, "TERM=vt100") {
			t.Fatalf("env = %v, want TERM=vt100 replacing inherited vt220", env)
		}
	})

	t.Run("drops empty TERM entry so it cannot shadow the injection", func(t *testing.T) {
		env := ensureTerminalEnv([]string{"TERM=", "PATH=/bin"}, "")
		if hasEnvKV(env, "TERM=") {
			t.Fatalf("env = %v, want empty TERM entry dropped", env)
		}
		if !hasEnvKV(env, "TERM="+DefaultTermType) {
			t.Fatalf("env = %v, want TERM=%s injected", env, DefaultTermType)
		}
	})
}

func hasEnvKV(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
