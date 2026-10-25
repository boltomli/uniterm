// Statement-splitting helpers for the SQL editor (DB-15).
//
// The editor used to determine "the one statement to run" with a bare
// `text.split(';')`, which truncates any statement whose first literal
// contains a semicolon (`SELECT ';' AS x` was sent as `SELECT '` — a syntax
// error), while the multi-statement check used a quote-aware splitter. Both
// decisions now come from the same quote/comment-aware splitter below.

/**
 * Split a script into statements. The scan respects single/double/backtick
 * quoting (with backslash escapes), `--` line comments (only when followed by
 * whitespace, so `a - -b`/`a--b` arithmetic survives), `/* … *\/` block
 * comments, and Postgres dollar-quoting ($tag$ … $tag$), so a `;` inside any
 * of them never separates statements. Fragments that contain nothing but
 * comments are dropped: `SELECT 1; -- tail` is ONE statement.
 */
export function splitScriptLike(text: string): string[] {
  const parts: string[] = []
  let cur = ''
  let mode: 'code' | 'single' | 'double' | 'backtick' | 'dollar' = 'code'
  let dollarTag = ''
  let lineComment = false
  let blockComment = false

  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    const pair = text.slice(i, i + 2)

    if (lineComment) {
      cur += c
      if (c === '\n') lineComment = false
      continue
    }
    if (blockComment) {
      cur += c
      if (pair === '*/') {
        cur += text[i + 1] ?? ''
        i++
        blockComment = false
      }
      continue
    }
    if (mode !== 'code') {
      if (mode === 'dollar') {
        // The closing tag starts with '$', so a prefix match at the CURRENT
        // position ends the literal (the opening tag already advanced i past
        // its own last character).
        if (text.startsWith(dollarTag, i)) {
          cur += dollarTag
          i += dollarTag.length - 1
          mode = 'code'
        } else {
          cur += c
        }
        continue
      }
      cur += c
      const closer = mode === 'single' ? "'" : mode === 'double' ? '"' : '`'
      if (c === closer && (i === 0 || text[i - 1] !== '\\')) mode = 'code'
      continue
    }

    // code mode
    if (pair === '--' && (i + 2 >= text.length || /\s/.test(text[i + 2]))) {
      lineComment = true
      cur += pair
      i++
      continue
    }
    if (pair === '/*') {
      blockComment = true
      cur += pair
      i++
      continue
    }
    if (c === "'") {
      mode = 'single'
      cur += c
      continue
    }
    if (c === '"') {
      mode = 'double'
      cur += c
      continue
    }
    if (c === '`') {
      mode = 'backtick'
      cur += c
      continue
    }
    // $tag$…$tag$ dollar-quoting (PostgreSQL). `$1` placeholders don't match:
    // the tag may only contain letters and underscores.
    const dollar = /^\$[A-Za-z_]*\$/.exec(text.slice(i))
    if (dollar) {
      mode = 'dollar'
      dollarTag = dollar[0]
      cur += dollarTag
      i += dollarTag.length - 1
      continue
    }
    if (c === ';') {
      if (cur.trim()) parts.push(cur)
      cur = ''
      continue
    }
    cur += c
  }
  if (cur.trim()) parts.push(cur)
  return parts.filter(hasSqlContent)
}

/** True when a split fragment contains anything other than whitespace and
 *  comments — i.e. it is an executable statement, not a trailing `-- …`. */
function hasSqlContent(stmt: string): boolean {
  return stmt
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/--[^\n]*/g, ' ')
    .trim().length > 0
}

/** The single statement to execute from an editor buffer (or selection).
 *  Uses the same splitter as hasMultipleStatements so the two decisions can
 *  never disagree (DB-15). Trailing semicolons are dropped. */
export function firstStatement(text: string): string {
  const trimmed = text.trim()
  if (!trimmed) return ''
  return splitScriptLike(trimmed)[0] || trimmed
}

export function hasMultipleStatements(text: string): boolean {
  return splitScriptLike(text).length > 1
}

/** Heuristic for routing a single statement: query-shaped prefixes go through
 *  QueryContext (result grid), everything else through ExecContext. */
export function isQuerySql(text: string): boolean {
  const stmt = firstStatement(text)
  return /^\s*(WITH|SELECT|SHOW|DESCRIBE|DESC|EXPLAIN|PRAGMA)\b/i.test(stmt)
}
