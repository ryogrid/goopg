package optimizer

// M0127-P5.5-a — `IndexPath.indexinfo` + `indexscandir` on goopg's `Path`
// (pathindexcarrier.go).
//
// The slice's consumer, P5.5's `createPlan` arm, does not exist yet, so nothing
// in the repository can yet observe a wrong answer here. What these tests pin is
// therefore the pair of invariants that arm will DEPEND on, each falsifiable on
// its own:
//
//   - every `PathIndexScan` the search builds names the index its cost and rows
//     were computed for, and no other path kind names one (a nil `IndexInfo` on
//     a chosen path is the failure `createPlan` cannot recover from); and
//   - the recorded direction and the recorded pathkeys describe the SAME scan.
//     A path whose keys were built backward under a forward `IndexScanDir` would
//     make `createPlan` emit a forward scan under an ordering that scan does not
//     deliver — a wrong answer, not a slow plan (hard-won rule #2).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestScanDirectionMatchesPGEncoding: the -1/0/+1 values of `ScanDirection`
// (access/sdir.h:24-29). They are asserted rather than assumed because the
// encoding is load-bearing twice over — PG's `ScanDirectionCombine` multiplies
// two directions, and goopg relies on `NoMovementScanDirection` being the ZERO
// value so that "not an index path" needs no separate sentinel.
func TestScanDirectionMatchesPGEncoding(t *testing.T) {
	if BackwardScanDirection != -1 || NoMovementScanDirection != 0 || ForwardScanDirection != 1 {
		t.Fatalf("ScanDirection encoding = (%d,%d,%d); want PG's (-1,0,1)",
			BackwardScanDirection, NoMovementScanDirection, ForwardScanDirection)
	}
	var zero ScanDirection
	if zero != NoMovementScanDirection {
		t.Fatalf("zero ScanDirection = %v; want nomovement, so a non-index Path needs no sentinel", zero)
	}
	if got := ForwardScanDirection.String(); got != "forward" {
		t.Fatalf("ForwardScanDirection.String() = %q; want %q", got, "forward")
	}
}

// TestIndexPathOrderingPairsKeysWithDirection is the reason the helper exists:
// the direction it returns is the direction the keys it returns were built for.
// Callers cannot obtain one without the other, so the two cannot drift.
func TestIndexPathOrderingPairsKeysWithDirection(t *testing.T) {
	idx, exprs := bipIndex("a"), bipExprs("a")

	fwdKeys, fwdDir := indexPathOrdering(idx, exprs, false)
	if fwdDir != ForwardScanDirection {
		t.Fatalf("forward request gave direction %v", fwdDir)
	}
	wantKeyNames(t, fwdKeys, "a")
	if !fwdKeys[0].SortAsc || fwdKeys[0].NullsFirst {
		t.Fatalf("forward key = %+v; want ASC NULLS LAST", fwdKeys[0])
	}

	bwdKeys, bwdDir := indexPathOrdering(idx, exprs, true)
	if bwdDir != BackwardScanDirection {
		t.Fatalf("backward request gave direction %v", bwdDir)
	}
	// The inversion `build_index_pathkeys` applies (pathkeys.c:770-774) — asserted
	// HERE too, not only in the pathkey builder's own tests, because the whole
	// point of the helper is that this ordering travels with that direction.
	if bwdKeys[0].SortAsc || !bwdKeys[0].NullsFirst {
		t.Fatalf("backward key = %+v; want DESC NULLS FIRST beside a backward direction", bwdKeys[0])
	}
}

// TestIndexPathOrderingUnorderableIndexIsStillForward: pathnodes.h:1834 —
// "unordered indexes will always have an indexscandir of ForwardScanDirection".
// A `USING hash` index promises no order (`indexIsOrderable` says so), but it is
// still probed forward; returning `NoMovementScanDirection` here would make an
// unorderable index path indistinguishable from a non-index path.
func TestIndexPathOrderingUnorderableIndexIsStillForward(t *testing.T) {
	idx := bipIndex("a")
	idx.DeclaredHash = true
	keys, dir := indexPathOrdering(idx, bipExprs("a"), false)
	if len(keys) != 0 {
		t.Fatalf("hash index offered pathkeys %v; want none", keys)
	}
	if dir != ForwardScanDirection {
		t.Fatalf("unorderable index direction = %v; want forward (pathnodes.h:1834)", dir)
	}
}

// TestParameterizedIndexPathNamesItsIndex: the parameterised constructor
// (`addOneParameterizedIndexPath`) records the index `pickIndexCoveringAllLeadingColumns`
// chose — the same index `parameterizedBaserelRows` and the cost were computed
// over. Without it a chosen NLI inner could not be re-emitted as the probe that
// was costed.
func TestParameterizedIndexPathNamesItsIndex(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := ppiCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))

	s.addParameterizedIndexPaths(cat)

	var got *Path
	for _, p := range s.findRel(inner).Pathlist {
		if p.Kind == PathIndexScan && p.RequiredOuter != 0 {
			got = p
			break
		}
	}
	if got == nil {
		t.Fatal("no parameterised index path was built")
	}
	if got.IndexInfo == nil {
		t.Fatal("parameterised index path names no index; createPlan cannot rebuild the probe")
	}
	if got.IndexInfo.Name != "orders_pkey" {
		t.Fatalf("IndexInfo = %q; want the orders_pkey the clause binds", got.IndexInfo.Name)
	}
	if got.IndexScanDir != ForwardScanDirection {
		t.Fatalf("IndexScanDir = %v; want forward (goopg emits no backward scan)", got.IndexScanDir)
	}
}

// TestOrderedIndexPathNamesItsIndex: the same for the unparameterised ordered
// constructor. This path is a FULL scan of the index — PG's "an empty
// indexclauses list implies a full index scan" (pathnodes.h:1817) — so the index
// name is the whole of what identifies it.
func TestOrderedIndexPathNamesItsIndex(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))

	s.addOrderedIndexPaths(cat)

	paths := orderedPathsOf(s.findRel(inner))
	if len(paths) != 1 {
		t.Fatalf("got %d unparameterised index paths; want exactly one", len(paths))
	}
	p := paths[0]
	if p.IndexInfo == nil || p.IndexInfo.Name != "orders_pkey" {
		t.Fatalf("IndexInfo = %v; want orders_pkey", p.IndexInfo)
	}
	if p.IndexScanDir != ForwardScanDirection {
		t.Fatalf("IndexScanDir = %v; want forward", p.IndexScanDir)
	}
	// The pairing invariant end-to-end: the ordering on the path is the FORWARD
	// ordering of the index it names, not the inverted one.
	if !p.Pathkeys[0].SortAsc || p.Pathkeys[0].NullsFirst {
		t.Fatalf("pathkey %+v does not describe the forward scan the path claims", p.Pathkeys[0])
	}
}

// TestNonIndexPathsCarryNoIndex: the negative half. `IndexInfo` is set on a
// `PathIndexScan` and on nothing else, which is what lets one flat struct carry
// the field without a second discriminator — and what lets P5.5's createPlan
// treat a nil `IndexInfo` on a `PathIndexScan` as a bug rather than as a
// legitimate state.
func TestNonIndexPathsCarryNoIndex(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))
	s.addBaseRelIndexPaths(cat)

	for _, rs := range []RelSet{outer, inner} {
		for _, p := range s.findRel(rs).Pathlist {
			if p.Kind == PathIndexScan {
				if p.IndexInfo == nil {
					t.Fatalf("rel %d: an index path names no index", rs)
				}
				continue
			}
			if p.IndexInfo != nil {
				t.Fatalf("rel %d: path kind %d names index %q", rs, p.Kind, p.IndexInfo.Name)
			}
			if p.IndexScanDir != NoMovementScanDirection {
				t.Fatalf("rel %d: path kind %d carries direction %v; want the zero value",
					rs, p.Kind, p.IndexScanDir)
			}
		}
	}
}
