package store

import (
	"reflect"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

func TestConnectionStoreDesktopRegions(t *testing.T) {
	s := mustConnStore(t)
	view := &session.DesktopView{
		Selected: "region:right",
		Regions: []session.DesktopRegion{
			{ID: "left", Name: "主屏", DesktopRect: session.DesktopRect{X: 0, Y: 0, Width: 1920, Height: 1080}},
			{ID: "right", Name: "副屏", DesktopRect: session.DesktopRect{X: 1920, Y: 0, Width: 2560, Height: 1440}},
		},
	}
	data := session.ConnectionStoreData{Connections: []session.ConnectionConfig{{ID: "desktop", Type: "vnc", AuthType: "password", Password: "secret", DesktopView: view}}}
	if err := s.Save(data); err != nil {
		t.Fatal(err)
	}
	// Load through a new store instance to exercise the file representation.
	reader := &ConnectionStore{configDir: s.configDir, passwordStore: fakeCipherStore{}}
	loaded, err := reader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Connections[0].DesktopView, view) {
		t.Fatalf("saved regions changed: %+v", loaded.Connections[0].DesktopView)
	}
	if loaded.Connections[0].Password != "secret" {
		t.Fatal("connection credentials did not survive the region save")
	}
	loaded.Connections[0].DesktopView.Regions = loaded.Connections[0].DesktopView.Regions[:1]
	loaded.Connections[0].DesktopView.Selected = "all"
	if err := reader.Save(loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Connections[0].DesktopView.Regions) != 1 || loaded.Connections[0].DesktopView.Selected != "all" {
		t.Fatalf("region deletion did not persist: %+v", loaded.Connections[0].DesktopView)
	}
}
