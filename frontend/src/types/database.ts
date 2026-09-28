export interface TableInfo {
  name: string
  type?: string  // "table" or "view"
  comment?: string
}

export interface ColumnInfo {
  name: string
  type: string
  nullable: boolean
  defaultVal: string
  defaultType: string  // "none" | "null" | "value" | "auto"
  isPrimary: boolean
  comment: string
  collation: string
  onUpdate: boolean
}

export interface IndexInfo {
  name: string
  columns: string[]
  unique: boolean
  isPrimary: boolean
}

export interface SchemaResult {
  columns: ColumnInfo[]
  indexes: IndexInfo[]
}

export interface QueryResultColumn {
  name: string
  type: string
}

export interface QueryResult {
  columns: QueryResultColumn[]
  rows: Record<string, any>[]
}

export interface ExecResult {
  affected: number
  lastInsertId: number
}

// Outcome of a multi-statement SQL script run (Go: backend/database/executor.go
// ScriptResult; fields mirror its JSON tags).
export interface ScriptResult {
  executed: number       // statements that ran successfully
  failedLine: number     // 1-based line of the failing statement; 0 if all ok
  failedSql: string      // the failing statement (truncated for display)
  error: string          // failure message
  affectedTotal: number  // sum of rows affected across statements
}

export interface HistoryEntry {
  id: string
  sql: string
  executedAt: string
  durationMs: number
  error?: string
  rowCount?: number
}

export interface ColumnDef {
  name: string
  type: string
  nullable: boolean
  defaultVal: string
  defaultType: string  // "none" | "null" | "value" | "auto"
  comment: string
  collation: string
  onUpdate: boolean
}

export interface IndexDef {
  name: string
  columns: string[]
  unique: boolean
  isPrimary: boolean
}
