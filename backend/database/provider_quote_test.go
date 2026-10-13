package database

import "testing"

// TestQuoteDoublesQuoteCharacters guards the Quote fix: quoting must double
// the dialect's quote character (the same rule SafeMyIdent/SafePgIdent
// implement on their success path) instead of silently dropping the error.
func TestQuoteDoublesQuoteCharacters(t *testing.T) {
	cases := []struct {
		dialect string
		input   string
		want    string
	}{
		{"mysql", "weird`name", "`weird``name`"},
		{"mysql", "plain", "`plain`"},
		{"postgres", `we"ird`, `"we""ird"`},
		{"postgres", "plain", `"plain"`},
		{"rqlite", `we"ird`, `"we""ird"`},
		{"sqlserver", "weird]name", "[weird]]name]"},
		{"oracle", `we"ird`, `"we""ird"`},
	}
	for _, tc := range cases {
		t.Run(tc.dialect+"/"+tc.input, func(t *testing.T) {
			p, err := NewProvider(tc.dialect)
			if err != nil {
				t.Fatalf("NewProvider(%q): %v", tc.dialect, err)
			}
			if got := p.Quote(tc.input); got != tc.want {
				t.Fatalf("Quote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestQuoteNeverReturnsEmpty guards the swallowed-error regression: names
// that Safe*Ident used to reject (dot-segments, NUL-free but odd) produced an
// empty string and silently broken SQL. Quoting by doubling keeps any name
// inert inside its quotes, so Quote must always return a quoted form.
func TestQuoteNeverReturnsEmpty(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres", "rqlite"} {
		t.Run(dialect, func(t *testing.T) {
			p, err := NewProvider(dialect)
			if err != nil {
				t.Fatalf("NewProvider(%q): %v", dialect, err)
			}
			for _, name := range []string{"", "odd..name", "a/b", `a"b`, "a`b"} {
				got := p.Quote(name)
				if got == "" {
					t.Fatalf("Quote(%q) returned empty string", name)
				}
			}
		})
	}
}
