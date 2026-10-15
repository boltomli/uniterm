package store

import "testing"

// TestQuickCommandsStoreSyncRoundTrip pins the sync exchange contract for
// quickCommands.json, including the Version backfill ImportPlain applies.
func TestQuickCommandsStoreSyncRoundTrip(t *testing.T) {
	a := NewQuickCommandsStore(t.TempDir())
	seed := QuickCommandData{
		Version:  1,
		Groups:   []QuickCommandGroup{{ID: "qg1", Name: "g", SortOrder: 1}},
		Commands: []QuickCommand{{ID: "q1", Name: "n", Command: "ls -la", GroupID: "qg1"}},
	}
	if err := a.Save(seed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plain, err := a.ExportPlain()
	if err != nil {
		t.Fatalf("ExportPlain: %v", err)
	}
	b := NewQuickCommandsStore(t.TempDir())
	if err := b.ImportPlain(plain); err != nil {
		t.Fatalf("ImportPlain: %v", err)
	}
	got, err := b.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Groups) != 1 || len(got.Commands) != 1 || got.Commands[0].Command != "ls -la" {
		t.Fatalf("quick commands lost: %+v", got)
	}
}
