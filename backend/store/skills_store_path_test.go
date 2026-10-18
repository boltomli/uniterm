package store

import (
	"os"
	"path/filepath"
	"testing"
)

// newSkillWithFiles seeds a single skill directory (dirName) with a SKILL.md
// and arbitrary extra files (rel -> content) under the given config dir.
func newSkillWithFiles(t *testing.T, cfg, dirName string, files map[string]string) *SkillsStore {
	t.Helper()
	skillDir := filepath.Join(cfg, skillsDirName, dirName)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	all := map[string]string{skillEntryFile: zipSkillMD}
	for k, v := range files {
		all[k] = v
	}
	for rel, content := range all {
		p := filepath.Join(skillDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return NewSkillsStore(cfg)
}

// F-12.3: GetSkillFile 路径约束 —— 合法的 ".." 前缀文件名不被误杀，
// 穿越/绝对路径一律拒绝。
func TestSkillsStore_GetSkillFilePathGuard(t *testing.T) {
	cfg := t.TempDir()
	s := newSkillWithFiles(t, cfg, "demo-skill", map[string]string{
		"..notes.md":        "legit double-dot name",
		"references/a.md":   "# ref a",
		"scripts/run.sh":    "echo hi",
		"outside-target.md": "never leaves the tree",
	})

	allowed := []struct {
		name string
		rel  string
		want string
	}{
		{"double dot prefix file allowed", "..notes.md", "legit double-dot name"},
		{"nested reference allowed", "references/a.md", "# ref a"},
		{"script allowed", "scripts/run.sh", "echo hi"},
	}
	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.GetSkillFile("demo-skill", tc.rel)
			if err != nil {
				t.Fatalf("GetSkillFile(%q): %v", tc.rel, err)
			}
			if got != tc.want {
				t.Fatalf("GetSkillFile(%q) = %q, want %q", tc.rel, got, tc.want)
			}
		})
	}

	denied := []struct {
		name string
		rel  string
	}{
		{"parent traversal", "../outside-target.md"},
		{"deep traversal", "../../outside-target.md"},
		{"absolute path", filepath.Join(string(filepath.Separator), "etc", "passwd")},
		{"dotdot via subdirectory", "references/../../../outside-target.md"},
		{"empty path", ""},
		{"wrong extension", "scripts/run.sh.bak"},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := s.GetSkillFile("demo-skill", tc.rel); err == nil {
				t.Fatalf("GetSkillFile(%q) should be rejected, got %q", tc.rel, got)
			}
		})
	}

	// 穿越名在 Windows 上经 Clean 变成 "..\x" 也必须拒绝（两种分隔符都算）。
	if _, err := s.GetSkillFile("demo-skill", `..\..\outside-target.md`); err == nil {
		t.Fatalf(`GetSkillFile %q should be rejected`, `..\..\outside-target.md`)
	}
}
