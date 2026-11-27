package executor

// applyworker_partition.go — DELETE / UPDATE for a partitioned local table
// (apply_handle_tuple_routing, worker.c; M0149-0057e,
// subscription/t/027_nosuperuser.pl). INSERT routing is in applyInsert
// (M0149-0056b).
//
// PG routes the remote old tuple to the leaf partition it belongs to
// (ExecFindPartition), finds the row there, and:
//   - DELETE deletes it from that leaf;
//   - UPDATE updates it in place when the new tuple still belongs to the
//     same leaf, and otherwise deletes it there and inserts the new tuple
//     into the leaf it routes to (a cross-partition move).

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// applyRouteLeaf is ExecFindPartition for the apply worker: the leaf of
// partitioned tbl that row (in tbl's column layout) belongs to, and row in
// the leaf's layout.
func (w *ApplyWorker) applyRouteLeaf(ctx *Context, tbl *catalog.Table, row Row) (*catalog.Table, Row, error) {
	im, ok := underlyingInMemory(w.cat)
	if !ok {
		return nil, nil, fmt.Errorf("applyworker: routing into partitioned %q needs an in-memory catalog", tbl.Name)
	}
	leaf, err := routeToPartition(tbl, row, im, ctx)
	if err != nil {
		return nil, nil, err
	}
	if leaf == tbl {
		return tbl, row, nil
	}
	return leaf, remapParentRowToChild(row, nil, tbl.Columns, leaf.Columns), nil
}

func (w *ApplyWorker) applyDeletePartitioned(ctx *Context, r *applyRel, keyRow Row) error {
	leaf, key, err := w.applyRouteLeaf(ctx, r.local, keyRow)
	if err != nil {
		return err
	}
	if err := applyTableAccess(ctx, w, leaf, "delete"); err != nil {
		return err
	}
	rel := w.cat.RelFileNode(leaf)
	found, err := applyFindTuple(ctx, rel, leaf, leaf.Columns, key, applyLocalIndex(ctx, leaf, r.remote, nil))
	if err != nil {
		return err
	}
	if found == nil {
		// delete_missing on the leaf the row would live in. M0149-0061e.
		w.logApplyConflict(ctx, leaf, leaf.Columns, "delete_missing", "Could not find the row to be deleted.", nil, nil, key)
		return nil
	}
	w.checkOriginDiffers(ctx, leaf, leaf.Columns, "delete", found, nil, key)
	return w.applyLeafDelete(ctx, leaf, rel, found)
}

// applyLeafDelete is ExecSimpleRelationDelete on a leaf partition: the
// leaf's BEFORE ROW triggers may skip the delete, its AFTER ROW triggers
// follow it. M0149-0061c.
func (w *ApplyWorker) applyLeafDelete(ctx *Context, leaf *catalog.Table, rel storage.RelFileNode, found *applyFoundTuple) error {
	trigCtx := w.triggerContext(ctx)
	if tableHasTrigger(leaf, "before", "delete", true) {
		_, keep, err := fireTriggers(trigCtx, leaf, "before", "delete", found.row, nil)
		if err != nil {
			return fmt.Errorf("applyworker: BEFORE DELETE trigger on %q: %w", leaf.Name, err)
		}
		if !keep {
			return nil
		}
	}
	if err := applyDeleteTuple(ctx, rel, found.ptr); err != nil {
		return err
	}
	if tableHasTrigger(leaf, "after", "delete", true) {
		if err := queueAfterRowTriggers(trigCtx, leaf, "delete", found.row, nil, nil); err != nil {
			return fmt.Errorf("applyworker: AFTER DELETE trigger on %q: %w", leaf.Name, err)
		}
	}
	return nil
}

func (w *ApplyWorker) applyUpdatePartitioned(ctx *Context, r *applyRel, oldKeyRow, newRow Row, newUnchanged, newMissing []bool) error {
	oldLeaf, oldKey, err := w.applyRouteLeaf(ctx, r.local, oldKeyRow)
	if err != nil {
		return err
	}
	if err := applyTableAccess(ctx, w, oldLeaf, "update"); err != nil {
		return err
	}
	oldRel := w.cat.RelFileNode(oldLeaf)
	found, err := applyFindTuple(ctx, oldRel, oldLeaf, oldLeaf.Columns, oldKey, applyLocalIndex(ctx, oldLeaf, r.remote, nil))
	if err != nil {
		return err
	}
	if found == nil {
		// update_missing: nothing to update (PG LOGs and skips); the
		// remote row is shown in the leaf's layout. M0149-0061e.
		w.logApplyConflict(ctx, oldLeaf, oldLeaf.Columns, "update_missing", "Could not find the row to be updated.", nil,
			remapParentRowToChild(newRow, nil, r.local.Columns, oldLeaf.Columns), oldKey)
		return nil
	}
	w.checkOriginDiffers(ctx, oldLeaf, oldLeaf.Columns, "update", found,
		remapParentRowToChild(newRow, nil, r.local.Columns, oldLeaf.Columns), oldKey)
	// The unchanged-TOAST and subscriber-only cells come from the located
	// row, in the parent's layout.
	applyFillFromOld(newRow, remapChildRowToParent(found.row, buildInheritColMap(r.local.Columns, oldLeaf.Columns)), newUnchanged, newMissing)
	newLeaf, leafRow, err := w.applyRouteLeaf(ctx, r.local, newRow)
	if err != nil {
		return err
	}
	trigCtx := w.triggerContext(ctx)
	if newLeaf == oldLeaf {
		// ExecSimpleRelationUpdate on the leaf: its BEFORE ROW triggers
		// may replace or skip the new row, its AFTER ROW triggers follow.
		// M0149-0061c.
		updCols := remoteUpdatedColumns(oldLeaf.Columns,
			remapParentFlagsToChild(newUnchanged, r.local.Columns, oldLeaf.Columns),
			remapParentFlagsToChild(newMissing, r.local.Columns, oldLeaf.Columns))
		if tableHasTrigger(oldLeaf, "before", "update", true) {
			var keep bool
			leafRow, keep, err = fireTriggersCols(trigCtx, oldLeaf, "before", "update", found.row, leafRow, updCols)
			if err != nil {
				return fmt.Errorf("applyworker: BEFORE UPDATE trigger on %q: %w", oldLeaf.Name, err)
			}
			if !keep {
				return nil
			}
		}
		conflicts, err := applyFindConflicts(ctx, oldRel, oldLeaf, oldLeaf.Columns, leafRow, &found.ptr)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return applyConflictError(ctx, oldLeaf, oldLeaf.Columns, "update_exists", conflicts, leafRow)
		}
		if err := applyUpdateFound(ctx, oldRel, oldLeaf, oldLeaf.Columns, found, leafRow); err != nil {
			return err
		}
		if tableHasTrigger(oldLeaf, "after", "update", true) {
			if err := queueAfterRowTriggers(trigCtx, oldLeaf, "update", found.row, leafRow, updCols); err != nil {
				return fmt.Errorf("applyworker: AFTER UPDATE trigger on %q: %w", oldLeaf.Name, err)
			}
		}
		return nil
	}
	// Cross-partition (apply_handle_tuple_routing): delete from the old
	// leaf (apply_handle_delete_internal) and insert into the new one
	// (apply_handle_insert_internal), each firing that leaf's DELETE /
	// INSERT row triggers. M0149-0061c.
	if err := applyTableAccess(ctx, w, newLeaf, "insert"); err != nil {
		return err
	}
	newRel := w.cat.RelFileNode(newLeaf)
	conflicts, err := applyFindConflicts(ctx, newRel, newLeaf, newLeaf.Columns, leafRow, nil)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return applyConflictError(ctx, newLeaf, newLeaf.Columns, "update_exists", conflicts, leafRow)
	}
	if err := w.applyLeafDelete(ctx, oldLeaf, oldRel, found); err != nil {
		return err
	}
	if tableHasTrigger(newLeaf, "before", "insert", true) {
		var keep bool
		leafRow, keep, err = fireTriggers(trigCtx, newLeaf, "before", "insert", nil, leafRow)
		if err != nil {
			return fmt.Errorf("applyworker: BEFORE INSERT trigger on %q: %w", newLeaf.Name, err)
		}
		if !keep {
			return nil
		}
	}
	if err := applyUpdateFound(ctx, newRel, newLeaf, newLeaf.Columns, nil, leafRow); err != nil {
		return err
	}
	if tableHasTrigger(newLeaf, "after", "insert", true) {
		if err := queueAfterRowTriggers(trigCtx, newLeaf, "insert", nil, leafRow, nil); err != nil {
			return fmt.Errorf("applyworker: AFTER INSERT trigger on %q: %w", newLeaf.Name, err)
		}
	}
	return nil
}

// remapParentFlagsToChild carries per-column flags from a partitioned
// parent's layout to a leaf's (matched by column name).
func remapParentFlagsToChild(flags []bool, parent, child []catalog.Column) []bool {
	if flags == nil {
		return nil
	}
	out := make([]bool, len(child))
	for ci, cc := range child {
		for pi, pc := range parent {
			if pi < len(flags) && strings.EqualFold(pc.Name, cc.Name) {
				out[ci] = flags[pi]
				break
			}
		}
	}
	return out
}
