package database

import (
	"os"
	"strings"
	"testing"
)

// DB-06/DB-07/DB-09 (2026-10-25 regression audit): with two schemas holding
// a same-named table, the metadata probes merged columns, the browse SELECT
// resolved through search_path to the wrong namesake, and DDL landed on
// whatever the default schema pointed at. TableRef + SchemaDDL pin the
// qualification; these tests assert the SQL shape without a live server.

func TestDefaultTableQueryRefQualifiesSchema(t *testing.T) {
	pg, err := NewProvider("postgres")
	if err != nil {
		t.Fatalf("postgres provider: %v", err)
	}
	sd, ok := pg.(SchemaDDL)
	if !ok {
		t.Fatal("postgres must implement SchemaDDL")
	}
	got := sd.DefaultTableQueryRef(TableRef{Schema: "audit", Name: "users"}, 100, 0)
	want := `SELECT * FROM "audit"."users" LIMIT 100`
	if got != want {
		t.Fatalf("pg qualified browse query = %q, want %q", got, want)
	}
	// Without a schema the historical unqualified form is preserved.
	got = sd.DefaultTableQueryRef(TableRef{Name: "users"}, 50, 100)
	want = `SELECT * FROM "users" LIMIT 50 OFFSET 100`
	if got != want {
		t.Fatalf("pg plain browse query = %q, want %q", got, want)
	}

	ss, err := NewProvider("sqlserver")
	if err != nil {
		t.Fatalf("sqlserver provider: %v", err)
	}
	ssd, ok := ss.(SchemaDDL)
	if !ok {
		t.Fatal("sqlserver must implement SchemaDDL")
	}
	got = ssd.DefaultTableQueryRef(TableRef{Schema: "sales", Name: "orders"}, 10, 20)
	want = "SELECT * FROM [sales].[orders] ORDER BY (SELECT NULL) OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"
	if got != want {
		t.Fatalf("mssql qualified browse query = %q, want %q", got, want)
	}
	got = ssd.DefaultTableQueryRef(TableRef{Name: "orders"}, 10, 0)
	want = "SELECT * FROM [orders] ORDER BY (SELECT NULL) OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY"
	if got != want {
		t.Fatalf("mssql plain browse query = %q, want %q", got, want)
	}
}

// TableInfo must carry the schema so the tree can show schema.table and the
// frontend can hand it back on every follow-up call.
func TestTableInfoCarriesSchema(t *testing.T) {
	src, err := os.ReadFile("schema.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)[strings.Index(string(src), "type TableInfo struct"):]
	body = body[:strings.Index(body, "}")]
	if !strings.Contains(body, `Schema`) || !strings.Contains(body, `json:"schema"`) {
		t.Fatal("TableInfo lacks a schema field with a json tag (DB-09)")
	}
}

// The metadata probes must be pinned to the requested schema — this is the
// exact "merged two schemas' columns" bug of DB-06/DB-07.
func TestSchemaPinnedMetadataProbes(t *testing.T) {
	pg, err := os.ReadFile("provider_postgres.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	pgSrc := string(pg)
	for _, needle := range []string{
		"c.table_schema = $2",  // columns
		"n.nspname = $2",       // PK + index probes
		"GetTableSchemaRef(db *sql.DB, ref TableRef)",
	} {
		if !strings.Contains(pgSrc, needle) {
			t.Errorf("postgres metadata probes lost the schema pin: %s", needle)
		}
	}

	ss, err := os.ReadFile("provider_sqlserver.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	ssSrc := string(ss)
	for _, needle := range []string{
		"AND TABLE_SCHEMA = @p2",                       // columns
		"tc.TABLE_SCHEMA = @p2 AND kcu.TABLE_SCHEMA",   // PK
		"SCHEMA_NAME(t.schema_id) = @p2",               // indexes + constraint probes
		"GetTableSchemaRef(db *sql.DB, ref TableRef)",  // ref entry point
	} {
		if !strings.Contains(ssSrc, needle) {
			t.Errorf("sqlserver metadata probes lost the schema pin: %s", needle)
		}
	}
}

// GetTables must publish the schema so the tree can disambiguate same-named
// tables and the doc model can carry the choice back.
func TestGetTablesPublishesSchema(t *testing.T) {
	for _, f := range []string{"provider_postgres.go", "provider_sqlserver.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		fn := string(src)
		fn = fn[strings.Index(fn, "func (p *"):]
		idx := strings.Index(fn, "GetTables(db *sql.DB")
		fn = fn[idx:strings.Index(fn[idx:], "\nfunc ")+idx]
		if !strings.Contains(fn, "Schema:") {
			t.Errorf("%s: GetTables does not fill TableInfo.Schema (DB-06/DB-07)", f)
		}
	}
}
