package sync

// syncedFiles is the single source of truth for the config files that
// participate in cloud sync. Encrypt (crypto.go), decrypt (crypto.go),
// content comparison (sync_service.go), the git commit whitelist (git.go),
// the empty-dir probe (isConfigDirEmpty) and the password-rotation file list
// (ChangePassword) all iterate this slice so their scopes can never drift
// apart again.
//
// ai-sessions.json and skills.json are intentionally absent — they are
// local-only data and must never be committed to the sync repo (a stray
// ai-sessions.json left in the repo dir from an old build stays untracked).
//
// settings.json is intentionally absent: it is device-local (theme,
// paths, shells, keybindings, UI state) and changes too often to sync
// well. The syncable AI slice (model catalog + maxTurns) lives in
// ai.json instead; see AIConfigStore.
var syncedFiles = []string{
	"connections.json",
	"favorites.json",
	"ai.json",
	"quickCommands.json",
	"tunnels.json",
	"identities.json",
	"proxies.json",
}

// settingsJSON is the device-leaning settings file (theme, shells, paths,
// keybindings, SFTP bookmarks). It is syncable but opt-in: the scope picker
// persists an explicit list once the user touches it, and a nil scope keeps
// the legacy syncedFiles default so upgrading devices see no behavior change.
const settingsJSON = "settings.json"

// syncableFiles is the full menu offered by the sync-scope picker: the
// always-on legacy set plus the opt-in settings.json.
var syncableFiles = append(append([]string{}, syncedFiles...), settingsJSON)

// EffectiveSyncFiles resolves the user's scope against syncableFiles.
// A nil/empty scope returns a copy of the legacy syncedFiles set.
// Unknown names are dropped so a stale scope list from an older or newer
// build can never smuggle an unintended file into the repo.
func EffectiveSyncFiles(scope []string) []string {
	if len(scope) == 0 {
		return append([]string{}, syncedFiles...)
	}
	valid := make(map[string]bool, len(syncableFiles))
	for _, name := range syncableFiles {
		valid[name] = true
	}
	out := make([]string, 0, len(scope))
	for _, name := range scope {
		if valid[name] {
			delete(valid, name)
			out = append(out, name)
		}
	}
	return out
}
