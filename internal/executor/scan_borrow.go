package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
)

// scanBorrowDisabled forces the per-tuple clone everywhere. Tests set it to
// compare a borrowing plan's results with the cloning path's.
var scanBorrowDisabled = false

// aggregateBorrowsScanRows reports whether agg provably keeps nothing of an
// input row past the row's own transition step, so a seq scan below it may
// hand up its reused row instead of a per-tuple clone (M0145-0008u).
//
// The seq scan's clone detaches the row slice (reused for the next tuple) and
// its arena-backed datums (the per-page arena resets at the next page). An
// aggregate is safe when every value it retains is detached by the aggregate
// itself or freshly computed:
//   - group keys: MaterializeArena'd at group creation (aggregateOp.Open);
//   - count / sum / avg: counters or freshly computed sums (numericAdd
//     always builds a new value);
//   - min / max: MaterializeArena'd when stored.
//
// Everything else is refused, because it can keep an input value as is:
// passthrough columns (stored from the group's first row undetached), user
// aggregates (an sfunc may return its argument as the state), DISTINCT
// (seen-sets), ordered and ordered-set aggregates (collected rows), and the
// sorted strategy (the current group's key spans rows). A Finalize node reads
// a Gather, never a scan.
func aggregateBorrowsScanRows(agg *optimizer.Aggregate) bool {
	if scanBorrowDisabled || agg == nil || agg.Mode == optimizer.AggModeFinal || len(agg.Passthrough) > 0 ||
		agg.Strategy == optimizer.AggStrategySorted {
		return false
	}
	for i := range agg.Aggs {
		a := &agg.Aggs[i]
		if a.UserAgg != nil || a.Distinct || a.WithinGroup || len(a.OrderBy) > 0 || len(a.WithinGroupOrderBy) > 0 {
			return false
		}
		switch strings.ToLower(a.Name) {
		case "count", "sum", "avg", "min", "max":
		default:
			return false
		}
	}
	return true
}
