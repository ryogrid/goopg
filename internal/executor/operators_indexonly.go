package executor

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/array"
	"github.com/goopg/goopg/internal/storage/lmgr"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// indexOnlyScanOp implements IndexOnlyScan (M0046-0004): when the Visibility
// Map marks a heap page ALL_VISIBLE, the operator decodes projected column
// values from the B-tree key bytes and returns them without fetching the heap.
// For pages not yet marked ALL_VISIBLE it falls back to a full heap fetch
// so MVCC visibility is always respected.
type indexOnlyScanOp struct {
	plan *optimizer.IndexOnlyScan
	ctx  *Context
	rows []Row
	idx  int
	// pidx is the shared leaf-block claim set when this scan is a Gather
	// worker's driving scan; nil for a serial scan, and a nil receiver
	// claims every block (parallel_scan.go). M0134-0189.
	pidx *parallelIndexScanState
	// leafOwned caches this worker's verdict per leaf, lastLeaf* the most
	// recent one. The shared claim is a sync.Map operation and a range scan
	// visits ~300 entries per leaf, so consulting it per ENTRY made the
	// parallel scan 3.5x SLOWER than serial (q16 1.6s -> 5.7s). A btree scan
	// walks leaves in key order, so the common case is "same block as the
	// last entry" and costs one comparison. The map is kept so revisiting a
	// block reuses this worker's OWN verdict rather than re-asking the
	// shared set, which would answer "already claimed" about our own claim
	// and silently drop rows.
	leafOwned     map[storage.BlockNumber]bool
	lastLeaf      storage.BlockNumber
	lastLeafOwned bool
	lastLeafValid bool
	// arrayStyle is the session DateStyle/TimeZone an array element's output
	// function reads, resolved once in Open. M0119-0006.
	arrayStyle array.OutputStyle
	// coveredHeapIdx[i] is the position of Covered[i] in the table's column
	// list, and coveredEnum[i] maps that column's enum labels to sort orders
	// (nil when the column is not an enum). review/260831 EO2-6/EO2-7: both
	// used to be recomputed PER ROW — a linear name scan over every table
	// column, and an enum lookup plus a linear label scan.
	coveredHeapIdx []int
	coveredEnum    []map[string]float64
	// coveredKeyIdx[i] is the position of Covered[i] among the index's key
	// columns, resolved once per scan (review/260831 EO2-7: the projection
	// used to go through a freshly allocated map[string]Datum per row).
	coveredKeyIdx []int
	// M0092-0007: embedded slot reused across every Next() call
	// so we don't allocate a fresh MaterializedSlot per emission.
	slot MaterializedSlot
	// heapFetchCount, when non-nil, is incremented once per index entry
	// whose heap page was NOT ALL_VISIBLE and therefore required a heap
	// fetch — the EXPLAIN ANALYZE "Heap Fetches" tally (design 0118-0102).
	// Set by maybeInstrument only under EXPLAIN ANALYZE; nil otherwise.
	heapFetchCount *int64
	// touchedBlocks records the heap blocks visited on the non-ALL_VISIBLE
	// fallback path; for a TEMPORARY relation they are opportunistically pruned
	// after the scan so a subsequent index-only scan reflects PG's prune-on-read
	// (horizons.spec, M0118-0009). nil keys are never inserted.
	touchedBlocks map[storage.BlockNumber]struct{}
	// State captured by openPrep and reused across every Rescan: the locks it
	// took are held for the operator's lifetime and the btree handle stays
	// valid, so an NLI parent probing once per outer row re-enters only Rescan.
	// `tree != nil` is also what Open reads to detect a re-Open.
	heapRel   storage.RelFileNode
	tree      *nbtree.BTree
	isHashIdx bool
	// outerSlot / outerWidth are bound by a NestedLoopIndexJoin parent before
	// each Rescan (BindOuter); the probe helpers resolve Key/Keys/LowKey/HighKey
	// against the slot. nil on the single-table path, where those expressions
	// must reduce to constants.
	outerSlot  SlotView
	outerWidth int
	// hashProbeFingerprint is the blob-format encoding of this scan's probe key
	// (ssiHashProbeFingerprint) — the bytes the hash-bucket SIREAD tag must be
	// derived from, as opposed to the tuple-image search key the same probe
	// descends the tree with. Set by lookupKey/lookupKeys.
	hashProbeFingerprint []byte
}

// setHeapFetchCounter implements the heapFetchCounter interface so EXPLAIN
// ANALYZE can surface this scan's heap-fetch count (design 0118-0102).
func (o *indexOnlyScanOp) setHeapFetchCounter(c *int64) { o.heapFetchCount = c }

func newIndexOnlyScanOp(p *optimizer.IndexOnlyScan) *indexOnlyScanOp {
	return &indexOnlyScanOp{plan: p}
}

func (o *indexOnlyScanOp) Schema() optimizer.Schema { return o.plan.Output() }

// Open is the single-table entry point: one-time setup, then one scan.
//
// The split into `openPrep` + `Rescan` mirrors `indexScanOp`
// (operators_index.go:274/292/353/362) exactly, and for the same reason: a
// `NestedLoopIndexJoin` parent calls `openPrep` ONCE and then `Rescan` per outer
// row, so the relation locks, the relation-grain SIREAD and `btree.Open` must
// not be repeated per probe — while the probe key, the bitmap of matched rows
// and the temp-page prune must be redone on every one.
//
// Which half each piece belongs to is mirrored from that sibling rather than
// re-derived, because the SSI allocation is the part that would be a
// correctness bug rather than a slow query if it were guessed wrong.
func (o *indexOnlyScanOp) Open(ctx *Context) error {
	if o.tree != nil {
		// Reopen (e.g. a re-executed subplan): locks are still held and the
		// btree handle is still valid, so only the scan is redone.
		o.ctx = ctx
		return o.Rescan(nil, 0)
	}
	if err := o.openPrep(ctx); err != nil {
		return err
	}
	return o.Rescan(nil, 0)
}

// openPrep is the one-time half: everything independent of any outer-row
// binding. See Open for why the boundary sits where it does.
func (o *indexOnlyScanOp) openPrep(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: "IndexOnlyScan requires storage handles"}
	}
	if o.plan.Table != nil && !dmlPrivilegePermittedAs(ctx, o.plan.Table, "SELECT", selectPrivilegeCheckRole(ctx, o.plan.PrivilegeCheckRoleSet, o.plan.PrivilegeCheckRole)) {
		return &ExecError{Code: "42501", Pos: o.plan.Pos(), Message: fmt.Sprintf("permission denied for table %s", o.plan.Table.Name)}
	}
	o.ctx = ctx
	// The array element output style, resolved once per scan (M0119-0006). An
	// index-only scan renders array elements from the KEY rather than the heap;
	// it is the seq/bitmap scans' sibling and must agree with them, so it reads
	// the same session GUCs through the same helper.
	o.arrayStyle = arrayOutputStyle(ctx)

	heapRel := ctx.Catalog.RelFileNode(o.plan.Table)
	o.heapRel = heapRel
	if err := ctx.acquireRelLock(heapRel, lmgr.AccessShareLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	if err := ctx.acquireScanReadLockTxn(heapRel); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	// PostgreSQL locks every index of the scanned relation in AccessShare, not
	// only the one this scan reads (get_relation_info opens them all). M0118-0008
	// (partition-drop-index-locking).
	if err := ctx.acquireScanIndexReadLocksTxn(o.plan.Table); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}

	// M0118-0009 (design 0118-0099): a hash index supports only equality, and PG
	// takes a PAGE-grain predicate lock on the probed bucket (PredicateLockPage in
	// hash.c) rather than a relation-grain lock — so a concurrent INSERT into a
	// DIFFERENT bucket forms no rw-conflict (predicate-hash "reduced false
	// positives"). For a hash index we therefore SKIP the relation-grain SIREAD
	// here and instead take the bucket-grain lock once the probe key is encoded
	// (below); the per-tuple heap reads also switch to conflict-out-only so they
	// do not coarsen into a heap-page lock that would re-introduce the false
	// positive. DeclaredHash marks an index created `USING hash` (Method stays
	// "btree" since goopg builds it on the B-tree substrate).
	isHashIdx := o.plan.Index != nil && o.plan.Index.DeclaredHash
	o.isHashIdx = isHashIdx
	// M0118-0001: a SERIALIZABLE index-only scan takes a relation-level SIREAD
	// predicate lock on the heap relation, exactly like the seq-scan and
	// (heap-fetching) index-scan paths. Acquired BEFORE the probe-bound lookups
	// below so it is held even when the scan matches no key — that empty-result
	// case is precisely the phantom the lock must cover (read-write-unique-2/-3
	// probe a non-existent key first, then both INSERT it). Gated to
	// SERIALIZABLE inside ssiRecordRelationRead; temp / matview relations are
	// excluded as PredicateLockingNeededForRelation requires.
	if !isHashIdx && (o.plan.Table == nil || (!o.plan.Table.Temp && !o.plan.Table.IsMatView)) {
		ssiRecordRelationRead(ctx, heapRel)
	}

	idxRel := ctx.Catalog.IndexRelFileNode(o.plan.Index)
	tree, err := openIndexBTree(ctx, o.plan.Index, idxRel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.tree = tree
	return nil

}

// BindOuter records the slot a NestedLoopIndexJoin parent binds before each
// Rescan; the probe helpers resolve Key/Keys/LowKey/HighKey against it. Mirrors
// indexScanOp.BindOuter.
func (o *indexOnlyScanOp) BindOuter(slot SlotView, outerWidth int) {
	o.outerSlot = slot
	o.outerWidth = outerWidth
}

// Rescan re-drains the index after binding an outer slot. The single-table path
// reaches it through Open with a nil slot; an NLI parent calls it once per outer
// row.
func (o *indexOnlyScanOp) Rescan(outerSlot SlotView, outerWidth int) error {
	if o.tree == nil {
		// Defensive: openPrep must have run.
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "indexOnlyScanOp.Rescan called before Open"}
	}
	o.rows = o.rows[:0]
	o.idx = 0
	o.touchedBlocks = nil
	o.outerSlot = outerSlot
	o.outerWidth = outerWidth
	ctx := o.ctx
	heapRel := o.heapRel
	tree := o.tree
	isHashIdx := o.isHashIdx
	o.hashProbeFingerprint = nil
	var loBytes, hiBytes []byte
	switch {
	case len(o.plan.Keys) > 0:
		// Multi-column equality probe (M0054-0006 composite), preserved
		// through IOS promotion by M0116-0003.
		//
		// `Keys` may bind a strict LEADING PREFIX of the index, since a
		// prefix `*IndexScan` is promotable like any other (PG's
		// `amoptionalkey`, indxpath.c:1029-1076). A full key addresses
		// one B-tree leaf entry exactly and must NOT be padded; a
		// prefix must be, or the scan stops at the first entry instead
		// of covering every entry sharing the prefix. Kept identical to
		// `indexScanOp`'s branch — the two probe the same index for the
		// same plan node, so they cannot be allowed to disagree.
		key, ok, err := o.lookupKeys()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = key
		hiBytes = key
		if len(o.plan.Keys) < len(o.plan.Index.Columns) {
			hiBytes = o.ctx.compositeUpperBound(o.plan.Index, key)
		}
	case o.plan.Key != nil:
		key, ok, err := o.lookupKey()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = key
		hiBytes = key
		if len(o.plan.Index.Columns) > 1 {
			hiBytes = o.ctx.compositeUpperBound(o.plan.Index, key)
		}
	default:
		lo, hi, ok, err := o.lookupRangeBounds()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = lo
		hiBytes = hi
		// M0134-0001 S4 (class 8): kept identical to `indexScanOp`'s range
		// branch — the two probe the same index for the same plan node, so
		// they cannot be allowed to disagree. Composite padding follows the
		// bound's strictness: an inclusive hi needs trailing +infinity to
		// cover every key sharing the leading columns; an EXCLUSIVE hi stays
		// a bare prefix. The lo side is symmetric: pad only when EXCLUSIVE so
		// the whole equal-key group is skipped.
		if len(o.plan.Index.Columns) > 1 {
			if loBytes != nil && o.plan.LowOp == parser.OpGt {
				loBytes = o.ctx.compositeUpperBound(o.plan.Index, loBytes)
			}
			if hiBytes != nil && o.plan.HighOp != parser.OpLt {
				hiBytes = o.ctx.compositeUpperBound(o.plan.Index, hiBytes)
			}
		}
	}

	// Hash-index equality probe: take the bucket-grain SIREAD on the index
	// (design 0118-0099) in place of the relation-grain lock skipped above. The
	// tag comes from the probe's FINGERPRINT (ssiHashProbeFingerprint) — the
	// same encoding ssiRecordHashIndexInsert hashes — NOT from loBytes, which is
	// the tree search key and, for a describable index, a tuple image. No-op
	// outside SERIALIZABLE. When no fingerprint could be made (a range probe, or
	// an unencodable key part) the relation-grain lock skipped above is taken
	// after all: over-approximating is safe, holding nothing is not.
	if isHashIdx {
		if len(o.hashProbeFingerprint) > 0 {
			ssiRecordHashBucketRead(ctx, heapRel.DBOid, o.plan.Index.OID, o.hashProbeFingerprint)
		} else if o.plan.Table == nil || (!o.plan.Table.Temp && !o.plan.Table.IsMatView) {
			ssiRecordRelationRead(ctx, heapRel)
		}
	}

	// Some key encodings cannot be inverted (interval's key is the lossy
	// comparison span — btree_interval_key.go), so the decode-from-key fast
	// path below has nothing to decode. Reading the heap instead is always
	// correct, just slower — and it is what the ALL_VISIBLE flag is an
	// optimization over — whereas letting decodeRowFromKey error would fail the
	// whole query. Decided once per scan: it is a property of the index's
	// column types, not of the row.
	keyDecodable := o.indexKeyIsDecodable()

	scanFn := func(key []byte, ptr storage.ItemPointer) (bool, error) {
		// Fast path: ALL_VISIBLE → decode from key, zero heap reads.
		if keyDecodable && ctx.VM != nil && ctx.VM.AllVisible(heapRel, ptr.Block) {
			row, err := o.decodeRowFromKey(key)
			if err != nil {
				return false, &ExecError{Code: "XX000", Pos: o.plan.Pos(),
					Message: fmt.Sprintf("IOS decode: %v", err)}
			}
			o.rows = append(o.rows, row)
			return true, nil
		}

		// Fallback: heap fetch + HOT chain + MVCC. The page was not ALL_VISIBLE.
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		// Peek the index entry's root line pointer first. If it was reclaimed
		// (LP_UNUSED / LP_DEAD) by a prior prune — the goopg analog of an index
		// entry PG's kill_prior_tuple would have marked LP_DEAD — this entry
		// resolves to no heap tuple: it costs no heap fetch and yields no row.
		// Skipping it here is what drops "Heap Fetches" to 0 on a re-scan after a
		// temp relation's deleted rows are pruned (horizons.spec, M0118-0009).
		if rootID, idErr := storage.PageGetItemID(slot.Page(), ptr.Offset); idErr == nil &&
			(rootID.Flags == storage.ItemIDUnused || rootID.Flags == storage.ItemIDDead) {
			slot.RUnlock()
			ctx.Pool.Unpin(slot)
			return true, nil
		}
		// A genuine heap fetch — count it for EXPLAIN ANALYZE "Heap Fetches"
		// (design 0118-0102), mirroring upstream's ioss_HeapFetches++ which fires
		// per visited entry regardless of the eventual visibility verdict.
		if o.heapFetchCount != nil {
			*o.heapFetchCount++
		}
		if o.plan.Table != nil && o.plan.Table.Temp {
			if o.touchedBlocks == nil {
				o.touchedBlocks = make(map[storage.BlockNumber]struct{})
			}
			o.touchedBlocks[ptr.Block] = struct{}{}
		}
		tuple, actualSlot, found := followHOTChain(slot.Page(), ptr.Offset, ctx.Snap, ctx.Tx.XID, ctx.MultiXact,
			ctx.CmdID, ctx.comboStore())
		// M0118-0001: SSI phantom conflict-out for an index-only-scanned tuple
		// present at this TID but invisible because a concurrent transaction
		// inserted it — the IOS analog of the seq-scan invisible-tuple path. The
		// VM bit is cleared by a concurrent in-flight insert, so this fallback
		// (non-ALL_VISIBLE) branch is exactly where that phantom surfaces.
		var invisXmin storage.TransactionID
		if !found && ssiActive(ctx) {
			if raw, terr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset); terr == nil {
				invisXmin = raw.Header.Xmin
			}
		}
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
		if !found {
			if invisXmin != storage.InvalidTransactionID {
				if serr := ssiRecordInvisibleTupleRead(ctx, heapRel, invisXmin); serr != nil {
					return false, serr
				}
			}
			return true, nil
		}
		// M0118-0001: SSI read-path per-tuple conflict-out on the HOT-resolved
		// live version. The ALL_VISIBLE fast path needs no heap tuple, but this
		// non-ALL_VISIBLE fallback already fetched it, so record the rw-edge
		// against the tuple's xmin (a concurrent inserter) AND xmax (a concurrent
		// deleter/updater) — the latter is the referential-integrity write-skew
		// edge where this reader sees a row an in-flight SERIALIZABLE peer has
		// DELETEd. Mirrors the heap-fetching index-scan path
		// (operators_index.go); the helper short-circuits for RC/RR and the
		// tuple-grain predicate lock it would take is pruned by the relation-grain
		// SIREAD already held (ssiRecordRelationRead above). The page RLock/pin is
		// released above, so a non-nil error (reader closed a dangerous structure
		// to an already-committed writer) propagates as a mid-statement 40001.
		if isHashIdx {
			// Hash bucket page lock already covers the phantom (above); a heap
			// tuple SIREAD here would coarsen to a heap-page lock and re-introduce
			// the different-bucket false positive. Keep conflict-out only so the
			// write-before-read same-bucket edge still forms (design 0118-0099).
			if serr := ssiConflictOutTupleRead(ctx, tuple.Header.Xmin, tuple.Header.Xmax); serr != nil {
				return false, serr
			}
		} else if serr := ssiRecordTupleRead(ctx, heapRel, ptr.Block, actualSlot, tuple.Header.Xmin, tuple.Header.Xmax); serr != nil {
			return false, serr
		}
		row, err := o.decodeRowFromHeap(tuple)
		if err != nil {
			return false, err
		}
		o.rows = append(o.rows, row)
		return true, nil
	}

	scanPosFn := func(key []byte, ptr storage.ItemPointer, _ nbtree.ScanPos) (bool, error) {
		return scanFn(key, ptr)
	}
	// nil for a serial scan, so the scan behaves exactly as it always has.
	var leafFilter func(storage.BlockNumber) bool
	if o.pidx != nil {
		leafFilter = o.ownsLeaf
	}

	// RangeScanWithPosLeafFilter rather than RangeScan: the leaf filter is
	// what partitions the scan across workers, and the bounds semantics are
	// identical — RangeScan is rangeScanPos with both ends inclusive. The two
	// bool flags carry the bound strictness (M0134-0001 S4 class 8); they are
	// false for every producer that leaves LowOp/HighOp at OpUnknown.
	if err := tree.RangeScanWithPosLeafFilter(loBytes, hiBytes, o.plan.LowOp == parser.OpGt, o.plan.HighOp == parser.OpLt, leafFilter, scanPosFn); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}

	// S6 max rewrite: a Backward IOS emits the materialised rows in reverse.
	// o.rows is only complete once RangeScan has run (Open materialises the
	// whole index range before Next ever runs), so the start index is derived
	// here — not at the top of Open, where o.rows is still nil.
	if o.plan.Backward {
		o.idx = len(o.rows) - 1
	}

	// Prune-on-read for TEMPORARY relations (horizons.spec, M0118-0009). PG
	// opportunistically prunes a heap page whenever a scan visits it; for a temp
	// relation that prune uses the session-local horizon (GlobalVisTempRels), so
	// rows the owning backend deleted in a committed statement are reclaimed even
	// while another session holds an older snapshot — that session cannot see the
	// temp table at all. Permanent relations are deliberately NOT pruned here:
	// their reclamation flows through VACUUM at the global horizon. We do NOT set
	// the VM ALL_VISIBLE bit, so a subsequent scan stays on this heap-checking
	// fallback path and skips the now-LP_UNUSED entries (see the root-line-pointer
	// peek above) instead of trusting the index key, which would resurrect the
	// deleted rows.
	o.pruneTouchedTempPages(ctx, heapRel)
	return nil
}

// pruneTouchedTempPages opportunistically reclaims dead tuples on the heap
// blocks this index-only scan fetched, for a TEMPORARY relation only, using the
// session-local horizon. Mirrors vacuumCore's reclamation kernel
// (storage.PageVacuumPrune + the LogHeapPruneOpt WAL hook / MarkDirty fallback)
// but takes no relation lock and never touches the Visibility Map. Best-effort:
// any error leaves the page untouched rather than failing the read.
// ownsLeaf reports whether this worker processes entries from leaf block blk,
// memoising the shared claim. See the leafOwned field for why the shared set is
// consulted at most once per block per worker rather than once per entry.
func (o *indexOnlyScanOp) ownsLeaf(blk storage.BlockNumber) bool {
	if o.lastLeafValid && o.lastLeaf == blk {
		return o.lastLeafOwned
	}
	owned, seen := o.leafOwned[blk]
	if !seen {
		owned = o.pidx.claimLeaf(blk)
		if o.leafOwned == nil {
			o.leafOwned = make(map[storage.BlockNumber]bool, 64)
		}
		o.leafOwned[blk] = owned
	}
	o.lastLeaf, o.lastLeafOwned, o.lastLeafValid = blk, owned, true
	return owned
}

func (o *indexOnlyScanOp) pruneTouchedTempPages(ctx *Context, heapRel storage.RelFileNode) {
	if len(o.touchedBlocks) == 0 || ctx.Pool == nil || ctx.TxnMgr == nil {
		return
	}
	if o.plan.Table == nil || !o.plan.Table.Temp {
		return
	}
	horizon := ctx.TxnMgr.OldestXminForProc(int32(ctx.Tx.Handle) - 1)
	if horizon == storage.InvalidTransactionID {
		return
	}
	logPrune := ctx.Pool.LogHeapPruneOpt()
	for blk := range o.touchedBlocks {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: blk})
		if err != nil {
			continue
		}
		page := slot.Page()
		if storage.IsNew(page) {
			ctx.Pool.Unpin(slot)
			continue
		}
		slot.Lock()
		pr, _, perr := storage.PageVacuumPrune(page, horizon)
		if perr != nil || (len(pr.Redirects) == 0 && len(pr.Unused) == 0) {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			continue
		}
		if logPrune != nil {
			blkCopy := blk
			_ = ctx.Pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
				return logPrune(heapRel, blkCopy, pr.Redirects, pr.Unused)
			})
		} else {
			ctx.Pool.MarkDirty(slot)
		}
		slot.Unlock()
		ctx.Pool.Unpin(slot)
	}
}

func (o *indexOnlyScanOp) Next() (TupleSlot, error) {
	// S6 min/max rewrite: the IOS carries a residual `col IS NOT NULL` qual in
	// Cond that could not be pushed into the btree probe (planagg.c pushes it as
	// an index qual; goopg's probe is equality/range-shaped). Skip rows that do
	// not satisfy it. Nil Cond means no filtering.
	//
	// The max (Backward) rewrite steps the materialised rows in reverse — and
	// MUST keep this same Cond check: emitting o.rows[len-1] without it leaks a
	// NULL for a table that contains NULLs (the NULL-trap rule).
	step := 1
	if o.plan.Backward {
		step = -1
	}
	for o.idx >= 0 && o.idx < len(o.rows) {
		r := o.rows[o.idx]
		o.idx += step
		if o.plan.Cond != nil {
			d, err := evalExpr(o.plan.Cond, r, o.ctx)
			if err != nil {
				return nil, err
			}
			if d.IsNull() || d.Kind != KindBool || !d.BoolValue() {
				continue
			}
		}
		// M0092-0007: stack-aliased slot reused across Next() calls.
		o.slot.schema = o.Schema()
		o.slot.row = r
		return &o.slot, nil
	}
	return nil, EOF
}

func (o *indexOnlyScanOp) Close() error {
	o.rows = nil
	return nil
}

// indexKeyIsDecodable reports whether decodeRowFromKey can invert this index's
// key. False for an index any of whose key columns has a deliberately
// non-invertible encoding — `interval`, whose key is upstream's
// interval_cmp_value span (btree_interval_key.go), and any ARRAY whose element
// type the key layer cannot render back (btree_key_decodable.go, which owns the
// whole answer). The whole index is declined rather than the single column
// because the composite walk decodes columns in order and cannot skip one whose
// byte width it does not know.
//
// It also asks the narrower question indexKeyColumnRendersHeapText owns (34th
// slice): a key that inverts to the right VALUE but the wrong SPELLING is worse
// than a refusal, because the scan succeeds and prints a different row than the
// heap would. `numeric` is that case — its key has no display scale — and it is
// asked only of the BLOB key format, since the display scale is present on the
// pgIndexKeyDesc (PG tuple-image) path, whose key carries per-attribute datums.
// decodeRowFromKey routes on that same descriptor, so the two agree on which
// decode this predicate is judging.
func (o *indexOnlyScanOp) indexKeyIsDecodable() bool {
	if o.plan.Index == nil || o.ctx == nil || o.ctx.Catalog == nil {
		return true
	}
	blobKey := o.ctx.pgIndexKeyDesc(o.plan.Index) == nil
	for _, colName := range o.plan.Index.Columns {
		col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, colName)
		if !ok {
			continue // the decode path reports this one itself
		}
		if !indexKeyColumnIsDecodable(*col) {
			return false
		}
		if blobKey && !indexKeyColumnRendersHeapText(*col) {
			return false
		}
	}
	return true
}

// decodeRowFromKey extracts covered column values from a B-tree key.
func (o *indexOnlyScanOp) decodeRowFromKey(key []byte) (Row, error) {
	// Tuple format (M0130-S11.4 slice 3b-2c-ii-B2-c, the flip): the key is one
	// IndexTuple image, not a concatenation of order-preserving segments, so
	// neither the single-column fast lane nor the running-offset loop below can
	// read it — they would consume a type's width out of a null bitmap and an
	// alignment hole. `pgIndexTupleKeyDatums` is the inverse of the encoder that
	// wrote it; the projection onto Covered is shared with the blob path, since
	// only the DECODE differs between the two formats, never which columns the
	// scan is answering from.
	if desc := o.ctx.pgIndexKeyDesc(o.plan.Index); desc != nil {
		keyCols := pgIndexKeyColumns(o.plan.Index)
		vals, err := pgIndexTupleKeyDatums(desc, keyCols, key)
		if err != nil {
			return nil, err
		}
		names := make([]string, len(keyCols))
		for i, kc := range keyCols {
			names[i] = kc.Name
		}
		return o.projectKeyVals(names, vals)
	}
	if len(o.plan.Index.Columns) == 1 && len(o.plan.Covered) == 1 {
		d, err := decodeBTreeKeyToDatumStyled(key, o.plan.Covered[0], o.arrayStyle)
		if err != nil {
			return nil, err
		}
		return Row{d}, nil
	}
	// Multi-column: decode all key columns in declaration order, then project.
	// EX1-02b: stop decoding after the highest covered key ordinal — decode
	// THROUGH gaps (offsets are sequential, so a gap column's bytes must
	// still be walked to reach higher ordinals). No codec change.
	maxKey := len(o.plan.Index.Columns) - 1
	if m, ok := iosMaxCoveredKeyPos(o.plan); ok && m < maxKey {
		maxKey = m
	}
	vals := make([]Datum, 0, maxKey+1)
	off := 0
	for ki, colName := range o.plan.Index.Columns {
		if ki > maxKey {
			break
		}
		col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, colName)
		if !ok {
			return nil, fmt.Errorf("IOS: index column %q not in catalog", colName)
		}
		d, n, err := decodeIndexKeyColumnStyled(key[off:], *col, o.arrayStyle)
		if err != nil {
			return nil, fmt.Errorf("IOS key col %q: %w", colName, err)
		}
		vals = append(vals, d)
		off += n
	}
	return o.projectKeyVals(o.plan.Index.Columns, vals)
}

// projectKeyVals picks the scan's output columns out of the decoded key
// attributes, using a position map resolved once per scan.
//
// review/260831 EO2-7: this was a map[string]Datum built per row and then read
// back per covered column. keyCols is fixed for the life of the scan — it comes
// from the index definition — so the positions are resolved on the first row.
func (o *indexOnlyScanOp) projectKeyVals(keyNames []string, vals []Datum) (Row, error) {
	if o.coveredKeyIdx == nil {
		idx := make([]int, len(o.plan.Covered))
		for i, col := range o.plan.Covered {
			idx[i] = -1
			for j, kn := range keyNames {
				if kn == col.Name {
					idx[i] = j
					break
				}
			}
			if idx[i] < 0 {
				return nil, fmt.Errorf("IOS: covered column %q not decoded", col.Name)
			}
		}
		o.coveredKeyIdx = idx
	}
	row := make(Row, len(o.plan.Covered))
	for i, j := range o.coveredKeyIdx {
		if j >= len(vals) {
			return nil, fmt.Errorf("IOS: covered column %q not decoded", o.plan.Covered[i].Name)
		}
		row[i] = vals[j]
	}
	return row, nil
}

// numericDatumFromBig wraps a decoded NUMERIC mantissa/scale pair in a Datum,
// taking the int64 fast lane when the mantissa fits and the big.Int overflow
// lane otherwise — the same split the codec makes (see datum.go). M0119-0006.
func numericDatumFromBig(m *big.Int, scale int16) Datum {
	if m.IsInt64() {
		return NewNumericInt64Datum(m.Int64(), scale)
	}
	return NewNumericBigDatum(m, scale)
}

// decodeIndexKeyColumn decodes one column from a B-tree key slice and returns
// the Datum plus the number of bytes consumed. Used by the multi-column path.
func decodeIndexKeyColumn(key []byte, col catalog.Column) (Datum, int, error) {
	return decodeIndexKeyColumnStyled(key, col, array.DefaultOutputStyle())
}

// decodeIndexKeyColumnStyled is decodeIndexKeyColumn under an explicit array
// output style; only an ARRAY key column of a date/timestamp/timestamptz
// element type reads it (M0119-0006).
func decodeIndexKeyColumnStyled(key []byte, col catalog.Column, st array.OutputStyle) (Datum, int, error) {
	typeName := col.Type.Name
	// Fixed-width branches must slice `key` to the type's exact byte
	// width before delegating — btree.DecodeInt4 / DecodeInt8 enforce
	// `len(b) == width`, which fails when the multi-column loop passes
	// the still-trailing remainder of a composite key.
	// ARRAY column: Type.Name is the ELEMENT type, so every predicate below (and
	// in decodeScalarBTreeKey) answers for the element and would consume the
	// element's width out of a longer array segment. Route arrays first, the
	// mirror of encodeBTreeKeyForColumn's own array-first routing. M0119-0006.
	if col.Type.IsArray {
		return decodeArrayBTreeKey(key, col, st)
	}
	// int2 / oid / bool / bytea / time (btree_scalar_keys.go). Routed BEFORE the
	// switch below so neither of these types can reach the `default:` arm, which
	// reads any 8 leading bytes as an enum float8 without ever erroring.
	if d, n, handled, err := decodeScalarBTreeKey(key, typeName); handled {
		return d, n, err
	}
	switch {
	case isInt4Type(typeName):
		if len(key) < 4 {
			return NullDatum, 0, fmt.Errorf("btree: int4 key truncated, got %d bytes", len(key))
		}
		v, err := nbtree.DecodeInt4(key[:4])
		return Datum{Kind: KindInt, Int: int64(v)}, 4, err
	case isInt8Type(typeName):
		if len(key) < 8 {
			return NullDatum, 0, fmt.Errorf("btree: int8 key truncated, got %d bytes", len(key))
		}
		v, err := nbtree.DecodeInt8(key[:8])
		return Datum{Kind: KindInt, Int: v}, 8, err
	case isFloat8Type(typeName):
		if len(key) < 8 {
			return NullDatum, 0, fmt.Errorf("btree: float8 key truncated, got %d bytes", len(key))
		}
		v, err := nbtree.DecodeFloat8(key[:8])
		return NewStringDatum(strconv.FormatFloat(v, 'g', -1, 64)), 8, err
	case isTimestampType(typeName) || isTimestamptzType(typeName):
		// timestamp and timestamptz share the int64-micros key form. M0118-0001.
		if len(key) < 8 {
			return NullDatum, 0, fmt.Errorf("btree: timestamp key truncated, got %d bytes", len(key))
		}
		v, err := nbtree.DecodeTimestamp(key[:8])
		ts := pgEpoch.Add(time.Duration(v) * time.Microsecond)
		// The key form is shared but the TYPE is not: an index-only scan answers
		// the column from the key, so this datum reaches the user in place of the
		// heap's, and must carry the same subtype the heap decode assigns
		// (decodeValuePG, codec.go). M0119-0006 (41st slice).
		if isTimestamptzType(typeName) {
			return NewTimestampTZDatum(ts), 8, err
		}
		return NewTimeDatum(ts), 8, err
	case isDateType(typeName):
		// date is encoded as int4 days since the PG epoch. M0118-0001.
		if len(key) < 4 {
			return NullDatum, 0, fmt.Errorf("btree: date key truncated, got %d bytes", len(key))
		}
		v, err := nbtree.DecodeInt4(key[:4])
		ts := pgEpoch.Add(time.Duration(v) * 24 * time.Hour)
		return NewDateDatum(ts), 4, err
	case isNumericType(typeName):
		// NUMERIC keys are variable-length but self-delimiting, so the
		// composite walk can consume exactly this column. Value-preserving,
		// not byte-preserving: EncodeNumericKey strips trailing mantissa
		// zeros, so 1.50 decodes as (15, scale 1). M0119-0006.
		m, scale, n, err := nbtree.DecodeNumericKey(key)
		if err != nil {
			return NullDatum, 0, err
		}
		return numericDatumFromBig(m, scale), n, nil
	case isVarcharType(typeName), isCharType(typeName), isTextType(typeName), isNameType(typeName),
		strings.ToLower(typeName) == "uuid":
		raw, n, err := nbtree.DecodeVarcharLen(key)
		return NewStringDatum(string(raw)), n, err
	default:
		// For unknown types (e.g. user-defined enums encoded as float8), attempt float8 decode.
		// Enum values are encoded via btree.EncodeFloat8(sortOrder) in encodeBTreeKeyForColumn.
		if len(key) < 8 {
			return NullDatum, 0, fmt.Errorf("IOS: unsupported key type %q (too short for float8)", typeName)
		}
		f, err := nbtree.DecodeFloat8(key[:8])
		if err != nil {
			return NullDatum, 0, fmt.Errorf("IOS: unsupported key type %q", typeName)
		}
		// Return as KindEnum with sort order; label unknown at this stage.
		return NewEnumDatum(f, ""), 8, nil
	}
}

// iosMaxCoveredKeyPos reports the highest position in the index's key-column
// order occupied by a Covered column. ok=false when any Covered column is not
// among the index key columns — then the key loop must decode everything
// (zero codec change: the caller only ever stops early, never skips).
func iosMaxCoveredKeyPos(p *optimizer.IndexOnlyScan) (max int, ok bool) {
	max = -1
	for _, col := range p.Covered {
		pos := -1
		for j, kn := range p.Index.Columns {
			if kn == col.Name {
				pos = j
				break
			}
		}
		if pos < 0 {
			return 0, false
		}
		if pos > max {
			max = pos
		}
	}
	return max, true
}

// iosHeapFallbackWidth is the exclusive heap-decode width for the
// non-ALL_VISIBLE fallback: [0, maxCovered+1), where maxCovered is the highest
// heap ordinal among the Covered columns. Gaps over-decode (offsets are
// sequential, so the range must still walk THROUGH them); a subset helper
// only if the gap cost ever matters.
func iosHeapFallbackWidth(p *optimizer.IndexOnlyScan) int {
	max := -1
	for _, col := range p.Covered {
		for j, tc := range p.Table.Columns {
			if tc.Name == col.Name {
				if j > max {
					max = j
				}
				break
			}
		}
	}
	return max + 1
}

// decodeRowFromHeap projects only the covered columns from a full heap tuple.
func (o *indexOnlyScanOp) decodeRowFromHeap(t storage.HeapTuple) (Row, error) {
	o.ensureCoveredMaps()
	// EX1-02b: subset decode — only [0, maxCovered+1) can be read through the
	// Covered projection below. A width at or past full takes the exact
	// pre-EX1-02b path.
	if to := iosHeapFallbackWidth(o.plan); to > 0 && to < len(o.plan.Table.Columns) {
		// Tuple-decompose + range-decode by name: there is NO HeapTuple
		// range helper, so natts comes from Infomask2 at this site. The
		// unstyled (default) array style matches the full path below.
		natts := int(t.Header.Infomask2 & storage.HeapNattsMask)
		fullRow := make(Row, len(o.plan.Table.Columns))
		if _, err := DecodeRowRangeIntoMctxPGTupleStyled(fullRow, o.plan.Table.Columns, t.Data, t.Bitmap, natts, nil, array.DefaultOutputStyle(), 0, to, 0); err != nil {
			return nil, err
		}
		return o.projectHeapRow(fullRow), nil
	}
	fullRow, err := DecodeHeapTupleRow(o.plan.Table.Columns, t, nil)
	if err != nil {
		return nil, err
	}
	return o.projectHeapRow(fullRow), nil
}

// projectHeapRow picks the scan's output columns out of a decoded heap row,
// converting enum spellings to sort orders like the key path must.
func (o *indexOnlyScanOp) projectHeapRow(fullRow Row) Row {
	row := make(Row, len(o.plan.Covered))
	for i, j := range o.coveredHeapIdx {
		if j >= 0 && j < len(fullRow) {
			row[i] = fullRow[j]
		}
	}
	// Convert KindString enum values to KindEnum (sort order) for correct
	// comparison in Filter predicates. M0097-0022.
	for i, labels := range o.coveredEnum {
		if labels == nil || row[i].Kind != KindString {
			continue
		}
		if order, ok := labels[row[i].StringValue()]; ok {
			row[i] = NewEnumDatum(order, row[i].StringValue())
		}
	}
	return row
}

// ensureCoveredMaps resolves, once per scan, where each Covered column lives in
// the table's column list and which of them are enums (with their label ->
// sort-order table). review/260831 EO2-6/EO2-7.
func (o *indexOnlyScanOp) ensureCoveredMaps() {
	if o.coveredHeapIdx != nil {
		return
	}
	o.coveredHeapIdx = make([]int, len(o.plan.Covered))
	o.coveredEnum = make([]map[string]float64, len(o.plan.Covered))
	im, hasIM := o.ctx.Catalog.(*catalog.InMemory)
	for i, col := range o.plan.Covered {
		o.coveredHeapIdx[i] = -1
		for j, tc := range o.plan.Table.Columns {
			if tc.Name == col.Name {
				o.coveredHeapIdx[i] = j
				break
			}
		}
		if !hasIM {
			continue
		}
		if et, isEnum := im.LookupEnum(col.Type.Name); isEnum {
			labels := make(map[string]float64, len(et.Values))
			for _, ev := range et.Values {
				labels[ev.Label] = ev.SortOrder
			}
			o.coveredEnum[i] = labels
		}
	}
}

// decodeBTreeKeyToDatum inverts the B-tree key encoding for a single column
// back to an executor Datum.
func decodeBTreeKeyToDatum(key []byte, col catalog.Column) (Datum, error) {
	return decodeBTreeKeyToDatumStyled(key, col, array.DefaultOutputStyle())
}

// decodeBTreeKeyToDatumStyled is decodeBTreeKeyToDatum under an explicit array
// output style (M0119-0006).
func decodeBTreeKeyToDatumStyled(key []byte, col catalog.Column, st array.OutputStyle) (Datum, error) {
	typeName := col.Type.Name
	// Sibling of decodeIndexKeyColumn's array routing (same ordering rationale:
	// an array's Type.Name is its ELEMENT type name, so it must not reach any
	// scalar predicate). Strict on the width: a single-column key is the whole
	// key, so bytes trailing the array's end marker mean this is not the
	// encoding we think it is. M0119-0006.
	if col.Type.IsArray {
		d, n, err := decodeArrayBTreeKey(key, col, st)
		if err != nil {
			return NullDatum, err
		}
		if n != len(key) {
			return NullDatum, fmt.Errorf("btree: array key for column %q has %d trailing bytes", col.Name, len(key)-n)
		}
		return d, nil
	}
	// Sibling of decodeIndexKeyColumn's routing — both must invert
	// encodeScalarBTreeKey, or an int2/oid/bool/bytea/time key column decodes
	// one way in a single-column IOS and another way in a composite one.
	if d, _, handled, err := decodeScalarBTreeKey(key, typeName); handled {
		return d, err
	}
	switch {
	case isInt4Type(typeName):
		v, err := nbtree.DecodeInt4(key)
		if err != nil {
			return NullDatum, err
		}
		return Datum{Kind: KindInt, Int: int64(v)}, nil

	case isInt8Type(typeName):
		v, err := nbtree.DecodeInt8(key)
		if err != nil {
			return NullDatum, err
		}
		return Datum{Kind: KindInt, Int: v}, nil

	case isFloat8Type(typeName):
		v, err := nbtree.DecodeFloat8(key)
		if err != nil {
			return NullDatum, err
		}
		return NewStringDatum(strconv.FormatFloat(v, 'g', -1, 64)), nil

	case isNumericType(typeName):
		// Sibling of the multi-column branch in decodeIndexKeyColumn — both
		// must invert EncodeNumericKey, or a NUMERIC key column decodes one
		// way in a single-column IOS and another way in a composite one.
		// M0119-0006.
		m, scale, _, err := nbtree.DecodeNumericKey(key)
		if err != nil {
			return NullDatum, err
		}
		return numericDatumFromBig(m, scale), nil

	case isVarcharType(typeName), isCharType(typeName), isTextType(typeName), isNameType(typeName),
		strings.EqualFold(typeName, "uuid"):
		// uuid rides EncodeVarchar (its canonical lowercase-hex text compares as
		// uuid_cmp's memcmp does), and its sibling decodeIndexKeyColumn has
		// always listed it here. Without this arm uuid reached the `default:`
		// enum guess below, which reads the first 8 ASCII bytes as a float8 sort
		// order and NEVER errors: a single-column index-only scan over a uuid
		// column answered from the key returned an empty enum Datum instead of
		// the uuid. Latent today only because a uuid index takes the PG
		// tuple-image key path (pgIndexTupleKeys), which is exactly why the
		// blob sibling drifted. M0119-0006, Hard-won Rule #2.
		b, err := nbtree.DecodeVarchar(key)
		if err != nil {
			return NullDatum, err
		}
		return NewStringDatum(string(b)), nil

	case isTimestampType(typeName) || isTimestamptzType(typeName):
		// timestamp and timestamptz share the int64-micros key form. M0118-0001.
		v, err := nbtree.DecodeTimestamp(key)
		if err != nil {
			return NullDatum, err
		}
		ts := pgEpoch.Add(time.Duration(v) * time.Microsecond)
		// Sibling of decodeIndexKeyColumn's arm above — both must tag, or the
		// same column decodes one way in a single-column index-only scan and
		// another way in a composite one. M0119-0006 (41st slice).
		if isTimestamptzType(typeName) {
			return NewTimestampTZDatum(ts), nil
		}
		return NewTimeDatum(ts), nil

	case isDateType(typeName):
		// date is encoded as int4 days since the PG epoch. M0118-0001.
		v, err := nbtree.DecodeInt4(key)
		if err != nil {
			return NullDatum, err
		}
		ts := pgEpoch.Add(time.Duration(v) * 24 * time.Hour)
		return NewDateDatum(ts), nil

	default:
		// Unknown type: attempt float8 decode for user-defined enums. M0097-0022.
		if len(key) < 8 {
			return NullDatum, fmt.Errorf("index-only scan: unsupported key type %q for key decode", typeName)
		}
		f, err2 := nbtree.DecodeFloat8(key[:8])
		if err2 != nil {
			return NullDatum, fmt.Errorf("index-only scan: unsupported key type %q for key decode", typeName)
		}
		return NewEnumDatum(f, ""), nil
	}
}

// lookupKeys evaluates Keys[i] in declared order and concatenates the
// per-column B-tree encodings, mirroring indexScanOp.lookupKeys. Carries
// the M0054-0006 composite probe through IOS promotion (M0116-0003).
// Any NULL component short-circuits to ok=false — equality on NULL is
// unknown, so the probe correctly produces zero rows.
// The three probe helpers resolve their expressions against `o.outerSlot`, not
// against a nil row. They took `nil` before this operator could be an NLI inner,
// which was correct then and would be a SILENT wrong answer now: an inner's
// probe key references OUTER columns, and evaluating it against no row yields
// whatever the nil path produces rather than an error. Mirrors indexScanOp's
// helpers, which have always been slot-aware. A nil slot is the single-table
// case, where the planner guarantees these reduce to constants.
func (o *indexOnlyScanOp) lookupKeys() ([]byte, bool, error) {
	if len(o.plan.Keys) == 0 || len(o.plan.Keys) > len(o.plan.Index.Columns) {
		return nil, false, &ExecError{
			Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexOnlyScanOp.lookupKeys: planner supplied %d keys for index %q with %d columns",
				len(o.plan.Keys), o.plan.Index.Name, len(o.plan.Index.Columns)),
		}
	}
	parts := make([]indexProbeKeyPart, 0, len(o.plan.Keys))
	for i, ke := range o.plan.Keys {
		v, err := evalExprSlot(ke, o.outerSlot, o.ctx)
		if err != nil {
			return nil, false, err
		}
		if v.IsNull() {
			return nil, false, nil
		}
		colName := o.plan.Index.Columns[i]
		col, found := o.ctx.Catalog.LookupColumn(o.plan.Table, colName)
		if !found {
			return nil, false, &ExecError{
				Code: "XX000", Pos: o.plan.Pos(),
				Message: fmt.Sprintf("indexed column %q not found on table %q", colName, o.plan.Table.Name),
			}
		}
		parts = append(parts, indexProbeKeyPart{col: col, val: v, pos: ke.Pos()})
	}
	probe, encErr := o.ctx.indexProbeKey(o.plan.Index, parts)
	if encErr != nil {
		return nil, false, encErr
	}
	o.hashProbeFingerprint = ssiHashProbeFingerprint(o.plan.Index, parts)
	return probe, true, nil
}

// lookupKey evaluates the equality probe key expression.
// Note: encodeBTreeKeyForColumn returns *ExecError (not error), so we
// must guard against the nil-pointer-in-interface issue explicitly.
func (o *indexOnlyScanOp) lookupKey() ([]byte, bool, error) {
	v, err := evalExprSlot(o.plan.Key, o.outerSlot, o.ctx)
	if err != nil {
		return nil, false, err
	}
	if v.IsNull() {
		return nil, false, nil
	}
	col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !ok {
		return nil, false, &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexed column %q not found", o.plan.Index.Columns[0])}
	}
	parts := []indexProbeKeyPart{{col: col, val: v, pos: o.plan.Key.Pos()}}
	k, encErr := o.ctx.indexProbeKey(o.plan.Index, parts)
	if encErr != nil {
		return nil, false, encErr
	}
	// The SSI bucket tag is derived from the FINGERPRINT of the same parts, not
	// from `k` — see ssiHashProbeFingerprint.
	o.hashProbeFingerprint = ssiHashProbeFingerprint(o.plan.Index, parts)
	return k, true, nil
}

func (o *indexOnlyScanOp) lookupRangeBounds() (lo, hi []byte, ok bool, err error) {
	col, found := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !found {
		return nil, nil, false, &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexed column %q not found", o.plan.Index.Columns[0])}
	}
	if o.plan.LowKey != nil {
		v, evalE := evalExprSlot(o.plan.LowKey, o.outerSlot, o.ctx)
		if evalE != nil {
			return nil, nil, false, evalE
		}
		if v.IsNull() {
			return nil, nil, false, nil
		}
		k, encE := o.ctx.indexProbeKey(o.plan.Index, []indexProbeKeyPart{{col: col, val: v, pos: o.plan.LowKey.Pos()}})
		if encE != nil {
			return nil, nil, false, encE
		}
		lo = k
	}
	if o.plan.HighKey != nil {
		v, evalE := evalExprSlot(o.plan.HighKey, o.outerSlot, o.ctx)
		if evalE != nil {
			return nil, nil, false, evalE
		}
		if v.IsNull() {
			return nil, nil, false, nil
		}
		k, encE := o.ctx.indexProbeKey(o.plan.Index, []indexProbeKeyPart{{col: col, val: v, pos: o.plan.HighKey.Pos()}})
		if encE != nil {
			return nil, nil, false, encE
		}
		hi = k
	}
	return lo, hi, true, nil
}
