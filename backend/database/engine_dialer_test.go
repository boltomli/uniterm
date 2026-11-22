package database

import (
	"context"
	"errors"
	"net"
	"testing"
)

// go-sql-driver's RegisterDialContext writes to a process-global map with no
// unregister API. Keying the registration by proxy identity keeps repeated
// connects through the same proxy from adding one entry each time.
func TestMySQLProxyDialerRegistryIsBounded(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("dial disabled in test")
	}
	key := t.Name() + "-proxy-a"
	before := len(mysqlDialNames)
	for i := 0; i < 5; i++ {
		db, err := NewDBWithDialKey("mysql", "u:p@tcp(127.0.0.1:3306)/db", DialContextFunc(dial), key)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		db.Close()
	}
	if got := len(mysqlDialNames) - before; got != 1 {
		t.Fatalf("registry grew by %d entries for one proxy key, want 1", got)
	}
	// A different proxy identity still gets its own entry.
	db, err := NewDBWithDialKey("mysql", "u:p@tcp(127.0.0.1:3306)/db", DialContextFunc(dial), t.Name()+"-proxy-b")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if got := len(mysqlDialNames) - before; got != 2 {
		t.Fatalf("registry grew by %d entries for two proxy keys, want 2", got)
	}
}
