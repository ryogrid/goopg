package optimizer

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// P6-08 (take3 08 §9) pins the three `restrictInfo` memos — PG's `norm_selec`,
// its operand resolution, and its `MergeScanSelCache` — as SPEED-ONLY: a warm
// cache must return exactly what a cold one computes, bit for bit, or the item
// stops being a planning-speed change and becomes a plan change.
//
// Each test below asks the same question in the same shape: compute the answer
// on a clause the cache has never seen, then compute it again on a SECOND
// clause that is structurally identical but whose memo is cold, and require
// bitwise equality. Comparing warm-vs-cold rather than warm-vs-warm is the
// point — a memo that returned its own stored value on both calls would pass a
// warm-vs-warm test no matter what it stored.

// riCacheCtx is `jsCtx` with histograms, which `mergeJoinScanSel` needs: with
// no histogram its estimate short-circuits to (1, 1) and every assertion below
// would hold vacuously.
func riCacheCtx(t *testing.T) *searchCtx {
	t.Helper()
	c := catalog.NewInMemory()
	hist := func(step int) []string {
		out := make([]string, 0, 11)
		for i := 0; i <= 10; i++ {
			out = append(out, fmt.Sprint(i*step))
		}
		return out
	}
	orders := jsTable(t, c, "orders", []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "int4"}},
		{Name: "o_custkey", Type: catalog.Type{Name: "int4"}},
	}, 1500000,
		catalog.ColumnStats{NDistinctFrac: 1.0, Histogram: hist(1000)},
		catalog.ColumnStats{NDistinctFrac: 0.1, Histogram: hist(700)},
	)
	lineitem := jsTable(t, c, "lineitem", []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}},
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000,
		catalog.ColumnStats{NDistinctFrac: 0.25, Histogram: hist(400)},
		catalog.ColumnStats{NDistinct: 20000, Histogram: hist(200)},
	)
	s, err := newSearchCtx(2, defaultCostParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: orders, baseRows: 1500000, filteredRows: 1500000},
		{table: lineitem, baseRows: 6000000, filteredRows: 6000000},
	}
	return s
}

// TestNormSelecCacheIsSpeedOnly: the second call on a warm clause and the
// first call on an identical cold clause must agree bitwise, for every arm of
// the dispatch (equality, its negation, the inequality constant, and the
// unhandled default), so no arm can be memoised at the wrong value.
func TestNormSelecCacheIsSpeedOnly(t *testing.T) {
	s := riCacheCtx(t)
	for _, op := range []parser.OpCode{parser.OpEq, parser.OpNe, parser.OpLt, parser.OpGe} {
		warm := jsEqui(op)
		cold := jsEqui(op)
		got, gotDefault := s.joinClauseSelectivityExt(warm)
		if !warm.normSelecValid {
			t.Fatalf("op %v: cache not populated", op)
		}
		again, againDefault := s.joinClauseSelectivityExt(warm)
		fresh, freshDefault := s.joinClauseSelectivityExt(cold)
		if again != got || fresh != got {
			t.Fatalf("op %v: cold=%v warm=%v other-cold=%v; want all equal", op, got, again, fresh)
		}
		if againDefault != gotDefault || freshDefault != gotDefault {
			t.Fatalf("op %v: isdefault cold=%v warm=%v other-cold=%v", op, gotDefault, againDefault, freshDefault)
		}
	}
}

// TestMergeScanSelCacheIsSpeedOnly is the same contract for the merge-join end
// selectivities, plus the property the memo's one-entry shape rests on: the
// two orientations of a commuted join are each other's swap, so ONE stored
// pair serves both.
func TestMergeScanSelCacheIsSpeedOnly(t *testing.T) {
	s := riCacheCtx(t)
	warm := jsEqui(parser.OpEq)
	cold := jsEqui(parser.OpEq)
	outerLeft := relsetOf(0)
	outerRight := relsetOf(1)

	l := s.mergeJoinScanSel([]*restrictInfo{warm}, outerLeft)
	if !warm.scanSelValid {
		t.Fatal("merge scan-sel cache not populated")
	}
	if l2 := s.mergeJoinScanSel([]*restrictInfo{warm}, outerLeft); l2 != l {
		t.Fatalf("warm %+v != cold %+v", l2, l)
	}
	if f := s.mergeJoinScanSel([]*restrictInfo{cold}, outerLeft); f != l {
		t.Fatalf("other-cold %+v != cold %+v", f, l)
	}
	// The estimate must actually have been computed, or the swap assertion
	// below is satisfied by the full-scan default meaning "declined".
	if l == fullMergeScan {
		t.Fatalf("full-scan default — the fixture did not reach the histogram arm")
	}
	// Commuting the join swaps the pair and nothing else.
	r := s.mergeJoinScanSel([]*restrictInfo{jsEqui(parser.OpEq)}, outerRight)
	want := mergeScanSel{outerStart: l.innerStart, outerEnd: l.innerEnd, innerStart: l.outerStart, innerEnd: l.outerEnd}
	if r != want {
		t.Fatalf("commuted %+v; want the swap %+v", r, want)
	}
}

// TestJoinKeyPairCacheIsSpeedOnly pins the operand-resolution memo. The
// resolution names a RELATION and a COLUMN, and a memo that returned the wrong
// relation would aim a uniqueness proof at another table's statistics — the
// failure `resolveJoinVarColumn`'s own header calls out — so the assertion is
// on the resolved pair, not just on `usable`.
func TestJoinKeyPairCacheIsSpeedOnly(t *testing.T) {
	s := riCacheCtx(t)
	warm := jsEqui(parser.OpEq)
	cold := jsEqui(parser.OpEq)
	outer, inner := relsetOf(0), relsetOf(1)

	got, ok := s.joinKeyPairOf(warm, outer, inner)
	if !ok {
		t.Fatal("clause did not resolve to a key pair")
	}
	if !warm.keyPairValid {
		t.Fatal("key-pair cache not populated")
	}
	again, okAgain := s.joinKeyPairOf(warm, outer, inner)
	fresh, okFresh := s.joinKeyPairOf(cold, outer, inner)
	if !okAgain || !okFresh || again != got || fresh != got {
		t.Fatalf("cold=%+v warm=%+v other-cold=%+v", got, again, fresh)
	}
	if got.rel != [2]int{0, 1} || got.col != [2]string{"o_custkey", "l_orderkey"} {
		t.Fatalf("resolved to %+v; want orders.o_custkey / lineitem.l_orderkey", got)
	}
	// The SIDE test is not cached: commuting the join keeps the pair usable
	// and must still go through the per-call subset checks.
	if _, okRev := s.joinKeyPairOf(warm, inner, outer); !okRev {
		t.Fatal("commuted join lost its key pair — the side test was memoised")
	}
}
