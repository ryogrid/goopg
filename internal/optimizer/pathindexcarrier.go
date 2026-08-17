package optimizer

// M0127-P5.5-a — `IndexPath.indexinfo` + `IndexPath.indexscandir`: the two
// facts a chosen index path must carry so that `createPlan` can re-emit it.
//
// PG oracle: `IndexPath` (postgres/src/include/nodes/pathnodes.h:1842),
// `ScanDirection` (postgres/src/include/access/sdir.h:24),
// `create_index_path` (pathnode.c:1041) which stores both, and
// `create_indexscan_plan` (createplan.c:3000) which reads them back out.
// Design: leftdeep-joins 03 §10, 04 §1.1.
//
// Why this is a slice of its own, ahead of the `createPlan` arm that consumes
// it. P5.4c-ii-b ledgered the finding that motivates it: goopg's `Path` is one
// flat struct with a `Kind` discriminator, and `PathIndexScan` recorded the
// ORDERING, COST and ROWS of a specific index scan without recording WHICH
// index produced them. The DP could therefore choose a path that no `*IndexScan`
// node can be built from — the search would win an argument it could not then
// carry out. PG never has this problem because `IndexPath` is a distinct node
// type whose extra fields exist by construction; goopg has to add them
// deliberately, and P5.5's `createPlan` arm is the slice that would otherwise
// have to invent them from nothing.
//
// The DIRECTION is carried even though goopg only ever records
// `ForwardScanDirection` today, for the same reason `Path.DisabledNodes` is
// carried at a constant 0 (path.go): the executor has no backward index-scan
// mode to select, so the second `create_index_path` call PG makes with
// `BackwardScanDirection` (indxpath.c:1010-1050) cannot be emitted — but a path
// that does not SAY which direction it means is one that silently means forward,
// and adding the backward arm later would then be a change to every reader
// rather than to the producer. Both halves are ledgered.
//
// The invariant this file exists to hold: **the recorded direction and the
// recorded pathkeys must describe the same scan.** `buildIndexPathkeys` takes a
// `backward bool` and INVERTS both the sort direction and the null placement
// (pathkeys.c:770-774); a path whose pathkeys were built backward while its
// `IndexScanDir` said forward would hand `createPlan` a forward `*IndexScan`
// under an ordering that scan does not deliver — a wrong answer, not a slow
// plan. `indexPathOrdering` below is therefore the ONLY way either constructor
// obtains the pair, so the two cannot drift apart (hard-won rule #2: sibling
// paths change together).

import "github.com/goopg/goopg/internal/catalog"

// ScanDirection is PG's `ScanDirection` (access/sdir.h:24), values included.
// The -1/0/+1 encoding is not decorative: PG's `ScanDirectionCombine` multiplies
// two of them, and `NoMovementScanDirection` being the ZERO value is what lets
// goopg's `Path` carry the field on every path kind and have "not an index path"
// be the natural zero rather than a fourth sentinel.
type ScanDirection int8

const (
	// BackwardScanDirection — a reverse scan of an ordered index. Never
	// produced today: goopg's executor `IndexScan` has no direction to set.
	// Ledgered against P5.4c-ii-a/b.
	BackwardScanDirection ScanDirection = -1
	// NoMovementScanDirection is the zero value, and on a `Path` it means "this
	// path is not an index scan" — the state every non-`PathIndexScan` kind is
	// in. PG never invokes a scan with it (sdir.h:19-20) and neither does goopg.
	NoMovementScanDirection ScanDirection = 0
	// ForwardScanDirection — the only direction goopg emits.
	ForwardScanDirection ScanDirection = 1
)

// String renders the direction the way PG's own comments name it, so a failing
// path-level assertion reads as `forward` rather than as `1`.
func (d ScanDirection) String() string {
	switch d {
	case BackwardScanDirection:
		return "backward"
	case ForwardScanDirection:
		return "forward"
	case NoMovementScanDirection:
		return "nomovement"
	default:
		return "invalid"
	}
}

// indexPathOrdering returns the pathkeys an index path delivers together with
// the scan direction those pathkeys were computed for. It is the single source
// of both halves of `create_index_path`'s ordering record.
//
// It is a two-line function on purpose. The point is not the computation — it is
// that no caller can obtain one half without the other, which is what keeps
// `IndexScanDir` from disagreeing with `Pathkeys`. A caller that wants a
// backward path passes `backward = true` and gets BOTH the inverted key list
// (`build_index_pathkeys`' :770-774 inversion) and `BackwardScanDirection`.
//
// An unorderable index yields no keys and still yields `ForwardScanDirection`,
// matching pathnodes.h:1834 — "unordered indexes will always have an
// indexscandir of ForwardScanDirection". Such an index can still be probed; it
// simply promises nothing about the order the rows come back in.
func indexPathOrdering(idx *catalog.Index, colExprs map[string]Expr, backward bool) ([]PathKey, ScanDirection) {
	if backward {
		return buildIndexPathkeys(idx, colExprs, true), BackwardScanDirection
	}
	return buildIndexPathkeys(idx, colExprs, false), ForwardScanDirection
}
