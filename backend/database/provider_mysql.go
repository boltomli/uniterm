package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

type mysqlProvider struct{}

func init() {
	Register("mysql", &mysqlProvider{})
}

func (p *mysqlProvider) DSN(host string, port int, user, password, dbName string, extraParams map[string]string) string {
	if port <= 0 {
		port = 3306
	}
	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", host, port)
	cfg.DBName = dbName
	cfg.Params = map[string]string{
		"charset":   "utf8mb4",
		"parseTime": "true",
		"loc":       "Local",
		// "timeout" only bounds the TCP dial — 10s is fine there.
		// readTimeout/writeTimeout are per-SOCKET-READ/WRITE deadlines in
		// go-sql-driver; they must NEVER be shorter than the statement
		// budget (queryTimeout=30s, ddlTimeout=60s, dumpTimeout=10m), or a
		// query that legitimately takes longer dies with "invalid connection"
		// while its context still had time left — and raising queryTimeout
		// cannot help (DB-10). 0 defers every deadline to the per-call
		// context: the driver aborts a blocked read by closing the
		// connection once that context expires.
		"timeout":      "10s",
		"readTimeout":  "0",
		"writeTimeout": "0",
		// clientFoundRows makes RowsAffected count MATCHED rows instead of
		// CHANGED rows. DBUpdateRow/DBDeleteRow surface the count so a 0
		// means "your WHERE matched nothing" (DB-01); without this flag an
		// UPDATE that writes the value a row already has reports 0 as well
		// and the frontend would raise a false "data changed elsewhere".
		"clientFoundRows": "true",
	}
	for k, v := range extraParams {
		cfg.Params[k] = v
	}
	return cfg.FormatDSN()
}

func (p *mysqlProvider) DriverName() string {
	return "mysql"
}

func (p *mysqlProvider) DBType() string {
	return "mysql"
}

// Quote returns the identifier wrapped in backticks with embedded backticks
// doubled — MySQL's escaping rule, the same one SafeMyIdent applies on its
// success path. It never returns an empty string for a non-empty name; the
// previous implementation discarded the SafeMyIdent error and silently
// produced broken SQL.
func (p *mysqlProvider) Quote(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (p *mysqlProvider) PrepareExec(db execer, dbName string) error {
	if dbName == "" {
		return nil
	}
	q, err := SafeMyIdent(dbName)
	if err != nil {
		return err
	}
	// Bounded: this runs on every statement path; a wedged server must not
	// pin the goroutine (DB-11).
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	_, err = db.ExecContext(ctx, "USE "+q)
	return err
}

func (p *mysqlProvider) DefaultTableQuery(dbName, tableName string, limit, offset int) string {
	if offset > 0 {
		return fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d", p.Quote(tableName), limit, offset)
	}
	return fmt.Sprintf("SELECT * FROM %s LIMIT %d", p.Quote(tableName), limit)
}


func (p *mysqlProvider) InsertRow(db *sql.DB, dbName, tableName string, values map[string]any) error {
	cols := sortedKeys(values)
	quotedCols := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	args := make([]any, 0, len(cols))
	for i, c := range cols {
		quotedCols[i] = p.Quote(c)
		placeholders[i] = "?"
		args = append(args, values[c])
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		p.Quote(tableName), strings.Join(quotedCols, ", "), strings.Join(placeholders, ", "))
	return execPrepared(p, db, dbName, sql, args)
}

func (p *mysqlProvider) UpdateRow(db *sql.DB, dbName, tableName string, set, where map[string]any) (int64, error) {
	args := make([]any, 0, len(set)+len(where))
	phIdx := 1

	setParts := make([]string, 0, len(set))
	for _, c := range sortedKeys(set) {
		if set[c] == nil {
			setParts = append(setParts, fmt.Sprintf("%s = NULL", p.Quote(c)))
		} else {
			setParts = append(setParts, fmt.Sprintf("%s = ?", p.Quote(c)))
			args = append(args, set[c])
			phIdx++
		}
	}

	whereParts := make([]string, 0, len(where))
	for _, c := range sortedKeys(where) {
		if where[c] == nil {
			whereParts = append(whereParts, fmt.Sprintf("%s IS NULL", p.Quote(c)))
		} else {
			whereParts = append(whereParts, fmt.Sprintf("%s = ?", p.Quote(c)))
			args = append(args, where[c])
			phIdx++
		}
	}

	sql := fmt.Sprintf("UPDATE %s SET %s", p.Quote(tableName), strings.Join(setParts, ", "))
	if len(whereParts) > 0 {
		sql += " WHERE " + strings.Join(whereParts, " AND ")
	}
	return execPreparedAffected(p, db, dbName, sql, args)
}

func (p *mysqlProvider) DeleteRow(db *sql.DB, dbName, tableName string, where map[string]any) (int64, error) {
	args := make([]any, 0, len(where))
	phIdx := 1
	whereParts := make([]string, 0, len(where))
	for _, c := range sortedKeys(where) {
		if where[c] == nil {
			whereParts = append(whereParts, fmt.Sprintf("%s IS NULL", p.Quote(c)))
		} else {
			whereParts = append(whereParts, fmt.Sprintf("%s = ?", p.Quote(c)))
			args = append(args, where[c])
			phIdx++
		}
	}

	sql := fmt.Sprintf("DELETE FROM %s", p.Quote(tableName))
	if len(whereParts) > 0 {
		sql += " WHERE " + strings.Join(whereParts, " AND ")
	}
	return execPreparedAffected(p, db, dbName, sql, args)
}

func (p *mysqlProvider) GetCapabilities() DBCapabilities {
	return DBCapabilities{
		"columnTypes": mysqlTypes,
		"intTypes":    mysqlIntTypes,
	}
}

// ── Schema discovery ──

func (p *mysqlProvider) GetDatabases(db *sql.DB) ([]string, error) {
	results, err := queryStrings(db, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(results))
	for _, row := range results {
		names = append(names, row["Database"])
	}
	return names, nil
}

func (p *mysqlProvider) GetTables(db *sql.DB, dbName string) ([]TableInfo, error) {
	results, err := queryStrings(db,
		`SELECT TABLE_NAME, TABLE_TYPE, IFNULL(TABLE_COMMENT, '') AS TABLE_COMMENT
		 FROM information_schema.TABLES
		 WHERE TABLE_SCHEMA = ?
		 ORDER BY TABLE_NAME`, dbName)
	if err != nil {
		return nil, fmt.Errorf("get tables: %w", err)
	}
	infos := make([]TableInfo, 0, len(results))
	for _, row := range results {
		tp := "table"
		if row["TABLE_TYPE"] == "VIEW" {
			tp = "view"
		}
		infos = append(infos, TableInfo{
			Name:    row["TABLE_NAME"],
			Type:    tp,
			Comment: row["TABLE_COMMENT"],
		})
	}
	return infos, nil
}

func (p *mysqlProvider) GetTableSchema(db *sql.DB, dbName, tableName string) (*SchemaResult, error) {
	qDb, err := SafeMyIdent(dbName)
	if err != nil {
		return nil, err
	}
	qTbl, err := SafeMyIdent(tableName)
	if err != nil {
		return nil, err
	}
	columns, err := mysqlColumnInfo(db, dbName, tableName)
	if err != nil {
		return nil, fmt.Errorf("get columns: %w", err)
	}

	idxRows, err := queryStrings(db, "SHOW INDEX FROM "+qDb+"."+qTbl)
	if err != nil {
		return nil, fmt.Errorf("get indexes: %w", err)
	}

	idxMap := make(map[string]*IndexInfo)
	var idxOrder []string
	for _, row := range idxRows {
		name := row["Key_name"]
		if _, ok := idxMap[name]; !ok {
			idxMap[name] = &IndexInfo{
				Name:      name,
				Columns:   []string{},
				Unique:    row["Non_unique"] == "0",
				IsPrimary: name == "PRIMARY",
			}
			idxOrder = append(idxOrder, name)
		}
		idxMap[name].Columns = append(idxMap[name].Columns, row["Column_name"])
	}

	indexes := make([]IndexInfo, 0, len(idxOrder))
	for _, name := range idxOrder {
		indexes = append(indexes, *idxMap[name])
	}

	return &SchemaResult{Columns: columns, Indexes: indexes}, nil
}

// mysqlColumnInfo reads the column list from information_schema instead of
// SHOW FULL COLUMNS so DEFAULT values survive the round-trip (DB-22):
// COLUMN_DEFAULT is NULL both for "no default" and for an explicit
// DEFAULT NULL on a nullable column (MySQL cannot separate the two — both
// become defaultType "null"), while DEFAULT '' comes back as an empty
// STRING and is now preserved as defaultType "value" instead of being
// mistaken for "no default" and silently dropped by MODIFY COLUMN.
// Expression defaults (MySQL 8) arrive parenthesised — e.g. (uuid()) — and
// pass the DEFAULT whitelist as-is (see RenderDefaultLiteral).
func mysqlColumnInfo(db *sql.DB, dbName, tableName string) ([]ColumnInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA,
		       COLUMN_COMMENT, COLUMN_KEY, COLLATION_NAME
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, dbName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make([]ColumnInfo, 0, 16)
	for rows.Next() {
		var name, ctype, nullable, extra, comment, colKey string
		var def, collation sql.NullString
		if err := rows.Scan(&name, &ctype, &nullable, &def, &extra, &comment, &colKey, &collation); err != nil {
			return nil, err
		}
		extraL := strings.ToLower(extra)

		defaultType := "none"
		defVal := ""
		switch {
		case strings.Contains(extraL, "auto_increment"):
			defaultType = "auto"
		case def.Valid:
			// Present default — including the empty string and parenthesised
			// expression defaults.
			defaultType = "value"
			defVal = def.String
		default:
			// COLUMN_DEFAULT NULL: no default, or explicit DEFAULT NULL.
			if strings.EqualFold(nullable, "YES") {
				defaultType = "null"
				defVal = "NULL"
			}
		}

		coll := ""
		if collation.Valid {
			coll = collation.String
		}
		columns = append(columns, ColumnInfo{
			Name:        name,
			Type:        ctype,
			Nullable:    strings.EqualFold(nullable, "YES"),
			DefaultVal:  defVal,
			DefaultType: defaultType,
			IsPrimary:   strings.EqualFold(colKey, "PRI"),
			Comment:     comment,
			Collation:   coll,
			OnUpdate:    strings.Contains(extraL, "on update"),
		})
	}
	return columns, rows.Err()
}

// ── DDL: Database ──

func (p *mysqlProvider) CreateDatabase(db *sql.DB, dbName string) error {
	q, err := SafeMyIdent(dbName)
	if err != nil {
		return err
	}
	_, err = db.Exec("CREATE DATABASE " + q)
	return err
}

func (p *mysqlProvider) DropDatabase(db *sql.DB, dbName string) error {
	q, err := SafeMyIdent(dbName)
	if err != nil {
		return err
	}
	_, err = db.Exec("DROP DATABASE " + q)
	return err
}

// mysqlUseDB acquires a dedicated connection and issues USE against it.
// All DDL helpers below return this conn so the subsequent DDL runs on the
// same physical connection (the database/sql pool might otherwise hand out
// different connections for USE and DDL, racing the session state).
// The caller owns the deadline: ctx is usually ddlCtx() (DB-11).
func (p *mysqlProvider) mysqlUseDB(ctx context.Context, db *sql.DB, dbName string) (*sql.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if dbName != "" {
		q, err := SafeMyIdent(dbName)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if _, err := conn.ExecContext(ctx, "USE "+q); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

// ── DDL: Table ──

func (p *mysqlProvider) CreateTable(db *sql.DB, dbName, tableName string) error {
	ctx, cancel := ddlCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()
	qTbl, err := SafeMyIdent(tableName)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "CREATE TABLE "+qTbl+" (id INT AUTO_INCREMENT PRIMARY KEY)")
	return err
}

func (p *mysqlProvider) DropTable(db *sql.DB, dbName, tableName string) error {
	ctx, cancel := ddlCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()
	qTbl, err := SafeMyIdent(tableName)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "DROP TABLE "+qTbl)
	return err
}

func (p *mysqlProvider) DropView(db *sql.DB, dbName, viewName string) error {
	ctx, cancel := ddlCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()
	qView, err := SafeMyIdent(viewName)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "DROP VIEW "+qView)
	return err
}

func (p *mysqlProvider) TruncateTable(db *sql.DB, dbName, tableName string) error {
	ctx, cancel := ddlCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()
	qTbl, err := SafeMyIdent(tableName)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "TRUNCATE TABLE "+qTbl)
	return err
}

// CopyTable clones structure via CREATE TABLE ... LIKE and bulk-copies rows
// with INSERT ... SELECT, both native server-side statements.
func (p *mysqlProvider) CopyTable(db *sql.DB, dbName, tableName, newTableName string) error {
	// Bulk INSERT … SELECT can run long; the export budget applies.
	ctx, cancel := dumpCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()
	qSrc, err := SafeMyIdent(tableName)
	if err != nil {
		return err
	}
	qDst, err := SafeMyIdent(newTableName)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE "+qDst+" LIKE "+qSrc); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO "+qDst+" SELECT * FROM "+qSrc); err != nil {
		// Keep the empty clone so the user can inspect it; report the copy error.
		return err
	}
	return nil
}

// ── DDL: Column ──

func (p *mysqlProvider) AddColumn(db *sql.DB, dbName, tableName string, col ColumnDef) error {
	// F-11: type/collation are free text in the UI — validate before
	// interpolation (collation lands in "COLLATE <name>").
	col, err := safeColumnTypeFor(col)
	if err != nil {
		return err
	}
	ctx, cancel := ddlCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()
	sql := p.buildColumnSQL("ADD COLUMN", tableName, col)
	_, err = conn.ExecContext(ctx, sql)
	return err
}

func (p *mysqlProvider) ModifyColumn(db *sql.DB, dbName, tableName string, col ColumnDef) error {
	// F-11: type/collation are free text in the UI — validate before
	// interpolation (collation lands in "COLLATE <name>").
	col, err := safeColumnTypeFor(col)
	if err != nil {
		return err
	}
	ctx, cancel := ddlCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()
	sql := p.buildColumnSQL("MODIFY COLUMN", tableName, col)
	_, err = conn.ExecContext(ctx, sql)
	return err
}

func (p *mysqlProvider) DropColumn(db *sql.DB, dbName, tableName, colName string) error {
	ctx, cancel := ddlCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", p.Quote(tableName), p.Quote(colName)))
	return err
}

// ── DDL: Index ──

func (p *mysqlProvider) AddIndex(db *sql.DB, dbName, tableName string, idx IndexDef) error {
	ctx, cancel := ddlCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()

	q := p.Quote
	var sql string
	if idx.IsPrimary {
		cols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			cols[i] = q(c)
		}
		sql = fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s)", q(tableName), strings.Join(cols, ", "))
	} else {
		uniqueStr := ""
		if idx.Unique {
			uniqueStr = "UNIQUE "
		}
		cols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			cols[i] = q(c)
		}
		sql = fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", uniqueStr, q(idx.Name), q(tableName), strings.Join(cols, ", "))
	}
	_, err = conn.ExecContext(ctx, sql)
	return err
}

func (p *mysqlProvider) DropIndex(db *sql.DB, dbName, tableName, idxName string, isPrimary bool, autoIncCols []string) error {
	ctx, cancel := ddlCtx()
	defer cancel()
	conn, err := p.mysqlUseDB(ctx, db, dbName)
	if err != nil {
		return err
	}
	defer conn.Close()
	q := p.Quote
	if isPrimary {
		sql, err := p.buildDropPK(ctx, conn, tableName, autoIncCols)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, sql)
		return err
	}
	_, err = conn.ExecContext(ctx, fmt.Sprintf("DROP INDEX %s ON %s", q(idxName), q(tableName)))
	return err
}

// ── SQL builders ──

func (p *mysqlProvider) buildColumnSQL(action, tableName string, col ColumnDef) string {
	q := p.Quote
	var parts []string
	parts = append(parts, q(col.Name), col.Type)

	if col.Nullable {
		parts = append(parts, "NULL")
	} else {
		parts = append(parts, "NOT NULL")
	}

	switch col.DefaultType {
	case "null":
		parts = append(parts, "DEFAULT NULL")
	case "value":
		// DB-22: whitelist rendering — CURRENT_TIMESTAMP / expression
		// defaults survive unquoted; plain text becomes a quoted literal.
		parts = append(parts, "DEFAULT "+RenderDefaultLiteral(col.DefaultVal))
	case "auto":
		parts = append(parts, "AUTO_INCREMENT")
	}

	if col.OnUpdate {
		parts = append(parts, "ON UPDATE CURRENT_TIMESTAMP")
	}

	if col.Comment != "" {
		parts = append(parts, fmt.Sprintf("COMMENT '%s'", strings.ReplaceAll(col.Comment, "'", "''")))
	}

	if col.Collation != "" {
		parts = append(parts, "COLLATE "+col.Collation)
	}

	return fmt.Sprintf("ALTER TABLE %s %s %s", q(tableName), action, strings.Join(parts, " "))
}

// buildDropPK handles AUTO_INCREMENT columns that must be modified before dropping PK.
func (p *mysqlProvider) buildDropPK(ctx context.Context, conn *sql.Conn, tableName string, autoIncCols []string) (string, error) {
	q := p.Quote
	if len(autoIncCols) > 0 {
		rows, err := conn.QueryContext(ctx, fmt.Sprintf("SHOW FULL COLUMNS FROM %s", q(tableName)))
		if err != nil {
			return "", fmt.Errorf("get columns for PK drop: %w", err)
		}
		defer rows.Close()

		cols, err := rows.Columns()
		if err != nil {
			return "", fmt.Errorf("get columns for PK drop: %w", err)
		}

		colTypes := make(map[string]string)
		for rows.Next() {
			values := make([]any, len(cols))
			valuePtrs := make([]any, len(cols))
			for i := range values {
				valuePtrs[i] = &values[i]
			}
			if err := rows.Scan(valuePtrs...); err != nil {
				return "", fmt.Errorf("scan columns for PK drop: %w", err)
			}
			row := make(map[string]string, len(cols))
			for i, col := range cols {
				row[col] = scanToString(values[i])
			}
			colTypes[row["Field"]] = row["Type"]
		}
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("iterate columns for PK drop: %w", err)
		}

		var modParts []string
		for _, c := range autoIncCols {
			ct, ok := colTypes[c]
			if !ok {
				ct = "INT"
			}
			modParts = append(modParts, fmt.Sprintf("MODIFY COLUMN %s %s NOT NULL", q(c), ct))
		}
		return fmt.Sprintf("ALTER TABLE %s %s, DROP PRIMARY KEY", q(tableName), strings.Join(modParts, ", ")), nil
	}
	return fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY", q(tableName)), nil
}

var mysqlTypes = []string{
	"TINYINT", "TINYINT(4)",
	"SMALLINT", "SMALLINT(6)",
	"MEDIUMINT", "MEDIUMINT(9)",
	"INT", "INT(11)",
	"INTEGER", "INTEGER(11)",
	"BIGINT", "BIGINT(20)",
	"FLOAT", "FLOAT(10,2)",
	"DOUBLE", "DOUBLE(10,2)",
	"DECIMAL", "DECIMAL(10,2)",
	"CHAR", "CHAR(1)",
	"VARCHAR", "VARCHAR(255)",
	"TINYTEXT", "TEXT", "MEDIUMTEXT", "LONGTEXT",
	"BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB",
	"DATE", "DATETIME", "TIMESTAMP", "TIME", "YEAR",
	"ENUM", "SET", "JSON", "BOOLEAN", "BOOL",
}

var mysqlIntTypes = []string{
	"INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "MEDIUMINT",
}

// binaryColumnTypes are MySQL types whose values should be emitted as hex
// _binary literals rather than quoted strings.
var mysqlBinaryTypes = map[string]bool{
	"BLOB": true, "TINYBLOB": true, "MEDIUMBLOB": true, "LONGBLOB": true,
	"BINARY": true, "VARBINARY": true,
}

func (p *mysqlProvider) DumpTable(db *sql.DB, dbName, tableName string, opts DumpOptions) (string, error) {
	// Whole-table export: bounded, but by the export budget, not the query
	// one (DB-11). DB-12 will add streaming/row caps on top.
	ctx, cancel := dumpCtx()
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	if err := p.PrepareExec(conn, dbName); err != nil {
		return "", err
	}

	var b strings.Builder
	quotedTable := p.Quote(tableName)

	if opts.Structure {
		createStmt, kind, derr := showCreateStatement(conn, dbName, tableName, quotedTable)
		if derr != nil {
			return "", derr
		}
		b.WriteString("--\n-- Structure for ")
		b.WriteString(tableName)
		b.WriteString("\n--\nDROP " + kind + " IF EXISTS " + quotedTable + ";\n")
		b.WriteString(createStmt)
		b.WriteString(";\n\n")
	}

	if opts.Data {
		cols, colTypes, derr := tableColumns(conn, dbName, tableName, quotedTable)
		if derr != nil {
			return "", derr
		}
		if len(cols) > 0 {
			b.WriteString("--\n-- Data for ")
			b.WriteString(tableName)
			b.WriteString("\n--\n")
			colList := make([]string, len(cols))
			for i, c := range cols {
				colList[i] = p.Quote(c)
			}
			prefix := "INSERT INTO " + quotedTable + " (" + strings.Join(colList, ", ") + ") VALUES "

			rows, err := conn.QueryContext(ctx, "SELECT "+strings.Join(colList, ", ")+" FROM "+quotedTable)
			if err != nil {
				return "", err
			}
			defer rows.Close()

			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					return "", err
				}
				b.WriteString(prefix)
				b.WriteByte('(')
				for i, v := range vals {
					if i > 0 {
						b.WriteString(", ")
					}
					b.WriteString(dumpMysqlValue(v, colTypes[i]))
				}
				b.WriteString(");\n")
			}
			if err := rows.Err(); err != nil {
				return "", err
			}
			b.WriteByte('\n')
		}
	}

	return b.String(), nil
}

// tableKind returns "TABLE" or "VIEW" for the given name by querying
// information_schema; defaults to TABLE on error. Scoped to the schema so a
// name shared across databases resolves to the right object.
func tableKind(conn *sql.Conn, dbName, tableName string) string {
	var t string
	ctx, cancel := context.WithTimeout(context.Background(), dumpTimeout)
	defer cancel()
	err := conn.QueryRowContext(ctx,
		"SELECT TABLE_TYPE FROM information_schema.tables WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? LIMIT 1", dbName, tableName).Scan(&t)
	if err == nil && strings.HasPrefix(t, "VIEW") {
		return "VIEW"
	}
	return "TABLE"
}

// showCreateStatement returns the CREATE TABLE/VIEW statement (without trailing ;)
// and the object kind ("TABLE" or "VIEW").
func showCreateStatement(conn *sql.Conn, dbName, tableName, quotedTable string) (string, string, error) {
	kind := tableKind(conn, dbName, tableName)
	ctx, cancel := context.WithTimeout(context.Background(), dumpTimeout)
	defer cancel()
	row := conn.QueryRowContext(ctx, "SHOW CREATE "+kind+" "+quotedTable)
	if kind == "VIEW" {
		var name, createSQL, charset, collation string
		if err := row.Scan(&name, &createSQL, &charset, &collation); err != nil {
			return "", kind, err
		}
		return createSQL, kind, nil
	}
	var name, createSQL string
	if err := row.Scan(&name, &createSQL); err != nil {
		return "", kind, err
	}
	return createSQL, kind, nil
}

// tableColumns returns column names and their uppercased type tokens.
// TABLE_SCHEMA is filtered because information_schema is global — without it,
// a name shared across schemas mixes in foreign columns and the SELECT below
// fails with "Unknown column".
func tableColumns(conn *sql.Conn, dbName, tableName, quotedTable string) ([]string, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dumpTimeout)
	defer cancel()
	rows, err := conn.QueryContext(ctx,
		"SELECT COLUMN_NAME, DATA_TYPE FROM information_schema.columns WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION", dbName, tableName)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var names, types []string
	for rows.Next() {
		var n, ty string
		if err := rows.Scan(&n, &ty); err != nil {
			return nil, nil, err
		}
		names = append(names, n)
		types = append(types, strings.ToUpper(ty))
	}
	return names, types, rows.Err()
}

// dumpMysqlValue renders a scanned cell value into a SQL literal. Binary-typed
// columns are emitted as _binary 0x.. literals; everything else goes through
// quoteSQLValue.
func dumpMysqlValue(v any, typeToken string) string {
	if v == nil {
		return "NULL"
	}
	if mysqlBinaryTypes[typeToken] {
		if b, ok := v.([]byte); ok {
			return quoteSQLValue(b)
		}
	}
	return quoteSQLValue(v)
}
