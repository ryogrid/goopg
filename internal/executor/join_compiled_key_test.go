package executor

// M0127-PS6.1 — compiled join key / residual evaluation
// (design leftdeep-joins/05 §6, stage E5).
//
// The stage replaces `evalExprSlot` with the compiled `evalFastExpr` on the
// two hottest expression seams left in the executor: the hash key (once per
// build row, once per probe row) and the residual conjunction (once per
// candidate match). It is a DISPATCH change, so the tests that matter are not
// row counts — they are the two ways a dispatch change can stop being one:
//
//   - the compiled twin disagrees with the interpreted twin about a value
//     (Hard-won Rule #2; the release gate for the full corpus is PS6.2), and
//   - the compiled twin FAILS DIFFERENTLY. This is the concrete one. The
//     interpreted `evalExprSlot` bounds-checks every ColumnRef and raises
//     XX000 (expr.go:353-393) precisely because a raw index panic once
//     escaped the hash-join build-side drain — run by gatherOp.Open in the
//     LEADER goroutine, outside ParallelGroup.Go's recover — reached
//     serveConn and closed the client socket (TPC-DS Q8). PS6.1 moves that
//     exact seam onto evalFastExpr, whose ColumnRef arm had no such check.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

func intCol(idx int) *planner.ColumnRef {
	return &planner.ColumnRef{Index: idx, Name: "c", Type: catalog.Type{Name: "int4"}}
}

// TestCompiledExprMatchesInterpretedOnJoinKeyShapes is the sibling-path
// diff for the expression shapes a join key and a residual actually take:
// bare column accessors, arithmetic keys, comparisons, and the AND-chain a
// multi-conjunct residual compiles to. An ExprAdapter shape (StringConst) is
// included on purpose — the fallback lane must stay exactly the interpreter.
func TestCompiledExprMatchesInterpretedOnJoinKeyShapes(t *testing.T) {
	cmp := func(op parser.OpCode, l, r planner.Expr) *planner.BinaryOp {
		return &planner.BinaryOp{Op: op, Left: l, Right: r}
	}
	corpus := []struct {
		name string
		expr planner.Expr
	}{
		{"bare column", intCol(0)},
		{"column arithmetic", &planner.BinaryOp{Op: parser.OpAdd, Left: intCol(0), Right: intCol(1), ResultType: "int4"}},
		{"comparison", cmp(parser.OpEq, intCol(0), &planner.IntegerConst{Value: 7})},
		{"comparison false", cmp(parser.OpGt, intCol(0), &planner.IntegerConst{Value: 99})},
		{"and chain", cmp(parser.OpAnd,
			cmp(parser.OpEq, intCol(0), &planner.IntegerConst{Value: 7}),
			cmp(parser.OpLt, intCol(1), &planner.IntegerConst{Value: 50}))},
		{"and chain short-circuits on false", cmp(parser.OpAnd,
			cmp(parser.OpEq, intCol(0), &planner.IntegerConst{Value: 0}),
			cmp(parser.OpLt, intCol(1), &planner.IntegerConst{Value: 50}))},
		{"null column propagates", cmp(parser.OpEq, intCol(2), &planner.IntegerConst{Value: 7})},
		{"adapter fallback", &planner.StringConst{}},
	}
	row := Row{NewIntDatum(7), NewIntDatum(11), NullDatum}

	for _, c := range corpus {
		var slab exprTreeSlab
		idx := slab.buildExpr(c.expr)
		slot := SlotFromRow(nil, row)
		want, wantErr := evalExprSlot(c.expr, slot, nil)
		got, gotErr := evalFastExpr(slab, idx, slot, nil)
		if (wantErr == nil) != (gotErr == nil) {
			t.Errorf("%s: interpreted err=%v, compiled err=%v", c.name, wantErr, gotErr)
			continue
		}
		if wantErr != nil {
			continue
		}
		if got.IsNull() != want.IsNull() || (!want.IsNull() && datumKey(got) != datumKey(want)) {
			t.Errorf("%s: compiled %v != interpreted %v", c.name, got, want)
		}
	}
}

// TestCompiledColumnRefOutOfRangeErrorsInsteadOfPanicking is the guard the
// header describes. A stale planner index is a bug either way; the contract
// is that it kills the STATEMENT (PG raises ERROR) and not the backend. All
// four SlotView implementations go through the one widthSlot assertion, so
// all four are checked here — *MaterializedSlot and *Slot were the two the
// interpreted twin had to add concrete-type arms for.
func TestCompiledColumnRefOutOfRangeErrorsInsteadOfPanicking(t *testing.T) {
	var slab exprTreeSlab
	ref := intCol(57) // width-1 slots below; 57 is TPC-DS Q8's actual index
	idx := slab.buildExpr(ref)

	slots := map[string]SlotView{
		"rowSlotView":       rowSlotView(Row{NewIntDatum(1)}),
		"*MaterializedSlot": SlotFromRow(nil, Row{NewIntDatum(1)}),
		"*Slot":             &Slot{Cells: Row{NewIntDatum(1)}},
		"*VirtualSlot":      mergedKeySlot(SlotFromRow(nil, Row{NewIntDatum(1)}), 1, 0, true),
	}
	for name, slot := range slots {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: compiled ColumnRef panicked (%v) — this is the "+
						"failure that closed the client socket on TPC-DS Q8", name, r)
				}
			}()
			_, err := evalFastExpr(slab, idx, slot, nil)
			if err == nil {
				t.Errorf("%s: out-of-range ColumnRef returned no error", name)
				return
			}
			// The error must be the interpreted twin's, byte for byte: the
			// compiled arm delegates rather than inventing a second text.
			_, wantErr := evalExprSlot(ref, slot, nil)
			if wantErr == nil {
				t.Fatalf("%s: interpreted twin accepted the same index — the "+
					"two evaluators disagree about what is in range", name)
			}
			if err.Error() != wantErr.Error() {
				t.Errorf("%s: compiled error %q != interpreted %q", name, err, wantErr)
			}
			if !strings.Contains(err.Error(), "out of") {
				t.Errorf("%s: unexpected error text %q", name, err)
			}
		}()
	}
}

// TestJoinCompilesKeysAndResidualAtOpen pins the wiring: a real build loop
// must leave the compiled slab populated and the node lists aligned with the
// interpreted expression lists they were derived from. A mismatch in length
// or orientation is the failure that builds on `l.a` and probes with `r.a` —
// a join that runs and returns wrong rows.
func TestJoinCompilesKeysAndResidualAtOpen(t *testing.T) {
	const leftWidth = 2
	rows := []Row{{NewIntDatum(1998), NewIntDatum(1)}, {NewIntDatum(1998), NewIntDatum(2)}}
	child := &bufferReuseOp{rows: rows}
	o := &joinOp{plan: twoKeyJoinPlan(leftWidth), right: child, lazyRW: 2}
	if err := child.Open(nil); err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := o.buildLoopRight(nil, leftWidth); err != nil {
		t.Fatalf("buildLoopRight: %v", err)
	}
	if !o.execCompiled {
		t.Fatal("the build loop ran without compiling the key expressions")
	}
	if len(o.buildKeyNodes) != len(o.buildKeyExprs) || len(o.probeKeyNodes) != len(o.probeKeyExprs) {
		t.Fatalf("node lists out of step with expression lists: build %d/%d, probe %d/%d",
			len(o.buildKeyNodes), len(o.buildKeyExprs), len(o.probeKeyNodes), len(o.probeKeyExprs))
	}
	// Each compiled node must evaluate to what its own expression does —
	// which is also what proves the two lists are in the same order.
	slot := SlotFromRow(nil, Row{NewIntDatum(5), NewIntDatum(6), NewIntDatum(7), NewIntDatum(8)})
	for i, e := range o.buildKeyExprs {
		want, err := evalExprSlot(e, slot, nil)
		if err != nil {
			t.Fatalf("interpreted build key %d: %v", i, err)
		}
		got, err := evalFastExpr(o.execExprs, o.buildKeyNodes[i], slot, nil)
		if err != nil {
			t.Fatalf("compiled build key %d: %v", i, err)
		}
		if datumKey(got) != datumKey(want) {
			t.Errorf("build key %d: compiled %v != interpreted %v", i, got, want)
		}
	}
	for i, e := range o.probeKeyExprs {
		want, err := evalExprSlot(e, slot, nil)
		if err != nil {
			t.Fatalf("interpreted probe key %d: %v", i, err)
		}
		got, err := evalFastExpr(o.execExprs, o.probeKeyNodes[i], slot, nil)
		if err != nil {
			t.Fatalf("compiled probe key %d: %v", i, err)
		}
		if datumKey(got) != datumKey(want) {
			t.Errorf("probe key %d: compiled %v != interpreted %v", i, got, want)
		}
	}
}

// TestCompositeEncodeNeverRunsOnAnUncompiledNodeList is the regression for
// the one way PS6.1 could produce WRONG ROWS rather than slow ones. The
// composite encoder walks compiled node indices; over an empty (uncompiled)
// list it does not fail — it succeeds and returns the EMPTY key, so every
// build row lands in one bucket and every probe row matches all of them.
// The encoders therefore self-initialise, and this pins that they do.
func TestCompositeEncodeNeverRunsOnAnUncompiledNodeList(t *testing.T) {
	// Deliberately NOT compiled: no plan, no initExecKeys, no Open.
	o := &joinOp{
		execKeys: make([]planner.JoinKeyPair, 2),
		buildKeyExprs: []planner.Expr{
			&planner.ColumnRef{Index: 0},
			&planner.ColumnRef{Index: 1},
		},
	}
	encode := func(a, b int64) string {
		t.Helper()
		ok, err := o.encodeBuildCompositeKey(SlotFromRow(nil, Row{NewIntDatum(a), NewIntDatum(b)}))
		if err != nil {
			t.Fatalf("encode(%d,%d): %v", a, b, err)
		}
		if !ok {
			t.Fatalf("encode(%d,%d): key rejected", a, b)
		}
		return string(o.execKeyBuf)
	}
	k1, k2 := encode(1998, 7), encode(1998, 8)
	if k1 == "" {
		t.Fatal("the encoder returned the empty key — it ran on an uncompiled node list")
	}
	if k1 == k2 {
		t.Fatalf("distinct key tuples encoded identically (%q): the second column was not evaluated", k1)
	}
}

// BenchmarkJoinKeyEval is the "no alloc regression" half of the PS6.1 bar
// (05 §6). Both arms evaluate the same ColumnRef key against the same
// VirtualSlot the build/probe loops use; the compiled arm must not allocate
// more than the interpreted one.
func BenchmarkJoinKeyEval(b *testing.B) {
	key := intCol(0)
	slot := mergedKeySlot(SlotFromRow(nil, Row{NewIntDatum(42), NewIntDatum(7)}), 2, 2, true)

	b.Run("interpreted", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := evalExprSlot(key, slot, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("compiled", func(b *testing.B) {
		var slab exprTreeSlab
		idx := slab.buildExpr(key)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := evalFastExpr(slab, idx, slot, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkJoinResidualEval is the same measurement for the residual seam,
// where the expression is an AND of two comparisons rather than a bare
// accessor — the shape a non-equijoin conjunct leaves behind.
func BenchmarkJoinResidualEval(b *testing.B) {
	residual := &planner.BinaryOp{
		Op:    parser.OpAnd,
		Left:  &planner.BinaryOp{Op: parser.OpLt, Left: intCol(0), Right: intCol(2)},
		Right: &planner.BinaryOp{Op: parser.OpGt, Left: intCol(1), Right: intCol(3)},
	}
	slot := SlotFromRow(nil, Row{NewIntDatum(1), NewIntDatum(9), NewIntDatum(5), NewIntDatum(2)})

	b.Run("interpreted", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := evalExprSlot(residual, slot, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("compiled", func(b *testing.B) {
		var slab exprTreeSlab
		idx := slab.buildExpr(residual)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := evalFastExpr(slab, idx, slot, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}
