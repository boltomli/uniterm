package database

import (
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Postgres GetTableSchema results are memoised — the schema panel walks
// tables on every refresh and each miss costs 3 parallel round-trips
// (F-115). The cache is deliberately narrow, and its keying is the contract
// fixed by DB-08 in the 2026-10-25 regression audit:
//
//   - The key is (connection pool, database, table). Including the *sql.DB
//     pool pointer keeps two sessions that merely share a database NAME
//     (test vs production, both "app.users") from reading each other's
//     column lists — the old "dbName.tableName" key leaked across
//     connections.
//   - Entries carry a short TTL, so a DDL change made outside this pool (or
//     through a path that forgets to invalidate) disappears within seconds
//     instead of never.
//   - The provider's own DDL methods invalidate eagerly on success via
//     pgSchemaCacheInvalidate; without that hook the structure page kept
//     serving the pre-DDL columns and users re-applied their change, hitting
//     "column already exists" — the most visible "越修越坏" symptom.
type pgSchemaCacheEntry struct {
	result   *SchemaResult
	storedAt time.Time
}

// pgSchemaCacheTTL bounds staleness for changes this connection did not make.
const pgSchemaCacheTTL = 30 * time.Second

var postgresSchemaCache sync.Map // map[string]pgSchemaCacheEntry

func pgSchemaCacheKey(db *sql.DB, dbName, tableName string) string {
	return fmt.Sprintf("%p|%s|%s", db, dbName, tableName)
}

func pgSchemaCacheLoad(db *sql.DB, dbName, tableName string) (*SchemaResult, bool) {
	key := pgSchemaCacheKey(db, dbName, tableName)
	v, ok := postgresSchemaCache.Load(key)
	if !ok {
		return nil, false
	}
	e, ok := v.(pgSchemaCacheEntry)
	if !ok || time.Since(e.storedAt) > pgSchemaCacheTTL {
		postgresSchemaCache.Delete(key)
		return nil, false
	}
	return e.result, true
}

func pgSchemaCacheStore(db *sql.DB, dbName, tableName string, res *SchemaResult) {
	postgresSchemaCache.Store(pgSchemaCacheKey(db, dbName, tableName),
		pgSchemaCacheEntry{result: res, storedAt: time.Now()})
}

// pgSchemaCacheInvalidate drops the memoised schema of one table. Every
// postgres DDL method that changes a table's shape (or drops it) calls this
// on success.
func pgSchemaCacheInvalidate(db *sql.DB, dbName, tableName string) {
	postgresSchemaCache.Delete(pgSchemaCacheKey(db, dbName, tableName))
}
