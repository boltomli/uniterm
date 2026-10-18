package store

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F-01: Zip Slip 回归用例。恶意 ZIP 一律返回 error，且不得在 skills 目录
// 或临时目录之外生成任何文件。

const zipSkillMD = "---\nname: demo-skill\ndescription: demo skill for tests\n---\nhello body\n"

type zipEntry struct {
	name    string
	content string
	mode    os.FileMode // 0 = 普通文件 0644
}

func writeTestZip(t *testing.T, path string, entries []zipEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatalf("create entry %q: %v", e.name, err)
		}
		if _, err := fw.Write([]byte(e.content)); err != nil {
			t.Fatalf("write entry %q: %v", e.name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// setupZipImportTemp 把进程临时目录指到受控目录（os.MkdirTemp 的落点），
// 便于断言解压没有穿出临时根。无法控制时返回 false。
func setupZipImportTemp(t *testing.T) (root, tmpBase string, controlled bool) {
	t.Helper()
	root = t.TempDir()
	tmpBase = filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpBase, 0755); err != nil {
		t.Fatalf("mkdir tmpBase: %v", err)
	}
	t.Setenv("TMPDIR", tmpBase)
	t.Setenv("TMP", tmpBase)
	t.Setenv("TEMP", tmpBase)
	got := filepath.Clean(os.TempDir())
	want := filepath.Clean(tmpBase)
	return root, tmpBase, strings.EqualFold(got, want)
}

// assertNoStrayFiles 断言 dir 树内只存在 wantRel 中列出的相对路径（及其祖先目录）。
func assertNoStrayFiles(t *testing.T, dir string, wantRel ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, r := range wantRel {
		clean := filepath.Clean(r)
		want[clean] = true
		// 允许目标路径自身的祖先目录
		for d := filepath.Dir(clean); d != "." && d != string(filepath.Separator); d = filepath.Dir(d) {
			want[d] = true
		}
	}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.Clean(rel)
		if info.IsDir() {
			if !want[rel] {
				t.Errorf("unexpected dir under temp root: %s", rel)
			}
			return nil
		}
		if !want[rel] {
			t.Errorf("unexpected file written under temp root: %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk temp root: %v", err)
	}
}

func TestSkillsStore_ImportFromZipRejectsTraversal(t *testing.T) {
	cases := []struct {
		name    string
		entries []zipEntry
	}{
		{"dotdot shallow", []zipEntry{
			{name: "evil/SKILL.md", content: zipSkillMD},
			{name: "evil/../sentinel-a.txt", content: "pwn"},
		}},
		{"dotdot two levels", []zipEntry{
			{name: "evil/SKILL.md", content: zipSkillMD},
			{name: "evil/../../sentinel-b.txt", content: "pwn"},
		}},
		{"backslash traversal", []zipEntry{
			{name: "evil/SKILL.md", content: zipSkillMD},
			{name: `evil/..\sentinel-c.txt`, content: "pwn"},
		}},
		{"double separator", []zipEntry{
			{name: "evil/SKILL.md", content: zipSkillMD},
			{name: "evil//abs-d.txt", content: "pwn"},
		}},
		{"drive prefix", []zipEntry{
			{name: "evil/SKILL.md", content: zipSkillMD},
			{name: `evil/C:\sentinel-e.txt`, content: "pwn"},
		}},
		{"nested traversal under root", []zipEntry{
			{name: "evil/SKILL.md", content: zipSkillMD},
			{name: "evil/references/../../../sentinel-f.txt", content: "pwn"},
		}},
		{"malicious skill root", []zipEntry{
			{name: "../SKILL.md", content: zipSkillMD},
			{name: "../../sentinel-g.txt", content: "pwn"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _, controlled := setupZipImportTemp(t)
			zipPath := filepath.Join(root, "zips", "evil.zip")
			if err := os.MkdirAll(filepath.Dir(zipPath), 0755); err != nil {
				t.Fatalf("mkdir zips: %v", err)
			}
			writeTestZip(t, zipPath, tc.entries)

			cfg := filepath.Join(root, "cfg")
			s := NewSkillsStore(cfg)
			if _, err := s.ImportFromZip(zipPath); err == nil {
				t.Fatalf("ImportFromZip should reject unsafe entries %v", tc.entries)
			}
			// 不允许有任何 skill 落盘
			if _, err := os.Stat(filepath.Join(s.skillsRoot(), "demo-skill")); err == nil {
				t.Fatalf("skill must not be imported for a rejected zip")
			}
			if !controlled {
				return
			}
			assertNoStrayFiles(t, root, filepath.Join("zips", "evil.zip"), "tmp")
		})
	}
}

func TestSkillsStore_ImportFromZipRejectsSymlinkEntry(t *testing.T) {
	root, tmpBase, controlled := setupZipImportTemp(t)
	zipPath := filepath.Join(root, "zips", "evil.zip")
	if err := os.MkdirAll(filepath.Dir(zipPath), 0755); err != nil {
		t.Fatalf("mkdir zips: %v", err)
	}
	writeTestZip(t, zipPath, []zipEntry{
		{name: "evil/SKILL.md", content: zipSkillMD},
		{name: "evil/link", content: filepath.Join(tmpBase, "target"), mode: os.ModeSymlink | 0777},
	})

	s := NewSkillsStore(filepath.Join(root, "cfg"))
	if _, err := s.ImportFromZip(zipPath); err == nil {
		t.Fatalf("ImportFromZip should refuse symlink entries")
	}
	if !controlled {
		return
	}
	assertNoStrayFiles(t, root, filepath.Join("zips", "evil.zip"), "tmp")
}

func TestSkillsStore_ImportFromZipValidStillWorks(t *testing.T) {
	root, _, _ := setupZipImportTemp(t)
	zipPath := filepath.Join(root, "zips", "good.zip")
	if err := os.MkdirAll(filepath.Dir(zipPath), 0755); err != nil {
		t.Fatalf("mkdir zips: %v", err)
	}
	writeTestZip(t, zipPath, []zipEntry{
		{name: "skill/SKILL.md", content: zipSkillMD},
		{name: "skill/references/a.md", content: "# ref\n"},
		{name: "skill/scripts/run.sh", content: "#!/bin/sh\necho hi\n"},
	})

	s := NewSkillsStore(filepath.Join(root, "cfg"))
	name, err := s.ImportFromZip(zipPath)
	if err != nil {
		t.Fatalf("valid zip should import: %v", err)
	}
	if name != "demo-skill" {
		t.Fatalf("unexpected skill name: %q", name)
	}
	for _, rel := range []string{"SKILL.md", "references/a.md", "scripts/run.sh"} {
		p := filepath.Join(s.skillsRoot(), "demo-skill", filepath.FromSlash(rel))
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s on disk: %v", rel, err)
		}
	}
	// 落盘结构不变：偏好奇好写入且锁定
	prefs, err := s.loadPrefs()
	if err != nil {
		t.Fatalf("loadPrefs: %v", err)
	}
	if len(prefs.Prefs) != 1 || prefs.Prefs[0].Name != "demo-skill" || !prefs.Prefs[0].Locked {
		t.Fatalf("unexpected prefs: %+v", prefs.Prefs)
	}
}
