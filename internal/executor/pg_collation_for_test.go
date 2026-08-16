package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestPgCollationForEvalFuncCall exercises evalFuncCall's "pg_collation_for"
// case directly, covering both the plan-time-folded fast path (the normal
// path once a query goes through planner.resolveExpr's foldPgCollationFor,
// see internal/planner/pg_collation_for_test.go) and the runtime fallback
// used when some other resolver hands the executor an un-folded call.
// M0122-0005.
func TestPgCollationForEvalFuncCall(t *testing.T) {
	ctx := &Context{}

	t.Run("folded StringConst fast path", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_collation_for", Args: []optimizer.Expr{&optimizer.StringConst{Value: "default"}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.Kind != KindString || got.StringValue() != "default" {
			t.Errorf("got %#v, want KindString \"default\"", got)
		}
	})

	t.Run("folded NullConst fast path", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_collation_for", Args: []optimizer.Expr{&optimizer.NullConst{}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if !got.IsNull() {
			t.Errorf("got %#v, want NULL", got)
		}
	})

	t.Run("un-folded explicit CollateExpr", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_collation_for", Args: []optimizer.Expr{
			&optimizer.CollateExpr{Operand: &optimizer.StringConst{Value: "x"}, CollationName: "POSIX"},
		}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.Kind != KindString || got.StringValue() != `"POSIX"` {
			t.Errorf("got %#v, want KindString `\"POSIX\"`", got)
		}
	})

	t.Run("un-folded runtime non-string arg has no collation", func(t *testing.T) {
		// A raw *planner.IntegerConst can't reach here through the normal
		// planner.resolveExpr path (which always folds first, see
		// TestPgCollationForFolds's "non-collatable type errors 42804" case),
		// but pins the runtime fallback's behavior for any resolver that
		// bypasses the fold: no collation name is guessed for a non-string
		// runtime value.
		call := &optimizer.FuncCall{Name: "pg_collation_for", Args: []optimizer.Expr{&optimizer.IntegerConst{Value: 5}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if !got.IsNull() {
			t.Errorf("got %#v, want NULL", got)
		}
	})
}
