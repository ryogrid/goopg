package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRangeTypeInputAndConstructors is the M0134-0173 guard.
//
// Before this fix goopg had NO evalCast arm for any range type, so every case
// in the "input" table below either succeeded when PG raises, or stored a
// non-canonical spelling that made two equal ranges compare unequal; and the
// six built-in range constructors were 42883 despite their pg_proc rows being
// present in goopg's catalog seed.
//
// Every expectation here was captured from a live PostgreSQL 18.3 oracle
// (postgres/local_install, `psql -f`), not derived from the source.
func TestRangeTypeInputAndConstructors(t *testing.T) {
	t.Run("canonicalization and parsing", func(t *testing.T) {
		cases := []struct {
			typ, in, want string
		}{
			// int4range/int8range/daterange carry a canonical proc, so every
			// value normalises to `[)` (int4range_canonical, rangetypes.c).
			{"int4range", "[1,4)", "[1,4)"},
			{"int4range", "[1,4]", "[1,5)"},
			{"int4range", "(1,4)", "[2,4)"},
			{"int4range", "(1,4]", "[2,5)"},
			{"int4range", "[1,1)", "empty"},
			{"int4range", "[1,1]", "[1,2)"},
			{"int8range", "[1,4]", "[1,5)"},
			{"daterange", "[2020-01-01,2020-02-01]", "[2020-01-01,2020-02-02)"},
			// numrange has rngcanonical = 0 — bounds are kept verbatim.
			{"numrange", "[1.5,4.5]", "[1.5,4.5]"},
			{"numrange", "(1.5,4.5)", "(1.5,4.5)"},
			// Infinite bounds, in every combination, are never inclusive.
			{"int4range", "(,4)", "(,4)"},
			{"int4range", "[1,)", "[1,)"},
			{"int4range", "(,)", "(,)"},
			{"int4range", "[,]", "(,)"},
			// "empty" is case-insensitive and whitespace-tolerant.
			{"int4range", "empty", "empty"},
			{"int4range", "  EMPTY  ", "empty"},
			// Bound de-quoting, then re-quoting only where needed.
			{"int4range", `["1","4")`, "[1,4)"},
			{"int4range", "[ 1 , 4 )", "[1,4)"},
			{"tsrange", "[2020-01-01,2020-02-01]", `["2020-01-01 00:00:00","2020-02-01 00:00:00"]`},
		}
		for _, c := range cases {
			got, err := rangeIn(c.typ, c.in, 0, nil)
			if err != nil {
				t.Errorf("rangeIn(%s, %q) error: %v; want %q", c.typ, c.in, err, c.want)
				continue
			}
			if got != c.want {
				t.Errorf("rangeIn(%s, %q) = %q, want %q", c.typ, c.in, got, c.want)
			}
		}
	})

	t.Run("malformed literals raise 22P02 with PG's DETAIL", func(t *testing.T) {
		cases := []struct {
			in, detail string
		}{
			{"garbage", "Missing left parenthesis or bracket."},
			{"empty junk", `Junk after "empty" key word.`},
			{"[1,4", "Unexpected end of input."},
			{"[1 4)", "Missing comma after lower bound."},
			{"[1,4)x", "Junk after right parenthesis or bracket."},
			{"[1,2,3)", "Too many commas."},
		}
		for _, c := range cases {
			_, err := rangeIn("int4range", c.in, 0, nil)
			ee, ok := err.(*ExecError)
			if !ok {
				t.Errorf("rangeIn(int4range, %q) = %v; want *ExecError 22P02", c.in, err)
				continue
			}
			if ee.Code != "22P02" {
				t.Errorf("rangeIn(int4range, %q) code = %s, want 22P02", c.in, ee.Code)
			}
			if ee.Detail != c.detail {
				t.Errorf("rangeIn(int4range, %q) detail = %q, want %q", c.in, ee.Detail, c.detail)
			}
		}
	})

	t.Run("lower bound above upper bound is 22000", func(t *testing.T) {
		_, err := rangeIn("int4range", "[5,1)", 0, nil)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "22000" {
			t.Fatalf("rangeIn(int4range, \"[5,1)\") = %v; want 22000", err)
		}
		if ee.Message != "range lower bound must be less than or equal to range upper bound" {
			t.Errorf("message = %q", ee.Message)
		}
	})

	t.Run("a bad bound raises the SUBTYPE's own error", func(t *testing.T) {
		// PG: `invalid input syntax for type integer: "a"` — int4in's error,
		// not a range error. The bound must therefore go through the subtype's
		// input function, not be accepted as opaque text.
		_, err := rangeIn("int4range", "[a,4)", 0, nil)
		ee, ok := err.(*ExecError)
		if !ok {
			t.Fatalf("rangeIn(int4range, \"[a,4)\") = %v; want an ExecError", err)
		}
		if ee.Message != `invalid input syntax for type integer: "a"` {
			t.Errorf("message = %q, want int4in's message", ee.Message)
		}
	})

	t.Run("constructors", func(t *testing.T) {
		cases := []struct {
			name string
			args []Datum
			want string
		}{
			{"int4range", []Datum{NewIntDatum(1), NewIntDatum(4)}, "[1,4)"},
			{"int4range", []Datum{NewIntDatum(1), NewIntDatum(4), NewStringDatum("[]")}, "[1,5)"},
			{"int4range", []Datum{NewIntDatum(1), NewIntDatum(4), NewStringDatum("()")}, "[2,4)"},
			{"int4range", []Datum{NewIntDatum(1), NewIntDatum(4), NewStringDatum("(]")}, "[2,5)"},
			// proisstrict = 'f': a NULL bound is an INFINITE bound.
			{"int4range", []Datum{NullDatum, NewIntDatum(4)}, "(,4)"},
			{"int4range", []Datum{NewIntDatum(1), NullDatum}, "[1,)"},
			{"int4range", []Datum{NullDatum, NullDatum}, "(,)"},
			// numrange keeps its bounds' scale — it has no canonical proc, and
			// the bound must render through numeric's output function (a
			// KindNumeric datum is not a string; taking StringValue gives "").
			{"numrange", []Datum{numericFromInt(1), numericFromInt(4)}, "[1,4)"},
			{"int8range", []Datum{NewIntDatum(1), NewIntDatum(4), NewStringDatum("[]")}, "[1,5)"},
		}
		for _, c := range cases {
			got, err := evalRangeConstructor(c.name, c.args, 0, nil)
			if err != nil {
				t.Errorf("%s(%v) error: %v; want %q", c.name, c.args, err, c.want)
				continue
			}
			if got.StringValue() != c.want {
				t.Errorf("%s(%v) = %q, want %q", c.name, c.args, got.StringValue(), c.want)
			}
		}
	})

	t.Run("constructor errors", func(t *testing.T) {
		if _, err := evalRangeConstructor("int4range",
			[]Datum{NewIntDatum(4), NewIntDatum(1)}, 0, nil); err == nil {
			t.Error("int4range(4,1) succeeded; want 22000 lower > upper")
		}
		_, err := evalRangeConstructor("int4range",
			[]Datum{NewIntDatum(1), NewIntDatum(4), NewStringDatum("x")}, 0, nil)
		ee, ok := err.(*ExecError)
		if !ok || ee.Message != "invalid range bound flags" {
			t.Errorf("int4range(1,4,'x') = %v; want \"invalid range bound flags\"", err)
		} else if ee.Hint != `Valid values are "[]", "[)", "(]", and "()".` {
			t.Errorf("hint = %q", ee.Hint)
		}
		_, err = evalRangeConstructor("int4range",
			[]Datum{NewIntDatum(1), NewIntDatum(4), NullDatum}, 0, nil)
		ee, ok = err.(*ExecError)
		if !ok || ee.Message != "range constructor flags argument must not be null" {
			t.Errorf("int4range(1,4,NULL) = %v; want the null-flags error", err)
		}
		// int4range_canonical raises rather than wrapping at the subtype's max.
		_, err = evalRangeConstructor("int4range",
			[]Datum{NewIntDatum(2147483647), NewIntDatum(2147483647), NewStringDatum("[]")}, 0, nil)
		ee, ok = err.(*ExecError)
		if !ok || ee.Code != "22003" || ee.Message != "integer out of range" {
			t.Errorf("int4range(int4max,int4max,'[]') = %v; want 22003 integer out of range", err)
		}
	})

	t.Run("cast path is wired to rangeIn", func(t *testing.T) {
		// The whole point of the fix: `::int4range` must NOT be a
		// pass-through. This is the arm at the foot of evalCast.
		got, err := evalCast(NewStringDatum("[1,4]"), "int4range", 0, nil)
		if err != nil {
			t.Fatalf("'[1,4]'::int4range: %v", err)
		}
		if got.StringValue() != "[1,5)" {
			t.Errorf("'[1,4]'::int4range = %q, want \"[1,5)\"", got.StringValue())
		}
		if _, err := evalCast(NewStringDatum("garbage"), "int4range", 0, nil); err == nil {
			t.Error("'garbage'::int4range succeeded; want 22P02")
		}
	})

	t.Run("evalFuncCall dispatches the constructors", func(t *testing.T) {
		// The second wiring point: goopg's pg_proc seed has carried the twelve
		// range_constructor2/3 rows all along, so the catalog resolved the name
		// while the executor's switch had no case for it — `SELECT int4range(1,4)`
		// was 42883. Guard the dispatch, not just the helper.
		fc := &optimizer.FuncCall{
			Name: "int4range",
			Args: []optimizer.Expr{
				&optimizer.IntegerConst{Value: 1},
				&optimizer.IntegerConst{Value: 4},
				&optimizer.StringConst{Value: "[]"},
			},
		}
		got, err := evalFuncCall(fc, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall(int4range(1,4,'[]')): %v", err)
		}
		if got.StringValue() != "[1,5)" {
			t.Errorf("int4range(1,4,'[]') = %q, want \"[1,5)\"", got.StringValue())
		}
		// pg_catalog-qualified spelling reaches the same case (the switch runs
		// after evalFuncCall's pg_catalog. strip) — stats_import.sql writes it
		// that way throughout.
		fc.Name = "pg_catalog.daterange"
		fc.Args = []optimizer.Expr{
			&optimizer.StringConst{Value: "2020-01-01"},
			&optimizer.StringConst{Value: "2020-02-01"},
			&optimizer.StringConst{Value: "[]"},
		}
		got, err = evalFuncCall(fc, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall(pg_catalog.daterange(...)): %v", err)
		}
		if got.StringValue() != "[2020-01-01,2020-02-02)" {
			t.Errorf("daterange(...,'[]') = %q, want \"[2020-01-01,2020-02-02)\"", got.StringValue())
		}
	})
}
