package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/multixact"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// followHOTChain walks the HOT chain starting at startSlot on the given
// page and returns the first visible tuple along with its actual slot.
// Returns (HeapTuple{}, 0, false) when no visible tuple exists in the chain.
//
// HOT invariant: all versions in a chain reside on the same page, so no
// additional I/O is needed. The caller must hold at least a read lock on
// the page for the duration of this call.
//
// ItemIDRedirect line pointers (created by opportunistic pruning when a chain
// root is freed) are followed transparently — the redirect leads to the live
// chain tip, skipping the freed slots.
func followHOTChain(page storage.Page, startSlot uint16, snap mvcc.Snapshot, xid storage.TransactionID, mxs *multixact.Store, curcid storage.CommandId, combo *mvcc.ComboCIDStore) (storage.HeapTuple, uint16, bool) {
	const maxChain = 64
	cur := startSlot
	for i := 0; i < maxChain; i++ {
		// Check line-pointer flags before fetching tuple bytes.
		item, err := storage.PageGetItemID(page, cur)
		if err != nil {
			return storage.HeapTuple{}, 0, false
		}
		if item.Flags == storage.ItemIDRedirect {
			// Pruning converted this slot to a redirect. Follow it.
			next := item.Offset // Offset holds the redirect target slot
			if next == cur {
				return storage.HeapTuple{}, 0, false // self-reference guard
			}
			cur = next
			continue
		}
		if item.Flags != storage.ItemIDNormal {
			return storage.HeapTuple{}, 0, false // unused or dead slot
		}
		t, err := storage.PageGetHeapTuple(page, cur)
		if err != nil {
			return storage.HeapTuple{}, 0, false
		}
		if mvcc.TupleVisible(t.Header, snap, xid, curcid, combo, mxs) {
			return t, cur, true
		}
		if t.Header.Infomask&storage.HeapHotUpdated == 0 {
			// Chain end: tuple is not visible and has no successor.
			return storage.HeapTuple{}, 0, false
		}
		next := t.Header.CTID.Offset
		if next == cur {
			return storage.HeapTuple{}, 0, false // self-reference guard
		}
		cur = next
	}
	return storage.HeapTuple{}, 0, false
}

// followHOTChainNoCopy mirrors followHOTChain but uses the no-copy
// PageGetHeapTupleNoCopy variant. Caller MUST hold the page's
// content RLock for the lifetime of the returned tuple — the
// returned tuple.Data aliases the page bytes (M0092-0006).
func followHOTChainNoCopy(page storage.Page, startSlot uint16, snap mvcc.Snapshot, xid storage.TransactionID, mxs *multixact.Store, curcid storage.CommandId, combo *mvcc.ComboCIDStore) (storage.HeapTuple, uint16, bool) {
	const maxChain = 64
	cur := startSlot
	for i := 0; i < maxChain; i++ {
		item, err := storage.PageGetItemID(page, cur)
		if err != nil {
			return storage.HeapTuple{}, 0, false
		}
		if item.Flags == storage.ItemIDRedirect {
			next := item.Offset
			if next == cur {
				return storage.HeapTuple{}, 0, false
			}
			cur = next
			continue
		}
		if item.Flags != storage.ItemIDNormal {
			return storage.HeapTuple{}, 0, false
		}
		t, err := storage.PageGetHeapTupleNoCopy(page, cur)
		if err != nil {
			return storage.HeapTuple{}, 0, false
		}
		if mvcc.TupleVisible(t.Header, snap, xid, curcid, combo, mxs) {
			return t, cur, true
		}
		if t.Header.Infomask&storage.HeapHotUpdated == 0 {
			return storage.HeapTuple{}, 0, false
		}
		next := t.Header.CTID.Offset
		if next == cur {
			return storage.HeapTuple{}, 0, false
		}
		cur = next
	}
	return storage.HeapTuple{}, 0, false
}

// heapChainDeadToAll walks the HOT chain from startSlot testing every
// member against storage.TupleDeadToAll (C3-S2: the executor's analog of
// PG heap_hot_search_buffer's all_dead outcome). It returns true only when
// EVERY reachable member is provably dead to all snapshots — the oracle
// that admits an index entry to the kill list (design D6: strict subset of
// VACUUM's reclaim; a single unprovable member vetoes the kill). A broken
// or recycled chain (Unused/Dead line pointer, decode failure) is
// conservatively NOT dead-to-all: the heap slot may already carry an
// unrelated tuple.
func heapChainDeadToAll(page storage.Page, startSlot uint16, oldestXmin storage.TransactionID) bool {
	const maxChain = 64
	cur := startSlot
	for i := 0; i < maxChain; i++ {
		item, err := storage.PageGetItemID(page, cur)
		if err != nil {
			return false
		}
		if item.Flags == storage.ItemIDRedirect {
			next := item.Offset
			if next == cur {
				return false
			}
			cur = next
			continue
		}
		if item.Flags == storage.ItemIDDead {
			// PG heap_hot_search_buffer parity: a heap LP_DEAD line
			// pointer means prune already proved the chain member dead
			// to all snapshots — the most common post-prune kill case.
			return true
		}
		if item.Flags != storage.ItemIDNormal {
			return false
		}
		t, err := storage.PageGetHeapTupleNoCopy(page, cur)
		if err != nil {
			return false
		}
		if !storage.TupleDeadToAll(t.Header, oldestXmin) {
			return false
		}
		if t.Header.Infomask&storage.HeapHotUpdated == 0 {
			return true // chain ends here; every member was dead-to-all
		}
		next := t.Header.CTID.Offset
		if next == cur {
			return false
		}
		cur = next
	}
	return false
}

// flushKills runs the deferred exclusive-latched marking pass (C3-S3,
// design §4b) over the kill list collected during Next(). Best-effort:
// KillItems drops anything whose leaf changed since capture (D7).
func (o *indexScanOp) flushKills() {
	if len(o.killList) == 0 || o.tree == nil {
		return
	}
	o.tree.KillItems(o.killList)
	o.killList = o.killList[:0]
}

type indexScanOp struct {
	plan *planner.IndexScan
	ctx  *Context
	// M0092-0001: TID-list-eager + heap-fetch-lazy.
	// `tids[i]` holds the (block, index-pointed offset) pair for the
	// i-th match emitted by btree.RangeScan. The HOT-resolved actual
	// slot offset is computed PER Next() and recorded in lastTID for
	// currentTID() — the lockRowsOp consumer.
	//
	// Pre-M0092 the operator also kept `rows []Row` (fully materialised
	// matches via cloneRow per scanFn invocation), which dominated 34 %
	// of allocations in the post-M0091 pgbench select-only profile.
	// The new lazy model decodes one row per Next() into the reusable
	// `scanRow` and returns a slot ALIASING it — caller must consume /
	// Materialize before the next Next() call (standard
	// MaterializedSlot contract).
	tids    []storage.ItemPointer
	idx     int
	lastTID storage.ItemPointer
	hasLast bool

	// C3-S2: physical index-entry positions parallel to tids (from
	// RangeScanWithPos), and the kill list of entries whose whole heap
	// chain proved dead-to-all at the Next() visibility step. S3 turns
	// killList into the deferred exclusive-latched mark pass at
	// Close/Rescan; S2 only collects.
	poss     []btree.ScanPos
	killList []btree.KillItem

	// M0054-0006a: state captured at Open() time and reused across
	// Rescan() calls when the index probe is driven by an outer row
	// from a parent NestedLoopIndexJoin.
	heapRel storage.RelFileNode
	tree    *btree.BTree
	// M0072-0001: outerSlot is the slot the parent NLI bound via
	// BindOuter. The slot's Get(col) is read by lookupKey /
	// lookupRangeBounds / lookupKeys via evalExprSlot. nil when this
	// scan is run from a single-table path (the historical case):
	// then `o.plan.Key` / `LowKey` / `HighKey` must reduce to
	// constants. outerWidth is captured at BindOuter time so the
	// per-call evalExprSlot path has a consistent width hint
	// (preserves the legacy `len(o.outerRow)` bound check
	// equivalence without requiring a Width() method on SlotView).
	outerSlot  SlotView
	outerWidth int

	// scanRow is the per-Next decode buffer; reused across every
	// Next() call. The slot returned by Next() aliases this buffer.
	// Acquired in openPrep from the rowPool (M0068-0004), released
	// in Close.
	scanRow Row

	// M0092-0007: embedded slot reused across every Next() call so
	// we don't allocate a fresh MaterializedSlot per emission.
	// The returned `&o.slot` pointer is stable across calls; its
	// `row` field is overwritten each Next.
	slot MaterializedSlot

	// hashBucketScan is set in Open when this is a SERIALIZABLE equality scan
	// over a HASH index (design 0118-0099). When true the scan takes a
	// bucket-grain SIREAD on the index instead of a relation-grain / per-tuple
	// heap lock, and the per-tuple reads in Next switch to conflict-out-only so
	// they cannot coarsen into a heap-page lock that would re-introduce the
	// different-bucket false positive (predicate-hash spec).
	hashBucketScan bool
}

func newIndexScanOp(p *planner.IndexScan) *indexScanOp {
	return &indexScanOp{plan: p}
}

func (o *indexScanOp) Schema() planner.Schema { return o.plan.Output() }

// Open performs the one-time prep (lock + btree.Open) and then runs
// a single drain pass with no outer row bound (the historical
// single-table-IndexScan path). Parent operators that drive multiple
// probes (M0054-0006 NestedLoopIndexJoin) instead call Open and
// then `Rescan(outerRow)` per outer row.
//
// When o.ctx is already set (operator reused for a correlated scalar
// subquery across multiple outer rows), Open skips openPrep — the lock
// and btree handle are still valid — and just rescans with the new
// outer context. This avoids repeated lock-acquire + btree.Open
// overhead in the subqueryImpl correlated-operator cache path.
func (o *indexScanOp) Open(ctx *Context) error {
	if o.ctx != nil {
		// Reopen: lock already held, btree handle still valid.
		// Update context (new ctx.OuterRows from evalSubquery) and rescan.
		o.ctx = ctx
		return o.Rescan(nil, 0)
	}
	if err := o.openPrep(ctx); err != nil {
		return err
	}
	return o.Rescan(nil, 0)
}

// openPrep does the one-time setup that is independent of any outer
// row binding: context capture, relation lock acquisition, and
// btree.Open. NLI parents call this directly and then issue
// `Rescan(outerRow)` once per outer row without re-acquiring locks
// or re-opening the index.
func (o *indexScanOp) openPrep(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "IndexScan requires storage handles in Context"}
	}
	if o.plan.Table != nil && !dmlPrivilegePermittedAs(ctx, o.plan.Table, "SELECT", selectPrivilegeCheckRole(ctx, o.plan.PrivilegeCheckRoleSet, o.plan.PrivilegeCheckRole)) {
		return &ExecError{Code: "42501", Pos: o.plan.Pos(), Message: fmt.Sprintf("permission denied for table %s", o.plan.Table.Name)}
	}
	o.ctx = ctx
	o.tids = nil
	o.poss = nil
	o.killList = nil
	o.idx = 0
	o.hasLast = false
	o.outerSlot = nil
	o.outerWidth = 0

	o.heapRel = ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(o.heapRel, lockmgr.AccessShareLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	if err := ctx.acquireScanReadLockTxn(o.heapRel); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	// PostgreSQL locks every index of the scanned relation in AccessShare, not
	// only the one this scan probes (get_relation_info opens them all). M0118-0008
	// (partition-drop-index-locking).
	if err := ctx.acquireScanIndexReadLocksTxn(o.plan.Table); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	// M0118-0001: the SERIALIZABLE index-scan SIREAD predicate lock is no
	// longer acquired eagerly here. Its granularity now depends on what the
	// probe matches and is decided at the end of Rescan once the matching TID
	// set is known (ssiRecordIndexScanGapLock): a matched equality probe relies
	// on the exact per-tuple SIREAD locks recorded in Next, while an empty
	// equality probe (the read-write-unique phantom gap) or a range scan falls
	// back to the relation-grain SIREAD. See ssiRecordIndexScanGapLock for the
	// multiple-row-versions rationale.
	idxRel := ctx.Catalog.IndexRelFileNode(o.plan.Index)
	tree, err := openIndexBTree(ctx, o.plan.Index, idxRel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.tree = tree
	return nil
}

// BindOuter is invoked by the M0054-0006 NestedLoopIndexJoin parent
// before each Rescan. The bound slot is the input to evalExprSlot
// when resolving Key / LowKey / HighKey expressions that reference
// outer columns. (M0072-0001: was BindOuter(row Row); reads now go
// through SlotView.Get(col) so the NLI parent passes its persistent
// outerMS slot directly without the legacy `boundRow` concat.)
func (o *indexScanOp) BindOuter(slot SlotView, outerWidth int) {
	o.outerSlot = slot
	o.outerWidth = outerWidth
}

// Rescan re-drains the underlying index after binding an outer slot.
// The historical single-table-IndexScan path calls Open which calls
// Rescan(nil, 0); the M0054-0006 NLI path calls Open once then Rescan
// per outer row.
func (o *indexScanOp) Rescan(outerSlot SlotView, outerWidth int) error {
	o.flushKills() // C3-S3: mark pending kills before discarding them
	o.tids = o.tids[:0]
	o.poss = o.poss[:0]
	o.killList = o.killList[:0]
	o.idx = 0
	o.hasLast = false
	o.outerSlot = outerSlot
	o.outerWidth = outerWidth

	if o.tree == nil {
		// Defensive: openPrep must have been called.
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "indexScanOp.Rescan called before Open"}
	}

	// M0118-0001: only a FULL-KEY point lookup on a single-column index can
	// rely on the exact per-tuple SIREAD locks recorded in Next when it
	// matches. A leading-column probe on a composite index (e.g.
	// read-write-unique-4's `WHERE year = 2016` on PK (year, invoice_number))
	// is a RANGE over the unspecified trailing columns: it matches existing
	// rows yet still has a gap a concurrent (2016, N) INSERT could fall into,
	// so it must keep the relation-grain gap lock. Range scans likewise. The
	// decision is finalised by ssiRecordIndexScanGapLock once o.tids is known
	// (after RangeScan, or on the unbound/NULL-key early returns).
	isFullKeyProbe := len(o.plan.Index.Columns) == 1 &&
		(o.plan.Key != nil || len(o.plan.Keys) > 0)
	// A hash index supports only single-column equality, so any full-key probe
	// over a declared-hash index is a bucket probe (design 0118-0099). Mark it so
	// the gap-lock and per-tuple-read paths use bucket-grain predicate locking.
	o.hashBucketScan = isFullKeyProbe && o.plan.Index.DeclaredHash

	var loBytes, hiBytes []byte
	if len(o.plan.Keys) > 0 {
		// Multi-column equality probe (M0054-0006-followup-Q9-
		// composite). Encode each leading column from
		// `Index.Columns[0..len(Keys)-1]` in order. The planner
		// guarantees `len(Keys) == len(Index.Columns)` whenever
		// `Keys` is non-empty, so no suffix padding is required —
		// we synthesise a full equality probe.
		key, ok, err := o.lookupKeys()
		if err != nil {
			return err
		}
		if !ok {
			o.ssiRecordIndexScanGapLock(isFullKeyProbe)
			return nil
		}
		loBytes = key
		hiBytes = key
	} else if o.plan.Key != nil {
		// Single-column equality scan: probe key is both lo and hi.
		key, ok, err := o.lookupKey()
		if err != nil {
			return err
		}
		if !ok {
			o.ssiRecordIndexScanGapLock(isFullKeyProbe)
			return nil
		}
		loBytes = key
		hiBytes = key
		// Composite-index leading-column probe (M0053-0001): the inclusive
		// upper bound must cover every key whose leading columns equal `key`.
		// How that is expressed depends on the key format — see
		// (*Context).compositeUpperBound.
		if len(o.plan.Index.Columns) > 1 {
			hiBytes = o.ctx.compositeUpperBound(o.plan.Index, key)
		}
	} else {
		// Range scan: evaluate lo/hi bounds independently.
		lo, hiB, ok, err := o.lookupRangeBounds()
		if err != nil {
			return err
		}
		if !ok {
			o.ssiRecordIndexScanGapLock(isFullKeyProbe)
			return nil
		}
		loBytes = lo
		hiBytes = hiB
		if len(o.plan.Index.Columns) > 1 && hiBytes != nil {
			hiBytes = o.ctx.compositeUpperBound(o.plan.Index, hiBytes)
		}
	}

	// M0092-0001: lazy iteration. The scanFn collects only TIDs;
	// HOT-chain follow + decode + detoast happen per Next() so the
	// produced row aliases scanRow (no cloneRow per match).
	scanFn := func(_ []byte, ptr storage.ItemPointer, pos btree.ScanPos) (bool, error) {
		o.tids = append(o.tids, ptr)
		o.poss = append(o.poss, pos) // C3-S2: kill-list coordinates
		return true, nil
	}

	if err := o.tree.RangeScanWithPos(loBytes, hiBytes, scanFn); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	if o.hashBucketScan && len(loBytes) > 0 {
		// Bucket-grain SIREAD on the hash index in place of the relation-grain
		// gap lock (design 0118-0099). loBytes is the encoded equality key.
		ssiRecordHashBucketRead(o.ctx, o.heapRel.DBOid, o.plan.Index.OID, loBytes)
	}
	o.ssiRecordIndexScanGapLock(isFullKeyProbe)
	return nil
}

// ssiRecordIndexScanGapLock finalises the SERIALIZABLE index-scan SIREAD
// predicate lock after Rescan has determined the matching TID set.
//
// A matched full-key probe (isFullKeyProbe && len(tids) > 0) relies on the
// exact per-tuple SIREAD locks recorded in Next: each locks the precise
// (block, slot) version the reader actually observed. A coarser relation-grain
// lock would over-approximate — it covers EVERY version of the matched key, so
// a later SERIALIZABLE writer that overwrites a DIFFERENT version (one the
// reader never read) would spuriously find this reader in its conflict-in walk
// and install a false rw-edge. That false edge is exactly what made the
// multiple-row-versions spec over-abort (the intermediate s2 write produces a
// version s3 overwrites, which s1 never read, so s1→s3 must NOT form).
//
// isFullKeyProbe is true only for a point lookup that binds every index column
// of a single-column index. A leading-column probe on a composite index, an
// empty probe (the read-write-unique phantom gap, where no tuple was read so no
// per-tuple lock exists), and a range scan all retain the relation-grain SIREAD
// — the walk-reachable analog of upstream's btree PredicateLockPage — because
// each leaves a gap a phantom INSERT could fall into. ssiRecordRelationRead
// gates to SERIALIZABLE and excludes system relations; temp / matview relations
// are filtered here.
func (o *indexScanOp) ssiRecordIndexScanGapLock(isFullKeyProbe bool) {
	if isFullKeyProbe && len(o.tids) > 0 {
		return
	}
	if o.hashBucketScan {
		// A hash bucket probe never takes the relation-grain gap lock — the
		// bucket-grain SIREAD (taken in Open from the probe key) is the whole
		// phantom mechanism, even when the probe matched no tuple. Design
		// 0118-0099.
		return
	}
	if o.plan.Table == nil || (!o.plan.Table.Temp && !o.plan.Table.IsMatView) {
		ssiRecordRelationRead(o.ctx, o.heapRel)
	}
}

func (o *indexScanOp) Next() (TupleSlot, error) {
	// M0092-0001: lazy iteration. Pin heap, follow HOT, decode into
	// the reusable scanRow, return slot aliasing it. Caller must
	// consume / Materialize before the next Next() call.
	// Loop instead of recursion to bound stack growth on workloads
	// that skip many invisible tuples (vacuum-pending dead rows).
	for {
		if o.idx >= len(o.tids) {
			o.hasLast = false
			return nil, EOF
		}
		ptr := o.tids[o.idx]
		o.idx++
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: o.heapRel, Block: ptr.Block})
		if err != nil {
			return nil, err
		}
		// M0092-0006: hold the RLock across decode so we can use
		// followHOTChainNoCopy → tuple.Data aliases the page bytes.
		// The RLock blocks heap writers on this page for the
		// duration of one tuple decode (~hundreds of ns for int
		// rows, ~µs for wide rows) — bounded write-starvation,
		// acceptable per the M0091-0002 audit.
		slot.RLock()
		tuple, actualSlot, found := followHOTChainNoCopy(slot.Page(), ptr.Offset, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.MultiXact, o.ctx.CmdID, o.ctx.comboStore())
		if !found {
			// C3-S2 kill-list oracle: invisible-to-me is upgraded to
			// dead-to-ALL only when every HOT-chain member proves dead
			// under the OldestXmin horizon (the same predicate prune/
			// VACUUM use — design D6). tidIdx is o.idx-1: Next() has
			// already advanced past the tid it is resolving.
			if o.ctx.TxnMgr != nil {
				if tidIdx := o.idx - 1; tidIdx >= 0 && tidIdx < len(o.poss) {
					if heapChainDeadToAll(slot.Page(), ptr.Offset, o.ctx.TxnMgr.OldestXmin()) {
						o.killList = append(o.killList, btree.KillItem{Pos: o.poss[tidIdx], Ptr: ptr})
					}
				}
			}
			// M0118-0001: SSI phantom conflict-out for an index-scanned tuple
			// that is physically present at this TID but invisible to us because
			// a concurrent transaction inserted it. Register the reader→inserter
			// rw-edge — the index-scan analog of the seq-scan invisible-tuple
			// path (operators_storage.go) — so an INSERT-before-READ ordering
			// still closes the dangerous structure (read-write-unique-3: each
			// session probes the key, finds the peer's in-flight insert
			// invisible, then inserts the duplicate itself). ssiActive gates the
			// extra page read to SERIALIZABLE; the Manager filters the aborted /
			// committed-before-snapshot (wr-dependency) cases.
			var invisXmin storage.TransactionID
			if ssiActive(o.ctx) {
				if raw, terr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset); terr == nil {
					invisXmin = raw.Header.Xmin
				}
			}
			slot.RUnlock()
			o.ctx.Pool.Unpin(slot)
			// Tuple invisible (deleted / not yet committed at snap);
			// register the SSI conflict-out then skip this TID.
			if invisXmin != storage.InvalidTransactionID {
				if serr := ssiRecordInvisibleTupleRead(o.ctx, o.heapRel, invisXmin); serr != nil {
					return nil, serr
				}
			}
			continue
		}
		if o.scanRow == nil || len(o.scanRow) != len(o.plan.Table.Columns) {
			o.scanRow = acquireRow(len(o.plan.Table.Columns))
		}
		decErr := DecodeHeapTupleRowInto(o.scanRow, o.plan.Table.Columns, tuple, nil)
		slot.RUnlock()
		o.ctx.Pool.Unpin(slot)
		if decErr != nil {
			continue // skip undecodable tuple (consistent with scanMatching)
		}
		row := o.scanRow
		// Convert KindString enum column values to KindEnum (sort order) so
		// Filter predicates can compare by declaration order. M0097-0022.
		if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
			for i, col := range o.plan.Table.Columns {
				if et, isEnum := im.LookupEnum(col.Type.Name); isEnum && i < len(row) {
					if row[i].Kind == KindString {
						label := row[i].StringValue()
						for _, ev := range et.Values {
							if ev.Label == label {
								row[i] = NewEnumDatum(ev.SortOrder, label)
								break
							}
						}
					}
				}
			}
		}
		if needsDetoast(row) {
			detoasted, err := DetoastRow(o.ctx, o.heapRel, o.plan.Table.Columns, row)
			if err != nil {
				// Skip undetoastable tuple, try the next TID.
				continue
			}
			row = detoasted
		}
		// M0119-0004-ACLHEAP: render a heap-backed ACL column (pg_type.typacl /
		// pg_attribute.attacl) from its _aclitem blob to canonical aclitemout text.
		// pg_dump's getColumnACLs reaches pg_attribute by an attrelid index scan, so
		// the seqScanOp inline hook is not enough — render here too (no-op off the
		// catalogs / for a NULL ACL).
		renderHeapACLColumnInto(o.ctx.Catalog, o.plan.Table, o.plan.Table.Columns, row)
		// Record the actual (HOT-resolved) live slot for
		// currentTID() — lockRowsOp stamps the live version.
		o.lastTID = storage.ItemPointer{Block: ptr.Block, Offset: actualSlot}
		o.hasLast = true
		// M0104-0007: SSI read-path hook on the HOT-resolved live slot.
		// Helper short-circuits for RC/RR; for SERIALIZABLE this installs a
		// tuple-grain predicate lock and an rw-conflict edge to the writer
		// identified by the visible tuple's xmin.
		// M0118-0001: a non-nil error means the reader closed a dangerous
		// structure to an already-committed writer and must abort the scan
		// mid-statement (40001). The heap page RLock/pin was already released
		// above (after the decode), so just propagate the error.
		if o.hashBucketScan {
			// Conflict-out only: the bucket-grain SIREAD on the index (Open)
			// covers the phantom; a heap tuple SIREAD here would coarsen to a
			// heap-page lock and re-introduce the different-bucket false positive
			// (design 0118-0099). The write-before-read same-bucket edge still
			// forms via this conflict-out against the in-flight inserter's xmin.
			if err := ssiConflictOutTupleRead(o.ctx, tuple.Header.Xmin, tuple.Header.Xmax); err != nil {
				return nil, err
			}
		} else if err := ssiRecordTupleRead(o.ctx, o.heapRel, ptr.Block, actualSlot, tuple.Header.Xmin, tuple.Header.Xmax); err != nil {
			return nil, err
		}
		// M0092-0007: stack-aliased slot — reuse o.slot across
		// every Next() call. Caller must consume / Materialize
		// before the next Next() invocation.
		// M0128-P6.1 resjunk-ctid rowmark: when the scan's schema
		// has been extended with a trailing ctid column, append
		// the TID as a string datum so it rides the row through
		// the plan tree.
		tableCols := len(o.plan.Table.Columns)
		if len(o.Schema()) > tableCols {
			for i := tableCols; i < len(o.Schema()); i++ {
				row = append(row, NewStringDatum(fmt.Sprintf("(%d,%d)", ptr.Block, actualSlot)))
			}
		}
		o.slot.schema = o.Schema()
		o.slot.row = row
		return &o.slot, nil
	}
}

func (o *indexScanOp) Close() error {
	o.flushKills() // C3-S3: mark pending kills before releasing state
	o.tids = nil
	o.poss = nil
	o.killList = nil
	o.hasLast = false
	if o.scanRow != nil {
		releaseRow(o.scanRow)
		o.scanRow = nil
	}
	return nil
}

// currentTID returns the (rel, ItemPointer) of the most recently
// emitted row, or ok=false before the first Next() call / past
// EOF. Mirrors seqScanOp.currentTID for the index-scan path so
// lockRowsOp can stamp per-row lock-only xmax (M0021 step 2c).
//
// M0092-0001: returns the HOT-resolved actual slot (lastTID),
// recorded by Next() during the HOT-chain follow. Before M0092
// the operator pre-collected `tids[]` of HOT-resolved offsets
// during scanFn; the lazy refactor moves HOT-follow to Next()
// and stashes the result in lastTID.
func (o *indexScanOp) currentTID() (storage.RelFileNode, storage.ItemPointer, bool) {
	if !o.hasLast {
		return storage.RelFileNode{}, storage.ItemPointer{}, false
	}
	rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	return rel, o.lastTID, true
}

// lookupKeys evaluates each `Keys[i]` against the bound outer row
// and concatenates the per-column B-tree encodings to form the
// multi-column equality probe key. Returns ok=false when any
// component evaluates to NULL — equality on NULL is unknown, so
// the probe correctly produces zero rows. (M0054-0006-followup-
// Q9-composite.)
func (o *indexScanOp) lookupKeys() ([]byte, bool, error) {
	if len(o.plan.Keys) != len(o.plan.Index.Columns) {
		// Defensive: the planner is contractually required to
		// supply one Key per index column. A mismatch here is a
		// planner bug, surfaced as runtime XX000 with the index
		// name so the bug is named at the failure site.
		return nil, false, &ExecError{
			Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexScanOp.lookupKeys: planner supplied %d keys for index %q with %d columns", len(o.plan.Keys), o.plan.Index.Name, len(o.plan.Index.Columns)),
		}
	}
	var probe []byte
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
		segment, encErr := encodeBTreeKeyForColumn(v, col, ke.Pos())
		if encErr != nil {
			return nil, false, encErr
		}
		probe = append(probe, segment...)
	}
	return probe, true, nil
}

func (o *indexScanOp) lookupKey() ([]byte, bool, error) {
	v, err := evalExprSlot(o.plan.Key, o.outerSlot, o.ctx)
	if err != nil {
		return nil, false, err
	}
	if v.IsNull() {
		return nil, false, nil
	}
	// Look up the indexed column on the underlying table so the
	// probe encoding matches what backfill stored. The index is
	// always single-column in v0 (createSingleColumnBTreeIndex
	// enforces this).
	col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !ok {
		return nil, false, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: fmt.Sprintf("indexed column %q not found on table %q", o.plan.Index.Columns[0], o.plan.Table.Name)}
	}
	key, encErr := encodeBTreeKeyForColumn(v, col, o.plan.Key.Pos())
	if encErr != nil {
		return nil, false, encErr
	}
	return key, true, nil
}

// lookupRangeBounds evaluates LowKey and HighKey for a range scan.
// Returns (loKey, hiKey, ok, err). ok=false when a non-nil bound
// evaluates to NULL (the scan should produce no rows). Either loKey
// or hiKey may be nil for an open-ended range.
func (o *indexScanOp) lookupRangeBounds() (loKey []byte, hiKey []byte, ok bool, err error) {
	col, found := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !found {
		return nil, nil, false, &ExecError{
			Code:    "XX000",
			Pos:     o.plan.Pos(),
			Message: fmt.Sprintf("indexed column %q not found on table %q", o.plan.Index.Columns[0], o.plan.Table.Name),
		}
	}

	if o.plan.LowKey != nil {
		v, evalErr := evalExprSlot(o.plan.LowKey, o.outerSlot, o.ctx)
		if evalErr != nil {
			return nil, nil, false, evalErr
		}
		if v.IsNull() {
			// NULL lower bound → skip entire scan (no row can satisfy >= NULL)
			return nil, nil, false, nil
		}
		k, encErr := encodeBTreeKeyForColumn(v, col, o.plan.LowKey.Pos())
		if encErr != nil {
			return nil, nil, false, encErr
		}
		loKey = k
	}

	if o.plan.HighKey != nil {
		v, evalErr := evalExprSlot(o.plan.HighKey, o.outerSlot, o.ctx)
		if evalErr != nil {
			return nil, nil, false, evalErr
		}
		if v.IsNull() {
			// NULL upper bound → skip entire scan (no row can satisfy <= NULL)
			return nil, nil, false, nil
		}
		k, encErr := encodeBTreeKeyForColumn(v, col, o.plan.HighKey.Pos())
		if encErr != nil {
			return nil, nil, false, encErr
		}
		hiKey = k
	}

	// ok = true as long as at least one bound is specified (the scan is valid)
	return loKey, hiKey, true, nil
}

// compositeUpperPaddingLen is how many 0xFF bytes are appended to a
// leading-column key to form an inclusive upper bound for a composite
// index probe (M0053-0001). It must exceed the maximum suffix-column
// encoding for any plausible composite key. 64 bytes covers up to
// ~8 trailing int4/int8 columns, ~3 NUMERIC(38) columns, or 1 varchar(60).
// PostgreSQL's MaxHighKeyLen on goopg is 32, but leaf keys are not
// truncated, so a generous bound is required.
const compositeUpperPaddingLen = 64

// appendCompositeUpperPadding returns key with `compositeUpperPaddingLen`
// trailing 0xFF bytes. Caller-owned slice; the input is not aliased.
func appendCompositeUpperPadding(key []byte) []byte {
	out := make([]byte, len(key)+compositeUpperPaddingLen)
	copy(out, key)
	for i := len(key); i < len(out); i++ {
		out[i] = 0xFF
	}
	return out
}
