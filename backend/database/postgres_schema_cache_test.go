package database

import (
	"database/sql"
	"sync"
	"testing"
	"time"
)

// The schema cache was the "越修越坏" hot spot of the 2026-10-25 regression
// audit (DB-08): keyed only by "dbName.tableName", never invalidated, no
// TTL. These tests pin the corrected contract:
//   - entries are scoped to a connection pool (no cross-server sharing),
//   - DDL invalidation drops them,
//   - stale entries expire.

// dbHandle returns a distinct *sql.DB pointer purely for keying; no
// connection is ever opened.
func dbHandle() *sql.DB { return &sql.DB{} }

func TestPostgresSchemaCacheScopedPerPool(t *testing.T) {
	const dbName = "app"
	const tbl = "users_key"
	db1, db2 := dbHandle(), dbHandle()

	r1 := &SchemaResult{Columns: []ColumnInfo{{Name: "id", Type: "int"}}}
	pgSchemaCacheStore(db1, dbName, tbl, r1)
	t.Cleanup(func() { pgSchemaCacheInvalidate(db1, dbName, tbl) })

	// Same pool, same names → the memoised pointer comes back.
	if got, ok := pgSchemaCacheLoad(db1, dbName, tbl); !ok || got != r1 {
		t.Fatalf("cache miss for %s.%s on the storing pool", dbName, tbl)
	}
	// A different pool (another server with the same database name) must NOT
	// see the first pool's column list.
	if _, ok := pgSchemaCacheLoad(db2, dbName, tbl); ok {
		t.Fatalf("cache hit across connection pools for %s.%s", dbName, tbl)
	}
}

func TestPostgresSchemaCacheInvalidate(t *testing.T) {
	const dbName = "app"
	const tbl = "users_inv"
	db := dbHandle()

	pgSchemaCacheStore(db, dbName, tbl, &SchemaResult{})
	pgSchemaCacheInvalidate(db, dbName, tbl)
	if _, ok := pgSchemaCacheLoad(db, dbName, tbl); ok {
		t.Fatalf("DDL invalidation left a stale schema for %s.%s", dbName, tbl)
	}
}

func TestPostgresSchemaCacheExpires(t *testing.T) {
	const dbName = "app"
	const tbl = "users_ttl"
	db := dbHandle()
	key := pgSchemaCacheKey(db, dbName, tbl)

	// Store an entry whose TTL has already elapsed.
	postgresSchemaCache.Store(key, pgSchemaCacheEntry{
		result:   &SchemaResult{},
		storedAt: time.Now().Add(-pgSchemaCacheTTL - time.Second),
	})
	t.Cleanup(func() { pgSchemaCacheInvalidate(db, dbName, tbl) })

	if _, ok := pgSchemaCacheLoad(db, dbName, tbl); ok {
		t.Fatalf("expired entry served for %s.%s", dbName, tbl)
	}
	// Loading an expired entry must also remove it.
	if _, ok := postgresSchemaCache.Load(key); ok {
		t.Fatalf("expired entry not evicted for %s.%s", dbName, tbl)
	}
}

// TestPostgresSchemaCacheParallel exercises concurrent Load/Store/Invalidate;
// with -race this catches unsynchronised access to the cache or a shared
// SchemaResult.
func TestPostgresSchemaCacheParallel(t *testing.T) {
	const dbName = "app"
	const tbl = "users_par"
	db := dbHandle()
	t.Cleanup(func() { pgSchemaCacheInvalidate(db, dbName, tbl) })

	expected := &SchemaResult{Columns: []ColumnInfo{{Name: "id", Type: "int"}}}
	pgSchemaCacheStore(db, dbName, tbl, expected)

	const N = 64
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				switch i % 4 {
				case 0:
					pgSchemaCacheStore(db, dbName, tbl, expected)
				case 1:
					pgSchemaCacheInvalidate(db, dbName, tbl)
				default:
					// A miss is legal here (a concurrent invalidator ran);
					// a hit must be the exact pointer we stored.
					if got, ok := pgSchemaCacheLoad(db, dbName, tbl); ok && got != expected {
						t.Errorf("concurrent Load returned %p, want %p", got, expected)
						return
					}
				}
			}
		}()
	}
	wg.Wait()

	// Final value must be the one last stored.
	pgSchemaCacheStore(db, dbName, tbl, expected)
	if got, ok := pgSchemaCacheLoad(db, dbName, tbl); !ok || got != expected {
		t.Fatalf("final cache state: got=%v ok=%v, want %v", got, ok, expected)
	}
}
