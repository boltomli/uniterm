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

// TestQueryHelpersHonorQueryTimeout guards F-12.4: queryStrings/queryAny must
// run under queryTimeout, otherwise a hung server pins the calling goroutine
// (and a pooled connection) forever. The watchdog makes a regression fail
// instead of hanging the test binary.
func TestQueryHelpersHonorQueryTimeout(t *testing.T) {
	old := queryTimeout
	queryTimeout = 200 * time.Millisecond
	t.Cleanup(func() { queryTimeout = old })

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
				t.Fatalf("%s: took %v, queryTimeout=%v not respected", name, elapsed, queryTimeout)
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
