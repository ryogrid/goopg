package executor

import "testing"

// TestPgRegexFlagsToGoModifiers pins pgRegexFlagsToGoModifiers, the shared
// PG-flags → Go-inline-modifiers translator introduced in M0134-0070 Round C
// (docs/design/m0134-0070-regexp-flags-and-family.md), including the two
// non-obvious traps documented there: PG's 's' flag does NOT map to Go's
// (?s) (naming collision — PG 's' means "single line, \n ordinary", the
// opposite intent of Go's "dot matches newline"), and 'm'/'n'/'p'/'w' all
// collapse onto Go's (?m).
func TestPgRegexFlagsToGoModifiers(t *testing.T) {
	cases := []struct {
		name       string
		flags      string
		wantGo     string
		wantGlobal bool
		wantErr    bool
	}{
		{"empty", "", "", false, false},
		{"i-only", "i", "(?i)", false, false},
		{"g-only", "g", "", true, false},
		{"ig", "ig", "(?i)", true, false},
		{"m-newline", "m", "(?m)", false, false},
		{"n-newline", "n", "(?m)", false, false},
		{"p-newline", "p", "(?m)", false, false},
		{"w-newline", "w", "(?m)", false, false},
		{"mg-combo", "mg", "(?m)", true, false},
		{"im-combo", "im", "(?im)", false, false},
		{"s-is-noop-not-go-dotall", "s", "", false, false},
		{"accepted-noop-flags", "cebtqx", "", false, false},
		{"unknown-flag", "z", "", false, true},
	}
	for _, c := range cases {
		goFlags, global, err := pgRegexFlagsToGoModifiers(c.flags)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got none", c.name)
				continue
			}
			ee, ok := err.(*ExecError)
			if !ok {
				t.Errorf("%s: err type=%T, want *ExecError", c.name, err)
				continue
			}
			if ee.Code != "22023" {
				t.Errorf("%s: SQLSTATE=%s want 22023", c.name, ee.Code)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if goFlags != c.wantGo || global != c.wantGlobal {
			t.Errorf("%s: got goFlags=%q global=%v, want goFlags=%q global=%v",
				c.name, goFlags, global, c.wantGo, c.wantGlobal)
		}
	}
}

// TestRegexpMatchesMFlagPerLineAnchoring pins the 'm' flag's effect on
// regexp_matches: '^'/'$' anchor per-line rather than to the whole string,
// so a multi-line haystack with 'mg' yields one row per line that matches
// (postgres/src/backend/utils/adt/regexp.c parse_re_flags: 'm'/'n' set
// REG_NEWLINE). Before this slice, goopg silently dropped 'm' and returned
// only a single whole-string match.
func TestRegexpMatchesMFlagPerLineAnchoring(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx, `SELECT regexp_matches('foo` + "\n" + `bar` + "\n" + `baz', '^ba.$', 'mg')`)
	want := []string{"{bar}", "{baz}"}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows %v, want %d rows %v", len(rows), rows, len(want), want)
	}
	for i, w := range want {
		if rows[i][0].StringValue() != w {
			t.Errorf("row %d = %q, want %q", i, rows[i][0].StringValue(), w)
		}
	}
}

// TestRegexpMatchesUnknownFlagErrors pins regexp_matches's response to an
// unrecognized flag character: SQLSTATE 22023 (ERRCODE_INVALID_PARAMETER_VALUE
// per parse_re_flags's default case, not ERRCODE_INVALID_REGULAR_EXPRESSION),
// instead of the pre-fix silent acceptance.
func TestRegexpMatchesUnknownFlagErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, err := runQueryErr(t, ctx, `SELECT regexp_matches('abc', 'a', 'z')`)
	if err == nil {
		t.Fatal("expected error, got none")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T, want *ExecError (err=%v)", err, err)
	}
	if ee.Code != "22023" {
		t.Fatalf("SQLSTATE=%s want 22023", ee.Code)
	}
}

// TestRegexpSplitToArrayRejectsGlobalFlag pins regexp_split_to_array()'s
// rejection of the 'g' flag (postgres/src/backend/utils/adt/regexp.c:1818-
// 1826 — "User mustn't specify 'g'"), and confirms an unrelated unknown flag
// char still raises 22023 too.
func TestRegexpSplitToArrayRejectsGlobalFlag(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, err := runQueryErr(t, ctx, `SELECT regexp_split_to_array('a,b,c', ',', 'g')`)
	if err == nil {
		t.Fatal("expected error for 'g' flag, got none")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T, want *ExecError (err=%v)", err, err)
	}
	if ee.Code != "22023" {
		t.Fatalf("SQLSTATE=%s want 22023", ee.Code)
	}

	_, err = runQueryErr(t, ctx, `SELECT regexp_split_to_array('a,b,c', ',', 'z')`)
	if err == nil {
		t.Fatal("expected error for unknown flag, got none")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "22023" {
		t.Fatalf("unknown-flag error = %v, want *ExecError{Code:22023}", err)
	}

	// A valid, non-'g' flag still splits correctly.
	rows := runQuery(t, ctx, `SELECT regexp_split_to_array('A,b,C', ',', 'i')`)
	if len(rows) != 1 || rows[0][0].StringValue() != "{A,b,C}" {
		t.Fatalf("got %v, want {A,b,C}", rows)
	}
}
