package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// TestEvalSubstr pins PostgreSQL-compatible substr() semantics for the
// shapes HammerDB TPC-H Q22 uses (`substr(c_phone, 1, 2)`) plus a few
// edge cases (NULL propagation, out-of-range start, two-argument form).
func TestEvalSubstr(t *testing.T) {
	cases := []struct {
		name string
		args []planner.Expr
		want Datum
	}{
		{
			name: "Q22: country code prefix",
			args: []planner.Expr{
				&planner.StringConst{Value: "13-123-456-7890"},
				&planner.IntegerConst{Value: 1},
				&planner.IntegerConst{Value: 2},
			},
			want: Datum{Kind: KindString, String: "13"},
		},
		{
			name: "two-argument tail form",
			args: []planner.Expr{
				&planner.StringConst{Value: "abcdef"},
				&planner.IntegerConst{Value: 3},
			},
			want: Datum{Kind: KindString, String: "cdef"},
		},
		{
			name: "start beyond end returns empty",
			args: []planner.Expr{
				&planner.StringConst{Value: "abc"},
				&planner.IntegerConst{Value: 10},
				&planner.IntegerConst{Value: 5},
			},
			want: Datum{Kind: KindString, String: ""},
		},
		{
			name: "count truncates to string length",
			args: []planner.Expr{
				&planner.StringConst{Value: "abc"},
				&planner.IntegerConst{Value: 1},
				&planner.IntegerConst{Value: 99},
			},
			want: Datum{Kind: KindString, String: "abc"},
		},
		{
			name: "NULL src propagates",
			args: []planner.Expr{
				&planner.NullConst{},
				&planner.IntegerConst{Value: 1},
				&planner.IntegerConst{Value: 2},
			},
			want: NullDatum,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &planner.FuncCall{Name: "substr", Args: c.args}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if got.Kind != c.want.Kind || got.String != c.want.String {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestEvalSubstrNegativeCount errors per PG's behavior (SQLSTATE 22011
// "negative substring length not allowed").
func TestEvalSubstrNegativeCount(t *testing.T) {
	fc := &planner.FuncCall{
		Name: "substr",
		Args: []planner.Expr{
			&planner.StringConst{Value: "abc"},
			&planner.IntegerConst{Value: 1},
			&planner.IntegerConst{Value: -1},
		},
	}
	if _, err := evalFuncCall(fc, nil, &Context{}); err == nil {
		t.Fatalf("expected negative-length error, got nil")
	}
}

// TestEvalToDate pins to_date() to the YYYY-MM-DD form HammerDB
// TPC-H Q15 uses, plus a NULL-propagation case.
func TestEvalToDate(t *testing.T) {
	fc := &planner.FuncCall{
		Name: "to_date",
		Args: []planner.Expr{
			&planner.StringConst{Value: "1996-01-01"},
			&planner.StringConst{Value: "YYYY-MM-DD"},
		},
	}
	got, err := evalFuncCall(fc, nil, &Context{})
	if err != nil {
		t.Fatalf("evalFuncCall: %v", err)
	}
	if got.Kind != KindTime {
		t.Fatalf("got kind=%v, want KindTime", got.Kind)
	}
	year, month, day := got.Time.Date()
	if year != 1996 || month != 1 || day != 1 {
		t.Fatalf("got %v, want 1996-01-01", got.Time)
	}
	if h, m, s := got.Time.Clock(); h != 0 || m != 0 || s != 0 {
		t.Fatalf("got time-of-day %02d:%02d:%02d, want 00:00:00", h, m, s)
	}

	null := &planner.FuncCall{
		Name: "to_date",
		Args: []planner.Expr{
			&planner.NullConst{},
			&planner.StringConst{Value: "YYYY-MM-DD"},
		},
	}
	gotNull, err := evalFuncCall(null, nil, &Context{})
	if err != nil {
		t.Fatalf("evalFuncCall(null): %v", err)
	}
	if !gotNull.IsNull() {
		t.Fatalf("got %+v, want NULL", gotNull)
	}
}
