package executor

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/hashsize"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// joinRowCTID carries the heap tuple identifier captured for one left-side row
// during the eager nested-loop drain. Used by lockRowsOp when the scan has
// been closed before drainAndStamp runs (M0100-0010).
type joinRowCTID struct {
	rel     storage.RelFileNode
	ptr     storage.ItemPointer
	hasCTID bool
}

// joinOp is a join operator that dispatches on plan.Algo.
// Hash joins use lazy materialization (M0036): joined rows are
// yielded on demand via Next() instead of pre-computed in o.rows.
// Merge and nested-loop still materialize in o.rows.
type joinOp struct {
	plan   *planner.Join
	left   Operator
	right  Operator
	schema planner.Schema

	ctx  *Context
	rows []Row
	idx  int

	// M0036 lazy-output state (hash join only)
	lazyHash     map[string][]Row // build-side hash table
	// int64 fast-path (cost-model ch.14): when the build-side keys are
	// int64-representable, lazyIntHash replaces lazyHash so the probe hot
	// path (e.g. Q9's ~6M lineitem rows) hashes an int64 instead of
	// allocating a datumKey string per row — the GC-heavy cost that made
	// the binary hash cascade slow where MultiHashJoin's int64 keys are
	// fast (multi_hash_join.go M0043-0003).
	//
	// M0127-P0.3 (05 §4, stage E3): exactly ONE of the two maps is built.
	// lazyHashIsInt is decided BEFORE the build from the plan's key types
	// (planner.Join.HashKeysAreInt64) instead of being discovered by
	// populating both maps and dropping the loser at the end — which used
	// to double peak build memory for every int-keyed join. Semi/Anti
	// builds now opt in too; the CTID-preserving build is the one exception
	// and stays on the string map, because lazyHashCTID is keyed alongside
	// it. demoteIntHash covers the residual case where an integer-typed
	// column yields a datum that is not int64-representable.
	lazyIntHash   map[int64][]Row
	lazyHashIsInt bool

	// M0127-P2.2 (05 §5, stage E4): the executor's key plan, resolved once
	// per Open from planner.Join.ExecHashKeyPlan. execKeys holds EVERY
	// equi-pair the hash table is keyed on — goopg carried one since M0003 —
	// and execResidual is what is left of Predicate once those pairs are
	// enforced by the hash itself, which on an all-equijoin join is nothing
	// at all. buildKeyExprs/probeKeyExprs are the same list already split by
	// side, so the two loops cannot disagree about orientation.
	//
	// execKeyPackInt selects the fixed-width int64 lane of the composite
	// encoding (join_composite_key.go); execKeyBuf is its per-row scratch,
	// reused so the probe side allocates nothing.
	execKeys       []planner.JoinKeyPair
	execResidual   planner.Expr
	buildKeyExprs  []planner.Expr
	probeKeyExprs  []planner.Expr
	execKeyPackInt bool
	execKeyBuf     []byte

	// M0127-P2.3 (07 §2): the same split for the MERGE algorithm, taken
	// from planner.Join.ExecMergeKeyPlan by initMergeKeys. Deliberately
	// NOT the execKeys/execResidual slots above: those are filled on the
	// lazy-hash path only, and a joinOp runs one algorithm, so keeping the
	// two apart makes a cross-read a compile error rather than a silent
	// wrong-key join. See join_merge_key.go.
	mergeKeys     []planner.JoinKeyPair
	mergeResidual planner.Expr
	lazyProbe    Operator         // probe side (streaming)
	lazyMatches  []Row            // matches for current probe row (borrowed from lazyHash)
	lazyMatchIdx int
	lazyActive   bool // true between probeRow and last match
	// lazyProbeMatched tracks whether the current probe row has already
	// emitted at least one hash-match that also passed the residual
	// Predicate (see nextLazy). A hash-key match alone isn't sufficient
	// for LEFT JOIN null-padding purposes: every entry in lazyMatches can
	// fail the residual filter, which must still be treated as "no match"
	// for the outer row.
	lazyProbeMatched bool
	lazyLW       int  // left schema width
	lazyRW       int  // right schema width

	// M0054-0005b: per-Open() reusable buffers for nullRow / hash
	// key evaluation. The pre-fix path called `nullRow(width)` and
	// `concatRows(...)` on every probe row, allocating fresh slices.
	// These buffers stay constant for a given (leftWidth, rightWidth)
	// pair, so a single allocation per side is enough.
	lazyNullLeft  Row
	lazyNullRight Row

	// M0071-0014 Stage D-2: VirtualSlot composition replaces the
	// nextLazy concatRows allocations. lazyBuildSlot's .row holds
	// the matched build row (for INNER / LEFT-no-match-fallback);
	// lazyProbeSlot's .row holds the current probe row;
	// lazyVirtualOut composes them in plan.Output() order
	// (BuildLeft swaps the source order). lazyOuterOnlySlot is the
	// emit slot for Semi / Anti, which return the probe row alone.
	// Allocated lazily on first nextLazy invocation (Open path is
	// shared with non-hash joinOp algorithms which don't need the
	// virtual composition).
	//
	// M0127-P1.1 (leftdeep-joins/05 §2, stage E1) demoted both
	// MaterializedSlots to the COPY FALLBACK: in steady state the probe
	// child's own slot is bound directly as the probe source, and these
	// two carry a row only when chaining is off or the child's slot
	// cannot serve the composed shape.
	lazyBuildSlot     *MaterializedSlot
	lazyProbeSlot     *MaterializedSlot
	lazyVirtualOut    *VirtualSlot
	lazyOuterOnlySlot *MaterializedSlot

	// M0127-P1.1 (design leftdeep-joins/05 §2, stage E1; the un-deferred
	// 0126-0004): probe-side slot chaining. nextLazy used to flatten the
	// probe child's slot with `r := slotRow(probeSlot)` — for a
	// VirtualSlot child that is a pooled acquireRow plus a width-wide
	// 48-byte-Datum copy per probe row, and the pooled row was never
	// released. Every aggregate-topped query runs its whole join subtree
	// on this legacy path, so it, not the slab, governs the analytic
	// workload. The child's slot is now bound straight into
	// lazyVirtualOut.sources (and lazyOuterOnlyOut for Semi/Anti), so an
	// emitted tuple reads through to the child's storage with no copy.
	//
	// The F7 contract (0126-0004 §2): a child does NOT return a stable
	// slot object — the same child may hand back its own lazyVirtualOut,
	// its lazyOuterOnlySlot, a fresh Materialize() (FOR UPDATE), or a
	// fresh asSlot per call (rowsOp/spillOp, spill.go). bindProbe
	// therefore rebinds on EVERY pull instead of caching, and falls back
	// to one copy when the slot cannot serve the composed shape.
	//
	// Lifetime is safe by control flow, not by construction: nextLazy
	// pulls a new probe row only after every match of the current one has
	// been drained (lazyActive false). bindProbe asserts exactly that.
	lazyProbeSrc     TupleSlot    // probe slot bound for the current row (nil = none)
	lazyProbeSrcIdx  int          // its position inside lazyVirtualOut.sources
	lazyProbeWidth   int          // columns lazyVirtualOut reads from it
	lazyOuterOnlyOut *VirtualSlot // Semi/Anti emit slot, chained to the probe
	lazyChainProbe   bool         // seam enabled (GOOPG_JOIN_SLOT_CHAIN=off disables)

	// M0127-P0.1 (design leftdeep-joins/05 §3, stage E2): hoisted
	// merged-key slots. mergedKeySlot allocated five objects per BUILD
	// row and per PROBE row (the null Row, its MaterializedSlot, the
	// []virtualCol, the sources slice, the VirtualSlot). That composed
	// shape is invariant for a given (realWidth, nullWidth, realOnLeft)
	// triple, and the child schemas fix those at Open — so each side
	// builds its slot once and rebinds the real source per pull.
	lazyBuildKeySlot mergedKeySlotCache
	lazyProbeKeySlot mergedKeySlotCache

	// M0122-0011: NullAware (NOT IN) anti-join build-side state,
	// computed once in openLazyHashJoin's build loop. Real NOT IN
	// three-valued-NULL semantics only depend on whether the
	// subquery produced ANY row and whether ANY of its keys was
	// NULL — not on the row values themselves — so a single pair of
	// flags (checked before the per-probe-row hash lookup in
	// nextLazy) is enough; see the Join.NullAware doc comment.
	antiBuildRows     int  // total right-side rows seen during build
	antiBuildHasNull  bool // any right-side row's join key was NULL

	// M0100-0010: ctid captured per left-row during eager NL drain so
	// lockRowsOp can stamp tuple locks after the scan is closed.
	leftCTIDs     []joinRowCTID
	rowSourceLeft []int

	// M0118-0009 (eval-plan-qual): when a downstream LockRows (FOR UPDATE OF
	// <rel>) needs to lock a relation that ends up on the BUILD side of a lazy
	// hash join, the build scan is drained + closed at Open so its currentTID
	// is gone by the time lockRowsOp.drainAndStamp runs. preserveCTIDRel is set
	// by lockRowsOp.Open (before child Open) to that relation; when the build
	// side contains it, the build loop captures each build row's heap ctid into
	// lazyHashCTID (parallel to lazyHash) and nextLazy stamps it onto the
	// emitted slot so lockRowsOp's ms.hasCTID fallback recovers the TID. nil /
	// off for every normal query, so the hot hash-join path is unaffected.
	preserveCTIDRel   *storage.RelFileNode
	preserveBuildSide bool
	lazyHashCTID      map[string][]joinRowCTID
	lazyMatchCTIDs    []joinRowCTID
}

func newJoinOp(plan *planner.Join, left, right Operator) *joinOp {
	schema := plan.Output()
	if len(schema) == 0 {
		schema = append(schema, left.Schema()...)
		schema = append(schema, right.Schema()...)
	}
	// M0127-P2.2: the residual defaults to the full Predicate, which is what
	// every non-hash algorithm evaluates. openLazyHashJoin narrows it to the
	// conjuncts the hash key does NOT already enforce.
	var residual planner.Expr
	if plan != nil {
		residual = plan.Predicate
	}
	return &joinOp{plan: plan, left: left, right: right, schema: schema, execResidual: residual}
}

func (o *joinOp) Open(ctx *Context) error {
	o.ctx = ctx
	// M0061-0001: keyed Semi / Anti joins run through the lazy hash
	// path. S4a (D3.2, matrix M14) adds a KEYLESS form: a sublink
	// whose correlation is entirely non-equi residuals decorrelates
	// to Algo=JoinAlgoNestedLoop with the residuals as Predicate —
	// runNestedLoop implements its emit-once semantics (semi: left
	// row on first qualifying inner row; anti: left row iff none).
	// Any other algo (Merge, or an unset zero value that is NOT the
	// planner's explicit NestedLoop choice with a predicate) stays
	// an internal error.
	if o.plan.Type == planner.JoinTypeSemi || o.plan.Type == planner.JoinTypeAnti {
		switch o.plan.Algo {
		case planner.JoinAlgoHash:
			return o.openLazyHashJoin(ctx)
		case planner.JoinAlgoNestedLoop:
			if o.plan.Predicate == nil {
				// A keyless semi/anti with no predicate would be an
				// unconditional cross semi — the planner never builds
				// it (unnestExistsExpr's belt); refuse loudly.
				return fmt.Errorf("internal error: nested-loop semi/anti join without a predicate")
			}
			if err := o.left.Open(ctx); err != nil {
				return err
			}
			if err := o.right.Open(ctx); err != nil {
				_ = o.left.Close()
				return err
			}
			leftRows, err := drainRowsCtx(o.left, ctx)
			if err != nil {
				return err
			}
			rightRows, err := drainRowsCtx(o.right, ctx)
			if err != nil {
				return err
			}
			leftWidth := len(o.left.Schema())
			if leftWidth == 0 && len(leftRows) > 0 {
				leftWidth = len(leftRows[0])
			}
			rightWidth := len(o.right.Schema())
			if rightWidth == 0 && len(rightRows) > 0 {
				rightWidth = len(rightRows[0])
			}
			return o.runNestedLoop(leftRows, rightRows, leftWidth, rightWidth)
		default:
			return fmt.Errorf("internal error: semi/anti join requires hash or nested-loop algorithm, got %d", o.plan.Algo)
		}
	}
	// Lateral joins must always use the per-row driver path so the right-side
	// plan can evaluate OuterColumnRef nodes against the current left row.
	// Check Lateral BEFORE Algo: even when the planner chose hash-join for the
	// equi-predicate, the equality predicate just means JOIN ON col=col, not
	// that the right side is independent of the outer row. M0097-0106.
	if o.plan.Lateral {
		return o.openLateral(ctx)
	}
	if o.plan.Algo == planner.JoinAlgoHash {
		return o.openLazyHashJoin(ctx)
	}
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	if err := o.right.Open(ctx); err != nil {
		_ = o.left.Close()
		return err
	}

	leftScanLeaf := findScanLeaf(o.left)
	leftRows, leftCTIDs, err := drainRowsCtxCTID(o.left, ctx, leftScanLeaf)
	if err != nil {
		return err
	}
	o.leftCTIDs = leftCTIDs
	rightRows, err := drainRowsCtx(o.right, ctx)
	if err != nil {
		return err
	}

	leftWidth := len(o.left.Schema())
	rightWidth := len(o.right.Schema())
	if leftWidth == 0 && len(leftRows) > 0 {
		leftWidth = len(leftRows[0])
	}
	if rightWidth == 0 && len(rightRows) > 0 {
		rightWidth = len(rightRows[0])
	}

	if o.plan.Algo == planner.JoinAlgoMerge {
		return o.runMergeJoin(leftRows, rightRows, leftWidth, rightWidth)
	}
	return o.runNestedLoop(leftRows, rightRows, leftWidth, rightWidth)
}

// lateralBindable is implemented by FROM-clause SRFs that can have
// their argument expressions resolved against an outer row supplied
// by a parent Join.Lateral. M0103-0008.
type lateralBindable interface {
	BindLateralOuter(slot SlotView)
}

// openLateral handles `Join.Lateral == true`: drain the left, then
// for each left row re-run the right side with the left row in scope.
// Concatenated rows accumulate in o.rows and are emitted via the
// existing Next() path.
//
// Two paths:
//   - If the right child implements lateralBindable (e.g. pg_get_publication_tables):
//     use BindLateralOuter to pass the outer row directly.
//   - Otherwise: push the left row onto ctx.OuterRows so OuterColumnRef
//     expressions inside the right subtree can resolve correlated refs.
//     The CTERowCache is saved/cleared per iteration so LATERAL CTEs that
//     depend on the outer row (e.g. WITH RECURSIVE inside LATERAL) are
//     re-evaluated for each outer row, not served from a stale cache.
//
// LEFT lateral joins emit a null-padded row when the right side yields
// zero rows; CROSS / INNER drop the outer row.
func (o *joinOp) openLateral(ctx *Context) error {
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	leftRows, err := drainRowsCtx(o.left, ctx)
	if err != nil {
		_ = o.left.Close()
		return err
	}
	leftWidth := len(o.left.Schema())
	if leftWidth == 0 && len(leftRows) > 0 {
		leftWidth = len(leftRows[0])
	}
	rightWidth := len(o.right.Schema())
	nullRight := nullRow(rightWidth)

	bindable, isSRF := o.right.(lateralBindable)
	if isSRF {
		outerSlot := SlotFromRow(o.left.Schema(), nil)
		bindable.BindLateralOuter(outerSlot)
		defer bindable.BindLateralOuter(nil)
		for i, l := range leftRows {
			if i&0xFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
				if err := o.ctx.Ctx.Err(); err != nil {
					return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			outerSlot.row = l
			if err := o.right.Open(ctx); err != nil {
				return err
			}
			rightRows, err := drainRowsCtx(o.right, ctx)
			_ = o.right.Close()
			if err != nil {
				return err
			}
			matched := false
			for _, r := range rightRows {
				joined := concatRows(l, r)
				ok, perr := o.joinPredicateMatch(joined)
				if perr != nil {
					return perr
				}
				if !ok {
					continue
				}
				matched = true
				o.rows = append(o.rows, joined)
			}
			// LEFT JOIN: null-extend when the right side produced no rows or
			// no right row satisfied the join predicate.
			if (len(rightRows) == 0 || !matched) && o.plan.Type == planner.JoinTypeLeft {
				o.rows = append(o.rows, concatRows(l, nullRight))
			}
		}
		return nil
	}

	// General LATERAL: push each left row as an outer row context so
	// OuterColumnRef (level=1) expressions in the right subtree resolve
	// against it. Also clear CTERowCache per iteration so LATERAL CTEs
	// whose content depends on the outer row are re-materialised.
	savedCTECache := ctx.CTERowCache
	for i, l := range leftRows {
		if i&0xFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		ctx.OuterRows = append(ctx.OuterRows, l)
		ctx.CTERowCache = nil // clear per-iteration so outer-dependent CTEs recompute
		var rightRows []Row
		if openErr := o.right.Open(ctx); openErr == nil {
			rightRows, err = drainRowsCtx(o.right, ctx)
			_ = o.right.Close()
		} else {
			err = openErr
		}
		ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1]
		if err != nil {
			ctx.CTERowCache = savedCTECache
			return err
		}
		matched := false
		for _, r := range rightRows {
			joined := concatRows(l, r)
			ok, perr := o.joinPredicateMatch(joined)
			if perr != nil {
				ctx.CTERowCache = savedCTECache
				return perr
			}
			if !ok {
				continue
			}
			matched = true
			o.rows = append(o.rows, joined)
		}
		// LEFT JOIN: null-extend when the right side produced no rows or
		// no right row satisfied the join predicate.
		if (len(rightRows) == 0 || !matched) && o.plan.Type == planner.JoinTypeLeft {
			o.rows = append(o.rows, concatRows(l, nullRight))
		}
	}
	return nil
}

// runNestedLoop is the universal fallback. O(N*M) over the two
// drained sides; supports INNER / LEFT / RIGHT / FULL / CROSS.
//
// Cancellation: ctx.Err() is checked once per outer-row loop so a
// CancelRequest interrupts a long join even when the inner side has
// no per-row hooks. (M0058-0005.) Q5 and Q13 ran 60+ minutes without
// responding to cancellation before this check existed.
func (o *joinOp) runNestedLoop(leftRows, rightRows []Row, leftWidth, rightWidth int) error {
	nullLeft := nullRow(leftWidth)
	nullRight := nullRow(rightWidth)

	rightMatched := make([]bool, len(rightRows))
	for i, l := range leftRows {
		if i&0xFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		matched := false
		semiAnti := o.plan.Type == planner.JoinTypeSemi || o.plan.Type == planner.JoinTypeAnti
		for j, r := range rightRows {
			// M0062-followup: also check ctx.Err() inside the inner
			// loop, every 4096 iterations. Q13 (customer LEFT JOIN
			// orders, 150K × 1.5M with a NOT LIKE residual) ran 300 s
			// past --cancel-after=600s in the M0061-0003 sweep
			// because the *outer* check (every 256 outer rows) only
			// fired between full passes of the 1.5M-row inner.
			if j&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
				if err := o.ctx.Ctx.Err(); err != nil {
					return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			joined := concatRows(l, r)
			ok, err := o.joinPredicateMatch(joined)
			if err != nil {
				return err
			}
			if !ok {
				// NULL predicate results count as no-match
				// (joinPredicateMatch), which is exactly the
				// semi/anti contract too.
				continue
			}
			matched = true
			if semiAnti {
				// S4a (D3.2): keyless semi/anti — one qualifying
				// inner row decides this outer row; never emit the
				// joined row (the join's schema is outer-only) and
				// never scan further inner rows.
				break
			}
			rightMatched[j] = true
			o.rows = append(o.rows, joined)
			if o.leftCTIDs != nil {
				o.rowSourceLeft = append(o.rowSourceLeft, i)
			}
		}
		if semiAnti {
			// Semi emits the left row exactly once iff a qualifying
			// inner row exists; Anti iff none does.
			hit := matched
			if o.plan.Type == planner.JoinTypeAnti {
				hit = !matched
			}
			if hit {
				o.rows = append(o.rows, append(Row(nil), l...))
				if o.leftCTIDs != nil {
					o.rowSourceLeft = append(o.rowSourceLeft, i)
				}
			}
			continue
		}
		if !matched && (o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull) {
			o.rows = append(o.rows, concatRows(l, nullRight))
			if o.leftCTIDs != nil {
				o.rowSourceLeft = append(o.rowSourceLeft, i)
			}
		}
	}

	if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
		for j, r := range rightRows {
			// M0062-followup: ctx check inside the unmatched-right
			// emission loop too. RIGHT/FULL join over a multi-million-
			// row right side could otherwise stall cancel here even
			// after the join body has finished.
			if j&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
				if err := o.ctx.Ctx.Err(); err != nil {
					return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			if rightMatched[j] {
				continue
			}
			// M0097-0060: FULL JOIN USING coalescing. For unmatched
			// right rows the left side is all NULL; copy each USING
			// column value from the right position to the left position
			// so `SELECT *` sees COALESCE(left.col, right.col) = right.col.
			merged := concatRows(nullLeft, r)
			for k, lIdx := range o.plan.UsingLeftCols {
				rIdx := o.plan.UsingRightCols[k]
				if rIdx < len(merged) {
					merged[lIdx] = merged[rIdx]
				}
			}
			o.rows = append(o.rows, merged)
			if o.leftCTIDs != nil {
				o.rowSourceLeft = append(o.rowSourceLeft, -1) // no left source
			}
		}
	}
	return nil
}

// coalesceUsingRow applies FULL JOIN USING coalescing: for each USING
// column pair, copy the right-side value into the left-side position
// so star-expansion sees the correct non-NULL value for unmatched right
// rows. Modifies merged in place. M0097-0060.
func (o *joinOp) coalesceUsingRow(merged Row) {
	for k, lIdx := range o.plan.UsingLeftCols {
		rIdx := o.plan.UsingRightCols[k]
		if lIdx < len(merged) && rIdx < len(merged) {
			merged[lIdx] = merged[rIdx]
		}
	}
}

// openLazyHashJoin builds a hash table from the build side and sets
// up lazy output. The build side is consumed in a single pass — the
// rows land in the hash table as they arrive (M0127-P0.2; see
// buildLazyHashTable).
// openLazyHashJoin builds the hash table (or adopts a shared one) and opens
// the probe side.
//
// P8 split this into two halves. Under parallelism the build must happen ONCE,
// in the leader, before any worker starts, while each worker opens only its own
// (partial) probe side — so "drain the build, then open the probe" could no
// longer be a single indivisible step.
func (o *joinOp) openLazyHashJoin(ctx *Context) error {
	// A Gather pre-builds the shared table in the leader and publishes it
	// before fan-out. It is frozen at that point and never written again,
	// which is the whole reason workers can read it with no lock.
	// M0127-P2.2: resolve the key plan before either branch. The shared-build
	// branch skips buildLazyHashTable entirely, and the probe half needs the
	// same key list the leader built with — deriving it from the plan (rather
	// than shipping it in sharedHashBuild) makes that agreement structural.
	o.initExecKeys()
	if sb := lookupSharedHashBuild(ctx, o.plan); sb != nil {
		o.applySharedBuild(sb)
		return o.openProbeSide(ctx, sb.probeIsLeft)
	}
	probeIsLeft, err := o.buildLazyHashTable(ctx)
	if err != nil {
		return err
	}
	return o.openProbeSide(ctx, probeIsLeft)
}

// openProbeSide opens whichever side the build did not consume.
func (o *joinOp) openProbeSide(ctx *Context, probeIsLeft bool) error {
	probe := o.right
	if probeIsLeft {
		probe = o.left
	}
	if err := probe.Open(ctx); err != nil {
		return err
	}
	o.lazyProbe = probe
	return nil
}

// buildLazyHashTable drains the build side into o.lazyHash and reports which
// side is left for probing. It does NOT open the probe side.
//
// M0127-P0.2 (design leftdeep-joins/05 §4, stage E3): the build is a SINGLE
// pass. It used to be drain-to-`[]Row` (drainRowsBounded) and then re-iterate
// the drained operator, which cost a `MaterializedSlot` allocation per row on
// the way back out (spill.go rowsOp.Next) plus a second traversal of the whole
// build side. Rows are now keyed and inserted as they arrive from the child.
//
// The drain's `ctx.WorkMem` budget went with it, and deliberately so: that
// budget bounded only the intermediate `[]Row`, never the hash table it was
// feeding — every spilled row was read straight back in and inserted, so peak
// memory was the finished table either way. Real work_mem enforcement for hash
// join is the batched hybrid-hash spill (06 / P3.2), which partitions rows at
// insert time and therefore *requires* this single-pass shape. Until it lands,
// this path is unbounded (deferral ledger 2026-08-03 M0127-P0.2).
func (o *joinOp) buildLazyHashTable(ctx *Context) (bool, error) {
	// M0127-P2.2: idempotent, and needed here as well as in openLazyHashJoin
	// because prebuildSharedHashJoins (parallel_hash_build.go) runs the build
	// phase alone, without ever opening the operator.
	o.initExecKeys()
	leftWidth := len(o.left.Schema())
	rightWidth := len(o.right.Schema())
	o.lazyLW = leftWidth
	o.lazyRW = rightWidth
	o.antiBuildRows = 0
	o.antiBuildHasNull = false
	// M0054-0005b: hoist the per-iteration `nullRow(...)` allocation
	// out of the build loop. The hash-key evaluation only needs the
	// other-side columns to be present so column-index resolution
	// works; the values are not read. We also reuse a single
	// `keyRow` buffer per side to avoid `concatRows`'s per-row
	// `make(Row, leftW+rightW)` churn (M0054-0004 cumulative
	// `concatRows` 56 GB on Q9, 7,980 GB on Q20).
	// M0061-0001: Semi / Anti join semantics require the OUTER
	// (left) side to drive the probe loop and the INNER (right)
	// side to be hashed. BuildLeft is INNER-only by contract; we
	// also defend here so a stray flag doesn't silently break the
	// emit-once-per-probe-row invariant.
	buildLeft := o.plan.BuildLeft
	if o.plan.Type == planner.JoinTypeSemi || o.plan.Type == planner.JoinTypeAnti {
		buildLeft = false
	}
	// M0127-P0.3 (05 §4, stage E3): pick the key representation ONCE, here,
	// from the plan's key types. Everything downstream of this line inserts
	// into exactly one map.
	//
	// M0127-P2.2: the int64 MAP lane is the single-key representation. A
	// multi-column key is packed into the string map instead (its own
	// fixed-width int64 lane lives in join_composite_key.go), so a
	// map[int64] table can never be built for a join that probes with a
	// composite key.
	o.lazyHashIsInt = !o.multiKey() && o.plan.HashKeysAreInt64()
	if buildLeft {
		if err := o.left.Open(ctx); err != nil {
			return false, err
		}
		o.presizeLazyHash(ctx, o.plan.Left, leftWidth)
		err := o.buildLoopLeft(ctx, rightWidth)
		_ = o.left.Close()
		if err != nil {
			return false, err
		}
		return false, nil
	}
	if err := o.right.Open(ctx); err != nil {
		return false, err
	}
	// M0118-0009 (eval-plan-qual): preserve build-side heap ctids when a
	// downstream FOR UPDATE locks a relation on this (right) build side.
	if o.preserveCTIDRel != nil {
		if sl := findScanLeafForRel(o.right, *o.preserveCTIDRel); sl != nil {
			// The CTID exception: lazyHashCTID is a map[string] keyed in
			// lockstep with lazyHash, so this build stays on the string map
			// whatever the key types say (M0127-P0.3).
			o.lazyHashIsInt = false
			if err := o.buildHashRightWithCTID(ctx, sl, leftWidth, rightWidth); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	o.presizeLazyHash(ctx, o.plan.Right, rightWidth)
	err := o.buildLoopRight(ctx, leftWidth)
	_ = o.right.Close()
	if err != nil {
		return false, err
	}
	return true, nil
}

// maxPresizeBuckets caps how many buckets presizeLazyHash will pre-allocate.
//
// The cap exists because the bucket count comes from planner.EstimateRows,
// and an estimate is not a measurement: a 100× over-estimate on a join that
// really holds a thousand rows would otherwise reserve a map big enough for
// the imagined build and hold it for the query's life. Go maps grow by
// doubling with incremental rehash, so past roughly a million buckets the
// presize buys progressively less anyway — the allocation it saves is the one
// the table would have grown into regardless. 1<<20 slots is about 48 MB of
// map metadata worst case, which is a bounded loss if the estimate is wrong.
//
// The cap is presize policy, deliberately NOT part of hashsize.Choose: the
// sizing function must keep returning PG's geometry so the planner and the
// executor agree on it. It goes away with M0127-P3.2, where nbatch makes the
// in-memory table genuinely bounded and the full bucket count becomes safe to
// allocate.
const maxPresizeBuckets = 1 << 20

// presizeLazyHash allocates the build-side table with its bucket count already
// chosen, instead of letting an empty map grow by rehashing.
//
// M0127-P3.1 (design leftdeep-joins/06 §2.1): PG picks (nbuckets, nbatch) once
// via ExecChooseHashTableSize and ExecHashTableCreate allocates the bucket
// array up front (nodeHash.c:446). goopg built every hash table from a nil map
// and let it double its way up — for a multi-million-row build that is a
// couple of dozen rehashes of the whole table. hashsize.Choose is the shared
// planner↔executor sizing rule; this is its first caller, and it uses only the
// NBuckets half. NBatch is computed and currently ignored: honouring it means
// partitioning rows at insert time, which is P3.2.
//
// buildNode is the plan node feeding the build side — the row estimate has to
// come from the plan because the executor cannot know the count before it has
// drained the side it is about to build.
//
// Not called for the FOR-UPDATE ctid build (buildHashRightWithCTID): that path
// materialises its rows first and its result sets are small by construction.
func (o *joinOp) presizeLazyHash(ctx *Context, buildNode planner.Node, buildWidth int) {
	if buildNode == nil || o.lazyHash != nil || o.lazyIntHash != nil {
		return
	}
	var workMem int64
	if ctx != nil {
		workMem = ctx.WorkMem
	}
	// avgVarBytes is 0: goopg has no per-column average-width statistic to
	// feed it, so a text-heavy build is under-counted and its geometry is
	// sized as if only the 48-byte Datum array were resident. That biases
	// NBatch low, which is the risk 04 §4 names; it is harmless while only
	// NBuckets is consumed. Deferral ledger 2026-08-03 M0127-P3.1.
	sizing := hashsize.Choose(float64(planner.EstimateRows(buildNode)), buildWidth, 0,
		hashsize.EffectiveMemLimit(workMem))
	n := sizing.NBuckets
	if n > maxPresizeBuckets {
		n = maxPresizeBuckets
	}
	if n <= hashsize.MinBuckets {
		// hashsize floors nbuckets at 1024 even for a three-row build, and
		// that floor is also what an unestimated relation returns. Both mean
		// "no useful information", so allocate nothing and let the map grow.
		return
	}
	if o.lazyHashIsInt {
		o.lazyIntHash = make(map[int64][]Row, n)
		return
	}
	o.lazyHash = make(map[string][]Row, n)
}

// buildLoopLeft is the single-pass build loop for the BuildLeft orientation
// (M0127-P0.2; 05 §4 stage E3). o.left is already Open and is closed by the
// caller. rightWidth is the null-side width of the merged key column space.
func (o *joinOp) buildLoopLeft(ctx *Context, rightWidth int) error {
	o.ensureExecKeys()
	leftWidth := o.lazyLW
	for buildCount := 0; ; buildCount++ {
		// M0062-followup: ctx check inside the build loop. With
		// 6M-row build inputs (Q21's anti-join lineitem) the
		// build alone runs minutes; without this check the
		// cancel-after deadline can be exceeded by 100+ s
		// while build keeps draining.
		if buildCount&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		lSlot, err := o.left.Next()
		if err == EOF {
			return nil
		}
		if err != nil {
			return err
		}
		l := slotRow(lSlot)
		if leftWidth == 0 && len(l) > 0 {
			leftWidth = len(l)
			o.lazyLW = leftWidth
		}
		// M0126-0003 0b: evaluate the key against a VirtualSlot
		// over {lSlot, nullRight} instead of copying into a
		// merged keyRow.
		keySlot := o.lazyBuildKeySlot.rebind(lSlot, leftWidth, rightWidth, true)
		// M0127-P2.2: a multi-column key is encoded and filed as one
		// composite; the single-key lanes are untouched.
		if o.multiKey() {
			ok, err := o.encodeBuildCompositeKey(keySlot)
			if err != nil {
				return err
			}
			if ok {
				o.fileCompositeBuildRow(ownedBuildRow(l))
			}
			continue
		}
		kd, ok, err := o.evalHashKeyDatumSlot(o.buildKeyExprs[0], keySlot)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		o.lazyHashInsertDatum(kd, ownedBuildRow(l))
	}
}

// buildLoopRight is the single-pass build loop for the (default) BuildRight
// orientation, including the Semi/Anti string-map lane and the NullAware
// bookkeeping. o.right is already Open and is closed by the caller.
func (o *joinOp) buildLoopRight(ctx *Context, leftWidth int) error {
	o.ensureExecKeys()
	rightWidth := o.lazyRW
	for buildCount := 0; ; buildCount++ {
		// M0062-followup: same ctx check on the build-right path.
		if buildCount&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		rSlot, err := o.right.Next()
		if err == EOF {
			return nil
		}
		if err != nil {
			return err
		}
		r := slotRow(rSlot)
		if rightWidth == 0 && len(r) > 0 {
			rightWidth = len(r)
			o.lazyRW = rightWidth
		}
		// M0126-0003 0b: evaluate the key against a VirtualSlot
		// over {nullLeft, rSlot} instead of copying into a
		// merged keyRow.
		keySlot := o.lazyBuildKeySlot.rebind(rSlot, rightWidth, leftWidth, false)
		// M0127-P2.2: composite lane. NullAware joins are forced single-key
		// by ExecHashKeyPlan (their antiBuildHasNull rule is defined over one
		// key column), so this branch never has to maintain those counters.
		if o.multiKey() {
			ok, err := o.encodeBuildCompositeKey(keySlot)
			if err != nil {
				return err
			}
			if ok {
				o.fileCompositeBuildRow(ownedBuildRow(r))
			}
			continue
		}
		kd, ok, err := o.evalHashKeyDatumSlot(o.buildKeyExprs[0], keySlot)
		if err != nil {
			return err
		}
		if o.plan.NullAware {
			o.antiBuildRows++
		}
		if !ok {
			if o.plan.NullAware {
				o.antiBuildHasNull = true
			}
			continue
		}
		// M0127-P0.3 (05 §4, stage E3): Semi/Anti go through the same
		// insert as INNER now. The old INNER-only gate existed because the
		// representation was DISCOVERED during the build and semi/anti
		// never ran the finalize step that committed it; with the choice
		// made up front there is nothing left for them to opt out of.
		// Their NullAware / emit-once invariants live in the counters above
		// and in nextLazy, neither of which reads the key representation.
		o.lazyHashInsertDatum(kd, ownedBuildRow(r))
	}
}

// ownedBuildRow copies a build-side row into storage the hash table can hold
// for the life of the join. This is the copy drainRowsBounded used to perform
// (spill.go, M0073-0004 retention boundary) before M0127-P0.2 folded the drain
// into the build loop: arena-backed Datums must be promoted to owned []byte
// because the producer's next Next may Reset the arena, while the non-arena
// case keeps the cheap O(width) struct copy. Dropping either half re-opens the
// M0097-0058 aliasing class — build rows must never alias a scan buffer.
func ownedBuildRow(row Row) Row {
	if rowHasArena(row) {
		return cloneRowOwned(row)
	}
	dup := make(Row, len(row))
	copy(dup, row)
	return dup
}

// buildHashRightWithCTID is the ctid-preserving variant of the !BuildLeft build
// loop: it drains the right (build) side capturing each row's heap ctid via
// scanLeaf, then builds lazyHash + the parallel lazyHashCTID map so nextLazy can
// stamp the build row's TID onto the emitted slot. Used only when a downstream
// FOR UPDATE locks a relation that landed on the build side of this hash join
// (M0118-0009, eval-plan-qual selectresultforupdate). o.right is already Open;
// drainRowsCtxCTID consumes it to EOF without an extra Close. The materialising
// drain (no spill) is acceptable because FOR UPDATE result sets are small.
func (o *joinOp) buildHashRightWithCTID(ctx *Context, scanLeaf currentTIDProvider, leftWidth, rightWidth int) error {
	o.ensureExecKeys()
	rows, ctids, err := drainRowsCtxCTID(o.right, ctx, scanLeaf)
	if err != nil {
		return err
	}
	o.preserveBuildSide = true
	o.lazyHashCTID = make(map[string][]joinRowCTID)
	for i, r := range rows {
		if rightWidth == 0 && len(r) > 0 {
			rightWidth = len(r)
			o.lazyRW = rightWidth
		}
		// M0126-0003 0b: slot-based key evaluation — skip the
		// merged keyRow copy.
		rSlot := SlotFromRow(nil, r)
		keySlot := o.lazyBuildKeySlot.rebind(rSlot, rightWidth, leftWidth, false)
		// M0127-P2.2: this path keys lazyHashCTID in lockstep with lazyHash,
		// so it must use the SAME encoding the probe will — composite when
		// the join has more than one key pair.
		var key string
		var ok bool
		if o.multiKey() {
			var kerr error
			ok, kerr = o.encodeBuildCompositeKey(keySlot)
			if kerr != nil {
				return kerr
			}
			key = string(o.execKeyBuf)
		} else {
			var kerr error
			key, ok, kerr = o.evalHashKeySlot(o.buildKeyExprs[0], keySlot)
			if kerr != nil {
				return kerr
			}
		}
		if !ok {
			continue
		}
		if o.lazyHash == nil {
			o.lazyHash = make(map[string][]Row)
		}
		o.lazyHash[key] = append(o.lazyHash[key], r)
		o.lazyHashCTID[key] = append(o.lazyHashCTID[key], ctids[i])
	}
	// P8: the probe side is opened by the caller, not here — the build and
	// probe halves of Open are separable now.
	return nil
}

// mergeKeyedRow is one side's row plus its evaluated merge key TUPLE.
//
// M0127-P2.3 widened `key Datum` to a slice: the key is the whole
// `Join.HashKeys` list the planner accepted for merge, not just its head. The
// slice is a window into buildMergeSide's one flat backing array, so a
// materialised side still costs a single allocation for all its keys.
type mergeKeyedRow struct {
	row  Row
	keys []Datum
}

// runMergeJoin sorts both sides on their join keys and merges the
// two ordered streams. NULL keys never match (same as hash join).
// Supports INNER / LEFT / RIGHT / FULL outer semantics.
func (o *joinOp) runMergeJoin(leftRows, rightRows []Row, leftWidth, rightWidth int) error {
	nullLeft := nullRow(leftWidth)
	nullRight := nullRow(rightWidth)

	// M0127-P2.3: resolve the full key list + residual before either side is
	// keyed — both sides must produce the same key arity in the same order.
	o.initMergeKeys()

	leftKeyed, leftNull, err := o.buildMergeSide(leftRows, true, leftWidth, rightWidth)
	if err != nil {
		return err
	}
	rightKeyed, rightNull, err := o.buildMergeSide(rightRows, false, leftWidth, rightWidth)
	if err != nil {
		return err
	}

	i, j := 0, 0
	for i < len(leftKeyed) && j < len(rightKeyed) {
		// M0058-0005: cheap ctx check every 256 left-side rows so a
		// CancelRequest interrupts a long sort-merge join promptly.
		if i&0xFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		cmp, err := compareMergeKeys(leftKeyed[i].keys, rightKeyed[j].keys, o.plan.Pos())
		if err != nil {
			return err
		}
		switch {
		case cmp < 0:
			if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
				o.rows = append(o.rows, concatRows(leftKeyed[i].row, nullRight))
			}
			i++
		case cmp > 0:
			if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
				merged := concatRows(nullLeft, rightKeyed[j].row)
				o.coalesceUsingRow(merged)
				o.rows = append(o.rows, merged)
			}
			j++
		default:
			li := i
			for i < len(leftKeyed) {
				eq, err := compareMergeKeys(leftKeyed[li].keys, leftKeyed[i].keys, o.plan.Pos())
				if err != nil {
					return err
				}
				if eq != 0 {
					break
				}
				i++
			}
			rj := j
			for j < len(rightKeyed) {
				eq, err := compareMergeKeys(rightKeyed[rj].keys, rightKeyed[j].keys, o.plan.Pos())
				if err != nil {
					return err
				}
				if eq != 0 {
					break
				}
				j++
			}
			// The residual is what remains of the ON clause once the
			// merge key has located the group — PostgreSQL's
			// EXEC_MJ_JOINTUPLES -> ExecQual(joinqual)
			// (postgres/src/backend/executor/nodeMergejoin.c).
			//
			// M0125-0011 first put this check here: the merge key was
			// only the FIRST equality conjunct, so without a per-pair
			// re-check a two-conjunct FULL OUTER JOIN degenerated into
			// its single-key counterpart (TPC-DS Q97: 2131274 rows
			// instead of 836302), and RIGHT OUTER JOIN was wrong the
			// same way. M0127-P2.3 removed the CAUSE rather than the
			// check: the key is now the whole accepted pair list, so on
			// an all-equijoin join mergeResidual is nil and this loop
			// does no evaluator work at all — the group it walks is
			// already the exact match set. A conjunct whose pair the
			// planner declined (non-merge-safe type) or a genuinely
			// non-equijoin conjunct still lands here, which is why the
			// check stays.
			leftMatched := make([]bool, i-li)
			rightMatched := make([]bool, j-rj)
			for a := li; a < i; a++ {
				for b := rj; b < j; b++ {
					joined := concatRows(leftKeyed[a].row, rightKeyed[b].row)
					ok, perr := o.mergeResidualMatch(joined)
					if perr != nil {
						return perr
					}
					if !ok {
						continue
					}
					leftMatched[a-li] = true
					rightMatched[b-rj] = true
					o.rows = append(o.rows, joined)
				}
			}
			// A row whose merge key matched but whose residual did not
			// is still an UNMATCHED row for outer-join purposes, so it
			// must be null-extended here; the cmp<0 / cmp>0 arms only
			// see rows whose key itself found no partner.
			if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
				for a := li; a < i; a++ {
					if !leftMatched[a-li] {
						o.rows = append(o.rows, concatRows(leftKeyed[a].row, nullRight))
					}
				}
			}
			if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
				for b := rj; b < j; b++ {
					if !rightMatched[b-rj] {
						merged := concatRows(nullLeft, rightKeyed[b].row)
						o.coalesceUsingRow(merged)
						o.rows = append(o.rows, merged)
					}
				}
			}
		}
	}

	for ; i < len(leftKeyed); i++ {
		if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
			o.rows = append(o.rows, concatRows(leftKeyed[i].row, nullRight))
		}
	}
	for ; j < len(rightKeyed); j++ {
		if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
			merged := concatRows(nullLeft, rightKeyed[j].row)
			o.coalesceUsingRow(merged)
			o.rows = append(o.rows, merged)
		}
	}

	if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
		for _, l := range leftNull {
			o.rows = append(o.rows, concatRows(l, nullRight))
		}
	}
	if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
		for _, r := range rightNull {
			merged := concatRows(nullLeft, r)
			o.coalesceUsingRow(merged)
			o.rows = append(o.rows, merged)
		}
	}

	return nil
}

func (o *joinOp) buildMergeSide(rows []Row, isLeft bool, leftWidth, rightWidth int) ([]mergeKeyedRow, []Row, error) {
	var paddedLeft, paddedRight Row
	if isLeft {
		paddedRight = nullRow(rightWidth)
	} else {
		paddedLeft = nullRow(leftWidth)
	}
	keyExprs := o.mergeSideKeyExprs(isLeft)
	if len(keyExprs) == 0 {
		return nil, nil, fmt.Errorf("merge join key is nil")
	}
	for _, e := range keyExprs {
		if e == nil {
			return nil, nil, fmt.Errorf("merge join key is nil")
		}
	}

	keyed := make([]mergeKeyedRow, 0, len(rows))
	nullKey := make([]Row, 0)
	// One flat backing array for every kept row's key tuple. Sized exactly,
	// and never appended to, so the sub-slices handed to mergeKeyedRow can
	// never be invalidated by a reallocation.
	n := len(keyExprs)
	store := make([]Datum, len(rows)*n)
	used := 0
	for _, row := range rows {
		var evalRow Row
		if isLeft {
			evalRow = concatRows(row, paddedRight)
		} else {
			evalRow = concatRows(paddedLeft, row)
		}
		base := used
		haveKey := true
		for _, keyExpr := range keyExprs {
			v, err := evalExpr(keyExpr, evalRow, o.ctx)
			if err != nil {
				return nil, nil, err
			}
			if v.IsNull() {
				// A NULL in ANY key column makes that column's equality
				// NULL, so the row can match nothing — the single-key
				// rule applied componentwise. Before P2.3 such a row was
				// grouped on the non-NULL leading column and then
				// rejected by the residual; both routes reach the same
				// emit decision (dropped for INNER, null-extended for the
				// outer side), which is what keeps this a cost change.
				haveKey = false
				break
			}
			store[used] = v
			used++
		}
		if !haveKey {
			used = base
			nullKey = append(nullKey, row)
			continue
		}
		keyed = append(keyed, mergeKeyedRow{row: row, keys: store[base:used:used]})
	}

	var sortErr error
	sort.SliceStable(keyed, func(i, j int) bool {
		cmp, err := compareMergeKeys(keyed[i].keys, keyed[j].keys, o.plan.Pos())
		if err != nil {
			sortErr = err
			return false
		}
		return cmp < 0
	})
	if sortErr != nil {
		return nil, nil, sortErr
	}

	return keyed, nullKey, nil
}

// evalHashKey evaluates one side of the hash-join key against a
// padded row and returns its canonical key string. The boolean
// is false when the key evaluated to NULL (never matches).
func (o *joinOp) evalHashKey(keyExpr planner.Expr, row Row) (string, bool, error) {
	v, err := evalExpr(keyExpr, row, o.ctx)
	if err != nil {
		return "", false, err
	}
	if v.IsNull() {
		return "", false, nil
	}
	return datumKey(v), true, nil
}

// evalHashKeyDatum is evalHashKey but returns the key Datum instead of its
// string form, so the int64 fast-path can try datumToInt64Key before
// falling back to datumKey. ok is false for a NULL key.
func (o *joinOp) evalHashKeyDatum(keyExpr planner.Expr, row Row) (Datum, bool, error) {
	v, err := evalExpr(keyExpr, row, o.ctx)
	if err != nil {
		return Datum{}, false, err
	}
	if v.IsNull() {
		return Datum{}, false, nil
	}
	return v, true, nil
}

// evalHashKeyDatumSlot is the SlotView variant of evalHashKeyDatum.
// M0126-0003 Stage 0b: evaluates the key expression against a slot
// instead of a merged Row, so callers can pass a VirtualSlot over
// {realSide, nullOtherSide} and skip the per-row merged-Row copy.
func (o *joinOp) evalHashKeyDatumSlot(keyExpr planner.Expr, slot SlotView) (Datum, bool, error) {
	v, err := evalExprSlot(keyExpr, slot, o.ctx)
	if err != nil {
		return Datum{}, false, err
	}
	if v.IsNull() {
		return Datum{}, false, nil
	}
	return v, true, nil
}

// evalHashKeySlot is the SlotView variant of evalHashKey.
func (o *joinOp) evalHashKeySlot(keyExpr planner.Expr, slot SlotView) (string, bool, error) {
	v, err := evalExprSlot(keyExpr, slot, o.ctx)
	if err != nil {
		return "", false, err
	}
	if v.IsNull() {
		return "", false, nil
	}
	return datumKey(v), true, nil
}

// mergedKeySlot builds a VirtualSlot that presents the merged
// (left+right) column space for hash-key evaluation. realSlot
// contributes realWidth columns; the other nullWidth columns are NULL.
// realOnLeft=true means realSlot occupies indices [0, realWidth).
// The returned slot satisfies SlotView and is valid until the next
// probe/build pull (the realSlot source must outlive it).
func mergedKeySlot(realSlot TupleSlot, realWidth, nullWidth int, realOnLeft bool) *VirtualSlot {
	total := realWidth + nullWidth
	nullRow := make(Row, nullWidth)
	nullSlot := SlotFromRow(nil, nullRow)
	var sources []TupleSlot
	cols := make([]virtualCol, total)
	if realOnLeft {
		sources = []TupleSlot{realSlot, nullSlot}
		for i := 0; i < realWidth; i++ {
			cols[i] = virtualCol{sourceIdx: 0, sourceCol: int16(i)}
		}
		for i := 0; i < nullWidth; i++ {
			cols[realWidth+i] = virtualCol{sourceIdx: 1, sourceCol: int16(i)}
		}
	} else {
		sources = []TupleSlot{nullSlot, realSlot}
		for i := 0; i < nullWidth; i++ {
			cols[i] = virtualCol{sourceIdx: 0, sourceCol: int16(i)}
		}
		for i := 0; i < realWidth; i++ {
			cols[nullWidth+i] = virtualCol{sourceIdx: 1, sourceCol: int16(i)}
		}
	}
	return &VirtualSlot{sources: sources, cols: cols}
}

// mergedKeySlotCache holds one hoisted merged-key VirtualSlot together
// with the shape it was built for (M0127-P0.1; design
// leftdeep-joins/05 §3, stage E2). The build and probe loops call
// rebind once per row; in steady state that swaps a single interface
// word inside slot.sources and allocates nothing, where the previous
// per-row mergedKeySlot call allocated five objects.
type mergedKeySlotCache struct {
	slot       *VirtualSlot
	realWidth  int
	nullWidth  int
	realOnLeft bool
	realIdx    int // position of the real source inside slot.sources
}

// rebind returns the cached slot presenting realSlot in the merged
// (left+right) column space. It rebuilds only when the requested shape
// differs from the cached one. Child schemas fix the widths at Open, so
// the rebuild fires at most once per operator in practice — the build
// loops' `width == 0 && len(row) > 0` first-row fallback (an empty child
// schema) is the only case that can change the shape mid-loop, and it
// can fire only once.
//
// The returned slot is valid until the next rebind on the same cache;
// like the slot mergedKeySlot returned before, it must not be retained
// past the key evaluation it was built for.
func (c *mergedKeySlotCache) rebind(realSlot TupleSlot, realWidth, nullWidth int, realOnLeft bool) *VirtualSlot {
	if c.slot == nil || c.realWidth != realWidth || c.nullWidth != nullWidth || c.realOnLeft != realOnLeft {
		c.slot = mergedKeySlot(realSlot, realWidth, nullWidth, realOnLeft)
		c.realWidth, c.nullWidth, c.realOnLeft = realWidth, nullWidth, realOnLeft
		c.realIdx = 1
		if realOnLeft {
			c.realIdx = 0
		}
		return c.slot
	}
	c.slot.sources[c.realIdx] = realSlot
	return c.slot
}

// lazyHashInsertDatum inserts a build row keyed by keyDatum into whichever of
// the two tables buildLazyHashTable selected (M0127-P0.3, 05 §4 stage E3).
//
// keyDatum is never NULL here: both build loops skip a NULL key before calling
// (that is what the `ok` return of evalHashKeyDatumSlot means), so a
// datumToInt64Key miss really does mean "an integer-typed key produced a
// non-integer datum" and not "this row has no key".
func (o *joinOp) lazyHashInsertDatum(keyDatum Datum, row Row) {
	if o.lazyHashIsInt {
		if ik, ok := datumToInt64Key(keyDatum); ok {
			if o.lazyIntHash == nil {
				o.lazyIntHash = make(map[int64][]Row)
			}
			o.lazyIntHash[ik] = append(o.lazyIntHash[ik], row)
			return
		}
		o.demoteIntHash()
	}
	if o.lazyHash == nil {
		o.lazyHash = make(map[string][]Row)
	}
	sk := datumKey(keyDatum)
	o.lazyHash[sk] = append(o.lazyHash[sk], row)
}

// demoteIntHash abandons the int64 representation mid-build and re-keys
// everything inserted so far into the general string table.
//
// This is the safety net under the planner's static type decision: the plan
// says both key columns are machine integers, so every key datum should be
// int64-representable, but a type is a promise about a column and the executor
// deals in datums. Rather than have a broken promise silently drop rows (an
// int64-only table cannot hold a key it cannot represent), the build degrades
// to the always-correct representation and continues.
//
// The re-key is exact rather than approximate: datumKey(KindInt(v)) is
// canonicalNumericKey(v, 0) by construction, which is the same canonical form
// this rebuild produces, so a row inserted before the demotion lands under the
// identical string key as one inserted after it.
func (o *joinOp) demoteIntHash() {
	o.lazyHashIsInt = false
	if o.lazyIntHash == nil {
		return
	}
	if o.lazyHash == nil {
		o.lazyHash = make(map[string][]Row, len(o.lazyIntHash))
	}
	for ik, rows := range o.lazyIntHash {
		sk := canonicalNumericKey(ik, 0)
		o.lazyHash[sk] = append(o.lazyHash[sk], rows...)
	}
	o.lazyIntHash = nil
}

func (o *joinOp) joinPredicateMatch(row Row) (bool, error) {
	if o.plan.Predicate == nil {
		return true, nil
	}
	v, err := evalExpr(o.plan.Predicate, row, o.ctx)
	if err != nil {
		return false, err
	}
	return !v.IsNull() && v.Kind == KindBool && v.BoolValue(), nil
}

// joinPredicateMatchSlot evaluates plan.Predicate against a slot
// (typically o.lazyVirtualOut). Caller must update the source-slot
// .row fields before invocation.
// M0127-P2.2 narrowed the expression from `plan.Predicate` to `execResidual`:
// a conjunct the hash key already enforces must not be re-evaluated per match.
// The narrowing is exactly the set of pairs the key encoding folded in
// (planner.Join.ExecHashKeyPlan), so a pair the planner declined as not
// hash-safe still gets its per-match check — which is why this is a pure
// saving and not a semantic change.
func (o *joinOp) joinPredicateMatchSlot(slot SlotView) (bool, error) {
	if o.execResidual == nil {
		return true, nil
	}
	v, err := evalExprSlot(o.execResidual, slot, o.ctx)
	if err != nil {
		return false, err
	}
	return !v.IsNull() && v.Kind == KindBool && v.BoolValue(), nil
}

// joinSlotChainOn is M0127-P1.1's operational kill switch. Default ON;
// GOOPG_JOIN_SLOT_CHAIN=off at server start (or
// SetJoinSlotChainEnabled(false) from tests) restores the pre-P1.1 seam
// that flattened the probe child's slot into a Row on every pull. Same
// pattern as GOOPG_HASHED_SUBPLAN (subplan_hash.go).
var joinSlotChainOn atomic.Bool

func init() {
	joinSlotChainOn.Store(os.Getenv("GOOPG_JOIN_SLOT_CHAIN") != "off")
}

// SetJoinSlotChainEnabled toggles probe-side slot chaining. Test-only
// API; the operational switch is the environment variable read at init.
// It is read once per joinOp in ensureLazyVirtual, so a toggle takes
// effect from the next Open, not mid-scan.
func SetJoinSlotChainEnabled(on bool) { joinSlotChainOn.Store(on) }

func joinSlotChainEnabled() bool { return joinSlotChainOn.Load() }

// ensureLazyVirtual lazily builds the persistent VirtualSlot used
// by nextLazy to emit joined rows without per-match concat. Source
// order depends on BuildLeft so plan.Output()'s left++right column
// layout is preserved.
func (o *joinOp) ensureLazyVirtual() {
	if o.lazyVirtualOut != nil {
		return
	}
	leftSchema := o.left.Schema()
	rightSchema := o.right.Schema()
	o.lazyBuildSlot = SlotFromRow(nil, nil)
	o.lazyProbeSlot = SlotFromRow(nil, nil)
	o.lazyOuterOnlySlot = SlotFromRow(o.schema, nil)
	// M0127-P1.1: the Semi/Anti emit slot has the probe as its only
	// source, so its column map is the identity over o.schema (which
	// Join.Output() derives from Left for those two types — the probe
	// side, by the buildLazyHashTable contract).
	outerCols := make([]virtualCol, len(o.schema))
	for i := range outerCols {
		outerCols[i] = virtualCol{sourceIdx: 0, sourceCol: int16(i)}
	}
	o.lazyOuterOnlyOut = NewVirtualSlot(o.schema,
		[]TupleSlot{o.lazyProbeSlot}, outerCols)
	o.lazyChainProbe = joinSlotChainEnabled()
	cols := make([]virtualCol, 0, o.lazyLW+o.lazyRW)
	if o.plan.BuildLeft {
		// Output is left ++ right; build side is left → sources
		// [build, probe], cols (0,*) ++ (1,*).
		_ = leftSchema
		_ = rightSchema
		for i := 0; i < o.lazyLW; i++ {
			cols = append(cols, virtualCol{sourceIdx: 0, sourceCol: int16(i)})
		}
		for i := 0; i < o.lazyRW; i++ {
			cols = append(cols, virtualCol{sourceIdx: 1, sourceCol: int16(i)})
		}
		o.lazyVirtualOut = NewVirtualSlot(o.schema,
			[]TupleSlot{o.lazyBuildSlot, o.lazyProbeSlot}, cols)
		// BuildLeft → the probe is the RIGHT side, at sources[1].
		o.lazyProbeSrcIdx, o.lazyProbeWidth = 1, o.lazyRW
		return
	}
	// !BuildLeft: probe is left, build is right → sources
	// [probe, build].
	for i := 0; i < o.lazyLW; i++ {
		cols = append(cols, virtualCol{sourceIdx: 0, sourceCol: int16(i)})
	}
	for i := 0; i < o.lazyRW; i++ {
		cols = append(cols, virtualCol{sourceIdx: 1, sourceCol: int16(i)})
	}
	o.lazyVirtualOut = NewVirtualSlot(o.schema,
		[]TupleSlot{o.lazyProbeSlot, o.lazyBuildSlot}, cols)
	o.lazyProbeSrcIdx, o.lazyProbeWidth = 0, o.lazyLW
}

// bindProbe points the join's output composition at the probe child's
// current slot (M0127-P1.1; design leftdeep-joins/05 §2, stage E1).
//
// Steady state writes one interface word into lazyVirtualOut.sources and
// copies nothing. Two conditions fall back to the pre-P1.1 copy through
// lazyProbeSlot, so the seam can never be the reason a row is wrong:
//
//   - the kill switch is off (GOOPG_JOIN_SLOT_CHAIN=off), and
//   - the child's slot is narrower than the columns lazyVirtualOut reads
//     from it. That is the F7 type-change fallback in its observable
//     form: what matters about "the child returned a different slot" is
//     only whether the new slot can still serve the composed shape.
//     (A WIDER slot needs no fallback — the extra columns are simply
//     never addressed, exactly as the flattened Row's were not.)
//
// The returned bool reports whether chaining took effect; Semi/Anti uses
// it to pick between the chained and the copied outer-only emit slot.
//
// Aliasing the child's storage is safe only because nextLazy pulls a new
// probe row after draining every match of the previous one. Nothing in
// the type system enforces that, so assert it here rather than trust it
// (bundle 02 §2.4's honesty note).
func (o *joinOp) bindProbe(probeSlot TupleSlot) (bool, error) {
	if o.lazyActive {
		return false, &ExecError{Code: "XX000", Message: "join probe rebound while matches were still draining"}
	}
	if o.lazyChainProbe && probeSlot != nil && probeSlot.Width() >= o.lazyProbeWidth {
		o.lazyVirtualOut.sources[o.lazyProbeSrcIdx] = probeSlot
		o.lazyProbeSrc = probeSlot
		return true, nil
	}
	o.lazyProbeSlot.row = slotRow(probeSlot)
	o.lazyVirtualOut.sources[o.lazyProbeSrcIdx] = o.lazyProbeSlot
	o.lazyProbeSrc = o.lazyProbeSlot
	return false, nil
}

// outerOnlyEmit returns the Semi/Anti emit slot for the probe row bound
// by bindProbe. chained reports bindProbe's verdict for the INNER
// composition; the outer-only slot needs the STRICTER width test because
// here the probe slot is the whole emitted tuple, so its width is the
// tuple's width. Pre-P1.1 an over-wide probe child emitted all of its
// columns (slotRow handed the full Row to lazyOuterOnlySlot) — chaining
// would silently narrow that to len(o.schema). P1.1 rewrites the seam, not
// the semantics, so a width mismatch takes the copy and keeps the old
// shape; a later stage may narrow it deliberately.
func (o *joinOp) outerOnlyEmit(probeSlot TupleSlot, chained bool) TupleSlot { //nolint:ireturn
	if chained {
		if probeSlot.Width() == len(o.schema) {
			o.lazyOuterOnlyOut.sources[0] = probeSlot
			return o.lazyOuterOnlyOut
		}
		o.lazyOuterOnlySlot.row = slotRow(probeSlot)
		return o.lazyOuterOnlySlot
	}
	// bindProbe already flattened the child's slot into lazyProbeSlot —
	// reuse that Row rather than materialise the child a second time.
	o.lazyOuterOnlySlot.row = o.lazyProbeSlot.row
	return o.lazyOuterOnlySlot
}

func (o *joinOp) Next() (TupleSlot, error) {
	// M0036 lazy output: yield joined rows on demand.
	if o.lazyProbe != nil {
		return o.nextLazy()
	}
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	idx := o.idx
	o.idx++
	// M0100-0010: propagate left-side ctid through the join so lockRowsOp
	// can stamp tuple locks even after the scan was eagerly drained/closed.
	if o.leftCTIDs != nil && idx < len(o.rowSourceLeft) {
		li := o.rowSourceLeft[idx]
		if li >= 0 && li < len(o.leftCTIDs) {
			lc := o.leftCTIDs[li]
			if lc.hasCTID {
				ms := SlotFromRow(o.Schema(), row)
				ms.hasCTID = true
				ms.ctidBlock = uint32(lc.ptr.Block)
				ms.ctidOff = lc.ptr.Offset
				return ms, nil
			}
		}
	}
	return asSlot(o.Schema(), row), nil
}

// nextLazy yields one joined row at a time for lazy hash joins.
//
// M0071-0014 Stage D-2: per-match concatRows is replaced by
// VirtualSlot composition. lazyBuildSlot.row / lazyProbeSlot.row
// are overwritten in place; predicate eval reads via
// joinPredicateMatchSlot. The plan-emit Schema is preserved by
// the cols mapping in ensureLazyVirtual (BuildLeft swaps source
// order).
func (o *joinOp) nextLazy() (TupleSlot, error) {
	// M0054-0005b: reuse the operator's per-Open null padding rows
	// instead of allocating per call.
	if o.lazyNullLeft == nil || len(o.lazyNullLeft) != o.lazyLW {
		o.lazyNullLeft = nullRow(o.lazyLW)
	}
	if o.lazyNullRight == nil || len(o.lazyNullRight) != o.lazyRW {
		o.lazyNullRight = nullRow(o.lazyRW)
	}
	nullRight := o.lazyNullRight
	o.ensureLazyVirtual()
	// M0127-P2.2: same lazy guard the build loops use — a probe-only unit
	// test reaches nextLazy without ever running Open's key resolution.
	o.ensureExecKeys()
	// Cancellation: cheap ctx check per Next() call so a long
	// probe-only join responds promptly to CancelRequest.
	// (M0058-0005.)
	if o.ctx != nil && o.ctx.Ctx != nil {
		if err := o.ctx.Ctx.Err(); err != nil {
			return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	// M0122-0011: `x NOT IN (subquery)` three-valued-NULL semantics
	// collapse to one of two constants, independent of x, whenever
	// the subquery side is empty or contains a NULL — real per-row
	// hash probing would give the wrong answer in both cases (see
	// the Join.NullAware doc comment), so short-circuit instead:
	//   - subquery produced zero rows: `x NOT IN ()` is TRUE for
	//     every x (even NULL x) — emit every probe row unconditionally.
	//   - subquery produced a NULL key: `x NOT IN (...)` is
	//     NULL/false for every x — emit nothing.
	if o.plan.Type == planner.JoinTypeAnti && o.plan.NullAware {
		if o.antiBuildHasNull {
			return nil, EOF
		}
		if o.antiBuildRows == 0 {
			probeSlot, err := o.lazyProbe.Next()
			if err == EOF {
				return nil, EOF
			}
			if err != nil {
				return nil, err
			}
			// M0127-P1.1: same chained seam as the main loop — this
			// branch emits the probe row untouched, which is exactly
			// what outerOnlyEmit composes.
			chained, err := o.bindProbe(probeSlot)
			if err != nil {
				return nil, err
			}
			o.lazyProbeSrc = nil
			return o.outerOnlyEmit(probeSlot, chained), nil
		}
	}
	for {
		// Continue serving matches from current probe row. Apply
		// the full join Predicate per emitted row — hash matching
		// only checks LeftKey=RightKey, but the planner may have
		// ANDed extra residual conjuncts onto the Predicate via
		// pushOneConjunct (e.g. TPC-H Q9's `ps_partkey=l_partkey`
		// pushed onto the part-join). Without the post-hash filter
		// those extras are silently dropped and the join over-emits.
		for o.lazyActive && o.lazyMatchIdx < len(o.lazyMatches) {
			mi := o.lazyMatchIdx
			m := o.lazyMatches[mi]
			o.lazyMatchIdx++
			o.lazyBuildSlot.row = m
			// M0127-P1.1: the probe source stays bound from bindProbe
			// for the whole drain of this probe row — no per-match
			// re-binding, and no flattened Row to re-bind from.
			ok, err := o.joinPredicateMatchSlot(o.lazyVirtualOut)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			o.lazyProbeMatched = true
			// M0118-0009 (eval-plan-qual): stamp the matched build row's heap
			// ctid onto the emitted slot so a downstream LockRows can recover
			// the TID of a locked relation on the build side (whose scan was
			// drained + closed at Open). Materialises the composed row only in
			// this rare FOR-UPDATE path; the hot path returns the shared
			// VirtualSlot untouched.
			if o.preserveBuildSide && mi < len(o.lazyMatchCTIDs) && o.lazyMatchCTIDs[mi].hasCTID {
				ms := o.lazyVirtualOut.Materialize()
				ms.hasCTID = true
				ms.ctidBlock = uint32(o.lazyMatchCTIDs[mi].ptr.Block)
				ms.ctidOff = o.lazyMatchCTIDs[mi].ptr.Offset
				return ms, nil
			}
			return o.lazyVirtualOut, nil
		}
		if o.lazyActive {
			// Exhausted every hash-bucket candidate for this probe row
			// without any of them passing the residual Predicate (the
			// hash key matched, but a residual conjunct like
			// `attnum = conkey[1]` didn't). The len(matches)==0 branch
			// below only covers the hash-level miss, not this
			// predicate-level one, so without this check the outer row
			// is silently dropped instead of null-padded.
			o.lazyActive = false
			if o.plan.Type == planner.JoinTypeLeft && !o.plan.BuildLeft && !o.lazyProbeMatched {
				// The probe source is still bound to this row; only
				// the build side changes to the NULL padding.
				o.lazyBuildSlot.row = nullRight
				o.lazyProbeSrc = nil
				return o.lazyVirtualOut, nil
			}
		}
		// Pull next probe row.
		probeSlot, err := o.lazyProbe.Next()
		if err == EOF {
			return nil, EOF
		}
		if err != nil {
			return nil, err
		}
		// M0127-P1.1 (05 §2, stage E1): bind the child's slot as the
		// probe source instead of flattening it with slotRow.
		chained, err := o.bindProbe(probeSlot)
		if err != nil {
			return nil, err
		}
		// M0126-0003 0b: evaluate the probe key against a
		// VirtualSlot over {probeSlot, nullOtherSide} instead
		// of copying into a merged key Row.
		//
		// realWidth must match the probe side's width:
		//   BuildLeft=true  → probe is right side → realWidth=lazyRW
		//   BuildLeft=false → probe is left side  → realWidth=lazyLW
		var keySlot *VirtualSlot
		if o.plan.BuildLeft {
			keySlot = o.lazyProbeKeySlot.rebind(probeSlot, o.lazyRW, o.lazyLW, false)
		} else {
			keySlot = o.lazyProbeKeySlot.rebind(probeSlot, o.lazyLW, o.lazyRW, true)
		}
		probeKeyExpr := o.probeKeyExprs[0]
		var key string // used only by the string path + the CTID lookup
		var ok bool
		var matches []Row
		if o.multiKey() {
			// M0127-P2.2 (05 §5, stage E4): every equi-pair is in the key, so
			// a column the qual placement pinned to a constant no longer
			// collapses the bucket space — and the conjuncts folded into the
			// key are gone from execResidual, which is what makes the
			// all-equijoin join do zero interpreted work per match.
			matches, key, ok, err = o.compositeProbeMatches(keySlot, o.preserveBuildSide)
			if err != nil {
				return nil, err
			}
		} else if o.lazyHashIsInt {
			// int64 fast-path: hash the probe key as an int64 (no per-row
			// string alloc). A probe key that isn't int64-representable
			// cannot equal any (all-int64) build key → no match.
			kd, kok, kerr := o.evalHashKeyDatumSlot(probeKeyExpr, keySlot)
			if kerr != nil {
				return nil, kerr
			}
			ok = kok
			if kok {
				if ik, iok := datumToInt64Key(kd); iok {
					matches = o.lazyIntHash[ik]
				}
			}
		} else {
			// Assign the OUTER key: the preserveBuildSide CTID lookup
			// below reads o.lazyHashCTID[key], so a shadowing inner
			// declaration would leave it empty for FOR UPDATE joins.
			key, ok, err = o.evalHashKeySlot(probeKeyExpr, keySlot)
			if err != nil {
				return nil, err
			}
			matches = o.lazyHash[key]
		}
		if !ok {
			matches = nil
		}
		// M0061-0001: Semi / Anti emit just the probe row at most
		// once. NULL probe key never matches (`ok == false`):
		//   - Semi: skip the probe row (no match).
		//   - Anti: keep the probe row (matches PostgreSQL
		//     `NOT EXISTS` semantics — equality cannot be true).
		//
		// M0122-0011: NullAware Anti (NOT IN) is the one exception —
		// a NULL outer/probe value means `x IN (non-empty subquery)`
		// evaluates to NULL, so `NOT IN` is NULL too and the row is
		// excluded, not kept. The build-empty/build-has-NULL cases
		// were already short-circuited above this loop, so reaching
		// here means the subquery is non-empty and NULL-free.
		if !ok && o.plan.Type == planner.JoinTypeAnti && o.plan.NullAware {
			continue
		}
		//
		// M0071-0009: hash matches are necessary but not
		// sufficient — the planner may have lifted residual
		// non-equi conjuncts (e.g. Q21's
		// `l3.l_suppkey <> l1.l_suppkey`) onto the join Predicate
		// via unnestExistsExpr's M0062-0005 residual lift. Without
		// re-evaluating the Predicate per match, Anti silently
		// over-excludes (every l1 self-matches a late l3 hash
		// entry where l3=l1, so the residual is essential to
		// distinguish self-match from a different-supplier
		// match). This was Q21's silent-FN root cause: 0 rows vs
		// canonical ~411.
		//
		// Walk matches and apply Predicate; treat a "match" as
		// hash match AND Predicate=TRUE. The slot composition
		// already covers both Semi/Anti and INNER predicate eval
		// — re-bind the build slot per candidate match.
		if o.plan.Type == planner.JoinTypeSemi || o.plan.Type == planner.JoinTypeAnti {
			anyMatch := false
			if ok && len(matches) > 0 {
				// Probe source already bound by bindProbe.
				for _, m := range matches {
					o.lazyBuildSlot.row = m
					pok, err := o.joinPredicateMatchSlot(o.lazyVirtualOut)
					if err != nil {
						return nil, err
					}
					if pok {
						anyMatch = true
						break
					}
				}
			}
			if o.plan.Type == planner.JoinTypeSemi {
				if !anyMatch {
					continue
				}
				o.lazyProbeSrc = nil
				return o.outerOnlyEmit(probeSlot, chained), nil
			}
			// Anti: keep iff no match passed the predicate.
			if anyMatch {
				continue
			}
			o.lazyProbeSrc = nil
			return o.outerOnlyEmit(probeSlot, chained), nil
		}
		if len(matches) == 0 {
			if o.plan.Type == planner.JoinTypeLeft && !o.plan.BuildLeft {
				// LEFT JOIN: preserve unmatched left rows. The probe
				// source is already bound; bind the build slot to the
				// nullRight padding. virtualOut already composes
				// [probe, build] for !BuildLeft.
				o.lazyProbeSrc = nil
				o.lazyBuildSlot.row = nullRight
				return o.lazyVirtualOut, nil
			}
			// No matches, not LEFT — skip this probe row.
			continue
		}
		o.lazyMatches = matches
		if o.preserveBuildSide {
			o.lazyMatchCTIDs = o.lazyHashCTID[key]
		}
		o.lazyMatchIdx = 0
		o.lazyProbeMatched = false
		o.lazyActive = true
		// Continue loop to yield first match.
	}
}

func (o *joinOp) Close() error {
	o.rows = nil
	o.leftCTIDs = nil
	o.rowSourceLeft = nil
	o.lazyHash = nil
	o.lazyIntHash = nil
	o.lazyProbe = nil
	o.lazyProbeSrc = nil
	o.lazyMatches = nil
	o.lazyMatchIdx = 0
	o.lazyActive = false
	o.ctx = nil
	o.idx = 0
	errL := o.left.Close()
	errR := o.right.Close()
	if errL != nil {
		return errL
	}
	return errR
}

func (o *joinOp) Schema() planner.Schema { return o.schema }

// aggregateOp performs grouped aggregation in memory.
type aggregateOp struct {
	plan   *planner.Aggregate
	child  Operator
	schema planner.Schema

	ctx  *Context
	rows []Row
	idx  int

	// sharedUserStates supports PG aggregate state sharing: when multiple user-defined
	// aggregates have the same sfunc/stype and the same input, sfunc is called only
	// once per row and the state is shared. SharedStateSlot=-1 means no sharing. M0097-0035.
	sharedUserStates       []Datum
	sharedUserStateSet     []bool
	sharedUserStateVersion []int64 // which currentRowVersion last updated this slot
	currentRowVersion      int64   // incremented per input row
}

// floatSpecialKind encodes the IEEE 754 special-value state for
// sum/avg when float4/float8 inputs contain NaN or Infinity.
type floatSpecialKind int8

const (
	floatSpecialNone     floatSpecialKind = 0
	floatSpecialNaN      floatSpecialKind = 1 // NaN dominates all
	floatSpecialPosInf   floatSpecialKind = 2 // +Infinity
	floatSpecialNegInf   floatSpecialKind = 3 // -Infinity
)

type aggRuntime struct {
	hasValue bool
	value    Datum
	// sum tracks INT-only running sums; numericSum tracks the
	// NUMERIC accumulator. Each aggregate-call uses exactly one
	// of them based on the first non-NULL argument's kind. The
	// two are not mixed within a single aggregate.
	sum        int64
	numericSum Datum
	count      int64
	// floatSpecial tracks NaN/Infinity state for sum/avg when float
	// inputs contain special IEEE 754 values (stored as KindString).
	// M0097-0053.
	floatSpecial floatSpecialKind
	distinct     map[string]struct{}
	// Extended aggregate accumulators (M0097-0007).
	boolResult bool   // for bool_and / bool_or / every
	intResult  int64  // for bit_and / bit_or / bit_xor
	strResult  string // for string_agg
	// arrayElems holds the accumulated element format-strings for
	// array_agg(expr); arrayElemNull[i] marks element i as NULL
	// (reserved — current applyAgg skips NULL inputs, so this is
	// always all-false in practice). finishAgg emits the standard
	// `{e1,e2}` text-array literal via formatTextArray, which the
	// libpqrcv `fetch_table_list` probe pipes back into
	// pg_get_publication_tables. M0103-0008 probe-survival.
	arrayElems    []string
	arrayElemNull []bool
	// arrayElemKeys stores ORDER BY key values for array_agg(x ORDER BY y).
	// Each entry corresponds to arrayElems[i]; nil when no ORDER BY.
	arrayElemKeys [][]Datum
	// strElems/strDelims/strElemKeys are string_agg's deferred-concatenation
	// mode, used ONLY when the call carries its own ORDER BY (M0125-0019).
	// PostgreSQL sorts the transition inputs before running the transition
	// function (nodeAgg.c process_ordered_aggregate_single), so the delimiter
	// that separates two adjacent pieces is the *right-hand* row's own second
	// argument — which is only knowable after the sort. strResult stays the
	// accumulator for the far more common unordered case.
	strElems    []string
	strDelims   []string
	strElemKeys [][]Datum
	// Variance accumulators (var_pop/var_samp/stddev_pop/stddev_samp).
	// Uses Youngs-Cramer algorithm for float inputs.
	floatSx float64 // running sum of values (Youngs-Cramer Sx)
	floatM2 float64 // running sum of squared deviations (Youngs-Cramer Sxx)
	// Exact integer accumulators for var_pop/var_samp/stddev on integer inputs.
	// When intExact is true, these hold exact values and finishAgg uses them
	// instead of the float Youngs-Cramer values. Matches PG's int4_accum/int8_accum.
	intExact bool
	intSx    *big.Int // Σx
	intSxx   *big.Int // Σx²
	// Exact rational accumulators for var_pop/var_samp/stddev on numeric inputs.
	// When numericExact is true, these hold exact rational values matching PG's
	// numeric_accum path (which uses exact arithmetic, not floating point). M0097-0125.
	numericExact bool
	numericSx    *big.Rat // Σx (exact)
	numericSxx   *big.Rat // Σx² (exact)
	// Regression accumulators for regr_*/covar_*/corr. M0097-0020.
	// Follows PostgreSQL's float8_regr_accum signature: first arg is y, second is x.
	regrN     int64   // count of non-NULL (x,y) pairs
	regrSumX  float64 // Σx
	regrSumY  float64 // Σy
	regrSumXX float64 // Σx²
	regrSumXY float64 // Σx*y
	regrSumYY float64 // Σy²
	// userState holds the running state for user-defined aggregates (CREATE AGGREGATE).
	// Initialized from initcond; updated by sfunc on each row.
	userState    Datum
	userStateSet bool // true once userState has been initialized
	// withinGroupElems accumulates per-row values for WITHIN GROUP (ORDER BY ...)
	// ordered-set aggregates (percentile_cont, percentile_disc, rank, dense_rank, mode).
	// Each entry is [sortKey0, sortKey1, ..., valueExpr]. M0097-0035.
	withinGroupElems [][]Datum
	// withinGroupDirectArg stores the "direct argument" (e.g. the fraction p in
	// percentile_cont(p)) evaluated from the first row of the group. M0097-0035.
	withinGroupDirectArg    Datum
	withinGroupDirectArgSet bool
	// withinGroupDirectArgs stores ALL direct args for multi-arg hypothetical-set aggregates
	// like rank(5,'AZZZZ',50) WITHIN GROUP (ORDER BY col1, col2, col3). Set alongside
	// withinGroupDirectArg; used for tuple comparison in rank/dense_rank/cume_dist/percent_rank.
	withinGroupDirectArgs []Datum
	// distinctUserAggRows holds per-row arg vectors for user-defined aggregates
	// with DISTINCT (and optional ORDER BY). Deferred to finishAgg for correct
	// multi-arg dedup and ORDER BY sort before sfunc calls. M0097-0035.
	distinctUserAggRows [][]Datum // inner: [sortKey0..., arg0, arg1, ...]
}

func newAggregateOp(plan *planner.Aggregate, child Operator) *aggregateOp {
	return &aggregateOp{plan: plan, child: child, schema: plan.Output()}
}

func (o *aggregateOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.idx = 0 // reset read cursor — o.rows is rebuilt below, so always start at 0

	// P9 Finalize: publish the accumulator BEFORE opening the child, because
	// the child is a Gather and opening it launches the workers that write to
	// it. Registering afterwards would be a race with the first worker.
	var accum *aggPartialAccum
	if o.plan.Mode == planner.AggModeFinal && o.plan.PartialSource != nil {
		accum = newAggPartialAccum()
		if ctx.PartialAggStates == nil {
			ctx.PartialAggStates = map[*planner.Aggregate]*aggPartialAccum{}
		}
		ctx.PartialAggStates[o.plan.PartialSource] = accum
		defer delete(ctx.PartialAggStates, o.plan.PartialSource)
	}

	if err := o.child.Open(ctx); err != nil {
		return err
	}

	// Compute the number of shared state slots needed for this aggregate. M0097-0035.
	maxSlot := -1
	for _, call := range o.plan.Aggs {
		if call.SharedStateSlot > maxSlot {
			maxSlot = call.SharedStateSlot
		}
	}
	nSlots := maxSlot + 1
	o.sharedUserStates = make([]Datum, nSlots)
	o.sharedUserStateSet = make([]bool, nSlots)
	o.sharedUserStateVersion = make([]int64, nSlots)
	o.currentRowVersion = 0

	type groupRuntime struct {
		groupValues      Row
		passthroughVals  Row // values of functionally-determined passthrough columns
		aggs             []aggRuntime
	}

	groups := map[string]*groupRuntime{}
	order := make([]string, 0)

	if len(o.plan.GroupExprs) == 0 {
		var ptVals Row
		if len(o.plan.Passthrough) > 0 {
			ptVals = make(Row, len(o.plan.Passthrough))
		}
		groups["__all__"] = &groupRuntime{groupValues: nil, passthroughVals: ptVals, aggs: make([]aggRuntime, len(o.plan.Aggs))}
		order = append(order, "__all__")
	}

	for {
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		if ctx.Ctx != nil {
			if cerr := ctx.Ctx.Err(); cerr != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		key, groupValues, err := o.evalGroupKey(slot)
		if err != nil {
			return err
		}
		gr, ok := groups[key]
		if !ok {
			// Evaluate passthrough (functionally-determined) columns from the first row.
			var ptVals Row
			if len(o.plan.Passthrough) > 0 {
				ptVals = make(Row, len(o.plan.Passthrough))
				for i, expr := range o.plan.Passthrough {
					v, err := evalExprSlot(expr, slot, ctx)
					if err != nil {
						ptVals[i] = NullDatum
					} else {
						ptVals[i] = v
					}
				}
			}
			gr = &groupRuntime{groupValues: groupValues, passthroughVals: ptVals, aggs: make([]aggRuntime, len(o.plan.Aggs))}
			groups[key] = gr
			order = append(order, key)
		}

		o.currentRowVersion++
		for i, call := range o.plan.Aggs {
			// For user-defined aggregates with shared transition state, only the
			// "leader" (first call with this SharedStateSlot) calls sfunc. Followers
			// skip applyAgg — they will be synced from the leader's final state just
			// before finishAgg. M0097-0035.
			if call.SharedStateSlot >= 0 && call.UserAgg != nil && i > 0 {
				isFollower := false
				for j := 0; j < i; j++ {
					if o.plan.Aggs[j].SharedStateSlot == call.SharedStateSlot {
						isFollower = true
						break
					}
				}
				if isFollower {
					continue
				}
			}
			if err := o.applyAgg(&gr.aggs[i], call, slot); err != nil {
				return err
			}
		}
	}

	switch o.plan.Mode {
	case planner.AggModePartial:
		// Publish this worker's groups and emit NOTHING. The Finalize node
		// supplies every output row from the accumulator, so a Partial node
		// returning zero rows is by construction, not a failure — see the
		// loud refusal on the Finalize side if the accumulator is missing.
		pub := lookupAggPartialAccum(ctx, o.plan)
		if pub == nil {
			return &ExecError{
				Code: "XX000",
				Message: "internal error: partial aggregate has no accumulator; " +
					"a Partial node was built without the Finalize node that reads it",
			}
		}
		for _, key := range order {
			gr := groups[key]
			if gr == nil {
				continue
			}
			if err := pub.merge(key, gr.groupValues, gr.passthroughVals, gr.aggs, o.plan.Aggs); err != nil {
				return err
			}
		}
		o.rows = nil
		return nil

	case planner.AggModeFinal:
		if accum == nil {
			return &ExecError{
				Code:    "XX000",
				Message: "internal error: finalize aggregate has no partial source",
			}
		}
		// The child (a Gather) has been drained to EOF, so every worker has
		// returned from its Open and every merge is complete. Replace the
		// locally-collected groups — which are empty, the Partial nodes having
		// emitted no rows — with the combined ones, and fall through to the
		// ordinary emit path so finishAgg, passthrough and the output sort are
		// the SAME code serial execution uses.
		accum.mu.Lock()
		groups = make(map[string]*groupRuntime, len(accum.groups))
		order = order[:0]
		for _, key := range accum.order {
			g := accum.groups[key]
			groups[key] = &groupRuntime{
				groupValues:     g.groupValues,
				passthroughVals: g.passthrough,
				aggs:            g.states,
			}
			order = append(order, key)
		}
		accum.mu.Unlock()
	}

	o.rows = make([]Row, 0, len(order))
	nGroupCols := len(o.plan.GroupExprs)
	for idx, key := range order {
		// M0062-followup: mirror the input-drain ctx check (line ~629)
		// on the output-materialisation loop. A 1 M-group aggregate's
		// rebuild can otherwise take seconds without a cancel
		// opportunity.
		if idx&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		gr := groups[key]
		if gr == nil {
			continue
		}
		out := make(Row, 0, len(gr.groupValues)+len(o.plan.Aggs)+len(gr.passthroughVals))
		out = append(out, gr.groupValues...)
		// Sync follower aggregates from their leader before finishAgg. M0097-0035.
		// For DISTINCT aggregates with actual followers (multiple aggs sharing the
		// same slot), compute the leader's sfunc state once and inject it into
		// followers so they skip sfunc calls (avoiding duplicate NOTICE/side-
		// effects) and only apply their own finalfunc. M0097-0155.
		//
		// First, count how many aggregates exist per SharedStateSlot to detect
		// actual sharing (a slot with only one agg is not shared).
		slotCount := map[int]int{}
		for _, call := range o.plan.Aggs {
			if call.SharedStateSlot >= 0 && call.UserAgg != nil {
				slotCount[call.SharedStateSlot]++
			}
		}
		type slotState struct {
			state    Datum
			computed bool
		}
		distinctSlotStates := map[int]slotState{}
		for i, call := range o.plan.Aggs {
			if call.SharedStateSlot < 0 || call.UserAgg == nil {
				continue
			}
			if !(call.Distinct || len(call.OrderBy) > 0) {
				// Non-DISTINCT sync: copy userState as before.
				if i == 0 {
					continue
				}
				for j := 0; j < i; j++ {
					if o.plan.Aggs[j].SharedStateSlot == call.SharedStateSlot {
						gr.aggs[i].userState = gr.aggs[j].userState
						gr.aggs[i].userStateSet = gr.aggs[j].userStateSet
						gr.aggs[i].hasValue = gr.aggs[j].hasValue
						break
					}
				}
				continue
			}
			// DISTINCT path: pre-compute leader's sfunc state once only when
			// there are actual followers (slot count > 1). Solo aggregates run
			// finishAgg normally to preserve ORDER BY sort behaviour.
			if slotCount[call.SharedStateSlot] <= 1 {
				continue
			}
			ss, alreadyComputed := distinctSlotStates[call.SharedStateSlot]
			if !alreadyComputed {
				// This is the leader: compute dedup+sort+sfunc state.
				ua := call.UserAgg
				st := gr.aggs[i]
				nSortKeys := len(call.OrderBy)
				var deduped [][]Datum
				if call.Distinct && len(st.distinctUserAggRows) > 0 {
					seen := map[string]struct{}{}
					for _, row := range st.distinctUserAggRows {
						argSlice := row[nSortKeys:]
						var keyParts []string
						for _, d := range argSlice {
							keyParts = append(keyParts, datumKey(d))
						}
						k := strings.Join(keyParts, "\t")
						if _, ok := seen[k]; ok {
							continue
						}
						seen[k] = struct{}{}
						deduped = append(deduped, row)
					}
				} else {
					deduped = st.distinctUserAggRows
				}
				state := userAggInitState(ua)
				for _, row := range deduped {
					argSlice := row[nSortKeys:]
					sfuncArgs := make([]Datum, 0, 1+len(argSlice))
					sfuncArgs = append(sfuncArgs, state)
					sfuncArgs = append(sfuncArgs, argSlice...)
					newState, serr := executeSFuncCall(ua.SFunc, sfuncArgs, o.ctx)
					if sfuncRaised(serr) {
						return serr
					}
					if serr == nil {
						state = newState
					}
				}
				ss = slotState{state: state, computed: true}
				distinctSlotStates[call.SharedStateSlot] = ss
				// Replace leader's rows with the pre-computed state so finishAgg
				// skips sfunc and applies only the leader's finalfunc.
				gr.aggs[i].userState = state
				gr.aggs[i].userStateSet = true
				gr.aggs[i].hasValue = true
				gr.aggs[i].distinctUserAggRows = nil
			} else {
				// Follower: inject leader's sfunc state; clear rows so finishAgg
				// skips sfunc and applies only the follower's finalfunc.
				gr.aggs[i].userState = ss.state
				gr.aggs[i].userStateSet = true
				gr.aggs[i].hasValue = true
				gr.aggs[i].distinctUserAggRows = nil
			}
		}
		for i, call := range o.plan.Aggs {
			d, ferr := o.finishAgg(gr.aggs[i], call)
			if ferr != nil {
				return ferr
			}
			out = append(out, d)
		}
		out = append(out, gr.passthroughVals...)
		o.rows = append(o.rows, out)
	}
	// Sort output rows by GROUP BY key columns for deterministic ordering
	// matching PostgreSQL's sort-based aggregate behavior. Without an explicit
	// ORDER BY the planner wraps with a Sort node; here we pre-sort by key
	// so hash-aggregate output is stable and GROUP BY queries without ORDER BY
	// match PG's sort-aggregate output order. M0097-0117.
	if nGroupCols > 0 {
		sort.SliceStable(o.rows, func(i, j int) bool {
			ra, rb := o.rows[i], o.rows[j]
			for k := 0; k < nGroupCols && k < len(ra) && k < len(rb); k++ {
				c, _ := compareDatum(ra[k], rb[k], 0)
				if c < 0 {
					return true
				}
				if c > 0 {
					return false
				}
			}
			return false
		})
	}
	return nil
}

func (o *aggregateOp) evalGroupKey(slot TupleSlot) (string, Row, error) {
	if len(o.plan.GroupExprs) == 0 {
		return "__all__", nil, nil
	}
	vals := make(Row, 0, len(o.plan.GroupExprs))
	parts := make([]string, 0, len(o.plan.GroupExprs))
	for _, g := range o.plan.GroupExprs {
		v, err := evalExprSlot(g, slot, o.ctx)
		if err != nil {
			return "", nil, err
		}
		// M0073-0004 retention boundary: arena-backed Datums
		// (varchar / char / text / bytea group keys) must be
		// promoted to owned []byte before they enter
		// groupRuntime.groupValues — the next input page's
		// arena.Reset would invalidate them otherwise.
		// MaterializeArena is a no-op for non-arena Datums.
		v = v.MaterializeArena()
		vals = append(vals, v)
		parts = append(parts, datumKey(v))
	}
	return strings.Join(parts, "|"), vals, nil
}

func (o *aggregateOp) applyAgg(st *aggRuntime, call planner.AggregateCall, slot TupleSlot) error {
	// FILTER (WHERE condition): skip this row if the condition is false/null.
	// M0097-0007.
	if call.Filter != nil {
		fv, ferr := evalExprSlot(call.Filter, slot, o.ctx)
		if ferr != nil || fv.IsNull() || fv.Kind != KindBool || !fv.BoolValue() {
			return nil // skip row — filter not satisfied
		}
	}

	name := strings.ToLower(call.Name)
	if call.Star {
		if call.UserAgg != nil {
			// User-defined star aggregate (e.g. newcnt(*)) — call sfunc with state only.
			ua := call.UserAgg
			if !st.userStateSet {
				st.userState = userAggInitState(ua)
				st.userStateSet = true
			}
			newState, serr := executeSFuncCall(ua.SFunc, []Datum{st.userState}, o.ctx)
			if sfuncRaised(serr) {
				return serr
			}
			if serr == nil {
				st.userState = newState
			}
			st.hasValue = true
			return nil
		}
		if name != "count" {
			return &ExecError{Code: "0A000", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s(*) is not supported", call.Name)}
		}
		if call.Distinct {
			return &ExecError{Code: "0A000", Pos: call.Pos(), Message: "count(distinct *) is not supported"}
		}
		st.count++
		st.hasValue = true
		return nil
	}

	// Ordered-set aggregates (WITHIN GROUP): accumulate sort-key values per row.
	// The direct arg (call.Arg, e.g. the fraction p in percentile_cont(p)) is
	// evaluated lazily in finishAgg from the first row's value. M0097-0035.
	if call.WithinGroup && len(call.WithinGroupOrderBy) > 0 {
		// Evaluate the direct argument (call.Arg) from the first row.
		// If evaluation fails (e.g. rank('fred') with int ORDER BY), propagate
		// the error so the caller sees "invalid input syntax" instead of NULL.
		if !st.withinGroupDirectArgSet && call.Arg != nil {
			v, verr := evalExprSlot(call.Arg, slot, o.ctx)
			if verr != nil {
				return verr
			}
			st.withinGroupDirectArg = v
			st.withinGroupDirectArgSet = true
			// Evaluate additional direct args for multi-arg hypothetical-set aggregates.
			if len(call.ExtraArgs) > 0 {
				st.withinGroupDirectArgs = make([]Datum, 1+len(call.ExtraArgs))
				st.withinGroupDirectArgs[0] = v
				for ei, ea := range call.ExtraArgs {
					ev, eerr := evalExprSlot(ea, slot, o.ctx)
					if eerr != nil {
						return eerr
					}
					st.withinGroupDirectArgs[ei+1] = ev
				}
			}
		}
		row := make([]Datum, len(call.WithinGroupOrderBy))
		allNull := true
		for i, sk := range call.WithinGroupOrderBy {
			v, verr := evalExprSlot(sk.Expr, slot, o.ctx)
			if verr != nil {
				return verr
			}
			row[i] = v
			if !v.IsNull() {
				allNull = false
			}
		}
		if !allNull {
			st.withinGroupElems = append(st.withinGroupElems, row)
			st.hasValue = true
		}
		return nil
	}

	if call.Arg == nil {
		// Zero-arg extended aggregate stub — just count rows.
		st.count++
		return nil
	}
	arg, err := evalExprSlot(call.Arg, slot, o.ctx)
	if err != nil {
		return err
	}
	// For user-defined aggregates with a strict sfunc: skip rows where any arg is NULL.
	// STRICT means "returns null on null input" — sfunc not called when any arg is NULL.
	// This includes the state (first sfunc arg): if state is NULL, skip all subsequent rows.
	if call.UserAgg != nil && call.UserAgg.SFuncStrict {
		if st.userStateSet && st.userState.IsNull() {
			return nil // state went NULL; sfunc cannot be called (state is first arg, strict)
		}
		if arg.IsNull() {
			return nil
		}
		if call.Arg2 != nil {
			a2, _ := evalExprSlot(call.Arg2, slot, o.ctx)
			if a2.IsNull() {
				return nil
			}
		}
		for _, ea := range call.ExtraArgs {
			eav, _ := evalExprSlot(ea, slot, o.ctx)
			if eav.IsNull() {
				return nil
			}
		}
	}
	// For built-in aggregates: NULL handling depends on the aggregate.
	// array_agg includes NULLs; all others skip NULLs.
	if arg.IsNull() && name != "array_agg" && call.UserAgg == nil {
		return nil
	}

	// User-defined DISTINCT aggregates: defer dedup to finishAgg for correct multi-arg handling.
	// Only apply the single-arg outer dedup for built-in aggregates. M0097-0035.
	if call.Distinct && call.UserAgg == nil {
		if st.distinct == nil {
			st.distinct = map[string]struct{}{}
		}
		k := datumKey(arg)
		if _, seen := st.distinct[k]; seen {
			return nil
		}
		st.distinct[k] = struct{}{}
	}

	switch name {
	case "count":
		st.count++
		st.hasValue = true
	case "sum", "avg":
		switch arg.Kind {
		case KindInt:
			st.sum += arg.Int
		case KindNumeric:
			if !st.hasValue || st.numericSum.Kind != KindNumeric {
				st.numericSum = Datum{Kind: KindNumeric, Scale: arg.Scale}
			}
			s, err := numericAdd(st.numericSum, arg)
			if err != nil {
				return &ExecError{Code: "22003", Pos: call.Pos(), Message: err.Error()}
			}
			st.numericSum = s
		case KindString:
			// IEEE 754 special values and regular float strings from evalCast.
			// NaN dominates; +Inf/-Inf obey IEEE addition rules. M0097-0053 / M0097-0020.
			// Use case-insensitive and alias-aware comparisons since evalCast for
			// float8 returns the raw KindString value without normalization.
			sv := strings.TrimSpace(arg.StringValue())
			svLow := strings.ToLower(sv)
			switch {
			case svLow == "nan":
				st.floatSpecial = floatSpecialNaN
			case svLow == "infinity" || svLow == "+infinity" || svLow == "inf" || svLow == "+inf":
				switch st.floatSpecial {
				case floatSpecialNegInf:
					st.floatSpecial = floatSpecialNaN // +Inf + (-Inf) = NaN
				case floatSpecialNone:
					st.floatSpecial = floatSpecialPosInf
				}
			case svLow == "-infinity" || svLow == "-inf":
				switch st.floatSpecial {
				case floatSpecialPosInf:
					st.floatSpecial = floatSpecialNaN // -Inf + (+Inf) = NaN
				case floatSpecialNone:
					st.floatSpecial = floatSpecialNegInf
				}
			default:
				// Regular numeric string (e.g. from evalCast on float8/float4 column).
				// Parse and accumulate as KindNumeric. M0097-0020.
				m, scale, perr := parseNumeric(sv)
				if perr != nil {
					return &ExecError{Code: "42804", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s requires numeric argument in v0", name)}
				}
				numArg := newNumeric(m, int(scale))
				if !st.hasValue || st.numericSum.Kind != KindNumeric {
					st.numericSum = Datum{Kind: KindNumeric, Scale: numArg.Scale}
				}
				s, serr := numericAdd(st.numericSum, numArg)
				if serr != nil {
					return &ExecError{Code: "22003", Pos: call.Pos(), Message: serr.Error()}
				}
				st.numericSum = s
			}
		default:
			return &ExecError{Code: "42804", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s requires numeric argument in v0", name)}
		}
		st.count++
		st.hasValue = true
	case "min":
		if !st.hasValue {
			// M0073-0004 retention boundary: arena-backed
			// Datums must be promoted before storage in
			// st.value (next input page's Reset would
			// invalidate the arena bytes otherwise).
			st.value = arg.MaterializeArena()
			st.hasValue = true
			return nil
		}
		cmp, err := compareDatum(arg, st.value, call.Pos())
		if err != nil {
			return err
		}
		if cmp < 0 {
			st.value = arg.MaterializeArena()
		}
	case "max":
		if !st.hasValue {
			st.value = arg.MaterializeArena()
			st.hasValue = true
			return nil
		}
		cmp, err := compareDatum(arg, st.value, call.Pos())
		if err != nil {
			return err
		}
		if cmp > 0 {
			st.value = arg.MaterializeArena()
		}
	case "bool_and", "every":
		bv, ok := arg.Kind == KindBool && arg.BoolValue(), arg.Kind == KindBool
		if !ok {
			return nil
		}
		if !st.hasValue {
			st.boolResult = bv
			st.hasValue = true
		} else {
			st.boolResult = st.boolResult && bv
		}
	case "bool_or":
		bv, ok := arg.BoolValue(), arg.Kind == KindBool
		if !ok {
			return nil
		}
		if !st.hasValue {
			st.boolResult = bv
			st.hasValue = true
		} else {
			st.boolResult = st.boolResult || bv
		}
	case "bit_and":
		if arg.Kind == KindInt {
			if !st.hasValue {
				st.intResult = arg.Int
				st.hasValue = true
			} else {
				st.intResult &= arg.Int
			}
		} else if arg.Kind == KindString {
			// BIT(n) type: parse binary string like "B0101" or "0101".
			bstr := arg.StringValue()
			bstr = strings.TrimPrefix(strings.TrimPrefix(bstr, "B"), "b")
			if v, err := strconv.ParseInt(bstr, 2, 64); err == nil {
				if !st.hasValue {
					st.intResult = v
					st.strResult = fmt.Sprintf("b%d", len(bstr))
					st.hasValue = true
				} else {
					st.intResult &= v
				}
			}
		}
	case "bit_or":
		if arg.Kind == KindInt {
			if !st.hasValue {
				st.intResult = arg.Int
				st.hasValue = true
			} else {
				st.intResult |= arg.Int
			}
		} else if arg.Kind == KindString {
			bstr := arg.StringValue()
			bstr = strings.TrimPrefix(strings.TrimPrefix(bstr, "B"), "b")
			if v, err := strconv.ParseInt(bstr, 2, 64); err == nil {
				if !st.hasValue {
					st.intResult = v
					st.strResult = fmt.Sprintf("b%d", len(bstr))
					st.hasValue = true
				} else {
					st.intResult |= v
				}
			}
		}
	case "bit_xor":
		if arg.Kind == KindInt {
			if !st.hasValue {
				st.intResult = arg.Int
				st.hasValue = true
			} else {
				st.intResult ^= arg.Int
			}
		} else if arg.Kind == KindString {
			bstr := arg.StringValue()
			bstr = strings.TrimPrefix(strings.TrimPrefix(bstr, "B"), "b")
			if v, err := strconv.ParseInt(bstr, 2, 64); err == nil {
				if !st.hasValue {
					st.intResult = v
					st.strResult = fmt.Sprintf("b%d", len(bstr))
					st.hasValue = true
				} else {
					st.intResult ^= v
				}
			}
		}
	case "string_agg":
		// string_agg(expr, delimiter) — accumulate in strResult with delimiter.
		// For bytea values, st.boolResult=true signals hex-encoded mode.
		const hexChars = "0123456789abcdef"
		if arg.Kind == KindBytes {
			// Bytea string_agg: concatenate hex-encoded bytes with hex-encoded delimiter.
			b := arg.BytesValue()
			hexBuf := make([]byte, len(b)*2)
			for i, bb := range b {
				hexBuf[2*i] = hexChars[bb>>4]
				hexBuf[2*i+1] = hexChars[bb&0x0f]
			}
			hexVal := string(hexBuf)
			delimHex := ""
			if call.Arg2 != nil {
				dv, _ := evalExprSlot(call.Arg2, slot, o.ctx)
				if !dv.IsNull() && dv.Kind == KindBytes {
					db := dv.BytesValue()
					dh := make([]byte, len(db)*2)
					for i, bb := range db {
						dh[2*i] = hexChars[bb>>4]
						dh[2*i+1] = hexChars[bb&0x0f]
					}
					delimHex = string(dh)
				}
			}
			st.boolResult = true // bytea mode flag
			if len(call.OrderBy) > 0 {
				st.strElems = append(st.strElems, hexVal)
				st.strDelims = append(st.strDelims, delimHex)
				st.strElemKeys = append(st.strElemKeys, evalAggOrderByKeys(call.OrderBy, slot, o.ctx))
				st.hasValue = true
				break
			}
			if !st.hasValue {
				st.strResult = hexVal
				st.hasValue = true
			} else {
				st.strResult += delimHex + hexVal
			}
			break
		}
		// Text string_agg: evaluate the delimiter from Arg2.
		delim := ""
		if call.Arg2 != nil {
			dv, derr := evalExprSlot(call.Arg2, slot, o.ctx)
			if derr != nil {
				break
			}
			if !dv.IsNull() {
				delim = formatDatumDateStyle(dv, o.ctx)
			}
		}
		sv := formatDatumDateStyle(arg, o.ctx)
		// With the aggregate's own ORDER BY, concatenation must wait for the
		// sort (M0125-0019) — see the strElems comment on aggRuntime.
		if len(call.OrderBy) > 0 {
			st.strElems = append(st.strElems, sv)
			st.strDelims = append(st.strDelims, delim)
			st.strElemKeys = append(st.strElemKeys, evalAggOrderByKeys(call.OrderBy, slot, o.ctx))
			st.hasValue = true
			break
		}
		if !st.hasValue {
			st.strResult = sv
			st.hasValue = true
		} else {
			st.strResult += delim + sv
		}
	case "array_agg":
		// array_agg(expr [ORDER BY sort_list]) — accumulate per-row elements.
		// NULLs are included as array elements (PostgreSQL semantics).
		// Elements are stored as their Format() string (DateStyle-aware for
		// KindTime); finishAgg wraps them in PG's text-array literal syntax.
		// Numeric kinds (int, numeric, oid) and string kinds (text, char,
		// varchar) all round-trip through Format() identically to how a
		// SELECT would print them.
		elemStr := ""
		isNull := arg.IsNull()
		if !isNull {
			elemStr = formatDatumDateStyle(arg, o.ctx)
		}
		st.arrayElems = append(st.arrayElems, elemStr)
		st.arrayElemNull = append(st.arrayElemNull, isNull)
		// Evaluate ORDER BY expressions for later sorting in finishAgg.
		if len(call.OrderBy) > 0 {
			keys := make([]Datum, 0, len(call.OrderBy))
			for _, sk := range call.OrderBy {
				kv, kerr := evalExprSlot(sk.Expr, slot, o.ctx)
				if kerr != nil {
					kv = NullDatum
				}
				keys = append(keys, kv.MaterializeArena())
			}
			st.arrayElemKeys = append(st.arrayElemKeys, keys)
		}
		st.hasValue = true
	case "any_value":
		// any_value(x) — return the first non-null value seen.
		if !st.hasValue && !arg.IsNull() {
			st.value = arg.MaterializeArena()
			st.hasValue = true
		}
	default:
		// User-defined aggregates (CREATE AGGREGATE): call sfunc on each row.
		if call.UserAgg != nil {
			// For DISTINCT or ORDER BY aggregates, accumulate all arg vectors for
			// deduplication and/or sorting in finishAgg. M0097-0035.
			// Non-DISTINCT with ORDER BY also needs row accumulation so rows can
			// be sorted before sfunc calls (PostgreSQL semantics). M0097-0113.
			if call.Distinct || len(call.OrderBy) > 0 {
				// Build row: [sortKey0, ..., arg0, arg1, ...extraArgs].
				// Sort keys first (for ORDER BY), then arg values for sfunc. M0097-0035.
				nSortKeys := len(call.OrderBy)
				row := make([]Datum, 0, nSortKeys+1+1+len(call.ExtraArgs))
				// Evaluate sort keys first.
				for _, sk := range call.OrderBy {
					kv, _ := evalExprSlot(sk.Expr, slot, o.ctx)
					row = append(row, kv.MaterializeArena())
				}
				// Then arg values.
				row = append(row, arg.MaterializeArena())
				// Arg2
				if call.Arg2 != nil {
					a2, a2err := evalExprSlot(call.Arg2, slot, o.ctx)
					if a2err == nil {
						row = append(row, a2.MaterializeArena())
					}
				}
				// ExtraArgs
				for _, ea := range call.ExtraArgs {
					ev, everr := evalExprSlot(ea, slot, o.ctx)
					if everr == nil {
						row = append(row, ev.MaterializeArena())
					}
				}
				st.distinctUserAggRows = append(st.distinctUserAggRows, row)
				st.hasValue = true
				return nil
			}
			ua := call.UserAgg
			// Initialize state from InitCond on first call.
			if !st.userStateSet {
				st.userState = userAggInitState(ua)
				st.userStateSet = true
			}
			// Build args: [state, arg1, arg2, ...extraArgs].
			sfuncArgs := []Datum{st.userState}
			if ua.Variadic {
				// For variadic aggregates, bundle all input args into a single array
				// (the sfunc expects state + VARIADIC anyarray). M0097-0117.
				var elems []string
				elems = append(elems, formatDatumDateStyle(arg, o.ctx))
				if call.Arg2 != nil {
					a2, a2err := evalExprSlot(call.Arg2, slot, o.ctx)
					if a2err == nil {
						elems = append(elems, formatDatumDateStyle(a2, o.ctx))
					}
				}
				for _, ea := range call.ExtraArgs {
					ev, everr := evalExprSlot(ea, slot, o.ctx)
					if everr == nil {
						elems = append(elems, formatDatumDateStyle(ev, o.ctx))
					}
				}
				sfuncArgs = append(sfuncArgs, NewStringDatum(formatTextArray(elems)))
			} else {
				sfuncArgs = append(sfuncArgs, arg)
				// Evaluate optional second argument.
				if call.Arg2 != nil {
					arg2, a2err := evalExprSlot(call.Arg2, slot, o.ctx)
					if a2err == nil {
						sfuncArgs = append(sfuncArgs, arg2)
					}
				}
				// Evaluate any extra arguments beyond the second (3-arg+ user aggregates).
				for _, ea := range call.ExtraArgs {
					ev, everr := evalExprSlot(ea, slot, o.ctx)
					if everr == nil {
						sfuncArgs = append(sfuncArgs, ev)
					}
				}
			}
			newState, serr := executeSFuncCall(ua.SFunc, sfuncArgs, o.ctx)
			if sfuncRaised(serr) {
				return serr
			}
			if serr == nil {
				st.userState = newState
			}
			st.hasValue = true
			return nil
		}
		// Variance/stddev aggregates: accumulate float sum and sum of squares.
		// M0097-0007.
		switch strings.ToLower(call.Name) {
		case "var_pop", "var_samp", "stddev_pop", "stddev_samp", "stddev", "variance":
			if !arg.IsNull() {
				// For integer inputs, use exact big.Int arithmetic (matching PG int4_accum/int8_accum).
				inputIsInt := func() bool {
					switch strings.ToLower(call.InputType.Name) {
					case "int2", "int4", "int8", "int", "integer", "smallint", "bigint",
						"serial", "bigserial", "smallserial":
						return true
					}
					return false
				}
				if arg.Kind == KindInt && inputIsInt() {
					if !st.intExact {
						st.intExact = true
						st.intSx = new(big.Int)
						st.intSxx = new(big.Int)
					}
					v := big.NewInt(arg.Int)
					st.intSx.Add(st.intSx, v)
					st.intSxx.Add(st.intSxx, new(big.Int).Mul(v, v))
					st.count++
					break
				}
				// For pure numeric inputs (not float4/float8 cast), use exact rational
				// arithmetic matching PG's numeric_accum path. M0097-0125.
				if arg.Kind == KindNumeric && !isFloat4TypeName(call.InputType.Name) &&
					strings.ToLower(call.InputType.Name) != "float4" &&
					strings.ToLower(call.InputType.Name) != "float8" &&
					strings.ToLower(call.InputType.Name) != "real" &&
					strings.ToLower(call.InputType.Name) != "double precision" {
					if st.numericSx == nil {
						st.numericExact = true
						st.numericSx = new(big.Rat)
						st.numericSxx = new(big.Rat)
					}
					numStr := formatNumeric(arg.NumericMantissaValue(), arg.NumericScaleValue())
					x := new(big.Rat)
					if _, ok := x.SetString(numStr); ok {
						st.numericSx.Add(st.numericSx, x)
						x2 := new(big.Rat).Mul(x, x)
						st.numericSxx.Add(st.numericSxx, x2)
					}
					st.count++
					break
				}
				var f float64
				switch arg.Kind {
				case KindInt:
					f = float64(arg.Int)
				case KindNumeric:
					f, _ = strconv.ParseFloat(formatNumeric(arg.NumericMantissaValue(), arg.NumericScaleValue()), 64)
				case KindString:
					f, _ = strconv.ParseFloat(arg.StringValue(), 64)
				}
				// For float4 input, round through float32 to match PG's float4_accum
				// semantics (PG stores float4 as 4-byte IEEE 754; accumulation uses the
				// float32-precision value, not the original decimal string). M0097-0115.
				if isFloat4TypeName(call.InputType.Name) {
					f = float64(float32(f))
				}
				// Youngs-Cramer algorithm: numerically stable for large-offset inputs.
				// tmp = x*N - Sx avoids subtracting two large nearly-equal values.
				// NaN/Inf propagates through the computation; we mark floatM2 = NaN
				// so the final result is NaN regardless of count (matches PG behavior).
				st.count++
				st.floatSx += f
				if math.IsNaN(f) || math.IsInf(f, 0) {
					st.floatM2 = math.NaN()
				} else if st.count > 1 && !math.IsNaN(st.floatM2) {
					tmp := f*float64(st.count) - st.floatSx
					st.floatM2 += tmp * tmp / (float64(st.count) * float64(st.count-1))
				}
			}
		case "regr_count", "regr_sxx", "regr_syy", "regr_sxy",
			"regr_avgx", "regr_avgy", "regr_r2", "regr_slope", "regr_intercept",
			"covar_pop", "covar_samp", "corr":
			// Two-argument regression aggregates. First arg is y (dependent),
			// second arg is x (independent). M0097-0020.
			if call.Arg2 == nil {
				break
			}
			arg2, a2err := evalExprSlot(call.Arg2, slot, o.ctx)
			if a2err != nil || arg2.IsNull() {
				break // skip row if either arg is NULL
			}
			y := aggDatumToFloat64(arg)
			x := aggDatumToFloat64(arg2)
			// For float4 input, round through float32 to match PG's float4_accum. M0097-0115.
			if isFloat4TypeName(call.InputType.Name) {
				y = float64(float32(y))
				x = float64(float32(x))
			}
			st.regrN++
			st.regrSumX += x
			st.regrSumY += y
			st.regrSumXX += x * x
			st.regrSumXY += x * y
			st.regrSumYY += y * y
		default:
			st.count++
			if arg.Kind == KindInt {
				st.sum += arg.Int
			}
		}
	}
	return nil
}

// evalAggOrderByKeys evaluates an aggregate call's own ORDER BY expressions
// against the current input row and materialises them, so finishAgg can sort
// the accumulated pieces later. A key that fails to evaluate becomes NULL
// rather than aborting the aggregate — the pre-existing array_agg behaviour
// this was factored out of.
func evalAggOrderByKeys(orderBy []planner.SortKey, slot TupleSlot, ctx *Context) []Datum {
	if len(orderBy) == 0 {
		return nil
	}
	keys := make([]Datum, 0, len(orderBy))
	for _, sk := range orderBy {
		kv, err := evalExprSlot(sk.Expr, slot, ctx)
		if err != nil {
			kv = NullDatum
		}
		keys = append(keys, kv.MaterializeArena())
	}
	return keys
}

// aggOrderBySortedIdx returns the permutation that puts the accumulated
// per-row key tuples into the aggregate's ORDER BY order. PostgreSQL performs
// this sort inside the aggregate, before the transition function runs
// (postgres/src/backend/executor/nodeAgg.c, process_ordered_aggregate_single),
// so it decides the aggregate's value and not merely its presentation.
//
// The sort is STABLE: rows whose keys compare equal keep arrival order, which
// is what PG's tuplesort gives for an aggregate whose sort keys do not fully
// determine the order.
//
// Shared by array_agg and string_agg — the two ordering-sensitive built-ins in
// finishAgg's switch. They diverged once already (string_agg simply ignored
// ORDER BY, M0125-0019); one comparator keeps them from diverging again.
func aggOrderBySortedIdx(keys [][]Datum, orderBy []planner.SortKey) []int {
	idx := make([]int, len(keys))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ka, kb := keys[idx[a]], keys[idx[b]]
		for ki := range ka {
			if ki >= len(kb) {
				break
			}
			akNull, bkNull := ka[ki].IsNull(), kb[ki].IsNull()
			if akNull && bkNull {
				continue
			}
			// NullsFirst is already resolved by the planner to PG's
			// defaults (ASC→NULLS LAST, DESC→NULLS FIRST) via
			// sortByNullsFirst, so it is read verbatim here.
			nullsFirst := false
			desc := false
			if ki < len(orderBy) {
				nullsFirst = orderBy[ki].NullsFirst
				desc = orderBy[ki].Desc
			}
			if akNull {
				return nullsFirst
			}
			if bkNull {
				return !nullsFirst
			}
			cmp, err := compareDatum(ka[ki], kb[ki], 0)
			if err != nil || cmp == 0 {
				continue
			}
			if desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
	return idx
}

// aggDatumToFloat64 converts any numeric datum to float64 for aggregate computation.
// isFloat4TypeName returns true for float4/real (single-precision) types.
// Used to apply float32 rounding for PG-compatible float4 aggregate semantics.
// withinGroupTupleLT returns true if row < directArgs in the hypothetical-set ordering.
// It performs lexicographic comparison of the row's sort-key tuple against directArgs
// respecting each sort key's ASC/DESC direction and NULL handling.
func withinGroupTupleLT(row []Datum, directArgs []Datum, sortKeys []planner.SortKey) bool {
	n := len(sortKeys)
	if n > len(row) {
		n = len(row)
	}
	if n > len(directArgs) {
		n = len(directArgs)
	}
	for i := 0; i < n; i++ {
		ri, di := row[i], directArgs[i]
		rNull, dNull := ri.IsNull(), di.IsNull()
		if rNull && dNull {
			continue // equal, move to next key
		}
		sk := sortKeys[i]
		nullsFirst := sk.NullsFirst
		// For DESC: default nulls FIRST; for ASC: default nulls LAST.
		if rNull {
			// row is NULL: comes before non-NULL if nullsFirst
			return nullsFirst
		}
		if dNull {
			// direct arg is NULL: row is non-NULL, comes after NULL if nullsFirst
			return !nullsFirst
		}
		cmp, err := compareDatum(ri, di, 0)
		if err != nil {
			return false
		}
		if cmp == 0 {
			continue // equal on this key, check next
		}
		if sk.Desc {
			return cmp > 0 // DESC: row > direct means row sorts BEFORE direct (is "less" in rank order)
		}
		return cmp < 0
	}
	return false // equal on all keys: not strictly less than
}

// compareDatumWithNullsFirst compares two Datums respecting nullsFirst and desc flags.
// Returns negative if a < b, zero if equal, positive if a > b in the sort order.
func compareDatumWithNullsFirst(a, b Datum, nullsFirst bool, desc bool) (int, error) {
	aNull, bNull := a.IsNull(), b.IsNull()
	if aNull && bNull {
		return 0, nil
	}
	if aNull {
		if nullsFirst {
			return -1, nil
		}
		return 1, nil
	}
	if bNull {
		if nullsFirst {
			return 1, nil
		}
		return -1, nil
	}
	cmp, err := compareDatum(a, b, 0)
	if err != nil {
		return 0, err
	}
	if desc {
		return -cmp, nil
	}
	return cmp, nil
}

func isFloat4TypeName(name string) bool {
	switch strings.ToLower(name) {
	case "float4", "real":
		return true
	default:
		return false
	}
}

// exactIntVariance computes variance/stddev for integer accumulators using exact big.Int arithmetic.
// Matching PostgreSQL's int4_accum / numeric_var_samp / numeric_var_pop path.
// isSample: true=var_samp/stddev_samp, false=var_pop/stddev_pop.
// isSqrt: true=stddev output, false=variance output.
// exactNumericVariance computes var_pop/var_samp/stddev for exact rational accumulators.
// Matches PG's numeric_accum + numeric_var_pop/var_samp path for numeric inputs. M0097-0125.
func exactNumericVariance(sx, sxx *big.Rat, n int64, isSample, isSqrt bool) Datum {
	bigN := new(big.Rat).SetInt64(n)
	// numerator = N * Σxx - (Σx)²
	numer := new(big.Rat).Mul(bigN, sxx)
	sxSq := new(big.Rat).Mul(sx, sx)
	numer.Sub(numer, sxSq)
	// denominator = N² (pop) or N*(N-1) (samp)
	var denom *big.Rat
	if isSample {
		denom = new(big.Rat).Mul(bigN, new(big.Rat).SetInt64(n-1))
	} else {
		denom = new(big.Rat).Mul(bigN, bigN)
	}
	if denom.Sign() == 0 {
		return NullDatum
	}
	result := new(big.Rat).Quo(numer, denom)
	if isSqrt {
		f64, _ := result.Float64()
		if f64 <= 0 {
			// Variance is 0 (or negative due to floating-point artifacts): stddev = 0.
			return NewStringDatum("0")
		}
		prec := uint(128)
		ratFloat := new(big.Float).SetPrec(prec).SetRat(result)
		seed := new(big.Float).SetPrec(prec).SetFloat64(math.Sqrt(f64))
		half := new(big.Float).SetPrec(prec).SetFloat64(0.5)
		for i := 0; i < 15; i++ {
			if seed.Sign() == 0 {
				break
			}
			div := new(big.Float).SetPrec(prec).Quo(ratFloat, seed)
			seed.Mul(half, new(big.Float).SetPrec(prec).Add(seed, div))
		}
		// PG uses 15 significant digits for numeric stddev output (extra_float_digits=0).
		s := seed.Text('g', 15)
		if strings.Contains(s, ".") && !strings.Contains(s, "e") {
			s = strings.TrimRight(s, "0")
			s = strings.TrimRight(s, ".")
		}
		return NewStringDatum(s)
	}
	return NewStringDatum(formatBigRatDecimal(result, 12))
}

func exactIntVariance(sx, sxx *big.Int, n int64, isSample, isSqrt bool) Datum {
	bigN := big.NewInt(n)
	// numerator = N * Sxx - Sx²
	num := new(big.Int).Sub(new(big.Int).Mul(bigN, sxx), new(big.Int).Mul(sx, sx))
	// denominator = N² (pop) or N*(N-1) (samp)
	var den *big.Int
	if isSample {
		den = new(big.Int).Mul(bigN, big.NewInt(n-1))
	} else {
		den = new(big.Int).Mul(bigN, bigN)
	}
	if den.Sign() == 0 {
		return NullDatum
	}
	// Use exact big.Rat arithmetic, then format as a decimal string.
	// Matching PostgreSQL numeric display: up to 18 decimal places, trailing zeros stripped.
	rat := new(big.Rat).SetFrac(num, den)
	if isSqrt {
		// For stddev, compute sqrt via high-precision float.
		f64, _ := rat.Float64()
		if f64 <= 0 {
			// Variance is exactly 0 (all inputs equal) or negative due to
			// floating-point artifacts: stddev = 0, matching PostgreSQL.
			//
			// Without this guard the Newton seed below is sqrt(0) = 0 and the
			// first iteration computes big.Float Quo(0, 0), which PANICS
			// ("division of zero by zero"). The panic escaped to serveConn and
			// dropped the connection — TPC-DS Q39's stddev_samp over a
			// (warehouse, item, month) inventory group whose quantity never
			// changes hit it on every run (the long-unexplained RC-4
			// "connection lost"). The numeric sibling exactNumericVariance
			// already had exactly this guard; the int path had lost it —
			// sibling paths must agree.
			return NewStringDatum("0")
		}
		// Newton-Raphson sqrt with 128-bit precision.
		prec := uint(128)
		ratFloat := new(big.Float).SetPrec(prec).SetRat(rat)
		seed := new(big.Float).SetPrec(prec).SetFloat64(math.Sqrt(f64))
		half := new(big.Float).SetPrec(prec).SetFloat64(0.5)
		for i := 0; i < 15; i++ {
			if seed.Sign() == 0 {
				break
			}
			div := new(big.Float).SetPrec(prec).Quo(ratFloat, seed)
			seed.Mul(half, new(big.Float).SetPrec(prec).Add(seed, div))
		}
		// Format with 18 significant digits matching PostgreSQL stddev output.
		s := seed.Text('g', 18)
		if strings.Contains(s, ".") && !strings.Contains(s, "e") {
			s = strings.TrimRight(s, "0")
			s = strings.TrimRight(s, ".")
		}
		return NewStringDatum(s)
	}
	// Format rational as decimal with up to 12 decimal places with round-half-up, then
	// strip trailing zeros. Matches PostgreSQL's numeric_poly_var_samp output scale
	// for integer inputs (consistently 12 decimal places across test cases).
	s := formatBigRatDecimal(rat, 12)
	return NewStringDatum(s)
}

// formatBigRatDecimal formats a big.Rat as a decimal string with up to maxDecimals decimal
// places, stripping trailing zeros and unnecessary decimal point.
func formatBigRatDecimal(r *big.Rat, maxDecimals int) string {
	if r.Sign() == 0 {
		return "0"
	}
	neg := r.Sign() < 0
	num := new(big.Int).Abs(r.Num())
	den := new(big.Int).Set(r.Denom())

	// Integer part
	intPart := new(big.Int)
	rem := new(big.Int)
	intPart.DivMod(num, den, rem)

	var sb strings.Builder
	if neg {
		sb.WriteByte('-')
	}
	sb.WriteString(intPart.String())
	if rem.Sign() == 0 || maxDecimals == 0 {
		return sb.String()
	}

	sb.WriteByte('.')
	ten := big.NewInt(10)
	digits := make([]byte, maxDecimals)
	for i := 0; i < maxDecimals; i++ {
		rem.Mul(rem, ten)
		d := new(big.Int)
		d.DivMod(rem, den, rem) // d = digit, rem = new remainder
		digits[i] = byte('0' + d.Int64())
		if rem.Sign() == 0 {
			// Exact: trim trailing slice and write
			sb.Write(digits[:i+1])
			return sb.String()
		}
	}
	// Write all digits, trimming trailing zeros
	// Round-half-up: look at the next digit to decide whether to round up.
	rem.Mul(rem, ten)
	nextD := new(big.Int)
	nextD.Div(rem, den)
	if nextD.Int64() >= 5 {
		for i := maxDecimals - 1; i >= 0; i-- {
			digits[i]++
			if digits[i] <= '9' {
				break
			}
			digits[i] = '0'
			if i == 0 {
				// Carry into integer part
				newInt := new(big.Int).Add(intPart, big.NewInt(1))
				s := sb.String()
				// Find the '.' and rebuild
				dotPos := strings.LastIndex(s, ".")
				sb.Reset()
				if neg {
					sb.WriteByte('-')
				}
				sb.WriteString(newInt.String())
				sb.WriteByte('.')
				_ = dotPos
			}
		}
	}
	end := maxDecimals
	for end > 0 && digits[end-1] == '0' {
		end--
	}
	if end == 0 {
		s := sb.String()
		return strings.TrimRight(s, ".")
	}
	sb.Write(digits[:end])
	return sb.String()
}

func aggDatumToFloat64(d Datum) float64 {
	switch d.Kind {
	case KindInt:
		return float64(d.Int)
	case KindNumeric:
		f, _ := strconv.ParseFloat(formatNumeric(d.NumericMantissaValue(), d.NumericScaleValue()), 64)
		return f
	case KindString:
		f, _ := strconv.ParseFloat(d.StringValue(), 64)
		return f
	}
	return 0
}

// finishAgg finalizes one accumulated aggregate.
//
// It returns an error only for a user-defined aggregate whose state, combine or
// final function was invoked and RAISED (M0125-0025); PG aborts the statement in
// that case, so the error must reach the client rather than be turned into a
// stale state or a NULL. Built-in finalization cannot fail, which is why the
// bulk of the work lives in finishBuiltinAgg with no error channel at all.
func (o *aggregateOp) finishAgg(st aggRuntime, call planner.AggregateCall) (Datum, error) {
	// Handle ordered-set aggregates (WITHIN GROUP) before regular handling. M0097-0035.
	if call.WithinGroup {
		return finishWithinGroupAgg(st, call, o.ctx), nil
	}
	// Handle user-defined aggregates first.
	if call.UserAgg != nil {
		ua := call.UserAgg
		// For DISTINCT or ORDER BY user-defined aggregates: deduplicate and/or sort,
		// then call sfunc in the resulting order. M0097-0035 / M0097-0113.
		if (call.Distinct || len(call.OrderBy) > 0) && len(st.distinctUserAggRows) > 0 {
			nSortKeys := len(call.OrderBy)
			// Deduplicate by all arg values (rows[nSortKeys:]) when DISTINCT is requested.
			var deduped [][]Datum
			if call.Distinct {
				seen := map[string]struct{}{}
				for _, row := range st.distinctUserAggRows {
					argSlice := row[nSortKeys:]
					var keyParts []string
					for _, d := range argSlice {
						keyParts = append(keyParts, datumKey(d))
					}
					k := strings.Join(keyParts, "	")
					if _, ok := seen[k]; ok {
						continue
					}
					seen[k] = struct{}{}
					deduped = append(deduped, row)
				}
			} else {
				// No dedup: use all accumulated rows in order (ORDER BY only).
				deduped = st.distinctUserAggRows
			}
			// Sort by ORDER BY sort keys when present. When no ORDER BY is given,
			// sort by the argument values (PostgreSQL's default for DISTINCT
			// aggregates — ensures deterministic, reproducible order). M0097-0035.
			sort.SliceStable(deduped, func(i, j int) bool {
				if nSortKeys > 0 {
					// User-specified ORDER BY columns come first.
					for ki := 0; ki < nSortKeys; ki++ {
						ai, bi := deduped[i][ki], deduped[j][ki]
						aNull, bNull := ai.IsNull(), bi.IsNull()
						if aNull && bNull {
							continue
						}
						nullsFirst := call.OrderBy[ki].NullsFirst
						if aNull {
							return nullsFirst
						}
						if bNull {
							return !nullsFirst
						}
						cmp, err := compareDatum(ai, bi, 0)
						if err != nil || cmp == 0 {
							continue
						}
						if call.OrderBy[ki].Desc {
							return cmp > 0
						}
						return cmp < 0
					}
					return false
				}
				// No ORDER BY: sort by arg values for deterministic DISTINCT order.
				argI := deduped[i][nSortKeys:]
				argJ := deduped[j][nSortKeys:]
				for k := 0; k < len(argI) && k < len(argJ); k++ {
					ai, bj := argI[k], argJ[k]
					aNull, bNull := ai.IsNull(), bj.IsNull()
					if aNull && bNull {
						continue
					}
					if aNull {
						return false // NULLs last
					}
					if bNull {
						return true
					}
					cmp, err := compareDatum(ai, bj, 0)
					if err != nil || cmp == 0 {
						continue
					}
					return cmp < 0
				}
				return false
			})
			// Initialize state and call sfunc for each deduped+sorted row.
			state := userAggInitState(ua)
			for _, row := range deduped {
				argSlice := row[nSortKeys:]
				sfuncArgs := make([]Datum, 0, 1+len(argSlice))
				sfuncArgs = append(sfuncArgs, state)
				sfuncArgs = append(sfuncArgs, argSlice...)
				newState, serr := executeSFuncCall(ua.SFunc, sfuncArgs, o.ctx)
				if sfuncRaised(serr) {
					return NullDatum, serr
				}
				if serr == nil {
					state = newState
				}
			}
			if ua.FinalFunc == "" {
				return state, nil
			}
			result, ferr := executeSFuncCall(ua.FinalFunc, []Datum{state}, o.ctx)
			if sfuncRaised(ferr) {
				return NullDatum, ferr
			}
			if ferr != nil {
				return NullDatum, nil
			}
			return result, nil
		}
		if !st.hasValue {
			return NullDatum, nil
		}
		state := st.userState
		// Apply CombineFunc when defined: combinefunc(NULL, partial_state).
		// Even in non-parallel mode, PG calls the combine function once to merge
		// the NULL initial combine-state with the single partial state. A STRICT
		// combinefunc with a NULL first arg returns NULL (the balk aggregate pattern). M0097-0122.
		if ua.CombineFunc != "" {
			combined, cerr := executeSFuncCall(ua.CombineFunc, []Datum{NullDatum, state}, o.ctx)
			if sfuncRaised(cerr) {
				return NullDatum, cerr
			}
			if cerr == nil {
				state = combined
			}
		}
		if ua.FinalFunc == "" {
			return state, nil
		}
		// Call the final function.
		result, ferr := executeSFuncCall(ua.FinalFunc, []Datum{state}, o.ctx)
		if sfuncRaised(ferr) {
			return NullDatum, ferr
		}
		if ferr != nil {
			return NullDatum, nil
		}
		return result, nil
	}
	return o.finishBuiltinAgg(st, call), nil
}

// finishBuiltinAgg finalizes a built-in aggregate. Split out of finishAgg by
// M0125-0025 so that adding the error channel the user-defined path needs did
// not have to touch this body's ~100 returns, none of which can fail.
func (o *aggregateOp) finishBuiltinAgg(st aggRuntime, call planner.AggregateCall) Datum {
	switch strings.ToLower(call.Name) {
	case "count":
		return Datum{Kind: KindInt, Int: st.count}
	case "sum":
		if !st.hasValue {
			return NullDatum
		}
		// IEEE 754 special values from float4/float8 columns take precedence.
		// M0097-0053.
		switch st.floatSpecial {
		case floatSpecialNaN:
			return NewStringDatum("NaN")
		case floatSpecialPosInf:
			return NewStringDatum("Infinity")
		case floatSpecialNegInf:
			return NewStringDatum("-Infinity")
		}
		if st.numericSum.Kind == KindNumeric {
			// For float4 input, PostgreSQL uses float4 as the transition type
			// (float4pl accumulation with intermediate float32 rounding). Simulate
			// this by casting the final sum through float32 and formatting with
			// float4 precision (6 significant digits). M0097-0115.
			if isFloat4TypeName(call.InputType.Name) {
				fsum := aggDatumToFloat64(st.numericSum)
				return NewStringDatum(strconv.FormatFloat(float64(float32(fsum)), 'g', 6, 32))
			}
			// For float8 input, format with 15 significant digits (PostgreSQL's
			// float8out format) to avoid spurious trailing-digit differences. M0097-0035.
			switch strings.ToLower(call.InputType.Name) {
			case "float8", "double precision", "double", "float":
				fsum := aggDatumToFloat64(st.numericSum)
				return NewStringDatum(strconv.FormatFloat(fsum, 'g', 15, 64))
			}
			return st.numericSum
		}
		return Datum{Kind: KindInt, Int: st.sum}
	case "avg":
		if st.count == 0 {
			return NullDatum
		}
		// IEEE 754 special values from float4/float8 columns. M0097-0053.
		switch st.floatSpecial {
		case floatSpecialNaN:
			return NewStringDatum("NaN")
		case floatSpecialPosInf:
			return NewStringDatum("Infinity")
		case floatSpecialNegInf:
			return NewStringDatum("-Infinity")
		}
		// avg(float4/float8) returns float8 — use float64 division and format
		// with %.15g to match PostgreSQL's float8out. M0097-0020.
		if strings.EqualFold(call.Type.Name, "float8") || strings.EqualFold(call.Type.Name, "float4") {
			var fsum float64
			if st.numericSum.Kind == KindNumeric {
				fsum = aggDatumToFloat64(st.numericSum)
			} else {
				fsum = float64(st.sum)
			}
			return NewStringDatum(strconv.FormatFloat(fsum/float64(st.count), 'g', 15, 64))
		}
		if st.numericSum.Kind == KindNumeric {
			d, err := numericDiv(st.numericSum, numericFromInt(st.count), call.Pos())
			if err != nil {
				return NullDatum
			}
			return d
		}
		// avg(integer types) must return numeric, not integer (PostgreSQL
		// behaviour: avg(int2/int4/int8) → numeric). M0097-0053.
		numSum := numericFromInt(st.sum)
		d, err := numericDiv(numSum, numericFromInt(st.count), call.Pos())
		if err != nil {
			return NullDatum
		}
		return d
	case "min", "max":
		if !st.hasValue {
			return NullDatum
		}
		return st.value
	case "bool_and", "every", "bool_or":
		if !st.hasValue {
			return NullDatum
		}
		return NewBoolDatum(st.boolResult)
	case "bit_and", "bit_or", "bit_xor":
		if !st.hasValue {
			return NullDatum
		}
		// BIT(n) type: format as zero-padded binary string.
		if strings.HasPrefix(st.strResult, "b") {
			if width, err := strconv.Atoi(st.strResult[1:]); err == nil && width > 0 {
				return NewStringDatum(fmt.Sprintf("%0*b", width, uint64(st.intResult)))
			}
		}
		return Datum{Kind: KindInt, Int: st.intResult}
	case "string_agg":
		if !st.hasValue {
			return NullDatum
		}
		out := st.strResult
		// The aggregate's own ORDER BY deferred concatenation to here
		// (M0125-0019). Each piece carries the delimiter it was collected
		// with, and PG emits the delimiter of the RIGHT-hand piece between
		// two neighbours — so both travel through the permutation together
		// and the first piece's delimiter is dropped.
		if len(st.strElems) > 0 {
			idx := aggOrderBySortedIdx(st.strElemKeys, call.OrderBy)
			var b strings.Builder
			for i, orig := range idx {
				if i > 0 && orig < len(st.strDelims) {
					b.WriteString(st.strDelims[orig])
				}
				b.WriteString(st.strElems[orig])
			}
			out = b.String()
		}
		// Bytea mode: st.boolResult=true means result is hex-encoded bytes.
		if st.boolResult {
			return NewStringDatum(`\x` + out)
		}
		return NewStringDatum(out)
	case "array_agg":
		if !st.hasValue {
			return NullDatum
		}
		// Sort elements by ORDER BY keys if present.
		if len(st.arrayElemKeys) == len(st.arrayElems) && len(st.arrayElemKeys) > 0 {
			idx := aggOrderBySortedIdx(st.arrayElemKeys, call.OrderBy)
			sortedElems := make([]string, len(st.arrayElems))
			sortedNulls := make([]bool, len(st.arrayElems))
			for i, origIdx := range idx {
				sortedElems[i] = st.arrayElems[origIdx]
				if origIdx < len(st.arrayElemNull) {
					sortedNulls[i] = st.arrayElemNull[origIdx]
				}
			}
			return NewStringDatum(formatTextArrayWithNulls(sortedElems, sortedNulls))
		}
		// array_agg(DISTINCT x) without ORDER BY: sort elements for deterministic output.
		// PostgreSQL returns distinct values in ascending order when no ORDER BY is given.
		if call.Distinct && len(call.OrderBy) == 0 && len(st.arrayElems) > 0 {
			sortedElems := make([]string, len(st.arrayElems))
			sortedNulls := make([]bool, len(st.arrayElemNull))
			copy(sortedElems, st.arrayElems)
			copy(sortedNulls, st.arrayElemNull)
			type elemWithNull struct {
				s      string
				isNull bool
			}
			pairs := make([]elemWithNull, len(sortedElems))
			for i := range sortedElems {
				pairs[i] = elemWithNull{sortedElems[i], i < len(sortedNulls) && sortedNulls[i]}
			}
			sort.SliceStable(pairs, func(a, b int) bool {
				if pairs[a].isNull && pairs[b].isNull {
					return false
				}
				if pairs[a].isNull {
					return false // NULLs last
				}
				if pairs[b].isNull {
					return true
				}
				return pairs[a].s < pairs[b].s
			})
			for i, p := range pairs {
				sortedElems[i] = p.s
				if i < len(sortedNulls) {
					sortedNulls[i] = p.isNull
				}
			}
			return NewStringDatum(formatTextArrayWithNulls(sortedElems, sortedNulls))
		}
		return NewStringDatum(formatTextArrayWithNulls(st.arrayElems, st.arrayElemNull))
	case "any_value":
		if !st.hasValue {
			return NullDatum
		}
		return st.value
	}
	// Variance/stddev aggregates. M0097-0007.
	switch strings.ToLower(call.Name) {
	case "var_pop":
		if st.count == 0 {
			return NullDatum
		}
		if st.intExact {
			return exactIntVariance(st.intSx, st.intSxx, st.count, false, false)
		}
		if st.numericExact {
			return exactNumericVariance(st.numericSx, st.numericSxx, st.count, false, false)
		}
		varPop := st.floatM2 / float64(st.count)
		return NewStringDatum(strconv.FormatFloat(varPop, 'g', 15, 64))
	case "variance", "var_samp":
		// In PostgreSQL, variance() = var_samp() (sample variance, divides by n-1).
		if st.count < 2 {
			return NullDatum
		}
		if st.intExact {
			return exactIntVariance(st.intSx, st.intSxx, st.count, true, false)
		}
		if st.numericExact {
			return exactNumericVariance(st.numericSx, st.numericSxx, st.count, true, false)
		}
		varSamp := st.floatM2 / float64(st.count-1)
		return NewStringDatum(strconv.FormatFloat(varSamp, 'g', 15, 64))
	case "stddev_pop":
		if st.count == 0 {
			return NullDatum
		}
		if st.intExact {
			return exactIntVariance(st.intSx, st.intSxx, st.count, false, true)
		}
		if st.numericExact {
			return exactNumericVariance(st.numericSx, st.numericSxx, st.count, false, true)
		}
		varPop := st.floatM2 / float64(st.count)
		if varPop < 0 {
			varPop = 0
		}
		return NewStringDatum(strconv.FormatFloat(math.Sqrt(varPop), 'g', 15, 64))
	case "stddev_samp", "stddev":
		if st.count < 2 {
			return NullDatum
		}
		if st.intExact {
			return exactIntVariance(st.intSx, st.intSxx, st.count, true, true)
		}
		if st.numericExact {
			return exactNumericVariance(st.numericSx, st.numericSxx, st.count, true, true)
		}
		varSamp := st.floatM2 / float64(st.count-1)
		if varSamp < 0 {
			varSamp = 0
		}
		return NewStringDatum(strconv.FormatFloat(math.Sqrt(varSamp), 'g', 15, 64))
	}

	// Regression aggregates. M0097-0020.
	switch strings.ToLower(call.Name) {
	case "regr_count":
		return NewIntDatum(st.regrN)
	case "regr_avgx":
		if st.regrN < 1 {
			return NullDatum
		}
		return NewStringDatum(strconv.FormatFloat(st.regrSumX/float64(st.regrN), 'g', 15, 64))
	case "regr_avgy":
		if st.regrN < 1 {
			return NullDatum
		}
		return NewStringDatum(strconv.FormatFloat(st.regrSumY/float64(st.regrN), 'g', 15, 64))
	case "regr_sxx":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(sxx, 'g', 15, 64))
	case "regr_syy":
		if st.regrN < 1 {
			return NullDatum
		}
		syy := st.regrSumYY - st.regrSumY*st.regrSumY/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(syy, 'g', 15, 64))
	case "regr_sxy":
		if st.regrN < 1 {
			return NullDatum
		}
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(sxy, 'g', 15, 64))
	case "covar_pop":
		if st.regrN < 1 {
			return NullDatum
		}
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(sxy/float64(st.regrN), 'g', 15, 64))
	case "covar_samp":
		if st.regrN < 2 {
			return NullDatum
		}
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(sxy/float64(st.regrN-1), 'g', 15, 64))
	case "regr_r2":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		syy := st.regrSumYY - st.regrSumY*st.regrSumY/float64(st.regrN)
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		if sxx == 0 {
			return NullDatum
		}
		r2 := (sxy * sxy) / (sxx * syy)
		return NewStringDatum(strconv.FormatFloat(r2, 'g', 15, 64))
	case "regr_slope":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		if sxx == 0 {
			return NullDatum
		}
		return NewStringDatum(strconv.FormatFloat(sxy/sxx, 'g', 15, 64))
	case "regr_intercept":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		if sxx == 0 {
			return NullDatum
		}
		slope := sxy / sxx
		intercept := (st.regrSumY - slope*st.regrSumX) / float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(intercept, 'g', 15, 64))
	case "corr":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		syy := st.regrSumYY - st.regrSumY*st.regrSumY/float64(st.regrN)
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		if sxx <= 0 || syy <= 0 {
			return NullDatum
		}
		return NewStringDatum(strconv.FormatFloat(sxy/math.Sqrt(sxx*syy), 'g', 15, 64))
	}
	return NullDatum
}

func (o *aggregateOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return asSlot(o.schema, row), nil
}

func (o *aggregateOp) Close() error {
	o.rows = nil
	o.ctx = nil
	o.idx = 0
	return o.child.Close()
}

func (o *aggregateOp) Schema() planner.Schema { return o.schema }

func drainRows(op Operator) ([]Row, error) {
	return drainRowsCtx(op, nil)
}

// drainRowsCtx drains all rows from op, checking ctx.Err() every 1000
// rows so a CancelRequest can interrupt long hash-join build phases.
//
// M0073-0004 retention boundary: when slots carry arena-backed Datums
// (KindStringArena / KindBytesArena), the cloneRowOwned helper deep-
// copies the arena bytes into owned []byte. Without this, the build-
// side hash tables would alias the source operator's per-page arena
// pages — invalidated on the next arena.Reset() (typically per-page
// in seqScan, per-Rescan in indexScan).
//
// Always copies the row slice and materializes arena-backed Datums before
// appending.  Producers (SeqScan, CTEScan, etc.) may reuse the same slot
// and row buffer across Next() calls, so the row slice must be independently
// owned.  MaterializeArena detaches arena-backed Datums onto fresh Buf
// allocations; the slice copy ensures non-arena Datums are safely owned too.
// (M0097-0058 CTE-left cross join fix.)
func drainRowsCtx(op Operator, ctx *Context) ([]Row, error) {
	rows := make([]Row, 0)
	n := 0
	for {
		if ctx != nil && ctx.Ctx != nil && n%1000 == 0 {
			if err := ctx.Ctx.Err(); err != nil {
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		slot, err := op.Next()
		if err == EOF {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		row := slotRow(slot)
		// Always make an independent copy: clone the slice AND
		// materialize any arena-backed Datums so each entry stays
		// valid regardless of the producer's slot reuse or arena
		// reset.  (M0097-0058 CTE-left cross join fix.)
		// Always make an independent copy: clone the slice AND materialize
		// any arena-backed Datums so each entry stays valid regardless of
		// the producer's slot reuse or arena reset.
		dup := make(Row, len(row))
		copy(dup, row)
		if rowHasArena(row) {
			dup = cloneRowOwned(dup)
		}
		rows = append(rows, dup)
		n++
	}
}

func drainRowsCtxCTID(op Operator, ctx *Context, scanLeaf currentTIDProvider) ([]Row, []joinRowCTID, error) {
	rows := make([]Row, 0)
	var ctids []joinRowCTID
	if scanLeaf != nil {
		ctids = make([]joinRowCTID, 0)
	}
	n := 0
	for {
		if ctx != nil && ctx.Ctx != nil && n%1000 == 0 {
			if err := ctx.Ctx.Err(); err != nil {
				return nil, nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		slot, err := op.Next()
		if err == EOF {
			return rows, ctids, nil
		}
		if err != nil {
			return nil, nil, err
		}
		row := slotRow(slot)
		// Always make an independent copy (see drainRowsCtx above).
		dup := make(Row, len(row))
		copy(dup, row)
		if rowHasArena(row) {
			dup = cloneRowOwned(dup)
		}
		rows = append(rows, dup)
		if scanLeaf != nil {
			rel, ptr, ok := scanLeaf.currentTID()
			ctids = append(ctids, joinRowCTID{rel: rel, ptr: ptr, hasCTID: ok})
		}
		n++
	}
}

// drainRowsCtxCTID drains op like drainRowsCtx and additionally captures the

func concatRows(a, b Row) Row {
	out := make(Row, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func nullRow(n int) Row {
	out := make(Row, n)
	for i := range out {
		out[i] = NullDatum
	}
	return out
}

// datumToInt64Key converts d to an int64 hash key without any allocation.
// Returns (key, true) when d is KindInt or a KindNumeric that normalises
// to an integer (scale == 0 after stripping trailing zeros). Returns
// (0, false) for NULL, bool, string, time, interval, and fractional
// numerics. Used by the MHJ int64 fast path (M0043-0003).
//
// The int64 canonical form matches canonicalNumericKey: KindInt(v) and
// KindNumeric{mantissa=v*10^n, scale=n} both produce the same int64
// after normalisation, preserving cross-kind hash equality.
func datumToInt64Key(d Datum) (int64, bool) {
	switch d.Kind {
	case KindInt:
		return d.Int, true
	case KindNumeric:
		if d.NumericBigValue() != nil {
			return 0, false // overflow lane: not representable as int64
		}
		m, s := d.NumericMantissaValue(), int(d.Scale)
		for s > 0 && m%10 == 0 {
			m /= 10
			s--
		}
		if s == 0 {
			return m, true
		}
		return 0, false // fractional numeric
	}
	return 0, false
}

func datumKey(d Datum) string {
	switch d.Kind {
	case KindNull:
		return "n"
	case KindBool:
		if d.BoolValue() {
			return "b:t"
		}
		return "b:f"
	case KindInt:
		// Cross-kind hash compatibility: integers and numerics
		// must hash equal when they represent the same value, so
		// `aid = $1` matches whether $1 lands as KindInt or as a
		// scale-0 KindNumeric. Normalise both to the same shape.
		return canonicalNumericKey(d.Int, 0)
	case KindNumeric:
		// R3-3: the big-mantissa lane must not go through
		// NumericMantissaValue(). For a flagBigNumeric datum that
		// accessor returns d.Int, which in this lane holds the mctx
		// (offset<<32 | len) encoding rather than the value — so two
		// equal big numerics stored at different offsets produced
		// DIFFERENT keys and a hash join silently dropped the pair
		// (measured: a 3-row equi-join on numerics past int64
		// returned 1 row). numericMant reads the correct mantissa
		// from either lane.
		if d.Flags&flagBigNumeric != 0 {
			return canonicalBigNumericKey(numericMant(d), int(d.Scale))
		}
		return canonicalNumericKey(d.NumericMantissaValue(), int(d.Scale))
	case KindString:
		return "s:" + d.StringValue()
	case KindBytes:
		return "x:" + string(d.BytesValue())
	case KindTime:
		var buf [20]byte
		b := append(buf[:0], 't', ':')
		b = strconv.AppendInt(b, d.TimeValue().UnixNano(), 10)
		return string(b)
	case KindInterval:
		var buf [48]byte
		b := append(buf[:0], 'v', ':')
		b = strconv.AppendInt(b, int64(d.IntervalMonthsValue()), 10)
		b = append(b, ':')
		b = strconv.AppendInt(b, int64(d.IntervalDaysValue()), 10)
		b = append(b, ':')
		b = strconv.AppendInt(b, d.IntervalMicrosValue(), 10)
		return string(b)
	}
	return fmt.Sprintf("k:%d", d.Kind)
}

// canonicalNumericKey produces a string identifier that's identical
// for two numerics that compare equal. `1` (m=1,s=0), `1.0`
// (m=10,s=1), and `1.00` (m=100,s=2) all canonicalise to the same
// key. Trailing zero pairs (one digit + one scale step) are stripped
// until either the scale reaches 0 or the trailing digit is non-zero.
// Negative-scale results are flagged as `e<N>` so `100` (m=100,s=0)
// and `100` (m=1,s=-2) — should the latter ever arise — both map
// to the same canonical form. v0 never produces negative scales,
// but the helper is kept robust.
func canonicalNumericKey(mantissa int64, scale int) string {
	// Special case: 0 at any scale collapses to a single value.
	if mantissa == 0 {
		return "m:0:0"
	}
	for scale > 0 && mantissa%10 == 0 {
		mantissa /= 10
		scale--
	}
	// Use strconv.AppendInt instead of fmt.Sprintf to avoid format-string
	// parsing overhead on the string-key hot path.
	var buf [32]byte
	b := append(buf[:0], 'm', ':')
	b = strconv.AppendInt(b, mantissa, 10)
	b = append(b, ':')
	b = strconv.AppendInt(b, int64(scale), 10)
	return string(b)
}

// canonicalBigNumericKey is canonicalNumericKey's big.Int lane. It
// applies the identical normalisation (strip trailing zero digit/scale
// pairs) so the two lanes AGREE: a value that fits int64 after
// stripping is handed back to canonicalNumericKey, which means
// `1.0000...` in the big lane and `1` in the fast lane produce one key.
// That convergence is what lets hashFamNumeric cover both lanes without
// splitting the family, and it mirrors numericCmp's aligned-mantissa
// equality — the relation compareEq actually uses.
//
// The big.Int allocations here are acceptable because this lane is cold:
// the int64 fast path above handles every normal-magnitude numeric.
func canonicalBigNumericKey(m *big.Int, scale int) string {
	if m.Sign() == 0 {
		return "m:0:0"
	}
	ten := big.NewInt(10)
	q, r := new(big.Int), new(big.Int)
	for scale > 0 {
		q.QuoRem(m, ten, r)
		if r.Sign() != 0 {
			break
		}
		m.Set(q)
		scale--
	}
	if m.IsInt64() {
		return canonicalNumericKey(m.Int64(), scale)
	}
	var b []byte
	b = append(b, 'm', ':')
	b = append(b, m.String()...)
	b = append(b, ':')
	b = strconv.AppendInt(b, int64(scale), 10)
	return string(b)
}

// userAggInitState returns the initial state Datum for a user-defined aggregate.
// An empty InitCond gives NullDatum; a numeric string like "0" gives NewIntDatum(0);
// an array literal like "{0,0}" or "{}" gives NewStringDatum(initcond).
func userAggInitState(ua *catalog.UserAggregate) Datum {
	ic := ua.InitCond
	if ic == "" {
		return NullDatum
	}
	// Try to parse as an integer.
	if n, err := strconv.ParseInt(ic, 10, 64); err == nil {
		return NewIntDatum(n)
	}
	// Otherwise treat as string (covers arrays, empty array, text, etc.).
	return NewStringDatum(ic)
}

// errSFuncNotFound is executeSFuncCall's "there was no routine to call"
// outcome. It must stay distinguishable from "a routine ran and raised",
// because the aggregate paths propagate the two in OPPOSITE directions
// (M0125-0025): a raise aborts the statement, a missing routine keeps the
// historical fall-through that lets an aggregate with no FINALFUNC, or one
// whose state/final function goopg models inline, finish normally.
//
// It Unwraps to its *ExecError so errors.As still reaches the 42883, but it is
// never propagated to a client by design — every call site swallows it — so a
// bare `err.(*ExecError)` assertion elsewhere cannot see it.
type errSFuncNotFound struct{ inner *ExecError }

func (e *errSFuncNotFound) Error() string { return e.inner.Error() }
func (e *errSFuncNotFound) Unwrap() error { return e.inner }

// sfuncRaised reports whether err came from a user-defined state, final or
// combine function that WAS found, invoked, and failed — the only
// executeSFuncCall failure that may reach the client.
//
// PG has no equivalent choice to make: advance_transition_function invokes the
// transition function through FunctionCallInvoke
// (postgres/src/backend/executor/nodeAgg.c), so an ereport(ERROR) inside it
// aborts the statement with the function's own SQLSTATE. goopg needs the
// distinction only because executeSFuncCall doubles as the lookup for the
// built-in state functions it models inline above.
func sfuncRaised(err error) bool {
	if err == nil {
		return false
	}
	var nf *errSFuncNotFound
	return !errors.As(err, &nf)
}

// executeSFuncCall invokes a named state-transition or final function for a
// user-defined aggregate.  Built-in SQL/PG functions are handled inline;
// user-defined SQL functions are executed via executeStoredRoutine.
//
// A non-nil error means one of two different things; use sfuncRaised to tell
// them apart before deciding whether to propagate.
func executeSFuncCall(funcName string, args []Datum, ctx *Context) (Datum, error) {
	switch strings.ToLower(funcName) {
	case "int8inc":
		// int8inc(int8) → int8+1: counts every row
		if len(args) >= 1 {
			n, _ := datumInt64(args[0])
			return NewIntDatum(n + 1), nil
		}
	case "int8inc_any":
		// int8inc_any(int8, any) → int8+1: counts rows ignoring second arg
		if len(args) >= 1 {
			n, _ := datumInt64(args[0])
			return NewIntDatum(n + 1), nil
		}
	case "int4pl", "int8pl":
		// int4pl/int8pl(int,int) → sum
		if len(args) == 2 {
			a, _ := datumInt64(args[0])
			b, _ := datumInt64(args[1])
			return NewIntDatum(a + b), nil
		}
	case "int2pl":
		if len(args) == 2 {
			a, _ := datumInt64(args[0])
			b, _ := datumInt64(args[1])
			return NewIntDatum(a + b), nil
		}
	case "int4_avg_accum":
		// int4_avg_accum(int8[], int4) → int8[] as {count, sum}
		if len(args) == 2 {
			state := parseTextArray(args[0].StringValue())
			cnt, sum := int64(0), int64(0)
			if len(state) == 2 {
				cnt, _ = strconv.ParseInt(state[0], 10, 64)
				sum, _ = strconv.ParseInt(state[1], 10, 64)
			}
			val, _ := datumInt64(args[1])
			return NewStringDatum(fmt.Sprintf("{%d,%d}", cnt+1, sum+val)), nil
		}
	case "int8_avg":
		// int8_avg(int8[]) → numeric: sum/count
		if len(args) == 1 {
			state := parseTextArray(args[0].StringValue())
			if len(state) == 2 {
				cnt, _ := strconv.ParseInt(state[0], 10, 64)
				sum, _ := strconv.ParseInt(state[1], 10, 64)
				if cnt == 0 {
					return NullDatum, nil
				}
				return NewStringDatum(formatAvgInt8(sum, cnt)), nil
			}
		}
	case "least_accum":
		// least_accum(int8, int8) → least($1, $2), strict (skip NULLs).
		// Only handle the scalar form here; the variadic form passes an array
		// string as args[1] and is handled by the SQL function path below.
		if len(args) == 2 && (args[1].Kind == KindInt || (args[1].Kind == KindString && len(args[1].StringValue()) > 0 && args[1].StringValue()[0] != '{')) {
			if args[0].IsNull() {
				return args[1], nil
			}
			if args[1].IsNull() {
				return args[0], nil
			}
			a, _ := datumInt64(args[0])
			b, _ := datumInt64(args[1])
			if b < a {
				return NewIntDatum(b), nil
			}
			return NewIntDatum(a), nil
		}
	}
	// Try user-defined routine from the catalog.
	rs := routineRegistry(ctx)
	if rs != nil {
		funcObjName := parser.ObjectName{Name: funcName}
		candidates := rs.LookupByName(funcObjName)
		if len(candidates) > 0 {
			// A routine of this name exists, so the outcome can no longer be
			// "does not exist": remember the FIRST error raised by a routine
			// that actually ran and report it if none succeeds (M0125-0025).
			// Candidates are matched by arity, not signature, so several may be
			// tried where PG resolves exactly one — keep trying after a failure
			// so every call that succeeds today still succeeds, and change only
			// the all-failed outcome, which used to be misreported as 42883.
			var raised error
			for _, r := range candidates {
				if len(r.ArgTypes) == len(args) {
					result, rerr := executeStoredRoutine(r, args, ctx, 0)
					if rerr == nil {
						return result, nil
					}
					if raised == nil {
						raised = rerr
					}
				}
			}
			// Fallback: try any candidate.
			result, rerr := executeStoredRoutine(candidates[0], args, ctx, 0)
			if rerr == nil {
				return result, nil
			}
			if raised == nil {
				raised = rerr
			}
			return NullDatum, raised
		}
	}
	return NullDatum, &errSFuncNotFound{&ExecError{Code: "42883", Message: fmt.Sprintf("aggregate state function %q does not exist", funcName)}}
}

// formatAvgInt8 formats sum/count as a PostgreSQL numeric with 16 decimal places.
func formatAvgInt8(sum, count int64) string {
	r := new(big.Rat).SetFrac(big.NewInt(sum), big.NewInt(count))
	return r.FloatString(16)
}

// finishWithinGroupAgg handles ordered-set aggregate finalization for
// WITHIN GROUP (ORDER BY ...) aggregate functions. M0097-0035.
//
// The st.withinGroupElems slice contains one []Datum per input row,
// where each entry holds the sort-key value(s) from WithinGroupOrderBy.
// For single-key cases (most common), each inner slice has one element.
func finishWithinGroupAgg(st aggRuntime, call planner.AggregateCall, ctx *Context) Datum {
	name := strings.ToLower(call.Name)
	elems := st.withinGroupElems
	if len(elems) == 0 {
		// Hypothetical-set aggregates return a defined value when there are 0 actual rows:
		// the hypothetical row is alone so rank=1, percent_rank=0, cume_dist=1.
		switch name {
		case "rank", "dense_rank":
			return NewIntDatum(1)
		case "percent_rank":
			return NewStringDatum("0")
		case "cume_dist":
			return NewStringDatum("1")
		}
		return NullDatum
	}

	// Sort the elements according to WithinGroupOrderBy direction.
	// Each elem is []Datum with one Datum per sort key.
	sortKeys := call.WithinGroupOrderBy
	sort.SliceStable(elems, func(i, j int) bool {
		for ki, sk := range sortKeys {
			if ki >= len(elems[i]) || ki >= len(elems[j]) {
				break
			}
			ai, bi := elems[i][ki], elems[j][ki]
			// NULLs: by default NULLs LAST for ASC, FIRST for DESC.
			aNull, bNull := ai.IsNull(), bi.IsNull()
			if aNull && bNull {
				continue
			}
			nullsFirst := sk.NullsFirst
			if aNull {
				return nullsFirst
			}
			if bNull {
				return !nullsFirst
			}
			cmp, err := compareDatum(ai, bi, 0)
			if err != nil || cmp == 0 {
				continue
			}
			if sk.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})

	// Extract the first sort-key values (the "ordered values").
	// For most ordered-set aggregates, only the first key is used as the value.
	orderedVals := make([]Datum, len(elems))
	for i, row := range elems {
		if len(row) > 0 {
			orderedVals[i] = row[0]
		} else {
			orderedVals[i] = NullDatum
		}
	}

	// User-defined ordered-set aggregates: delegate to built-in implementations
	// based on their final function name (e.g. test_rank uses rank_final → rank).
	// This handles aggregates created via CREATE AGGREGATE and renamed with ALTER AGGREGATE.
	if call.UserAgg != nil {
		ff := strings.ToLower(call.UserAgg.FinalFunc)
		var builtinName string
		switch ff {
		case "rank_final":
			builtinName = "rank"
		case "dense_rank_final":
			builtinName = "dense_rank"
		case "percent_rank_final":
			builtinName = "percent_rank"
		case "cume_dist_final":
			builtinName = "cume_dist"
		case "percentile_disc_final":
			builtinName = "percentile_disc"
		case "percentile_cont_final":
			builtinName = "percentile_cont"
		case "mode_final":
			builtinName = "mode"
		}
		if builtinName != "" {
			bc := call
			bc.Name = builtinName
			bc.UserAgg = nil
			return finishWithinGroupAgg(st, bc, ctx)
		}
	}

	switch name {
	case "percentile_cont":
		// Use direct arg stored during applyAgg from the first row of the group.
		if call.Arg == nil || !st.withinGroupDirectArgSet {
			return NullDatum
		}
		p := st.withinGroupDirectArg
		n := len(orderedVals)
		if n == 0 {
			return NullDatum
		}
		float4Input := isFloat4TypeName(call.WithinGroupKeyType.Name)
		// Array form: percentile_cont(array[p1,p2,...]) WITHIN GROUP (ORDER BY x)
		// returns an array of interpolated values. M0097-0035.
		if fracs, ok := tryParseFloatArray(p); ok {
			results := make([]string, len(fracs))
			for i, pf := range fracs {
				if math.IsNaN(pf) {
					results[i] = "NULL"
					continue
				}
				if pf < 0 || pf > 1 {
					results[i] = "NULL"
					continue
				}
				results[i] = percentileContOneFloat(orderedVals, n, pf, float4Input)
			}
			return NewStringDatum(formatTextArray(results))
		}
		pf := aggDatumToFloat64(p)
		if math.IsNaN(pf) || pf < 0 || pf > 1 {
			return NullDatum
		}
		return NewStringDatum(percentileContOneFloat(orderedVals, n, pf, float4Input))

	case "percentile_disc":
		// Use direct arg stored during applyAgg from the first row of the group.
		if call.Arg == nil || !st.withinGroupDirectArgSet {
			return NullDatum
		}
		p := st.withinGroupDirectArg
		n := len(orderedVals)
		if n == 0 {
			return NullDatum
		}
		// Array form: percentile_disc(array[p1,p2,...]) WITHIN GROUP (ORDER BY x)
		// returns an array of discrete values. 2D arrays preserve structure. M0097-0125.
		if rows, ok := tryParseFloat2DArray(p); ok {
			result2D := make([][]string, len(rows))
			for r, row := range rows {
				result2D[r] = make([]string, len(row))
				for c, pf := range row {
					if math.IsNaN(pf) || pf < 0 || pf > 1 {
						result2D[r][c] = "NULL"
						continue
					}
					d := percentileDiscOneFloat(orderedVals, n, pf)
					if d.IsNull() {
						result2D[r][c] = "NULL"
					} else {
						result2D[r][c] = formatDatumDateStyle(d, ctx)
					}
				}
			}
			return NewStringDatum(format2DTextArray(result2D))
		}
		if fracs, ok := tryParseFloatArray(p); ok {
			results := make([]string, len(fracs))
			for i, pf := range fracs {
				if math.IsNaN(pf) {
					results[i] = "NULL"
					continue
				}
				if pf < 0 || pf > 1 {
					results[i] = "NULL"
					continue
				}
				d := percentileDiscOneFloat(orderedVals, n, pf)
				if d.IsNull() {
					results[i] = "NULL"
				} else {
					results[i] = formatDatumDateStyle(d, ctx)
				}
			}
			return NewStringDatum(formatTextArray(results))
		}
		pf := aggDatumToFloat64(p)
		if math.IsNaN(pf) || pf < 0 || pf > 1 {
			return NullDatum
		}
		return percentileDiscOneFloat(orderedVals, n, pf)

	case "rank":
		// rank(v) WITHIN GROUP (ORDER BY x): count values strictly less than v, +1.
		if call.Arg == nil || !st.withinGroupDirectArgSet {
			return NullDatum
		}
		rank := int64(1)
		if len(st.withinGroupDirectArgs) > 1 {
			// Multi-arg rank: tuple comparison against all direct args.
			for _, row := range elems {
				if withinGroupTupleLT(row, st.withinGroupDirectArgs, call.WithinGroupOrderBy) {
					rank++
				}
			}
		} else {
			v := st.withinGroupDirectArg
			for _, val := range orderedVals {
				if val.IsNull() {
					continue
				}
				cmp, cerr := compareDatumWithNullsFirst(val, v, call.WithinGroupOrderBy[0].NullsFirst, call.WithinGroupOrderBy[0].Desc)
				if cerr != nil {
					continue
				}
				if cmp < 0 {
					rank++
				}
			}
		}
		return NewIntDatum(rank)

	case "dense_rank":
		// dense_rank(v) WITHIN GROUP (ORDER BY x): count distinct values strictly less than v, +1.
		if call.Arg == nil || !st.withinGroupDirectArgSet {
			return NullDatum
		}
		seen := map[string]struct{}{}
		if len(st.withinGroupDirectArgs) > 1 {
			// Multi-arg dense_rank: tuple comparison.
			for _, row := range elems {
				if withinGroupTupleLT(row, st.withinGroupDirectArgs, call.WithinGroupOrderBy) {
					key := ""
					for _, d := range row {
						key += datumKey(d) + "|"
					}
					seen[key] = struct{}{}
				}
			}
		} else {
			v := st.withinGroupDirectArg
			for _, val := range orderedVals {
				if val.IsNull() {
					continue
				}
				cmp, cerr := compareDatum(val, v, 0)
				if cerr != nil {
					continue
				}
				if cmp < 0 {
					seen[datumKey(val)] = struct{}{}
				}
			}
		}
		return NewIntDatum(int64(len(seen)) + 1)

	case "cume_dist":
		// cume_dist(v) WITHIN GROUP (ORDER BY x): fraction of rows <= v (including hypothetical row).
		if !st.withinGroupDirectArgSet {
			return NullDatum
		}
		v := st.withinGroupDirectArg
		totalWithHypothetical := int64(len(orderedVals)) + 1
		lessOrEqual := int64(0)
		for _, val := range orderedVals {
			if val.IsNull() {
				continue
			}
			cmp, cerr := compareDatum(val, v, 0)
			if cerr != nil {
				continue
			}
			if cmp <= 0 {
				lessOrEqual++
			}
		}
		lessOrEqual++ // count the hypothetical row itself
		result := float64(lessOrEqual) / float64(totalWithHypothetical)
		return NewStringDatum(strconv.FormatFloat(result, 'g', 15, 64))

	case "percent_rank":
		// percent_rank(v) WITHIN GROUP (ORDER BY x): (rank-1) / (total-1).
		if !st.withinGroupDirectArgSet {
			return NullDatum
		}
		v := st.withinGroupDirectArg
		totalWithHypothetical := int64(len(orderedVals)) + 1
		if totalWithHypothetical <= 1 {
			return NewStringDatum("0")
		}
		rank := int64(1)
		for _, val := range orderedVals {
			if val.IsNull() {
				continue
			}
			cmp, cerr := compareDatum(val, v, 0)
			if cerr != nil {
				continue
			}
			if cmp < 0 {
				rank++
			}
		}
		result := float64(rank-1) / float64(totalWithHypothetical-1)
		return NewStringDatum(strconv.FormatFloat(result, 'g', 15, 64))

	case "mode":
		// mode() WITHIN GROUP (ORDER BY x): most frequent value (lowest if tie).
		freq := map[string]int{}
		order := []string{}
		vals := map[string]Datum{}
		for _, val := range orderedVals {
			if val.IsNull() {
				continue
			}
			k := datumKey(val)
			if _, seen := freq[k]; !seen {
				order = append(order, k)
				vals[k] = val
			}
			freq[k]++
		}
		if len(order) == 0 {
			return NullDatum
		}
		maxFreq := 0
		best := order[0]
		for _, k := range order {
			if freq[k] > maxFreq {
				maxFreq = freq[k]
				best = k
			}
		}
		return vals[best]
	}

	return NullDatum
}

// evalExprFromRow evaluates a planner expression against a fixed (possibly empty) row.
// Used by ordered-set aggregate finalizers to evaluate "direct arguments" like the
// percentile fraction p in `percentile_cont(p) WITHIN GROUP (ORDER BY x)`.
// tryParseFloatArray attempts to parse a Datum as a text-format float array
// like {0.25,0.5,0.75}. Returns the fractions and true on success. M0097-0035.
func tryParseFloatArray(d Datum) ([]float64, bool) {
	sv := ""
	switch d.Kind {
	case KindString:
		sv = d.StringValue()
	case KindBytes:
		sv = string(d.BytesValue())
	default:
		return nil, false
	}
	sv = strings.TrimSpace(sv)
	if !strings.HasPrefix(sv, "{") {
		return nil, false
	}
	// Flatten multi-dimensional arrays: {{NULL,1,0.5},{0.75,0.25,NULL}}
	// → [NULL, 1, 0.5, 0.75, 0.25, NULL]
	flat := strings.NewReplacer("{", "", "}", "").Replace(sv)
	parts := strings.Split(flat, ",")
	fracs := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.EqualFold(p, "null") || p == "" {
			fracs = append(fracs, math.NaN())
			continue
		}
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return nil, false
		}
		fracs = append(fracs, f)
	}
	return fracs, true
}

// tryParseFloat2DArray parses a 2D array string like {{null,1,0.5},{0.75,0.25,null}}
// into a slice of rows, each row being a slice of float64 (NaN for NULL).
// Returns (rows, true) if input is a well-formed 2D array; otherwise (nil, false). M0097-0125.
func tryParseFloat2DArray(d Datum) ([][]float64, bool) {
	sv := ""
	switch d.Kind {
	case KindString:
		sv = d.StringValue()
	case KindBytes:
		sv = string(d.BytesValue())
	default:
		return nil, false
	}
	sv = strings.TrimSpace(sv)
	// Must start with {{ to be a 2D array.
	if !strings.HasPrefix(sv, "{{") {
		return nil, false
	}
	// Strip outer braces: {{a,b},{c,d}} → {a,b},{c,d}
	inner := sv[1 : len(sv)-1]
	// Split into rows: find each {...} sub-array.
	var rows [][]float64
	depth := 0
	start := -1
	for i, ch := range inner {
		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				rowStr := inner[start+1 : i] // strip { and }
				parts := strings.Split(rowStr, ",")
				row := make([]float64, len(parts))
				for j, p := range parts {
					p = strings.TrimSpace(p)
					if strings.EqualFold(p, "null") || p == "" {
						row[j] = math.NaN()
						continue
					}
					f, err := strconv.ParseFloat(p, 64)
					if err != nil {
						return nil, false
					}
					row[j] = f
				}
				rows = append(rows, row)
				start = -1
			}
		}
	}
	if len(rows) == 0 {
		return nil, false
	}
	return rows, true
}

// format2DTextArray formats a 2D slice of string values as {{a,b},{c,d}}. M0097-0125.
func format2DTextArray(rows [][]string) string {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, row := range rows {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('{')
		for j, v := range row {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(v)
		}
		sb.WriteByte('}')
	}
	sb.WriteByte('}')
	return sb.String()
}

// percentileContOneFloat computes percentile_cont for a single fraction pf
// over the sorted orderedVals. Returns the result as a string Datum. M0097-0035.
// If float4Input is true, ordered values are rounded through float32 to match
// PG's float4_accum semantics (float4 binary storage → float8 interpolation). M0097-0115.
func percentileContOneFloat(orderedVals []Datum, n int, pf float64, float4Input bool) string {
	pos := pf * float64(n-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower >= n {
		lower = n - 1
	}
	if upper >= n {
		upper = n - 1
	}
	if lower == upper {
		lo := aggDatumToFloat64(orderedVals[lower])
		if float4Input {
			lo = float64(float32(lo))
		}
		return strconv.FormatFloat(lo, 'g', 15, 64)
	}
	frac := pos - float64(lower)
	lo := aggDatumToFloat64(orderedVals[lower])
	hi := aggDatumToFloat64(orderedVals[upper])
	if float4Input {
		lo = float64(float32(lo))
		hi = float64(float32(hi))
	}
	result := lo + frac*(hi-lo)
	return strconv.FormatFloat(result, 'g', 15, 64)
}

// percentileDiscOneFloat computes percentile_disc for a single fraction pf
// over the sorted orderedVals. Returns the matching element Datum. M0097-0035.
func percentileDiscOneFloat(orderedVals []Datum, n int, pf float64) Datum {
	rowNum := int(math.Ceil(pf * float64(n)))
	if rowNum < 1 {
		rowNum = 1
	}
	if rowNum > n {
		rowNum = n
	}
	return orderedVals[rowNum-1]
}


func evalExprFromRow(expr planner.Expr, row Row, ctx *Context) (Datum, error) {
	slot := SlotFromRow(nil, row)
	return evalExprSlot(expr, slot, ctx)
}
