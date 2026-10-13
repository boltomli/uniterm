package session

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// --- Harness ----------------------------------------------------------------

// fakeRawStream serves a scripted in-memory tar stream to extractTarInto.
type fakeRawStream struct {
	*bytes.Reader
}

func (f *fakeRawStream) Close() error { return nil }
func (f *fakeRawStream) Wait() error  { return nil }

// tarEntry describes one entry of the scripted tar stream.
type tarEntry struct {
	name     string
	typeflag byte
	linkname string
	body     string
}

// buildTarStream packs entries into an in-memory tar stream.
func buildTarStream(t *testing.T, entries []tarEntry) *fakeRawStream {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Linkname: e.linkname,
			Mode:     0o644,
		}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typeflag == tar.TypeReg && e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &fakeRawStream{bytes.NewReader(buf.Bytes())}
}

// tarTestEnv is the shared session + paths for extraction tests. rp is
// "/container/out" so the tar root entry name ("base") is "out"; lp sits two
// levels deep (tmp/x/dl) so escapes land inside tmp and stay detectable.
type tarTestEnv struct {
	s    *ContainerFileSession
	tmp  string
	lp   string
	task *TransferTask
}

func newTarTestEnv(t *testing.T) *tarTestEnv {
	t.Helper()
	tmp := t.TempDir()
	lp := filepath.Join(tmp, "x", "dl")
	if err := os.MkdirAll(lp, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewContainerFileSession("t", nil, "cid", "title")
	task := &TransferTask{ID: "t1", Type: "download", LocalPath: lp, RemotePath: "/container/out", Status: "running"}
	task.start()
	t.Cleanup(task.done)
	return &tarTestEnv{s: s, tmp: tmp, lp: lp, task: task}
}

// requireSymlinks skips symlink tests when the platform refuses os.Symlink
// (Windows without Developer Mode / privileges).
func requireSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "l")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// --- Traversal guards -------------------------------------------------------

// TestExtractTarRejectsTraversalEntry: a compromised container controls every
// tar header; an entry "out/../evil.txt" must abort extraction instead of
// being joined past the download folder (kubectl cp CVE-2019-1002101 class).
func TestExtractTarRejectsTraversalEntry(t *testing.T) {
	env := newTarTestEnv(t)
	rs := buildTarStream(t, []tarEntry{
		{name: "out/", typeflag: tar.TypeDir},
		{name: "out/../evil.txt", typeflag: tar.TypeReg, body: "BAD"},
	})
	if err := env.s.extractTarInto(rs, "/container/out", env.lp, env.task); err == nil {
		t.Fatal("extractTarInto accepted the traversal entry out/../evil.txt")
	}
	if _, err := os.Stat(filepath.Join(env.tmp, "x", "evil.txt")); err == nil {
		t.Fatal("traversal entry wrote outside the download folder")
	}
}

// TestExtractTarRejectsAbsoluteEntry: an absolute entry name must abort
// extraction instead of being silently remapped into the download folder.
func TestExtractTarRejectsAbsoluteEntry(t *testing.T) {
	env := newTarTestEnv(t)
	rs := buildTarStream(t, []tarEntry{
		{name: "/abs/evil2.txt", typeflag: tar.TypeReg, body: "BAD"},
	})
	if err := env.s.extractTarInto(rs, "/container/out", env.lp, env.task); err == nil {
		t.Fatal("extractTarInto accepted the absolute entry /abs/evil2.txt")
	}
	if _, err := os.Stat(filepath.Join(env.lp, "abs", "evil2.txt")); err == nil {
		t.Fatal("absolute entry was remapped into the download folder")
	}
}

// TestExtractTarSkipsAbsoluteSymlink: a symlink whose target points outside
// the download folder must not be created.
func TestExtractTarSkipsAbsoluteSymlink(t *testing.T) {
	requireSymlinks(t)
	env := newTarTestEnv(t)
	rs := buildTarStream(t, []tarEntry{
		{name: "out/", typeflag: tar.TypeDir},
		{name: "out/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	if err := env.s.extractTarInto(rs, "/container/out", env.lp, env.task); err != nil {
		t.Fatalf("extractTarInto: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(env.lp, "link")); err == nil {
		t.Fatal("symlink with absolute outside target /etc/passwd was created")
	}
}

// TestExtractTarSkipsEscapingRelativeSymlink: a relative "../.." target that
// resolves outside the download folder must not be created.
func TestExtractTarSkipsEscapingRelativeSymlink(t *testing.T) {
	requireSymlinks(t)
	env := newTarTestEnv(t)
	rs := buildTarStream(t, []tarEntry{
		{name: "out/", typeflag: tar.TypeDir},
		{name: "out/up", typeflag: tar.TypeSymlink, linkname: "../../outside"},
	})
	if err := env.s.extractTarInto(rs, "/container/out", env.lp, env.task); err != nil {
		t.Fatalf("extractTarInto: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(env.lp, "up")); err == nil {
		t.Fatal("symlink with escaping target ../../outside was created")
	}
}

// TestExtractTarLegitEntriesSucceed: ordinary dirs, nested files and an
// in-root relative symlink still extract (guard against over-blocking).
func TestExtractTarLegitEntriesSucceed(t *testing.T) {
	requireSymlinks(t)
	env := newTarTestEnv(t)
	rs := buildTarStream(t, []tarEntry{
		{name: "out/", typeflag: tar.TypeDir},
		{name: "out/sub/", typeflag: tar.TypeDir},
		{name: "out/sub/file.txt", typeflag: tar.TypeReg, body: "DATA"},
		{name: "out/ok", typeflag: tar.TypeSymlink, linkname: "sub/file.txt"},
	})
	if err := env.s.extractTarInto(rs, "/container/out", env.lp, env.task); err != nil {
		t.Fatalf("extractTarInto: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(env.lp, "sub", "file.txt"))
	if err != nil {
		t.Fatalf("legit nested file: %v", err)
	}
	if string(b) != "DATA" {
		t.Fatalf("nested file content = %q, want DATA", b)
	}
	fi, err := os.Lstat(filepath.Join(env.lp, "ok"))
	if err != nil {
		t.Fatalf("legit symlink: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("out/ok is not a symlink")
	}
}

// TestExtractTarRefusesWriteThroughSymlink: lp/x is a pre-existing symlink to
// a file outside the download folder; a regular entry "out/x" must not
// overwrite the symlink target.
func TestExtractTarRefusesWriteThroughSymlink(t *testing.T) {
	requireSymlinks(t)
	env := newTarTestEnv(t)
	outside := filepath.Join(env.tmp, "outside.txt")
	if err := os.WriteFile(outside, []byte("OUT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(env.lp, "x")); err != nil {
		t.Fatal(err)
	}
	rs := buildTarStream(t, []tarEntry{
		{name: "out/", typeflag: tar.TypeDir},
		{name: "out/x", typeflag: tar.TypeReg, body: "NEW"},
	})
	if err := env.s.extractTarInto(rs, "/container/out", env.lp, env.task); err == nil {
		t.Fatal("extractTarInto wrote through the symlink lp/x")
	}
	b, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "OUT" {
		t.Fatalf("outside file clobbered: %q, want OUT", b)
	}
}

// TestExtractTarRefusesDirThroughSymlink: lp/d is a pre-existing symlink to a
// directory outside the download folder; entries "out/d" + "out/d/f.txt" must
// not create files through it.
func TestExtractTarRefusesDirThroughSymlink(t *testing.T) {
	requireSymlinks(t)
	env := newTarTestEnv(t)
	odir := filepath.Join(env.tmp, "odir")
	if err := os.MkdirAll(odir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(odir, filepath.Join(env.lp, "d")); err != nil {
		t.Fatal(err)
	}
	rs := buildTarStream(t, []tarEntry{
		{name: "out/", typeflag: tar.TypeDir},
		{name: "out/d/", typeflag: tar.TypeDir},
		{name: "out/d/f.txt", typeflag: tar.TypeReg, body: "X"},
	})
	if err := env.s.extractTarInto(rs, "/container/out", env.lp, env.task); err == nil {
		t.Fatal("extractTarInto created a directory through the symlink lp/d")
	}
	if _, err := os.Stat(filepath.Join(odir, "f.txt")); err == nil {
		t.Fatal("file was written through the symlinked directory")
	}
}

// TestTarLinkTargetInRoot: link-target containment predicate.
func TestTarLinkTargetInRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "base", "dl")
	local := filepath.Join(root, "link")
	inside := filepath.Join(root, "sub", "file")
	cases := []struct {
		name string
		link string
		want bool
	}{
		{"absolute outside", filepath.Join(string(filepath.Separator), "etc", "passwd"), false},
		{"windows drive absolute", `C:\Windows\x`, false},
		{"relative escape", "../x", false},
		{"relative inside", "sub/file", true},
		{"absolute inside", inside, true},
	}
	for _, c := range cases {
		if got := tarLinkTargetInRoot(root, local, c.link); got != c.want {
			t.Errorf("%s: tarLinkTargetInRoot(%q) = %v, want %v", c.name, c.link, got, c.want)
		}
	}
}

// TestExtractTarSkipsRootSymlinkWithOutsideTarget: when the requested path
// ITSELF is a symlink (single-entry tar whose name equals the base), the
// target check must apply too — an absolute container target must not be
// materialised as a link pointing outside the download folder.
func TestExtractTarSkipsRootSymlinkWithOutsideTarget(t *testing.T) {
	requireSymlinks(t)
	env := newTarTestEnv(t)
	rs := buildTarStream(t, []tarEntry{
		{name: "out", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	if err := env.s.extractTarInto(rs, "/container/out", env.lp, env.task); err != nil {
		t.Fatalf("extractTarInto: %v", err)
	}
	fi, err := os.Lstat(env.lp)
	if err != nil {
		t.Fatalf("Lstat(lp): %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("root symlink with absolute outside target /etc/passwd was created")
	}
}
