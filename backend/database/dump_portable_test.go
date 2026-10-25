package database

import (
	"os"
	"strings"
	"testing"
)

// TestDumpStructureIsPortable pins DB-23 (2026-10-25 regression audit):
// exported scripts must replay in an empty schema on any supported server
// version. The dialect-specific hazards:
//   - PostgreSQL/SQL Server used a fixed DROP TABLE + synthesized CREATE
//     TABLE even for VIEWs — the DROP failed and the view became a table.
//   - Oracle emitted `DROP … IF EXISTS`, which only exists on 23ai+.
//   - Dialects left DROP/INSERT targets unqualified, so replay depended on
//     the importing session's search_path / default schema.
func TestDumpStructureIsPortable(t *testing.T) {
	src := map[string]string{}
	for _, f := range []string{
		"provider_postgres.go", "provider_sqlserver.go",
		"provider_oracle.go", "provider_mysql.go",
	} {
		text, err := readFileString(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src[f] = text
	}

	pg := src["provider_postgres.go"]
	if !strings.Contains(pg, "pgIsView(") || !strings.Contains(pg, "DROP VIEW IF EXISTS ") {
		t.Error("postgres dump must emit DROP VIEW for views (DB-23)")
	}
	if !strings.Contains(pg, "pg_get_viewdef") {
		t.Error("postgres dump must export a view's definition, not a table shell (DB-23)")
	}
	if !strings.Contains(pg, "quotedTable := p.pgQual(schema, tableName)") {
		t.Error("postgres dump must schema-qualify its object references (DB-23)")
	}

	ss := src["provider_sqlserver.go"]
	if !strings.Contains(ss, "sqlserverObjectSchema(") || !strings.Contains(ss, "DROP VIEW IF EXISTS ") {
		t.Error("sqlserver dump must emit DROP VIEW for views (DB-23)")
	}
	if !strings.Contains(ss, "sys.sql_modules") {
		t.Error("sqlserver dump must export a view's module body (DB-23)")
	}
	if !strings.Contains(ss, "quotedTable := p.qualifiedTableSchema(schema, tableName)") {
		t.Error("sqlserver dump must schema-qualify its object references (DB-23)")
	}

	oc := src["provider_oracle.go"]
	if strings.Contains(oc, "IF EXISTS \"") || strings.Contains(oc, "IF EXISTS "+`"`) {
		t.Error("oracle dump still emits DROP … IF EXISTS (illegal before 23ai, DB-23)")
	}
	if !strings.Contains(oc, "EXECUTE IMMEDIATE 'DROP ") || !strings.Contains(oc, "WHEN OTHERS THEN NULL") {
		t.Error("oracle dump must wrap the drop in PL/SQL for old-server replay (DB-23)")
	}
	if !strings.Contains(oc, "quotedTable := p.qualifiedTable(owner, tableName)") {
		t.Error("oracle dump must owner-qualify its object references (DB-23)")
	}

	my := src["provider_mysql.go"]
	if !strings.Contains(my, "qDb + \".\" + quotedTable") {
		t.Error("mysql dump must library-qualify its object references (DB-23)")
	}
}

func readFileString(f string) (string, error) {
	b, err := os.ReadFile(f)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
