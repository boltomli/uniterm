package session

import (
	"encoding/json"
	"testing"
)

func TestVNCConnectionPorts(t *testing.T) {
	for _, tc := range []struct {
		kind string
		port int
		want string
	}{
		{"vnc", 0, "[::1]:5900"}, {"vnc", 3, "[::1]:5903"},
		{"vnc", 5901, "[::1]:5901"},
	} {
		t.Run(tc.kind+tc.want, func(t *testing.T) {
			manager := NewSessionManager()
			s, err := manager.Create(tc.kind, ConnectionConfig{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { manager.Close(s.ID()) })
			if err := s.Connect(ConnectionConfig{Host: "::1", Port: tc.port}); err != nil {
				t.Fatal(err)
			}
			vnc := s.(*VNCSession)
			if s.Type() != tc.kind || vnc.proxy.target != tc.want || vnc.ProxyAddr() == "" {
				t.Fatalf("type=%q target=%q proxy=%q", s.Type(), vnc.proxy.target, vnc.ProxyAddr())
			}
		})
	}
}

func TestDesktopViewConfigRoundTrip(t *testing.T) {
	const raw = `{"type":"vnc","desktopView":{"selected":"custom","region":{"x":1920,"y":0,"width":2560,"height":1440}}}`
	var config ConnectionConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var loaded ConnectionConfig
	if err := json.Unmarshal(encoded, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.DesktopView == nil || loaded.DesktopView.Region == nil || loaded.DesktopView.Selected != "custom" || loaded.DesktopView.Region.X != 1920 || loaded.DesktopView.Region.Width != 2560 {
		t.Fatalf("desktop viewport lost during persistence: %+v", loaded.DesktopView)
	}
}
