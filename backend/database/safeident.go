package database

import (
	"errors"
	"regexp"
	"fmt"
	"strings"
)

// SafeMyIdent returns the MySQL identifier with backticks escaped (doubled)
// and rejects names containing NUL bytes, dot-segments, or excessive length.
// Addresses AUDIT-database-01 / AUDIT-database-02 (SQL injection via dbName
// and Quote on MySQL).
func SafeMyIdent(name string) (string, error) {
	if err := identValidate(name); err != nil {
		return "", err
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
}

// SafePgIdent returns the Postgres / rqlite identifier with double-quotes
// escaped (doubled). Addresses AUDIT-database-03 and AUDIT-database-05.
func SafePgIdent(name string) (string, error) {
	if err := identValidate(name); err != nil {
		return "", err
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
}

// SafeDefaultLiteral validates a literal value suitable for embedding in a
// `DEFAULT` clause. Only number / string / NULL / CURRENT_* / boolean
// literals are accepted; everything else is rejected to prevent injection
// into `ALTER TABLE ... SET DEFAULT %s` (AUDIT-database-04).
func SafeDefaultLiteral(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("empty DEFAULT literal")
	}
	upper := strings.ToUpper(v)
	switch {
	case upper == "NULL",
		upper == "TRUE",
		upper == "FALSE",
		upper == "CURRENT_TIMESTAMP",
		upper == "CURRENT_DATE",
		upper == "CURRENT_TIME",
		strings.HasPrefix(upper, "CURRENT_TIMESTAMP("),
		strings.HasPrefix(upper, "LOCALTIMESTAMP"),
		strings.HasPrefix(upper, "LOCALTIME"):
		return v, nil
	}
	// Numeric literal: digits + optional decimal + optional sign.
	isNum := true
	for i, r := range v {
		if r == '+' || r == '-' || r == '.' {
			continue
		}
		if r < '0' || r > '9' {
			isNum = false
			break
		}
		_ = i
	}
	if isNum {
		return v, nil
	}
	// String literal: 'foo' (single quotes doubled).
	if v[0] == '\'' && v[len(v)-1] == '\'' {
		// no embedded unescaped quotes
		if !strings.Contains(v[1:len(v)-1], "'") {
			return v, nil
		}
	}
	return "", fmt.Errorf("unsafe DEFAULT literal: %s", v)
}

// SafeColumnType validates a column TYPE string before it is interpolated
// into a DDL statement (F-11). Types are free text in the UI, so only a
// conservative character set survives: letters, digits, underscore, spaces,
// parentheses, comma and single quotes — enough for "VARCHAR2(100 CHAR)",
// "NUMBER(10,2)", "TIMESTAMP(6) WITH TIME ZONE" … Statement separators,
// comments, backticks, double quotes and backslashes are all rejected.
func SafeColumnType(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", errors.New("empty column type")
	}
	if len(t) > 120 {
		return "", errors.New("column type too long")
	}
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == ' ' || r == '(' || r == ')' || r == ',' || r == '\'':
		default:
			return "", fmt.Errorf("unsafe character %q in column type", r)
		}
	}
	return t, nil
}

// SafeColumnCollation validates a collation/charset name (F-11): identifiers
// only — MySQL interpolates it as "COLLATE <name>".
func SafeColumnCollation(c string) (string, error) {
	c = strings.TrimSpace(c)
	if c == "" {
		return "", nil
	}
	if len(c) > 64 {
		return "", errors.New("collation name too long")
	}
	for _, r := range c {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return "", fmt.Errorf("unsafe character %q in collation", r)
		}
	}
	return c, nil
}

// safeColumnTypeFor rewrites col with a validated type/collation so every
// provider's DDL builders can interpolate the fields without re-checking
// (F-11: shared entry guard for AddColumn/ModifyColumn).
func safeColumnTypeFor(col ColumnDef) (ColumnDef, error) {
	t, err := SafeColumnType(col.Type)
	if err != nil {
		return col, err
	}
	c, err := SafeColumnCollation(col.Collation)
	if err != nil {
		return col, err
	}
	col.Type, col.Collation = t, c
	return col, nil
}

// identValidate rejects names that are empty, contain path-traversal
// segments, NUL bytes, or are absurdly long (MySQL limit is 64, Postgres
// 63; we pick 128 as a generous cap covering schema-qualified forms).
func identValidate(name string) error {
	if name == "" {
		return errors.New("empty identifier")
	}
	if len(name) > 128 {
		return errors.New("identifier too long")
	}
	if strings.ContainsRune(name, 0) {
		return errors.New("identifier contains NUL")
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return errors.New("identifier contains path separator")
	}
	return nil
}
// RenderDefaultLiteral renders a UI-entered column DEFAULT for interpolation
// into a DDL statement (DB-22, 2026-10-25 regression audit). The old
// per-dialect rules either quoted EVERYTHING (MySQL turning CURRENT_TIMESTAMP
// into the constant string 'CURRENT_TIMESTAMP', expression defaults into
// literals, and losing empty-string defaults) or passed anything containing a
// parenthesis straight through (Oracle), letting arbitrary user text reach
// the DDL stream.
//
// The contract:
//   - empty input             → '' (an empty-string default stays expressible
//     instead of being dropped as "no default")
//   - whitelisted bare token  → unquoted: numbers, NULL/TRUE/FALSE, the
//     CURRENT_TIMESTAMP / LOCALTIME family with optional precision
//   - whitelisted no-arg fn   → unquoted: now(), SYSDATE, SYSTIMESTAMP,
//     SYS_GUID(), uuid(), GETDATE(), GETUTCDATE() … (read paths return these
//     bare)
//   - parenthesised           → MySQL 8 / SQLite report expression defaults
//     wrapped in parens; they pass through when the inside is a conservative
//     identifier/argument list (letters, digits, underscore, parens, comma,
//     dot, space, colon, single quotes — no semicolons, backticks, double
//     quotes, dashes or backslashes)
//   - well-formed 'literal'   → kept as-is
//   - everything else         → treated as a string literal and re-quoted
//     with doubled single quotes
//
// It never returns an error: anything not whitelisted becomes a quoted
// string, which is always safe to interpolate.
func RenderDefaultLiteral(v string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return "''"
	}
	// Whitelisted bare tokens (numbers, NULL/boolean, current-time family).
	if _, err := SafeDefaultLiteral(trimmed); err == nil && trimmed[0] != '\'' {
		return trimmed
	}
	// Oracle's bare date tokens (identifiers, not function calls).
	switch strings.ToUpper(trimmed) {
	case "SYSDATE", "SYSTIMESTAMP":
		return trimmed
	}
	// Zero-argument function calls from the dialect whitelist.
	if m := defaultFuncRe.FindStringSubmatch(trimmed); m != nil && whitelistedDefaultFuncs[strings.ToLower(m[1])] {
		return trimmed
	}
	// Argument-taking functions from the whitelist (nextval('s'::regclass),
	// TO_DATE(...) …): the arguments must pass the conservative inner
	// charset, so no separator or comment marker can reach the DDL.
	if m := defaultFuncCallRe.FindStringSubmatch(trimmed); m != nil &&
		whitelistedDefaultFuncsWithArgs[strings.ToLower(m[1])] && isSafeDefaultExpr(m[2]) {
		return trimmed
	}
	// PostgreSQL cast form from the read path: 'literal'::type.
	if len(trimmed) <= 200 && pgDefaultCastRe.MatchString(trimmed) {
		return trimmed
	}
	// Parenthesised expression defaults (MySQL 8 / SQLite read-back form).
	if len(trimmed) > 1 && trimmed[0] == '(' && trimmed[len(trimmed)-1] == ')' && isSafeDefaultExpr(trimmed) {
		return trimmed
	}
	// Well-formed single-quoted literal from a read path.
	if len(trimmed) > 1 && trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'' {
		if _, err := SafeDefaultLiteral(trimmed); err == nil {
			return trimmed
		}
	}
	return "'" + strings.ReplaceAll(trimmed, "'", "''") + "'"
}

var defaultFuncRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\(\)$`)

var defaultFuncCallRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)(\(.*\))$`)

// pgDefaultCastRe matches the PostgreSQL read-path form of a string default:
// 'literal'::type. Quotes inside are balanced by construction (each segment
// is either a non-quote char or a doubled quote), so nothing can break out
// of the literal; the type part is identifier-ish only.
var pgDefaultCastRe = regexp.MustCompile(`^'(?:[^']|'')*'::[A-Za-z0-9_\s"().,]+$`)

var whitelistedDefaultFuncs = map[string]bool{
	"now":               true,
	"uuid":              true,
	"sysdate":           true,
	"systimestamp":      true,
	"sys_guid":          true,
	"getdate":           true,
	"getutcdate":        true,
	"current_date":      true,
	"current_timestamp": true,
}

// whitelistedDefaultFuncsWithArgs admits the small set of argument-taking
// defaults read paths actually return (Postgres sequences, Oracle date
// conversions). Everything else with arguments is treated as a string.
var whitelistedDefaultFuncsWithArgs = map[string]bool{
	"nextval":     true,
	"to_char":     true,
	"to_date":     true,
	"to_timestamp": true,
	"to_number":   true,
}

// isSafeDefaultExpr validates a parenthesised expression default: only
// identifier/argument-list characters survive, so no statement separator,
// comment marker, quote of the other kind, or backslash can slip into the
// surrounding DDL.
func isSafeDefaultExpr(s string) bool {
	if len(s) > 200 {
		return false
	}
	// A `--` would comment out the rest of the DDL line (single dashes are
	// fine: TO_DATE('2020-01-01', …)).
	if strings.Contains(s, "--") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '(' || r == ')' || r == ',' || r == '.' || r == ' ' || r == '\'' || r == '+' || r == ':' || r == '>' || r == '-':
		default:
			return false
		}
	}
	return true
}
