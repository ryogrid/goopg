package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRegexpReplaceExtendedForm pins the M0134-0070 Round F extended
// regexp_replace overloads — 5-arg (start), 5-arg (start, N), and 6-arg
// (start, N, flags) — against the strings.sql regress fixture values
// (postgres/src/test/regress/sql/strings.sql:~224-251,
// expected/strings.out:736-800), and confirms the existing 3/4-arg forms
// (already-passing before this round) are unchanged. pg_proc.dat:3755-3768
// (oids 2284/2285/6251/6252/6253).
func TestRegexpReplaceExtendedForm(t *testing.T) {
	intC := func(v int64) *optimizer.IntegerConst { return &optimizer.IntegerConst{Value: v} }
	strC := func(v string) *optimizer.StringConst { return &optimizer.StringConst{Value: v} }

	t.Run("happy paths", func(t *testing.T) {
		cases := []struct {
			name string
			args []optimizer.Expr
			want string
		}{
			// Criterion 1 / strings.out:737-739 — 4-arg int-start form
			// (oid 6253): replace only the 1st match found at/after start=1.
			{"4-arg start-only, 1st match", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("A|e|i|o|u"), strC("X"), intC(1),
			}, "X PostgreSQL function"},

			// Criterion 2 / strings.out:741-743 — 5-arg (start, N), no
			// flags (oid 6252): replace only the 2nd match.
			{"5-arg start+N, 2nd match", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("A|e|i|o|u"), strC("X"), intC(1), intC(2),
			}, "A PXstgreSQL function"},

			// Criterion 3 / strings.out:745-747 — 6-arg, N=0 means "all
			// matches at/after start", case-insensitive.
			{"6-arg N=0 replaces all, case-insensitive", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("a|e|i|o|u"), strC("X"), intC(1), intC(0), strC("i"),
			}, "X PXstgrXSQL fXnctXXn"},

			// strings.out:749-751 — N=1 with flags present.
			{"6-arg N=1, case-insensitive", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("a|e|i|o|u"), strC("X"), intC(1), intC(1), strC("i"),
			}, "X PostgreSQL function"},

			// strings.out:753-755 — N=2 with flags present.
			{"6-arg N=2, case-insensitive", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("a|e|i|o|u"), strC("X"), intC(1), intC(2), strC("i"),
			}, "A PXstgreSQL function"},

			// strings.out:757-759 — N=3.
			{"6-arg N=3, case-insensitive", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("a|e|i|o|u"), strC("X"), intC(1), intC(3), strC("i"),
			}, "A PostgrXSQL function"},

			// strings.out:761-763 — N exceeds match count: unchanged.
			{"6-arg N exceeds match count", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("a|e|i|o|u"), strC("X"), intC(1), intC(9), strC("i"),
			}, "A PostgreSQL function"},

			// strings.out:765-767 — start=7 skips earlier matches.
			{"6-arg start=7, N=0", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("A|e|i|o|u"), strC("X"), intC(7), intC(0), strC("i"),
			}, "A PostgrXSQL fXnctXXn"},

			// Criterion 4 / strings.out:769-771 — the decisive N-overrides-g
			// test: N=1 wins over the 'g' flag, only 1 match replaced.
			{"6-arg N=1 overrides 'g' flag", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("a|e|i|o|u"), strC("X"), intC(1), intC(1), strC("g"),
			}, "A PXstgreSQL function"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				fc := &optimizer.FuncCall{Name: "regexp_replace", Args: c.args}
				got, err := evalFuncCall(fc, nil, &Context{})
				if err != nil {
					t.Fatalf("evalFuncCall: %v", err)
				}
				if got.IsNull() {
					t.Fatalf("got NULL, want %q", c.want)
				}
				if got.StringValue() != c.want {
					t.Fatalf("got %q, want %q", got.StringValue(), c.want)
				}
			})
		}
	})

	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			name     string
			args     []optimizer.Expr
			wantCode string
			wantMsg  string
		}{
			// Criterion 5 / strings.out:773-774.
			{"start=-1", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("a|e|i|o|u"), strC("X"), intC(-1), intC(0), strC("i"),
			}, "22023", `invalid value for parameter "start": -1`},
			// Criterion 6 / strings.out:775-776.
			{"n=-1", []optimizer.Expr{
				strC("A PostgreSQL function"), strC("a|e|i|o|u"), strC("X"), intC(1), intC(-1), strC("i"),
			}, "22023", `invalid value for parameter "n": -1`},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				fc := &optimizer.FuncCall{Name: "regexp_replace", Args: c.args}
				_, err := evalFuncCall(fc, nil, &Context{})
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				ee, ok := err.(*ExecError)
				if !ok {
					t.Fatalf("err type=%T, want *ExecError", err)
				}
				if ee.Code != c.wantCode {
					t.Errorf("SQLSTATE=%s want %s", ee.Code, c.wantCode)
				}
				if ee.Message != c.wantMsg {
					t.Errorf("message=%q want %q", ee.Message, c.wantMsg)
				}
			})
		}
	})

	// Arity-ambiguity shapes: goopg's untyped AST can't distinguish PG's
	// two 4-arg overloads (oid 2285 flags-string vs oid 6253 int-start) by
	// arity alone, so it branches on the arg-3 Datum kind. Confirm both
	// sides of that branch still behave correctly, and that the existing
	// 3-arg/4-arg-flags forms (incl. backreferences) are unaffected.
	t.Run("arity-ambiguity and pre-existing forms unchanged", func(t *testing.T) {
		// 4-arg flags-string form (oid 2285) — pre-existing behavior, must
		// be unchanged: 'g' flag replaces every match.
		fc := &optimizer.FuncCall{Name: "regexp_replace", Args: []optimizer.Expr{
			strC("AAA aaa"), strC("A+"), strC("Z"), strC("gi"),
		}}
		got, err := evalFuncCall(fc, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.StringValue() != "Z Z" {
			t.Fatalf("4-arg flags form: got %q, want %q", got.StringValue(), "Z Z")
		}

		// 4-arg int-start form (oid 6253) — the new form colliding in
		// arity with the flags form above; disambiguated by Datum kind.
		// "banana" has 'a' at chars 2,4,6; start=3 skips the first and
		// replaces only the first match at/after char 3 (char 4), since
		// N always defaults to 1 with no flags arg present.
		fc = &optimizer.FuncCall{Name: "regexp_replace", Args: []optimizer.Expr{
			strC("banana"), strC("a"), strC("Z"), intC(3),
		}}
		got, err = evalFuncCall(fc, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.StringValue() != "banZna" {
			t.Fatalf("4-arg start-only form: got %q, want %q", got.StringValue(), "banZna")
		}

		// 3-arg form — replace only the first match (pre-existing behavior).
		fc = &optimizer.FuncCall{Name: "regexp_replace", Args: []optimizer.Expr{
			strC("A PostgreSQL function"), strC("A|e|i|o|u"), strC("X"),
		}}
		got, err = evalFuncCall(fc, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.StringValue() != "X PostgreSQL function" {
			t.Fatalf("3-arg form: got %q, want %q", got.StringValue(), "X PostgreSQL function")
		}

		// 3-arg form with \1/\2 backreferences in the replacement (the
		// Round-C-deferral scope — \1/\2-only — is untouched here; the
		// pattern itself has no backreference, only capture groups).
		fc = &optimizer.FuncCall{Name: "regexp_replace", Args: []optimizer.Expr{
			strC("555-1234"), strC(`(\d{3})-(\d{4})`), strC(`\2:\1`),
		}}
		got, err = evalFuncCall(fc, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.StringValue() != "1234:555" {
			t.Fatalf("3-arg form w/ backreference in replacement: got %q, want %q", got.StringValue(), "1234:555")
		}

		// 5-arg start-only shape (start present, N absent is NOT a legal
		// PG arity for regexp_replace at 5 args — pg_proc only defines
		// (start, N) at 5 args, oid 6252). Verify the 5-arg path treats
		// arg index 4 as N unconditionally, matching oid 6252's shape.
		fc = &optimizer.FuncCall{Name: "regexp_replace", Args: []optimizer.Expr{
			strC("abcabcabc"), strC("a.c"), strC("X"), intC(1), intC(2),
		}}
		got, err = evalFuncCall(fc, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.StringValue() != "abcXabc" {
			t.Fatalf("5-arg start+N form: got %q, want %q", got.StringValue(), "abcXabc")
		}
	})
}

// TestRegexpReplaceBackreferenceGrammar pins the M0134-0070 Round G
// generalized replacement-string escape grammar --
// postgres/src/backend/utils/adt/varlena.c:4357-4447
// (appendStringInfoRegexpSubstr): "\1".."\9" single-digit backreferences,
// "\&" whole-match, "\\" literal backslash, unrecognized "\c" passthrough,
// and a trailing lone "\". strings.sql:224 is the multi-digit-group
// (\1-\3) fixture that motivated this round (see Round F working-set entry).
func TestRegexpReplaceBackreferenceGrammar(t *testing.T) {
	strC := func(v string) *optimizer.StringConst { return &optimizer.StringConst{Value: v} }

	cases := []struct {
		name string
		args []optimizer.Expr
		want string
	}{
		// \1-\9 single-digit backrefs (only 2 groups used here; \3-\9
		// covered by the out-of-range case below).
		{"backreferences \\1 \\2 reordered", []optimizer.Expr{
			strC("foobarbaz"), strC("(bar)(baz)"), strC(`\2\1`),
		}, "foobazbar"},

		// strings.sql:224 fixture (expected/strings.out:694-697): 3
		// capture groups referenced in the replacement, the exact case
		// that motivated Round G (the prior \1/\2-only hardcoding had no
		// \3 handling at all).
		{"strings.sql:224 three-group replacement", []optimizer.Expr{
			strC("1112223333"), strC(`(\d{3})(\d{3})(\d{4})`), strC(`(\1) \2-\3`),
		}, "(111) 222-3333"},

		// \& -> whole match, doubled ('g' flag so all 3 chars are hit —
		// regexp_replace only replaces the first match without 'g').
		{"whole-match backreference doubled", []optimizer.Expr{
			strC("abc"), strC("."), strC(`\&\&`), strC("g"),
		}, "aabbcc"},

		// \\ -> literal backslash, does NOT consume the following char as
		// an escape target (varlena.c: "\\\\" case continues the loop
		// without falling through to normal-text copying of the next char
		// as anything special -- but the next char itself still gets
		// copied through normally on its own iteration).
		{"literal backslash before ordinary char", []optimizer.Expr{
			strC("abc"), strC("b"), strC(`x\\y`),
		}, "ax\\yc"},

		// \9 when only 2 groups exist in the pattern: PG's pmatch[9] guard
		// (so >= 0 && eo >= 0) yields "no substitution" (silent, not an
		// error). Go's regexp.Expand/ReplaceAllString independently returns
		// an empty string for an out-of-range submatch index, which
		// matches PG's net effect (verified via /tmp probe: both produce
		// empty output for the missing group -- no divergence to report).
		{"out-of-range group index \\9 is silent no-op", []optimizer.Expr{
			strC("ab"), strC("(a)(b)"), strC(`\9-tail`),
		}, "-tail"},

		// Unrecognized escape \a passes through literally (backslash + 'a'
		// unchanged).
		{"unrecognized escape passes through literally", []optimizer.Expr{
			strC("abc"), strC("b"), strC(`\a`),
		}, `a\ac`},

		// Trailing lone backslash at end of replacement string.
		{"trailing lone backslash", []optimizer.Expr{
			strC("abc"), strC("b"), strC(`x\`),
		}, `ax\c`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &optimizer.FuncCall{Name: "regexp_replace", Args: c.args}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if got.IsNull() {
				t.Fatalf("got NULL, want %q", c.want)
			}
			if got.StringValue() != c.want {
				t.Fatalf("got %q, want %q", got.StringValue(), c.want)
			}
		})
	}
}
