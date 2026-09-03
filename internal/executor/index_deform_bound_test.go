package executor

// EX1-02b verification: bound threading for the index/bitmap/IOS paths.
//
// Covers per-path threading (index/bitmap/IOS/NLI-inner incl. Memoize wrap),
// recheck-widen, Cond-union, the no-face-value-key-folding regression (a key
// expr with an outer-space index must NOT narrow the inner), a forced-lossy
// bitmap run, cold-VM heap-fallback and all-visible key-served IOS states,
// poison runs over the Rescan re-probe paths, and the Q6-shape bound (still
// 3/8 — the shared walk helpers are untouched).
import (
	"fmt"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// idxDeformEff normalises an operator's stamped deformBound the way the
// seqLeafBound helper does: unset reads as full width.
func idxDeformEff(bound, ncols int) int {
	if bound <= 0 {
		return ncols
	}
	return bound
}

// idxDeformIndexScan builds a synthetic 8-column IndexScan on c0 and returns
// the stamped effective bound of the built operator, following unary
// pass-throughs (a Filter above the scan is the threading under test).
func idxDeformIndexScan(t *testing.T, plan optimizer.Node) int {
	t.Helper()
	op := mustBuildDeform(t, plan)
	for {
		switch o := op.(type) {
		case *indexScanOp:
			return idxDeformEff(o.deformBound, 8)
		case *filterOp:
			op = o.child
		default:
			t.Fatalf("built %T, want *indexScanOp under pass-throughs", op)
			return 0
		}
	}
}

func idxDeformScan8(cond optimizer.Expr) *optimizer.IndexScan {
	return &optimizer.IndexScan{
		Table: deformTable(8),
		Index: &catalog.Index{Name: "i8a", Columns: []string{"c0"}},
		Cond:  cond,
	}
}

// TestIndexDeformBoundThreading pins the IndexScan arm: bound = union of the
// parent walk and the leaf-local Cond refs, widened with the index key
// columns (belt-and-braces, never load-bearing).
func TestIndexDeformBoundThreading(t *testing.T) {
	filter := func(pred optimizer.Expr, child optimizer.Node) *optimizer.Filter {
		return &optimizer.Filter{Child: child, Predicate: pred}
	}

	t.Run("cond-union", func(t *testing.T) {
		// No parent refs; Cond reads c4, key col is c0 → 5/8.
		if b := idxDeformIndexScan(t, idxDeformScan8(deformLt(deformCol(4), deformInt(1)))); b != 5 {
			t.Fatalf("bound=%d, want 5", b)
		}
	})

	t.Run("parent-walk-union", func(t *testing.T) {
		// Filter reads c5 above a cond-less scan on c0 → 6/8.
		plan := filter(deformLt(deformCol(5), deformInt(1)), idxDeformScan8(nil))
		if b := idxDeformIndexScan(t, plan); b != 6 {
			t.Fatalf("bound=%d, want 6", b)
		}
	})

	t.Run("key-cols-widen", func(t *testing.T) {
		// Cond reads c1 but the index sits on c6: unioning the key column
		// widens to 7/8 instead of narrowing to 2/8.
		p := idxDeformScan8(deformLt(deformCol(1), deformInt(1)))
		p.Index.Columns = []string{"c6"}
		if b := idxDeformIndexScan(t, p); b != 7 {
			t.Fatalf("bound=%d, want 7", b)
		}
	})

	t.Run("declined-cond-full", func(t *testing.T) {
		// An unreadable Cond re-widens the whole bound even under a narrow
		// parent walk.
		bad := &optimizer.FuncCall{Name: "abs", Args: []optimizer.Expr{deformCol(0)}}
		plan := filter(deformLt(deformCol(1), deformInt(1)), idxDeformScan8(bad))
		if b := idxDeformIndexScan(t, plan); b != 8 {
			t.Fatalf("bound=%d, want full 8", b)
		}
	})

	t.Run("bare-full", func(t *testing.T) {
		// No Cond and no key-column narrowing past full: an index on c7
		// widens the empty consumer set to the last column.
		p := idxDeformScan8(nil)
		p.Index.Columns = []string{"c7"}
		if b := idxDeformIndexScan(t, p); b != 8 {
			t.Fatalf("bound=%d, want full 8", b)
		}
	})
}

// idxDeformBitmapOp builds a synthetic BitmapHeapScan and returns the stamped
// effective bound of the built operator, following unary pass-throughs.
func idxDeformBitmapOp(t *testing.T, plan optimizer.Node) int {
	t.Helper()
	op := mustBuildDeform(t, plan)
	for {
		switch o := op.(type) {
		case *bitmapHeapScanOp:
			return idxDeformEff(o.deformBound, 8)
		case *filterOp:
			op = o.child
		default:
			t.Fatalf("built %T, want *bitmapHeapScanOp under pass-throughs", op)
			return 0
		}
	}
}

func idxDeformBitmap8(quals []optimizer.Expr, cond optimizer.Expr) *optimizer.BitmapHeapScan {
	leaf := deformTable(8)
	return &optimizer.BitmapHeapScan{
		Table: leaf,
		Outer: &optimizer.BitmapIndexScan{
			Table: leaf,
			Index: &catalog.Index{Name: "i8b", Columns: []string{"c0"}},
		},
		BitmapQual: quals,
		Cond:       cond,
	}
}

// TestBitmapDeformBoundThreading pins the BitmapHeapScan arm: BitmapQual
// recheck refs + Cond refs folded first, then unioned with the parent walk.
func TestBitmapDeformBoundThreading(t *testing.T) {
	filter := func(pred optimizer.Expr, child optimizer.Node) *optimizer.Filter {
		return &optimizer.Filter{Child: child, Predicate: pred}
	}
	recheck := deformLt(deformCol(6), deformInt(1))

	t.Run("recheck-and-parent-union", func(t *testing.T) {
		// Filter reads c2, recheck reads c6 → 7/8.
		plan := filter(deformLt(deformCol(2), deformInt(1)), idxDeformBitmap8([]optimizer.Expr{recheck}, nil))
		if b := idxDeformBitmapOp(t, plan); b != 7 {
			t.Fatalf("bound=%d, want 7", b)
		}
	})

	t.Run("cond-union", func(t *testing.T) {
		// No parent refs, no recheck; Cond reads c3 → 4/8.
		plan := idxDeformBitmap8(nil, deformLt(deformCol(3), deformInt(1)))
		if b := idxDeformBitmapOp(t, plan); b != 4 {
			t.Fatalf("bound=%d, want 4", b)
		}
	})

	t.Run("bare-full", func(t *testing.T) {
		if b := idxDeformBitmapOp(t, idxDeformBitmap8(nil, nil)); b != 8 {
			t.Fatalf("bound=%d, want full 8", b)
		}
	})
}

// TestBitmapDeformRecheckWiden pins the recheck-first rule: the recheck
// reader is the consumer most likely to be forgotten, so any decline in it
// (or in Cond) fails the whole bound — re-widen, never narrow past a
// recheck reader.
func TestBitmapDeformRecheckWiden(t *testing.T) {
	declined := &optimizer.FuncCall{Name: "abs", Args: []optimizer.Expr{deformCol(0)}}

	t.Run("declined-recheck-full", func(t *testing.T) {
		plan := idxDeformBitmap8([]optimizer.Expr{declined}, nil)
		if b := idxDeformBitmapOp(t, plan); b != 8 {
			t.Fatalf("bound=%d, want full 8", b)
		}
	})

	t.Run("declined-cond-full", func(t *testing.T) {
		plan := idxDeformBitmap8(nil, declined)
		if b := idxDeformBitmapOp(t, plan); b != 8 {
			t.Fatalf("bound=%d, want full 8", b)
		}
	})

	t.Run("high-recheck-widens", func(t *testing.T) {
		// A narrow parent walk (c1) must not narrow past the recheck
		// reader (c6): the bound re-widens to 7/8.
		plan := &optimizer.Filter{
			Child:     idxDeformBitmap8([]optimizer.Expr{deformLt(deformCol(6), deformInt(1))}, nil),
			Predicate: deformLt(deformCol(1), deformInt(1)),
		}
		if b := idxDeformBitmapOp(t, plan); b != 7 {
			t.Fatalf("bound=%d, want 7", b)
		}
	})
}

// idxDeformCovered builds Covered entries naming c<i> for each heap ordinal.
func idxDeformCovered(ords ...int) []catalog.Column {
	out := make([]catalog.Column, len(ords))
	for i, o := range ords {
		out[i] = catalog.Column{Name: fmt.Sprintf("c%d", o)}
	}
	return out
}

func idxDeformIndexOnly(cols []string, covered []catalog.Column) *optimizer.IndexOnlyScan {
	return &optimizer.IndexOnlyScan{
		Table:   deformTable(8),
		Index:   &catalog.Index{Name: "i8c", Columns: cols},
		Covered: covered,
	}
}

// TestIndexOnlyDeformLocalWidths pins the IOS local fixes (no plan walk —
// the consumer is the Covered list): the key loop stops after the highest
// covered key ordinal while decoding THROUGH gaps, and the heap fallback
// decodes [0, maxCovered+1].
func TestIndexOnlyDeformLocalWidths(t *testing.T) {
	t.Run("key-stop-through-gap", func(t *testing.T) {
		// Index (c0,c1,c2), Covered {c0,c2}: the gap c1 over-decodes —
		// stop after ordinal 2, not after 1.
		p := idxDeformIndexOnly([]string{"c0", "c1", "c2"}, idxDeformCovered(0, 2))
		if m, ok := iosMaxCoveredKeyPos(p); !ok || m != 2 {
			t.Fatalf("maxKeyPos=%d ok=%v, want 2/true", m, ok)
		}
		// Heap ordinals {0,2} → fallback width 3.
		if w := iosHeapFallbackWidth(p); w != 3 {
			t.Fatalf("fallback width=%d, want 3", w)
		}
	})

	t.Run("key-stop-prefix", func(t *testing.T) {
		// Index (c0,c1,c2), Covered {c0,c1}: trailing c2 is skipped.
		p := idxDeformIndexOnly([]string{"c0", "c1", "c2"}, idxDeformCovered(0, 1))
		if m, ok := iosMaxCoveredKeyPos(p); !ok || m != 1 {
			t.Fatalf("maxKeyPos=%d ok=%v, want 1/true", m, ok)
		}
		if w := iosHeapFallbackWidth(p); w != 2 {
			t.Fatalf("fallback width=%d, want 2", w)
		}
	})

	t.Run("non-key-covered-declines", func(t *testing.T) {
		// A Covered column outside the key must decode everything.
		p := idxDeformIndexOnly([]string{"c0", "c1"}, idxDeformCovered(0, 5))
		if _, ok := iosMaxCoveredKeyPos(p); ok {
			t.Fatal("ok=true, want false (covered c5 is not a key column)")
		}
		// The heap width still narrows to the highest covered heap
		// ordinal + 1.
		if w := iosHeapFallbackWidth(p); w != 6 {
			t.Fatalf("fallback width=%d, want 6", w)
		}
	})
}

// idxDeformNLI builds a synthetic inner-join NLI over 8-column tables and
// returns the join operator. The inner Key carries outer-space indexes by
// construction (evaluated against the bound outer row at runtime).
func idxDeformNLI(t *testing.T, inner optimizer.Node, pred optimizer.Expr, memo bool, above optimizer.Expr) *nestedLoopIndexJoinOp {
	t.Helper()
	outer := &optimizer.SeqScan{Table: deformTable(8)}
	nli := &optimizer.NestedLoopIndexJoin{
		Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: pred,
	}
	if memo {
		innerScan, ok := inner.(*optimizer.IndexScan)
		if !ok {
			t.Fatalf("memoize test needs an *optimizer.IndexScan inner, got %T", inner)
		}
		nli.InnerMemo = &optimizer.Memoize{Child: innerScan}
	}
	var plan optimizer.Node = nli
	if above != nil {
		plan = &optimizer.Filter{Child: nli, Predicate: above}
	}
	op := mustBuildDeform(t, plan)
	if above != nil {
		fo, ok := op.(*filterOp)
		if !ok {
			t.Fatalf("built %T, want *filterOp", op)
		}
		op = fo.child
	}
	jo, ok := op.(*nestedLoopIndexJoinOp)
	if !ok {
		t.Fatalf("built %T, want *nestedLoopIndexJoinOp", op)
	}
	return jo
}

// idxDeformNLIInnerBound reads the stamped effective bound of an NLI inner,
// following the Memoize wrap like nliInnerIndexScan does.
func idxDeformNLIInnerBound(t *testing.T, jo *nestedLoopIndexJoinOp, ncols int) int {
	t.Helper()
	switch in := jo.inner.(type) {
	case *indexScanOp:
		return idxDeformEff(in.deformBound, ncols)
	case *memoizeOp:
		return idxDeformEff(in.child.deformBound, ncols)
	case *bitmapHeapScanOp:
		return idxDeformEff(in.deformBound, ncols)
	default:
		t.Fatalf("inner is %T", jo.inner)
		return 0
	}
}

func idxDeformInnerScan(key, cond optimizer.Expr) *optimizer.IndexScan {
	return &optimizer.IndexScan{
		Table: deformTable(8),
		Index: &catalog.Index{Name: "i8n", Columns: []string{"c0"}},
		Key:   key,
		Cond:  cond,
	}
}

// TestNLIDeformInnerBound pins the NLI-inner threading: bound =
// union(parentMapped, CondRefs), key probe exprs never folded at face value,
// Memoize wrap preserving the stamped child.
func TestNLIDeformInnerBound(t *testing.T) {
	t.Run("no-face-value-key-folding", func(t *testing.T) {
		// The Key carries an outer-space index (outer col 7) that a
		// face-value fold would file into the inner bound (range-checked
		// past the inner width → full 8/8). The correct bound unions only
		// the empty parent share, the absent Cond, and the key COLUMNS
		// {c0} → 1/8.
		jo := idxDeformNLI(t, idxDeformInnerScan(deformCol(15), nil), nil, false, nil)
		if b := idxDeformNLIInnerBound(t, jo, 8); b != 1 {
			t.Fatalf("inner bound=%d, want 1 (key expr must not fold)", b)
		}
	})

	t.Run("cond-union", func(t *testing.T) {
		// Inner Cond reads inner c4; key cols {c0} → 5/8.
		inner := idxDeformInnerScan(deformCol(15), deformLt(deformCol(4), deformInt(1)))
		jo := idxDeformNLI(t, inner, nil, false, nil)
		if b := idxDeformNLIInnerBound(t, jo, 8); b != 5 {
			t.Fatalf("inner bound=%d, want 5", b)
		}
	})

	t.Run("parent-mapped", func(t *testing.T) {
		// The filter above reads merged col 10 (inner c2): mapped through
		// the 8-wide outer to inner 2, union key cols {c0} → 3/8.
		jo := idxDeformNLI(t, idxDeformInnerScan(deformCol(15), nil), nil, false,
			deformLt(deformCol(10), deformInt(1)))
		if b := idxDeformNLIInnerBound(t, jo, 8); b != 3 {
			t.Fatalf("inner bound=%d, want 3", b)
		}
	})

	t.Run("predicate-inner-refs", func(t *testing.T) {
		// The join Predicate evaluates per joined pair against the merged
		// (outer++inner) row: its inner-side ref (merged c10 → inner c2)
		// is a genuine inner consumer. Without this fold the inner would
		// narrow to the key cols (1/8) and the residual would read an
		// undeformed tail — the semi/anti residual wrong-answer shape.
		inner := idxDeformInnerScan(deformCol(15), nil)
		pred := deformLt(deformCol(0), deformCol(10))
		jo := idxDeformNLI(t, inner, pred, false, nil)
		if b := idxDeformNLIInnerBound(t, jo, 8); b != 3 {
			t.Fatalf("inner bound=%d, want 3", b)
		}
	})

	t.Run("declined-predicate-full", func(t *testing.T) {
		// An unreadable Predicate fails the inner to full, like every
		// other decline.
		inner := idxDeformInnerScan(deformCol(15), nil)
		bad := &optimizer.FuncCall{Name: "abs", Args: []optimizer.Expr{deformCol(10)}}
		jo := idxDeformNLI(t, inner, bad, false, nil)
		if b := idxDeformNLIInnerBound(t, jo, 8); b != 8 {
			t.Fatalf("inner bound=%d, want full 8", b)
		}
	})

	t.Run("outer-keeps-left-side-rule", func(t *testing.T) {
		// EX1-02 rule, verified not changed: the outer takes its share of
		// the above prefix plus the outer-side predicate refs. The inner
		// Key reads outer c2 (same column — key fold agrees, no widening).
		inner := idxDeformInnerScan(deformCol(2), nil)
		pred := deformLt(deformCol(2), deformCol(10))
		jo := idxDeformNLI(t, inner, pred, false, nil)
		so, ok := jo.outer.(*seqScanOp)
		if !ok {
			t.Fatalf("outer is %T, want *seqScanOp", jo.outer)
		}
		// Outer-side ref c2, no above refs → 3/8.
		if b := idxDeformEff(so.deformBound, 8); b != 3 {
			t.Fatalf("outer bound=%d, want 3", b)
		}
	})

	t.Run("outer-covers-probe-key-cols", func(t *testing.T) {
		// NLI probe-key hole regression: the inner Key evaluates
		// against the OUTER slot, so outer col 7 read by the key must
		// deform on the outer scan even when the above prefix ([0,1))
		// and the Predicate cover only col 0. Without the key fold
		// the outer narrows to 1/8 and the re-probe reads a stale
		// tail slot.
		inner := idxDeformInnerScan(deformCol(7), nil)
		above := deformLt(deformCol(0), deformInt(100))
		jo := idxDeformNLI(t, inner, nil, false, above)
		so, ok := jo.outer.(*seqScanOp)
		if !ok {
			t.Fatalf("outer is %T, want *seqScanOp", jo.outer)
		}
		if b := idxDeformEff(so.deformBound, 8); b != 8 {
			t.Fatalf("outer bound=%d, want 8 (probe key col 7)", b)
		}
	})

	t.Run("memoize-wrapped", func(t *testing.T) {
		// The Memoize cache wraps the stamped probe untouched: Cond c4 →
		// 5/8 on the child.
		inner := idxDeformInnerScan(deformCol(15), deformLt(deformCol(4), deformInt(1)))
		jo := idxDeformNLI(t, inner, nil, true, nil)
		if _, ok := jo.inner.(*memoizeOp); !ok {
			t.Fatalf("inner is %T, want *memoizeOp", jo.inner)
		}
		if b := idxDeformNLIInnerBound(t, jo, 8); b != 5 {
			t.Fatalf("memoized inner bound=%d, want 5", b)
		}
	})

	t.Run("bitmap-inner", func(t *testing.T) {
		// A bitmap inner threads the same way: recheck c6, no parent
		// share → 7/8.
		leaf := deformTable(8)
		inner := &optimizer.BitmapHeapScan{
			Table: leaf,
			Outer: &optimizer.BitmapIndexScan{
				Table: leaf,
				Index: &catalog.Index{Name: "i8m", Columns: []string{"c0"}},
				Key:   deformCol(15),
			},
			BitmapQual: []optimizer.Expr{deformLt(deformCol(6), deformInt(1))},
		}
		jo := idxDeformNLI(t, inner, nil, false, nil)
		if b := idxDeformNLIInnerBound(t, jo, 8); b != 7 {
			t.Fatalf("bitmap inner bound=%d, want 7", b)
		}
	})
}

// TestIndexDeformQ6ShapeStillThreeOfEight pins the no-op condition: no index
// sits in the Q6 (filter+aggregate) chain, so the bound is unchanged at 3/8
// — the shared walk helpers were not touched.
func TestIndexDeformQ6ShapeStillThreeOfEight(t *testing.T) {
	ctx := deformW8Fixture(t)

	narrowSQL := `SELECT sum(b) FROM w WHERE a > 11 AND c < 33`
	plan, err := testPlanDeform(t, ctx, narrowSQL)
	if err != nil {
		t.Fatalf("plan narrow: %v", err)
	}
	b, n := seqLeafBound(t, mustBuildDeform(t, plan))
	if n != 8 {
		t.Fatalf("ncols=%d, want 8", n)
	}
	if b != 3 {
		t.Fatalf("bound=%d, want 3 (refs a=0, b=1, c=2)", b)
	}
}

// TestIndexDeformRescanPersistsBound runs a real narrowed index probe twice
// through Rescan with the tail-poison armed: the bound is build-time static
// (no per-Rescan work), so the re-probe decodes the same window and no
// consumer reads past it.
func TestIndexDeformRescanPersistsBound(t *testing.T) {
	ctx := deformW8Fixture(t)
	if err := runDDL(t, ctx, `CREATE INDEX w_a_idx ON w (a)`); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "w"})
	if !ok {
		t.Fatal("lookup w")
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "w_a_idx"})
	if !ok {
		t.Fatal("lookup w_a_idx")
	}
	plan := &optimizer.IndexScan{Table: tbl, Index: idx, Key: deformInt(20)}
	op := mustBuildDeform(t, plan)
	io, ok := op.(*indexScanOp)
	if !ok {
		t.Fatalf("built %T, want *indexScanOp", op)
	}
	ncols := len(tbl.Columns)
	if b := idxDeformEff(io.deformBound, ncols); b != 1 {
		t.Fatalf("bound=%d, want 1 (key cols {a} only)", b)
	}

	drainHeads := func() []string {
		var heads []string
		for {
			slot, err := io.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			heads = append(heads, string(slotRow(slot)[0].AppendValueText(nil)))
		}
		return heads
	}

	old := seqScanDeformPoison
	seqScanDeformPoison = true
	defer func() { seqScanDeformPoison = old }()
	if err := io.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Poison-armed first probe: only the head column is asserted (the tail
	// carries the sentinel by design); survival without panic is the assert.
	if heads := drainHeads(); fmt.Sprint(heads) != "[20]" {
		t.Fatalf("probe heads=%v, want [20]", heads)
	}
	// The re-probe reuses the build-time bound untouched.
	if err := io.Rescan(nil, 0); err != nil {
		t.Fatalf("Rescan: %v", err)
	}
	if b := idxDeformEff(io.deformBound, ncols); b != 1 {
		t.Fatalf("post-Rescan bound=%d, want 1", b)
	}
	if heads := drainHeads(); fmt.Sprint(heads) != "[20]" {
		t.Fatalf("re-probe heads=%v, want [20]", heads)
	}
	if err := io.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	seqScanDeformPoison = false

	// Flag off: the re-probe still serves the same head values (the tail is
	// unwound only as far as the bound — by design, exactly like seqScanOp
	// — so only the in-window columns are asserted here; full-row values
	// ride the Project-narrowed SQL path in TestIndexDeformRescanPoisonNLI).
	op2 := mustBuildDeform(t, plan)
	io2 := op2.(*indexScanOp)
	if err := io2.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var plainHeads []string
	for {
		slot, err := io2.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		plainHeads = append(plainHeads, string(slotRow(slot)[0].AppendValueText(nil)))
	}
	if err := io2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fmt.Sprint(plainHeads) != "[20]" {
		t.Fatalf("plain heads=%v, want [20]", plainHeads)
	}

	// Project-narrowed SQL values through the heap-fetching IndexScan: the
	// output carries only in-window columns, so it is clean with the poison
	// armed or not. Selecting b (not in the (a) index) defeats IOS
	// promotion, keeping the heap path; the bound narrows to 2/8.
	// The plan-shape gate proves the index (not a seq scan) serves it and
	// the built probe actually narrowed.
	const pointSQL = `SELECT a, b FROM w WHERE a = 20`
	pointPlan, err := testPlanDeform(t, ctx, pointSQL)
	if err != nil {
		t.Fatalf("plan point: %v", err)
	}
	for n := pointPlan; ; {
		switch v := n.(type) {
		case *optimizer.IndexScan:
			goto planned
		case *optimizer.Project:
			n = v.Child
		case *optimizer.Filter:
			n = v.Child
		case *optimizer.Sort:
			n = v.Child
		case *optimizer.Limit:
			n = v.Child
		default:
			t.Fatalf("point query plans %T, want an IndexScan", n)
		}
	}
planned:
	var pointBounds [][2]int
	collectDeformBounds(mustBuildDeform(t, pointPlan), &pointBounds)
	t.Logf("point-query bounds=%v", pointBounds)
	foundNarrow := false
	for _, b := range pointBounds {
		if b[0] == 2 && b[1] == 8 {
			foundNarrow = true
		}
	}
	if !foundNarrow {
		t.Fatalf("no narrowed 2/8 leaf in %v", pointBounds)
	}
	for _, poison := range []bool{true, false} {
		old2 := seqScanDeformPoison
		seqScanDeformPoison = poison
		rows, err := runQueryWithErr(ctx, pointSQL)
		seqScanDeformPoison = old2
		if err != nil {
			t.Fatalf("poison=%v run: %v", poison, err)
		}
		if got := renderDeformRows(rows); fmt.Sprint(got) != "[20|21]" {
			t.Fatalf("poison=%v rows=%v, want [20|21]", poison, got)
		}
	}
}

// TestIndexDeformRescanPoisonNLI runs a real NLI join poison-armed: every
// inner re-probe reuses its build-time bound, and the join values match the
// flag-off run exactly. The fixture/query are the LEFT-residual shape (whose
// plan requirement helper proves the NLI actually serves the query).
func TestIndexDeformRescanPoisonNLI(t *testing.T) {
	ctx, cleanup := newNLILeftResidualFixture(t)
	defer cleanup()
	requireNLILeftPlan(t, ctx)

	plan, err := testPlanDeform(t, ctx, nliLeftQuery)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var bounds [][2]int
	collectDeformBounds(mustBuildDeform(t, plan), &bounds)
	t.Logf("NLI plan deform bounds (bound/ncols): %v", bounds)

	oldPoison := seqScanDeformPoison
	seqScanDeformPoison = true
	poisonRows, err := runQueryWithErr(ctx, nliLeftQuery)
	seqScanDeformPoison = oldPoison
	if err != nil {
		t.Fatalf("poison run: %v", err)
	}
	plainRows, err := runQueryWithErr(ctx, nliLeftQuery)
	if err != nil {
		t.Fatalf("plain run: %v", err)
	}
	want := []string{"1|", "2|500", "3|", "4|"}
	for name, rows := range map[string][]Row{"poison": poisonRows, "plain": plainRows} {
		if got := renderDeformRows(rows); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s rows = %v, want %v", name, got, want)
		}
	}
	if fmt.Sprint(renderDeformRows(poisonRows)) != fmt.Sprint(renderDeformRows(plainRows)) {
		t.Errorf("poison %v != plain %v", renderDeformRows(poisonRows), renderDeformRows(plainRows))
	}
}

// collectDeformBounds records (effective bound, ncols) for every scan leaf
// under op, following single-child pass-throughs and both join sides.
func collectDeformBounds(op Operator, out *[][2]int) {
	switch o := op.(type) {
	case *seqScanOp:
		*out = append(*out, [2]int{idxDeformEff(o.deformBound, len(o.cols)), len(o.cols)})
	case *indexScanOp:
		n := 0
		if o.plan.Table != nil {
			n = len(o.plan.Table.Columns)
		}
		*out = append(*out, [2]int{idxDeformEff(o.deformBound, n), n})
	case *bitmapHeapScanOp:
		*out = append(*out, [2]int{idxDeformEff(o.deformBound, len(o.cols)), len(o.cols)})
	case *filterOp:
		collectDeformBounds(o.child, out)
	case *projectOp:
		collectDeformBounds(o.child, out)
	case *sortOp:
		collectDeformBounds(o.child, out)
	case *limitOp:
		collectDeformBounds(o.child, out)
	case *aggregateOp:
		collectDeformBounds(o.child, out)
	case *joinOp:
		collectDeformBounds(o.left, out)
		collectDeformBounds(o.right, out)
	case *nestedLoopIndexJoinOp:
		collectDeformBounds(o.outer, out)
		// The inner satisfies nliInner (no Open), not Operator: unwrap
		// the Memoize cache the same way nliInnerIndexScan does.
		if mo, ok := o.inner.(*memoizeOp); ok {
			collectDeformBounds(mo.child, out)
			return
		}
		if io, ok := o.inner.(*indexScanOp); ok {
			collectDeformBounds(io, out)
			return
		}
		if bo, ok := o.inner.(*bitmapHeapScanOp); ok {
			collectDeformBounds(bo, out)
			return
		}
		if ioso, ok := o.inner.(*indexOnlyScanOp); ok {
			_ = ioso // IOS has no plan walk: nothing to record
			return
		}
	}
}

// TestBitmapDeformLossyRecheckPoison forces a lossy bitmap (tiny work_mem)
// over a real table with the tail-poison armed: lossy pages walk every line
// pointer and re-check each tuple, and the recheck reader sits inside the
// narrowed window. The bound survives the Rescan rebuild, and values match
// the exact-bitmap run.
func TestBitmapDeformLossyRecheckPoison(t *testing.T) {
	ctx := deformW8Fixture(t)
	// Enough rows for several heap pages so the tiny budget lossifies.
	var stmts []string
	for i := int64(5); i <= 600; i++ {
		vals := fmt.Sprintf("%d,%d,%d,%d,%d,%d,%d,%d",
			10*((i-1)%4+1), 10*((i-1)%4+1)+1, 10*((i-1)%4+1)+2, 10*((i-1)%4+1)+3,
			10*((i-1)%4+1)+4, 10*((i-1)%4+1)+5, 10*((i-1)%4+1)+6, 10*((i-1)%4+1)+7)
		stmts = append(stmts, "INSERT INTO w VALUES ("+vals+")")
	}
	stmts = append(stmts, `CREATE INDEX w_a_idx ON w (a)`)
	for _, s := range stmts {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("fixture %q: %v", s, err)
		}
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "w"})
	if !ok {
		t.Fatal("lookup w")
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "w_a_idx"})
	if !ok {
		t.Fatal("lookup w_a_idx")
	}
	plan := &optimizer.BitmapHeapScan{
		Table: tbl,
		Outer: &optimizer.BitmapIndexScan{Table: tbl, Index: idx, Key: deformInt(20)},
		// Recheck is the probe's own qual (a = 20): a lossy page walks
		// every line pointer, and only the probe's rows may survive.
		// Cond reads c (c2) → window [0,3).
		BitmapQual: []optimizer.Expr{
			&optimizer.BinaryOp{Op: parser.OpEq, Left: deformCol(0), Right: deformInt(20)},
		},
		Cond: deformLt(deformCol(2), deformInt(33)),
	}

	drainHeads := func(op Operator) []string {
		var heads []string
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			r := slotRow(slot)
			heads = append(heads,
				string(r[0].AppendValueText(nil))+"|"+
					string(r[1].AppendValueText(nil))+"|"+
					string(r[2].AppendValueText(nil)))
		}
		return heads
	}
	runProbe := func(poison bool) ([]string, int64) {
		op := mustBuildDeform(t, plan)
		bo, ok := op.(*bitmapHeapScanOp)
		if !ok {
			t.Fatalf("built %T, want *bitmapHeapScanOp", op)
		}
		if b := idxDeformEff(bo.deformBound, len(tbl.Columns)); b != 3 {
			t.Fatalf("bound=%d, want 3 (recheck c0 + cond c2)", b)
		}
		oldWM := ctx.WorkMem
		ctx.WorkMem = 512 // force lossify: ~16-entry budget over 600 rows
		defer func() { ctx.WorkMem = oldWM }()
		oldPoison := seqScanDeformPoison
		seqScanDeformPoison = poison
		defer func() { seqScanDeformPoison = oldPoison }()
		if err := bo.Open(ctx); err != nil {
			t.Fatalf("Open: %v", err)
		}
		heads := drainHeads(bo)
		lossy := bo.lossyPages
		// The Rescan rebuild reuses the build-time bound untouched.
		if err := bo.Rescan(nil, 0); err != nil {
			t.Fatalf("Rescan: %v", err)
		}
		if b := idxDeformEff(bo.deformBound, len(tbl.Columns)); b != 3 {
			t.Fatalf("post-Rescan bound=%d, want 3", b)
		}
		heads2 := drainHeads(bo)
		// Iteration order across the rebuild is not pinned (map-ordered
		// lossify ties), so both drains are compared sorted.
		sort.Strings(heads)
		sort.Strings(heads2)
		if fmt.Sprint(heads) != fmt.Sprint(heads2) {
			t.Fatalf("probe vs re-probe differ: %d vs %d rows", len(heads), len(heads2))
		}
		if err := bo.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		return heads, lossy
	}

	poisonHeads, lossy := runProbe(true)
	if lossy == 0 {
		t.Fatal("no lossy pages: the recheck path was not exercised")
	}
	t.Logf("lossy pages=%d heads=%d", lossy, len(poisonHeads))
	plainHeads, _ := runProbe(false)
	if fmt.Sprint(poisonHeads) != fmt.Sprint(plainHeads) {
		t.Fatal("poison run differs from plain run")
	}
	// Every surviving head is the a=20 fixture row: 4 seed rows + 596 added
	// rows cycle a ∈ {10,20,30,40} starting at i=5 → (i-1)%4+1==2 exactly
	// when i ≡ 3 (mod 4): i in {7,11,…,599} → 149 rows, plus the seed a=20
	// row → 150.
	if len(poisonHeads) != 150 {
		t.Fatalf("heads=%d, want 150", len(poisonHeads))
	}
	for _, h := range poisonHeads {
		if h != "20|21|22" {
			t.Fatalf("head=%q, want 20|21|22", h)
		}
	}
}

// TestIndexOnlyDeformColdAndVisible runs the same covered queries in the two
// IOS states: cold-VM heap fallback (subset decode [0, maxCovered+1)) and
// all-visible key-served. Values must agree in both, with the tail-poison
// armed throughout.
func TestIndexOnlyDeformColdAndVisible(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	mkWide := func(name, index string) {
		cols := "a int, b int, c int, d int, e int, f int, g int, h int"
		runComposite(t, ctx,
			"CREATE TABLE "+name+" ("+cols+")",
			"CREATE INDEX "+name+"_idx ON "+name+" ("+index+")",
			"INSERT INTO "+name+" VALUES (10,11,12,13,14,15,16,17)",
			"INSERT INTO "+name+" VALUES (20,21,22,23,24,25,26,27)",
			"INSERT INTO "+name+" VALUES (30,31,32,33,34,35,36,37)",
			"INSERT INTO "+name+" VALUES (40,41,42,43,44,45,46,47)",
		)
	}
	mkWide("iof1", "a, b")
	mkWide("iof2", "a, c")

	queries := []struct {
		name     string
		sql      string
		want     []string
		fallback int // expected [0, fallback) heap-decode width
	}{
		{"contiguous", "SELECT a, b FROM iof1 WHERE a = 20 AND b = 21",
			[]string{"20|21"}, 2},
		{"gap", "SELECT a, c FROM iof2 WHERE a = 20 AND c = 22",
			[]string{"20|22"}, 3},
	}
	for _, q := range queries {
		ios := findIndexOnlyScan(planOne(t, q.sql, ctx.Catalog))
		if ios == nil {
			t.Fatalf("%s: plan for %q has no IndexOnlyScan", q.name, q.sql)
		}
		if w := iosHeapFallbackWidth(ios); w != q.fallback {
			t.Fatalf("%s: fallback width=%d, want %d", q.name, w, q.fallback)
		}
	}

	coldRun := func() map[string][]string {
		out := map[string][]string{}
		old := seqScanDeformPoison
		seqScanDeformPoison = true
		defer func() { seqScanDeformPoison = old }()
		for _, q := range queries {
			rows, err := runQueryWithErr(ctx, q.sql)
			if err != nil {
				t.Fatalf("%s cold: %v", q.name, err)
			}
			out[q.name] = renderDeformRows(rows)
		}
		return out
	}

	// Cold: the VM bit is unset, so every entry takes the heap fallback.
	for _, tbl := range []string{"iof1", "iof2"} {
		c, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: tbl})
		if !ok {
			t.Fatalf("lookup %s", tbl)
		}
		if ctx.VM.AllVisible(ctx.Catalog.RelFileNode(c), 0) {
			t.Fatalf("%s unexpectedly ALL_VISIBLE before VACUUM", tbl)
		}
	}
	cold := coldRun()
	for _, q := range queries {
		if fmt.Sprint(cold[q.name]) != fmt.Sprint(q.want) {
			t.Errorf("%s cold rows = %v, want %v", q.name, cold[q.name], q.want)
		}
	}

	// All-visible: VACUUM flips the pages, so the same queries are served
	// from the key with zero heap reads.
	vacuumThen(t, ctx, "iof1")
	vacuumThen(t, ctx, "iof2")
	visible := coldRun()
	for _, q := range queries {
		if fmt.Sprint(visible[q.name]) != fmt.Sprint(q.want) {
			t.Errorf("%s visible rows = %v, want %v", q.name, visible[q.name], q.want)
		}
		if fmt.Sprint(visible[q.name]) != fmt.Sprint(cold[q.name]) {
			t.Errorf("%s visible %v != cold %v", q.name, visible[q.name], cold[q.name])
		}
	}
}
