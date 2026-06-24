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
	retRows      [][]Datum // collected RETURNING rows
	retIdx       int       // next retRows index to yield
}

// mergePendingMod records a single MERGE modification to apply after the
// target scan completes. srcRow is kept for EPQ re-evaluation. M0100-0005.
type mergePendingMod struct {
	rel          storage.RelFileNode // actual relfilenode to write to (child for partitioned)
	tblRef       *catalog.Table      // actual table metadata (child for partitioned)
	blk          storage.BlockNumber
	slot         uint16
	action       planner.MergeActionKind
	newRow       Row  // for UPDATE
	srcRow       Row  // source row for EPQ re-evaluation
	tgtRow       Row  // target old row for BEFORE trigger firing
	srcIdx       int  // index into srcRows for EPQ-delete fallback to NOT MATCHED
	hasDuplicate bool // a second source row also matched this target during scan; error after apply
}

func newMergeOp(p *planner.Merge) *mergeOp { return &mergeOp{plan: p} }

// errMergeSourceUnmatched is returned by applyMod when the target row was
// deleted by a concurrent committed transaction during EPQ. The outer loop
// must reset the source row's matched flag and retry via NOT MATCHED clauses.
var errMergeSourceUnmatched = errors.New("merge: source row unmatched (target row deleted)")

// mergeEPQError is returned by mergeApplyUpdate/mergeApplyDelete when the
// target row was concurrently updated. The caller must re-evaluate WHEN
// MATCHED conditions against the new live row (EPQ recheck). M0100-0005.
type mergeEPQError struct {
	newBlk    storage.BlockNumber // same as caller's blk for HOT; may differ for non-HOT
	newSlot   uint16
	newTgtRow Row
}

func (e *mergeEPQError) Error() string { return "merge EPQ recheck" }

// mergeEPQOnFailedError is returned by applyMod when EPQ produced a live target
// row that no longer satisfies the ON condition. The caller must unmark the
// source row (so step 3 retries it as NOT MATCHED INSERT) and apply WHEN NOT
// MATCHED BY SOURCE against epqTgtRow. M0100-0007.
type mergeEPQOnFailedError struct {
	blk       storage.BlockNumber
	slot      uint16
	epqTgtRow Row // in parent column order
	rel       storage.RelFileNode
	tblRef    *catalog.Table
}

func (e *mergeEPQOnFailedError) Error() string { return "merge EPQ: ON failed" }

func (o *mergeOp) Schema() planner.Schema { return o.plan.ReturningSchema }
func (o *mergeOp) RowsAffected() int64   { return o.rowsAffected }

func (o *mergeOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *mergeOp) Close() error { return nil }

// collectReturningRow evaluates the RETURNING expressions for one MERGE action.
// oldRow is the pre-action target row (nil = NULLs for INSERT).
// newRow is the post-action target row (nil = NULLs for DELETE).
// The evaluation row has layout: [std || old || new] where std is the
// post-action row for INSERT/UPDATE and pre-action row for DELETE.
// M0100-0007.
func (o *mergeOp) collectReturningRow(action planner.MergeActionKind, oldRow, newRow Row) {
	if len(o.plan.Returning) == 0 {
		return
	}
	n := len(o.plan.Target.Columns)
	nullRow := make(Row, n)

	var stdRow, oldR, newR Row
	switch action {
	case planner.MergeActionInsert:
		stdRow = newRow
		oldR = nullRow
		newR = newRow
	case planner.MergeActionDelete:
		stdRow = oldRow
		oldR = oldRow
		newR = nullRow
	default: // UPDATE
		stdRow = newRow
		oldR = oldRow
		newR = newRow
	}

	combined := make(Row, 3*n)
	if len(stdRow) > 0 {
		copy(combined[0:n], stdRow)
	}
	if len(oldR) > 0 {
		copy(combined[n:2*n], oldR)
	}
	if len(newR) > 0 {
		copy(combined[2*n:3*n], newR)
	}

	o.ctx.MergeAction = action
	o.ctx.MergeOldRow = oldRow
	o.ctx.MergeNewRow = newRow
	retRow := make([]Datum, len(o.plan.Returning))
	for i, expr := range o.plan.Returning {
		val, _ := evalExpr(expr, combined, o.ctx)
		retRow[i] = val
	}
	o.retRows = append(o.retRows, retRow)
}

func (o *mergeOp) Next() (TupleSlot, error) {
	// Yield pre-collected RETURNING rows on subsequent calls.
	if o.done {
		if o.retIdx < len(o.retRows) {
			row := o.retRows[o.retIdx]
			o.retIdx++
			return SlotFromRow(o.plan.ReturningSchema, row), nil
		}
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
	// For partitioned tables, scan all partition children (the parent has no rows). M0100-0005.
	var mods []mergePendingMod

	// unmatchedTarget records a target row that had no matching source row,
	// for WHEN NOT MATCHED BY SOURCE processing in step 2b. M0100-0007.
	type unmatchedTarget struct {
		rel    storage.RelFileNode
		tblRef *catalog.Table
		blk    storage.BlockNumber
		slot   uint16
		tgtRow Row
	}
	var unmatchedTargets []unmatchedTarget

	// For partitioned tables, scan only the children (parent has no rows).
	// For non-partitioned tables, scan only the parent. M0100-0005.
	scanTables := []*catalog.Table{tbl}
	if len(tbl.PartitionKey) > 0 {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			children := im.PartitionChildren(tbl.OID)
			if len(children) > 0 {
				scanTables = children // replace parent with children
			}
		}
	}

	for _, scanTbl := range scanTables {
		scanRel := o.ctx.Catalog.RelFileNode(scanTbl)
		// Lock child partitions (parent already locked above).
		if scanTbl != tbl {
			if err := o.ctx.acquireRelLock(scanRel, lockmgr.RowExclusiveLock); err != nil {
				if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
					ee.Pos = o.plan.Pos()
				}
				return nil, err
			}
		}
	nBlocks, err := o.ctx.Pool.NBlocks(scanRel)
	if err != nil {
		return nil, err
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: scanRel, Block: blk})
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
			if !mvcc.TupleVisibleSubxact(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.TxnMgr, o.ctx.MultiXact) {
				continue
			}
			tgtRow, err := DecodeHeapTupleRow(scanTbl.Columns, tuple, nil)
			if err != nil {
				continue
			}
			visible = append(visible, scannedTuple{slotIdx: slotIdx, tgtRow: tgtRow})
		}
		s.RUnlock()
		o.ctx.Pool.Unpin(s)

		for _, vt := range visible {
			matchedSrc := false
			// Normalize scanned row to parent column order so plan expressions
			// (which use parent indices) evaluate correctly for child partitions
			// with different column ordering (e.g. part2 with val,key). M0100-0007.
			vtTgtRow := vt.tgtRow
			if scanTbl != tbl {
				vtTgtRow = remapRowForPartition(scanTbl.Columns, tbl.Columns, vt.tgtRow)
			}
			for si := range srcRows {
				combined := mergedRow(vtTgtRow, srcRows[si].row)
				v, err := evalExpr(o.plan.On, combined, o.ctx)
				if err != nil {
					continue
				}
				if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
					continue
				}
				// ON matched. If a previous source row already matched this target
				// row, mark the pending mod as having a duplicate — we defer the
				// error to the apply phase so that any concurrent lock on the
				// target row (causing epqWait) is resolved first, matching PG's
				// blocking behaviour. M0100-0007.
				if matchedSrc {
					mods[len(mods)-1].hasDuplicate = true
					srcRows[si].matched = true
					break // only need to detect the first duplicate
				}
				// First source match — record action and continue scanning to detect
				// if a second source row also matches (potential duplicate).
				srcRows[si].matched = true
				matchedSrc = true
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
								newRow[i] = vtTgtRow[i]
								continue
							}
							val, err := evalExpr(clause.UpdateSet[i], combined, o.ctx)
							if err != nil {
								newRow[i] = vtTgtRow[i]
								continue
							}
							newRow[i] = val
						}
						_ = computeGeneratedColumns(tbl.Columns, newRow)
						// tgtRow/newRow in parent order; applyMod remaps to child at write.
						mods = append(mods, mergePendingMod{rel: scanRel, tblRef: scanTbl, blk: blk, slot: vt.slotIdx,
							action: planner.MergeActionUpdate, newRow: newRow,
							srcRow: cloneRow(srcRows[si].row), tgtRow: vtTgtRow, srcIdx: si})
					case planner.MergeActionDelete:
						mods = append(mods, mergePendingMod{rel: scanRel, tblRef: scanTbl, blk: blk, slot: vt.slotIdx,
							action: planner.MergeActionDelete, srcRow: cloneRow(srcRows[si].row),
							tgtRow: vtTgtRow, srcIdx: si})
					case planner.MergeActionDoNothing:
						// DO NOTHING — skip this row. M0097-0016.
					}
					break // first clause wins
				}
				// Do NOT break here — continue scanning remaining source rows to
				// detect duplicates that would match the same target row.
			}
			// Target row had no matching source row — record for WHEN NOT MATCHED BY SOURCE. M0100-0007.
			if !matchedSrc {
				unmatchedTargets = append(unmatchedTargets, unmatchedTarget{
					rel: scanRel, tblRef: scanTbl, blk: blk, slot: vt.slotIdx, tgtRow: vt.tgtRow,
				})
			}
		}
	} // end for blk
	} // end for scanTbl

	// Step 2b: WHEN NOT MATCHED BY SOURCE — target rows with no source match. M0100-0007.
	// Evaluate conditions/actions against a null-padded source row so that
	// expressions referencing source columns simply get NULL.
	if len(unmatchedTargets) > 0 {
		srcColCount := len(o.plan.Source.Output())
		nullSrcRow := make(Row, srcColCount)
		for _, ut := range unmatchedTargets {
			// Normalize tgtRow to parent column order for expression evaluation
			// and RETURNING. Partition children may have different column ordering
			// than the parent (e.g. part2 with (val,key) vs parent (key,val)). M0100-0007.
			utTgtRow := ut.tgtRow
			if ut.tblRef != nil && ut.tblRef != tbl {
				utTgtRow = remapRowForPartition(ut.tblRef.Columns, tbl.Columns, ut.tgtRow)
			}
			combined := mergedRow(utTgtRow, nullSrcRow)
			for _, clause := range o.plan.Clauses {
				if clause.Matched || !clause.BySource {
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
							newRow[i] = utTgtRow[i]
							continue
						}
						val, err := evalExpr(clause.UpdateSet[i], combined, o.ctx)
						if err != nil {
							newRow[i] = utTgtRow[i]
							continue
						}
						newRow[i] = val
					}
					_ = computeGeneratedColumns(tbl.Columns, newRow)
					// Store tgtRow/newRow in parent order; applyMod remaps to child for write.
					mods = append(mods, mergePendingMod{
						rel: ut.rel, tblRef: ut.tblRef, blk: ut.blk, slot: ut.slot,
						action: planner.MergeActionUpdate, newRow: newRow,
						tgtRow: utTgtRow,
					})
				case planner.MergeActionDelete:
					mods = append(mods, mergePendingMod{
						rel: ut.rel, tblRef: ut.tblRef, blk: ut.blk, slot: ut.slot,
						action: planner.MergeActionDelete,
						tgtRow: utTgtRow,
					})
				case planner.MergeActionDoNothing:
					// skip
				}
				break // first matching clause wins
			}
		}
	}

	// Apply pending modifications (with EPQ retry loop for concurrent updates).
	for i := range mods {
		mod := &mods[i]
		modTbl := mod.tblRef
		if modTbl == nil {
			modTbl = tbl
		}
		modRel := mod.rel
		if modRel == (storage.RelFileNode{}) {
			modRel = rel
		}
		applied, err := o.applyMod(modRel, modTbl, n, mod)
		if errors.Is(err, errMergeSourceUnmatched) {
			// The target row was deleted by a concurrent committed transaction.
			// Unmark the source row so step 3 can attempt NOT MATCHED INSERT.
			srcRows[mod.srcIdx].matched = false
			continue
		}
		if epqOf, ok := err.(*mergeEPQOnFailedError); ok {
			// Concurrent commit changed target key so ON no longer matches.
			// Unmark source (→ NOT MATCHED INSERT in step 3) and apply WHEN
			// NOT MATCHED BY SOURCE against the now-visible EPQ row. M0100-0007.
			srcRows[mod.srcIdx].matched = false
			if applyErr := o.applyNotMatchedBySource(epqOf.rel, epqOf.tblRef, tbl, rel, n, epqOf.blk, epqOf.slot, epqOf.epqTgtRow); applyErr != nil {
				return nil, applyErr
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if applied {
			o.rowsAffected++
			// Collect RETURNING row using post-EPQ values (mod is a pointer so
			// applyMod's EPQ re-evaluation of mod.newRow propagates here).
			if mod.action == planner.MergeActionUpdate {
				o.collectReturningRow(planner.MergeActionUpdate, mod.tgtRow, mod.newRow)
			} else if mod.action == planner.MergeActionDelete {
				o.collectReturningRow(planner.MergeActionDelete, mod.tgtRow, nil)
			}
			// Raise duplicate-source error AFTER the apply (and any epqWait blocking)
			// so concurrent callers see the <waiting...> / <...completed> sequence that
			// PostgreSQL produces. M0100-0007.
			if mod.hasDuplicate {
				return nil, &ExecError{Code: "21000", Pos: o.plan.Pos(),
					Message: "MERGE command cannot affect row a second time"}
			}
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

			// Partition routing: route the row to the correct leaf partition.
			insertTbl := tbl
			insertRel := rel
			if len(tbl.PartitionKey) > 0 {
				if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
					part, partErr := routeToPartition(tbl, row, im, o.ctx)
					if partErr != nil {
						return nil, partErr
					}
					if part != nil {
						insertTbl = part
						insertRel = o.ctx.Catalog.RelFileNode(part)
						row = remapRowForPartition(tbl.Columns, part.Columns, row)
						_ = computeGeneratedColumns(part.Columns, row)
					}
				}
			}

			// BEFORE INSERT triggers (mirrors insertOp path). M0100-0005.
			if len(insertTbl.Triggers) > 0 {
				newRow, ok := fireTriggers(o.ctx, insertTbl, "before", "insert", nil, row)
				if !ok {
					break // trigger returned NULL — suppress insert
				}
				row = newRow
			}

			// Unique constraint check with wait semantics — mirrors insertOp so
			// concurrent INSERT / MERGE NOT MATCHED on the same key causes the
			// correct wait → 23505 (committed) or retry (aborted) behaviour.
			if uerr := checkUniqueIndexesForInsert(o.ctx, insertTbl, insertTbl.Columns, row, o.plan.Pos()); uerr != nil {
				return nil, uerr
			}
			ptr, werr := writeHeapRowReturning(o.ctx, insertRel, insertTbl.Columns, row)
			if werr != nil {
				return nil, werr
			}
			maintainUniqueIndexesForInsert(o.ctx, insertTbl, insertTbl.Columns, row, ptr)
			o.rowsAffected++
			o.collectReturningRow(planner.MergeActionInsert, nil, row)
			break // first matching clause wins
		}
	}
	// Yield first RETURNING row (if any); subsequent rows via retIdx.
	if len(o.plan.Returning) > 0 && len(o.retRows) > 0 {
		row := o.retRows[0]
		o.retIdx = 1
		return SlotFromRow(o.plan.ReturningSchema, row), nil
	}
	return nil, EOF
}

// mergedRow returns a new row that is the concatenation of target and source rows.
// Target columns occupy indices 0..len(tgt)-1, source at len(tgt)..
// applyMod applies a single pending MERGE modification with EPQ retry loop.
// Returns (true, nil) on success, (false, nil) if skipped (row gone or
// conditions no longer match), (false, err) on fatal error.
func (o *mergeOp) applyMod(rel storage.RelFileNode, tbl *catalog.Table, n int, mod *mergePendingMod) (applied bool, _ error) {
	for {
		// Triggers are fired inside mergeApplyUpdate/Delete after EPQ resolves,
		// so they fire exactly once per successful write. M0100-0005.
		var err error
		// mod.newRow and mod.tgtRow are in parent column order. Remap to child
		// order for the on-disk write; RETURNING uses the parent-order values. M0100-0007.
		parentTbl := o.plan.Target
		writeNewRow := mod.newRow
		writeTgtRow := mod.tgtRow
		destRel := rel
		destCols := tbl.Columns
		if tbl != parentTbl {
			writeTgtRow = remapRowForPartition(parentTbl.Columns, tbl.Columns, mod.tgtRow)
			// Route newRow to the correct partition; for cross-partition UPDATEs
			// (e.g. key change that moves a row to a different child), destRel/destCols
			// will differ from rel/tbl.Columns. M0100-0007.
			destRel, destCols, writeNewRow = o.routeModNewRow(parentTbl, tbl, rel, mod.newRow)
		}
		switch mod.action {
		case planner.MergeActionUpdate:
			err = mergeApplyUpdate(o.ctx, rel, tbl, tbl.Columns, mod.blk, mod.slot, writeNewRow, writeTgtRow, destRel, destCols, o.plan.Pos())
		case planner.MergeActionDelete:
			err = mergeApplyDelete(o.ctx, rel, tbl, tbl.Columns, mod.blk, mod.slot, writeTgtRow, o.plan.Pos())
		default:
			return false, nil
		}
		if err == nil {
			return true, nil
		}
		if errors.Is(err, errMergeSourceUnmatched) {
			return false, errMergeSourceUnmatched
		}
		epqErr, isEPQ := err.(*mergeEPQError)
		if !isEPQ {
			return false, err
		}

		// Normalize EPQ row to parent column order so plan expressions
		// (which use parent column indices) evaluate correctly. M0100-0007.
		epqTgtNorm := epqErr.newTgtRow
		if tbl != parentTbl {
			epqTgtNorm = remapRowForPartition(tbl.Columns, parentTbl.Columns, epqErr.newTgtRow)
		}
		// Re-evaluate WHEN MATCHED conditions against the new live row.
		combined := mergedRow(epqTgtNorm, mod.srcRow)
		v, evErr := evalExpr(o.plan.On, combined, o.ctx)
		if evErr != nil || v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
			// ON failed after EPQ: the concurrent commit changed the target row
			// so it no longer matches this source. Report the live row so the
			// caller can route it through WHEN NOT MATCHED BY SOURCE. M0100-0007.
			return false, &mergeEPQOnFailedError{
				blk: epqErr.newBlk, slot: epqErr.newSlot,
				epqTgtRow: epqTgtNorm,
				rel:    mod.rel,
				tblRef: mod.tblRef,
			}
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
				for i := range parentTbl.Columns {
					if i >= len(clause.UpdateSet) || clause.UpdateSet[i] == nil {
						newRow[i] = epqTgtNorm[i]
						continue
					}
					val, _ := evalExpr(clause.UpdateSet[i], combined, o.ctx)
					newRow[i] = val
				}
				_ = computeGeneratedColumns(parentTbl.Columns, newRow)
				mod.blk = epqErr.newBlk
				mod.slot = epqErr.newSlot
				mod.tgtRow = epqTgtNorm // parent order for RETURNING
				mod.newRow = newRow     // parent order; remapped to child at write time
				mod.action = planner.MergeActionUpdate
				reMatched = true
			case planner.MergeActionDelete:
				mod.blk = epqErr.newBlk
				mod.slot = epqErr.newSlot
				mod.tgtRow = epqTgtNorm
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
		// Loop back to retry with updated slot/newRow. mergeApplyUpdate/Delete fires trigger.
	}
}

// routeModNewRow returns the destination rel/cols and the write-order newRow for
// a MERGE UPDATE, applying partition routing when the new key would move the row
// to a different child partition. newRowParent must be in parentTbl column order.
// If routing is unavailable or the row stays in the same partition, scanRel/scanTbl
// are returned unchanged. M0100-0007.
func (o *mergeOp) routeModNewRow(parentTbl, scanTbl *catalog.Table, scanRel storage.RelFileNode, newRowParent Row) (destRel storage.RelFileNode, destCols []catalog.Column, writeNewRow Row) {
	destRel = scanRel
	destCols = scanTbl.Columns
	writeNewRow = remapRowForPartition(parentTbl.Columns, scanTbl.Columns, newRowParent)
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		dp, _ := routeToPartition(parentTbl, newRowParent, im, o.ctx)
		if dp != nil && dp.OID != scanTbl.OID {
			destRel = o.ctx.Catalog.RelFileNode(dp)
			destCols = dp.Columns
			writeNewRow = remapRowForPartition(parentTbl.Columns, dp.Columns, newRowParent)
		}
	}
	return
}

// applyNotMatchedBySource evaluates WHEN NOT MATCHED BY SOURCE clauses against
// tgtRow (in parent column order) at (epqRel, epqTbl, blk, slot) and applies
// the first matching clause. Used when EPQ reveals a target row no longer
// matches the source ON condition. M0100-0007.
func (o *mergeOp) applyNotMatchedBySource(epqRel storage.RelFileNode, epqTbl *catalog.Table, parentTbl *catalog.Table, fallbackRel storage.RelFileNode, n int, blk storage.BlockNumber, slot uint16, tgtRow Row) error {
	if epqTbl == nil {
		epqTbl = parentTbl
	}
	if epqRel == (storage.RelFileNode{}) {
		epqRel = fallbackRel
	}
	srcColCount := len(o.plan.Source.Output())
	nullSrcRow := make(Row, srcColCount)
	combined := mergedRow(tgtRow, nullSrcRow)
	for _, clause := range o.plan.Clauses {
		if clause.Matched || !clause.BySource {
			continue
		}
		if !mergeClauseCondMatches(clause, combined, o.ctx) {
			continue
		}
		switch clause.Action {
		case planner.MergeActionUpdate:
			newRow := make(Row, n)
			for i := range parentTbl.Columns {
				if i >= len(clause.UpdateSet) || clause.UpdateSet[i] == nil {
					newRow[i] = tgtRow[i]
					continue
				}
				val, _ := evalExpr(clause.UpdateSet[i], combined, o.ctx)
				newRow[i] = val
			}
			_ = computeGeneratedColumns(parentTbl.Columns, newRow)
			mod := &mergePendingMod{
				rel: epqRel, tblRef: epqTbl,
				blk: blk, slot: slot,
				action: planner.MergeActionUpdate, newRow: newRow, tgtRow: tgtRow,
			}
			applied, applyErr := o.applyMod(epqRel, epqTbl, n, mod)
			if applyErr != nil {
				return applyErr
			}
			if applied {
				o.rowsAffected++
				o.collectReturningRow(planner.MergeActionUpdate, tgtRow, newRow)
			}
		case planner.MergeActionDelete:
			mod := &mergePendingMod{
				rel: epqRel, tblRef: epqTbl,
				blk: blk, slot: slot,
				action: planner.MergeActionDelete, tgtRow: tgtRow,
			}
			applied, applyErr := o.applyMod(epqRel, epqTbl, n, mod)
			if applyErr != nil {
				return applyErr
			}
			if applied {
				o.rowsAffected++
				o.collectReturningRow(planner.MergeActionDelete, tgtRow, nil)
			}
		case planner.MergeActionDoNothing:
			// skip
		}
		break
	}
	return nil
}

// mergeEPQRefreshSnap refreshes ctx.Snap for READ COMMITTED after epqWait so
// that committed xmax values are visible as "deleted" rather than "in progress".
// Without this, the frozen snapshot keeps the concurrent xmax in InProgress,
// making the deleted tuple appear live and causing an infinite EPQ retry loop.
func mergeEPQRefreshSnap(ctx *Context) {
	if ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
		return
	}
	if ctx.TxnMgr == nil {
		return
	}
	snap, err := ctx.TxnMgr.SnapshotFor(ctx.Tx)
	if err != nil {
		// If we can't refresh (e.g. isolation mismatch), use a best-effort approach:
		// remove the waited-on XID from the snapshot's InProgress list.
		return
	}
	ctx.Snap = snap
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
// mergeApplyUpdate writes a MERGE MATCHED UPDATE.  destRel/destCols specify the
// destination partition for cross-partition moves (same as rel/cols when the row
// stays in the same partition). M0100-0007.
func mergeApplyUpdate(ctx *Context, rel storage.RelFileNode, tbl *catalog.Table, cols []catalog.Column, blk storage.BlockNumber, slot uint16, newRow, tgtRow Row, destRel storage.RelFileNode, destCols []catalog.Column, pos int) error {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	s.Lock()
	oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), slot)
	if oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, ctx.Tx.XID, &ctx.Snap, ctx.MultiXact) {
		xmax := concurrentModifierXID(oldTup.Header, ctx.MultiXact)
		s.Unlock()
		ctx.Pool.Unpin(s)
		if dl, terr := epqWait(ctx, xmax); terr != nil {
			terr.Pos = pos
			return terr
		} else if dl {
			return &ExecError{Code: "40001", Pos: pos, Message: "could not serialize access due to concurrent update"}
		}
		// For RC: refresh snapshot so committed xmax is visible as "deleted"
		// (frozen snapshot keeps xmax in InProgress, making the tuple appear live).
		mergeEPQRefreshSnap(ctx)
		// Check for moved-partition sentinel (cross-partition UPDATE) before chain follow.
		if epqSlotMovedToAnotherPartition(ctx, rel, blk, slot) {
			return errMovedToAnotherPartition(pos)
		}
		// Follow HOT chain to find the current live tuple.
		newSlot, newTgtRow, _, predOk := epqFollowHOT(ctx, rel, blk, slot, cols, nil, nil)
		if predOk {
			// Signal the caller to re-evaluate WHEN MATCHED conditions with the new row.
			// Trigger will fire on the NEXT call with the correct live row. M0100-0005.
			return &mergeEPQError{newBlk: blk, newSlot: newSlot, newTgtRow: newTgtRow}
		}
		// Non-HOT cross-page update (partition children skip HOT in updateOp because
		// puRel != rel): fall back to raw t_ctid chain, same pattern as updateOp.Next().
		// Use epqFollowChainFull to detect when the chain ended due to a moved-partition
		// sentinel (e.g. HOT chain where the live successor was itself cross-partition moved).
		_, cRes, chainHadSentinel := epqFollowChainFull(ctx, rel, blk, slot, cols, nil, nil)
		if cRes.ok {
			return &mergeEPQError{newBlk: cRes.blk, newSlot: cRes.slot, newTgtRow: cRes.row}
		}
		// If the chain ended because of a sentinel anywhere along the chain (at the
		// starting slot or at any HOT-chain successor), raise the partition-moved error.
		if chainHadSentinel || epqSlotMovedToAnotherPartition(ctx, rel, blk, slot) {
			return errMovedToAnotherPartition(pos)
		}
		return errMergeSourceUnmatched // row deleted; caller retries as NOT MATCHED
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
	// For cross-partition moves: use PageSetHeapTupleMovedPartition which sets
	// BOTH xmax AND the sentinel CTID {InvalidBlockNumber, MovedPartitionsOffsetNumber}
	// so that concurrent EPQ callers can detect the partition move via
	// epqSlotMovedToAnotherPartition. For same-partition updates: use the standard
	// PageSetHeapTupleXmax + stampOldCtid chain. M0100-0007.
	crossPart := destRel != rel
	if crossPart {
		if err := storage.PageSetHeapTupleMovedPartition(s.Page(), slot, effectiveWriterXID(ctx)); err != nil {
			s.Unlock()
			ctx.Pool.Unpin(s)
			return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
	} else {
		// Preserve a pre-existing non-conflicting foreign locker into a {updater +
		// survivors} multi (M0118-0004 producer); plain single-xid stamp otherwise.
		// keysUpdated=true (non-HOT MERGE update).
		var mErr error
		if oldGerr == nil {
			mErr = stampUpdaterXmaxNonHOT(ctx, s.Page(), slot, oldTup.Header, true)
		} else {
			mErr = storage.PageSetHeapTupleXmax(s.Page(), slot, effectiveWriterXID(ctx))
		}
		if err := mErr; err != nil {
			s.Unlock()
			ctx.Pool.Unpin(s)
			return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
	}
	derr := markHeapDeleteDirtyAndClearVM(ctx, s, rel, blk, slot, effectiveWriterXID(ctx), oldTupleBytes)
	s.Unlock()
	ctx.Pool.Unpin(s)
	if derr != nil {
		return derr
	}
	// Write new tuple to destination (may differ for cross-partition moves).
	newPtr, werr := writeHeapRowReturning(ctx, destRel, destCols, newRow)
	if werr != nil {
		return werr
	}
	// Link old→new via t_ctid for same-partition updates so EPQ chain
	// followers (epqFollowChainFull) can locate the latest version. Cross-partition
	// moves use the sentinel CTID (already set above) instead. M0100-0007.
	if !crossPart {
		if cerr := stampOldCtid(ctx, rel, blk, slot, newPtr); cerr != nil {
			return cerr
		}
	}
	return nil
}

// mergeApplyDelete stamps xmax on the tuple at (blk, slot).
func mergeApplyDelete(ctx *Context, rel storage.RelFileNode, tbl *catalog.Table, cols []catalog.Column, blk storage.BlockNumber, slot uint16, tgtRow Row, pos int) error {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	s.Lock()
	oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), slot)
	if oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, ctx.Tx.XID, &ctx.Snap, ctx.MultiXact) {
		xmax := concurrentModifierXID(oldTup.Header, ctx.MultiXact)
		s.Unlock()
		ctx.Pool.Unpin(s)
		if dl, terr := epqWait(ctx, xmax); terr != nil {
			terr.Pos = pos
			return terr
		} else if dl {
			return &ExecError{Code: "40001", Pos: pos, Message: "could not serialize access due to concurrent update"}
		}
		// Refresh snapshot (RC) so the committed xmax is visible as "deleted".
		// Mirrors mergeApplyUpdate — required so epqFollowHOT/epqFollowChain
		// can correctly classify the old tuple as dead and follow to the
		// live successor.
		mergeEPQRefreshSnap(ctx)
		// Follow HOT/non-HOT chain to find the live successor. If the
		// concurrent tx was an UPDATE the chain points to the new version
		// and we re-evaluate WHEN conditions via mergeEPQError. If it was
		// a DELETE (or cross-partition move) no successor exists.
		if epqSlotMovedToAnotherPartition(ctx, rel, blk, slot) {
			return errMovedToAnotherPartition(pos)
		}
		newSlot, newTgtRow, _, predOk2 := epqFollowHOT(ctx, rel, blk, slot, cols, nil, nil)
		if predOk2 {
			return &mergeEPQError{newBlk: blk, newSlot: newSlot, newTgtRow: newTgtRow}
		}
		_, cRes2, chainHadSentinel2 := epqFollowChainFull(ctx, rel, blk, slot, cols, nil, nil)
		if cRes2.ok {
			return &mergeEPQError{newBlk: cRes2.blk, newSlot: cRes2.slot, newTgtRow: cRes2.row}
		}
		if chainHadSentinel2 || epqSlotMovedToAnotherPartition(ctx, rel, blk, slot) {
			return errMovedToAnotherPartition(pos)
		}
		return errMergeSourceUnmatched // row truly deleted; caller retries as NOT MATCHED
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
	// Preserve a pre-existing non-conflicting foreign locker (M0118-0004
	// producer). MERGE DELETE is StatusUpdate (keysUpdated=true) → no-op unless a
	// non-conflicting locker survives; wired for sibling-path parity.
	var mErr error
	if oldGerr == nil {
		mErr = stampUpdaterXmaxNonHOT(ctx, s.Page(), slot, oldTup.Header, true)
	} else {
		mErr = storage.PageSetHeapTupleXmax(s.Page(), slot, effectiveWriterXID(ctx))
	}
	if err := mErr; err != nil {
		s.Unlock()
		ctx.Pool.Unpin(s)
		if errors.Is(err, storage.ErrUnsupportedItem) {
			return nil
		}
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	derr := markHeapDeleteDirtyAndClearVM(ctx, s, rel, blk, slot, effectiveWriterXID(ctx), oldTupleBytes)
	s.Unlock()
	ctx.Pool.Unpin(s)
	return derr
}
