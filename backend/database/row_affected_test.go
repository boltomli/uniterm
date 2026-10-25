package database

import (
	"path/filepath"
	"testing"
)

// TestUpdateDeleteRowReportAffectedRows pins the DB-01 contract fix: the
// grid builds each UPDATE/DELETE WHERE clause from a snapshot of the row as
// it was read. If the row moved on in the database (edited or deleted by
// someone else, or the snapshot itself was stale), the statement matches
// nothing — and that must be observable as an affected count of 0 instead of
// being reported to the user as a successful save.
func TestUpdateDeleteRowReportAffectedRows(t *testing.T) {
	p, err := NewProvider("sqlite")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	path := filepath.Join(t.TempDir(), "affected.db")
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
	for _, r := range []map[string]any{
		{"id": int64(1), "name": "a"},
		{"id": int64(2), "name": "b"},
	} {
		if err := p.InsertRow(db, "main", "items", r); err != nil {
			t.Fatalf("InsertRow: %v", err)
		}
	}

	// Snapshot WHERE (id = 1) matches exactly one row.
	n, err := p.UpdateRow(db, "main", "items", map[string]any{"name": "a2"}, map[string]any{"id": int64(1)})
	if err != nil {
		t.Fatalf("UpdateRow: %v", err)
	}
	if n != 1 {
		t.Fatalf("UpdateRow affected = %d, want 1", n)
	}

	// Stale snapshot: the row with this PK no longer exists → 0, not success.
	n, err = p.UpdateRow(db, "main", "items", map[string]any{"name": "ghost"}, map[string]any{"id": int64(99)})
	if err != nil {
		t.Fatalf("UpdateRow stale: %v", err)
	}
	if n != 0 {
		t.Fatalf("UpdateRow(stale WHERE) affected = %d, want 0", n)
	}

	n, err = p.DeleteRow(db, "main", "items", map[string]any{"id": int64(2)})
	if err != nil {
		t.Fatalf("DeleteRow: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteRow affected = %d, want 1", n)
	}

	// Deleting the same snapshot twice must report the second as a no-op.
	n, err = p.DeleteRow(db, "main", "items", map[string]any{"id": int64(2)})
	if err != nil {
		t.Fatalf("DeleteRow twice: %v", err)
	}
	if n != 0 {
		t.Fatalf("DeleteRow(already deleted) affected = %d, want 0", n)
	}
}
