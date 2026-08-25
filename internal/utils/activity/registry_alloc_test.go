package activity

import "testing"

// TestWaitEventStartEndZeroAllocs pins the pg_stat_activity recording
// contract: WaitEventStart/WaitEventEnd are on every statement's hot path
// and must stay allocation-free (design docs/design/not_ralph/
// pg-stat-activity-probes/03-design.md invariant I2). The wait identity is
// packed via the init-time interned maps, so a regression that starts
// allocating here would also add GC sweep pressure under load.
func TestWaitEventStartEndZeroAllocs(t *testing.T) {
	r := NewActivityRegistry(8)
	pn := int32(2)

	got := testing.AllocsPerRun(200, func() {
		r.WaitEventStart(pn, WaitTypeIO, WaitWALWrite)
		r.WaitEventEnd(pn)
	})
	if got != 0 {
		t.Fatalf("WaitEventStart/End allocs = %v, want 0", got)
	}

	// The Lock/Timeout events added by the probe work must be interned too:
	// an unmaped name would pack to 0 silently.
	if code := packWaitStrings(WaitTypeLock, WaitTransactionID); code == 0 {
		t.Fatal("packWaitStrings(Lock, transactionid) = 0 (not interned)")
	}
	if code := packWaitStrings(WaitTypeLock, WaitAdvisoryLock); code == 0 {
		t.Fatal("packWaitStrings(Lock, advisory) = 0 (not interned)")
	}
	if code := packWaitStrings(WaitTypeTimeout, WaitPgSleep); code == 0 {
		t.Fatal("packWaitStrings(Timeout, PgSleep) = 0 (not interned)")
	}
}
