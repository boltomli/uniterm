package database

import (
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

	if err := p.UpdateRow(db, "main", "items", map[string]any{"name": "b"}, map[string]any{"id": int64(1)}); err != nil {
		t.Fatalf("UpdateRow: %v", err)
	}
	rows, err := queryStrings(db, p.DefaultTableQuery("main", "items", 50, 0))
	if err != nil {
		t.Fatalf("DefaultTableQuery: %v", err)
	}
	if len(rows) != 1 || rows[0]["name"] != "b" {
		t.Fatalf("unexpected rows: %v", rows)
	}

	if err := p.DeleteRow(db, "main", "items", map[string]any{"id": int64(1)}); err != nil {
		t.Fatalf("DeleteRow: %v", err)
	}
	rows, err = queryStrings(db, p.PagedTableQuery("main", "items", 50, 0))
	if err != nil {
		t.Fatalf("PagedTableQuery: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty table, got %v", rows)
	}
}
