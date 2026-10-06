package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// newFlowService builds a SyncService wired to a bare remote with a data
// dir and a known encryption key, ready to exercise full Sync() flows.
func newFlowService(t *testing.T) (*SyncService, []byte) {
	t.Helper()
	s, _ := initLocalRepoWithBareRemote(t)
	s.dataDir = t.TempDir()
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
	if err := EncryptConfigFiles(src, dirB, key, nil, nil); err != nil {
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
	if err := EncryptConfigFiles(s.dataDir, s.repoPath, key, nil, nil); err != nil {
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
