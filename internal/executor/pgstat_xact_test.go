package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
)

// TestExecuteTransactionalStatsDrops pins the standby's replay of
// dropped-stats items (pgstat_execute_transactional_drops, M0149-0032c): a
// relation or function item drops that entry; an item naming another
// database leaves a same-OID entry alone (PG keys on dboid too); a database
// item drops the database entry and everything attributed to it.
func TestExecuteTransactionalStatsDrops(t *testing.T) {
	const sid, otherSid = 990101, 990102
	const db, otherDB = 990110, 990111
	const rel, fn, keep, otherRel = 990120, 990121, 990122, 990123
	relStats.recordScan(sid, rel, 1)
	relStats.recordScan(sid, keep, 1)
	funcStats.record(sid, fn, 0, 0)
	FlushSessionStats(sid, db)
	relStats.recordScan(otherSid, otherRel, 1)
	FlushSessionStats(otherSid, otherDB)
	have := func(kind string, d uint32, oid uint64) bool {
		t.Helper()
		ok, err := haveStats(kind, d, oid)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !have("relation", db, rel) || !have("function", db, fn) || !have("database", db, 0) {
		t.Fatal("stats missing before the drops")
	}

	// An item for the same OID in another database is not this entry.
	ExecuteTransactionalStatsDrops([]transam.XactStatsItem{{Kind: transam.PgStatKindRelation, DBOid: otherDB, ObjID: rel}})
	if !have("relation", db, rel) {
		t.Fatal("an item of another database dropped the entry")
	}
	ExecuteTransactionalStatsDrops([]transam.XactStatsItem{
		{Kind: transam.PgStatKindRelation, DBOid: db, ObjID: rel},
		{Kind: transam.PgStatKindFunction, DBOid: db, ObjID: fn},
	})
	if have("relation", db, rel) || have("function", db, fn) {
		t.Fatal("relation/function stats survive their items")
	}
	if !have("relation", db, keep) {
		t.Fatal("an unrelated relation lost its stats")
	}

	// A database item drops the database and its contents only.
	ExecuteTransactionalStatsDrops([]transam.XactStatsItem{{Kind: transam.PgStatKindDatabase, DBOid: db}})
	if have("database", db, 0) || have("relation", db, keep) {
		t.Fatal("database item left the database or its relation")
	}
	if !have("relation", otherDB, otherRel) {
		t.Fatal("database item dropped another database's relation")
	}
	ExecuteTransactionalStatsDrops([]transam.XactStatsItem{{Kind: transam.PgStatKindDatabase, DBOid: otherDB}})
}

// TestNoteFlushDefaultDatabase: a session of the default database reports
// database OID 0; its objects are attributed to the postgres database, so
// the stats file keeps them under the right dboid across a restart
// (M0149-0032c).
func TestNoteFlushDefaultDatabase(t *testing.T) {
	const sid, oid = 990201, 990202
	relStats.recordScan(sid, oid, 1)
	FlushSessionStats(sid, 0)
	dbStats.mu.Lock()
	got := dbStats.relDB[oid]
	dbStats.mu.Unlock()
	if got != 5 {
		t.Fatalf("relation attributed to db %d, want 5 (postgres)", got)
	}
	ExecuteTransactionalStatsDrops([]transam.XactStatsItem{{Kind: transam.PgStatKindRelation, DBOid: 5, ObjID: oid}})
}
