package executor

import (
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
)

// noteStatsDrop is pgstat_drop_transactional (pgstat_xact.c): the running
// transaction dropped an object, so its commit record names the object's
// cumulative-statistics entry (XACT_XINFO_HAS_DROPPED_STATS) and a standby
// replaying the commit drops the entry too. kind is one of
// transam.PgStatKind*; dboid is the object's database (0 for a shared
// object); objid its OID. Contexts with no transaction manager (unit
// fixtures that never commit) record nothing. M0149-0032b.
func noteStatsDrop(ctx *Context, kind int32, dboid uint32, objid uint32) {
	if ctx == nil || ctx.TxnMgr == nil {
		return
	}
	if (kind == transam.PgStatKindDatabase && dboid == 0) || (kind != transam.PgStatKindDatabase && objid == 0) {
		return
	}
	if ctx.MaterializeWriterXID() != nil {
		return
	}
	ctx.TxnMgr.AddPendingStatsDrop(ctx.TxnMgr.TopLevelXid(ctx.Tx.XID), transam.PendingStatsDrop{
		Item: transam.XactStatsItem{Kind: kind, DBOid: dboid, ObjID: uint64(objid)},
		Xid:  effectiveWriterXID(ctx),
	})
}

// noteRelationStatsDrop is pgstat_drop_relation's transactional half for a
// relation of the current database.
func noteRelationStatsDrop(ctx *Context, reloid uint32) {
	if ctx == nil {
		return
	}
	noteStatsDrop(ctx, transam.PgStatKindRelation, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid), reloid)
}

// noteFunctionStatsDrop is pgstat_drop_function's transactional half.
func noteFunctionStatsDrop(ctx *Context, funcoid uint32) {
	if ctx == nil {
		return
	}
	noteStatsDrop(ctx, transam.PgStatKindFunction, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid), funcoid)
}

// NoteDatabaseStatsDrop is pgstat_drop_database (dbcommands.c dropdb): the
// DROP DATABASE transaction's commit record names the database's stats
// entry, whose replay drops the database's stats and everything in it.
func NoteDatabaseStatsDrop(ctx *Context, dboid uint32) {
	noteStatsDrop(ctx, transam.PgStatKindDatabase, dboid, 0)
}
