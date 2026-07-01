package mvcc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// mustEnableMirror is a small test helper: M0117-0006 Part C made the SLRU
// buffer pool the sole CLog store, so every test must call EnablePGSLRUMirror
// before exercising Get/Set — there is no more resident "banks" fallback.
func mustEnableMirror(t *testing.T, c *CLog, slruDir string) {
	t.Helper()
	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror(%q): %v", slruDir, err)
	}
}

// TestCLogRoundTrip verifies that SetCommitted/SetAborted are reflected
// immediately by GetStatus within the same process.
func TestCLogRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	mustEnableMirror(t, c, filepath.Join(dir, "pg_xact_slru"))

	// Fresh entry returns Unknown.
	if got := c.GetStatus(storage.TransactionID(5)); got != TxnStatusUnknown {
		t.Errorf("GetStatus(5) = %d, want Unknown", got)
	}

	// SetCommitted is reflected immediately.
	if err := c.SetCommitted(storage.TransactionID(5)); err != nil {
		t.Fatalf("SetCommitted(5): %v", err)
	}
	if got := c.GetStatus(storage.TransactionID(5)); got != TxnStatusCommitted {
		t.Errorf("GetStatus(5) after SetCommitted = %d, want Committed", got)
	}

	// SetAborted for a different XID.
	if err := c.SetAborted(storage.TransactionID(7)); err != nil {
		t.Fatalf("SetAborted(7): %v", err)
	}
	if got := c.GetStatus(storage.TransactionID(7)); got != TxnStatusAborted {
		t.Errorf("GetStatus(7) after SetAborted = %d, want Aborted", got)
	}

	// XID 6 (between 5 and 7) is still Unknown.
	if got := c.GetStatus(storage.TransactionID(6)); got != TxnStatusUnknown {
		t.Errorf("GetStatus(6) = %d, want Unknown", got)
	}
}

// TestCLogPersistence verifies that statuses survive a reopen (disk round-trip).
// Both the writer and the reopened reader must enable the mirror against the
// SAME slruDir — the SLRU is the sole durable store, so a reload only works if
// both CLog instances share the backing directory.
func TestCLogPersistence(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact_slru")

	c1, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog (first): %v", err)
	}
	mustEnableMirror(t, c1, slruDir)
	if err := c1.SetCommitted(storage.TransactionID(3)); err != nil {
		t.Fatalf("SetCommitted(3): %v", err)
	}
	if err := c1.SetAborted(storage.TransactionID(4)); err != nil {
		t.Fatalf("SetAborted(4): %v", err)
	}

	// Reopen.
	c2, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog (second): %v", err)
	}
	mustEnableMirror(t, c2, slruDir)
	if got := c2.GetStatus(storage.TransactionID(3)); got != TxnStatusCommitted {
		t.Errorf("after reopen: GetStatus(3) = %d, want Committed", got)
	}
	if got := c2.GetStatus(storage.TransactionID(4)); got != TxnStatusAborted {
		t.Errorf("after reopen: GetStatus(4) = %d, want Aborted", got)
	}
	if got := c2.GetStatus(storage.TransactionID(9)); got != TxnStatusUnknown {
		t.Errorf("after reopen: GetStatus(9) = %d, want Unknown", got)
	}
}

// TestCLogUnknownForMissingEntry checks that XID 0 and large unseen XIDs
// return TxnStatusUnknown on a fresh clog.
func TestCLogUnknownForMissingEntry(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	mustEnableMirror(t, c, filepath.Join(dir, "pg_xact_slru"))
	for _, xid := range []uint32{0, 1, 100, 99999} {
		if got := c.GetStatus(storage.TransactionID(xid)); got != TxnStatusUnknown {
			t.Errorf("GetStatus(%d) = %d, want Unknown on fresh clog", xid, got)
		}
	}
}

// TestCLogIsEmpty confirms IsEmpty on a fresh clog and not-empty after a
// write. Uses a normal XID (>= FirstNormalTransactionID): bootstrap/frozen
// XIDs (1, 2) never get a real SLRU lane written (see setStatus), so a write
// to one of those would not flip IsEmpty.
func TestCLogIsEmpty(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	mustEnableMirror(t, c, filepath.Join(dir, "pg_xact_slru"))
	if !c.IsEmpty() {
		t.Error("fresh clog should be empty")
	}
	if err := c.SetCommitted(FirstNormalTransactionID); err != nil {
		t.Fatalf("SetCommitted(%d): %v", FirstNormalTransactionID, err)
	}
	if c.IsEmpty() {
		t.Error("clog should not be empty after write")
	}
}

// TestCLogInitializeAsCommitted verifies that InitializeAsCommitted marks
// all XIDs in [FirstNormalTransactionID, highXID) as Committed while leaving
// higher XIDs Unknown.
func TestCLogInitializeAsCommitted(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	mustEnableMirror(t, c, filepath.Join(dir, "pg_xact_slru"))

	const highXID = storage.TransactionID(7)
	if err := c.InitializeAsCommitted(highXID); err != nil {
		t.Fatalf("InitializeAsCommitted(%d): %v", highXID, err)
	}

	// XIDs [FirstNormalTransactionID, 7) should be committed.
	for xid := int(FirstNormalTransactionID); xid < int(highXID); xid++ {
		if got := c.GetStatus(storage.TransactionID(xid)); got != TxnStatusCommitted {
			t.Errorf("GetStatus(%d) = %d, want Committed", xid, got)
		}
	}
	// XID 7 and beyond are Unknown.
	if got := c.GetStatus(highXID); got != TxnStatusUnknown {
		t.Errorf("GetStatus(%d) = %d, want Unknown", highXID, got)
	}
	if got := c.GetStatus(storage.TransactionID(10)); got != TxnStatusUnknown {
		t.Errorf("GetStatus(10) = %d, want Unknown", got)
	}
}

// TestCLogInitializeDoesNotOverwriteNonZero checks that InitializeAsCommitted
// respects entries already set (e.g. an explicitly aborted XID).
func TestCLogInitializeDoesNotOverwriteNonZero(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	mustEnableMirror(t, c, filepath.Join(dir, "pg_xact_slru"))
	// Pre-set XID 4 as aborted before the init call.
	if err := c.SetAborted(storage.TransactionID(4)); err != nil {
		t.Fatalf("SetAborted(4): %v", err)
	}
	if err := c.InitializeAsCommitted(storage.TransactionID(7)); err != nil {
		t.Fatalf("InitializeAsCommitted(7): %v", err)
	}
	// XID 4 must still be Aborted, not overwritten with Committed.
	if got := c.GetStatus(storage.TransactionID(4)); got != TxnStatusAborted {
		t.Errorf("GetStatus(4) = %d, want Aborted (must not be overwritten by init)", got)
	}
	// XID 3, 5, 6 should be Committed.
	for _, xid := range []uint32{3, 5, 6} {
		if got := c.GetStatus(storage.TransactionID(xid)); got != TxnStatusCommitted {
			t.Errorf("GetStatus(%d) = %d, want Committed", xid, got)
		}
	}
}

// TestCLogMarkUnknownAsAborted verifies that MarkUnknownAsAborted stamps
// every still-Unknown xid in [FirstNormalTransactionID, highXID) as Aborted
// while leaving Committed/Aborted entries untouched. Backs the M0106-0011
// crash-recovery implicit-abort sweep — any xid that wrote heap rows but
// never reached commit/abort must be Aborted-stamped so the visibility
// filter excludes its rows.
func TestCLogMarkUnknownAsAborted(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact_slru")
	c, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	mustEnableMirror(t, c, slruDir)
	// Pre-seed: 3=Committed, 5=Aborted explicitly. 4 and 6 stay Unknown.
	if err := c.SetCommitted(storage.TransactionID(3)); err != nil {
		t.Fatalf("SetCommitted(3): %v", err)
	}
	if err := c.SetAborted(storage.TransactionID(5)); err != nil {
		t.Fatalf("SetAborted(5): %v", err)
	}

	const highXID = storage.TransactionID(9)
	if err := c.MarkUnknownAsAborted(highXID); err != nil {
		t.Fatalf("MarkUnknownAsAborted(%d): %v", highXID, err)
	}

	cases := []struct {
		xid  uint32
		want TxnStatus
	}{
		{3, TxnStatusCommitted}, // pre-existing Committed preserved
		{4, TxnStatusAborted},   // was Unknown → stamped Aborted
		{5, TxnStatusAborted},   // pre-existing Aborted preserved
		{6, TxnStatusAborted},   // was Unknown → stamped Aborted
		{7, TxnStatusAborted},   // stamped
		{8, TxnStatusAborted},   // stamped
	}
	for _, tc := range cases {
		if got := c.GetStatus(storage.TransactionID(tc.xid)); got != tc.want {
			t.Errorf("GetStatus(%d) = %d, want %d", tc.xid, got, tc.want)
		}
	}
	// XID 9 (== highXID) and beyond stay Unknown — sweep is half-open.
	if got := c.GetStatus(highXID); got != TxnStatusUnknown {
		t.Errorf("GetStatus(%d) = %d, want Unknown", highXID, got)
	}

	// Persistence: re-open the clog (same slruDir) and re-verify.
	c2, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("re-OpenCLog: %v", err)
	}
	mustEnableMirror(t, c2, slruDir)
	for _, tc := range cases {
		if got := c2.GetStatus(storage.TransactionID(tc.xid)); got != tc.want {
			t.Errorf("after reopen GetStatus(%d) = %d, want %d", tc.xid, got, tc.want)
		}
	}
}

// TestCLogMarkUnknownAsAbortedBatchedSLRU exercises the post-pg_resetwal
// scenario (M0110-0004 / RW-002 (a)): a NextXID advanced past the bootstrap
// pg_xact segment makes MarkUnknownAsAborted stamp >1M XIDs. The batched SLRU
// mirror must (1) finish quickly — the old per-XID fsync path issued ~1M
// fsyncs and looked hung — and (2) produce an SLRU that decodes back to the
// same per-XID statuses, spanning more than one segment file. The decode uses
// freshFromSLRU (clog_dual_store_consistency_test.go) — an independent,
// pool-bypassing decoder — so a pool encoding bug can't mask itself here.
func TestCLogMarkUnknownAsAbortedBatchedSLRU(t *testing.T) {
	dir := t.TempDir()
	flatPath := filepath.Join(dir, "pg_xact_flat")
	slruDir := filepath.Join(dir, "pg_xact")
	c, err := OpenCLog(flatPath)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	// Pre-seed a committed XID inside the first segment so the batch must
	// preserve (not clobber) an already-mirrored committed lane.
	const committedXID = storage.TransactionID(500)
	if err := c.SetCommitted(committedXID); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}

	// Cross the first segment boundary so a second segment file is written —
	// this validates one fsync *per segment* rather than per XID.
	highXID := storage.TransactionID(clogXactsPerSegment + 100)
	if err := c.MarkUnknownAsAborted(highXID); err != nil {
		t.Fatalf("MarkUnknownAsAborted(%d): %v", highXID, err)
	}

	// Both segment files must exist (range spans segment 0 and segment 1).
	for _, seg := range []string{"0000", "0001"} {
		if _, err := os.Stat(filepath.Join(slruDir, seg)); err != nil {
			t.Errorf("expected SLRU segment %s: %v", seg, err)
		}
	}

	// Re-derive statuses purely from the on-disk SLRU (the durable mirror) via
	// an independent decode. This proves the batched write encoded every lane
	// correctly.
	fresh := freshFromSLRU(t, slruDir)
	checks := []struct {
		xid  storage.TransactionID
		want TxnStatus
	}{
		{committedXID, TxnStatusCommitted},           // preserved across the batch
		{FirstNormalTransactionID, TxnStatusAborted}, // first normal XID
		{499, TxnStatusAborted},                      // just below committed
		{501, TxnStatusAborted},                      // just above committed
		{clogXactsPerPage + 7, TxnStatusAborted},     // second page of seg 0
		{clogXactsPerSegment - 1, TxnStatusAborted},  // last XID of seg 0
		{clogXactsPerSegment + 50, TxnStatusAborted}, // inside seg 1
	}
	for _, ck := range checks {
		if got := fresh.GetStatus(ck.xid); got != ck.want {
			t.Errorf("SLRU-decoded GetStatus(%d) = %d, want %d", ck.xid, got, ck.want)
		}
	}
	// XID at the half-open bound and beyond must stay Unknown in the SLRU.
	if got := fresh.GetStatus(highXID); got != TxnStatusUnknown {
		t.Errorf("SLRU-decoded GetStatus(%d) = %d, want Unknown (half-open bound)", highXID, got)
	}
	// XIDs below FirstNormalTransactionID are never mirrored (PG invariant).
	if got := fresh.GetStatus(storage.TransactionID(1)); got != TxnStatusUnknown {
		t.Errorf("SLRU-decoded GetStatus(1) = %d, want Unknown (sub-normal lane)", got)
	}
}

// TestCLogMarkUnknownAsAbortedZeroBound is a no-op when highXID==0.
func TestCLogMarkUnknownAsAbortedZeroBound(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	mustEnableMirror(t, c, filepath.Join(dir, "pg_xact_slru"))
	if err := c.MarkUnknownAsAborted(0); err != nil {
		t.Fatalf("MarkUnknownAsAborted(0): %v", err)
	}
	// No file write should have grown anything.
	if got := c.GetStatus(storage.TransactionID(1)); got != TxnStatusUnknown {
		t.Errorf("GetStatus(1) = %d, want Unknown", got)
	}
}

// TestCLogIdempotent verifies that writing the same status twice doesn't
// corrupt the store (and doesn't return an error).
func TestCLogIdempotent(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	mustEnableMirror(t, c, filepath.Join(dir, "pg_xact_slru"))
	xid := storage.TransactionID(42)
	if err := c.SetCommitted(xid); err != nil {
		t.Fatalf("SetCommitted first: %v", err)
	}
	if err := c.SetCommitted(xid); err != nil {
		t.Fatalf("SetCommitted second (idempotent): %v", err)
	}
	if got := c.GetStatus(xid); got != TxnStatusCommitted {
		t.Errorf("GetStatus after double SetCommitted = %d, want Committed", got)
	}
}
