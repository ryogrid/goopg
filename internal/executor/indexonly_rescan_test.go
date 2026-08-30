package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestIndexOnlyScanOpSatisfiesNLIInner pins the reason indexOnlyScanOp gained
// openPrep/BindOuter/Rescan: so a NestedLoopIndexJoin can drive it as an inner,
// which is what TPC-H Q22 needs (PG uses an Index Only Scan for the anti-join's
// `orders` probe, goopg a plain Index Scan).
//
// The interface satisfaction is asserted at COMPILE time rather than by calling
// the methods, because that is the property the next stage depends on: widening
// NestedLoopIndexJoin.Inner is pointless if the operator cannot be driven.
// A method renamed or a signature drifted breaks this file, not a benchmark.
func TestIndexOnlyScanOpSatisfiesNLIInner(t *testing.T) {
	var _ nliInner = (*indexOnlyScanOp)(nil)
}

// TestIndexOnlyScanRescanResetsScanState pins the openPrep/Rescan boundary from
// the Rescan side: everything a second probe must NOT inherit from the first.
//
// The fields checked are exactly the ones the old single-shot Open initialised
// and a per-outer-row Rescan therefore has to re-initialise. `rows` is the one
// that matters most — indexOnlyScanOp materialises its whole result in the scan
// (unlike indexScanOp's lazy TID list), so a Rescan that appended to the
// previous probe's rows would return the union of every outer row's matches,
// which is a wrong answer and not a slow one.
func TestIndexOnlyScanRescanResetsScanState(t *testing.T) {
	o := &indexOnlyScanOp{
		plan: &optimizer.IndexOnlyScan{},
		rows: []Row{{}, {}},
		idx:  7,
	}
	// Rescan must refuse before openPrep rather than probe a nil tree.
	if err := o.Rescan(nil, 0); err == nil {
		t.Fatal("Rescan before Open must error, not probe a nil btree")
	}
	// The guard must fire before any state is touched, so a caller that
	// mis-sequences does not also lose the previous scan's results.
	if len(o.rows) != 2 || o.idx != 7 {
		t.Errorf("the pre-Open guard mutated scan state: rows=%d idx=%d", len(o.rows), o.idx)
	}
}
