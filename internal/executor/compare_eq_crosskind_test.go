package executor

import (
	"testing"
	"time"
)

// TestCompareEqCrossKindStringLiteral pins that compareEq — the
// equality oracle behind IN-list membership (evalInExpr) and CASE —
// coerces a bare, unknown-typed string literal to the other operand's
// type, the same way the plain `=` operator does.
//
// Why this test guards a real bug: TPC-DS Q83 gates all three of its
// CTEs on `d_date in ('2001-07-13','2001-09-10','2001-11-16')` and
// returned 0 rows on goopg where PostgreSQL returns 22. The `=` form
// of the same comparison was already correct, because BinaryOp routes
// through compareDatum → promoteCrossKind → tryParseStringAs, which
// parses the literal. compareEq had no KindTime↔KindString arm and
// fell through to its unconditional not-equal return, so every
// date-versus-literal IN test was silently false.
//
// PostgreSQL resolves this at parse time instead (parse_expr.c
// transformAExprIn → select_common_type → coerce_to_common_type).
// goopg types a bare StringConst as `unknown` and resolves coercion at
// runtime by design — see docs/design/root-0019-unknown-literal-coercion.md
// — so the coercion belongs here, in the executor.
func TestCompareEqCrossKindStringLiteral(t *testing.T) {
	date := func(s string) Datum {
		tm, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return NewDateDatum(tm)
	}
	str := func(s string) Datum { return NewStringDatum(s) }

	cases := []struct {
		name string
		a, b Datum
		want bool
	}{
		{"date == matching literal", date("2001-07-13"), str("2001-07-13"), true},
		{"literal == matching date", str("2001-07-13"), date("2001-07-13"), true},
		{"date != other literal", date("2001-07-13"), str("2001-09-10"), false},
		{"date != unparseable literal", date("2001-07-13"), str("not-a-date"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := compareEq(c.a, c.b)
			if err != nil {
				t.Fatalf("compareEq returned error: %v", err)
			}
			if got.IsNull() {
				t.Fatalf("compareEq returned NULL for non-null operands")
			}
			if got.BoolValue() != c.want {
				t.Errorf("compareEq = %v, want %v", got.BoolValue(), c.want)
			}
		})
	}
}

// TestCompareEqNullAndSameKindUnchanged pins that the cross-kind
// fallback did not disturb the pre-existing arms: NULL still
// short-circuits to NULL (not false), and same-kind comparisons still
// take their dedicated paths.
func TestCompareEqNullAndSameKindUnchanged(t *testing.T) {
	got, err := compareEq(NullDatum, NewStringDatum("x"))
	if err != nil {
		t.Fatalf("compareEq(NULL, 'x') errored: %v", err)
	}
	if !got.IsNull() {
		t.Errorf("compareEq(NULL, 'x') = %v, want NULL", got)
	}

	got, err = compareEq(NewStringDatum("a"), NewStringDatum("a"))
	if err != nil {
		t.Fatalf("compareEq('a','a') errored: %v", err)
	}
	if !got.BoolValue() {
		t.Errorf("compareEq('a','a') = false, want true")
	}

	got, err = compareEq(Datum{Kind: KindInt, Int: 7}, Datum{Kind: KindInt, Int: 8})
	if err != nil {
		t.Fatalf("compareEq(7,8) errored: %v", err)
	}
	if got.BoolValue() {
		t.Errorf("compareEq(7,8) = true, want false")
	}
}
