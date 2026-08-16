package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
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
		args []optimizer.Expr
		want Datum
	}{
		{
			name: "no capture group returns whole match",
			fn:   "regexp_match",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "foobar123"},
				&optimizer.StringConst{Value: "[0-9]+"},
			},
			want: NewStringDatum("{123}"),
		},
		{
			name: "single capture group returns the group, not the full match",
			fn:   "regexp_matches",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "foo123bar"},
				&optimizer.StringConst{Value: "foo([0-9]+)bar"},
			},
			want: NewStringDatum("{123}"),
		},
		{
			name: "multiple capture groups",
			fn:   "regexp_matches",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "2026-07-04"},
				&optimizer.StringConst{Value: "([0-9]+)-([0-9]+)-([0-9]+)"},
			},
			want: NewStringDatum("{2026,07,04}"),
		},
		{
			name: "non-participating group is NULL",
			fn:   "regexp_matches",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "b"},
				&optimizer.StringConst{Value: "(a)|(b)"},
			},
			want: NewStringDatum("{NULL,b}"),
		},
		{
			name: "no match returns NULL",
			fn:   "regexp_matches",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "abc"},
				&optimizer.StringConst{Value: "[0-9]+"},
			},
			want: NullDatum,
		},
		{
			name: "case-insensitive flag",
			fn:   "regexp_matches",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "HELLO"},
				&optimizer.StringConst{Value: "hello"},
				&optimizer.StringConst{Value: "i"},
			},
			want: NewStringDatum("{HELLO}"),
		},
		{
			name: "NULL input propagates",
			fn:   "regexp_matches",
			args: []optimizer.Expr{
				&optimizer.NullConst{},
				&optimizer.StringConst{Value: "x"},
			},
			want: NullDatum,
		},
		{
			// PostgreSQL quotes an empty-string element as "" to distinguish it
			// from a zero-element array ({} vs {""}); a naive comma-join without
			// quoting collapses the two. postgres/src/backend/utils/adt/regexp.c.
			name: "empty pattern match yields a one-element array with an empty string",
			fn:   "regexp_match",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "abc"},
				&optimizer.StringConst{Value: ""},
			},
			want: NewStringDatum(`{""}`),
		},
		{
			// A matched element containing the array delimiter must be quoted so
			// it isn't misread as two elements (arrayfuncs.c array_out needquote).
			name: "matched text containing a comma is quoted",
			fn:   "regexp_match",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "a,b"},
				&optimizer.StringConst{Value: ".*"},
			},
			want: NewStringDatum(`{"a,b"}`),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &optimizer.FuncCall{Name: c.fn, Args: c.args}
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
