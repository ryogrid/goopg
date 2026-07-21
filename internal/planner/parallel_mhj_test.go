package planner

// Chapter 12 — the planner-side integration of the parallel multi-way hash
// join, and specifically the safety hole its enabling change opens (§7).
//
// Making drivingSeqScan descend into an MHJ's probe side is harmless on its
// own. What makes it dangerous is that subtreeHasUnsafeNode walks via
// parallelChildren, and parallelChildren used not to list *MultiHashJoin — so
// the safety walk stopped at the MHJ and treated "no children" as "nothing
// unsafe below". A temp or virtual relation under the probe side would then be
// approved for a plan a worker cannot execute. These tests pin that the walk
// now sees through an MHJ.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// mhjOver builds a MultiHashJoin whose probe is the first table and whose other
// tables are dimension builds.
func mhjOver(t *testing.T, probe Node, dims ...Node) *MultiHashJoin {
	t.Helper()
	tables := append([]Node{probe}, dims...)
	keys := make([]MultiHashKey, 0, len(dims))
	for i := range dims {
		keys = append(keys, MultiHashKey{LeftTable: 0, LeftCol: 0, RightTable: i + 1, RightCol: 0})
	}
	return &MultiHashJoin{Tables: tables, Keys: keys, ProbeTable: 0}
}

// TestMHJBecomesPartialCapable: a Gather must be placed over an MHJ whose probe
// is a large plain relation.
func TestMHJBecomesPartialCapable(t *testing.T) {
	fact := bigTable(t, "mhj_fact")
	d1 := bigTable(t, "mhj_d1")
	root := mhjOver(t, seqScanOver(fact), seqScanOver(d1))

	got := MaybeAddGather(root, parallelTestSettings())
	if _, ok := got.(*Gather); !ok {
		t.Fatalf("root is %T, want *Gather over the MHJ", got)
	}
}

// TestMHJProbeIsSized: the workers must be sized off the PROBE relation, so a
// small probe yields no Gather even when a dimension table is large. This is
// what makes the removed "probe-selection floor" redundant (§6).
func TestMHJProbeIsSized(t *testing.T) {
	smallProbe := bigTable(t, "mhj_small") // "big" fixture, but we size it small below
	bigDim := bigTable(t, "mhj_bigdim")
	root := mhjOver(t, seqScanOver(smallProbe), seqScanOver(bigDim))

	s := parallelTestSettings()
	// Size the probe below the threshold and the dimension above it. Only the
	// probe should matter.
	s.BlocksForTable = func(tbl *catalog.Table) (int64, bool) {
		if tbl == smallProbe {
			return 8, true // below MinTableScanBlocks
		}
		return 100000, true
	}
	if _, ok := MaybeAddGather(root, s).(*Gather); ok {
		t.Fatal("Gather placed for an MHJ whose PROBE is small; the worker " +
			"count must be sized off the probe, not a large dimension table")
	}
}

// TestMHJSafetyWalkSeesProbeSide is the headline safety test. A temp relation
// under the MHJ's probe side must suppress parallelism — the walk has to see
// through the MHJ to find it.
func TestMHJSafetyWalkSeesProbeSide(t *testing.T) {
	fact := bigTable(t, "mhj_fact_tmp")
	fact.Temp = true // a worker cannot read this session's temp table
	d1 := bigTable(t, "mhj_d1s")
	root := mhjOver(t, seqScanOver(fact), seqScanOver(d1))

	if _, ok := MaybeAddGather(root, parallelTestSettings()).(*Gather); ok {
		t.Fatal("Gather placed over an MHJ whose probe reads a TEMP relation; " +
			"subtreeHasUnsafeNode did not see through the MHJ")
	}
}

// TestMHJSafetyWalkSeesBuildSide: the unsafe relation on a BUILD side must also
// be seen. parallelChildren returns all Tables, not just the probe, precisely
// so the safety walk is complete.
func TestMHJSafetyWalkSeesBuildSide(t *testing.T) {
	fact := bigTable(t, "mhj_fact2")
	tmpDim := bigTable(t, "mhj_dim_tmp")
	tmpDim.Temp = true
	root := mhjOver(t, seqScanOver(fact), seqScanOver(tmpDim))

	if _, ok := MaybeAddGather(root, parallelTestSettings()).(*Gather); ok {
		t.Fatal("Gather placed over an MHJ with a TEMP relation on a build " +
			"side; the safety walk must descend every Table")
	}
}

// TestMHJParallelChildrenReturnsAllTables pins the mechanism the safety tests
// rely on.
func TestMHJParallelChildrenReturnsAllTables(t *testing.T) {
	fact := bigTable(t, "mhj_f")
	d1 := bigTable(t, "mhj_a")
	d2 := bigTable(t, "mhj_b")
	root := mhjOver(t, seqScanOver(fact), seqScanOver(d1), seqScanOver(d2))
	if kids := parallelChildren(root); len(kids) != 3 {
		t.Fatalf("parallelChildren(MHJ) returned %d children, want 3 (all "+
			"Tables) — the safety walk needs every table visible", len(kids))
	}
}

// TestMHJMalformedRefuses: an out-of-range ProbeTable must fail toward serial,
// not panic.
func TestMHJMalformedRefuses(t *testing.T) {
	fact := bigTable(t, "mhj_mal")
	root := mhjOver(t, seqScanOver(fact))
	root.ProbeTable = 99 // out of range

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MaybeAddGather panicked on a malformed MHJ: %v", r)
		}
	}()
	if _, ok := MaybeAddGather(root, parallelTestSettings()).(*Gather); ok {
		t.Fatal("Gather placed over a malformed MHJ")
	}
}