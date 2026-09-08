package executor

// E-05 gate: twin-parity per arm on the merge-space corpus, through the
// shared outcome harness (panic + SQLSTATE + pos + message — value-only
// comparison cannot deliver error parity, and PS6.2's AND/OR-on-non-bool
// precedent is exactly the merge-residual risk class).

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// mergeParityRow is a joined (left++right) row: ints, a NULL, strings,
// a bool. Indexes mirror a two-column merge key on 0/3 with residual
// conjuncts over 1/4/2.
func mergeParityRow() Row {
	return Row{
		NewIntDatum(7),
		NewIntDatum(11),
		NullDatum,
		NewIntDatum(7),
		NewStringDatum("abc"),
		NewBoolDatum(true),
	}
}

func TestMergeTwinParityCorpus(t *testing.T) {
	// Runs through checkParityCorpus: parityAllSlots now includes the
	// mergedKeySlot the merge path evaluates through.
	pb := func(op parser.OpCode, l, r optimizer.Expr) *optimizer.BinaryOp {
		return &optimizer.BinaryOp{Op: op, Left: l, Right: r}
	}
	i := func(idx int) *optimizer.ColumnRef { return pcol(idx, "int4") }
	corpus := []parityCase{
		{"equi residual", pb(parser.OpEq, i(0), i(3))},
		{"equi conjunction", pb(parser.OpAnd, pb(parser.OpEq, i(0), i(3)), pb(parser.OpEq, i(1), i(1)))},
		{"non-equi", pb(parser.OpLt, i(0), i(3))},
		{"null AND true", pb(parser.OpAnd, pcol(2, "int4"), &optimizer.BooleanConst{Value: true})},
		{"null AND false", pb(parser.OpAnd, pcol(2, "int4"), &optimizer.BooleanConst{Value: false})},
		{"null OR col", pb(parser.OpOr, pcol(2, "bool"), pcol(5, "bool"))},
		{"div by zero", pb(parser.OpEq, pbin(parser.OpDiv, pint(1), pint(0)), pint(0))},
		{"func call", &optimizer.FuncCall{Name: "lower", Args: []optimizer.Expr{pcol(4, "text")}}},
		// No volatile case (`random()`): value parity is meaningless
		// across two evaluations by construction. Volatiles fall to
		// ExprAdapter on BOTH twins (same delegated call, same per-row
		// count) — verified by inspection of the buildExpr fallback,
		// not observable through outcomes.
		{"outer ref panics both sides", &optimizer.OuterColumnRef{Index: 0, Name: "x"}},
		{"out of range col", pcol(99, "int4")},
		{"ctid reads null", &optimizer.CTIDExpr{}},
		{"merge whole row ref", &optimizer.MergeWholeRowRef{}},
		{"is null on null", &optimizer.IsNullExpr{Operand: pcol(2, "int4")}},
		{"is distinct from null", &optimizer.IsDistinctFromExpr{Left: pcol(0, "int4"), Right: pcol(2, "int4")}},
	}
	checkParityCorpus(t, corpus, mergeParityRow())
}

// TestMergeResidualNoAllocs pins the E-05 alloc arm: the compiled
// residual path (hoisted slot, prebuilt nodes) allocates nothing per
// pair. Measured 0 B/op on first pinning (vs the interpreted path's
// per-eval dispatch); any regression fails here, not in profiles.
// Scope: the natively-compiled shape only — an ExprAdapter-fallback
// residual allocates via slotToRow materialization on the same path.
func TestMergeResidualNoAllocs(t *testing.T) {
	o := &joinOp{
		mergeKeys:     []optimizer.JoinKeyPair{{Left: pcol(0, "int4"), Right: pcol(3, "int4")}},
		mergeResidual: pbin(parser.OpEq, pcol(0, "int4"), pcol(3, "int4")),
	}
	o.compileMergeExprs()
	row := mergeParityRow()
	if n := testing.AllocsPerRun(100, func() {
		if _, err := o.mergeResidualMatch(row); err != nil {
			t.Fatalf("mergeResidualMatch: %v", err)
		}
	}); n != 0 {
		t.Fatalf("mergeResidualMatch allocates %v per pair, want 0", n)
	}
}

// TestMergeNilResidualShortCircuits pins the noExpr inversion hazard:
// routing nil through build→noExpr→eval yields NullDatum→false, so the
// explicit short-circuit to true must stay IN FRONT of eval. Dropping it
// zeroes every all-equi merge join.
func TestMergeNilResidualShortCircuits(t *testing.T) {
	o := &joinOp{}
	ok, err := o.mergeResidualMatch(mergeParityRow())
	if err != nil {
		t.Fatalf("nil residual: %v", err)
	}
	if !ok {
		t.Fatal("nil residual must short-circuit to true, not evaluate to false")
	}
}

// TestMergeResidualCompiledPath exercises the compiled residual node
// end to end (compile in initMergeKeys, evaluate in mergeResidualMatch)
// on true/false/NULL outcomes through the hoisted slot.
func TestMergeResidualCompiledPath(t *testing.T) {
	newOp := func(res optimizer.Expr) *joinOp {
		o := &joinOp{mergeKeys: []optimizer.JoinKeyPair{{Left: pcol(0, "int4"), Right: pcol(3, "int4")}}, mergeResidual: res}
		o.compileMergeExprs()
		return o
	}
	row := mergeParityRow()
	for _, tc := range []struct {
		name string
		res  optimizer.Expr
		want bool
	}{
		{"true", pbin(parser.OpEq, pcol(0, "int4"), pcol(3, "int4")), true},
		{"false", pbin(parser.OpLt, pcol(0, "int4"), pcol(3, "int4")), false},
		{"null", pcol(2, "int4"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newOp(tc.res)
			ok, err := o.mergeResidualMatch(row)
			if err != nil {
				t.Fatalf("mergeResidualMatch: %v", err)
			}
			if ok != tc.want {
				t.Errorf("mergeResidualMatch = %v, want %v", ok, tc.want)
			}
			if o.mergeResidSlot == nil || len(o.mergeResidSlot.row) != len(row) {
				t.Errorf("hoisted slot not rebound (len=%d, want %d)", len(o.mergeResidSlot.row), len(row))
			}
		})
	}
}
