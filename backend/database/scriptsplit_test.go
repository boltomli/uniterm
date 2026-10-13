package database

import (
	"testing"
)

func stmts(ss []ScriptStatement) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.SQL
	}
	return out
}

func TestSplitScript_BasicMulti(t *testing.T) {
	s := SplitScript("SELECT 1; SELECT 2;", "mysql")
	got := stmts(s)
	want := []string{"SELECT 1", "SELECT 2"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSplitScript_SemicolonInString(t *testing.T) {
	s := SplitScript("INSERT INTO t VALUES ('a;b'); SELECT 2;", "mysql")
	got := stmts(s)
	if len(got) != 2 || got[0] != "INSERT INTO t VALUES ('a;b')" || got[1] != "SELECT 2" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitScript_LineComments(t *testing.T) {
	in := "-- comment\nSELECT 1; # another\nSELECT 2;"
	got := stmts(SplitScript(in, "mysql"))
	if len(got) != 2 || got[0] != "SELECT 1" || got[1] != "SELECT 2" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitScript_BlockComment(t *testing.T) {
	in := "/* hi; */ SELECT 1; /* x */ SELECT 2;"
	got := stmts(SplitScript(in, "mysql"))
	if len(got) != 2 || got[0] != "SELECT 1" || got[1] != "SELECT 2" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitScript_DelimiterRoutine(t *testing.T) {
	in := "DROP PROCEDURE IF EXISTS p;\nDELIMITER $$\nCREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND$$\nDELIMITER ;\nCALL p();"
	got := stmts(SplitScript(in, "mysql"))
	want := []string{
		"DROP PROCEDURE IF EXISTS p",
		"CREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND",
		"CALL p()",
	}
	if len(got) != 3 {
		t.Fatalf("got %d stmts: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stmt %d:\n got=[%s]\nwant=[%s]", i, got[i], want[i])
		}
	}
}

func TestSplitScript_LineNumbers(t *testing.T) {
	in := "SELECT 1;\nSELECT 2;\nBAD SQL HERE"
	got := SplitScript(in, "mysql")
	if len(got) != 3 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].Line != 1 || got[1].Line != 2 || got[2].Line != 3 {
		t.Fatalf("lines: %v", []int{got[0].Line, got[1].Line, got[2].Line})
	}
}

func TestSplitScript_EscapedQuote(t *testing.T) {
	in := `INSERT INTO t VALUES ('it''s'); SELECT 2;`
	got := stmts(SplitScript(in, "mysql"))
	if len(got) != 2 || got[0] != `INSERT INTO t VALUES ('it''s')` {
		t.Fatalf("got %v", got)
	}
}

// TestSplitScript_HashCommentByDialect guards the dialect-aware `#` handling:
// only MySQL-family dialects treat `#` as a line comment. In SQL Server `#t`
// names temp tables and in PostgreSQL `#` is a bitwise operator, so treating
// it as a comment truncated those statements mid-way.
func TestSplitScript_HashCommentByDialect(t *testing.T) {
	cases := []struct {
		name    string
		dialect string
		script  string
		want    []string
	}{
		{
			name:    "mysql hash comment",
			dialect: "mysql",
			script:  "SELECT 1; # comment; still comment\nSELECT 2;",
			want:    []string{"SELECT 1", "SELECT 2"},
		},
		{
			name:    "sqlserver temp table",
			dialect: "sqlserver",
			script:  "SELECT * INTO #t FROM x; SELECT 2;",
			want:    []string{"SELECT * INTO #t FROM x", "SELECT 2"},
		},
		{
			name:    "postgres hash operator",
			dialect: "postgres",
			script:  "UPDATE t SET f = f # 4 WHERE id = 1; SELECT 2;",
			want:    []string{"UPDATE t SET f = f # 4 WHERE id = 1", "SELECT 2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stmts(SplitScript(tc.script, tc.dialect))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d statements %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("statement %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
