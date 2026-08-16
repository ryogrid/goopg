package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
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
		args []optimizer.Expr
		want Datum
	}{
		{
			name: "removes every matching element",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "{a,b,a,c}"},
				&optimizer.StringConst{Value: "a"},
			},
			want: NewStringDatum("{b,c}"),
		},
		{
			name: "no match leaves array unchanged",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "{x,y,z}"},
				&optimizer.StringConst{Value: "a"},
			},
			want: NewStringDatum("{x,y,z}"),
		},
		{
			name: "pg_dump reloptions check_option strip",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "{security_barrier=true,check_option=local}"},
				&optimizer.StringConst{Value: "check_option=local"},
			},
			want: NewStringDatum("{security_barrier=true}"),
		},
		{
			name: "removing only element yields empty array",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "{check_option=cascaded}"},
				&optimizer.StringConst{Value: "check_option=cascaded"},
			},
			want: NewStringDatum("{}"),
		},
		{
			name: "NULL array propagates",
			args: []optimizer.Expr{
				&optimizer.NullConst{},
				&optimizer.StringConst{Value: "a"},
			},
			want: NullDatum,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &optimizer.FuncCall{Name: "array_remove", Args: c.args}
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
	inner := &optimizer.FuncCall{
		Name: "array_remove",
		Args: []optimizer.Expr{
			&optimizer.StringConst{Value: "{check_option=local,check_option=cascaded}"},
			&optimizer.StringConst{Value: "check_option=local"},
		},
	}
	outer := &optimizer.FuncCall{
		Name: "array_remove",
		Args: []optimizer.Expr{
			inner,
			&optimizer.StringConst{Value: "check_option=cascaded"},
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
