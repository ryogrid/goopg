package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestSubstringSimilarEscape pins the SQL:2003 SUBSTRING(str SIMILAR pattern
// ESCAPE escape) form end-to-end (parse → constant-fold → evalSubstrRegex),
// against postgres/src/test/regress/expected/strings.out ("T581 regular
// expression substring"). M0134-0070.
func TestSubstringSimilarEscape(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql      string
		wantNull bool
		want     string
	}{
		{sql: `select substring('abcdefg' similar 'a#"(b_d)#"%' escape '#')`, want: "bcd"},
		{sql: `select substring('abcdefg' similar '#"(b_d)#"%' escape '#')`, wantNull: true},
		{sql: `select substring('abcdefg' similar '%' escape NULL)`, wantNull: true},
		{sql: `select substring(NULL similar '%' escape '#')`, wantNull: true},
		{sql: `select substring('abcdefg' similar NULL escape '#')`, wantNull: true},
		{sql: `select substring('abcdefg' similar 'a#"%#"g' escape '#')`, want: "bcdef"},
		{sql: `select substring('abcdefg' similar 'a|b#"%#"g' escape '#')`, want: "bcdef"},
		{sql: `select substring('abcdefg' similar 'a#"%#"x|g' escape '#')`, want: "bcdef"},
		{sql: `select substring('abcdefg' similar 'a#"%|ab#"g' escape '#')`, want: "bcdef"},
		{sql: `select substring('abcdefg' similar 'a#"%g' escape '#')`, want: "bcdefg"},
		{sql: `select substring('abcdefg' similar 'a%g' escape '#')`, want: "abcdefg"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			advanceStmtCounter(ctx)
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("Plan(%q): %v", tc.sql, err)
			}
			op, err := Build(plan)
			if err != nil {
				t.Fatalf("Build(%q): %v", tc.sql, err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("Open(%q): %v", tc.sql, err)
			}
			rows, err := drainScan(op)
			_ = op.Close()
			if err != nil {
				t.Fatalf("exec(%q): %v", tc.sql, err)
			}
			if len(rows) != 1 || len(rows[0]) != 1 {
				t.Fatalf("%q: want 1x1 result, got %d rows", tc.sql, len(rows))
			}
			d := rows[0][0]
			if tc.wantNull {
				if !d.IsNull() {
					t.Errorf("%q: got %q, want NULL", tc.sql, d.StringValue())
				}
				return
			}
			if d.IsNull() {
				t.Fatalf("%q: got NULL, want %q", tc.sql, tc.want)
			}
			if got := d.StringValue(); got != tc.want {
				t.Errorf("%q: got %q, want %q (PG 18.3)", tc.sql, got, tc.want)
			}
		})
	}
}

// TestSubstringSimilarEscapeGreedyStarAmbiguity documents (does NOT fix — see
// M0134-0070 brief's Escalate criterion 1) a discovered PG-semantics gap:
// `SUBSTRING('abcdefg' SIMILAR 'a*#"%#"g*' ESCAPE '#')` expects "abcdefg"
// (strings.out:504-507) because PG's ARE engine resolves the part1/part3
// `a*`/`g*` ambiguity via true POSIX leftmost-longest matching with
// subexpression-greediness tiebreak (both divisions of the input between
// part1's `a*` and part2 produce the same *overall* match length, so the
// `{1,1}?` non-greedy marker on part1 picks the division that lets `a*`
// match zero characters). Go's regexp.Compile implements Perl-style
// leftmost-first (priority) matching, not POSIX leftmost-longest, so it
// instead returns "bcdefg" (part1's `a*` greedily consumes the leading "a"
// on its first — and here only-tried — attempt, since that division already
// satisfies the rest of the pattern with no need to backtrack for equal
// length). Go's regexp.CompilePOSIX does implement leftmost-longest
// matching but rejects the `{1,1}?` non-greedy syntax entirely (POSIX ERE
// has no non-greedy operator), so it cannot compile PG's converted pattern
// either — there is no drop-in fix within Go's stdlib regexp package. This
// is a regex-engine-level gap (affects any pattern where the escape-quote
// conversion's part1/part3 greediness matters and the input is ambiguous
// under leftmost-first matching), not specific to SUBSTRING-SIMILAR-ESCAPE's
// pattern conversion — out of scope for this slice per the brief's Escalate
// criterion 1.
func TestSubstringSimilarEscapeGreedyStarAmbiguity(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	sql := `select substring('abcdefg' similar 'a*#"%#"g*' escape '#')`
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open(%q): %v", sql, err)
	}
	rows, err := drainScan(op)
	_ = op.Close()
	if err != nil {
		t.Fatalf("exec(%q): %v", sql, err)
	}
	got := rows[0][0].StringValue()
	const pgWant = "abcdefg"
	if got == pgWant {
		t.Fatalf("got %q == PG's expected value — the Go regexp leftmost-first/POSIX-leftmost-longest gap this test documents appears to be resolved; update/remove this test and re-check strings.out convergence", got)
	}
	if got != "bcdefg" {
		t.Fatalf("got %q, want the documented divergent value %q (PG expects %q)", got, "bcdefg", pgWant)
	}
}

// TestSubstringSimilarEscapeTooManySeparators pins ERROR 2200C for a
// pattern with more than two escape-double-quote separators, raised at
// parse time. PG oracle: regexp.c:940-944.
func TestSubstringSimilarEscapeTooManySeparators(t *testing.T) {
	sql := `select substring('abcdefg' similar 'a*#"%#"g*#"x' escape '#')`
	_, err := parser.Parse(sql)
	if err == nil {
		t.Fatal("Parse: want error, got nil")
	}
	se, ok := err.(*parser.SyntaxError)
	if !ok {
		t.Fatalf("err=%T(%v), want *parser.SyntaxError", err, err)
	}
	if se.Code != "2200C" {
		t.Errorf("Code=%q, want 2200C", se.Code)
	}
}
