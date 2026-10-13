package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// pgDumpDDLDriver serves one column per name handed via the DSN, all of type
// `text` with no default and not-null, so the dump-DDL path
// (pgCreateTableSQL) can be exercised without a live Postgres.
type pgDumpDDLDriver struct{}

type pgDumpDDLNopTx struct{}

func (pgDumpDDLNopTx) Commit() error   { return nil }
func (pgDumpDDLNopTx) Rollback() error { return nil }

func (pgDumpDDLDriver) Open(namesCSV string) (driver.Conn, error) {
	return &pgDumpDDLConn{names: strings.Split(namesCSV, "\x00")}, nil
}

type pgDumpDDLConn struct{ names []string }

func (c *pgDumpDDLConn) Prepare(query string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *pgDumpDDLConn) Close() error                              { return nil }
func (c *pgDumpDDLConn) Begin() (driver.Tx, error)                 { return pgDumpDDLNopTx{}, nil }

func (c *pgDumpDDLConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	// The column query is first; the constraint query follows and returns
	// zero rows.
	if strings.Contains(query, "pg_get_expr") {
		return &pgDumpDDLRows{names: c.names}, nil
	}
	return &pgDumpDDLRows{}, nil
}

type pgDumpDDLRows struct {
	names []string
	i     int
}

func (r *pgDumpDDLRows) Columns() []string { return []string{"attname", "fmt", "attnotnull", "default"} }
func (r *pgDumpDDLRows) Close() error      { return nil }
func (r *pgDumpDDLRows) Next(dest []driver.Value) error {
	if r.i >= len(r.names) {
		return io.EOF
	}
	// A nil driver.Value is what makes the `sql.NullString` scan on the
	// default column succeed; passing a sql.NullString value does not.
	dest[0] = r.names[r.i]
	dest[1] = "text"
	dest[2] = false
	dest[3] = nil
	r.i++
	return nil
}

var registerPgDumpDDLDriver sync.Once

func openPgDumpDDLConn(t *testing.T, names []string) *sql.Conn {
	t.Helper()
	const driverName = "pg_dump_ddl_test"
	registerPgDumpDDLDriver.Do(func() { sql.Register(driverName, pgDumpDDLDriver{}) })
	dsn := strings.Join(names, "\x00")
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestPgDumpDDLQuotingIdentifiers guards the fix for the discarded
// SafePgIdent error in the dump-DDL path. Column names that SafeIdentValidate
// rejects as "path traversal" (`..`, `/`, `\`) are perfectly legal Postgres
// identifiers. The old code did `quotedName, _ := SafePgIdent(name)` and
// silently emitted an empty identifier, producing `CREATE TABLE t (\n   text\n)`.
// Dump DDL must quote by doubling `"` — the same rule as Quote — so every
// name stays non-empty and inert inside its quotes.
func TestPgDumpDDLQuotingIdentifiers(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "id", `"id"`},
		{"dotdot", "a..b", `"a..b"`},
		{"slash", "a/b", `"a/b"`},
		{"backslash", `a\b`, `"a\b"`},
		{"quote doubled", `we"ird`, `"we""ird"`},
		{"all three", `x../y\z`, `"x../y\z"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := openPgDumpDDLConn(t, []string{tc.input})
			out, err := pgCreateTableSQL(conn, "public", "t", `"t"`)
			if err != nil {
				t.Fatalf("pgCreateTableSQL: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("dump DDL does not contain quoted column %s:\n%s", tc.want, out)
			}
			// The column line must carry a non-empty identifier before the
			// type; the old bug left it blank.
			bad := "  " + " " + "text"
			for _, line := range strings.Split(out, "\n") {
				if strings.TrimSpace(line) == "text" || line == bad {
					t.Fatalf("column emitted with an empty identifier:\n%s", out)
				}
			}
		})
	}
}

// TestPgDumpDDLEmptyIdentifierNeverAppears is a direct guard on the shape the
// old bug produced: `CREATE TABLE t (\n   text\n)` — a bare type with no
// column name. No legal input may ever yield that.
func TestPgDumpDDLEmptyIdentifierNeverAppears(t *testing.T) {
	inputs := []string{"a..b", "a/b", `a\b`, `a"b`}
	for _, in := range inputs {
		t.Run(fmt.Sprintf("%q", in), func(t *testing.T) {
			conn := openPgDumpDDLConn(t, []string{in})
			out, err := pgCreateTableSQL(conn, "public", "t", `"t"`)
			if err != nil {
				t.Fatalf("pgCreateTableSQL: %v", err)
			}
			if strings.Contains(out, "(\n   text") || strings.Contains(out, "\n   text\n") {
				t.Fatalf("empty column identifier in dump DDL for input %q:\n%s", in, out)
			}
		})
	}
}
