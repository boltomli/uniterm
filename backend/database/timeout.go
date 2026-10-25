package database

import (
	"context"
	"database/sql"
	"time"
)

// Statement budgets for the database layer (DB-10/DB-11, 2026-10-25
// regression audit). Every backend path that talks to a server runs under
// one of these deadlines — a bare context.Background() means a lock wait
// (ALTER TABLE blocked behind a metadata lock, a wedged server) spins the
// UI forever with no way out except killing the app.
//
//   - queryTimeout (executor.go) — ad-hoc SELECT/UPDATE, script statements,
//     row CRUD: 30s.
//   - ddlTimeout — DDL, which may legitimately wait on metadata locks longer
//     than a query should: 60s.
//   - dumpTimeout — full-table export / bulk copy: 10m, the same budget as a
//     whole script run (DB-12 will add streaming and row caps on top).
//
// MySQL note (DB-10): the DSN's socket readTimeout/writeTimeout must never
// be shorter than these budgets — the driver aborts the read first with
// "invalid connection" and the context deadline never gets a chance. They
// are therefore set to 0 (deferred to the context) in provider_mysql.go.
const (
	ddlTimeout  = 60 * time.Second
	dumpTimeout = 10 * time.Minute
)

// ddlCtx returns a context for a single DDL statement; the caller must call
// the returned cancel func.
func ddlCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), ddlTimeout)
}

// dumpCtx returns a context for a table export / bulk copy.
func dumpCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), dumpTimeout)
}

// execDDL runs a single DDL statement under the DDL budget (DB-11). All
// providers' plain db.Exec DDL sites go through here so no dialect keeps a
// deadline-less path.
func execDDL(db *sql.DB, query string, args ...any) error {
	ctx, cancel := ddlCtx()
	defer cancel()
	_, err := db.ExecContext(ctx, query, args...)
	return err
}
