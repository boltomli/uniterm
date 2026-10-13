package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strconv"
	"sync"
	"testing"
)

// cappedRowsDriver serves a fixed number of single-column int64 rows so the
// ExecuteQuery row cap can be tested without a live database. The DSN is the
// row count.
type cappedRowsDriver struct{}

func (cappedRowsDriver) Open(name string) (driver.Conn, error) {
	n, err := strconv.Atoi(name)
	if err != nil {
		return nil, err
	}
	return &cappedRowsConn{n: n}, nil
}

type cappedRowsConn struct{ n int }

func (c *cappedRowsConn) Prepare(query string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *cappedRowsConn) Close() error                              { return nil }
func (c *cappedRowsConn) Begin() (driver.Tx, error)                 { return nil, driver.ErrSkip }

func (c *cappedRowsConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return &cappedRowsRows{n: c.n}, nil
}

type cappedRowsRows struct {
	n int
	i int
}

func (r *cappedRowsRows) Columns() []string { return []string{"v"} }
func (r *cappedRowsRows) Close() error      { return nil }
func (r *cappedRowsRows) Next(dest []driver.Value) error {
	if r.i >= r.n {
		return io.EOF
	}
	dest[0] = int64(r.i)
	r.i++
	return nil
}

// stubProvider satisfies Provider for executor tests; only PrepareExec is
// exercised. Embedding the interface panics loudly if another method is hit.
type stubProvider struct{ Provider }

func (stubProvider) PrepareExec(db execer, dbName string) error { return nil }

var registerCappedRowsDriver sync.Once

func openCappedRowsDB(t *testing.T, rows int) *sql.DB {
	t.Helper()
	const driverName = "capped_rows_test"
	registerCappedRowsDriver.Do(func() { sql.Register(driverName, cappedRowsDriver{}) })
	db, err := sql.Open(driverName, strconv.Itoa(rows))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestExecuteQueryCapsRows guards the P3 large-result-set fix: a query
// returning more rows than the cap is truncated and flagged.
func TestExecuteQueryCapsRows(t *testing.T) {
	old := maxQueryRows
	maxQueryRows = 5
	t.Cleanup(func() { maxQueryRows = old })

	db := openCappedRowsDB(t, 20)
	qr, err := ExecuteQuery(stubProvider{}, db, "test", "SELECT v")
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if len(qr.Rows) != 5 {
		t.Fatalf("got %d rows, want capped 5", len(qr.Rows))
	}
	if !qr.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(qr.Columns) != 1 || qr.Columns[0].Name != "v" {
		t.Fatalf("columns = %+v, want [v]", qr.Columns)
	}
}

func TestExecuteQueryExactCapIsNotTruncated(t *testing.T) {
	old := maxQueryRows
	maxQueryRows = 5
	t.Cleanup(func() { maxQueryRows = old })

	db := openCappedRowsDB(t, 5)
	qr, err := ExecuteQuery(stubProvider{}, db, "test", "SELECT v")
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if len(qr.Rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(qr.Rows))
	}
	if qr.Truncated {
		t.Fatal("Truncated = true for an exact-cap result, want false")
	}
}

func TestExecuteQueryBelowCapNotTruncated(t *testing.T) {
	old := maxQueryRows
	maxQueryRows = 5
	t.Cleanup(func() { maxQueryRows = old })

	db := openCappedRowsDB(t, 3)
	qr, err := ExecuteQuery(stubProvider{}, db, "test", "SELECT v")
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if len(qr.Rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(qr.Rows))
	}
	if qr.Truncated {
		t.Fatal("Truncated = true for a below-cap result, want false")
	}
}

// dupColsDriver reports two columns that share the name "id" (as in
// `SELECT a.id, b.id FROM a JOIN b …`) and serves one row with distinct
// values. The old name-keyed []map[string]any row shape kept only the last
// value and the grid showed it twice.
type dupColsDriver struct{}

func (dupColsDriver) Open(name string) (driver.Conn, error) { return &dupColsConn{}, nil }

type dupColsConn struct{}

func (c *dupColsConn) Prepare(query string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *dupColsConn) Close() error                              { return nil }
func (c *dupColsConn) Begin() (driver.Tx, error)                 { return nil, driver.ErrSkip }

func (c *dupColsConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return &dupColsRows{}, nil
}

type dupColsRows struct{ sent bool }

func (r *dupColsRows) Columns() []string { return []string{"id", "id"} }
func (r *dupColsRows) Close() error      { return nil }
func (r *dupColsRows) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	dest[0] = int64(1)
	dest[1] = int64(2)
	return nil
}

var registerDupColsDriver sync.Once

// TestExecuteQueryKeepsDuplicateColumnNames guards the positional-row fix:
// both same-named columns must survive with their own values.
func TestExecuteQueryKeepsDuplicateColumnNames(t *testing.T) {
	const driverName = "dup_cols_test"
	registerDupColsDriver.Do(func() { sql.Register(driverName, dupColsDriver{}) })
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	qr, err := ExecuteQuery(stubProvider{}, db, "test", "SELECT a.id, b.id")
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if len(qr.Columns) != 2 || qr.Columns[0].Name != "id" || qr.Columns[1].Name != "id" {
		t.Fatalf("columns = %+v, want two entries named %q", qr.Columns, "id")
	}
	if len(qr.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(qr.Rows))
	}
	row := qr.Rows[0]
	if len(row) != 2 {
		t.Fatalf("row has %d cells, want 2 (one per column)", len(row))
	}
	if row[0] != int64(1) || row[1] != int64(2) {
		t.Fatalf("row = %v, want [1 2]", row)
	}
}
