package executor

import (
	"log/slog"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
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
			Rel:      rel,
			Xid:      effectiveWriterXID(ctx),
			AtCommit: true,
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

// createdRelationStorage is RelationCreateStorage's pending delete
// (catalog/storage.c): a relation CREATE TABLE / CREATE INDEX / CREATE
// MATERIALIZED VIEW made is scheduled for removal if the transaction aborts.
// The abort record lists it (XACT_XINFO_HAS_RELFILELOCATORS), the storage goes
// after that record, and replay of the abort removes the files too, so a
// crash after the abort leaves no orphan. It reports whether the removal was
// scheduled; when it was not (no WAL writer or transaction manager), the
// caller's ROLLBACK undo removes the file itself.
func createdRelationStorage(ctx *Context, rel storage.RelFileNode, heap bool) bool {
	if ctx.TxnMgr == nil || ctx.WAL == nil || ctx.MaterializeWriterXID() != nil {
		return false
	}
	pool, fsm, vm := ctx.Pool, ctx.FSM, ctx.VM
	ctx.TxnMgr.AddPendingRelDelete(ctx.TxnMgr.TopLevelXid(ctx.Tx.XID), transam.PendingRelDelete{
		Rel: rel,
		Xid: effectiveWriterXID(ctx),
		Drop: func() {
			if err := removeRelationStorage(pool, fsm, vm, rel, heap); err != nil {
				slog.Warn("could not remove relation storage at abort", "rel", rel, "err", err)
			}
		},
	})
	return true
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
			// The commit record's dropped-relation list removes the fork at
			// replay; PG writes nothing else (M0149-0002).
			vm.DropRelationNoWAL(rel)
		}
	}
	return nil
}

// logRelationTruncate WAL-logs a truncation of rel to zero blocks as
// RelationTruncate does (catalog/storage.c): XLOG_SMGR_TRUNCATE with
// SMGR_TRUNCATE_ALL, under the transaction's xid. goopg truncates a relation
// in place where PG gives TRUNCATE a new relfilenode, so this is the record PG
// writes for its own in-place case (a relation created in the same
// transaction). Without it, replay rebuilds the pre-TRUNCATE pages from the
// earlier inserts and the post-TRUNCATE rows land among them. Temporary and
// unlogged relations are not logged (RelationNeedsWAL). M0149-0002.
func logRelationTruncate(ctx *Context, tbl *catalog.Table, rel storage.RelFileNode) error {
	if ctx.Pool == nil || tbl.Temp || tbl.Unlogged {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	payload, err := xlog.EncodeSmgrTruncatePG(rel, 0, xlog.SmgrTruncateAll, ctx.Tx.XID)
	if err != nil {
		return err
	}
	_, err = ctx.Pool.LogChangeRecord(payload)
	return err
}
