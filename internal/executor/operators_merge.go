package executor

import (
	"errors"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// mergeOp executes a MERGE INTO … USING … ON … WHEN … statement.
//
// Execution model (nested-loop, suitable for the table sizes goopg targets):
//  1. Drain the USING source into an in-memory slice.
//  2. Scan the target heap. For each visible target row find the first
//     source row that satisfies the ON condition.
//     – If found: apply the first matching WHEN MATCHED clause (UPDATE or DELETE).
//     – Mark the matched source row so it is not revisited in step 3.
//  3. For every source row that had no matching target row, apply the first
//     matching WHEN NOT MATCHED INSERT clause.
//
// M0096-0010.
type mergeOp struct {
	plan         *planner.Merge
	ctx          *Context
	rowsAffected int64
	done         bool
}

// mergePendingMod records a single MERGE modification to apply after the
// target scan completes. srcRow is kept for EPQ re-evaluation. M0100-0005.
type mergePendingMod struct {
	blk    storage.BlockNumber
	slot   uint16
	action planner.MergeActionKind
	newRow Row // for UPDATE
	srcRow Row // source row for EPQ re-evaluation
	tgtRow Row // target old row for BEFORE trigger firing
}

func newMergeOp(p *planner.Merge) *mergeOp { return &mergeOp{plan: p} }

// mergeEPQError is returned by mergeApplyUpdate/mergeApplyDelete when the
// target row was concurrently updated. The caller must re-evaluate WHEN
// MATCHED conditions against the new live row (EPQ recheck). M0100-0005.
type mergeEPQError struct {
	newSlot   uint16
	newTgtRow Row
}

func (e *mergeEPQError) Error() string { return "merge EPQ recheck" }

func (o *mergeOp) Schema() planner.Schema { return nil }
func (o *mergeOp) RowsAffected() int64   { return o.rowsAffected }

func (o *mergeOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *mergeOp) Close() error { return nil }

func (o *mergeOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true

	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return nil, err
	}

	tbl := o.plan.Target
	rel := o.ctx.Catalog.RelFileNode(tbl)
	n := len(tbl.Columns)

	if err := o.ctx.acquireRelLock(rel, lockmgr.RowExclusiveLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return nil, err
	}

	// Step 1: drain source into memory.
	srcOp, err := Build(o.plan.Source)
	if err != nil {
		return nil, err
	}
	if err := srcOp.Open(o.ctx); err != nil {
		return nil, err
	}
	defer srcOp.Close()

	type srcEntry struct {
		row     Row
		matched bool
	}
	var srcRows []srcEntry
	for {
		slot, err := srcOp.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		srcRows = append(srcRows, srcEntry{row: cloneRow(slotRow(slot))})
	}

	// Step 2: scan target, apply WHEN MATCHED clauses.
	var mods []mergePendingMod

	nBlocks, err := o.ctx.Pool.NBlocks(rel)
	if err != nil {
		return nil, err
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return nil, err
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			o.ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			s.RUnlock()
			o.ctx.Pool.Unpin(s)
			return nil, err
		}
		// Collect visible tuples before releasing the page lock.
		type scannedTuple struct {
			slotIdx uint16
			tgtRow  Row
		}
		var visible []scannedTuple
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				if errors.Is(err, storage.ErrUnsupportedItem) {
					continue
				}
				continue
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.TxnMgr) {
				continue
			}
			tgtRow, err := DecodeRow(tbl.Columns, tuple.Data)
			if err != nil {
				continue
			}
			visible = append(visible, scannedTuple{slotIdx: slotIdx, tgtRow: tgtRow})
		}
		s.RUnlock()
		o.ctx.Pool.Unpin(s)

		for _, vt := range visible {
			for si := range srcRows {
				combined := mergedRow(vt.tgtRow, srcRows[si].row)
				v, err := evalExpr(o.plan.On, combined, o.ctx)
				if err != nil {
					continue
				}
				if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
					continue
				}
				// ON matched — first matching WHEN MATCHED clause wins.
				srcRows[si].matched = true
				for _, clause := range o.plan.Clauses {
					if !clause.Matched {
						continue
					}
					if !mergeClauseCondMatches(clause, combined, o.ctx) {
						continue
					}
					switch clause.Action {
					case planner.MergeActionUpdate:
						newRow := make(Row, n)
						for i := range tbl.Columns {
							if i >= len(clause.UpdateSet) || clause.UpdateSet[i] == nil {
								newRow[i] = vt.tgtRow[i]
								continue
							}
							val, err := evalExpr(clause.UpdateSet[i], combined, o.ctx)
							if err != nil {
								newRow[i] = vt.tgtRow[i]
								continue
							}
							newRow[i] = val
						}
						_ = computeGeneratedColumns(tbl.Columns, newRow)
						mods = append(mods, mergePendingMod{blk: blk, slot: vt.slotIdx,
							action: planner.MergeActionUpdate, newRow: newRow,
							srcRow: cloneRow(srcRows[si].row), tgtRow: cloneRow(vt.tgtRow)})
					case planner.MergeActionDelete:
						mods = append(mods, mergePendingMod{blk: blk, slot: vt.slotIdx,
							action: planner.MergeActionDelete, srcRow: cloneRow(srcRows[si].row),
							tgtRow: cloneRow(vt.tgtRow)})
					case planner.MergeActionDoNothing:
						// DO NOTHING — skip this row. M0097-0016.
					}
					break // first clause wins
				}
				break // first source match wins
			}
		}
	}

	// Apply pending modifications (with EPQ retry loop for concurrent updates).
	for _, mod := range mods {
		applied, err := o.applyMod(rel, tbl, n, mod)
		if err != nil {
			return nil, err
		}
		if applied {
			o.rowsAffected++
		}
	}

	// Step 3: WHEN NOT MATCHED INSERT (or DO NOTHING) for unmatched source rows.
	for _, sr := range srcRows {
		if sr.matched {
			continue
		}
		for _, clause := range o.plan.Clauses {
			if clause.Matched {
				continue
			}
			if clause.Action != planner.MergeActionInsert && clause.Action != planner.MergeActionDoNothing {
				continue
			}
			// Evaluate condition against source row only.
			if clause.Condition != nil {
				cv, err := evalExpr(clause.Condition, sr.row, o.ctx)
				if err != nil {
					continue
				}
				if cv.IsNull() || cv.Kind != KindBool || !cv.BoolValue() {
					continue
				}
			}
			// DO NOTHING — skip this row without inserting. M0097-0016.
			if clause.Action == planner.MergeActionDoNothing {
				break // first matching clause wins
			}
			row := make(Row, n)
			if clause.InsertExprs != nil {
				for i, expr := range clause.InsertExprs {
					if i >= len(clause.InsertColIdx) {
						break
					}
					val, err := evalExpr(expr, sr.row, o.ctx)
					if err != nil {
						continue
					}
					row[clause.InsertColIdx[i]] = val
				}
			}
			_ = computeGeneratedColumns(tbl.Columns, row)
			if err := writeHeapRow(o.ctx, rel, tbl.Columns, row); err != nil {
				return nil, err
			}
			o.rowsAffected++
			break // first matching clause wins
		}
	}
	return nil, EOF
}

// mergedRow returns a new row that is the concatenation of target and source rows.
// Target columns occupy indices 0..len(tgt)-1, source at len(tgt)..
// applyMod applies a single pending MERGE modification with EPQ retry loop.
// Returns (true, nil) on success, (false, nil) if skipped (row gone or
// conditions no longer match), (false, err) on fatal error.
func (o *mergeOp) applyMod(rel storage.RelFileNode, tbl *catalog.Table, n int, mod mergePendingMod) (applied bool, _ error) {
	for {
		// Fire BEFORE UPDATE/DELETE triggers before the actual write.
		if len(tbl.Triggers) > 0 {
			switch mod.action {
			case planner.MergeActionUpdate:
				retRow, ok := fireTriggers(o.ctx, tbl, "before", "update", mod.tgtRow, mod.newRow)
				if !ok {
					return false, nil // RETURN NULL — skip row
				}
				mod.newRow = retRow
			case planner.MergeActionDelete:
				_, ok := fireTriggers(o.ctx, tbl, "before", "delete", mod.tgtRow, nil)
				if !ok {
					return false, nil // RETURN NULL — skip row
				}
			}
		}

		var err error
		switch mod.action {
		case planner.MergeActionUpdate:
			err = mergeApplyUpdate(o.ctx, rel, nil, tbl.Columns, mod.blk, mod.slot, mod.newRow, mod.tgtRow, o.plan.Pos())
		case planner.MergeActionDelete:
			err = mergeApplyDelete(o.ctx, rel, nil, tbl.Columns, mod.blk, mod.slot, mod.tgtRow, o.plan.Pos())
		default:
			return false, nil
		}
		if err == nil {
			return true, nil
		}
		epqErr, isEPQ := err.(*mergeEPQError)
		if !isEPQ {
			return false, err
		}

		// Re-evaluate WHEN MATCHED conditions against the new live row.
		combined := mergedRow(epqErr.newTgtRow, mod.srcRow)
		v, evErr := evalExpr(o.plan.On, combined, o.ctx)
		if evErr != nil || v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
			return false, nil // no longer matches ON condition
		}
		reMatched := false
		for _, clause := range o.plan.Clauses {
			if !clause.Matched {
				continue
			}
			if !mergeClauseCondMatches(clause, combined, o.ctx) {
				continue
			}
			switch clause.Action {
			case planner.MergeActionUpdate:
				newRow := make(Row, n)
				for i := range tbl.Columns {
					if i >= len(clause.UpdateSet) || clause.UpdateSet[i] == nil {
						newRow[i] = epqErr.newTgtRow[i]
						continue
					}
					val, _ := evalExpr(clause.UpdateSet[i], combined, o.ctx)
					newRow[i] = val
				}
				_ = computeGeneratedColumns(tbl.Columns, newRow)
				mod.slot = epqErr.newSlot
				mod.tgtRow = epqErr.newTgtRow // update for trigger firing in next iteration
				mod.newRow = newRow
				mod.action = planner.MergeActionUpdate
				reMatched = true
			case planner.MergeActionDelete:
				mod.slot = epqErr.newSlot
				mod.tgtRow = epqErr.newTgtRow
				mod.action = planner.MergeActionDelete
				reMatched = true
			case planner.MergeActionDoNothing:
				return false, nil
			}
			break // first matching clause wins
		}
		if !reMatched {
			return false, nil // no clause matched after EPQ re-eval
		}
		// Loop back to retry with updated slot/newRow. Trigger fires again at top.
	}
}

func mergedRow(tgt, src Row) Row {
	out := make(Row, len(tgt)+len(src))
	copy(out, tgt)
	copy(out[len(tgt):], src)
	return out
}

// mergeClauseCondMatches reports whether clause.Condition is nil (always matches)
// or evaluates to true against row.
func mergeClauseCondMatches(clause *planner.MergeWhenClause, row Row, ctx *Context) bool {
	if clause.Condition == nil {
		return true
	}
	v, err := evalExpr(clause.Condition, row, ctx)
	if err != nil {
		return false
	}
	return !v.IsNull() && v.Kind == KindBool && v.BoolValue()
}

// mergeApplyUpdate stamps xmax on the old tuple and writes a new version.
// When a concurrent update is detected (RC isolation), it waits for the
// conflicting transaction to complete (EPQ) and then re-checks the row.
// If the row is still updatable, the update is applied; otherwise it is
// skipped (the MERGE WHEN clause no longer matches after the concurrent update).
func mergeApplyUpdate(ctx *Context, rel storage.RelFileNode, tbl *catalog.Table, cols []catalog.Column, blk storage.BlockNumber, slot uint16, newRow, tgtRow Row, pos int) error {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	s.Lock()
	oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), slot)
	if oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, ctx.Tx.XID, &ctx.Snap) {
		xmax := oldTup.Header.Xmax
		s.Unlock()
		ctx.Pool.Unpin(s)
		if epqWait(ctx, xmax) {
			return &ExecError{Code: "40001", Pos: pos, Message: "could not serialize access due to concurrent update"}
		}
		// Follow HOT chain to find the current live tuple.
		newSlot, newTgtRow, found := epqFollowHOT(ctx, rel, blk, slot, cols, nil)
		if !found {
			return nil // row deleted by concurrent txn, skip this MERGE action
		}
		// Signal the caller to re-evaluate WHEN MATCHED conditions with the new row.
		// Trigger will fire on the NEXT call with the correct live row. M0100-0005.
		return &mergeEPQError{newSlot: newSlot, newTgtRow: newTgtRow}
	}
	// No concurrent update: safe to write. Release lock, fire BEFORE trigger, re-pin, write.
	s.Unlock()
	ctx.Pool.Unpin(s)

	// Fire BEFORE UPDATE trigger with the confirmed live row values. M0100-0005.
	if tbl != nil && len(tbl.Triggers) > 0 {
		retRow, ok := fireTriggers(ctx, tbl, "before", "update", tgtRow, newRow)
		if !ok {
			return nil // trigger RETURN NULL — skip this row
		}
		newRow = retRow
	}

	// Re-pin and apply the write.
	s, err = ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	s.Lock()
	oldTup, oldGerr = storage.PageGetHeapTuple(s.Page(), slot)
	var oldTupleBytes []byte
	if oldGerr == nil {
		oldTupleBytes, _ = oldTup.MarshalBinary()
	}
	if err := storage.PageSetHeapTupleXmax(s.Page(), slot, ctx.Tx.XID); err != nil {
		s.Unlock()
		ctx.Pool.Unpin(s)
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	derr := markHeapDeleteDirtyAndClearVM(ctx, s, rel, blk, slot, ctx.Tx.XID, oldTupleBytes)
	s.Unlock()
	ctx.Pool.Unpin(s)
	if derr != nil {
		return derr
	}
	return writeHeapRow(ctx, rel, cols, newRow)
}

// mergeApplyDelete stamps xmax on the tuple at (blk, slot).
func mergeApplyDelete(ctx *Context, rel storage.RelFileNode, tbl *catalog.Table, cols []catalog.Column, blk storage.BlockNumber, slot uint16, tgtRow Row, pos int) error {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	s.Lock()
	oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), slot)
	if oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, ctx.Tx.XID, &ctx.Snap) {
		xmax := oldTup.Header.Xmax
		s.Unlock()
		ctx.Pool.Unpin(s)
		if epqWait(ctx, xmax) {
			return &ExecError{Code: "40001", Pos: pos, Message: "could not serialize access due to concurrent update"}
		}
		newSlot, newTgtRow, found := epqFollowHOT(ctx, rel, blk, slot, cols, nil)
		if !found {
			return nil
		}
		return &mergeEPQError{newSlot: newSlot, newTgtRow: newTgtRow}
	}
	s.Unlock()
	ctx.Pool.Unpin(s)

	// Fire BEFORE DELETE trigger with the confirmed live row values.
	if tbl != nil && len(tbl.Triggers) > 0 {
		_, ok := fireTriggers(ctx, tbl, "before", "delete", tgtRow, nil)
		if !ok {
			return nil // trigger RETURN NULL — skip
		}
	}

	// Re-pin and apply the delete.
	s, err = ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	s.Lock()
	oldTup, oldGerr = storage.PageGetHeapTuple(s.Page(), slot)
	var oldTupleBytes []byte
	if oldGerr == nil {
		oldTupleBytes, _ = oldTup.MarshalBinary()
	}
	if err := storage.PageSetHeapTupleXmax(s.Page(), slot, ctx.Tx.XID); err != nil {
		s.Unlock()
		ctx.Pool.Unpin(s)
		if errors.Is(err, storage.ErrUnsupportedItem) {
			return nil
		}
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	derr := markHeapDeleteDirtyAndClearVM(ctx, s, rel, blk, slot, ctx.Tx.XID, oldTupleBytes)
	s.Unlock()
	ctx.Pool.Unpin(s)
	return derr
}
