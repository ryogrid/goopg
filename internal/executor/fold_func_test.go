package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestFoldConstantsEvaluatesImmutableBuiltins pins M0146-0123:
// eval_const_expressions' evaluate_function folds an IMMUTABLE plain function
// over constant arguments at plan time (PG 18.3 plans `a = abs(-1)` as
// `(a = 1)` and `length('abc') = 4` as `One-Time Filter: false`). The
// overload the argument types select decides: length(text) is immutable
// although length(bytea, name) is stable. Aggregates, volatile and stable
// functions, NULL arguments and result types whose Datum does not round-trip
// through a literal stay calls.
func TestFoldConstantsEvaluatesImmutableBuiltins(t *testing.T) {
	call := func(name string, args ...optimizer.Expr) *optimizer.FuncCall {
		return &optimizer.FuncCall{Name: name, Args: args}
	}
	i := func(v int64) optimizer.Expr { return &optimizer.IntegerConst{Value: v} }
	str := func(v string) optimizer.Expr { return &optimizer.StringConst{Value: v} }

	if got, ok := optimizer.FoldConstants(call("abs", i(-7))).(*optimizer.IntegerConst); !ok || got.Value != 7 || got.Wide {
		t.Errorf("abs(-7) = %#v, want the int4 constant 7", optimizer.FoldConstants(call("abs", i(-7))))
	}
	if got, ok := optimizer.FoldConstants(call("length", str("abc"))).(*optimizer.IntegerConst); !ok || got.Value != 3 {
		t.Errorf("length('abc') = %#v, want 3", optimizer.FoldConstants(call("length", str("abc"))))
	}
	if got, ok := optimizer.FoldConstants(call("upper", str("x"))).(*optimizer.TypedStringLit); !ok || got.Type != "text" || got.Value != "X" {
		t.Errorf("upper('x') = %#v, want 'X'::text", optimizer.FoldConstants(call("upper", str("x"))))
	}
	// Nested: the inner call folds first, then the outer one sees a literal.
	if got, ok := optimizer.FoldConstants(call("abs", call("abs", i(-2)))).(*optimizer.IntegerConst); !ok || got.Value != 2 {
		t.Errorf("abs(abs(-2)) did not fold to 2")
	}
	for name, keep := range map[string]*optimizer.FuncCall{
		"max(1)":         call("max", i(1)),
		"random()":       call("random"),
		"abs(NULL)":      call("abs", &optimizer.NullConst{}),
		"pg_typeof(1)":   call("pg_typeof", i(1)),
		"now()":          call("now"),
		"pg_catalog.abs": call("pg_catalog.abs", i(-1)),
	} {
		if _, still := optimizer.FoldConstants(keep).(*optimizer.FuncCall); !still {
			t.Errorf("%s was folded; it must stay a call", name)
		}
	}
}
