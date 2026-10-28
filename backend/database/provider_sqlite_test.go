package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteProviderRegistered(t *testing.T) {
	if _, err := NewProvider("sqlite"); err != nil {
		t.Fatalf("sqlite provider not registered: %v", err)
	}
}

func TestSQLiteDSNPlainPath(t *testing.T) {
	p, err := NewProvider("sqlite")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	got := p.DSN(`C:\data\app.db`, 0, "", "", "", nil)
	if got != `C:\data\app.db` {
		t.Fatalf("got %q, want plain path", got)
	}
}

func TestSQLiteDSNParamsBecomePragmas(t *testing.T) {
	p, err := NewProvider("sqlite")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	got := p.DSN(`C:\data\a b.db`, 0, "", "", "", map[string]string{"journal_mode": "WAL"})
	want := `file:C:/data/a b.db?_pragma=busy_timeout%3D5000&_pragma=journal_mode%3DWAL`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSQLiteSchemaAndCRUD(t *testing.T) {
	p, err := NewProvider("sqlite")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	dbs, err := p.GetDatabases(nil)
	if err != nil || len(dbs) != 1 || dbs[0] != "main" {
		t.Fatalf("GetDatabases = %v, %v; want [main]", dbs, err)
	}

	path := filepath.Join(t.TempDir(), "test.db")
	db, err := NewDB("sqlite", p.DSN(path, 0, "", "", "", nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := p.CreateTable(db, "main", "items"); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if err := p.AddColumn(db, "main", "items", ColumnDef{Name: "name", Type: "TEXT", Nullable: true}); err != nil {
		t.Fatalf("AddColumn: %v", err)
	}
	if err := p.InsertRow(db, "main", "items", map[string]any{"id": int64(1), "name": "a"}); err != nil {
		t.Fatalf("InsertRow: %v", err)
	}

	tables, err := p.GetTables(db, "main")
	if err != nil {
		t.Fatalf("GetTables: %v", err)
	}
	found := false
	for _, tb := range tables {
		if tb.Name == "items" && tb.Type == "table" {
			found = true
		}
	}
	if !found {
		t.Fatalf("table items not found in %v", tables)
	}

	schema, err := p.GetTableSchema(db, "main", "items")
	if err != nil {
		t.Fatalf("GetTableSchema: %v", err)
	}
	if len(schema.Columns) != 2 || schema.Columns[0].Name != "id" || !schema.Columns[0].IsPrimary || schema.Columns[1].Name != "name" {
		t.Fatalf("unexpected schema: %+v", schema.Columns)
	}

	// The result grid sends all changed fields for a row in one UPDATE,
	// using the original primary key to locate it.
	if _, err := p.UpdateRow(db, "main", "items", map[string]any{"id": int64(2), "name": "b"}, map[string]any{"id": int64(1)}); err != nil {
		t.Fatalf("UpdateRow: %v", err)
	}
	rows, err := queryStrings(db, p.DefaultTableQuery("main", "items", 50, 0))
	if err != nil {
		t.Fatalf("DefaultTableQuery: %v", err)
	}
	if len(rows) != 1 || rows[0]["id"] != "2" || rows[0]["name"] != "b" {
		t.Fatalf("unexpected rows: %v", rows)
	}

	if _, err := p.DeleteRow(db, "main", "items", map[string]any{"id": int64(2)}); err != nil {
		t.Fatalf("DeleteRow: %v", err)
	}
	rows, err = queryStrings(db, p.DefaultTableQuery("main", "items", 50, 0))
	if err != nil {
		t.Fatalf("DefaultTableQuery: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty table, got %v", rows)
	}
}

// TestSQLiteDSNEscapesURIStructure: a path or parameter token carrying URI
// structural characters must be percent-encoded, so it cannot break out of
// the path segment (opening a different file / injecting an extra pragma)
// and a literal "%" survives the driver's percent-decoding intact.
func TestSQLiteDSNEscapesURIStructure(t *testing.T) {
	p, err := NewProvider("sqlite")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	got := p.DSN(`/data/a?b#c&d%e.db`, 0, "", "", "", map[string]string{"busy_timeout": "1000&x=2"})
	want := `file:/data/a%3Fb%23c%26d%25e.db?_pragma=busy_timeout%3D1000%26x%3D2`
	if got != want {
		t.Fatalf("DSN = %q, want %q", got, want)
	}
	// Driver-level options pass through the same token escaping.
	got = p.DSN(`/data/x.db`, 0, "", "", "", map[string]string{"_timefmt": "%Y%m%d&z"})
	want = `file:/data/x.db?_pragma=busy_timeout%3D5000&_timefmt=%25Y%25m%25d%26z`
	if got != want {
		t.Fatalf("DSN = %q, want %q", got, want)
	}
}

// TestSQLiteEscapedPathOpensSameFile: proves the escaped file: URI is
// accepted by the driver and lands on the configured file — not a
// percent-decoded lookalike (which would silently open an empty database).
func TestSQLiteEscapedPathOpensSameFile(t *testing.T) {
	p, err := NewProvider("sqlite")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "a&b%c#d.db")

	seed, err := NewDB("sqlite", path)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE marker (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	db, err := NewDB("sqlite", p.DSN(path, 0, "", "", "", map[string]string{"busy_timeout": "5000"}))
	if err != nil {
		t.Fatalf("escaped open: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'marker'`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("escaped DSN opened the wrong file (marker table missing, n=%d)", n)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("escaped DSN created stray files: %v", names)
	}
}
