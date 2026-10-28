package store

import (
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

func TestConnectionStoreDesktopPreferences(t *testing.T) {
	for _, protocol := range []string{"vnc", "rdp", "spice"} {
		t.Run(protocol, func(t *testing.T) {
			s := mustConnStore(t)
			data := session.ConnectionStoreData{Connections: []session.ConnectionConfig{
				{ID: "remembered", Type: protocol, AuthType: "password", Password: "secret",
					DesktopPreferences: &session.DesktopPreferences{ScaleViewport: true, ViewOnly: true, ShowDotCursor: true}},
				{ID: "defaults", Type: protocol},
			}}
			if err := s.Save(data); err != nil {
				t.Fatal(err)
			}
			reader := &ConnectionStore{configDir: s.configDir, passwordStore: fakeCipherStore{}}
			loaded, err := reader.Load()
			if err != nil {
				t.Fatal(err)
			}
			remembered, defaults := loaded.Connections[0], loaded.Connections[1]
			if remembered.DesktopPreferences == nil || !remembered.DesktopPreferences.ScaleViewport ||
				!remembered.DesktopPreferences.ViewOnly || !remembered.DesktopPreferences.ShowDotCursor {
				t.Fatal("display preferences were not restored")
			}
			if remembered.Password != "secret" {
				t.Fatal("saving display preferences changed the credentials")
			}
			if defaults.DesktopPreferences != nil {
				t.Fatal("preferences leaked into another connection or changed legacy defaults")
			}
			// Turning an option back off must also survive closing and reopening.
			loaded.Connections[0].DesktopPreferences.ScaleViewport = false
			loaded.Connections[0].DesktopPreferences.ViewOnly = false
			loaded.Connections[0].DesktopPreferences.ShowDotCursor = false
			if err := reader.Save(loaded); err != nil {
				t.Fatal(err)
			}
			loaded, err = s.Load()
			if err != nil {
				t.Fatal(err)
			}
			remembered = loaded.Connections[0]
			if remembered.DesktopPreferences == nil || remembered.DesktopPreferences.ScaleViewport ||
				remembered.DesktopPreferences.ViewOnly || remembered.DesktopPreferences.ShowDotCursor {
				t.Fatal("turning display preferences off did not persist")
			}
		})
	}
}
