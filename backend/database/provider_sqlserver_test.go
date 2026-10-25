package database

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestSqlserverDSN covers DSN building for plain hosts and named instances
// (issue: host containing "\" must not end up in url.URL.Host, where
// url.URL.String() escapes it to %5C and the driver's url.Parse rejects it
// as an invalid URL escape).
func TestSqlserverDSN(t *testing.T) {
	cases := []struct {
		name         string
		host         string
		port         int
		wantHost     string // expected u.Host in the parsed DSN
		wantInstance string // expected u.Path ("/instance") or ""
		wantRawQuery string // substring that must appear in RawQuery
		noPort       bool   // DSN must not carry an explicit port
	}{
		{
			name:         "plain host with default port",
			host:         "localhost",
			port:         0,
			wantHost:     "localhost:1433",
			wantInstance: "",
			wantRawQuery: "database=mydb",
		},
		{
			name:         "plain host with explicit port",
			host:         "db.example.com",
			port:         11433,
			wantHost:     "db.example.com:11433",
			wantInstance: "",
			wantRawQuery: "database=mydb",
		},
		{
			name:         "named instance without port omits port for SQL Browser",
			host:         `localhost\SQLEXPRESS`,
			port:         0,
			wantHost:     "localhost",
			wantInstance: "/SQLEXPRESS",
			wantRawQuery: "database=mydb",
			noPort:       true,
		},
		{
			// DB-25: the form prefills 1433 on type switch, so the explicit
			// port must be ignored for named instances — otherwise the fixed
			// port path is the only reachable one in the UI and SQL Browser
			// is bypassed.
			name:         "named instance ignores an explicit port (DB-25)",
			host:         `192.168.1.5\SQL2019`,
			port:         21433,
			wantHost:     "192.168.1.5",
			wantInstance: "/SQL2019",
			wantRawQuery: "database=mydb",
			noPort:       true,
		},
	}

	p := &sqlserverProvider{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := p.DSN(tc.host, tc.port, "sa", "secret", "mydb", nil)
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("driver-side url.Parse failed on DSN %q: %v", dsn, err)
			}
			if u.Scheme != "sqlserver" {
				t.Errorf("scheme = %q, want sqlserver", u.Scheme)
			}
			if u.Host != tc.wantHost {
				t.Errorf("host = %q, want %q", u.Host, tc.wantHost)
			}
			if u.Path != tc.wantInstance {
				t.Errorf("path = %q, want %q", u.Path, tc.wantInstance)
			}
			if !strings.Contains(u.RawQuery, tc.wantRawQuery) {
				t.Errorf("raw query %q does not contain %q", u.RawQuery, tc.wantRawQuery)
			}
			if tc.noPort && strings.Contains(u.Host, ":") {
				t.Errorf("DSN %q must omit port for browser resolution", dsn)
			}
			if got, ok := u.User.Password(); !ok || got != "secret" {
				t.Errorf("password not preserved")
			}
		})
	}
}

// TestSqlServerNoPooledUseBatch guards DB-24: the old withUse helper ran
// `USE [db];\n<statement>` on an arbitrary POOLED connection, so the
// database switch survived the statement and polluted whatever session the
// pool handed out next. All database-scoped work must go through the
// dedicated-connection helpers instead.
func TestSqlServerNoPooledUseBatch(t *testing.T) {
	src, err := os.ReadFile("provider_sqlserver.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(src)
	if strings.Contains(text, "func (p *sqlserverProvider) withUse(") {
		t.Fatal("withUse is back — it poisons pooled connections (DB-24)")
	}
	if strings.Contains(text, `"USE %s;\n%s"`) {
		t.Fatal("string-batched USE statement found — use a pinned connection (DB-24)")
	}
	for _, helper := range []string{"func (p *sqlserverProvider) onDB(", "execDDLOnDB(", "queryStringsOnDB("} {
		if !strings.Contains(text, helper) {
			t.Errorf("missing dedicated-connection helper %q (DB-24)", helper)
		}
	}
}
