package database

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseParams parses "key1=val1&key2=val2" into a map.
func parseParams(s string) map[string]string {
	m := make(map[string]string)
	for s != "" {
		var pair string
		if before, after, found := strings.Cut(s, "&"); found {
			pair, s = before, after
		} else {
			pair, s = s, ""
		}
		k, v, _ := strings.Cut(pair, "=")
		if k != "" {
			m[k] = v
		}
	}
	return m
}

// BuildDSN builds a DSN string from connection config fields.
func BuildDSN(dbType, host, user, password, dbName, extraParams string, port int) (string, error) {
	p, err := NewProvider(dbType)
	if err != nil {
		return "", err
	}
	return p.DSN(host, port, user, password, dbName, parseParams(extraParams)), nil
}

// NewDB opens a database/sql connection for the given database type and DSN.
func NewDB(dbType, dsn string) (*sql.DB, error) {
	p, err := NewProvider(dbType)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(p.DriverName(), dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbType, err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	return db, nil
}

// queryer is satisfied by *sql.DB and *sql.Conn — metadata probes run on
// either depending on the dialect's connection policy (DB-24 pins SQL
// Server's database switch to a dedicated connection).
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// queryStrings executes a SQL query and returns all rows as []map[string]string.
// F-12.4: runs under queryTimeout — schema/metadata probes against a slow or
// hung server must not pin a goroutine (and its connection) forever.
func queryStrings(db queryer, query string, args ...any) ([]map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	return queryStringsCtx(ctx, db, query, args...)
}

// queryStringsCtx is queryStrings with a caller-owned context (used when the
// query runs on a connection the caller already set up).
func queryStringsCtx(ctx context.Context, db queryer, query string, args ...any) ([]map[string]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	guids := guidColumns(rows)
	var result []map[string]string
	for rows.Next() {
		values := make([]any, len(cols))
		valuePtrs := make([]any, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}
		row := make(map[string]string, len(cols))
		for i, col := range cols {
			row[col] = scanToStringCol(values[i], isGuidCol(guids, i))
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// queryAny is like queryStrings but preserves nil values for proper NULL handling.
// Returns rows, column names in SQL order, and error. Bounded by queryTimeout
// for the same reason as queryStrings. (F-12.4)
func queryAny(db *sql.DB, query string, args ...any) ([]map[string]any, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}

	guids := guidColumns(rows)
	var result []map[string]any
	for rows.Next() {
		values := make([]any, len(cols))
		valuePtrs := make([]any, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, nil, err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = scanToAnyCol(values[i], isGuidCol(guids, i))
		}
		result = append(result, row)
	}
	return result, cols, rows.Err()
}

func scanToAny(v any) any {
	return scanToAnyCol(v, false)
}

// scanToAnyCol is scanToAny with the column's Guid flag (see guidColumns).
func scanToAnyCol(v any, guid bool) any {
	if v == nil {
		return nil
	}
	switch s := v.(type) {
	case []byte:
		return formatBytes(s, guid)
	default:
		return v
	}
}

func scanToString(v any) string {
	return scanToStringCol(v, false)
}

// scanToStringCol is scanToString with the column's Guid flag (see guidColumns).
func scanToStringCol(v any, guid bool) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case []byte:
		return formatBytes(x, guid)
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// toAnySlice converts []string to []any for IN clauses etc.
func toAnySlice(items []string) []any {
	res := make([]any, len(items))
	for i, item := range items {
		res[i] = item
	}
	return res
}

// buildPlaceholders returns "?,?,?" for the given count.
func buildPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
