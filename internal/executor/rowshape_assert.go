package executor

// Planner-vs-executor row-shape assertion — take2, from the P4-A rev 5 review.
//
// WHY THIS EXISTS. The reverted P4-01b narrowed a scan's planner-side schema
// while `newSeqScanOp` kept decoding the full table width
// (`cols: p.Table.Columns`, `scanRow` sized `len(o.cols)`). The planner then
// re-based every consumer onto narrowed positions while the executor still
// emitted table-order rows, so ColumnRef i read table column i: TPC-H Q2 and Q5
// returned 0 rows and Q18 returned the right COUNT with the wrong tuples.
//
// Every existing tripwire missed it, and the reason is structural: `boundaryMap`'s
// totality panic and `translateToLayout`'s refusal both check the PLAN against
// itself, and the plan was self-consistent. The violated invariant spans the two
// sides — *a node's `Output()` must equal the row its operator emits* — so
// nothing that looks at one side alone can see it.
//
// This is that invariant, asserted. It is off by default and costs one integer
// compare per row when on.
//
//	GOOPG_ASSERT_ROW_SHAPE=1
//
// Intended use: any change to projection, scan schemas or `PathTarget` runs its
// unit suite and one TPC-H sweep with this set. That turns the whole failure
// class from a benchmark-run discovery into a test failure naming the operator.

import (
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/optimizer"
)

var assertRowShapeEnabled = os.Getenv("GOOPG_ASSERT_ROW_SHAPE") == "1"

// assertRowShapeInline checks one emitted row against the schema its operator
// advertises. Call it from the operator's own Next, NOT from a wrapper.
//
// WHY NOT A WRAPPER. The obvious implementation — wrap every Operator in
// maybeInstrument — was built, and it CHANGED QUERY RESULTS: TPC-H went to
// 7 VALUE-DIFF / 4 ROWS-DIFF with zero assertion failures, i.e. the wrapper
// itself was the bug. This package discovers operator capabilities by TYPE
// ASSERTION — ten `op.(type)` switches plus `op.(*lockRowsOp)`,
// `bitmapProducer`, `lateralBindable`, `heapFetchCounter` and others — and an
// opaque wrapper hides all of them, so a type switch falls to its default arm.
// `instrumentedOp` gets away with it only because it is confined to
// EXPLAIN ANALYZE. There is no general unwrap protocol to hook (`underlying()`
// exists on that one type alone).
//
// So the check is inline and placed where the invariant is actually at risk:
// the scan operators, which are what a PathTarget narrows.
func assertRowShapeInline(label string, schema optimizer.Schema, width int) {
	if !assertRowShapeEnabled {
		return
	}
	if want := len(schema); want != width {
		panic(fmt.Sprintf(
			"row-shape assertion: %s emitted a %d-column row but advertises a "+
				"%d-column schema — a consumer resolving column i will read the "+
				"wrong value (GOOPG_ASSERT_ROW_SHAPE)",
			label, width, want))
	}
}
