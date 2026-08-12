package initdb

import (
	"path/filepath"
	"testing"
)

// TestOpenAttachesCLogToTxnManager is the M0131-S30.7 wiring guard.
//
// mvcc.Manager.SetCLog was added by M0117-0002 with the contract "called once
// during startup/recovery wiring (initdb.Open)", but the call was never
// written: `grep -rn "SetCLog("` matched only the definition. Snapshot.clog was
// therefore nil on every live server and the durable-abort fallback in
// Snapshot.SeesCommittedXID — the mechanism that hides the heap changes of
// transactions the crash-recovery MarkUnknownAsAborted sweep stamped Aborted —
// never ran. The visible symptom was a torn pgbench transaction surviving a
// crash: sum(pgbench_accounts.abalance) > sum(pgbench_history.delta).
//
// A unit test cannot see the dead-code condition through behaviour (the
// fallback only matters after a crash), so assert the wiring directly.
// See docs/design/0131-0027-post-recovery-aborted-xid-visibility.md.
func TestOpenAttachesCLogToTxnManager(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if !rt.TxnMgr.HasCLog() {
		t.Fatal("initdb.Open must install the durable CLOG on the transaction manager " +
			"(mvcc.Manager.SetCLog); without it every snapshot's durable-abort " +
			"fallback is dead and recovered aborts read as committed")
	}
}
