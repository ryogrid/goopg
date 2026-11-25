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

// ExecuteTransactionalStatsDrops is pgstat_execute_transactional_drops
// (pgstat_xact.c), called by a standby for each replayed commit or abort
// record that carries dropped-stats items (xact_redo_commit /
// xact_redo_abort). A relation or function item drops that entry; a database
// item drops the database's entry and every relation and function entry
// attributed to it (pgstat_drop_entry → pgstat_drop_database_and_contents).
// An item for an entry that does not exist is a no-op. M0149-0032c.
func ExecuteTransactionalStatsDrops(items []transam.XactStatsItem) {
	for _, it := range items {
		switch it.Kind {
		case transam.PgStatKindRelation:
			if dbStats.forgetObject(true, it.DBOid, uint32(it.ObjID)) {
				relStats.dropTable(uint32(it.ObjID))
			}
		case transam.PgStatKindFunction:
			if dbStats.forgetObject(false, it.DBOid, uint32(it.ObjID)) {
				funcStats.dropFunction(uint32(it.ObjID))
			}
		case transam.PgStatKindDatabase:
			rels, funcs := dbStats.dropDatabase(it.DBOid)
			for _, oid := range rels {
				relStats.dropTable(oid)
			}
			for _, oid := range funcs {
				funcStats.dropFunction(oid)
			}
		}
	}
}

// forgetObject removes a dropped relation's (rel) or function's database
// attribution. The stores key on the OID alone, so it reports whether the
// entry belongs to dboid (or has no attribution) and may be dropped: an item
// for another database's object of the same OID leaves this one alone, as
// PG's (kind, dboid, objid) key does.
func (m *dbStatsManager) forgetObject(rel bool, dboid, oid uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	attr := m.funcDB
	if rel {
		attr = m.relDB
	}
	if db, ok := attr[oid]; ok && db != dboid {
		return false
	}
	delete(attr, oid)
	return true
}

// dropDatabase removes dboid's database entry and returns the relations and
// functions attributed to it, whose entries the caller drops.
func (m *dbStatsManager) dropDatabase(dboid uint32) (rels, funcs []uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.databases, dboid)
	for oid, db := range m.relDB {
		if db == dboid {
			rels = append(rels, oid)
			delete(m.relDB, oid)
		}
	}
	for oid, db := range m.funcDB {
		if db == dboid {
			funcs = append(funcs, oid)
			delete(m.funcDB, oid)
		}
	}
	return rels, funcs
}
