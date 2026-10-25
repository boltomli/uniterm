package database

import (
	"context"
	"testing"
	"time"
)

// TestExecuteQueryCtxHonoursParentCancellation pins the DB-16 backend
// contract: the bound App method passes the binding call's request context
// here, so the frontend cancel button must abort a running statement
// immediately — not after the 30s query budget. hangConn (engine_timeout_test)
// blocks until its context is done, which is exactly the "server never
// answers" case the cancel button exists for.
func TestExecuteQueryCtxHonoursParentCancellation(t *testing.T) {
	db := openHangDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := ExecuteQueryCtx(ctx, stubProvider{}, db, "test", "SELECT v")
	if err == nil {
		t.Fatal("expected an error after parent cancellation")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v — the statement is not bound to the parent context", elapsed)
	}
}

// cancelStubProvider supplies DBType (stubProvider's embedded nil Provider
// would panic on the call), everything else stays stubbed.
type cancelStubProvider struct{ stubProvider }

func (cancelStubProvider) DBType() string { return "sqlite" }

// Same contract for the script runner: cancelling between statements stops
// the run.
func TestExecuteScriptCtxStopsOnCancellation(t *testing.T) {
	db := openHangDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the first statement

	start := time.Now()
	res, err := ExecuteScriptCtx(ctx, cancelStubProvider{}, db, "test", "SELECT 1")
	if err == nil && (res == nil || res.FailedLine == 0) {
		t.Fatal("expected the script to fail/stop on an already-cancelled context")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancelled script took %v", elapsed)
	}
}
