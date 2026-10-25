// Statement splitting for the SQL editor (DB-15, database regression audit
// 2026-10-25). firstStatement() used to be a bare text.split(';'), so any
// statement whose first literal contained a semicolon was truncated into a
// syntax error while the multi-statement check disagreed and said "one
// statement". Both decisions now share splitScriptLike.

import { describe, expect, it } from 'vitest'
import { firstStatement, hasMultipleStatements, isQuerySql, splitScriptLike } from './sqlStatements'

describe('semicolons inside literals (DB-15 table)', () => {
  it('keeps a semicolon inside a single-quoted string', () => {
    expect(firstStatement("SELECT ';' AS x")).toBe("SELECT ';' AS x")
    expect(hasMultipleStatements("SELECT ';' AS x")).toBe(false)
  })

  it('keeps a semicolon inside a longer literal', () => {
    expect(firstStatement("SELECT 'a;b' AS x, 2")).toBe("SELECT 'a;b' AS x, 2")
    expect(hasMultipleStatements("SELECT 'a;b' AS x, 2")).toBe(false)
  })

  it('treats a trailing line comment as part of the same statement', () => {
    expect(firstStatement('SELECT 1; -- tail')).toBe('SELECT 1')
    expect(hasMultipleStatements('SELECT 1; -- tail')).toBe(false)
  })

  it('drops a comment-only fragment', () => {
    expect(splitScriptLike('SELECT 1; -- only a comment')).toEqual(['SELECT 1'])
    expect(splitScriptLike('/* nothing */')).toEqual([])
  })
})

describe('other quoting forms', () => {
  it('handles double quotes and backticks', () => {
    expect(firstStatement('SELECT "a;b" FROM t')).toBe('SELECT "a;b" FROM t')
    expect(firstStatement('SELECT `a;b` FROM t; SELECT 2')).toBe('SELECT `a;b` FROM t')
    expect(hasMultipleStatements('SELECT `a;b` FROM t; SELECT 2')).toBe(true)
  })

  it('honours backslash escapes', () => {
    expect(firstStatement("SELECT 'a\\';b' AS x")).toBe("SELECT 'a\\';b' AS x")
  })

  it('handles doubled single quotes (SQL string escaping)', () => {
    expect(firstStatement("SELECT 'it''s; here' AS x")).toBe("SELECT 'it''s; here' AS x")
  })

  it('handles block comments', () => {
    expect(firstStatement('SELECT /* ; in a comment */ 1; SELECT 2')).toBe('SELECT /* ; in a comment */ 1')
    expect(hasMultipleStatements('SELECT /* ; */ 1; SELECT 2')).toBe(true)
  })

  it('handles Postgres dollar-quoted literals', () => {
    expect(firstStatement('SELECT $tag$ a; b $tag$; SELECT 2')).toBe('SELECT $tag$ a; b $tag$')
    expect(firstStatement('SELECT $$ a; b $$')).toBe('SELECT $$ a; b $$')
    expect(hasMultipleStatements('SELECT $body$ ; $body$')).toBe(false)
  })

  it('does not treat $1 placeholders as dollar-quote openings', () => {
    expect(firstStatement('SELECT * FROM t WHERE id = $1; SELECT 2')).toBe('SELECT * FROM t WHERE id = $1')
    expect(hasMultipleStatements('SELECT * FROM t WHERE id = $1; SELECT 2')).toBe(true)
  })

  it('only starts a line comment on -- followed by whitespace', () => {
    // Arithmetic `a--1` must not swallow the ';' that follows (a naive
    // `--`-anywhere rule would merge the two statements into one).
    expect(hasMultipleStatements('SELECT a--1; SELECT 2')).toBe(true)
    expect(splitScriptLike('SELECT a--1; SELECT 2')).toHaveLength(2)
    // With the required whitespace, the comment runs to end of line…
    expect(splitScriptLike('SELECT 1 -- c\n; SELECT 2')).toHaveLength(2)
  })
})

describe('statement routing', () => {
  it('strips the trailing semicolon of a single statement', () => {
    expect(firstStatement('SELECT 1;')).toBe('SELECT 1')
  })

  it('recognises query-shaped statements', () => {
    expect(isQuerySql('SELECT 1')).toBe(true)
    expect(isQuerySql('  with x as (select 1) select * from x')).toBe(true)
    expect(isQuerySql('PRAGMA table_info(t)')).toBe(true)
    expect(isQuerySql('UPDATE t SET a = 1')).toBe(false)
    expect(isQuerySql('INSERT INTO t VALUES (1)')).toBe(false)
  })

  it('returns the trimmed text when there is nothing to split', () => {
    expect(firstStatement('')).toBe('')
    expect(firstStatement('   ')).toBe('')
  })
})
