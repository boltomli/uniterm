package sync

// Tests for the sync service: the Sync() flow over a bare git remote, the
// per-device sync scope, the content comparison the flow is built on, and the
// wrong-key / password-rotation guards. Helpers that stand a service up
// against a temporary bare remote come first.

import (
	"encoding/json"
	"fmt"
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

// initLocalRepoWithBareRemote stands up a normal git repo at repoPath, plus a
// bare repo at remotePath used as the "origin". Returns a SyncService whose
// config.RepoURL points at the bare repo via file:// so CloneOrOpen succeeds
// without touching the network and Push has somewhere to land.
func initLocalRepoWithBareRemote(t *testing.T) (*SyncService, string) {
	t.Helper()
	keyring.MockInit()

	configDir := t.TempDir()
	repoPath := filepath.Join(configDir, "sync-repo")
	remotePath := filepath.Join(configDir, "remote.git")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.MkdirAll(remotePath, 0755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}

	bare, err := git.PlainInit(remotePath, true)
	if err != nil {
		t.Fatalf("init bare: %v", err)
	}
	// Align both HEADs with config.Branch ("main") — PlainInit defaults to
	// master, which would desync every branch reference in the flow tests.
	if err := bare.Storer.SetReference(
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main")),
	); err != nil {
		t.Fatalf("set bare HEAD: %v", err)
	}

	work, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatalf("init work: %v", err)
	}
	if err := work.Storer.SetReference(
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main")),
	); err != nil {
		t.Fatalf("set work HEAD: %v", err)
	}
	if _, err := work.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{remotePath},
		Fetch: []config.RefSpec{
			config.RefSpec("+refs/heads/*:refs/remotes/origin/*"),
		},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}

	s := &SyncService{
		configDir:   configDir,
		repoPath:    repoPath,
		keychain:    NewKeychain(),
		configStore: NewSyncConfigStore(configDir),
		ready:       make(chan struct{}),
	}
	close(s.ready)
	cfg := SyncConfig{
		RepoURL:  remotePath,
		Branch:   "main",
		Username: "u",
	}
	if err := s.configStore.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return s, repoPath
}

// newFlowService builds a SyncService wired to a bare remote with a data
// dir and a known encryption key, ready to exercise full Sync() flows.
func newFlowService(t *testing.T) (*SyncService, []byte) {
	t.Helper()
	s, _ := initLocalRepoWithBareRemote(t)
	// dataDir is overwritten AFTER initLocalRepoWithBareRemote, so the
	// fake stores must be wired here — after the overwrite — to point at
	// the dir the flow tests actually write connections.json into.
	s.dataDir = t.TempDir()
	stores := make([]SyncableStore, 0, len(syncableFiles))
	for _, name := range syncableFiles {
		stores = append(stores, &snapshotBlobStore{name: name, dir: s.dataDir})
	}
	s.SetStores(stores)
	key := DeriveKey("flow-pass", []byte("0123456789abcdef"))
	if err := s.keychain.StoreEncryptionKey(key); err != nil {
		t.Fatalf("store key: %v", err)
	}
	return s, key
}

func writeConnections(t *testing.T, dir, id string) {
	t.Helper()
	// Shape mirrors the production ConnectionStoreData wrapper — the
	// upload normalizer re-marshals through a {groups, connections}
	// struct, so the file must carry both keys to round-trip byte-stable.
	if err := os.WriteFile(filepath.Join(dir, "connections.json"),
		[]byte(`{"groups":[],"connections":[{"id":"`+id+`","name":"n"}]}`), 0600); err != nil {
		t.Fatalf("write connections: %v", err)
	}
}

func readConnectionsID(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "connections.json"))
	if err != nil {
		t.Fatalf("read connections: %v", err)
	}
	var wrapper struct {
		Connections []struct {
			ID string `json:"id"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatalf("parse connections: %v", err)
	}
	if len(wrapper.Connections) == 0 {
		return ""
	}
	return wrapper.Connections[0].ID
}

// pushRemoteContent simulates ANOTHER machine: clones the remote, commits
// the given content and pushes. Returns the pushed head hash.
func pushRemoteContent(t *testing.T, s *SyncService, key []byte, id string) plumbing.Hash {
	t.Helper()
	remotePath := filepath.Join(s.configDir, "remote.git")
	dirB := t.TempDir()
	repoB, err := git.PlainClone(dirB, false, &git.CloneOptions{URL: remotePath})
	if err != nil {
		t.Fatalf("device-B clone: %v", err)
	}
	src := t.TempDir()
	writeConnections(t, src, id)
	if err := EncryptConfigFiles(src, dirB, key); err != nil {
		t.Fatalf("device-B encrypt: %v", err)
	}
	gb := &GitRepo{repo: repoB, repoPath: dirB}
	if _, err := gb.StageAndCommit("device B"); err != nil {
		t.Fatalf("device-B commit: %v", err)
	}
	if err := repoB.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("device-B push: %v", err)
	}
	head, err := repoB.Head()
	if err != nil {
		t.Fatalf("device-B head: %v", err)
	}
	return head.Hash()
}

// commitLocalWithoutPush simulates a local commit that never reached the
// remote (e.g. push failed on the previous sync).
func commitLocalWithoutPush(t *testing.T, s *SyncService, key []byte) {
	t.Helper()
	if err := EncryptConfigFiles(s.dataDir, s.repoPath, key); err != nil {
		t.Fatalf("local encrypt: %v", err)
	}
	repoO, err := git.PlainOpen(s.repoPath)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	g := &GitRepo{repo: repoO, repoPath: s.repoPath}
	if _, err := g.StageAndCommit("local unpushed"); err != nil {
		t.Fatalf("local commit: %v", err)
	}
}

// TestSync_SilentPullWhenRemoteAdvanced is the issue-#745 regression: the
// other machine pushed a change, this machine changed nothing. Sync must
// silently pull — never surface the conflict dialog.
func TestSync_SilentPullWhenRemoteAdvanced(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	res, err := s.Sync()
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if res.Direction != SyncPush {
		t.Fatalf("initial sync direction = %d, want SyncPush", res.Direction)
	}

	pushRemoteContent(t, s, key, "c2")

	res, err = s.Sync()
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if res.Direction != SyncPull {
		t.Fatalf("direction = %d, want SyncPull (silent) — conflict dialog on unchanged machine", res.Direction)
	}
	if got := readConnectionsID(t, s.dataDir); got != "c2" {
		t.Fatalf("after pull connections id = %q, want c2", got)
	}
}

// TestSync_ConflictOnlyWhenBothChanged: remote advanced AND local content
// changed independently since the merge base — only then is the conflict
// dialog legitimate.
func TestSync_ConflictOnlyWhenBothChanged(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	pushRemoteContent(t, s, key, "c2")
	writeConnections(t, s.dataDir, "c3") // local diverges too

	res, err := s.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Direction != SyncConflict {
		t.Fatalf("direction = %d, want SyncConflict", res.Direction)
	}
}

// TestResolveConflict_LocalKeepsHistoryFastForwardable is the anti-ping-pong
// regression: resolving with the local side must land a normal commit on
// top of the remote head (fast-forward), NOT a force push. Force-pushing
// orphaned the other machine's head so it conflicted again on its next
// open, forever.
func TestResolveConflict_LocalKeepsHistoryFastForwardable(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	remoteHead := pushRemoteContent(t, s, key, "c2")
	writeConnections(t, s.dataDir, "c3")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("sync (expect conflict): %v", err)
	}

	if _, err := s.ResolveConflict(true); err != nil {
		t.Fatalf("resolve conflict (use local): %v", err)
	}

	// The new remote head must have the previous remote head as its parent —
	// i.e. history was NOT rewritten.
	bare, err := git.PlainOpen(filepath.Join(s.configDir, "remote.git"))
	if err != nil {
		t.Fatalf("open bare remote: %v", err)
	}
	ref, err := bare.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("remote ref: %v", err)
	}
	commit, err := bare.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("remote head commit: %v", err)
	}
	parents := commit.ParentHashes
	if len(parents) != 1 || parents[0] != remoteHead {
		t.Fatalf("remote history rewritten: head parents = %v, want parent == previous remote head %s — force-push ping-pong regression", parents, remoteHead)
	}

	// And the very next sync must be a quiet no-op, not another conflict.
	writeConnections(t, s.dataDir, "c3")
	res, err := s.Sync()
	if err != nil {
		t.Fatalf("sync after resolve: %v", err)
	}
	if res.Direction != SyncNone {
		t.Fatalf("direction after resolve = %d, want SyncNone", res.Direction)
	}
}

// TestSync_HealsDivergedHeadsWithSameContent: both heads moved but the
// decrypted content is identical (e.g. re-encrypt noise) — Sync must heal
// the history silently instead of showing the conflict dialog.
func TestSync_HealsDivergedHeadsWithSameContent(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Local side: commit a re-encryption of the SAME content (new IV,
	// different ciphertext, identical plaintext).
	commitLocalWithoutPush(t, s, key)
	// Remote side: another machine re-encrypts the same content too.
	pushRemoteContent(t, s, key, "c1")

	res, err := s.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Direction != SyncNone {
		t.Fatalf("direction = %d, want SyncNone (healed, no conflict)", res.Direction)
	}
}

// TestSync_RecoversFromUnpushedLocalCommit: a local commit that failed to
// push on a previous sync must not wedge the next sync — the changed
// content is re-anchored on the remote head and pushed.
func TestSync_RecoversFromUnpushedLocalCommit(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	writeConnections(t, s.dataDir, "c2")
	commitLocalWithoutPush(t, s, key)

	res, err := s.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Direction != SyncPush {
		t.Fatalf("direction = %d, want SyncPush", res.Direction)
	}
	if got := readConnectionsID(t, s.dataDir); got != "c2" {
		t.Fatalf("local data corrupted by recovery: id = %q, want c2", got)
	}
}

// remoteDepth walks first-parent depth on the test bare remote.
func remoteDepth(t *testing.T, s *SyncService) int {
	t.Helper()
	bare, err := git.PlainOpen(filepath.Join(s.configDir, "remote.git"))
	if err != nil {
		t.Fatalf("open bare remote: %v", err)
	}
	ref, err := bare.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("remote ref: %v", err)
	}
	depth := 0
	h := ref.Hash()
	for {
		c, err := bare.CommitObject(h)
		if err != nil {
			t.Fatalf("remote walk: %v", err)
		}
		depth++
		if len(c.ParentHashes) == 0 {
			break
		}
		h = c.ParentHashes[0]
	}
	return depth
}

// TestCompaction_TriggersAtThreshold: once the first-parent depth reaches
// compactionThreshold, the next successful push rewrites the remote down to
// compactionKeep commits and rebuilds the local clone.
func TestCompaction_TriggersAtThreshold(t *testing.T) {
	s, _ := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Grow history to the threshold through real pushes. A single Sync
	// squashes unpushed local commits onto the remote head (they were
	// never shared), so depth can only build via successful syncs; the
	// push that reaches compactionThreshold triggers the rewrite itself.
	for i := 0; i < compactionThreshold-1; i++ {
		writeConnections(t, s.dataDir, fmt.Sprintf("c%d", i))
		if _, err := s.Sync(); err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
	}
	if _, err := s.Sync(); err != nil {
		t.Fatalf("sync at threshold: %v", err)
	}

	if got := remoteDepth(t, s); got != compactionKeep {
		t.Fatalf("remote depth after compaction = %d, want %d", got, compactionKeep)
	}
	// The clone was rebuilt and is a valid repo again.
	if _, err := git.PlainOpen(s.repoPath); err != nil {
		t.Fatalf("re-clone: %v", err)
	}
	// Local data untouched by compaction.
	if got := readConnectionsID(t, s.dataDir); got != fmt.Sprintf("c%d", compactionThreshold-2) {
		t.Fatalf("local data changed by compaction: id = %q", got)
	}
	// The next sync is a quiet no-op.
	res, err := s.Sync()
	if err != nil {
		t.Fatalf("post-compaction sync: %v", err)
	}
	if res.Direction != SyncNone {
		t.Fatalf("post-compaction sync direction = %d, want SyncNone", res.Direction)
	}
}

// TestCompaction_SkipsBelowThreshold: below the threshold a sync must not
// rewrite anything.
func TestCompaction_SkipsBelowThreshold(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	before := remoteDepth(t, s)

	pushRemoteContent(t, s, key, "c2")
	res, err := s.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Direction != SyncPull {
		t.Fatalf("direction = %d, want SyncPull", res.Direction)
	}
	if got := remoteDepth(t, s); got != before+1 {
		t.Fatalf("remote depth = %d, want %d (history must be untouched below threshold)", got, before+1)
	}
}

// rewriteRemoteHistory simulates ANOTHER machine compacting history: clones
// the remote, rewrites the chain to the most recent `keep` commits and
// force-pushes. Remote hashes change wholesale; content is untouched.
func rewriteRemoteHistory(t *testing.T, s *SyncService, keep int) {
	t.Helper()
	remotePath := filepath.Join(s.configDir, "remote.git")
	dirB := t.TempDir()
	repoB, err := git.PlainClone(dirB, false, &git.CloneOptions{URL: remotePath})
	if err != nil {
		t.Fatalf("device-B clone: %v", err)
	}
	gb := &GitRepo{repo: repoB, repoPath: dirB}
	if err := gb.RewriteHistory(keep); err != nil {
		t.Fatalf("device-B rewrite: %v", err)
	}
	if err := gb.PushForce("", ""); err != nil {
		t.Fatalf("device-B force push: %v", err)
	}
}

// TestSync_NilBaseSameContentHeals: after another device force-pushes a
// rewritten chain with identical content, this device must silently
// re-anchor — no conflict dialog.
func TestSync_NilBaseSameContentHeals(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Remote grows to depth 3, then is rewritten to a single parent-less
	// commit — new hashes, same c2 content.
	pushRemoteContent(t, s, key, "c2")
	pushRemoteContent(t, s, key, "c2")
	rewriteRemoteHistory(t, s, 1)

	// Local content matches the rewritten remote content.
	writeConnections(t, s.dataDir, "c2")

	res, err := s.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Direction != SyncNone {
		t.Fatalf("direction = %d, want SyncNone (silent re-anchor on nil merge base)", res.Direction)
	}
	if got := readConnectionsID(t, s.dataDir); got != "c2" {
		t.Fatalf("data changed by re-anchor: id = %q, want c2", got)
	}

	// The re-anchor produced a shared, fast-forwardable chain: a new local
	// edit pushes normally instead of diverging.
	writeConnections(t, s.dataDir, "c4")
	res, err = s.Sync()
	if err != nil {
		t.Fatalf("sync after re-anchor: %v", err)
	}
	if res.Direction != SyncPush {
		t.Fatalf("direction after re-anchor = %d, want SyncPush", res.Direction)
	}
}

// TestSync_NilBaseDifferentContentConflicts: rewritten remote with
// different content surfaces the conflict dialog, and resolving with
// local converges to a quiet, sharing-history state.
func TestSync_NilBaseDifferentContentConflicts(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	pushRemoteContent(t, s, key, "c2")
	pushRemoteContent(t, s, key, "c2")
	rewriteRemoteHistory(t, s, 1)

	res, err := s.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Direction != SyncConflict {
		t.Fatalf("direction = %d, want SyncConflict on nil merge base with differing content", res.Direction)
	}
	if res.Conflict == nil {
		t.Fatal("conflict info missing")
	}

	// Resolve with local — converges and the next sync is quiet.
	writeConnections(t, s.dataDir, "c3")
	if _, err := s.ResolveConflict(true); err != nil {
		t.Fatalf("resolve (local): %v", err)
	}
	res, err = s.Sync()
	if err != nil {
		t.Fatalf("sync after resolve: %v", err)
	}
	if res.Direction != SyncNone {
		t.Fatalf("direction after resolve = %d, want SyncNone", res.Direction)
	}
}

// TestSync_NilBaseConflictResolvedWithRemote: the pull direction of a
// nil-base conflict also converges.
func TestSync_NilBaseConflictResolvedWithRemote(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	pushRemoteContent(t, s, key, "c2")
	pushRemoteContent(t, s, key, "c2")
	rewriteRemoteHistory(t, s, 1)

	if _, err := s.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := s.ResolveConflict(false); err != nil {
		t.Fatalf("resolve (remote): %v", err)
	}
	if got := readConnectionsID(t, s.dataDir); got != "c2" {
		t.Fatalf("after resolve (remote) id = %q, want c2", got)
	}
	res, err := s.Sync()
	if err != nil {
		t.Fatalf("sync after resolve: %v", err)
	}
	if res.Direction != SyncNone {
		t.Fatalf("direction after resolve = %d, want SyncNone", res.Direction)
	}
}

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
	// Wire the file-backed fake stores over dataDir — mirrors app.go's
	// future assembly structurally: sync holds SyncableStore interface
	// values only (sync → store would cycle through credentials), so the
	// fake lives on the consumer side. A store whose blob is not in the
	// scope is skipped by the scoped encrypt/decrypt, and one whose file
	// was never written reports fs.ErrNotExist from ExportPlain, so tests
	// that never touch a file see no behavior change.
	stores := make([]SyncableStore, 0, len(syncableFiles))
	for _, name := range syncableFiles {
		stores = append(stores, &snapshotBlobStore{name: name, dir: dataDir})
	}
	s.SetStores(stores)
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
	if len(got) != len(defaultSyncedFiles) {
		t.Fatalf("default scope = %v, want legacy %v", got, defaultSyncedFiles)
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

func TestCompareConfigDirsScopedIgnoresOutOfScope(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "connections.json"), []byte(`{"v":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "connections.json"), []byte(`{"v":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	// settings.json differs but is out of scope.
	if err := os.WriteFile(filepath.Join(a, "settings.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "settings.json"), []byte(`{"theme":"light"}`), 0600); err != nil {
		t.Fatal(err)
	}
	same, err := compareConfigDirs([]string{"connections.json"}, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("out-of-scope difference must be ignored")
	}
	same, err = compareConfigDirs([]string{"connections.json", "settings.json"}, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Fatal("in-scope difference must be detected")
	}
}

func TestIsConfigDirEmptyScoped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// settings.json not in scope → dir counts as empty.
	if !isConfigDirEmpty([]string{"connections.json"}, dir) {
		t.Fatal("expected empty when settings.json is out of scope")
	}
	if isConfigDirEmpty(syncableFiles, dir) {
		t.Fatal("expected non-empty when settings.json is in scope")
	}
}

// Regression tests: favorites.json is a top-level JSON array (unlike every
// other synced file, which is an object). The comparison and empty-dir probe
// paths must not assume every synced file unmarshals into a map.

func writeFileSync(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestCompareConfigFilesHandlesArrayFiles verifies array-shaped synced files
// (favorites.json) compare correctly: identical content is equal regardless of
// key-order/whitespace, different content is a difference, and a missing file
// equals an explicitly empty one (both mean "no data").
func TestCompareConfigFilesHandlesArrayFiles(t *testing.T) {
	base := t.TempDir()
	local := filepath.Join(base, "local")
	remote := filepath.Join(base, "remote")
	for _, d := range []string{local, remote} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	name := filepath.Join("favorites.json")

	t.Run("identical arrays are equal", func(t *testing.T) {
		writeFileSync(t, local, "favorites.json", `["a","b"]`)
		writeFileSync(t, remote, "favorites.json", "[\n  \"a\",\n  \"b\"\n]")
		same, err := compareConfigFiles(filepath.Join(local, name), filepath.Join(remote, name))
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		if !same {
			t.Fatal("identical favorites arrays reported as different")
		}
	})

	t.Run("different arrays differ and order matters", func(t *testing.T) {
		writeFileSync(t, local, "favorites.json", `["a","b"]`)
		writeFileSync(t, remote, "favorites.json", `["b","a"]`)
		same, err := compareConfigFiles(filepath.Join(local, name), filepath.Join(remote, name))
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		if same {
			t.Fatal("differently ordered favorites arrays reported as equal")
		}
	})

	t.Run("missing file equals empty array", func(t *testing.T) {
		if err := os.Remove(filepath.Join(local, "favorites.json")); err != nil {
			t.Fatalf("remove local: %v", err)
		}
		writeFileSync(t, remote, "favorites.json", `[]`)
		same, err := compareConfigFiles(filepath.Join(local, name), filepath.Join(remote, name))
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		if !same {
			t.Fatal("missing favorites.json vs empty array reported as different")
		}
	})
}

// TestIsConfigDirEmptyArrayFiles verifies the first-sync empty probe treats an
// empty favorites array as "no data" but a non-empty one as real data.
func TestIsConfigDirEmptyArrayFiles(t *testing.T) {
	dir := t.TempDir()

	writeFileSync(t, dir, "favorites.json", `[]`)
	if !isConfigDirEmpty(defaultSyncedFiles, dir) {
		t.Fatal("dir with only an empty favorites array should be empty")
	}

	writeFileSync(t, dir, "favorites.json", `["conn-1"]`)
	if isConfigDirEmpty(defaultSyncedFiles, dir) {
		t.Fatal("dir with a non-empty favorites array should not be empty")
	}
}

// TestCompareLocalWithRepo_WrongKeyReturnsError was folded into
// TestSync_WrongPasswordSetsPasswordMismatchStatus: the wrong-key abort is
// now the verifyDecryption guard inside Sync() itself.

// TestSync_WrongPasswordSetsPasswordMismatchStatus drives Sync() with an
// encKey that cannot open the existing ciphertext and asserts the on-disk
// status record reflects the password_mismatch outcome (not a generic
// "failed").
func TestSync_WrongPasswordSetsPasswordMismatchStatus(t *testing.T) {
	s, repoPath := initLocalRepoWithBareRemote(t)

	realKey := DeriveKey("correct-password", []byte("0123456789abcdef"))
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "connections.json"),
		[]byte(`{"connections":[]}`), 0600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	// Only connections.json lands in the repo: sync skips a store whose blob
	// was never written instead of uploading an empty document for it, so a
	// real repo routinely carries a subset of the synced files.
	if err := EncryptConfigFiles(srcDir, repoPath, realKey); err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Store the WRONG derived key in the keychain so Sync() loads the wrong
	// encKey and hits the compareLocalWithRepo decrypt-fail path.
	if err := s.keychain.StoreEncryptionKey(
		DeriveKey("WRONG-password", []byte("0123456789abcdef")),
	); err != nil {
		t.Fatalf("store wrong key: %v", err)
	}

	if _, err := s.Sync(); err == nil {
		t.Fatal("Sync() with wrong key returned nil error — expected password_mismatch")
	}

	loaded, err := s.configStore.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.LastSyncStatus != "password_mismatch" {
		t.Errorf("LastSyncStatus = %q, want %q", loaded.LastSyncStatus, "password_mismatch")
	}
}

// TestRepoHasFilesAcceptsPartialRepo: a repo legitimately carries only the
// blobs whose stores had data, so "does the repo hold ciphertext" must mean
// "any file present", not "all of them" — requiring all of them silently
// disables the wrong-key guard below for every real repo.
func TestRepoHasFilesAcceptsPartialRepo(t *testing.T) {
	partial := t.TempDir()
	writeSyncFile(t, partial, "connections.json", "ciphertext")
	if !repoHasFiles(partial) {
		t.Fatal("a repo carrying one blob must count as carrying files")
	}
	if repoHasFiles(t.TempDir()) {
		t.Fatal("an empty repo must not count as carrying files")
	}
}

// TestVerifyDecryptionCoversEveryPresentBlob: the wrong-key guard must
// inspect every blob the repo carries, not just connections.json — a repo
// whose only blob is, say, favorites.json used to verify clean with a wrong
// key and let sync proceed against ciphertext it cannot read.
func TestVerifyDecryptionCoversEveryPresentBlob(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	wrong := []byte("fedcba9876543210fedcba9876543210")
	src := t.TempDir()
	writeSyncFile(t, src, "favorites.json", `["a"]`)
	repo := t.TempDir()
	if err := EncryptConfigFiles(src, repo, key); err != nil {
		t.Fatal(err)
	}
	if err := verifyDecryption(repo, key); err != nil {
		t.Fatalf("correct key must verify: %v", err)
	}
	if err := verifyDecryption(repo, wrong); err == nil {
		t.Fatal("wrong key must fail verification even when the repo carries no connections.json")
	}
}

// TestChangePassword_RewritesSaltAndReencryptsFiles is the SYNC-P1-6
// regression guard: changing the master password must (a) replace the salt
// in .sync-salt so PBKDF2 work cannot amortize across old + new keys, and
// (b) re-encrypt every repo file so the new key decrypts them and the old
// key does not.
func TestChangePassword_RewritesSaltAndReencryptsFiles(t *testing.T) {
	s, repoPath := initLocalRepoWithBareRemote(t)

	oldSalt := []byte("0123456789abcdef")
	oldKey := DeriveKey("old-pass", oldSalt)

	srcDir := t.TempDir()
	seed := map[string]string{
		"connections.json":   `{"connections":[{"id":"c1","name":"n"}]}`,
		"ai.json":            `{"maxTurns":20}`,
		"quickCommands.json": `[{"name":"q1","cmd":"ls"}]`,
		"identities.json":    `{"identities":[{"id":"i1","name":"prod"}]}`,
		"proxies.json":       `{"proxies":[{"id":"p1","name":"vpn"}]}`,
	}
	for name, body := range seed {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(body), 0600); err != nil {
			t.Fatalf("write seed %s: %v", name, err)
		}
	}
	if err := EncryptConfigFiles(srcDir, repoPath, oldKey); err != nil {
		t.Fatalf("encrypt with oldKey: %v", err)
	}
	if err := WriteSaltFile(repoPath, oldSalt); err != nil {
		t.Fatalf("write old salt: %v", err)
	}

	preFiles := map[string][]byte{}
	for name := range seed {
		b, err := os.ReadFile(filepath.Join(repoPath, name))
		if err != nil {
			t.Fatalf("read pre %s: %v", name, err)
		}
		preFiles[name] = b
	}

	if err := s.ChangePassword("old-pass", "new-pass"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	gotSalt, err := ReadSaltFile(repoPath)
	if err != nil {
		t.Fatalf("read salt after rotation: %v", err)
	}
	if string(gotSalt) == string(oldSalt) {
		t.Fatal(".sync-salt unchanged after ChangePassword — SYNC-P1-6 regression (PBKDF2 work could amortize)")
	}

	// Every repo file must decrypt with the NEW key.
	newKey := DeriveKey("new-pass", gotSalt)
	dstDir := t.TempDir()
	if err := DecryptConfigFiles(repoPath, dstDir, newKey); err != nil {
		t.Fatalf("decrypt with newKey: %v", err)
	}
	for name, want := range seed {
		got, err := os.ReadFile(filepath.Join(dstDir, name))
		if err != nil {
			t.Fatalf("read decrypted %s: %v", name, err)
		}
		var gw, ww interface{}
		if err := json.Unmarshal(got, &gw); err != nil {
			t.Fatalf("parse decrypted %s: %v", name, err)
		}
		if err := json.Unmarshal([]byte(want), &ww); err != nil {
			t.Fatalf("parse seed %s: %v", name, err)
		}
		gj, _ := json.Marshal(gw)
		wj, _ := json.Marshal(ww)
		if string(gj) != string(wj) {
			t.Errorf("decrypted %s = %s, want %s", name, gj, wj)
		}
	}

	// And must NOT decrypt with the OLD key — proves rotation re-encrypted.
	repoFormat, fvErr := readFormatVersion(repoPath)
	if fvErr != nil {
		t.Fatalf("read format version: %v", fvErr)
	}
	for name := range seed {
		ct, err := os.ReadFile(filepath.Join(repoPath, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := decryptBytes(string(ct), oldKey, name, repoFormat); err == nil {
			t.Errorf("%s still decryptable with oldKey — ChangePassword did not re-encrypt", name)
		}
	}

	// Ciphertext bytes must actually differ from the pre-rotation snapshot.
	for name, pre := range preFiles {
		post, err := os.ReadFile(filepath.Join(repoPath, name))
		if err != nil {
			t.Fatalf("read post %s: %v", name, err)
		}
		if string(pre) == string(post) {
			t.Errorf("%s ciphertext unchanged after ChangePassword", name)
		}
	}
}

// TestSyncCommitOnlyCarriesChangedFiles pins the per-file sync granularity in
// git mode: after a one-file change the blobs whose content did not change
// keep their previous bytes in the new commit. The per-blob encryption nonce
// is random, so without the change-detection skip every push rewrote every
// file and the repo grew by the whole config on every sync.
func TestSyncCommitOnlyCarriesChangedFiles(t *testing.T) {
	s, key := newFlowService(t)

	writeConnections(t, s.dataDir, "c1")
	writeSyncFile(t, s.dataDir, "favorites.json", `["c1"]`)
	if _, err := s.Sync(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	repoO, err := git.PlainOpen(s.repoPath)
	if err != nil {
		t.Fatal(err)
	}
	g := &GitRepo{repo: repoO, repoPath: s.repoPath}

	headFiles := func(hash plumbing.Hash) map[string]string {
		t.Helper()
		dir := t.TempDir()
		if err := g.ExtractCommitFilesIn(hash, defaultSyncedFiles, dir); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		fv, fvErr := readFormatVersion(dir)
		if fvErr != nil {
			t.Fatalf("read format version: %v", fvErr)
		}
		for _, name := range defaultSyncedFiles {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue // blob not carried by that commit
			}
			plain, err := decryptBytes(string(data), key, name, fv)
			if err != nil {
				t.Fatalf("decrypt %s: %v", name, err)
			}
			out[name] = string(plain)
		}
		return out
	}

	heads, err := g.ResolveBranchHeads("main")
	if err != nil {
		t.Fatal(err)
	}
	before := headFiles(*heads.Local)

	writeConnections(t, s.dataDir, "c2")
	if _, err := s.Sync(); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	heads, err = g.ResolveBranchHeads("main")
	if err != nil {
		t.Fatal(err)
	}
	after := headFiles(*heads.Local)

	if after["connections.json"] == before["connections.json"] {
		t.Fatal("the changed file must have been re-published")
	}
	if after["favorites.json"] != before["favorites.json"] {
		t.Fatal("unchanged blob was rewritten in the new commit")
	}
}
