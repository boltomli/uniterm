package main

// Regression tests for the file_grants coverage extension (security.md,
// "file_grants protects 8 bindings while many others offer the same or more
// power"): every path-/program-taking binding now requires either a
// user-selected grant or a session-scoped navigation root, and the external
// editor command is resolved Go-side from persisted settings instead of
// being exec'd straight from a webview argument.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
	"github.com/ys-ll/uniterm/backend/store"
)

func TestOpenPathInExplorerRequiresGrant(t *testing.T) {
	a := &App{}
	ungranted := filepath.Join(t.TempDir(), "target.desktop")
	if err := a.OpenPathInExplorer(ungranted); err == nil {
		t.Fatal("OpenPathInExplorer accepted an ungranted path")
	}
}

func TestSetDefaultSessionLogDirRequiresGrant(t *testing.T) {
	a := &App{}
	dir := t.TempDir()
	if err := a.SetDefaultSessionLogDir(dir); err == nil {
		t.Fatal("SetDefaultSessionLogDir accepted an ungranted directory")
	}
	if got := a.GetDefaultSessionLogDir(); got == dir {
		t.Fatalf("rejected directory was still applied: %s", got)
	}
	fileGrants.addDir(dir)
	if err := a.SetDefaultSessionLogDir(dir); err != nil {
		t.Fatalf("SetDefaultSessionLogDir(granted): %v", err)
	}
	if got := a.GetDefaultSessionLogDir(); got != dir {
		t.Fatalf("granted directory not applied: %s", got)
	}
	// Empty clears the override.
	if err := a.SetDefaultSessionLogDir(""); err != nil {
		t.Fatalf("SetDefaultSessionLogDir(\"\"): %v", err)
	}
}

func TestEnableSessionOutputLogRejectsUngrantedDir(t *testing.T) {
	// The dir argument is checked before any panel/log state is touched, so
	// a bare App suffices.
	app := &App{}
	if _, err := app.EnableSessionOutputLog("panel-1", filepath.Join(t.TempDir(), "logs")); err == nil {
		t.Fatal("EnableSessionOutputLog accepted an ungranted directory")
	}
}

func TestReadLocalAttachmentFileRequiresGrant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := &App{}
	if _, err := a.ReadLocalAttachmentFile(path); err == nil {
		t.Fatal("ReadLocalAttachmentFile accepted an ungranted path")
	}
	// The drag-drop/dialog flow grants the file first — reading then works.
	fileGrants.addFile(path)
	got, err := a.ReadLocalAttachmentFile(path)
	if err != nil {
		t.Fatalf("ReadLocalAttachmentFile(granted): %v", err)
	}
	if got.Name != "note.txt" || got.Size != 5 {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestImportSkillRequiresGrant(t *testing.T) {
	a := &App{}
	dir := t.TempDir()
	if _, err := a.ImportSkillFromDir(dir); err == nil {
		t.Fatal("ImportSkillFromDir accepted an ungranted directory")
	}
	if _, err := a.ImportSkillFromZip(filepath.Join(dir, "skill.zip")); err == nil {
		t.Fatal("ImportSkillFromZip accepted an ungranted path")
	}
}

func TestKubeconfigPathRequiresGrantOrStoredConnection(t *testing.T) {
	dir := t.TempDir()
	kube := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kube, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newRedactionTestApp(t)
	seedConnections(t, a, []session.ConnectionConfig{
		{ID: "c-k8s", Name: "k8s", Type: "k8s", AuthType: "password", K8sConfigPath: kube, K8sContext: "ctx"},
		{ID: "c-other", Name: "ssh", Type: "ssh", Host: "h", Port: 22, AuthType: "password"},
	})

	// A kubeconfig path that is neither dialog-granted nor stored on a k8s
	// connection is refused before any read happens.
	other := filepath.Join(dir, "not-yours")
	if _, err := a.K8sListContexts(other, true); err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("K8sListContexts(ungranted path) = %v; want not-granted error", err)
	}
	if _, err := a.K8sConnect(other, true, "", "", "", ""); err == nil {
		t.Fatal("K8sConnect accepted an ungranted kubeconfig path")
	}
	// The path persisted on a saved k8s connection is trusted (same model as
	// the zmodem / session-log dirs): the grant is applied Go-side.
	if err := a.grantKubeconfigPath(kube); err != nil {
		t.Fatalf("grantKubeconfigPath(stored path): %v", err)
	}
	if err := requireFileGrant(kube); err != nil {
		t.Fatalf("stored kubeconfig path not granted: %v", err)
	}
	// A path stored on a NON-k8s connection must not grant anything.
	sshPath := filepath.Join(dir, "ssh-file")
	if err := a.grantKubeconfigPath(sshPath); err == nil {
		t.Fatal("grantKubeconfigPath accepted a path stored on a non-k8s connection")
	}
}

func TestSftpLocalScope(t *testing.T) {
	a := &App{}
	home := t.TempDir()
	secret := filepath.Join(t.TempDir(), "id_rsa")

	// No navigation yet: everything is refused.
	if err := a.requireSftpLocalPath("s1", filepath.Join(home, "file.txt")); err == nil {
		t.Fatal("requireSftpLocalPath accepted a path before any navigation")
	}
	a.noteSftpLocalDir("s1", home)

	inside := filepath.Join(home, "sub", "file.txt")
	if err := a.requireSftpLocalPath("s1", inside); err != nil {
		t.Fatalf("browsed-root path rejected: %v", err)
	}
	if err := a.requireSftpLocalPath("s1", secret); err == nil {
		t.Fatal("path outside the browsed roots accepted")
	}
	if err := a.requireSftpLocalPath("s1", filepath.Join(home, "..", "escape")); err == nil {
		t.Fatal("dot-dot traversal accepted")
	}
	// Roots are per session: another session has no roots yet.
	if err := a.requireSftpLocalPath("s2", inside); err == nil {
		t.Fatal("another session reused the roots of s1")
	}
	// Closing the session drops its roots.
	a.forgetSftpLocalRoots("s1")
	if err := a.requireSftpLocalPath("s1", inside); err == nil {
		t.Fatal("roots survived the session close")
	}
}

func TestOpenWithSystemLocalRequiresGrantOrScope(t *testing.T) {
	a := &App{}
	target := filepath.Join(t.TempDir(), "app.command")
	if err := a.OpenWithSystemLocal(target); err == nil {
		t.Fatal("OpenWithSystemLocal accepted an ungranted path")
	}
	// The local pane's own navigation is a valid trust source.
	a.noteSftpLocalDir("s1", filepath.Dir(target))
	if err := a.requireLocalPanePath(target); err != nil {
		t.Fatalf("browsed-pane path rejected: %v", err)
	}
}

func TestExternalEditorResolvedFromPersistedSettings(t *testing.T) {
	dir := t.TempDir()
	ls := store.NewLocalStateStore(dir)
	a := &App{dataDir: dir, localStateStore: ls}

	target := filepath.Join(t.TempDir(), "remote-copy.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.noteSftpLocalDir("s1", filepath.Dir(target))

	// No persisted editor: the webview-supplied command must be IGNORED —
	// no exec happens, the call fails with "not configured".
	if err := a.OpenExternalEditorLocal(target, "calc.exe"); err == nil || !strings.Contains(err.Error(), "no external editor configured") {
		t.Fatalf("OpenExternalEditorLocal with JS-supplied cmd = %v; want not-configured error", err)
	}

	// A persisted editor with a path separator is rejected: the setting can
	// only launch a bare program name found on PATH.
	state, err := ls.Load()
	if err != nil {
		t.Fatalf("Load local state: %v", err)
	}
	state.ExternalEditor = filepath.Join("evil", "payload")
	if err := ls.Save(state); err != nil {
		t.Fatalf("Save local state: %v", err)
	}
	if _, err := a.externalEditorCommand(); err == nil {
		t.Fatal("externalEditorCommand accepted a path-ful program")
	}

	// A bare program name with flags (the documented "code -w" shape) is
	// accepted — the binding then appends the file path itself.
	state.ExternalEditor = "code -w"
	if err := ls.Save(state); err != nil {
		t.Fatalf("Save local state: %v", err)
	}
	cmd, err := a.externalEditorCommand()
	if err != nil || cmd != "code -w" {
		t.Fatalf("externalEditorCommand = %q, %v", cmd, err)
	}
}
