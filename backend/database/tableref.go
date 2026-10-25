package database

import "database/sql"

// TableRef identifies a table in a dialect-neutral way (DB-09, 2026-10-25
// regression audit). The old contract — every method taking
// (db, dbName, tableName) — forced one string to mean three different
// things: a MySQL database, a PostgreSQL "database" whose tables actually
// live in schemas resolved through search_path, an Oracle owner, an SQL
// Server database whose schema was never mentioned, and a SQLite file path.
// The result was the "clicked table A, read/modified table B" class of bugs
// whenever two schemas shared a table name (DB-06, DB-07).
//
// This is the migration's first slice: the metadata and DDL entry points the
// UI actually drives now come in schema-aware variants, bundled in the
// SchemaDDL capability interface. Dialects implement it only when a schema
// qualifier changes resolution:
//
//   - PostgreSQL / SQL Server — yes: schema.table, default "public"/"dbo".
//   - Oracle — no: the owner already travels in the dbName slot.
//   - MySQL — no: the database is selected per connection (USE).
//   - SQLite / rqlite — no: a file is a database, "main" is the only schema.
//
// The App layer asserts SchemaDDL when the frontend supplies a schema and
// falls back to the dbName-only Provider methods otherwise, so the six
// dialects migrate gradually instead of in one flag-day rewrite.
type TableRef struct {
	Database string // database (MySQL/MSSQL) or connection-level name; unused where the connection pins it
	Schema   string // schema/owner (PG/MSSQL/Oracle); empty = dialect default resolution
	Name     string
}

// SchemaDDL is the set of operations that must become schema-aware for the
// "same table name in two schemas" matrix to be safe. See TableRef for the
// migration strategy.
type SchemaDDL interface {
	GetTableSchemaRef(db *sql.DB, ref TableRef) (*SchemaResult, error)
	DefaultTableQueryRef(ref TableRef, limit, offset int) string

	CreateTableSchema(db *sql.DB, dbName, schema, tableName string) error
	DropTableSchema(db *sql.DB, dbName, schema, tableName string) error
	DropViewSchema(db *sql.DB, dbName, schema, viewName string) error
	TruncateTableSchema(db *sql.DB, dbName, schema, tableName string) error
	CopyTableSchema(db *sql.DB, dbName, schema, tableName, newTableName string) error

	AddColumnSchema(db *sql.DB, dbName, schema, tableName string, col ColumnDef) error
	ModifyColumnSchema(db *sql.DB, dbName, schema, tableName string, col ColumnDef) error
	DropColumnSchema(db *sql.DB, dbName, schema, tableName, colName string) error

	AddIndexSchema(db *sql.DB, dbName, schema, tableName string, idx IndexDef) error
	DropIndexSchema(db *sql.DB, dbName, schema, tableName, idxName string, isPrimary bool, autoIncCols []string) error

	// Row-level CRUD: the WHERE/SET clauses are schema-blind, so the TABLE
	// reference is the part that must be qualified (DB-06 edit half).
	InsertRowSchema(db *sql.DB, dbName, schema, tableName string, values map[string]any) error
	UpdateRowSchema(db *sql.DB, dbName, schema, tableName string, set, where map[string]any) (int64, error)
	DeleteRowSchema(db *sql.DB, dbName, schema, tableName string, where map[string]any) (int64, error)
	UpdateRowsSchema(db *sql.DB, dbName, schema, tableName string, edits []RowEdit) ([]int64, error)
}

// Compile-time proof that the two schema-sensitive dialects carry the full
// DB-09 surface.
var (
	_ SchemaDDL = (*postgresProvider)(nil)
	_ SchemaDDL = (*sqlserverProvider)(nil)
)
