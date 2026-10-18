package database

import (
	"strings"
	"testing"
)

// ── F-11: DDL identifier / type safety ──────────────────────────────────────

// Column types are free text in the UI, so every provider's DDL builders
// validate them through SafeColumnType before interpolation.
func TestSafeColumnType(t *testing.T) {
	ok := []string{
		"VARCHAR2(100)",
		"NUMBER(10,2)",
		"TIMESTAMP(6) WITH TIME ZONE",
		"NVARCHAR(255)",
		"DECIMAL(10, 2)",
		"CHAR(100 CHAR)",
		"int",
		"BIGINT",
		"DOUBLE PRECISION",
		"  varchar(50)  ", // trimmed
	}
	for _, in := range ok {
		got, err := SafeColumnType(in)
		if err != nil {
			t.Errorf("SafeColumnType(%q) = %v, want accepted", in, err)
			continue
		}
		if got != strings.TrimSpace(in) {
			t.Errorf("SafeColumnType(%q) = %q, want trimmed input", in, got)
		}
	}

	bad := []string{
		"",
		"INT; DROP TABLE users; --",
		`INT"; DROP TABLE users; --`,
		"INT`; DROP TABLE users; --",
		"INT-- comment",
		"INT /* comment */",
		"INT\nDROP TABLE users",
		"INT\tdrop",
		`INT\C:\x`,
		strings.Repeat("a", 200),
	}
	for _, in := range bad {
		if got, err := SafeColumnType(in); err == nil {
			t.Errorf("SafeColumnType(%q) = %q, want rejection", in, got)
		}
	}
}

func TestSafeColumnCollation(t *testing.T) {
	if got, err := SafeColumnCollation("utf8mb4_unicode_ci"); err != nil || got != "utf8mb4_unicode_ci" {
		t.Errorf("SafeColumnCollation(valid) = %q, %v", got, err)
	}
	if got, err := SafeColumnCollation(""); err != nil || got != "" {
		t.Errorf("SafeColumnCollation(empty) = %q, %v, want no-op", got, err)
	}
	for _, in := range []string{"utf8; DROP TABLE t", "a b", "x`y", `x"y`, "x-y", strings.Repeat("a", 100)} {
		if got, err := SafeColumnCollation(in); err == nil {
			t.Errorf("SafeColumnCollation(%q) = %q, want rejection", in, got)
		}
	}
}

func TestSafeColumnTypeForRewritesColumn(t *testing.T) {
	col, err := safeColumnTypeFor(ColumnDef{Name: "c", Type: "  NUMBER(10,2) ", Collation: "utf8_general_ci"})
	if err != nil {
		t.Fatalf("safeColumnTypeFor: %v", err)
	}
	if col.Type != "NUMBER(10,2)" || col.Collation != "utf8_general_ci" {
		t.Fatalf("not normalized: %+v", col)
	}
	if _, err := safeColumnTypeFor(ColumnDef{Name: "c", Type: "INT; DROP TABLE t"}); err == nil {
		t.Fatal("unsafe type must be rejected")
	}
	if _, err := safeColumnTypeFor(ColumnDef{Name: "c", Type: "INT", Collation: "bad; coll"}); err == nil {
		t.Fatal("unsafe collation must be rejected")
	}
}

// The Oracle builder used to skip quoting whenever the table name already
// contained a double quote — a condition user input satisfies trivially, so
// `t"; DROP TABLE users; --` bypassed quoting entirely (F-11). The table
// parts are now ALWAYS quoted through qualifiedTable.
func TestOracleBuildColumnSQLAlwaysQuotesTable(t *testing.T) {
	p := &oracleProvider{}
	col := ColumnDef{Name: "c1", Type: "NUMBER(10)", Nullable: true}

	got := p.buildColumnSQL("ADD", "", "users", col)
	if want := `ALTER TABLE "users" ADD "c1" NUMBER(10) NULL`; got != want {
		t.Fatalf("plain table: got %q, want %q", got, want)
	}

	got = p.buildColumnSQL("ADD", "mydb", "users", col)
	if !strings.Contains(got, `"mydb"."users"`) {
		t.Fatalf("schema-qualified table not quoted: %q", got)
	}

	// Quote-shaped user input stays inside the quoted identifier.
	got = p.buildColumnSQL("ADD", "", `weird"name`, col)
	if !strings.Contains(got, `"weird""name"`) {
		t.Fatalf("embedded quote not doubled: %q", got)
	}

	// Injection-shaped input: the statement structure must not change.
	evil := `t"; DROP TABLE users; --`
	got = p.buildColumnSQL("ADD", "", evil, col)
	if strings.Count(got, "ALTER TABLE") != 1 || strings.Contains(got, "\n") {
		t.Fatalf("statement structure broken: %q", got)
	}
	if !strings.Contains(got, `"t""; DROP TABLE users; --"`) {
		t.Fatalf("evil table name not fully quoted: %q", got)
	}

	// The column name is quoted with the same escaping.
	got = p.buildColumnSQL("ADD", "", "users", ColumnDef{Name: `c"x`, Type: "INT"})
	if !strings.Contains(got, `"c""x" INT`) {
		t.Fatalf("column name not quoted: %q", got)
	}
}
