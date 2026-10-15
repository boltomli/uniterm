package sync

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// newTestRepo builds an empty repo on branch main.
func newTestRepo(t *testing.T) *GitRepo {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := repo.Storer.SetReference(
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main")),
	); err != nil {
		t.Fatalf("set HEAD: %v", err)
	}
	return &GitRepo{repo: repo, repoPath: dir}
}

// commitFile writes connections.json with marker i and commits it via
// StageAndCommit (whitelist staging, same as production).
func commitFile(t *testing.T, g *GitRepo, i int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(g.repoPath, "connections.json"),
		[]byte(fmt.Sprintf(`{"n":%d}`, i)), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ok, err := g.StageAndCommit(fmt.Sprintf("c%d", i))
	if err != nil || !ok {
		t.Fatalf("commit %d: ok=%v err=%v", i, ok, err)
	}
}

func TestCommitDepth_CountsFirstParent(t *testing.T) {
	g := newTestRepo(t)
	depth, err := g.CommitDepth()
	if err != nil {
		t.Fatalf("depth on empty repo: %v", err)
	}
	if depth != 0 {
		t.Fatalf("empty repo depth = %d, want 0", depth)
	}
	for i := 0; i < 5; i++ {
		commitFile(t, g, i)
	}
	depth, err = g.CommitDepth()
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != 5 {
		t.Fatalf("depth = %d, want 5", depth)
	}
}

func TestCommitDepth_CapsScanAtSixty(t *testing.T) {
	g := newTestRepo(t)
	for i := 0; i < 65; i++ {
		commitFile(t, g, i)
	}
	depth, err := g.CommitDepth()
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != 60 {
		t.Fatalf("depth = %d, want capped 60", depth)
	}
}

func TestCommitDepth_IgnoresSideBranchOnMerge(t *testing.T) {
	g := newTestRepo(t)
	commitFile(t, g, 0)
	mainRef, err := g.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("ref c0: %v", err)
	}
	c0Hash := mainRef.Hash()
	commitFile(t, g, 1)
	mainRef, err = g.repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("ref c1: %v", err)
	}
	c1Hash := mainRef.Hash()

	// Side branch from c0 with two extra commits.
	if err := g.repo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("side"), c0Hash),
	); err != nil {
		t.Fatalf("create side branch: %v", err)
	}
	wt, err := g.repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("side")}); err != nil {
		t.Fatalf("checkout side: %v", err)
	}
	commitFile(t, g, 100)
	commitFile(t, g, 101)
	sideRef, err := g.repo.Reference(plumbing.NewBranchReferenceName("side"), true)
	if err != nil {
		t.Fatalf("ref side: %v", err)
	}
	sideHash := sideRef.Hash()

	// Back on main, merge side by constructing a merge commit directly:
	// first parent is main (c1), second parent is the side tip. The tree is
	// reused from c1 — the walk only cares about parent hashes.
	if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	c1Commit, err := g.repo.CommitObject(c1Hash)
	if err != nil {
		t.Fatalf("c1 object: %v", err)
	}
	when := time.Now()
	mergeCommit := &object.Commit{
		Author:    object.Signature{Name: "t", Email: "t@example.com", When: when},
		Committer: object.Signature{Name: "t", Email: "t@example.com", When: when},
		Message:      "merge side into main",
		TreeHash:     c1Commit.TreeHash,
		ParentHashes: []plumbing.Hash{c1Hash, sideHash},
	}
	enc := g.repo.Storer.NewEncodedObject()
	if err := mergeCommit.Encode(enc); err != nil {
		t.Fatalf("encode merge commit: %v", err)
	}
	mergeHash, err := g.repo.Storer.SetEncodedObject(enc)
	if err != nil {
		t.Fatalf("store merge commit: %v", err)
	}
	if err := g.repo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), mergeHash),
	); err != nil {
		t.Fatalf("update main: %v", err)
	}

	// First-parent chain: merge -> c1 -> c0 = 3. Five commits are reachable
	// in total, so a walker that followed ALL parents would return 5.
	depth, err := g.CommitDepth()
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != 3 {
		t.Fatalf("depth = %d, want first-parent 3", depth)
	}
}

func TestRewriteHistory_KeepsRecentCommits(t *testing.T) {
	g := newTestRepo(t)
	for i := 0; i < 15; i++ {
		commitFile(t, g, i)
	}
	head, err := g.repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	before, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("head commit: %v", err)
	}
	// Capture the pre-rewrite chain (newest first) so the rewrite can be
	// checked commit-by-commit, not just at the head.
	var origTrees []plumbing.Hash
	h := head.Hash()
	for i := 0; i < 5; i++ {
		c, err := g.repo.CommitObject(h)
		if err != nil {
			t.Fatalf("orig walk %d: %v", i, err)
		}
		origTrees = append(origTrees, c.TreeHash)
		h = c.ParentHashes[0]
	}

	if err := g.RewriteHistory(5); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	// Depth is now exactly 5 and the head hash changed.
	depth, err := g.CommitDepth()
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != 5 {
		t.Fatalf("depth after rewrite = %d, want 5", depth)
	}
	ref, err := g.repo.Head()
	if err != nil {
		t.Fatalf("head after rewrite: %v", err)
	}
	if ref.Hash() == head.Hash() {
		t.Fatal("head hash unchanged after rewrite with keep < depth")
	}

	// The head commit preserved tree, message and author/committer timestamps.
	after, err := g.repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("rewritten head commit: %v", err)
	}
	if after.TreeHash != before.TreeHash {
		t.Fatalf("tree changed: %s -> %s", before.TreeHash, after.TreeHash)
	}
	if after.Message != before.Message {
		t.Fatalf("message changed: %q -> %q", before.Message, after.Message)
	}
	if after.Author.When.Unix() != before.Author.When.Unix() {
		t.Fatalf("author time changed: %v -> %v", before.Author.When, after.Author.When)
	}
	if after.Committer.When.Unix() != before.Committer.When.Unix() {
		t.Fatalf("committer time changed: %v -> %v", before.Committer.When, after.Committer.When)
	}

	// Every kept commit preserved its tree — the kept window's interior is
	// the right one, not just the head.
	h = ref.Hash()
	for i, want := range origTrees {
		c, err := g.repo.CommitObject(h)
		if err != nil {
			t.Fatalf("rewritten walk %d: %v", i, err)
		}
		if c.TreeHash != want {
			t.Fatalf("kept commit %d tree changed: %s -> %s", i, want, c.TreeHash)
		}
		if i == len(origTrees)-1 {
			break
		}
		h = c.ParentHashes[0]
	}

	// The new root (5th commit back) has no parent.
	h = ref.Hash()
	for i := 0; i < 4; i++ {
		c, err := g.repo.CommitObject(h)
		if err != nil {
			t.Fatalf("walk %d: %v", i, err)
		}
		h = c.ParentHashes[0]
	}
	root, err := g.repo.CommitObject(h)
	if err != nil {
		t.Fatalf("root commit: %v", err)
	}
	if len(root.ParentHashes) != 0 {
		t.Fatalf("new root has parents: %v", root.ParentHashes)
	}

	// The worktree is clean against the rewritten head (same tree).
	wt, err := g.repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	status, err := wt.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.IsClean() {
		t.Fatalf("worktree dirty after rewrite: %v", status)
	}
}

func TestRewriteHistory_NoopWhenBelowKeep(t *testing.T) {
	g := newTestRepo(t)
	for i := 0; i < 3; i++ {
		commitFile(t, g, i)
	}
	head, err := g.repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if err := g.RewriteHistory(10); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	ref, err := g.repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if ref.Hash() != head.Hash() {
		t.Fatal("hash changed although depth <= keep (rebuild should reproduce identical objects)")
	}
}

func TestPushForce_ReplacesRemoteHistory(t *testing.T) {
	g := newTestRepo(t)
	remoteDir := t.TempDir()
	if _, err := git.PlainInit(remoteDir, true); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	repo := g.repo
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{remoteDir},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}

	for i := 0; i < 3; i++ {
		commitFile(t, g, i)
	}
	if err := g.PushToBranch("main", "", ""); err != nil {
		t.Fatalf("push: %v", err)
	}

	commitFile(t, g, 99)
	if err := g.RewriteHistory(1); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := g.PushForce("", ""); err != nil {
		t.Fatalf("force push: %v", err)
	}

	// The bare remote now holds exactly 1 first-parent commit whose tree
	// matches the rewritten head.
	bare, err := git.PlainOpen(remoteDir)
	if err != nil {
		t.Fatalf("open bare: %v", err)
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
	if depth != 1 {
		t.Fatalf("remote depth after force push = %d, want 1", depth)
	}
}

// initTestRepo creates a local git repo at repoPath with a single initial commit.
// Returns a GitRepo wrapping the freshly-initialized repo so callers can drive
// StageAndCommit exactly the way SyncService does in production.
func initTestRepo(t *testing.T, repoPath string) *GitRepo {
	t.Helper()
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	// Seed an empty README so HEAD exists and IsClean() is sane.
	readme := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(readme, []byte("# test\n"), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("add readme: %v", err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "t@t"},
	}); err != nil {
		t.Fatalf("commit init: %v", err)
	}
	return &GitRepo{repo: repo, repoPath: repoPath}
}

// TestStageAndCommitWhitelist verifies SYNC-P1-9: StageAndCommit only stages
// the synced config files (defaultSyncedFiles, shared with encrypt/decrypt/compare)
// plus the repo metadata files .sync-salt and README.md — never wt.Add(".").
// A stray file representing an SSH key (id_rsa) dropped into the sync dir
// must NOT be committed plaintext.
//
// tunnels.json is an explicit regression guard: it was missing from the old
// hand-written commit whitelist, so tunnel changes were encrypted into the
// repo but never committed or synced.
func TestStageAndCommitWhitelist(t *testing.T) {
	repoPath := t.TempDir()
	g := initTestRepo(t, repoPath)

	// Whitelisted files — should be staged. The list mirrors the
	// exact names StageAndCommit iterates over.
	whitelisted := map[string]string{
		".sync-salt": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
		"README.md":  "# sync repo\n",
	}
	for _, name := range defaultSyncedFiles {
		whitelisted[name] = "{}"
	}
	for name, content := range whitelisted {
		if err := os.WriteFile(filepath.Join(repoPath, name), []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// Stray files — must NOT be staged. Models an attacker dropping
	// ~/.ssh/id_rsa into the sync directory, plus the local-only store
	// files that must never ride along into the sync repo.
	stray := map[string]string{
		"id_rsa":           "-----BEGIN RSA PRIVATE KEY-----\nMOCK\n-----END RSA PRIVATE KEY-----",
		"secret.txt":       "internal passwords",
		"notes.md":         "echo pwned",
		"backup.tgz":       "binary",
		"ai-sessions.json": `[]`,
		"skills.json":      `[]`,
	}
	for name, content := range stray {
		if err := os.WriteFile(filepath.Join(repoPath, name), []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	committed, err := g.StageAndCommit("test whitelist")
	if err != nil {
		t.Fatalf("StageAndCommit: %v", err)
	}
	if !committed {
		t.Fatalf("expected commit=true (whitelisted files present)")
	}

	wt, err := g.repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	// Stray files MUST stay untracked — never appear in the index with a
	// staged/modified/deleted code. The whitelist leaves them on disk; we
	// only need to prove they did not get staged.
	status, err := wt.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for strayName := range stray {
		if fileStatus, ok := status[strayName]; ok && fileStatus.Worktree != git.Untracked {
			t.Errorf("stray file %s reached worktree status %q — must remain Untracked", strayName, fileStatus.Worktree)
		}
	}

	// Walk the latest commit's tree and assert the stray files are absent.
	head, err := g.repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	commit, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit object: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	for strayName := range stray {
		if _, err := tree.File(strayName); err == nil {
			t.Errorf("stray file %s was committed plaintext — SYNC-P1-9 regression", strayName)
		}
	}
	for wantName := range whitelisted {
		if _, err := tree.File(wantName); err != nil {
			t.Errorf("whitelisted file %s missing from commit", wantName)
		}
	}
}

// TestStageAndCommitIgnoresUnknownDirEntries — even nested junk under the
// repo path must not be staged. Defends against a future maintainer who
// might try to widen the whitelist by accident.
func TestStageAndCommitIgnoresUnknownDirEntries(t *testing.T) {
	repoPath := t.TempDir()
	g := initTestRepo(t, repoPath)

	subdir := filepath.Join(repoPath, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "evil.pem"), []byte("PEM"), 0600); err != nil {
		t.Fatalf("write evil: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "connections.json"), []byte(`{"c":[]}`), 0600); err != nil {
		t.Fatalf("write conn: %v", err)
	}

	committed, err := g.StageAndCommit("narrow whitelist")
	if err != nil {
		t.Fatalf("StageAndCommit: %v", err)
	}
	if !committed {
		t.Fatalf("expected commit=true")
	}

	head, err := g.repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	commit, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if _, err := tree.File("subdir/evil.pem"); err == nil {
		t.Errorf("nested stray file subdir/evil.pem was committed — whitelist is too wide")
	}
}

// TestStageAndCommitFilesScopedWhitelist verifies that StageAndCommitFiles
// stages exactly the given scope plus repo metadata: an out-of-scope
// syncable file (settings.json) sitting in the worktree must NOT be
// committed, while .sync-salt stays whitelisted.
func TestStageAndCommitFilesScopedWhitelist(t *testing.T) {
	repoPath := t.TempDir()
	g := initTestRepo(t, repoPath)

	if err := os.WriteFile(filepath.Join(repoPath, "connections.json"), []byte(`{"c":[]}`), 0600); err != nil {
		t.Fatalf("write connections: %v", err)
	}
	// Out of scope but present in the worktree — must not be committed.
	if err := os.WriteFile(filepath.Join(repoPath, "settings.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	// Repo metadata — always whitelisted by StageAndCommitFiles.
	if err := os.WriteFile(filepath.Join(repoPath, ".sync-salt"), []byte("aabbccdd"), 0600); err != nil {
		t.Fatalf("write salt: %v", err)
	}

	committed, err := g.StageAndCommitFiles("scoped commit", []string{"connections.json"})
	if err != nil {
		t.Fatalf("StageAndCommitFiles: %v", err)
	}
	if !committed {
		t.Fatalf("expected commit=true (scoped whitelist present)")
	}

	head, err := g.repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	commit, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit object: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if _, err := tree.File("connections.json"); err != nil {
		t.Errorf("scoped file connections.json missing from commit")
	}
	if _, err := tree.File(".sync-salt"); err != nil {
		t.Errorf("repo metadata .sync-salt missing from commit")
	}
	if _, err := tree.File("settings.json"); err == nil {
		t.Errorf("out-of-scope file settings.json was committed — scope leaked into the whitelist")
	}
}
