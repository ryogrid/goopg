package executor

import "testing"

// The exact HINT text PG attaches in textregexreplace (the 4-arg text
// regexp_replace overload, oid 2285). postgres/src/backend/utils/adt/
// regexp.c:673-684.
const regexpReplaceStartHint = "If you meant to use regexp_replace() with a start parameter, cast the fourth argument to integer explicitly."

// TestRegexpReplaceDigitFirstFlagHints pins the strings.sql:251 fixture
// (expected strings.out:802-804): when the 4th arg of the 4-arg regexp_replace
// is non-empty and its first byte is '0'..'9', goopg must emit the SAME error
// as the generic flags path plus PG's HINT. Before this slice goopg dropped
// the Hint field entirely, so this test fails pre-fix.
func TestRegexpReplaceDigitFirstFlagHints(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, err := runQueryErr(t, ctx, `SELECT regexp_replace('A PostgreSQL function', 'a|e|i|o|u', 'X', '1')`)
	if err == nil {
		t.Fatal("expected error, got none")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T, want *ExecError (err=%v)", err, err)
	}
	if ee.Code != "22023" {
		t.Errorf("SQLSTATE=%s want 22023", ee.Code)
	}
	if ee.Message != `invalid regular expression option: "1"` {
		t.Errorf("Message=%q want %q", ee.Message, `invalid regular expression option: "1"`)
	}
	if ee.Hint != regexpReplaceStartHint {
		t.Errorf("Hint=%q want %q", ee.Hint, regexpReplaceStartHint)
	}
}

// TestRegexpReplaceDigitFirstMulticharFlagPrintsWholeOpt: PG prints the WHOLE
// opt string (pg_mblen_range, regexp.c:682), so a digit-first multi-char flag
// must not truncate to just its first rune ("1z", not "1").
func TestRegexpReplaceDigitFirstMulticharFlagPrintsWholeOpt(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, err := runQueryErr(t, ctx, `SELECT regexp_replace('A PostgreSQL function', 'a|e|i|o|u', 'X', '1z')`)
	if err == nil {
		t.Fatal("expected error, got none")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T, want *ExecError (err=%v)", err, err)
	}
	if ee.Code != "22023" {
		t.Errorf("SQLSTATE=%s want 22023", ee.Code)
	}
	if ee.Message != `invalid regular expression option: "1z"` {
		t.Errorf("Message=%q want %q", ee.Message, `invalid regular expression option: "1z"`)
	}
	if ee.Hint != regexpReplaceStartHint {
		t.Errorf("Hint=%q want %q", ee.Hint, regexpReplaceStartHint)
	}
}

// TestRegexpReplaceNonDigitInvalidFlagUnhinted: a non-digit invalid flag keeps
// PG's pre-existing behavior — same 22023 error, but NO hint (parse_re_flags
// default case; the generic arm already handles this correctly, so the new
// guard must not hint it).
func TestRegexpReplaceNonDigitInvalidFlagUnhinted(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, err := runQueryErr(t, ctx, `SELECT regexp_replace('A PostgreSQL function', 'a|e|i|o|u', 'X', 'z')`)
	if err == nil {
		t.Fatal("expected error, got none")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T, want *ExecError (err=%v)", err, err)
	}
	if ee.Code != "22023" {
		t.Errorf("SQLSTATE=%s want 22023", ee.Code)
	}
	if ee.Message != `invalid regular expression option: "z"` {
		t.Errorf("Message=%q want %q", ee.Message, `invalid regular expression option: "z"`)
	}
	if ee.Hint != "" {
		t.Errorf("Hint=%q want empty (no hint for non-digit flag)", ee.Hint)
	}
}

// TestRegexpMatchesDigitFirstFlagUnhinted: the digit-first HINT must NOT leak
// into the shared pgRegexFlagsToGoModifiers helper — other regexp_* functions
// (here regexp_matches, 3-arg flags form) raise the same 22023 error with no
// hint, matching parse_re_flags (regexp.c:442-446). Regression guard proving
// the fix stayed single-site.
func TestRegexpMatchesDigitFirstFlagUnhinted(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, err := runQueryErr(t, ctx, `SELECT regexp_matches('abc', 'a', '1')`)
	if err == nil {
		t.Fatal("expected error, got none")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T, want *ExecError (err=%v)", err, err)
	}
	if ee.Code != "22023" {
		t.Errorf("SQLSTATE=%s want 22023", ee.Code)
	}
	if ee.Message != `invalid regular expression option: "1"` {
		t.Errorf("Message=%q want %q", ee.Message, `invalid regular expression option: "1"`)
	}
	if ee.Hint != "" {
		t.Errorf("Hint=%q want empty (shared helper must not over-hint)", ee.Hint)
	}
}
