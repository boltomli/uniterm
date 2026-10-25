package database

import (
	"os"
	"strings"
	"testing"
)

// DB-22 (2026-10-25 regression audit): column DEFAULTs round-trip through
// the UI as text. The old per-dialect rules were both wrong — MySQL/MSSQL
// quoted EVERYTHING (CURRENT_TIMESTAMP became the constant string
// 'CURRENT_TIMESTAMP'; empty-string defaults were dropped by MODIFY) while
// Oracle passed anything containing a parenthesis verbatim (arbitrary user
// text reached the DDL stream). RenderDefaultLiteral is the single
// whitelist all dialects now render through.
func TestRenderDefaultLiteralWhitelist(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Empty-string default stays expressible (was lost as "no default").
		{"", "''"},
		{"   ", "''"},
		// Bare whitelisted tokens pass through unquoted.
		{"NULL", "NULL"},
		{"null", "null"},
		{"0", "0"},
		{"-1.5", "-1.5"},
		{"CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP"},
		{"current_timestamp(3)", "current_timestamp(3)"},
		{"SYSDATE", "SYSDATE"},
		{"SYSTIMESTAMP", "SYSTIMESTAMP"},
		// Whitelisted no-argument functions.
		{"now()", "now()"},
		{"uuid()", "uuid()"},
		{"SYS_GUID()", "SYS_GUID()"},
		{"GETDATE()", "GETDATE()"},
		// Whitelisted argument-taking functions (read-path forms).
		{"nextval('items_id_seq'::regclass)", "nextval('items_id_seq'::regclass)"},
		{"TO_DATE('2020-01-01','YYYY-MM-DD')", "TO_DATE('2020-01-01','YYYY-MM-DD')"},
		// PostgreSQL cast form from the read path.
		{"'foo'::text", "'foo'::text"},
		{"'it''s'::character varying", "'it''s'::character varying"},
		// Parenthesised expression defaults (MySQL 8 / SQLite read-back).
		{"(uuid())", "(uuid())"},
		{"(1+1)", "(1+1)"},
		// Plain text becomes a quoted literal.
		{"foo", "'foo'"},
		{"it's", "'it''s'"},
		{"'foo'", "'foo'"},
		// Anything trying to break out is neutralised by quoting.
		{"x'); DROP TABLE t; --", "'x''); DROP TABLE t; --'"},
		{"(uuid()); DROP TABLE t", "'(uuid()); DROP TABLE t'"},
		// A balanced quoted literal is already safe SQL — kept as-is …
		{"' || (SELECT secret) || '", "' || (SELECT secret) || '"},
		// … while an unbalanced attempt is re-quoted with doubled quotes.
		{"'x' || (SELECT secret)", "'''x'' || (SELECT secret)'"},
		{"$(rm -rf /)", "'$(rm -rf /)'"},
		{"a`b;c", "'a`b;c'"},
	}
	for _, tc := range cases {
		if got := RenderDefaultLiteral(tc.in); got != tc.want {
			t.Errorf("RenderDefaultLiteral(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The whitelist must be the ONLY rendering path: no provider may quote
// defaults inline or interpolate the raw UI text anymore.
func TestNoInlineDefaultInterpolation(t *testing.T) {
	providers := []string{
		"provider_mysql.go",
		"provider_postgres.go",
		"provider_oracle.go",
		"provider_sqlserver.go",
		"provider_rqlite.go",
		"provider_sqlite.go",
	}
	for _, f := range providers {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		text := string(src)
		if strings.Contains(text, `fmt.Sprintf("DEFAULT '%s'"`) {
			t.Errorf("%s: inline DEFAULT quoting survived (DB-22)", f)
		}
		if strings.Contains(text, `"DEFAULT "+col.DefaultVal`) {
			t.Errorf("%s: raw DEFAULT interpolation survived (DB-22)", f)
		}
	}
}

// MSSQL's MODIFY used to ignore defaultVal entirely (DB-22): a comment-only
// edit left the old default in place and there was no way to set a new one.
func TestSQLServerModifyColumnAppliesDefault(t *testing.T) {
	src, err := os.ReadFile("provider_sqlserver.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(src)
	modify := text[strings.Index(text, "func (p *sqlserverProvider) ModifyColumn"):strings.Index(text, "// setDefaultValue")]
	if !strings.Contains(modify, "setDefaultValue") {
		t.Fatal("sqlserver ModifyColumn never touches the default constraint (DB-22)")
	}
	if !strings.Contains(text, "DEFAULT CONSTRAINT") && !strings.Contains(text, "sys.default_constraints") {
		t.Fatal("setDefaultValue does not drop the existing default constraint first")
	}
}

// The MySQL read path must come from information_schema so an empty-string
// default (COLUMN_DEFAULT = '') is distinguishable from "no default" — the
// old SHOW FULL COLUMNS scan collapsed both into "" and MODIFY COLUMN then
// dropped the default (DB-22).
func TestMySQLColumnInfoUsesInformationSchema(t *testing.T) {
	src, err := os.ReadFile("provider_mysql.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(src), "func mysqlColumnInfo(") {
		t.Fatal("mysqlColumnInfo missing — the column read path still cannot see defaults (DB-22)")
	}
	fn := string(src)[strings.Index(string(src), "func mysqlColumnInfo("):]
	fn = fn[:strings.Index(fn, "\nfunc ")]
	if !strings.Contains(fn, "information_schema.COLUMNS") {
		t.Fatal("mysqlColumnInfo must read COLUMN_DEFAULT from information_schema (DB-22)")
	}
	if !strings.Contains(fn, "sql.NullString") {
		t.Fatal("mysqlColumnInfo must scan COLUMN_DEFAULT as NULL-able (DB-22)")
	}
}
