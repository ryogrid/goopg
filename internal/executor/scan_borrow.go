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

// markScanBorrowChain hands the seq scan at the bottom of op the borrow
// (M0145-0008u) when every operator between it and a borrowing aggregate
// passes each row up before pulling the next and keeps nothing of it — PG's
// slot discipline, where a scan slot's values stay valid until the next fetch
// and a node that keeps a tuple copies it (ExecMaterializeSlot).
//
// Two operators pass rows through that way:
//   - a Project evaluates its targets into its own reused row (projectOp.Next)
//     and returns before pulling again;
//   - a non-lateral hash join of any type reads its probe side one row at a
//     time. It holds only the prefetched first row (while it builds, with the
//     probe child not advanced) and the current row (while it emits that row's
//     matches or its null-extended self); a row a spilling batch defers is
//     deep-copied (spill.go). Its build side is kept in the hash table and
//     never borrows, and the RIGHT / FULL / RIGHT ANTI sweep after the last
//     probe row emits build rows only (PG's HJ_FILL_INNER_TUPLES).
//
// A parameterised nested loop (NestedLoopIndexJoin) passes its outer rows
// through the same way. Anything else stops the descent, a Filter included
// (its batch path reads ahead). A Gather ends it too, after marking its leader tree (see
// gatherOp.borrowLeader).
//
// Besides a borrowing aggregate, two roots start a descent: a hash join's
// build side (joinBorrowBuildChild) and a Gather worker's tree
// (gatherOp.runWorker), each of which copies every row it keeps. TPC-H Q17's lineitem probe, Aggregate → Hash Join → Project → Seq
// Scan, cloned all 6M rows because the borrow reached only a scan directly
// under the aggregate; TPC-H Q13's orders probe under a Hash Right Join
// cloned its 1.5M rows because the descent stopped at the RIGHT join.
func markScanBorrowChain(op Operator) {
	if scanBorrowDisabled {
		return
	}
	for op != nil {
		if io, ok := op.(*instrumentedOp); ok {
			op = io.inner
			continue
		}
		switch o := op.(type) {
		case *seqScanOp:
			o.borrowRows = true
			return
		case *projectOp:
			op = o.child
		case *joinOp:
			if o.plan != nil && o.plan.Algo == optimizer.JoinAlgoNestedLoop {
				// The streaming nested loops (join_nl_stream.go, and
				// join_lateral_stream.go for a parameterised inner) pull
				// the next outer row only once the inner side is done with
				// the current one, and keep nothing of it afterwards. The
				// inner side is re-scanned (or, for LATERAL, re-opened) per
				// outer row; what it caches is keyed by formatted strings
				// (Memoize, the sublink caches) or reset per outer row (the
				// lateral CTE cache). PG's NestLoop holds the outer slot
				// across the inner rescans the same way. TPC-H Q3's orders
				// probe sits under one.
				//
				// The loop's own output passes up the same chain, so it is
				// borrowed too: the joined pair is emitted from the reused
				// buffer, not cloned per row (M0122-0012 slice 37).
				o.nlOuterBorrowed = true
				o.nlEmitBorrowed = true
				op = o.left
				continue
			}
			op = joinBorrowProbeChild(o)
		case *nestedLoopIndexJoinOp:
			// The parameterised nested loop copies only the outer row's
			// slice (currentOuter) and pulls the next outer row only after
			// the inner probe is exhausted, so the outer scan's values stay
			// valid for every match it emits — PG's NestLoop holds the outer
			// slot across the inner rescans the same way. Its inner keeps
			// nothing of the outer row: the index probe reads the bound
			// slot at Rescan, and Memoize keys its cache by a formatted
			// string. TPC-H Q3's orders probe sits under one.
			op = o.outer
		case *gatherOp:
			// The leader's own rows pass straight up (gatherOp.Next); worker
			// rows are copied before they are queued either way.
			o.borrowLeader = true
			return
		case *setOp:
			// A streaming UNION ALL (Append / Parallel Append, nextStreaming)
			// returns each child slot as it is and keeps nothing between
			// calls — it drains one branch, then the other — so both branches
			// pass the borrow on (M0122-0012 slice 51; TPC-DS Q71's Parallel
			// Append over three Parallel Hash Joins cloned every probe row).
			// A Merge Append (nextMerge) holds each side's front row across
			// calls, and the other set operations buffer, so they stop the
			// walk.
			if o.streaming && len(o.plan.MergeKeys) == 0 {
				markScanBorrowChain(o.left)
				markScanBorrowChain(o.right)
			}
			return
		default:
			return
		}
	}
}

// joinBorrowBuildChild is the build-side child of a non-lateral hash join,
// or nil. Every build loop copies each row it keeps before pulling the next
// one: retainBuildRow re-homes arena payloads into the join's build arena
// (buildLoopLeft / buildLoopRight, which Parallel Hash participants also run),
// and the FOR UPDATE build clones in drainRowsCtxCTID. So the build side is a
// borrow root of its own, whatever sits above the join — PG's
// ExecHashTableInsert likewise copies the slot into the hash table
// (ExecFetchSlotMinimalTuple + memcpy). TPC-H Q10 and Q21 cloned every
// lineitem row that fed a Parallel Hash this way.
func joinBorrowBuildChild(o *joinOp) Operator {
	if joinBorrowProbeChild(o) == nil {
		return nil
	}
	if o.hashBuildIsLeft() {
		return o.left
	}
	return o.right
}

// joinBorrowProbeChild is the probe-side child of a hash join that passes its
// probe rows through without keeping them (markScanBorrowChain), or nil. The
// probe is the side the lazy-hash runtime does not build (hashBuildIsLeft,
// which forces semi/anti builds onto the right), not plan.BuildLeft's raw
// value.
func joinBorrowProbeChild(o *joinOp) Operator {
	if o.plan == nil || o.plan.Algo != optimizer.JoinAlgoHash || o.plan.Lateral {
		return nil
	}
	switch o.plan.Type {
	case optimizer.JoinTypeInner, optimizer.JoinTypeLeft, optimizer.JoinTypeRight, optimizer.JoinTypeFull,
		optimizer.JoinTypeSemi, optimizer.JoinTypeAnti, optimizer.JoinTypeRightSemi, optimizer.JoinTypeRightAnti:
		if o.hashBuildIsLeft() {
			return o.right
		}
		return o.left
	}
	return nil
}
