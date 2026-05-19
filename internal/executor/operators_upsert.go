package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// upsertOp executes `INSERT … ON CONFLICT (cols) DO {NOTHING |
// UPDATE SET … [WHERE …]}` (M0017-0003 — Stage A runtime). The
// planner has already (a) resolved the arbiter unique index, (b)
// resolved SET / WHERE expressions against a target+excluded scope
// where ColumnRef indices 0..N-1 reference the existing tuple and
// indices N..2N-1 reference the inserted tuple. This op consumes
// the planner state at runtime:
//
//  1. For each child row, build the target-shape inserted row.
//  2. Encode the conflict key from the inserted row's
//     OnConflict.ArbiterColumns slots.
//  3. Probe OnConflict.ArbiterIndex via btree.RangeScan; for each
//     matching ItemPointer, fetch the heap tuple and check
//     visibility under ctx.Snap. The first visible match is the
//     conflict tuple.
//  4. No conflict → writeHeapRow + insert (key → newPtr) into the
//     arbiter index so subsequent rows in the same statement see
//     the new entry.
//  5. Conflict + DO NOTHING → skip the row (no rowsAffected bump).
//  6. Conflict + DO UPDATE → build a 2N-wide merged tuple
//     (existing || inserted), evaluate UpdateWhere, then evaluate
//     each non-nil UpdateSet[i]; nil slots inherit existing[i].
//     Stamp xmax on the existing tuple, writeHeapRow the new
//     tuple, and insert (key → newPtr) into the arbiter index so
//     a future probe walks both old (dead, will be skipped via
//     visibility) and new (live) entries.
//
// Stage A scope:
//   - Conflict-key columns must not be modified by UpdateSet —
//     otherwise the index entry for the original key would point
//     at a tuple whose actual data has a different key, breaking
//     future probes. Enforced at Open() time so the runtime check
//     fires before any heap mutation.
//   - The arbiter is required for every shape except `ON CONFLICT
//     DO NOTHING` (the bare no-target form). The bare form is
//     handled as a plain insert in v0 — no other unique
//     constraints exist in v0 catalogs, so there's nothing to
//     check; this is consistent with upstream's "any unique
//     constraint" semantics narrowed to v0's single-constraint
//     reality.
//   - Indexes other than the arbiter are not maintained on
//     INSERT. This is a pre-existing v0 limitation that
//     M0017-0003 inherits — non-arbiter indexes are populated via
//     CREATE INDEX backfill.
type upsertOp struct {
	plan         *planner.Insert
	ctx          *Context
	child        Operator
	rowsAffected int64
	done         bool
	// arbiterTree is opened lazily on first conflict probe (or
	// first row insert when ArbiterIndex != nil) and kept across
	// the statement for cheap reuse. nil when ArbiterIndex is nil
	// (the bare DO NOTHING form). For partitioned targets, this
	// is swapped per-row to point at the routed leaf partition's
	// matching arbiter index (M0100-0005t).
	arbiterTree *btree.BTree
	// leafTrees caches per-leaf-partition arbiter btree handles so
	// multi-row UPSERTs over the same partition reuse a single
	// open tree.  Keyed by leaf table OID.  M0100-0005t.
	leafTrees map[uint32]*btree.BTree
}

// RowsAffected satisfies executor.RowCounter — for UPSERT, the
// upstream contract is "INSERT/UPDATE rows changed". DO NOTHING
// rows that hit a conflict do NOT bump the counter (mirrors
// upstream).
func (o *upsertOp) RowsAffected() int64 { return o.rowsAffected }

func newUpsertOp(p *planner.Insert, child Operator) *upsertOp {
	return &upsertOp{plan: p, child: child}
}

func (o *upsertOp) Schema() planner.Schema { return nil }

func (o *upsertOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "Insert requires storage handles in Context"}
	}
	if o.plan.OnConflict == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "upsertOp built without OnConflict plan"}
	}
	o.ctx = ctx

	// Stage A guard: DO UPDATE SET cannot rewrite conflict-key
	// columns. The arbiter index entry for the original key would
	// still point at the new tuple, but the new tuple's actual
	// key bytes differ — future probes would land on the wrong
	// row. Reject loudly rather than silently corrupting the
	// index.
	if o.plan.OnConflict.Action == planner.OnConflictActionUpdate {
		for _, ord := range o.plan.OnConflict.ArbiterColumns {
			if o.plan.OnConflict.UpdateSet[ord] != nil {
				col := o.plan.Table.Columns[ord]
				return &ExecError{
					Code:    "0A000",
					Pos:     o.plan.Pos(),
					Message: fmt.Sprintf("ON CONFLICT DO UPDATE may not modify conflict-key column %q in v0 (Stage A)", col.Name),
				}
			}
		}
	}

	rel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(rel, lockmgr.RowExclusiveLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	if o.plan.OnConflict.ArbiterIndex != nil {
		idxRel := ctx.Catalog.IndexRelFileNode(o.plan.OnConflict.ArbiterIndex)
		tree, err := btree.Open(ctx.Pool, idxRel)
		if err != nil {
			return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
		}
		o.arbiterTree = tree
	}
	return nil
}

func (o *upsertOp) Close() error { return o.child.Close() }

func (o *upsertOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	parentRel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	parentCols := o.plan.Table.Columns
	parentTree := o.arbiterTree
	isPartitioned := len(o.plan.Table.PartitionKey) > 0
	for {
		srcSlot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		src := slotRow(srcSlot)
		// Reorder source row → target column order via plan.ColumnIndex.
		inserted := make(Row, len(parentCols))
		for i := range parentCols {
			inserted[i] = NullDatum
		}
		for srcIdx, tgtIdx := range o.plan.ColumnIndex {
			inserted[tgtIdx] = src[srcIdx]
		}

		// M0100-0005t: partition routing for INSERT … ON CONFLICT.  When
		// the target is partitioned, route the row to a leaf and swap
		// the arbiter tree to the leaf's matching unique/PK index.  The
		// parent's arbiter index is empty (writes go to leaves), so
		// probing it would miss every live duplicate.
		rel := parentRel
		cols := parentCols
		writeTbl := o.plan.Table
		if isPartitioned {
			leaf, leafTree, ferr := o.routeAndOpenLeaf(inserted)
			if ferr != nil {
				return nil, ferr
			}
			if leaf == nil {
				return nil, &ExecError{Code: "23514", Pos: o.plan.Pos(),
					Message: fmt.Sprintf("no partition of relation %q found for row", o.plan.Table.Name)}
			}
			rel = o.ctx.Catalog.RelFileNode(leaf)
			cols = leaf.Columns
			writeTbl = leaf
			o.arbiterTree = leafTree
		}

		conflictPtr, conflictRow, conflicted, err := o.probeArbiterWaiting(rel, cols, inserted)
		if err != nil {
			return nil, err
		}
		if !conflicted {
			if err := o.applyInsert(rel, cols, inserted); err != nil {
				return nil, err
			}
			o.rowsAffected++
			_ = writeTbl
			continue
		}
		switch o.plan.OnConflict.Action {
		case planner.OnConflictActionNothing:
			// Skip silently — RowsAffected does NOT bump.
		case planner.OnConflictActionUpdate:
			updated, skip, err := o.evalUpdate(conflictRow, inserted)
			if err != nil {
				return nil, err
			}
			if skip {
				continue
			}
			if err := o.applyUpdate(rel, cols, conflictPtr, updated); err != nil {
				return nil, err
			}
			o.rowsAffected++
		default:
			return nil, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: fmt.Sprintf("unexpected OnConflictAction %d", o.plan.OnConflict.Action)}
		}
	}
	// Restore parent's tree handle so Close() releases a stable resource.
	o.arbiterTree = parentTree
	return nil, EOF
}

// routeAndOpenLeaf finds the partition leaf that the row maps to and
// returns the leaf table plus a cached btree handle for the leaf's
// arbiter index (the unique/primary index whose column list matches
// o.plan.OnConflict.ArbiterIndex).  Returns (nil, nil, nil) when the
// row does not map to any partition.  M0100-0005t.
func (o *upsertOp) routeAndOpenLeaf(inserted Row) (*catalog.Table, *btree.BTree, error) {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil, nil, nil
	}
	leaf := routeToPartition(o.plan.Table, inserted, im)
	if leaf == nil {
		return nil, nil, nil
	}
	if o.leafTrees == nil {
		o.leafTrees = make(map[uint32]*btree.BTree)
	}
	if tree, hit := o.leafTrees[leaf.OID]; hit {
		return leaf, tree, nil
	}
	leafIdx := o.resolveLeafArbiter(leaf)
	if leafIdx == nil {
		// No matching arbiter on the leaf — leave tree nil so probe
		// short-circuits as "no entries"; applyInsert falls through
		// to writeHeapRowReturning + index maintenance no-op.
		o.leafTrees[leaf.OID] = nil
		return leaf, nil, nil
	}
	idxRel := o.ctx.Catalog.IndexRelFileNode(leafIdx)
	tree, err := btree.Open(o.ctx.Pool, idxRel)
	if err != nil {
		return nil, nil, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.leafTrees[leaf.OID] = tree
	return leaf, tree, nil
}

// resolveLeafArbiter finds the leaf-partition index whose column list
// (and primary/unique flag) matches the parent's planner-resolved
// arbiter index.  M0100-0005t.
func (o *upsertOp) resolveLeafArbiter(leaf *catalog.Table) *catalog.Index {
	parentIdx := o.plan.OnConflict.ArbiterIndex
	if parentIdx == nil || o.ctx.Catalog == nil {
		return nil
	}
	for _, idx := range o.ctx.Catalog.IndexesOnTable(leaf) {
		if parentIdx.Primary && !idx.Primary {
			continue
		}
		if !parentIdx.Primary && !idx.Unique {
			continue
		}
		if len(idx.Columns) != len(parentIdx.Columns) {
			continue
		}
		ok := true
		for i := range idx.Columns {
			if !strings.EqualFold(idx.Columns[i], parentIdx.Columns[i]) {
				ok = false
				break
			}
		}
		if ok {
			return idx
		}
	}
	return nil
}

// probeArbiter looks up the inserted row's conflict key in the
// arbiter index, returns the first visible heap tuple (if any) as
// the conflicting row, and the ItemPointer that addresses it.
//
// Returns (ptr, existingRow, true, nil) on conflict;
// (_, nil, false, nil) on no conflict;
// (_, _, _, err) on storage / btree errors.
//
// Invisible (dead/aborted) tuples that the index still references
// are skipped — this is essential because UPSERT writes new
// tuples and inserts duplicate index entries, so historical dead
// versions may still be reachable via the same key.
func (o *upsertOp) probeArbiter(rel storage.RelFileNode, cols []catalog.Column, inserted Row) (storage.ItemPointer, Row, bool, error) {
	if o.arbiterTree == nil {
		return storage.ItemPointer{}, nil, false, nil
	}
	key, err := encodeArbiterKey(o.plan.OnConflict, o.plan.Table, inserted, o.plan.Pos())
	if err != nil {
		return storage.ItemPointer{}, nil, false, err
	}
	if key == nil {
		// NULLs in conflict-key columns never collide per
		// upstream semantics — DO NOTHING / DO UPDATE both fall
		// through to a plain insert.
		return storage.ItemPointer{}, nil, false, nil
	}
	var (
		foundPtr storage.ItemPointer
		foundRow Row
		found    bool
	)
	scanErr := o.arbiterTree.RangeScan(key, key, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		tuple, err := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
		slot.RUnlock()
		o.ctx.Pool.Unpin(slot)
		if err != nil {
			if errors.Is(err, storage.ErrUnsupportedItem) {
				return true, nil
			}
			return false, err
		}
		// M0100-0005x: upstream `_bt_check_unique` probes with
		// HeapTupleSatisfiesDirty, which sees committed-after-snapshot
		// commits and aborted deletes.  isLiveForUniqueCheck implements
		// that subset: xmin live iff not aborted (committed-after counts
		// as live); xmax dead iff committed (regardless of whether our
		// frozen RR snapshot still thinks the deleter is in-progress).
		// This is required so a wait-then-recheck on Case 2 (in-flight
		// delete that commits during wait) clears the apparent conflict
		// and lets the INSERT proceed — partition-key-update-3.spec
		// permutations 1/5.  TupleVisible would still report the dead
		// row as visible under RR's frozen snapshot.
		if !isLiveForUniqueCheck(o.ctx, tuple.Header.Xmin, tuple.Header.Xmax) {
			return true, nil
		}
		row, err := DecodeRow(cols, tuple.Data)
		if err != nil {
			return false, err
		}
		foundPtr = ptr
		foundRow = row
		found = true
		return false, nil
	})
	if scanErr != nil {
		return storage.ItemPointer{}, nil, false, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: scanErr.Error()}
	}
	return foundPtr, foundRow, found, nil
}

// probeArbiterWaiting wraps probeArbiter with row-wait semantics:
// if a non-visible tuple from an in-progress transaction is found,
// block until that transaction commits or aborts, then re-probe.
// This implements the "speculative insert" blocking that makes
// INSERT … ON CONFLICT produce correct <waiting ...> output in
// concurrent isolation tests.
func (o *upsertOp) probeArbiterWaiting(rel storage.RelFileNode, cols []catalog.Column, inserted Row) (storage.ItemPointer, Row, bool, error) {
	for {
		// First look for an in-progress transaction that could change
		// the probe outcome (in-flight insert with our key, or
		// in-flight delete of a visible match). If one exists, wait
		// for it to settle and re-probe under a fresh snapshot.
		inProgressXID, isInFlightInsert, hasInProgress := o.findInProgressConflict(rel, cols, inserted)
		if !hasInProgress {
			return o.probeArbiter(rel, cols, inserted)
		}
		qctx := o.ctx.Ctx
		if qctx == nil {
			qctx = context.Background()
		}
		if o.ctx.TxnMgr != nil {
			if werr := o.ctx.TxnMgr.WaitForXID(qctx, inProgressXID); werr != nil {
				// Context cancelled (e.g. IsolationRunner drain timeout).
				return storage.ItemPointer{}, nil, false, nil
			}
		}
		// M0100-0005x: under RR / SERIALIZABLE, if the in-flight conflict
		// was an INSERT (Case 1 — xmin in-progress) and the inserter
		// committed, the resulting unique conflict is invisible to our
		// frozen snapshot.  Upstream `_bt_check_unique` (via DirtySnapshot)
		// sees the row and aborts the inserter with 40001 rather than
		// silently proceeding with a duplicate or silently skipping.
		// Case 2 (in-flight delete on a visible row) does NOT raise —
		// the deletion clears the apparent conflict, INSERT proceeds.
		if isInFlightInsert && o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
			if o.ctx.TxnMgr != nil && !o.ctx.TxnMgr.HasAbortedXID(inProgressXID) {
				return storage.ItemPointer{}, nil, false, &ExecError{
					Code:    "40001",
					Pos:     o.plan.Pos(),
					Message: "could not serialize access due to concurrent update",
				}
			}
		}
		if o.ctx.TxnMgr != nil && o.ctx.Tx.Handle != 0 {
			if snap, serr := o.ctx.TxnMgr.SnapshotFor(o.ctx.Tx); serr == nil {
				o.ctx.Snap = snap.Clone()
			}
		}
	}
}

// findInProgressConflict scans the arbiter index for a tuple whose xmin
// is from a currently in-progress transaction (not yet committed/aborted).
// Returns the in-progress XID and true if found; (0, false) otherwise.
func (o *upsertOp) findInProgressConflict(rel storage.RelFileNode, cols []catalog.Column, inserted Row) (xid storage.TransactionID, isInFlightInsert bool, found bool) {
	if o.arbiterTree == nil || o.ctx == nil {
		return 0, false, false
	}
	key, err := encodeArbiterKey(o.plan.OnConflict, o.plan.Table, inserted, o.plan.Pos())
	if err != nil || key == nil {
		return 0, false, false
	}
	selfXID := o.ctx.Tx.XID
	var (
		foundXID storage.TransactionID
		case1    bool
	)
	_ = o.arbiterTree.RangeScan(key, key, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		tuple, err := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
		slot.RUnlock()
		o.ctx.Pool.Unpin(slot)
		if err != nil {
			return true, nil
		}
		xmin := tuple.Header.Xmin
		xmax := tuple.Header.Xmax
		// Case 1: in-flight insert (xmin still active, not us). Use the live
		// manager active-set (not the snapshot InProgress list) so we also
		// catch XIDs that were materialised after this session's snapshot
		// was taken (M0100-0002).
		if xmin != storage.InvalidTransactionID && xmin != selfXID && o.ctx.TxnMgr != nil && o.ctx.TxnMgr.IsXIDActive(xmin) {
			foundXID = xmin
			case1 = true
			return false, nil
		}
		// Case 2: visible-being-deleted. The tuple's xmin is already settled
		// from this snapshot's view (committed or our own xact), and xmax is
		// a non-lock-only in-flight non-self xact — i.e. someone is in the
		// middle of deleting (or cross-partition-moving) what would otherwise
		// be a real arbiter conflict. Upstream `_bt_check_unique` waits on
		// this xmax to determine whether the apparent conflict survives.
		if xmax != storage.InvalidTransactionID && xmax != selfXID && !storage.IsHeapTupleLockOnly(tuple.Header.Infomask) {
			xminSettled := xmin == selfXID || (o.ctx.Snap.SeesCommittedXID(xmin))
			if xminSettled && o.ctx.TxnMgr != nil && o.ctx.TxnMgr.IsXIDActive(xmax) {
				foundXID = xmax
				case1 = false
				return false, nil
			}
		}
		// Case 3: lock-only xmax (SELECT FOR UPDATE/SHARE) from a live
		// transaction. Upstream _bt_check_unique blocks via
		// ConditionalLockTuple until the lock holder releases, then
		// re-probes. The row is still live (xmin settled, xmax is only a
		// lock stamp) — we must wait because the lock holder may
		// subsequently UPDATE or DELETE the conflicting row before
		// committing, changing the arbiter outcome.
		if xmax != storage.InvalidTransactionID && xmax != selfXID && storage.IsHeapTupleLockOnly(tuple.Header.Infomask) {
			xminSettled := xmin == selfXID || o.ctx.Snap.SeesCommittedXID(xmin)
			if xminSettled && o.ctx.TxnMgr != nil && o.ctx.TxnMgr.IsXIDActive(xmax) {
				foundXID = xmax
				case1 = false
				return false, nil
			}
		}
		return true, nil
	})
	return foundXID, case1, foundXID != 0
}

// applyInsert is the no-conflict happy path. Writes the heap row
// and stitches a new (key → ItemPointer) entry into the arbiter
// index so subsequent rows in the same statement (multi-row
// VALUES, CTE-fed INSERT, etc.) see it.
func (o *upsertOp) applyInsert(rel storage.RelFileNode, cols []catalog.Column, inserted Row) error {
	ptr, err := writeHeapRowReturning(o.ctx, rel, cols, inserted)
	if err != nil {
		return err
	}
	return o.maintainArbiter(inserted, ptr)
}

// applyUpdate stamps xmax on the conflicting tuple and writes the
// updated row as a fresh heap tuple. The arbiter index gets a new
// entry pointing at the new ItemPointer; the old entry is left in
// place — visibility filtering at the next probe will skip it
// because the tuple's xmax now blocks it.
func (o *upsertOp) applyUpdate(rel storage.RelFileNode, cols []catalog.Column, oldPtr storage.ItemPointer, updated Row) error {
	// Materialise the XID BEFORE stamping xmax so the old tuple gets a
	// real delete stamp (not InvalidTransactionID). Without this, the old
	// tuple's xmax=0 would make it appear still-live to subsequent scans.
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	pinned, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: oldPtr.Block})
	if err != nil {
		return err
	}
	pinned.Lock()
	if err := storage.PageSetHeapTupleXmax(pinned.Page(), oldPtr.Offset, o.ctx.Tx.XID); err != nil {
		pinned.Unlock()
		o.ctx.Pool.Unpin(pinned)
		return err
	}
	derr := markHeapDeleteDirty(o.ctx.Pool, pinned, rel, oldPtr.Block, oldPtr.Offset, o.ctx.Tx.XID, nil)
	pinned.Unlock()
	o.ctx.Pool.Unpin(pinned)
	if derr != nil {
		return derr
	}
	newPtr, err := writeHeapRowReturning(o.ctx, rel, cols, updated)
	if err != nil {
		return err
	}
	return o.maintainArbiter(updated, newPtr)
}

// maintainArbiter inserts (conflict-key → ptr) into the arbiter
// index. NULL keys (any conflict-key column is null) are skipped —
// upstream's IS NULL doesn't participate in unique-constraint
// equality.
func (o *upsertOp) maintainArbiter(row Row, ptr storage.ItemPointer) error {
	if o.arbiterTree == nil {
		return nil
	}
	key, err := encodeArbiterKey(o.plan.OnConflict, o.plan.Table, row, o.plan.Pos())
	if err != nil {
		return err
	}
	if key == nil {
		return nil
	}
	if err := o.arbiterTree.Insert(key, ptr); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	return nil
}

// evalUpdate builds the merged 2N-wide row (existing || inserted)
// and evaluates UpdateWhere + UpdateSet against it. Returns
// (updatedRow, false, nil) when the update should apply,
// (_, true, nil) when UpdateWhere evaluates false (skip silently
// per upstream — no DO NOTHING fallback).
func (o *upsertOp) evalUpdate(existing Row, inserted Row) (Row, bool, error) {
	merged := make(Row, 0, len(existing)+len(inserted))
	merged = append(merged, existing...)
	merged = append(merged, inserted...)
	if pred := o.plan.OnConflict.UpdateWhere; pred != nil {
		v, err := evalExpr(pred, merged, o.ctx)
		if err != nil {
			return nil, false, err
		}
		if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
			return nil, true, nil
		}
	}
	updated := make(Row, len(existing))
	for i := range existing {
		expr := o.plan.OnConflict.UpdateSet[i]
		if expr == nil {
			updated[i] = existing[i]
			continue
		}
		v, err := evalExpr(expr, merged, o.ctx)
		if err != nil {
			return nil, false, err
		}
		updated[i] = v
	}
	return updated, false, nil
}

// encodeArbiterKey turns the inserted row's conflict-key columns
// into the byte form the arbiter btree stores. Returns (nil, nil)
// when any conflict-key column is NULL — signals "no probe, no
// maintenance" to the caller (matches upstream's NULL-never-
// matches semantics for unique-constraint inference). Multi-column
// arbiters are supported by concatenating per-column encodings.
func encodeArbiterKey(oc *planner.OnConflictPlan, tbl *catalog.Table, row Row, pos int) ([]byte, error) {
	if oc.ArbiterIndex == nil || len(oc.ArbiterColumns) == 0 {
		return nil, nil
	}
	var out []byte
	for _, ord := range oc.ArbiterColumns {
		v := row[ord]
		if v.IsNull() {
			// NULL never conflicts per upstream semantics.
			return nil, nil
		}
		col := &tbl.Columns[ord]
		k, ee := encodeBTreeKeyForColumn(v, col, pos)
		if ee != nil {
			return nil, ee
		}
		out = append(out, k...)
	}
	return out, nil
}
 
