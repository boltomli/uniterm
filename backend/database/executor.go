package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// queryTimeout caps user-initiated ad-hoc queries / DDL on a single
// connection. Without this the UI cannot cancel a stuck query — the user
// has to kill the whole app. 30s is generous enough for normal browsing
// and short enough that an accidental cross-join SELECT will not freeze the
// result grid for half a minute.
var queryTimeout = 30 * time.Second

// scriptTimeout caps a whole .sql import run. A single 30s cap per statement
// still applies inside ExecuteScript; this bounds the total for very large
// dumps so the goroutine cannot wedge forever.
var scriptTimeout = 10 * time.Minute

// maxQueryRows caps rows returned by a single ExecuteQuery. The UI grid
// cannot render unbounded results (a cross join can produce millions of
// rows and freeze the app), so the scan stops at the cap and the result is
// flagged Truncated. A var so tests can shrink it.
var maxQueryRows = 10000

type QueryResultColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type QueryResult struct {
	// Columns keeps the result-set column metadata in ordinal order.
	// Duplicate names are preserved as-is (SELECT a.id, b.id yields two
	// entries named "id"); consumers must address cells by index.
	Columns   []QueryResultColumn `json:"columns"`
	Rows      [][]any             `json:"rows"`
	Truncated bool                `json:"truncated"`
}

type ExecResult struct {
	Affected     int64 `json:"affected"`
	LastInsertID int64 `json:"lastInsertId"`
}

func ExecuteQuery(p Provider, db *sql.DB, dbName, sqlStr string) (*QueryResult, error) {
	return ExecuteQueryCtx(context.Background(), p, db, dbName, sqlStr)
}

// ExecuteQueryCtx is ExecuteQuery with a caller-owned parent context. The
// bound App method passes the request context here, so the frontend's cancel
// button aborts the in-flight statement instead of only hiding the result
// (DB-16); the per-query budget still applies on top of it.
func ExecuteQueryCtx(parent context.Context, p Provider, db *sql.DB, dbName, sqlStr string) (*QueryResult, error) {
	ctx, cancel := context.WithTimeout(parent, queryTimeout)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := p.PrepareExec(conn, dbName); err != nil {
		return nil, err
	}

	rows, err := conn.QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	guids := guidColumns(rows)
	var result [][]any
	truncated := false
	for rows.Next() {
		if len(result) >= maxQueryRows {
			// More rows than the cap: stop scanning and flag the result so
			// the UI can tell the user the data is incomplete.
			truncated = true
			break
		}
		values := make([]any, len(cols))
		valuePtrs := make([]any, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}
		row := make([]any, len(cols))
		for i := range cols {
			row[i] = scanToAnyCol(values[i], isGuidCol(guids, i))
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	columns := make([]QueryResultColumn, 0, len(cols))
	for _, c := range cols {
		columns = append(columns, QueryResultColumn{Name: c, Type: ""})
	}

	if len(result) == 0 {
		return &QueryResult{Columns: columns, Rows: [][]any{}, Truncated: truncated}, nil
	}
	return &QueryResult{Columns: columns, Rows: result, Truncated: truncated}, nil
}

func ExecuteStatement(p Provider, db *sql.DB, dbName, sqlStr string) (*ExecResult, error) {
	return ExecuteStatementCtx(context.Background(), p, db, dbName, sqlStr)
}

// ExecuteStatementCtx is ExecuteStatement with a caller-owned parent context
// (see ExecuteQueryCtx, DB-16).
func ExecuteStatementCtx(parent context.Context, p Provider, db *sql.DB, dbName, sqlStr string) (*ExecResult, error) {
	ctx, cancel := context.WithTimeout(parent, queryTimeout)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := p.PrepareExec(conn, dbName); err != nil {
		return nil, err
	}

	result, err := conn.ExecContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}

	affected, _ := result.RowsAffected()
	lastID, _ := result.LastInsertId()

	return &ExecResult{Affected: affected, LastInsertID: lastID}, nil
}

// QueryResultToJSON serializes a QueryResult to JSON bytes.
func QueryResultToJSON(qr *QueryResult) ([]byte, error) {
	return json.Marshal(qr)
}

// ScriptResult is the outcome of running a multi-statement SQL script.
type ScriptResult struct {
	Executed      int    `json:"executed"`       // statements that ran successfully
	FailedLine    int    `json:"failedLine"`     // 1-based line of the failing statement; 0 if all ok
	FailedSQL     string `json:"failedSql"`      // the failing statement (truncated for display)
	Error         string `json:"error"`          // failure message
	AffectedTotal int64  `json:"affectedTotal"`  // sum of rows affected across statements
}

// ExecuteScript splits a SQL script and runs each statement sequentially on a
// single connection. It stops at the first error and reports the failing
// statement's line. Routines defined via DELIMITER are supported because the
// splitter already merged their bodies into a single statement.
func ExecuteScript(p Provider, db *sql.DB, dbName, script string) (*ScriptResult, error) {
	return ExecuteScriptCtx(context.Background(), p, db, dbName, script)
}

// ExecuteScriptCtx is ExecuteScript with a caller-owned parent context (see
// ExecuteQueryCtx, DB-16). Cancelling it stops the run between statements
// and aborts the statement in flight.
func ExecuteScriptCtx(parent context.Context, p Provider, db *sql.DB, dbName, script string) (*ScriptResult, error) {
	stmts := SplitScript(script, p.DBType())
	res := &ScriptResult{}

	ctx, cancel := context.WithTimeout(parent, scriptTimeout)
	defer cancel()

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := p.PrepareExec(conn, dbName); err != nil {
		return nil, err
	}

	for _, st := range stmts {
		// per-statement deadline so one runaway statement cannot consume the
		// whole script budget
		sctx, scancel := context.WithTimeout(ctx, queryTimeout)
		r, err := conn.ExecContext(sctx, st.SQL)
		scancel()
		if err != nil {
			res.FailedLine = st.Line
			res.FailedSQL = truncate(st.SQL, 500)
			res.Error = err.Error()
			return res, nil
		}
		if r != nil {
			if n, e := r.RowsAffected(); e == nil {
				res.AffectedTotal += n
			}
		}
		res.Executed++
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// quotedString wraps a string in single quotes with embedded quotes doubled.
// Shared by the per-dialect dump value escapers (all dialects agree on this
// rule).
func quotedString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// hexLower returns the lowercase hex encoding of b.
func hexLower(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hex[c>>4]
		out[i*2+1] = hex[c&0x0f]
	}
	return string(out)
}

// quoteStringValue escapes and quotes a value for an INSERT statement. NULL is
// emitted as the bare keyword. Binary payloads become _binary 0x… .
func quoteSQLValue(v any) string {
	if v == nil {
		return "NULL"
	}
	switch val := v.(type) {
	case []byte:
		var b strings.Builder
		b.WriteString("_binary 0x")
		for _, c := range val {
			b.WriteString(hexByte(c))
		}
		return b.String()
	case string:
		return "'" + strings.ReplaceAll(val, "'", "''") + "'"
	case bool:
		if val {
			return "1"
		}
		return "0"
	default:
		return scanToString(v)
	}
}

func hexByte(c byte) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[c>>4], hex[c&0x0f]})
}
