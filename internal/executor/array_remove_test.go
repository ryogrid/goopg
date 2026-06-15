package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// TestEvalArrayRemove pins PostgreSQL-compatible array_remove(anyarray,
// anyelement) semantics. pg_dump's getTables strips the view check_option
// markers from reloptions with a nested
// array_remove(array_remove(c.reloptions,'check_option=local'),
// 'check_option=cascaded'), so the nested/reloptions shapes are covered
// explicitly. M0110-0001 / DU-002 slice 5.
func TestEvalArrayRemove(t *testing.T) {
	cases := []struct {
		name string
		args []planner.Expr
		want Datum
	}{
		{
			name: "removes every matching element",
			args: []planner.Expr{
				&planner.StringConst{Value: "{a,b,a,c}"},
				&planner.StringConst{Value: "a"},
			},
			want: NewStringDatum("{b,c}"),
		},
		{
			name: "no match leaves array unchanged",
			args: []planner.Expr{
				&planner.StringConst{Value: "{x,y,z}"},
				&planner.StringConst{Value: "a"},
			},
			want: NewStringDatum("{x,y,z}"),
		},
		{
			name: "pg_dump reloptions check_option strip",
			args: []planner.Expr{
				&planner.StringConst{Value: "{security_barrier=true,check_option=local}"},
				&planner.StringConst{Value: "check_option=local"},
			},
			want: NewStringDatum("{security_barrier=true}"),
		},
		{
			name: "removing only element yields empty array",
			args: []planner.Expr{
				&planner.StringConst{Value: "{check_option=cascaded}"},
				&planner.StringConst{Value: "check_option=cascaded"},
			},
			want: NewStringDatum("{}"),
		},
		{
			name: "NULL array propagates",
			args: []planner.Expr{
				&planner.NullConst{},
				&planner.StringConst{Value: "a"},
			},
			want: NullDatum,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &planner.FuncCall{Name: "array_remove", Args: c.args}
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

// TestEvalArrayRemoveNested verifies the exact nested form pg_dump's getTables
// uses to strip both view check_option markers in one expression.
func TestEvalArrayRemoveNested(t *testing.T) {
	inner := &planner.FuncCall{
		Name: "array_remove",
		Args: []planner.Expr{
			&planner.StringConst{Value: "{check_option=local,check_option=cascaded}"},
			&planner.StringConst{Value: "check_option=local"},
		},
	}
	outer := &planner.FuncCall{
		Name: "array_remove",
		Args: []planner.Expr{
			inner,
			&planner.StringConst{Value: "check_option=cascaded"},
		},
	}
	got, err := evalFuncCall(outer, nil, &Context{})
	if err != nil {
		t.Fatalf("evalFuncCall: %v", err)
	}
	if want := "{}"; got.StringValue() != want {
		t.Fatalf("got %q, want %q", got.StringValue(), want)
	}
}
