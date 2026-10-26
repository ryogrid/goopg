package optimizer

import (
	"fmt"
	"math"
	"os"
)

// The Sort spill decision prices cost_tuplesort's own byte currency —
// relation_byte_size(tuples, pathtarget width) (costsize.c). R113 shipped it
// default-off; M0146-0234 turned it on: PG prices every sort with this
// formula, and goopg's executor-entry-byte currency (hashsize.EntryBytes)
// priced TPC-DS SF1 Q64's store_sales Sort as a disk sort (1190592) where PG's
// fits in work_mem (389568.13, which the PG currency reproduces exactly).
// M0147-0042 retired the GOOPG_PG_SORT_RELATION_BYTES_COST=0 measurement
// switch. The goopg currency survives only as the fallback for a Sort with no
// valid planner width (costSortRunWithWidth).

const pgMaxAlignBytes = 8

func pgSortMaxAlign(n int) (int, bool) {
	if n <= 0 || n > math.MaxInt-(pgMaxAlignBytes-1) {
		return 0, false
	}
	return (n + pgMaxAlignBytes - 1) &^ (pgMaxAlignBytes - 1), true
}

// pgRelationByteSize is PG18's relation_byte_size: a planner-private heap
// tuple representation, not Goopg's runtime sort or Datum allocation size.
func pgRelationByteSize(rows float64, width int) (float64, bool) {
	if rows < 0 || math.IsNaN(rows) || math.IsInf(rows, 0) {
		return 0, false
	}
	alignedWidth, ok := pgSortMaxAlign(width)
	alignedHeader, headerOK := pgSortMaxAlign(int(pgHeapTupleHeaderBytes))
	if !ok || !headerOK || alignedWidth > math.MaxInt-alignedHeader {
		return 0, false
	}
	rowBytes := alignedWidth + alignedHeader
	bytes := rows * float64(rowBytes)
	if math.IsInf(bytes, 0) || math.IsNaN(bytes) {
		return 0, false
	}
	return bytes, true
}

// tracePGSortRelationBytes records both currencies for every costed Sort when
// shaped-DP tracing is active. It observes an election but never participates
// in one; caller names the only production call paths that price a Sort.
func tracePGSortRelationBytes(caller string, inputRows, tuples, output, limitTuples float64, width int,
	goopgInput, goopgOutput, pgInput, pgOutput float64, goopgBranch, pgBranch, currency string) {
	if !pathTraceEnabled {
		return
	}
	fmt.Fprintf(os.Stderr,
		"DPPGSORT caller=%s inputrows=%.6g tuples=%.6g output=%.6g limit=%.6g width=%d goopginput=%.6g goopgoutput=%.6g pginput=%.6g pgoutput=%.6g goopgbranch=%s pgbranch=%s currency=%s\n",
		caller, inputRows, tuples, output, limitTuples, width,
		goopgInput, goopgOutput, pgInput, pgOutput, goopgBranch, pgBranch, currency)
}
