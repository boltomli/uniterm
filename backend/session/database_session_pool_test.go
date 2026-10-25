package session

import (
	"os"
	"strings"
	"testing"
)

// TestConnectDoesNotOverridePoolSizing pins DB-13 (2026-10-25 regression
// audit): F-116 tuned the pool in database.NewDB (20 open / 5 idle), but
// Connect re-applied SetMaxOpenConns(5)/SetMaxIdleConns(2) right after —
// so the tuned values never took effect and concurrent multi-tab queries
// queued on five connections. Pool sizing must have a single source of
// truth: backend/database/engine.go.
func TestConnectDoesNotOverridePoolSizing(t *testing.T) {
	src, err := os.ReadFile("database_session.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	for _, forbidden := range []string{"SetMaxOpenConns", "SetMaxIdleConns"} {
		if strings.Contains(string(src), forbidden) {
			t.Fatalf("database_session.go calls %s — pool sizing belongs in database.NewDB (DB-13)", forbidden)
		}
	}
}
