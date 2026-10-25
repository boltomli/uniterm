package database

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestDumpTableToStreamsAndCaps pins DB-12 (2026-10-25 regression audit):
// exports used to build one giant in-memory SQL string, ship it through the
// webview as base64, and have the frontend write it back — three copies and
// an unbounded row count. The streaming entry point writes to an io.Writer
// and honours a row cap, marking truncation explicitly.
func TestDumpTableToStreamsAndCaps(t *testing.T) {
	p, err := NewProvider("sqlite")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	path := filepath.Join(t.TempDir(), "dump.db")
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
	for i := int64(1); i <= 5; i++ {
		if err := p.InsertRow(db, "main", "items", map[string]any{"id": i, "name": "row"}); err != nil {
			t.Fatalf("InsertRow: %v", err)
		}
	}

	// Uncapped: everything lands in the writer.
	var buf bytes.Buffer
	n, err := DumpTableTo(p, db, "main", "items", DumpOptions{Structure: true, Data: true}, 0, &buf)
	if err != nil {
		t.Fatalf("DumpTableTo: %v", err)
	}
	if n != 5 {
		t.Fatalf("rows = %d, want 5", n)
	}
	if !strings.Contains(buf.String(), "CREATE TABLE") || strings.Count(buf.String(), "INSERT INTO") != 5 {
		t.Fatalf("unexpected dump:\n%s", buf.String())
	}

	// Capped: at most maxRows INSERTs, truncation marked, structure intact.
	buf.Reset()
	n, err = DumpTableTo(p, db, "main", "items", DumpOptions{Structure: true, Data: true}, 2, &buf)
	if err != nil {
		t.Fatalf("DumpTableTo capped: %v", err)
	}
	if n != 2 {
		t.Fatalf("capped rows = %d, want 2", n)
	}
	out := buf.String()
	if strings.Count(out, "INSERT INTO") != 2 {
		t.Fatalf("capped dump kept %d INSERTs, want 2:\n%s", strings.Count(out, "INSERT INTO"), out)
	}
	if !strings.Contains(out, "-- truncated: dropped 3 rows after the 2 row export cap") {
		t.Fatalf("capped dump is not marked as truncated:\n%s", out)
	}
	if !strings.Contains(out, "CREATE TABLE") {
		t.Fatalf("capped dump lost the structure section:\n%s", out)
	}
}
