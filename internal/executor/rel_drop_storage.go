package executor

import (
	"log/slog"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
)

// dropRelationStorage is RelationDropStorage (catalog/storage.c): DROP TABLE,
// DROP INDEX and DROP MATERIALIZED VIEW schedule the relation's storage for
// removal at commit instead of removing it during the statement. So:
//   - a rolled-back DROP, or one inside a rolled-back savepoint, keeps its
//     data, whatever happens to the catalog entry;
//   - the commit record names the relation (XACT_XINFO_HAS_RELFILELOCATORS),
//     which replay and the WAL summarizer read;
//   - the files go only after the commit record is written, as
//     smgrDoPendingDeletes runs after RecordTransactionCommit.
//
// heap marks a table or materialized view, whose free-space and visibility
// maps go with it. Contexts with no WAL writer or transaction manager (unit
// fixtures that never commit) remove the storage at once, as before.
func dropRelationStorage(ctx *Context, rel storage.RelFileNode, heap bool) error {
	pool, fsm, vm := ctx.Pool, ctx.FSM, ctx.VM
	if ctx.TxnMgr != nil && ctx.WAL != nil && ctx.MaterializeWriterXID() == nil {
		ctx.TxnMgr.AddPendingRelDelete(ctx.TxnMgr.TopLevelXid(ctx.Tx.XID), transam.PendingRelDelete{
			Rel: rel,
			Xid: effectiveWriterXID(ctx),
			Drop: func() {
				if err := removeRelationStorage(pool, fsm, vm, rel, heap); err != nil {
					// PG warns and carries on: the transaction has committed.
					slog.Warn("could not remove relation storage at commit", "rel", rel, "err", err)
				}
			},
		})
		return nil
	}
	if err := removeRelationStorage(pool, fsm, vm, rel, heap); err != nil {
		return &ExecError{Code: "XX000", Message: err.Error()}
	}
	return nil
}

// removeRelationStorage drops rel's buffers and file, and for a heap its
// free-space and visibility maps (smgrdounlinkall).
func removeRelationStorage(pool *storage.Pool, fsm *storage.FSM, vm *storage.VisibilityMap, rel storage.RelFileNode, heap bool) error {
	if pool != nil {
		pool.InvalidateRel(rel)
		if err := pool.Manager().DropRelation(rel); err != nil {
			return err
		}
	}
	if heap {
		if fsm != nil {
			fsm.DropRelation(rel)
		}
		if vm != nil {
			vm.DropRelation(rel)
		}
	}
	return nil
}
