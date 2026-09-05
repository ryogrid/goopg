package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
)

// E-15 (EX3-07): the presorted-prefix input contract a future Incremental
// Sort operator must be built against — published BEFORE and independently
// of planner C-14 (take3 13 §8.7 tiebreaker), so the two sides never wait
// on each other.
//
// INPUT GUARANTEE (what the planner must promise, C-14's half):
//
//   - The input arrives ordered by the FIRST n sort keys (n = presorted
//     count, 0 < n < len(keys)), each under that key's own direction and
//     NULL placement. "Ordered" means exactly what sortOp emits for those
//     keys today (lessKeyVals restricted to the prefix).
//   - Rows with equal prefix values are CONTIGUOUS (a group); groups arrive
//     in prefix order. Equality is sortPrefixEqual below: per-key
//     compareDatum == 0 with both-NULL counting as equal — direction plays
//     no role in equality, matching PG's group framing (nodesort.c builds
//     each group with an independent full tuplesort).
//
// EXECUTOR GUARANTEE (what a future node must deliver; pinned by the
// contract tests against CURRENT full-sort behaviour):
//
//   - Output is fully ordered over all keys, identical (as a sequence) to
//     what sortOp produces for the same input — order-equivalence, the
//     property an incremental execution must preserve.
//   - Peak memory is bounded by the largest GROUP, not the input (that is
//     the whole point; a degenerate single-group input degrades to a full
//     sort and must still be correct).
//   - A group split on an incomparable key pair (compareDatum error) is a
//     new group, never an error and never a merged group: over-splitting
//     costs a group sort (perf), merging would emit wrong order.
//
// NON-GUARANTEES: stability across groups is not promised beyond what the
// full sort does today (sortOp is SliceStable; keep it); spill-path
// framing (chunk/merge) is orthogonal — groups compose with, not inside,
// the existing chunk discipline (decided at E-03 implementation, not here).

// sortPrefixEqual reports whether two evaluated key rows belong to the same
// presorted group: the first n keys compare equal. Both-NULL counts as
// equal. n <= 0 is vacuously true; n is clamped to the available widths.
// An incomparable pair (compareDatum error) returns false — see contract.
func sortPrefixEqual(a, b []Datum, keys []optimizer.SortKey, n int) bool {
	if n <= 0 {
		return true
	}
	if n > len(keys) {
		n = len(keys)
	}
	if n > len(a) {
		n = len(a)
	}
	if n > len(b) {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		av, bv := a[i], b[i]
		if av.IsNull() && bv.IsNull() {
			continue
		}
		if av.IsNull() || bv.IsNull() {
			return false
		}
		// Nil-Expr leniency (pos=0) diverges from lessKeyVals/lessRows,
	// which call k.Expr.Pos() unguarded: production keys always carry
	// Expr, so this arm serves tests only and can only widen equality
	// toward split-safe behaviour, never toward a merge.
	pos := 0
	if keys[i].Expr != nil {
		pos = keys[i].Expr.Pos()
	}
		cmp, err := compareDatum(av, bv, pos)
		if err != nil {
			// Defensive: for non-null Datums compareDatum effectively
			// never errors (cross-kind falls back to Format-compare);
			// only unhandled DatumKinds reach the error arm. Split
			// (never merge) on any error — see contract.
			return false
		}
		if cmp != 0 {
			return false
		}
	}
	return true
}
