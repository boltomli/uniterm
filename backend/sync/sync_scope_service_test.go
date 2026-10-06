package sync

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/zalando/go-keyring"
)

// newScopeTestService builds a SyncService against temp dirs. It mirrors
// initLocalRepoWithBareRemote's struct construction: NewSyncService pins
// configDir to os.UserConfigDir()/uniTerm, so using it here would read and
// overwrite the developer's real sync-config.json on the host.
func newScopeTestService(t *testing.T, configDir, dataDir string) *SyncService {
	t.Helper()
	s := &SyncService{
		configDir:   configDir,
		dataDir:     dataDir,
		repoPath:    filepath.Join(configDir, "sync-repo"),
		keychain:    NewKeychain(),
		configStore: NewSyncConfigStore(configDir),
		ready:       make(chan struct{}),
	}
	close(s.ready)
	return s
}

func TestEffectiveFilesRespectsScope(t *testing.T) {
	s := newScopeTestService(t, t.TempDir(), t.TempDir())
	if err := s.configStore.Save(SyncConfig{Branch: "main", SyncScope: []string{"connections.json", "settings.json"}}); err != nil {
		t.Fatal(err)
	}
	got := s.effectiveFiles()
	want := []string{"connections.json", "settings.json"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("effectiveFiles = %v, want %v", got, want)
	}
}

func TestEffectiveFilesDefaultsToLegacy(t *testing.T) {
	s := newScopeTestService(t, t.TempDir(), filepath.Join(t.TempDir(), "data"))
	got := s.effectiveFiles()
	if len(got) != len(syncedFiles) {
		t.Fatalf("default scope = %v, want legacy %v", got, syncedFiles)
	}
}

// TestConfigureRepoPreservesSyncScope drives the real ConfigureRepo
// "existing repo, local empty → adopt remote" path against a local bare
// remote (no network) and asserts the post-configure config still carries
// the device-local SyncScope and AutoSync saved beforehand.
func TestConfigureRepoPreservesSyncScope(t *testing.T) {
	keyring.MockInit()

	// Bare remote holding one commit with .sync-salt, so ConfigureRepo
	// takes the existing-repo (salt found) branch after cloning.
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	seedPath := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(seedPath, 0755); err != nil {
		t.Fatalf("mkdir seed: %v", err)
	}
	seed, err := git.PlainInit(seedPath, false)
	if err != nil {
		t.Fatalf("init seed: %v", err)
	}
	if err := seed.Storer.SetReference(
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main")),
	); err != nil {
		t.Fatalf("set seed HEAD: %v", err)
	}
	if err := WriteSaltFile(seedPath, []byte("0123456789abcdef")); err != nil {
		t.Fatalf("write salt: %v", err)
	}
	seedWt, err := seed.Worktree()
	if err != nil {
		t.Fatalf("seed worktree: %v", err)
	}
	if _, err := seedWt.Add(".sync-salt"); err != nil {
		t.Fatalf("add salt: %v", err)
	}
	if _, err := seedWt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "t@t"},
	}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	bare, err := git.PlainInit(remotePath, true)
	if err != nil {
		t.Fatalf("init bare: %v", err)
	}
	if err := bare.Storer.SetReference(
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main")),
	); err != nil {
		t.Fatalf("set bare HEAD: %v", err)
	}
	if _, err := seed.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{remotePath},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}
	if err := seed.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	os.RemoveAll(seedPath)

	// Device-local state a real device would already have on disk.
	configDir := t.TempDir()
	dataDir := t.TempDir()
	s := newScopeTestService(t, configDir, dataDir)
	scope := []string{"connections.json", "settings.json"}
	if err := s.configStore.Save(SyncConfig{
		Branch:    "main",
		AutoSync:  true,
		SyncScope: scope,
	}); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	res, err := s.ConfigureRepo(remotePath, "u", "", "test-password")
	if err != nil {
		t.Fatalf("ConfigureRepo: %v", err)
	}
	if res.Direction != SyncPull {
		t.Fatalf("direction = %v, want SyncPull (local-empty adopt path)", res.Direction)
	}

	cfg, err := s.configStore.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RepoURL != remotePath {
		t.Errorf("RepoURL = %q, want %q", cfg.RepoURL, remotePath)
	}
	if !reflect.DeepEqual(cfg.SyncScope, scope) {
		t.Errorf("SyncScope = %v, want preserved %v", cfg.SyncScope, scope)
	}
	if !cfg.AutoSync {
		t.Errorf("AutoSync = false, want preserved true")
	}
}
