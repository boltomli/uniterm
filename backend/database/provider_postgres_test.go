package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL tree reuses the MSSQL composite-node encoding
// ("db.schema" nodes, schema-qualified table names); these tests lock the
// quoting and node-splitting behavior.

func TestPostgresQualifyTable(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"employees", `"employees"`},
		{"sales.employees", `"sales"."employees"`},
		{"my.db.sales", `"my.db"."sales"`},
	}
	p := &postgresProvider{}
	for _, tc := range cases {
		if got := p.QualifyTable(tc.in); got != tc.want {
			t.Errorf("QualifyTable(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPostgresQualifyWithNodeSchema(t *testing.T) {
	cases := []struct {
		dbName    string
		tableName string
		want      string
	}{
		{"shop.sales", "newtbl", "sales.newtbl"},
		{"shop.sales", "dbo.explicit", "dbo.explicit"},
		{"shop", "newtbl", "newtbl"},
		{"", "newtbl", "newtbl"},
	}
	p := &postgresProvider{}
	for _, tc := range cases {
		if got := p.qualifyWithNodeSchema(tc.dbName, tc.tableName); got != tc.want {
			t.Errorf("qualifyWithNodeSchema(%q, %q) = %q, want %q",
				tc.dbName, tc.tableName, got, tc.want)
		}
	}
}

// TestPostgresDSNParsesWithPgx locks the DSN shape the provider generates to
// what the pgx stdlib driver accepts. lib/pq was lenient about unknown URL
// query parameters; pgx treats them as runtime parameters, but a handful of
// lib/pq-era keywords are rejected outright — a regression here would break
// every postgres connection at sql.Open time.
func TestPostgresDSNParsesWithPgx(t *testing.T) {
	cases := []struct {
		name        string
		extraParams string
	}{
		{"plain", ""},
		{"sslmode override", "sslmode=require"},
		{"connect timeout", "connect_timeout=10"},
		{"runtime param", "search_path=public,app"},
		{"combined", "sslmode=verify-full&connect_timeout=5&application_name=uniterm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn, err := BuildDSN("postgres", "pg.example.com", "user", "pass", "appdb", tc.extraParams, 5433)
			if err != nil {
				t.Fatalf("BuildDSN: %v", err)
			}
			cfg, err := pgx.ParseConfig(dsn)
			if err != nil {
				t.Fatalf("pgx rejected generated DSN %q: %v", dsn, err)
			}
			if cfg.Host != "pg.example.com" || cfg.Port != 5433 {
				t.Errorf("host/port mismatch: host=%q port=%d", cfg.Host, cfg.Port)
			}
		})
	}
}

// captureDriver records statements at the driver boundary. Issue #1621 was a
// bug in the generated SQL text, not in server-side behaviour, so asserting
// on the exact statement is the only regression guard that would have caught
// it: testing QualifyTable alone does not prove the CRUD callers use it.
// No external mock dependency or live PostgreSQL server is required.
type captureDriver struct {
	mu      sync.Mutex
	queries []string
	args    [][]driver.NamedValue
}

func (d *captureDriver) Open(string) (driver.Conn, error) { return &captureConn{d: d}, nil }

func (d *captureDriver) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = nil
	d.args = nil
}

func (d *captureDriver) record(query string, args []driver.NamedValue) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = append(d.queries, query)
	d.args = append(d.args, append([]driver.NamedValue(nil), args...))
}

func (d *captureDriver) snapshot() ([]string, [][]driver.NamedValue) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.queries...), append([][]driver.NamedValue(nil), d.args...)
}

type captureConn struct{ d *captureDriver }

func (c *captureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("capture driver does not implement Prepare")
}
func (c *captureConn) Close() error { return nil }
func (c *captureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("capture driver does not implement Begin")
}

func (c *captureConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.d.record(query, args)
	return driver.RowsAffected(1), nil
}

var crudCapture = &captureDriver{}

func init() {
	sql.Register("uniterm-pg-crud-capture", crudCapture)
}

func newCaptureDB(t *testing.T) *sql.DB {
	t.Helper()
	crudCapture.reset()
	db, err := sql.Open("uniterm-pg-crud-capture", "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestPostgresCRUDQualifiesSchemaTable locks the fix for issue #1621: the
// schema-grouped tree hands out "schema.table" names, which must be quoted as
// "schema"."table". Quoting the whole string as a single identifier produces
// DELETE FROM "public.user_log", i.e. SQLSTATE 42P01 on PostgreSQL/GaussDB.
// Bare names (the pre-1.12.4 flat tree) must keep working unchanged.
func TestPostgresCRUDQualifiesSchemaTable(t *testing.T) {
	p := &postgresProvider{}

	tables := []struct {
		name      string
		tableName string
		wantIdent string
	}{
		{"schema_qualified", "public.user_log", `"public"."user_log"`},
		{"bare_legacy", "user_log", `"user_log"`},
		{"dotted_database", "my.db.sales", `"my.db"."sales"`},
	}

	operations := []struct {
		name     string
		run      func(db *sql.DB, table string) error
		wantSQL  func(ident string) string
		wantArgs int
	}{
		{
			name: "insert",
			run: func(db *sql.DB, table string) error {
				return p.InsertRow(db, "shop", table, map[string]any{"id": int64(7)})
			},
			wantSQL: func(ident string) string {
				return `INSERT INTO ` + ident + ` ("id") VALUES ($1)`
			},
			wantArgs: 1,
		},
		{
			name: "update",
			run: func(db *sql.DB, table string) error {
				// UpdateRow/DeleteRow return (affected, error) since DB-05; the SQL
				// text is what issue #1621 was about, so drop the count here.
				_, err := p.UpdateRow(db, "shop", table,
					map[string]any{"name": "alice"}, map[string]any{"id": int64(7)})
				return err
			},
			wantSQL: func(ident string) string {
				return `UPDATE ` + ident + ` SET "name" = $1 WHERE "id" = $2`
			},
			wantArgs: 2,
		},
		{
			name: "delete",
			run: func(db *sql.DB, table string) error {
				_, err := p.DeleteRow(db, "shop", table, map[string]any{"id": int64(7)})
				return err
			},
			wantSQL: func(ident string) string {
				return `DELETE FROM ` + ident + ` WHERE "id" = $1`
			},
			wantArgs: 1,
		},
		{
			name: "drop_view",
			run: func(db *sql.DB, table string) error {
				return p.DropView(db, "shop", table)
			},
			wantSQL: func(ident string) string {
				return `DROP VIEW ` + ident
			},
			wantArgs: 0,
		},
	}

	for _, tc := range tables {
		for _, op := range operations {
			t.Run(tc.name+"/"+op.name, func(t *testing.T) {
				db := newCaptureDB(t)
				if err := op.run(db, tc.tableName); err != nil {
					t.Fatalf("%s: %v", op.name, err)
				}
				queries, args := crudCapture.snapshot()
				if len(queries) != 1 {
					t.Fatalf("got %d statements, want 1: %q", len(queries), queries)
				}
				want := op.wantSQL(tc.wantIdent)
				if queries[0] != want {
					t.Errorf("SQL = %q\nwant %q", queries[0], want)
				}
				if len(args) != 1 || len(args[0]) != op.wantArgs {
					t.Errorf("bound args = %v, want %d values", args, op.wantArgs)
				}
			})
		}
	}
}
