package storage

import (
	"strings"
	"testing"
)

// newAssertPool builds a pool with WAL logging wired (LogPageImage non-nil is
// the pool's own "this runtime writes WAL" signal, and the one the guard keys
// on) plus one initialised page pinned for mutation.
func newAssertPool(t *testing.T) (*Pool, *Slot) {
	t.Helper()
	mgr := NewManager(ManagerConfig{DataDir: t.TempDir()})
	t.Cleanup(func() { mgr.Close() })

	var nextLSN LSN = 100
	pool, err := NewPool(mgr, PoolConfig{
		Slots: 4,
		LogPageImage: func(_ RelFileNode, _ BlockNumber, _ Page) (LSN, error) {
			nextLSN++
			return nextLSN, nil
		},
		FullPageWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })

	s, _, err := pool.PinNew(RelFileNode{DBOid: 1, RelOid: 626, Fork: MainFork})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Unpin(s) })
	if err := InitPage(s.Page()); err != nil {
		t.Fatal(err)
	}
	return pool, s
}

// TestPDLSNAssertReportsPlainMarkDirty pins the M0131-S26 guard
// (docs/design/0131-0033, deferred item 1). The audit that found the cross-page
// UPDATE defect was done by inspection, so a new call site could reintroduce it
// silently. The guard is the mechanised replacement: in a runtime whose WAL
// hooks are wired, no page mutation should reach the pool through *plain*
// MarkDirty, because plain MarkDirty advances pd_lsn only as a side effect of
// the first-touch image and leaves any later-touch mutation behind its own
// record.
func TestPDLSNAssertReportsPlainMarkDirty(t *testing.T) {
	pool, s := newAssertPool(t)

	var msgs []string
	restore := SetPDLSNAssertSinkForTest(func(m string) { msgs = append(msgs, m) })
	defer restore()

	s.Lock()
	s.Page()[300]++
	pool.MarkDirty(s)
	s.Unlock()

	if len(msgs) != 1 {
		t.Fatalf("reports = %d (%v), want 1 for a plain MarkDirty under a wired pool", len(msgs), msgs)
	}
	if !strings.HasPrefix(msgs[0], "PDLSN-UNSTAMPED ") {
		t.Errorf("report = %q, want the PDLSN-UNSTAMPED tag", msgs[0])
	}
	// The report must name the call site, not the pool internals — its whole
	// purpose is telling a human which caller to classify.
	if !strings.Contains(msgs[0], "TestPDLSNAssertReportsPlainMarkDirty") {
		t.Errorf("report = %q, want it to attribute the calling function", msgs[0])
	}

	// One report per call SITE (a site is the calling instruction, so the two
	// fallback branches of one helper report separately — they are distinct
	// sites to classify). A hot site repeated would otherwise drown the set of
	// distinct sites, which is the output that matters.
	for i := 0; i < 3; i++ {
		s.Lock()
		s.Page()[301]++
		pool.MarkDirty(s)
		s.Unlock()
	}
	if len(msgs) != 2 {
		t.Fatalf("reports = %d (%v), want 2: the first site plus the repeated one reported once", len(msgs), msgs)
	}
}

// TestPDLSNAssertSilentForStampingAndUnloggedVariants pins the exemptions: a
// variant that stamps pd_lsn is correct by construction, MarkDirtyUnlogged
// names a deliberate class-B exemption, and hint marks are exempt by contract
// (hint bits are recomputable from pg_xact and PG does not log them either).
// Without these exemptions the guard's report is noise and nobody reads it.
func TestPDLSNAssertSilentForStampingAndUnloggedVariants(t *testing.T) {
	pool, s := newAssertPool(t)

	var msgs []string
	restore := SetPDLSNAssertSinkForTest(func(m string) { msgs = append(msgs, m) })
	defer restore()

	s.Lock()
	s.Page()[300]++
	pool.MarkDirtyWithLSNLocked(s, 500)
	s.Page()[301]++
	pool.MarkDirtyCoveredByRecordLocked(s, 600)
	s.Page()[302]++
	pool.MarkDirtyUnlogged(s, "test: deliberate class-B mutation")
	s.Unlock()
	pool.MarkDirtyHint(s)

	if len(msgs) != 0 {
		t.Fatalf("reports = %v, want none from the stamping/unlogged/hint variants", msgs)
	}
}

// TestPDLSNAssertSilentWhenWALUnwired pins the other half of the gate: a pool
// with no LogPageImage hook writes no WAL at all (test harnesses, pre-runtime
// callers), so plain MarkDirty there is class A — nothing was emitted, so
// nothing can be behind it.
func TestPDLSNAssertSilentWhenWALUnwired(t *testing.T) {
	mgr := NewManager(ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s, _, err := pool.PinNew(RelFileNode{DBOid: 1, RelOid: 626, Fork: MainFork})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(s)
	if err := InitPage(s.Page()); err != nil {
		t.Fatal(err)
	}

	var msgs []string
	restore := SetPDLSNAssertSinkForTest(func(m string) { msgs = append(msgs, m) })
	defer restore()

	s.Lock()
	s.Page()[300]++
	pool.MarkDirty(s)
	s.Unlock()

	if len(msgs) != 0 {
		t.Fatalf("reports = %v, want none when the pool has no WAL hook", msgs)
	}
}

// TestPDLSNAssertOffByDefault keeps the guard off the hot path: with no sink
// installed and GOOPG_PDLSN_ASSERT unset, the flag read is the only cost.
func TestPDLSNAssertOffByDefault(t *testing.T) {
	if PDLSNAssertEnabled() {
		t.Fatal("pd_lsn guard is enabled by default; it must be opt-in via GOOPG_PDLSN_ASSERT=1")
	}
}
