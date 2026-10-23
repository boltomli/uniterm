package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStateRoundtrip(t *testing.T) {
	s := NewLocalStateStore(t.TempDir())

	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	if loaded.CollapsedQuickCommandGroupIds == nil {
		t.Fatalf("default LocalState should init CollapsedQuickCommandGroupIds, got nil")
	}
	if len(loaded.CollapsedQuickCommandGroupIds) != 0 {
		t.Fatalf("default CollapsedQuickCommandGroupIds should be empty, got %v", loaded.CollapsedQuickCommandGroupIds)
	}

	loaded.CollapsedQuickCommandGroupIds = []string{"qcg-1", "__ungrouped__"}
	if err := s.Save(loaded); err != nil {
		t.Fatalf("save: %v", err)
	}

	again, err := s.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(again.CollapsedQuickCommandGroupIds) != 2 ||
		again.CollapsedQuickCommandGroupIds[0] != "qcg-1" ||
		again.CollapsedQuickCommandGroupIds[1] != "__ungrouped__" {
		t.Fatalf("roundtrip mismatch: got %v", again.CollapsedQuickCommandGroupIds)
	}

	// Defaults must survive: older configs lack the new key entirely.
	if err := os.WriteFile(filepath.Join(s.configDir, localStateFileName), []byte(`{"sidebarVisible":false}`), 0600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	legacy, err := s.Load()
	if err != nil {
		t.Fatalf("load legacy: %v", err)
	}
	if legacy.CollapsedQuickCommandGroupIds == nil || len(legacy.CollapsedQuickCommandGroupIds) != 0 {
		t.Fatalf("legacy config should yield empty CollapsedQuickCommandGroupIds, got %v", legacy.CollapsedQuickCommandGroupIds)
	}
}

func TestLocalStateSftpHiddenColumns(t *testing.T) {
	s := NewLocalStateStore(t.TempDir())

	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	if loaded.SftpHiddenColumns != nil {
		t.Fatalf("default SftpHiddenColumns should be nil (all visible), got %v", loaded.SftpHiddenColumns)
	}

	loaded.SftpHiddenColumns = []string{"permission", "owner", "group"}
	if err := s.Save(loaded); err != nil {
		t.Fatalf("save: %v", err)
	}

	again, err := s.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(again.SftpHiddenColumns) != 3 || again.SftpHiddenColumns[0] != "permission" {
		t.Fatalf("roundtrip mismatch: got %v", again.SftpHiddenColumns)
	}

	// Older configs lack the key entirely; columns stay visible (nil set).
	if err := os.WriteFile(filepath.Join(s.configDir, localStateFileName), []byte(`{"sidebarVisible":true}`), 0600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	legacy, err := s.Load()
	if err != nil {
		t.Fatalf("load legacy: %v", err)
	}
	if legacy.SftpHiddenColumns != nil {
		t.Fatalf("legacy config should yield nil SftpHiddenColumns, got %v", legacy.SftpHiddenColumns)
	}
}

func TestLocalStateAIPreferencesRoundtrip(t *testing.T) {
	s := NewLocalStateStore(t.TempDir())
	state, err := s.Load()
	if err != nil || state.AI != nil {
		t.Fatalf("legacy/default state should not invent an AI selection: %v, %v", state.AI, err)
	}
	state.AI = &AILocalState{ActiveModelID: "second", ModelPreferences: map[string]AIReasoningPreference{
		"first":  {ReasoningEffort: "high"},
		"second": {ReasoningEffort: "low"},
	}}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	restarted := NewLocalStateStore(s.configDir)
	got, err := restarted.Load()
	if err != nil || got.AI == nil || got.AI.ActiveModelID != "second" || got.AI.ModelPreferences["first"].ReasoningEffort != "high" || got.AI.ModelPreferences["second"].ReasoningEffort != "low" {
		t.Fatalf("lost local AI preferences: %+v, %v", got.AI, err)
	}
	got.SidebarVisible = false
	if err := restarted.Save(got); err != nil {
		t.Fatal(err)
	}
	again, _ := restarted.Load()
	if again.AI == nil || len(again.AI.ModelPreferences) != 2 {
		t.Fatal("unrelated local state save discarded AI preferences")
	}
}
