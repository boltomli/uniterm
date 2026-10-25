package database

import (
	"strings"
	"testing"
)

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
		{"sqlite", `we"ird`, `"we""ird"`},
		{"sqlite", "a/b", `"a/b"`},
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

// quoteRule describes how one dialect wraps and escapes identifiers.
type quoteRule struct {
	open, close string // wrapper characters
	esc         string // character that must be doubled inside
}

// quoteRules pins the quoting contract of EVERY registered provider. A new
// dialect joining the registry must be added here — the length check below
// fails otherwise, which is exactly the "the test missed sqlite" gap the
// 2026-10-25 audit found (DB-21).
var quoteRules = map[string]quoteRule{
	"mysql":     {open: "`", close: "`", esc: "`"},
	"postgres":  {open: `"`, close: `"`, esc: `"`},
	"rqlite":    {open: `"`, close: `"`, esc: `"`},
	"sqlite":    {open: `"`, close: `"`, esc: `"`},
	"oracle":    {open: `"`, close: `"`, esc: `"`},
	"sqlserver": {open: `[`, close: `]`, esc: `]`},
}

// TestQuoteNeverReturnsEmpty guards the swallowed-error regression: names
// that Safe*Ident used to reject (dot-segments, slashes, over-long) produced
// an empty string and silently broken SQL — for SQLite this meant the table
// could not be browsed at all (`SELECT * FROM `). Quoting by doubling keeps
// any name inert inside its quotes, so Quote must always return a quoted
// form. The matrix walks the provider REGISTRY, not a hand-written list.
func TestQuoteNeverReturnsEmpty(t *testing.T) {
	if len(providers) != len(quoteRules) {
		t.Fatalf("provider registry has %d entries, quote rules cover %d — add the new dialect to quoteRules", len(providers), len(quoteRules))
	}
	names := []string{"", "odd..name", "a/b", `a"b`, "a`b", "a]b", `a\b`, strings.Repeat("x", 129)}
	for dialect := range providers {
		rule, ok := quoteRules[dialect]
		if !ok {
			t.Fatalf("provider %q has no quote rule (DB-21)", dialect)
		}
		t.Run(dialect, func(t *testing.T) {
			p, err := NewProvider(dialect)
			if err != nil {
				t.Fatalf("NewProvider(%q): %v", dialect, err)
			}
			for _, name := range names {
				got := p.Quote(name)
				if got == "" {
					t.Fatalf("Quote(%q) returned empty string", name)
				}
				// Wraps: open … close, exactly the wrapper characters.
				if !strings.HasPrefix(got, rule.open) || !strings.HasSuffix(got, rule.close) {
					t.Fatalf("Quote(%q) = %q, want wrapper %s…%s", name, got, rule.open, rule.close)
				}
				inner := got[len(rule.open) : len(got)-len(rule.close)]
				// The escape character is doubled inside; nothing else changes.
				// (An unescaped quote here would be an injectable literal.)
				wantInner := strings.ReplaceAll(name, rule.esc, rule.esc+rule.esc)
				if inner != wantInner {
					t.Fatalf("Quote(%q) inner = %q, want %q", name, inner, wantInner)
				}
			}
		})
	}
}
