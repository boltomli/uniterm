package database

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// sortedKeys returns the sorted keys of a map; deterministic ordering makes
// generated SQL stable and easier to test.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// execer is the common interface for sql.DB, sql.Conn, and sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// DumpOptions controls what a DumpTable export includes.
type DumpOptions struct {
	Structure bool // include CREATE TABLE/VIEW statement
	Data      bool // include INSERT statements for rows
}

// errDumpUnsupported is returned by providers that do not implement export yet.
type errDumpUnsupported struct{ dbType string }

func (e *errDumpUnsupported) Error() string { return "export not supported for " + e.dbType }

// Provider encapsulates all database-type-specific behavior.
type Provider interface {
	// DSN generates a driver-specific connection string from connection fields.
	DSN(host string, port int, user, password, dbName string, extraParams map[string]string) string

	// DriverName returns the Go SQL driver name to use for sql.Open.
	DriverName() string

	// DBType returns the registered database type key (e.g. "mysql",
	// "postgres"). Behaviors keyed on the dialect — such as the script
	// splitter's `#` comment handling — use this.
	DBType() string

	// Quote returns an identifier quoted for this database.
	Quote(name string) string

	// Schema discovery
	GetDatabases(db *sql.DB) ([]string, error)
	GetTables(db *sql.DB, dbName string) ([]TableInfo, error)
	GetTableSchema(db *sql.DB, dbName, tableName string) (*SchemaResult, error)

	// DefaultTableQuery returns the default SELECT used when browsing a table.
	// offset is 0-based; limit is the page size. Browsing and pagination share
	// this one entry point (DB-26 removed the unused PagedTableQuery twin).
	DefaultTableQuery(dbName, tableName string, limit, offset int) string

	// DumpTable produces SQL text for a table: its CREATE statement and/or
	// INSERT statements for all rows. Used for the "export as .sql" feature.
	DumpTable(db *sql.DB, dbName, tableName string, opts DumpOptions) (string, error)

	// Row-level CRUD helpers for the result-grid inline actions.
	InsertRow(db *sql.DB, dbName, tableName string, values map[string]any) error
	// UpdateRow and DeleteRow return the number of rows the statement
	// affected. Callers must surface a zero count: it means the WHERE clause
	// matched nothing (the row was modified elsewhere, or the snapshot the
	// WHERE was built from is stale) and reporting "saved" would silently
	// lose the edit (DB-01). -1 means the driver cannot report a count —
	// treat it as "unknown", never as zero.
	UpdateRow(db *sql.DB, dbName, tableName string, set, where map[string]any) (int64, error)
	DeleteRow(db *sql.DB, dbName, tableName string, where map[string]any) (int64, error)

	// DDL: Database
	CreateDatabase(db *sql.DB, dbName string) error
	DropDatabase(db *sql.DB, dbName string) error

	// DDL: Table
	CreateTable(db *sql.DB, dbName, tableName string) error
	DropTable(db *sql.DB, dbName, tableName string) error
	DropView(db *sql.DB, dbName, viewName string) error
	TruncateTable(db *sql.DB, dbName, tableName string) error

	// CopyTable creates a new table as a copy of an existing one (structure
	// plus data where the dialect supports it). Only plain tables are offered
	// this in the UI; views are not passed here.
	CopyTable(db *sql.DB, dbName, tableName, newTableName string) error

	// DDL: Column
	AddColumn(db *sql.DB, dbName, tableName string, col ColumnDef) error
	ModifyColumn(db *sql.DB, dbName, tableName string, col ColumnDef) error
	DropColumn(db *sql.DB, dbName, tableName string, colName string) error

	// DDL: Index
	AddIndex(db *sql.DB, dbName, tableName string, idx IndexDef) error
	DropIndex(db *sql.DB, dbName, tableName string, idxName string, isPrimary bool, autoIncCols []string) error

	// Capabilities
	GetCapabilities() DBCapabilities

	// PrepareExec executes any per-database setup before running user SQL.
	PrepareExec(db execer, dbName string) error
}

// execPrepared executes a parameterized statement on a dedicated connection
// after running the provider's per-database setup (PrepareExec).
func execPrepared(p Provider, db *sql.DB, dbName, sql string, args []any) error {
	_, err := execPreparedAffected(p, db, dbName, sql, args)
	return err
}

// execPreparedAffected is execPrepared plus the driver's affected-row count.
// A count the driver refuses to provide becomes -1 (unknown), never 0: the
// statement itself already committed, so it must not be reported as
// "matched nothing".
func execPreparedAffected(p Provider, db *sql.DB, dbName, sql string, args []any) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	if err := p.PrepareExec(conn, dbName); err != nil {
		return 0, err
	}

	res, err := conn.ExecContext(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return -1, nil
	}
	return n, nil
}

var providers = map[string]Provider{}

// Register registers a Provider for a database type. Call from init().
func Register(dbType string, p Provider) {
	providers[dbType] = p
}

// NewProvider returns the Provider for the given database type, or an error
// if the type is not supported.
func NewProvider(dbType string) (Provider, error) {
	p, ok := providers[dbType]
	if !ok {
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}
	return p, nil
}
