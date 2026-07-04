package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// TestEvalRegexpMatch pins PostgreSQL's regexp_match/regexp_matches element
// semantics (postgres/src/backend/utils/adt/regexp.c setup_regexp_matches):
// with capture groups, the array holds the GROUPS, not the overall match;
// with no groups, the sole element is the whole match; a non-participating
// group (losing side of an alternation) is SQL NULL. Both function names
// share one non-SRF scalar path (regexp_matches' 'g'-flag/multi-row SRF
// expansion is out of scope here — see .ralph/deferral_ledger.md).
func TestEvalRegexpMatch(t *testing.T) {
	cases := []struct {
		name string
		fn   string
		args []planner.Expr
		want Datum
	}{
		{
			name: "no capture group returns whole match",
			fn:   "regexp_match",
			args: []planner.Expr{
				&planner.StringConst{Value: "foobar123"},
				&planner.StringConst{Value: "[0-9]+"},
			},
			want: NewStringDatum("{123}"),
		},
		{
			name: "single capture group returns the group, not the full match",
			fn:   "regexp_matches",
			args: []planner.Expr{
				&planner.StringConst{Value: "foo123bar"},
				&planner.StringConst{Value: "foo([0-9]+)bar"},
			},
			want: NewStringDatum("{123}"),
		},
		{
			name: "multiple capture groups",
			fn:   "regexp_matches",
			args: []planner.Expr{
				&planner.StringConst{Value: "2026-07-04"},
				&planner.StringConst{Value: "([0-9]+)-([0-9]+)-([0-9]+)"},
			},
			want: NewStringDatum("{2026,07,04}"),
		},
		{
			name: "non-participating group is NULL",
			fn:   "regexp_matches",
			args: []planner.Expr{
				&planner.StringConst{Value: "b"},
				&planner.StringConst{Value: "(a)|(b)"},
			},
			want: NewStringDatum("{NULL,b}"),
		},
		{
			name: "no match returns NULL",
			fn:   "regexp_matches",
			args: []planner.Expr{
				&planner.StringConst{Value: "abc"},
				&planner.StringConst{Value: "[0-9]+"},
			},
			want: NullDatum,
		},
		{
			name: "case-insensitive flag",
			fn:   "regexp_matches",
			args: []planner.Expr{
				&planner.StringConst{Value: "HELLO"},
				&planner.StringConst{Value: "hello"},
				&planner.StringConst{Value: "i"},
			},
			want: NewStringDatum("{HELLO}"),
		},
		{
			name: "NULL input propagates",
			fn:   "regexp_matches",
			args: []planner.Expr{
				&planner.NullConst{},
				&planner.StringConst{Value: "x"},
			},
			want: NullDatum,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &planner.FuncCall{Name: c.fn, Args: c.args}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if got.IsNull() != c.want.IsNull() || got.StringValue() != c.want.StringValue() {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
