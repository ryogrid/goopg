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

	"github.com/goopg/goopg/internal/catalog"
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
	if err != nil || found == nil {
		return err
	}
	return applyDeleteTuple(ctx, rel, found.ptr)
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
		// update_missing: nothing to update (PG LOGs and skips).
		return nil
	}
	// The unchanged-TOAST and subscriber-only cells come from the located
	// row, in the parent's layout.
	applyFillFromOld(newRow, remapChildRowToParent(found.row, buildInheritColMap(r.local.Columns, oldLeaf.Columns)), newUnchanged, newMissing)
	newLeaf, leafRow, err := w.applyRouteLeaf(ctx, r.local, newRow)
	if err != nil {
		return err
	}
	if newLeaf == oldLeaf {
		conflicts, err := applyFindConflicts(ctx, oldRel, oldLeaf, oldLeaf.Columns, leafRow, &found.ptr)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return applyConflictError(ctx, oldLeaf, oldLeaf.Columns, "update_exists", conflicts, leafRow)
		}
		return applyUpdateFound(ctx, oldRel, oldLeaf, oldLeaf.Columns, found, leafRow)
	}
	// Cross-partition: delete from the old leaf, insert into the new one.
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
	if err := applyDeleteTuple(ctx, oldRel, found.ptr); err != nil {
		return err
	}
	return applyUpdateFound(ctx, newRel, newLeaf, newLeaf.Columns, nil, leafRow)
}
