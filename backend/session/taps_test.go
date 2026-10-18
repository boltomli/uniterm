package session

import "testing"

func TestTapRemovalKeepsOtherObservers(t *testing.T) {
	s := &baseSession{}
	var data, status [3]int
	d0 := s.AddDataTap(func([]byte) { data[0]++ })
	d1 := s.AddDataTap(func([]byte) { data[1]++ })
	d2 := s.AddDataTap(func([]byte) { data[2]++ })
	s0 := s.AddStatusTap(func(SessionStatus) { status[0]++ })
	s1 := s.AddStatusTap(func(SessionStatus) { status[1]++ })
	s2 := s.AddStatusTap(func(SessionStatus) { status[2]++ })
	d0()
	d1()
	s0()
	s1()
	s.emitData([]byte("test"))
	s.setStatus(StatusConnected)
	if data != [3]int{0, 0, 1} || status != [3]int{0, 0, 1} {
		t.Fatalf("wrong observers removed: data=%v status=%v", data, status)
	}
	// Reuse slots, then invoke an old unsubscribe again.
	n := 0
	s.AddDataTap(func([]byte) { n++ })
	s.AddStatusTap(func(SessionStatus) { n++ })
	d0()
	s0()
	d2()
	s2()
	s.emitData([]byte("again"))
	s.setStatus(StatusConnected)
	if n != 2 || data[2] != 1 || status[2] != 1 {
		t.Fatalf("unsubscribe removed a reused slot: n=%d data=%v status=%v", n, data, status)
	}
}
