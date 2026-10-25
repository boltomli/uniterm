package database

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// DB-10: MySQL's driver-level readTimeout/writeTimeout are per-SOCKET
// deadlines; set below the statement budget they kill any query that takes
// longer with "invalid connection" while its context still had time left
// (SELECT SLEEP(12) died at 10s even though queryTimeout is 30s — and
// raising queryTimeout could never help). The DSN must therefore leave the
// socket deadlines to the context.
func TestMySQLSocketTimeoutsDoNotUndercutStatementBudgets(t *testing.T) {
	p, err := NewProvider("mysql")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	dsn := p.DSN("db.example", 3306, "u", "p", "app", nil)

	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(%q): %v", dsn, err)
	}
	// go-sql-driver folds the timeout params into struct fields on parse, so
	// pin both the struct values and the explicit DSN tokens (an absent
	// readTimeout would parse to 0 as well).
	if cfg.Timeout != 10*time.Second {
		t.Errorf("dial timeout = %v, want 10s", cfg.Timeout)
	}
	if !strings.Contains(dsn, "readTimeout=0") || !strings.Contains(dsn, "writeTimeout=0") {
		t.Errorf("DSN must set readTimeout/writeTimeout to 0 explicitly: %s", dsn)
	}
	if cfg.ReadTimeout != 0 || cfg.WriteTimeout != 0 {
		t.Errorf("socket timeouts = %v/%v, want 0/0 (context deadlines own the budget, DB-10)", cfg.ReadTimeout, cfg.WriteTimeout)
	}
	// found-rows semantics feed the DB-01 affected-row check.
	if !cfg.ClientFoundRows {
		t.Errorf("clientFoundRows not set in DSN: %s", dsn)
	}
	if queryTimeout > ddlTimeout || ddlTimeout > dumpTimeout {
		t.Fatalf("budget ordering broken: query=%v ddl=%v dump=%v", queryTimeout, ddlTimeout, dumpTimeout)
	}
}

// DB-11: no DDL/export path may run on a bare context.Background() — a lock
// wait would spin the UI forever. Each provider must route its db.Exec DDL
// through execDDL (DDL budget) and create its export context via dumpCtx
// (export budget).
func TestDDLAndDumpPathsAreBounded(t *testing.T) {
	providers := []string{
		"provider_mysql.go",
		"provider_postgres.go",
		"provider_oracle.go",
		"provider_sqlserver.go",
		"provider_rqlite.go",
		"provider_sqlite.go",
	}
	for _, f := range providers {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		text := string(src)
		// MySQL pins a physical connection for DDL (mysqlUseDB) and calls
		// ddlCtx() per method; the other dialects go through execDDL.
		if f == "provider_mysql.go" {
			if !strings.Contains(text, "ddlCtx()") {
				t.Errorf("%s: no ddlCtx — DDL runs without the DDL budget (DB-11)", f)
			}
		} else if !strings.Contains(text, "execDDL(db,") {
			t.Errorf("%s: no execDDL call — DDL runs without the DDL budget (DB-11)", f)
		}
		if !strings.Contains(text, "dumpCtx()") {
			t.Errorf("%s: no dumpCtx — the export path has no deadline (DB-11)", f)
		}
		// The only sanctioned bare context in these files is the errgroup
		// plumbing, which never executes SQL itself.
		if strings.Contains(text, "ctx := context.Background()") &&
			!strings.Contains(text, "errgroup.WithContext(context.Background())") {
			t.Errorf("%s: bare 'ctx := context.Background()' — unbounded statement path (DB-11)", f)
		}
	}
	if ddlTimeout < queryTimeout {
		t.Fatalf("ddlTimeout (%v) must not be shorter than queryTimeout (%v)", ddlTimeout, queryTimeout)
	}
	if dumpTimeout <= ddlTimeout {
		t.Fatalf("dumpTimeout (%v) must exceed ddlTimeout (%v)", dumpTimeout, ddlTimeout)
	}
}
