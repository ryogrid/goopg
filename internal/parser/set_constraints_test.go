package parser

import "testing"

// TestParseSetConstraints covers the four grammar shapes of
// SET CONSTRAINTS { ALL | name [, ...] } { DEFERRED | IMMEDIATE }. 0119-0004.
func TestParseSetConstraints(t *testing.T) {
	cases := []struct {
		in       string
		wantAll  bool
		wantDefr bool
		wantName []string
	}{
		{"SET CONSTRAINTS ALL DEFERRED", true, true, nil},
		{"SET CONSTRAINTS ALL IMMEDIATE", true, false, nil},
		{"SET CONSTRAINTS my_fk DEFERRED", false, true, []string{"my_fk"}},
		{"SET CONSTRAINTS a, b IMMEDIATE", false, false, []string{"a", "b"}},
		{"SET CONSTRAINTS public.my_fk DEFERRED", false, true, []string{"my_fk"}},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("Parse(%q): got %d stmts, want 1", c.in, len(stmts))
		}
		sc, ok := stmts[0].(*SetConstraintsStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T, want *SetConstraintsStmt", c.in, stmts[0])
		}
		if sc.All != c.wantAll {
			t.Errorf("Parse(%q): All=%v want %v", c.in, sc.All, c.wantAll)
		}
		if sc.Deferred != c.wantDefr {
			t.Errorf("Parse(%q): Deferred=%v want %v", c.in, sc.Deferred, c.wantDefr)
		}
		if len(sc.Names) != len(c.wantName) {
			t.Fatalf("Parse(%q): Names=%v want %v", c.in, sc.Names, c.wantName)
		}
		for i := range sc.Names {
			if sc.Names[i] != c.wantName[i] {
				t.Errorf("Parse(%q): Names[%d]=%q want %q", c.in, i, sc.Names[i], c.wantName[i])
			}
		}
	}
}

// TestParseSetConstraintsMissingMode rejects a SET CONSTRAINTS without a
// DEFERRED/IMMEDIATE mode word.
func TestParseSetConstraintsMissingMode(t *testing.T) {
	if _, err := Parse("SET CONSTRAINTS ALL"); err == nil {
		t.Fatalf("Parse(SET CONSTRAINTS ALL): expected error, got nil")
	}
}
