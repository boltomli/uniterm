package sync

import (
	"reflect"
	"testing"
)

func TestEffectiveSyncFilesNilScopeKeepsLegacy(t *testing.T) {
	got := EffectiveSyncFiles(nil)
	if !reflect.DeepEqual(got, syncedFiles) {
		t.Fatalf("nil scope = %v, want legacy %v", got, syncedFiles)
	}
	// DeepEqual cannot detect aliasing; mutating the result must not
	// touch the shared syncedFiles slice.
	got[0] = "x"
	if syncedFiles[0] == "x" {
		t.Fatal("nil scope result aliases syncedFiles; want a copy")
	}
}

func TestEffectiveSyncFilesFiltersToSyncable(t *testing.T) {
	got := EffectiveSyncFiles([]string{"settings.json", "connections.json", "not-a-file.json"})
	want := []string{"settings.json", "connections.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEffectiveSyncFilesDedupesScope(t *testing.T) {
	got := EffectiveSyncFiles([]string{"settings.json", "settings.json"})
	want := []string{"settings.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSyncableFilesIncludesSettings(t *testing.T) {
	found := false
	for _, n := range syncableFiles {
		if n == "settings.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("syncableFiles must contain settings.json")
	}
	if len(syncableFiles) != len(syncedFiles)+1 {
		t.Fatalf("syncableFiles len %d, want syncedFiles len %d + 1", len(syncableFiles), len(syncedFiles))
	}
}
