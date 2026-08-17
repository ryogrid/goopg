package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestEvalSubstr pins PostgreSQL-compatible substr() semantics for the
// shapes HammerDB TPC-H Q22 uses (`substr(c_phone, 1, 2)`) plus a few
// edge cases (NULL propagation, out-of-range start, two-argument form).
func TestEvalSubstr(t *testing.T) {
	cases := []struct {
		name string
		args []optimizer.Expr
		want Datum
	}{
		{
			name: "Q22: country code prefix",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "13-123-456-7890"},
				&optimizer.IntegerConst{Value: 1},
				&optimizer.IntegerConst{Value: 2},
			},
			want: NewStringDatum("13"),
		},
		{
			name: "two-argument tail form",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "abcdef"},
				&optimizer.IntegerConst{Value: 3},
			},
			want: NewStringDatum("cdef"),
		},
		{
			name: "start beyond end returns empty",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "abc"},
				&optimizer.IntegerConst{Value: 10},
				&optimizer.IntegerConst{Value: 5},
			},
			want: NewStringDatum(""),
		},
		{
			name: "count truncates to string length",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "abc"},
				&optimizer.IntegerConst{Value: 1},
				&optimizer.IntegerConst{Value: 99},
			},
			want: NewStringDatum("abc"),
		},
		{
			name: "NULL src propagates",
			args: []optimizer.Expr{
				&optimizer.NullConst{},
				&optimizer.IntegerConst{Value: 1},
				&optimizer.IntegerConst{Value: 2},
			},
			want: NullDatum,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &optimizer.FuncCall{Name: "substr", Args: c.args}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if got.Kind != c.want.Kind || got.StringValue() != c.want.StringValue() {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestEvalSubstrNegativeCount errors per PG's behavior (SQLSTATE 22011
// "negative substring length not allowed").
func TestEvalSubstrNegativeCount(t *testing.T) {
	fc := &optimizer.FuncCall{
		Name: "substr",
		Args: []optimizer.Expr{
			&optimizer.StringConst{Value: "abc"},
			&optimizer.IntegerConst{Value: 1},
			&optimizer.IntegerConst{Value: -1},
		},
	}
	if _, err := evalFuncCall(fc, nil, &Context{}); err == nil {
		t.Fatalf("expected negative-length error, got nil")
	}
}

// TestEvalToDate pins to_date() to the YYYY-MM-DD form HammerDB
// TPC-H Q15 uses, plus a NULL-propagation case.
func TestEvalToDate(t *testing.T) {
	fc := &optimizer.FuncCall{
		Name: "to_date",
		Args: []optimizer.Expr{
			&optimizer.StringConst{Value: "1996-01-01"},
			&optimizer.StringConst{Value: "YYYY-MM-DD"},
		},
	}
	got, err := evalFuncCall(fc, nil, &Context{})
	if err != nil {
		t.Fatalf("evalFuncCall: %v", err)
	}
	if got.Kind != KindTime {
		t.Fatalf("got kind=%v, want KindTime", got.Kind)
	}
	year, month, day := got.TimeValue().Date()
	if year != 1996 || month != 1 || day != 1 {
		t.Fatalf("got %v, want 1996-01-01", got.TimeValue())
	}
	if h, m, s := got.TimeValue().Clock(); h != 0 || m != 0 || s != 0 {
		t.Fatalf("got time-of-day %02d:%02d:%02d, want 00:00:00", h, m, s)
	}

	null := &optimizer.FuncCall{
		Name: "to_date",
		Args: []optimizer.Expr{
			&optimizer.NullConst{},
			&optimizer.StringConst{Value: "YYYY-MM-DD"},
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
