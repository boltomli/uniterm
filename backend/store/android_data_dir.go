package store

import "path/filepath"

// resolveAndroidDataDir maps the Android app files dir (from the wails
// bridge) to the uniTerm data dir. Kept untagged so it is unit-testable on
// desktop hosts; it is only called by bootstrap_android.go. An empty base
// (bridge not yet ready during very early startup) falls back to the
// process temp dir, which Android points at the app's private cache dir.
func resolveAndroidDataDir(base string) (DataDir, error) {
	if base == "" {
		base = androidTempDir()
	}
	dir := filepath.Join(base, "uniTerm")
	if err := mkdirAllAndroid(dir); err != nil {
		return DataDir{}, err
	}
	return DataDir{Path: dir, Type: "default"}, nil
}
