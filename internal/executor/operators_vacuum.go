package executor

import (
	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/vacuum"
)

// vacuumOp executes a VACUUM statement, running heap page-prune on the
// target relations and updating the FSM, VM, and relfrozenxid with the
// resulting state (M0046-0003/0004/0005). VACUUM without a target list
// vacuums all user tables.
type vacuumOp struct {
	plan *planner.Utility
	ctx  *Context
	done bool
	outSlot MaterializedSlot
}

func newVacuumOp(p *planner.Utility) *vacuumOp { return &vacuumOp{plan: p} }

func (o *vacuumOp) Schema() planner.Schema { return nil }

func (o *vacuumOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *vacuumOp) Close() error { return nil }

// Next runs the VACUUM as a one-shot side effect. Errors are suppressed:
// a VACUUM failure should not abort the client session.
func (o *vacuumOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	if o.ctx == nil || o.ctx.Pool == nil || o.ctx.TxnMgr == nil {
		return nil, EOF
	}

	vs := o.plan.Stmt.(*parser.VacuumStmt)

	// Compute freeze horizon (M0046-0005): tuples with xmin < freezeBelow
	// will have their xmin rewritten to FrozenTransactionID.
	var freezeBelow storage.TransactionID
	if o.ctx.FreezeMinAge > 0 {
		currentXID := o.ctx.TxnMgr.NextXID()
		if int64(currentXID) > o.ctx.FreezeMinAge {
			freezeBelow = currentXID - storage.TransactionID(o.ctx.FreezeMinAge)
		}
	}

	opts := vacuum.VacuumOptions{
		FSM:         o.ctx.FSM,
		VM:          o.ctx.VM,
		FreezeBelow: freezeBelow,
	}

	for _, tbl := range o.vacuumTableTargets(vs) {
		rel := o.ctx.Catalog.RelFileNode(tbl)
		stats, err := vacuum.VacuumWithOptions(o.ctx.Pool, o.ctx.TxnMgr, rel, opts)
		if err == nil && freezeBelow > 0 && stats.NewFrozenXID != 0 {
			// Advance relfrozenxid to the lowest unfrozen xmin found.
			tbl.RelFrozenXID = stats.NewFrozenXID
		} else if err == nil && freezeBelow > 0 && stats.NewFrozenXID == 0 && stats.Frozen > 0 {
			// All tuples frozen — relfrozenxid advances to freezeBelow.
			tbl.RelFrozenXID = freezeBelow
		}

		// Index vacuum (M0047-0002): remove stale B-tree entries pointing
		// to dead heap tuples and delete any empty leaf pages.
		if err == nil && len(stats.DeadTIDs) > 0 && o.ctx.Pool != nil {
			vacuumIndexes(o.ctx, tbl, stats.DeadTIDs)
		}
	}
	return nil, EOF
}

// vacuumIndexes removes stale B-tree index entries that point to dead heap
// tuples collected during the heap vacuum pass. Empty index leaf pages are
// deleted and the tree is compacted if fully empty (M0047-0002).
func vacuumIndexes(ctx *Context, tbl *catalog.Table, deadTIDs []storage.ItemPointer) {
	indexes := ctx.Catalog.IndexesOnTable(tbl)
	for _, idx := range indexes {
		if idx.Method != "btree" {
			continue
		}
		idxRel := ctx.Catalog.IndexRelFileNode(idx)
		tree, err := btree.Open(ctx.Pool, idxRel)
		if err != nil {
			continue // index may not exist yet (e.g. freshly created)
		}
		_, _ = tree.VacuumIndexPages(deadTIDs)
	}
}

// vacuumTableTargets resolves the *catalog.Table list to vacuum (so we can
// update RelFrozenXID after each pass).
func (o *vacuumOp) vacuumTableTargets(vs *parser.VacuumStmt) []*catalog.Table {
	cat := o.ctx.Catalog
	if len(vs.Targets) > 0 {
		var out []*catalog.Table
		for _, name := range vs.Targets {
			tbl, ok := cat.LookupTable(name)
			if !ok || tbl.Virtual {
				continue
			}
			out = append(out, tbl)
		}
		return out
	}
	if im, ok := cat.(*catalog.InMemory); ok {
		var out []*catalog.Table
		for _, tbl := range im.AllTables() {
			if !tbl.Virtual {
				out = append(out, tbl)
			}
		}
		return out
	}
	return nil
}

// vacuumTargets keeps backward compatibility (used by FSM tests).
func (o *vacuumOp) vacuumTargets(vs *parser.VacuumStmt) []storage.RelFileNode {
	tbls := o.vacuumTableTargets(vs)
	out := make([]storage.RelFileNode, 0, len(tbls))
	for _, t := range tbls {
		out = append(out, o.ctx.Catalog.RelFileNode(t))
	}
	return out
}
