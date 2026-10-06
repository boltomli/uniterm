package sync

import (
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
