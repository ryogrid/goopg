package executor

import "testing"

// TestFlushSessionStatsAtExit pins pgstat_shutdown_hook's flush (M0149-0032e):
// a session's pending relation stats are invisible to pg_stat_have_stats
// until the session flushes, and FlushSessionStats — called when the
// connection ends — moves them to the shared store under the session's
// database.
func TestFlushSessionStatsAtExit(t *testing.T) {
	const sid, oid, db = 990001, 990002, 5
	relStats.recordScan(sid, oid, 3)
	if ok, _ := haveStats("relation", db, oid); ok {
		t.Fatal("pending stats visible before the flush")
	}
	FlushSessionStats(sid, db)
	if ok, err := haveStats("relation", db, oid); err != nil || !ok {
		t.Fatalf("after flush pg_stat_have_stats = %v, %v; want true", ok, err)
	}
	FlushSessionStats(0, db) // no session identity: a no-op
}
