package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestSubstringRegexForm pins the SQL-standard regex form of SUBSTRING —
// `SUBSTRING(str FROM pattern)` where pattern is POSIX-regex text rather
// than an integer start position — against upstream's `textregexsubstr`
// (postgres/src/backend/utils/adt/regexp.c:583-627). Every expected value
// below was confirmed against a live PostgreSQL 18.3 oracle. M0134-0061.
func TestSubstringRegexForm(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql      string
		wantNull bool
		want     string
	}{
		// Whole match returned when the pattern has no parenthesized
		// subexpression.
		{sql: `select substring('foobar' from 'o.b')`, want: "oob"},
		// First parenthesized subexpression preferred over the whole match.
		{sql: `select substring('foobar' from 'o(.)b')`, want: "o"},
		// No match at all -> NULL.
		{sql: `select substring('foobar' from 'xyz')`, wantNull: true},
		// Subtlest case from the oracle's own comment: the pattern has a
		// subexpression that's present but did NOT participate in an
		// otherwise-successful overall match. Upstream returns NULL here
		// (so < 0 || eo < 0 for the subexpression), NOT the whole match.
		{sql: `select substring('foo' from 'foo(bar)?')`, wantNull: true},
		// Same pattern, but the subexpression DOES participate this time.
		{sql: `select substring('foobar' from 'foo(bar)?')`, want: "bar"},
		// Regression cases from regex.sql (M0134-0061 sizing pass bucket 4) —
		// these crashed real PG 9.2beta1 via pmatch[] overrun and are now
		// pinned as substring-regex-form correctness checks.
		{sql: `select substring('asd TO foo' from ' TO (([a-z0-9._]+|"([^"]+|"")+")+)')`, want: "foo"},
		{sql: `select substring('a' from '((a))+')`, want: "a"},
		{sql: `select substring('a' from '((a)+)')`, want: "a"},
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
			if d.Kind != KindString {
				t.Fatalf("%q: Kind = %d, want KindString", tc.sql, d.Kind)
			}
			if got := d.StringValue(); got != tc.want {
				t.Errorf("%q: got %q, want %q (PG 18.3)", tc.sql, got, tc.want)
			}
		})
	}
}

// TestSubstringNumericFormUnaffected guards against the branch condition in
// evalSubstr becoming too broad: the existing 3-arg numeric SUBSTRING(str
// FROM start FOR count) path must be completely unchanged by the new regex
// dispatch. M0134-0061.
func TestSubstringNumericFormUnaffected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{sql: `select substring('foobar' from 2 for 3)`, want: "oob"},
		{sql: `select substring('foobar' from 1)`, want: "foobar"},
		{sql: `select substr('abcdef', -2, 4)`, want: "a"},
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
			if got := rows[0][0].StringValue(); got != tc.want {
				t.Errorf("%q: got %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}
