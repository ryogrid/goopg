package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// M0122-0012 slice 45: joinPredicateMatch — the per-pair check of the
// nested-loop and lateral streams — evaluates plan.Predicate compiled
// (evalFastExpr over a reused slot) instead of through evalExpr. The two
// evaluators are siblings and must agree on every pair; these tests pin that
// over a predicate matrix and pin that the compiled check allocates nothing
// per pair (the interpreted one boxed the row into an interface every call).

func TestJoinPredicateMatchCompiledMatchesInterpreted(t *testing.T) {
	cols, rows := unknownLitFixture() // d, ts, i, n, iv — mixed kinds, NULLs
	// A pair is the fixture row followed by a shifted copy of another row,
	// so the right half's columns are 5..9.
	right := func(c optimizer.Expr) optimizer.Expr {
		cr := *c.(*optimizer.ColumnRef)
		cr.Index += len(cols)
		return &cr
	}
	bin := func(op parser.OpCode, l, r optimizer.Expr) optimizer.Expr {
		return &optimizer.BinaryOp{Op: op, Left: l, Right: r}
	}
	preds := []optimizer.Expr{
		bin(parser.OpEq, cols[2], right(cols[2])),
		bin(parser.OpLt, cols[0], right(cols[0])),
		bin(parser.OpGe, cols[3], right(cols[3])),
		bin(parser.OpNe, cols[4], right(cols[4])),
		bin(parser.OpAnd, bin(parser.OpEq, cols[2], right(cols[2])), bin(parser.OpLe, cols[1], right(cols[1]))),
		bin(parser.OpOr, bin(parser.OpEq, cols[0], &optimizer.StringConst{Value: "2001-07-13"}), bin(parser.OpGt, cols[3], right(cols[3]))),
		&optimizer.InExpr{Operand: cols[2], List: []optimizer.Expr{right(cols[2]), &optimizer.IntegerConst{Value: 6}}},
		&optimizer.UnaryOp{Op: parser.OpNot, Operand: bin(parser.OpEq, cols[2], right(cols[2]))},
	}
	checked, matched := 0, 0
	for pi, pred := range preds {
		o := &joinOp{plan: &optimizer.Join{Predicate: pred}}
		for a := range rows {
			for b := range rows {
				pair := append(append(Row{}, rows[a]...), rows[b]...)
				v, wantErr := evalExpr(pred, pair, nil)
				want := wantErr == nil && !v.IsNull() && v.Kind == KindBool && v.BoolValue()
				got, gotErr := o.joinPredicateMatch(pair)
				if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
					t.Errorf("pred %d pair (%d,%d): compiled err %v, interpreted err %v", pi, a, b, gotErr, wantErr)
					continue
				}
				if got != want {
					t.Errorf("pred %d pair %v: compiled %v, interpreted %v", pi, fmt.Sprint(pair), got, want)
				}
				checked++
				if got {
					matched++
				}
			}
		}
	}
	if matched == 0 || matched == checked {
		t.Fatalf("matched %d of %d pairs: the matrix does not separate matches from rejects", matched, checked)
	}
}

func TestJoinPredicateMatchAllocatesNothingPerPair(t *testing.T) {
	ty := catalog.Type{Name: "int4"}
	pred := &optimizer.BinaryOp{Op: parser.OpEq,
		Left:  &optimizer.ColumnRef{Index: 0, Name: "a", Type: ty},
		Right: &optimizer.ColumnRef{Index: 2, Name: "b", Type: ty}}
	o := &joinOp{plan: &optimizer.Join{Predicate: pred}}
	pair := Row{NewIntDatum(7), NewIntDatum(1), NewIntDatum(8), NewIntDatum(2)}
	if _, err := o.joinPredicateMatch(pair); err != nil { // compiles the slab
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(200, func() {
		pair[2] = NewIntDatum(pair[2].Int + 1)
		if _, err := o.joinPredicateMatch(pair); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("joinPredicateMatch allocates %.1f per pair, want 0", allocs)
	}
}
