package session

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// newTestRedisSession connects a RedisSession to a miniredis server.
func newTestRedisSession(t *testing.T, m *miniredis.Miniredis) *RedisSession {
	t.Helper()
	host, portStr, err := net.SplitHostPort(m.Addr())
	if err != nil {
		t.Fatalf("miniredis addr %q: %v", m.Addr(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("miniredis port %q: %v", portStr, err)
	}
	s := NewRedisSession("test-redis")
	if err := s.Connect(ConnectionConfig{Type: "redis", Host: host, Port: port}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Disconnect() })
	return s
}

// TestSwitchDBAllOperationsUseSelectedDB guards the SwitchDB fix: every
// operation after a switch must land in the selected database. The previous
// implementation sent SELECT on a single pooled connection, so any other
// pooled connection silently kept operating on DB 0.
func TestSwitchDBAllOperationsUseSelectedDB(t *testing.T) {
	m := miniredis.RunT(t)
	s := newTestRedisSession(t, m)

	if err := s.SwitchDB(3); err != nil {
		t.Fatalf("SwitchDB(3): %v", err)
	}
	if got := s.DBIndex(); got != 3 {
		t.Fatalf("DBIndex() = %d, want 3", got)
	}

	if err := s.SetString("k", "v"); err != nil {
		t.Fatalf("SetString: %v", err)
	}
	if v, err := s.GetString("k"); err != nil || v != "v" {
		t.Fatalf("GetString = %q, %v; want \"v\"", v, err)
	}
	if m.DB(0).Exists("k") {
		t.Fatal("key landed in db 0 after SwitchDB(3)")
	}
	if !m.DB(3).Exists("k") {
		t.Fatal("key missing from db 3 after SwitchDB(3)")
	}

	// Concurrent writes exercise the whole pool: with the old single-connection
	// SELECT, any connection beyond the first wrote to db 0.
	const parallel = 8
	var wg sync.WaitGroup
	errCh := make(chan error, parallel)
	for i := range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.SetString(fmt.Sprintf("par:%d", i), "x"); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("parallel SetString: %v", err)
	}
	for i := range parallel {
		k := fmt.Sprintf("par:%d", i)
		if m.DB(0).Exists(k) {
			t.Fatalf("%s landed in db 0", k)
		}
		if !m.DB(3).Exists(k) {
			t.Fatalf("%s missing from db 3", k)
		}
	}
}

func TestSwitchDBRoundTripAndValidation(t *testing.T) {
	m := miniredis.RunT(t)
	s := newTestRedisSession(t, m)

	if err := s.SwitchDB(5); err != nil {
		t.Fatalf("SwitchDB(5): %v", err)
	}
	if err := s.SwitchDB(0); err != nil {
		t.Fatalf("SwitchDB(0): %v", err)
	}
	if err := s.SetString("back", "home"); err != nil {
		t.Fatalf("SetString after switch back: %v", err)
	}
	if !m.DB(0).Exists("back") {
		t.Fatal("key missing from db 0 after switching back")
	}
	if m.DB(5).Exists("back") {
		t.Fatal("key landed in db 5 after switching back to 0")
	}

	for _, idx := range []int{-1, 16} {
		if err := s.SwitchDB(idx); err == nil {
			t.Fatalf("SwitchDB(%d) succeeded, want error", idx)
		}
	}
}

// TestListReplace guards the atomic list save: one MULTI/DEL/RPUSH/EXEC
// instead of per-element LPOP round trips that can leave the list empty when
// the connection drops mid-save.
func TestListReplace(t *testing.T) {
	m := miniredis.RunT(t)
	s := newTestRedisSession(t, m)

	if _, err := s.ListPush("L", "right", []string{"a", "b", "c"}); err != nil {
		t.Fatalf("ListPush: %v", err)
	}
	if err := s.ListReplace("L", []string{"x", "y"}); err != nil {
		t.Fatalf("ListReplace: %v", err)
	}
	got, err := s.GetListRange("L", 0, -1)
	if err != nil {
		t.Fatalf("GetListRange: %v", err)
	}
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("list = %v, want [x y]", got)
	}

	// Replacing with an empty list must delete the key entirely.
	if err := s.ListReplace("L", nil); err != nil {
		t.Fatalf("ListReplace(nil): %v", err)
	}
	if m.DB(0).Exists("L") {
		t.Fatal("list key still exists after replacing with empty list")
	}
}

// TestGetHashFieldsRespectsLimit guards the capped hash read: HSCAN must stop
// at the requested limit instead of loading a huge hash whole.
func TestGetHashFieldsRespectsLimit(t *testing.T) {
	m := miniredis.RunT(t)
	s := newTestRedisSession(t, m)

	for i := range 30 {
		if err := s.HashSet("h", fmt.Sprintf("f%02d", i), fmt.Sprintf("v%02d", i)); err != nil {
			t.Fatalf("HashSet: %v", err)
		}
	}
	got, err := s.GetHashFields("h", 10)
	if err != nil {
		t.Fatalf("GetHashFields: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d entries, want 10", len(got))
	}
	for i, e := range got {
		want := fmt.Sprintf("f%02d", i)
		if e.Field != want {
			t.Fatalf("entry %d field = %q, want %q (sorted order)", i, e.Field, want)
		}
	}
	// A limit at or above the size returns everything.
	all, err := s.GetHashFields("h", 100)
	if err != nil || len(all) != 30 {
		t.Fatalf("GetHashFields(100) = %d entries, %v; want 30", len(all), err)
	}
}

func TestGetSetMembersRespectsLimit(t *testing.T) {
	m := miniredis.RunT(t)
	s := newTestRedisSession(t, m)

	members := make([]string, 25)
	for i := range members {
		members[i] = fmt.Sprintf("m%02d", i)
	}
	if _, err := s.SetAdd("S", members); err != nil {
		t.Fatalf("SetAdd: %v", err)
	}
	got, err := s.GetSetMembers("S", 5)
	if err != nil {
		t.Fatalf("GetSetMembers: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d members, want 5", len(got))
	}
	seen := make(map[string]bool, len(members))
	for _, mm := range members {
		seen[mm] = true
	}
	for _, g := range got {
		if !seen[g] {
			t.Fatalf("unexpected member %q", g)
		}
	}
}

func TestGetZSetRangeCappedRespectsLimit(t *testing.T) {
	m := miniredis.RunT(t)
	s := newTestRedisSession(t, m)

	for i := range 20 {
		if _, err := s.ZSetAdd("Z", []ScoredMember{{Member: fmt.Sprintf("z%02d", i), Score: float64(i)}}); err != nil {
			t.Fatalf("ZSetAdd: %v", err)
		}
	}
	got, err := s.GetZSetRangeCapped("Z", "-inf", "+inf", 7)
	if err != nil {
		t.Fatalf("GetZSetRangeCapped: %v", err)
	}
	if len(got) != 7 {
		t.Fatalf("got %d members, want 7", len(got))
	}
	for i, g := range got {
		if want := fmt.Sprintf("z%02d", i); g.Member != want {
			t.Fatalf("member %d = %q, want %q", i, g.Member, want)
		}
	}
}

// TestHashSetFields guards the batched hash write used by the value editor:
// one call must set every field-value pair.
func TestHashSetFields(t *testing.T) {
	m := miniredis.RunT(t)
	s := newTestRedisSession(t, m)

	entries := []FieldEntry{
		{Field: "a", Value: "1"},
		{Field: "b", Value: "2"},
		{Field: "c", Value: "3"},
	}
	if err := s.HashSetFields("h", entries); err != nil {
		t.Fatalf("HashSetFields: %v", err)
	}
	got, err := s.GetHashAll("h")
	if err != nil {
		t.Fatalf("GetHashAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("hash has %d fields, want 3", len(got))
	}
	for _, e := range entries {
		v, err := s.client.HGet(t.Context(), "h", e.Field).Result()
		if err != nil || v != e.Value {
			t.Fatalf("field %s = %q, %v; want %q", e.Field, v, err, e.Value)
		}
	}
}

// TestGetKeyInfoLength guards the element-count metadata the value editor
// uses to detect a truncated load.
func TestGetKeyInfoLength(t *testing.T) {
	m := miniredis.RunT(t)
	s := newTestRedisSession(t, m)

	if err := s.HashSetFields("h", []FieldEntry{{Field: "a"}, {Field: "b"}, {Field: "c"}}); err != nil {
		t.Fatalf("HashSetFields: %v", err)
	}
	if err := s.ListReplace("L", []string{"x", "y"}); err != nil {
		t.Fatalf("ListReplace: %v", err)
	}
	if err := s.SetString("S", "abc"); err != nil {
		t.Fatalf("SetString: %v", err)
	}

	cases := []struct {
		key  string
		want int64
	}{
		{"h", 3},
		{"L", 2},
		{"S", 3},
	}
	for _, tc := range cases {
		info, err := s.GetKeyInfo(tc.key)
		if err != nil {
			t.Fatalf("GetKeyInfo(%s): %v", tc.key, err)
		}
		if info.Length != tc.want {
			t.Fatalf("GetKeyInfo(%s).Length = %d, want %d", tc.key, info.Length, tc.want)
		}
	}
}
