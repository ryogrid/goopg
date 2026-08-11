package mvcc

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// newTestCLog opens a fresh CLog under a temp dir for fallback tests, with the
// SLRU buffer pool enabled — M0117-0006 Part C made the pool the sole CLog
// store, so every test must enable it before exercising Get/Set.
func newTestCLog(t *testing.T) *CLog {
	t.Helper()
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact_slru")); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	return c
}

// baseSnap builds an in-window snapshot: Xmin=10, Xmax=100, no running XIDs and
// an empty in-memory Aborted list, so every XID in [10,100) is "assumed
// committed" by the pre-M0117-0002 path.
func baseSnap() Snapshot {
	return Snapshot{Xmin: 10, Xmax: 100}
}

// TestSeesCommittedXID_NilCLog_Unchanged confirms a nil CLOG preserves the exact
// pre-M0117-0002 behaviour: an in-window XID not running and not in the Aborted
// array reads as committed.
func TestSeesCommittedXID_NilCLog_Unchanged(t *testing.T) {
	s := baseSnap()
	if s.clog != nil {
		t.Fatal("baseSnap must have nil clog")
	}
	if !s.SeesCommittedXID(50) {
		t.Fatal("in-window XID with nil clog should be assumed committed (unchanged v0 behaviour)")
	}
	// Boundaries unchanged.
	if !s.SeesCommittedXID(5) { // < Xmin
		t.Fatal("XID below Xmin should be committed")
	}
	if s.SeesCommittedXID(100) { // >= Xmax
		t.Fatal("XID at/above Xmax should be invisible")
	}
}

// TestSeesCommittedXID_CLogAborted_Invisible is the G4 fix: an in-window XID the
// in-memory arrays cannot classify, which the durable CLOG records as aborted,
// must be invisible.
func TestSeesCommittedXID_CLogAborted_Invisible(t *testing.T) {
	c := newTestCLog(t)
	if err := c.SetAborted(50); err != nil {
		t.Fatalf("SetAborted: %v", err)
	}
	s := baseSnap().WithCLog(c)
	if s.SeesCommittedXID(50) {
		t.Fatal("in-window XID marked aborted in the CLOG must be invisible (gap G4)")
	}
}

// TestSeesCommittedXID_CLogCommitted_Visible confirms an explicit CLOG commit
// status keeps the XID visible.
func TestSeesCommittedXID_CLogCommitted_Visible(t *testing.T) {
	c := newTestCLog(t)
	if err := c.SetCommitted(50); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}
	s := baseSnap().WithCLog(c)
	if !s.SeesCommittedXID(50) {
		t.Fatal("in-window XID marked committed in the CLOG must be visible")
	}
}

// TestSeesCommittedXID_CLogUnknown_Visible is the no-regression guarantee for the
// live path: goopg's runtime commit does not write CLOG, so freshly-committed
// XIDs read TxnStatusUnknown and must fall through to "assumed committed".
func TestSeesCommittedXID_CLogUnknown_Visible(t *testing.T) {
	c := newTestCLog(t)
	if c.GetStatus(50) != TxnStatusUnknown {
		t.Fatalf("precondition: XID 50 should be Unknown, got %v", c.GetStatus(50))
	}
	s := baseSnap().WithCLog(c)
	if !s.SeesCommittedXID(50) {
		t.Fatal("in-window XID with Unknown CLOG status must fall through to committed (no live-path regression)")
	}
}

// TestSeesCommittedXID_BelowOldestClogXid_SkipsConsult confirms an XID below the
// oldest retained CLOG XID is treated committed/frozen even if a stale Aborted
// byte lingers below the truncation horizon — the consult is skipped.
func TestSeesCommittedXID_BelowOldestClogXid_SkipsConsult(t *testing.T) {
	c := newTestCLog(t)
	// Mark 20 aborted, then advance the oldest-retained horizon past it.
	if err := c.SetAborted(20); err != nil {
		t.Fatalf("SetAborted: %v", err)
	}
	c.AdvanceOldestClogXid(40)
	// Use a snapshot window that includes 20 but whose Xmin is below the horizon
	// so 20 is still "in window" for the residual check.
	s := Snapshot{Xmin: 15, Xmax: 100}.WithCLog(c)
	if !s.SeesCommittedXID(20) {
		t.Fatal("XID below OldestClogXid must be treated committed/frozen (consult skipped)")
	}
	// Sanity: an aborted XID at/above the horizon is still consulted.
	if err := c.SetAborted(50); err != nil {
		t.Fatalf("SetAborted: %v", err)
	}
	if s.SeesCommittedXID(50) {
		t.Fatal("aborted XID at/above OldestClogXid must remain invisible")
	}
}

// TestSeesCommittedXID_InProgressBeatsCLog confirms the running-array fast path
// still wins: an in-progress XID is invisible regardless of CLOG status.
func TestSeesCommittedXID_InProgressBeatsCLog(t *testing.T) {
	c := newTestCLog(t)
	if err := c.SetCommitted(50); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}
	s := Snapshot{Xmin: 10, Xmax: 100, InProgress: []storage.TransactionID{50}}.WithCLog(c)
	if s.SeesCommittedXID(50) {
		t.Fatal("an in-progress XID must be invisible even when the CLOG says committed")
	}
}

// TestSnapshotClone_PreservesCLog confirms Clone carries the fallback CLOG so EPQ
// re-evaluation / origSnap clones make the same visibility decision.
func TestSnapshotClone_PreservesCLog(t *testing.T) {
	c := newTestCLog(t)
	if err := c.SetAborted(50); err != nil {
		t.Fatalf("SetAborted: %v", err)
	}
	s := baseSnap().WithCLog(c).Clone()
	if s.clog == nil {
		t.Fatal("Clone must preserve the clog pointer")
	}
	if s.SeesCommittedXID(50) {
		t.Fatal("cloned snapshot must inherit the CLOG fallback")
	}
}

// TestSeesCommittedXIDWithSubxacts_InheritsCLogFallback audits the sibling path:
// the subxact-aware twin resolves to the top-level ancestor then delegates to
// SeesCommittedXID, so a CLOG-aborted top-level XID makes the subxact row
// invisible without any separate edit in subxact_visibility.go.
func TestSeesCommittedXIDWithSubxacts_InheritsCLogFallback(t *testing.T) {
	c := newTestCLog(t)
	const topXID = storage.TransactionID(50)
	const subXID = storage.TransactionID(60)
	if err := c.SetAborted(topXID); err != nil {
		t.Fatalf("SetAborted: %v", err)
	}
	snap := Snapshot{Xmin: 10, Xmax: 100}.WithCLog(c)

	r := NewSubxactMap()
	r.Register(subXID, topXID)

	if SeesCommittedXIDWithSubxacts(snap, subXID, r) {
		t.Fatal("subxact whose top-level ancestor is CLOG-aborted must be invisible (inherited fallback)")
	}
	// A subxact whose top-level ancestor has no abort record stays visible.
	const topXID2 = storage.TransactionID(55)
	const subXID2 = storage.TransactionID(65)
	r.Register(subXID2, topXID2)
	if !SeesCommittedXIDWithSubxacts(snap, subXID2, r) {
		t.Fatal("subxact with an un-aborted top-level ancestor must be visible")
	}
}

// TestManagerCaptureSnapshot_AttachesCLog confirms the Manager wiring: after
// SetCLog, captured snapshots carry the CLOG and honour a recorded abort.
func TestManagerCaptureSnapshot_AttachesCLog(t *testing.T) {
	m := NewManager()
	c := newTestCLog(t)
	m.SetCLog(c)
	snap := m.captureSnapshot()
	if snap.clog == nil {
		t.Fatal("captureSnapshot must attach the Manager's CLOG after SetCLog")
	}
	// A fresh Manager (no SetCLog) attaches nil → unchanged behaviour.
	if NewManager().captureSnapshot().clog != nil {
		t.Fatal("captureSnapshot must attach nil when SetCLog was never called")
	}
}

// TestSeesCommittedXID_BelowXminAborted_Invisible pins M0131-S30.7: the CLOG
// consult must run BEFORE the `xid < Xmin` shortcut, not only for the in-window
// residual case.
//
// This is the exact post-crash geometry. initdb.Open's MarkUnknownAsAborted
// sweep stamps every XID that was in flight at the crash Aborted, and then
// advances NextXID past all of them, so the first snapshot a post-restart
// session takes has Xmin ABOVE the whole aborted range. Before the fix those
// XIDs took the below-Xmin shortcut and read as committed, which made the
// replayed half of a torn pgbench transaction visible and broke
// sum(pgbench_accounts.abalance) == sum(pgbench_history.delta). Upstream's
// HeapTupleSatisfiesMVCC always resolves a non-hinted xmin through
// TransactionIdDidCommit, whatever the snapshot's xmin is.
func TestSeesCommittedXID_BelowXminAborted_Invisible(t *testing.T) {
	c := newTestCLog(t)
	if err := c.SetAborted(40); err != nil {
		t.Fatalf("SetAborted: %v", err)
	}
	if err := c.SetCommitted(41); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}
	// Xmin/Xmax both above the recovered range, as after a restart with no
	// running transactions.
	s := Snapshot{Xmin: 50, Xmax: 50}.WithCLog(c)
	if s.SeesCommittedXID(40) {
		t.Fatal("an XID the CLOG says aborted must be invisible even below Xmin")
	}
	// The shortcut's positive direction is unchanged.
	if !s.SeesCommittedXID(41) {
		t.Fatal("a committed XID below Xmin must stay visible")
	}
	// An XID with no CLOG lane at all still falls through to committed — the
	// consult only acts on a POSITIVE abort (conservative contract).
	if !s.SeesCommittedXID(42) {
		t.Fatal("an unknown-status XID below Xmin must stay visible")
	}
}
