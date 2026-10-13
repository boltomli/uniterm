package database

import "strings"

// ScriptStatement is one executable statement extracted from a SQL script,
// together with the 1-based line number where it starts (for error reporting).
type ScriptStatement struct {
	SQL  string
	Line int
}

// SplitScript splits a SQL script into individual statements for the given
// dialect (a registered dbType such as "mysql", "postgres", "sqlserver"). It
// understands string literals ('…', "…", `…`), line comments (--, and # for
// MySQL-compatible dialects), block comments (/* … */), and the MySQL
// DELIMITER command used to define routines. The DELIMITER line itself is
// consumed and not emitted as a statement.
//
// `#` starts a line comment only in MySQL-family dialects: SQL Server uses it
// in temp-table names (`SELECT * INTO #t FROM x`) and PostgreSQL as a bitwise
// operator (`f # 4`), so treating it as a comment there would truncate
// statements.
func SplitScript(script string, dialect string) []ScriptStatement {
	// MySQL-family dialects (MySQL/MariaDB/TiDB register as "mysql") treat
	// `#` as a line comment, honour backslash escapes inside strings, and
	// keep /*! … */ executable comments. Only PostgreSQL dollar-quotes.
	mysqlFamily := mysqlFamilyDialect(dialect)
	dollarQuote := dialect == "postgres"
	var stmts []ScriptStatement

	var b strings.Builder
	stmtStartLine := 1
	line := 1
	i := 0
	n := len(script)
	delimiter := ";"
	flushed := true // true means no statement content since last flush

	emit := func() {
		s := strings.TrimSpace(b.String())
		if s != "" {
			stmts = append(stmts, ScriptStatement{SQL: s, Line: stmtStartLine})
		}
		b.Reset()
		flushed = true
	}

	for i < n {
		// DELIMITER command at any line start (including the very first
		// line, which the old after-'\n' check missed), but only when not
		// mid-statement. matchDelimiterCmd skips leading whitespace.
		if flushed && (i == 0 || script[i-1] == '\n') {
			if d, consumed, ok := matchDelimiterCmd(script, i); ok {
				delimiter = d
				i += consumed
				// skip rest of that line
				for i < n && script[i] != '\n' {
					i++
				}
				// leave line counting to next loop; b is empty
				continue
			}
		}

		c := script[i]

		// newline tracking for line numbers
		if c == '\n' {
			line++
			b.WriteByte(c)
			i++
			continue
		}

		// whitespace
		if c == ' ' || c == '\t' || c == '\r' {
			b.WriteByte(c)
			i++
			continue
		}

		// block comment /* ... */ : skip entirely — except MySQL executable
		// comments (/*! ... */), whose inner SQL the server runs (mysqldump
		// relies on this for SET FOREIGN_KEY_CHECKS=0 etc.). Keep those
		// verbatim as statement content.
		if c == '/' && i+1 < n && script[i+1] == '*' {
			if mysqlFamily && i+2 < n && script[i+2] == '!' {
				start := i
				startLine := line
				j := i + 2
				for j < n {
					if script[j] == '*' && j+1 < n && script[j+1] == '/' {
						j += 2
						break
					}
					if script[j] == '\n' {
						line++
					}
					j++
				}
				if flushed {
					stmtStartLine = startLine
					flushed = false
				}
				b.WriteString(script[start:j])
				i = j
				continue
			}
			i += 2
			for i < n {
				if script[i] == '*' && i+1 < n && script[i+1] == '/' {
					i += 2
					break
				}
				if script[i] == '\n' {
					line++
				}
				i++
			}
			continue
		}

		// line comment -- (all dialects) or # (MySQL-family only) : skip to
		// end of line (comments are not statement content)
		if c == '-' && i+1 < n && script[i+1] == '-' {
			for i < n && script[i] != '\n' {
				i++
			}
			continue
		}
		if mysqlFamily && c == '#' {
			for i < n && script[i] != '\n' {
				i++
			}
			continue
		}

		// PostgreSQL dollar-quoted bodies: $$ … $$ / $tag$ … $tag$ are one
		// literal unit (the body of CREATE FUNCTION / DO blocks); their
		// inner ';' must not split the statement.
		if dollarQuote && c == '$' {
			if tag, ok := matchDollarQuote(script, i); ok {
				closer := "$" + tag + "$"
				if flushed {
					stmtStartLine = line
					flushed = false
				}
				end := strings.Index(script[i+len(closer):], closer)
				stop := n
				if end >= 0 {
					stop = i + len(closer) + end + len(closer)
				}
				for k := i; k < stop; k++ {
					if script[k] == '\n' {
						line++
					}
				}
				b.WriteString(script[i:stop])
				i = stop
				continue
			}
		}

		// string literals
		if c == '\'' || c == '"' || c == '`' {
			if flushed {
				stmtStartLine = line
			}
			quote := c
			b.WriteByte(c)
			i++
			for i < n {
				ch := script[i]
				// Backslash escapes are a MySQL-family extension; treating
				// them as escapes in PG/MSSQL/Oracle/SQLite turned 'a\'
				// into an unterminated string that swallowed the rest of
				// the script.
				if mysqlFamily && ch == '\\' && quote != '`' {
					b.WriteByte(ch)
					if i+1 < n {
						b.WriteByte(script[i+1])
						if script[i+1] == '\n' {
							line++
						}
						i += 2
						continue
					}
					i++
					continue
				}
				if ch == quote {
					// doubled quote = escaped quote
					if i+1 < n && script[i+1] == quote {
						b.WriteByte(ch)
						b.WriteByte(ch)
						i += 2
						continue
					}
					b.WriteByte(ch)
					i++
					break
				}
				if ch == '\n' {
					line++
				}
				b.WriteByte(ch)
				i++
			}
			flushed = false
			continue
		}

		// delimiter match (custom or ';')
		if strings.HasPrefix(script[i:], delimiter) {
			emit()
			i += len(delimiter)
			continue
		}

		if flushed {
			stmtStartLine = line
			flushed = false
		}
		b.WriteByte(c)
		i++
	}

	emit()
	return stmts
}

// mysqlFamilyDialect reports whether dialect is MySQL-family (MySQL,
// MariaDB and TiDB all register as "mysql"). Those dialects treat `#` as a
// line comment, honour backslash escapes in string literals, and execute
// /*! … */ comments.
func mysqlFamilyDialect(dialect string) bool {
	return dialect == "mysql"
}

// matchDollarQuote checks whether script[pos:] starts with a PostgreSQL
// dollar-quote opener ($$ or $tag$) and returns the tag (empty for $$).
// Tags must look like identifiers and may not start with a digit, so
// placeholders like $1 are not mistaken for an opener.
func matchDollarQuote(script string, pos int) (string, bool) {
	if pos+1 >= len(script) || script[pos] != '$' {
		return "", false
	}
	j := pos + 1
	start := j
	if j < len(script) && (script[j] >= '0' && script[j] <= '9') {
		return "", false
	}
	for j < len(script) && (script[j] == '_' ||
		(script[j] >= 'a' && script[j] <= 'z') ||
		(script[j] >= 'A' && script[j] <= 'Z') ||
		(script[j] >= '0' && script[j] <= '9')) {
		j++
	}
	if j >= len(script) || script[j] != '$' {
		return "", false
	}
	return script[start:j], true
}

// matchDelimiterCmd checks whether the text starting at pos begins (after
// optional whitespace) with "DELIMITER <new-delimiter>", and returns the
// new delimiter + number of bytes consumed (including the keyword). Returns
// ok=false if it does not match.
func matchDelimiterCmd(script string, pos int) (string, int, bool) {
	i := pos
	for i < len(script) && (script[i] == ' ' || script[i] == '\t') {
		i++
	}
	kw := "DELIMITER"
	if i+len(kw) > len(script) || !strings.EqualFold(script[i:i+len(kw)], kw) {
		return "", 0, false
	}
	// must be followed by whitespace
	j := i + len(kw)
	if j >= len(script) || (script[j] != ' ' && script[j] != '\t') {
		return "", 0, false
	}
	for j < len(script) && (script[j] == ' ' || script[j] == '\t') {
		j++
	}
	start := j
	for j < len(script) && script[j] != ' ' && script[j] != '\t' && script[j] != '\n' && script[j] != '\r' {
		j++
	}
	if j == start {
		return "", 0, false
	}
	return script[start:j], j - pos, true
}
