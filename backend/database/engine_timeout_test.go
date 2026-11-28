package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"testing"
	"time"
)

// hangDriver's QueryContext blocks until the statement context finishes,
// simulating a slow/unresponsive database server that never returns rows.
type hangDriver struct{}

func (hangDriver) Open(name string) (driver.Conn, error) { return &hangConn{}, nil }

type hangConn struct{}

func (c *hangConn) Prepare(query string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *hangConn) Close() error                              { return nil }
func (c *hangConn) Begin() (driver.Tx, error)                 { return nil, driver.ErrSkip }

func (c *hangConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

var registerHangDriver sync.Once

func openHangDB(t *testing.T) *sql.DB {
	t.Helper()
	const driverName = "hang_test"
	registerHangDriver.Do(func() { sql.Register(driverName, hangDriver{}) })
	db, err := sql.Open(driverName, "hang")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestQueryHelpersHonorSchemaTimeout guards F-12.4: queryStrings/queryAny
// are the schema/metadata probe helpers and must run under schemaTimeout,
// otherwise a hung server pins the calling goroutine (and a pooled
// connection) forever. The watchdog makes a regression fail instead of
// hanging the test binary.
func TestQueryHelpersHonorSchemaTimeout(t *testing.T) {
	old := schemaTimeout
	schemaTimeout = 200 * time.Millisecond
	t.Cleanup(func() { schemaTimeout = old })

	run := func(name string, fn func(db *sql.DB) error) {
		t.Helper()
		db := openHangDB(t)
		done := make(chan error, 1)
		start := time.Now()
		go func() { done <- fn(db) }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("%s: expected an error from a hung server", name)
			}
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("%s: took %v, schemaTimeout=%v not respected", name, elapsed, schemaTimeout)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: did not return within 5s — query without context (F-12.4 regression)", name)
		}
	}

	run("queryStrings", func(db *sql.DB) error {
		_, err := queryStrings(db, "SELECT hang")
		return err
	})
	run("queryAny", func(db *sql.DB) error {
		_, _, err := queryAny(db, "SELECT hang")
		return err
	})
}

// TestQueryHelpersDmGetsNoDeadlineButKeepsParentCancel pins the dm
// exemption in dbCtx: the driver nil-derefs on a deadline context, so dm
// must get none — but it must still inherit the caller's cancellation, or a
// hung dm server would pin the goroutine for good (the F-12.4 failure mode
// the exemption must not reintroduce). It needs a *sql.DB recorded as "dm"
// under a driver that hangs until its context ends, so the same hang driver
// is reused with the dm name recorded for the handle.
func TestQueryHelpersDmGetsNoDeadlineButKeepsParentCancel(t *testing.T) {
	old := schemaTimeout
	schemaTimeout = 100 * time.Millisecond
	t.Cleanup(func() { schemaTimeout = old })

	db := openHangDB(t)
	trackDBDriver(db, "dm")

	// 1. No deadline: with the (short) schemaTimeout budget and no caller
	//    cancellation, the helper must still be waiting well past the
	//    budget — a deadline would have fired by then and returned.
	done := make(chan error, 1)
	go func() { _, _, err := queryAny(db, "SELECT hang"); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("dm query returned under a deadline-free policy (err=%v) — dbCtx gave it a deadline", err)
	case <-time.After(3 * schemaTimeout):
	}

	// 2. Parent cancellation still reaches it: cancel the parent and the
	//    helper must come back promptly with the context error.
	parent, cancel := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() {
		_, err := queryStringsCtx(parent, db, "SELECT hang")
		done2 <- err
	}()
	time.Sleep(2 * schemaTimeout)
	cancel()
	select {
	case err := <-done2:
		if err == nil {
			t.Fatal("dm query returned nil after parent cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dm query ignored parent cancellation — a hung dm server would pin this goroutine forever (F-12.4)")
	}

	// The un-cancelled query from step 1 is released by closing the handle.
	_ = db.Close()
}
