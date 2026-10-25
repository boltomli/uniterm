package database

import (
	"path/filepath"
	"testing"
)

// TestUpdateRowsBatchIsTransactional pins DB-05 (2026-10-25 regression
// audit): a grid save used to issue one UPDATE per staged cell with no
// transaction — the first failure left earlier rows committed while the
// frontend reverted/kept the rest inconsistently. The batch path must:
//   - apply every row's snapshot WHERE inside ONE transaction,
//   - report per-row affected counts in order (0 = snapshot went stale),
//   - roll the WHOLE batch back when any statement fails.
func TestUpdateRowsBatchIsTransactional(t *testing.T) {
	p, err := NewProvider("sqlite")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	path := filepath.Join(t.TempDir(), "batch.db")
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
		{"id": int64(3), "name": "c"},
	} {
		if err := p.InsertRow(db, "main", "items", r); err != nil {
			t.Fatalf("InsertRow: %v", err)
		}
	}

	// Happy path: three rows, per-row counts in order; a stale snapshot in
	// the middle reports 0 while the others commit.
	counts, err := p.UpdateRows(db, "main", "items", []RowEdit{
		{Set: map[string]any{"name": "a2"}, Where: map[string]any{"id": int64(1)}},
		{Set: map[string]any{"name": "ghost"}, Where: map[string]any{"id": int64(99)}},
		{Set: map[string]any{"name": "c2"}, Where: map[string]any{"id": int64(3)}},
	})
	if err != nil {
		t.Fatalf("UpdateRows: %v", err)
	}
	if len(counts) != 3 || counts[0] != 1 || counts[1] != 0 || counts[2] != 1 {
		t.Fatalf("counts = %v, want [1 0 1]", counts)
	}
	rows, err := queryStrings(db, p.DefaultTableQuery("main", "items", 50, 0))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r["id"]] = r["name"]
	}
	if got["1"] != "a2" || got["3"] != "c2" || got["2"] != "b" {
		t.Fatalf("rows after batch = %v", got)
	}

	// Failure path: the second statement names a nonexistent column — the
	// whole batch must roll back, including the first (valid) row.
	if _, err := p.UpdateRows(db, "main", "items", []RowEdit{
		{Set: map[string]any{"name": "should-roll-back"}, Where: map[string]any{"id": int64(1)}},
		{Set: map[string]any{"no_such_column": 1}, Where: map[string]any{"id": int64(2)}},
	}); err == nil {
		t.Fatal("expected the failing batch to return an error")
	}
	rows, err = queryStrings(db, p.DefaultTableQuery("main", "items", 50, 0))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, r := range rows {
		if r["id"] == "1" && r["name"] != "a2" {
			t.Fatalf("row 1 changed to %q — the failed batch was not rolled back (DB-05)", r["name"])
		}
	}
}
