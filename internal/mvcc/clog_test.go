package mvcc

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestCLogRoundTrip verifies that SetCommitted/SetAborted are reflected
// immediately by GetStatus within the same process.
func TestCLogRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg_xact")
	c, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}

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
func TestCLogPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg_xact")

	c1, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog (first): %v", err)
	}
	if err := c1.SetCommitted(storage.TransactionID(3)); err != nil {
		t.Fatalf("SetCommitted(3): %v", err)
	}
	if err := c1.SetAborted(storage.TransactionID(4)); err != nil {
		t.Fatalf("SetAborted(4): %v", err)
	}

	// Reopen.
	c2, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog (second): %v", err)
	}
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
	path := filepath.Join(t.TempDir(), "pg_xact")
	c, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	for _, xid := range []uint32{0, 1, 100, 99999} {
		if got := c.GetStatus(storage.TransactionID(xid)); got != TxnStatusUnknown {
			t.Errorf("GetStatus(%d) = %d, want Unknown on fresh clog", xid, got)
		}
	}
}

// TestCLogIsEmpty confirms IsEmpty on a fresh clog and not-empty after a write.
func TestCLogIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg_xact")
	c, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if !c.IsEmpty() {
		t.Error("fresh clog should be empty")
	}
	if err := c.SetCommitted(storage.TransactionID(1)); err != nil {
		t.Fatalf("SetCommitted(1): %v", err)
	}
	if c.IsEmpty() {
		t.Error("clog should not be empty after write")
	}
}

// TestCLogInitializeAsCommitted verifies that InitializeAsCommitted marks
// all XIDs in [1, highXID) as Committed while leaving higher XIDs Unknown.
func TestCLogInitializeAsCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg_xact")
	c, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}

	const highXID = storage.TransactionID(5)
	if err := c.InitializeAsCommitted(highXID); err != nil {
		t.Fatalf("InitializeAsCommitted(%d): %v", highXID, err)
	}

	// XIDs [1,5) should be committed.
	for _, xid := range []uint32{1, 2, 3, 4} {
		if got := c.GetStatus(storage.TransactionID(xid)); got != TxnStatusCommitted {
			t.Errorf("GetStatus(%d) = %d, want Committed", xid, got)
		}
	}
	// XID 5 and beyond are Unknown.
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
	path := filepath.Join(t.TempDir(), "pg_xact")
	c, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	// Pre-set XID 2 as aborted before the init call.
	if err := c.SetAborted(storage.TransactionID(2)); err != nil {
		t.Fatalf("SetAborted(2): %v", err)
	}
	if err := c.InitializeAsCommitted(storage.TransactionID(5)); err != nil {
		t.Fatalf("InitializeAsCommitted(5): %v", err)
	}
	// XID 2 must still be Aborted, not overwritten with Committed.
	if got := c.GetStatus(storage.TransactionID(2)); got != TxnStatusAborted {
		t.Errorf("GetStatus(2) = %d, want Aborted (must not be overwritten by init)", got)
	}
	// XID 1, 3, 4 should be Committed.
	for _, xid := range []uint32{1, 3, 4} {
		if got := c.GetStatus(storage.TransactionID(xid)); got != TxnStatusCommitted {
			t.Errorf("GetStatus(%d) = %d, want Committed", xid, got)
		}
	}
}

// TestCLogMarkUnknownAsAborted verifies that MarkUnknownAsAborted stamps
// every still-Unknown xid in [1, highXID) as Aborted while leaving
// Committed/Aborted entries untouched. Backs the M0106-0011 crash-recovery
// implicit-abort sweep — any xid that wrote heap rows but never reached
// commit/abort must be Aborted-stamped so the visibility filter excludes
// its rows.
func TestCLogMarkUnknownAsAborted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg_xact")
	c, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	// Pre-seed: 1=Committed, 3=Aborted explicitly. 2 and 4 stay Unknown.
	if err := c.SetCommitted(storage.TransactionID(1)); err != nil {
		t.Fatalf("SetCommitted(1): %v", err)
	}
	if err := c.SetAborted(storage.TransactionID(3)); err != nil {
		t.Fatalf("SetAborted(3): %v", err)
	}

	const highXID = storage.TransactionID(7)
	if err := c.MarkUnknownAsAborted(highXID); err != nil {
		t.Fatalf("MarkUnknownAsAborted(%d): %v", highXID, err)
	}

	cases := []struct {
		xid  uint32
		want TxnStatus
	}{
		{1, TxnStatusCommitted}, // pre-existing Committed preserved
		{2, TxnStatusAborted},   // was Unknown → stamped Aborted
		{3, TxnStatusAborted},   // pre-existing Aborted preserved
		{4, TxnStatusAborted},   // was Unknown → stamped Aborted
		{5, TxnStatusAborted},   // grown + stamped
		{6, TxnStatusAborted},   // grown + stamped
	}
	for _, tc := range cases {
		if got := c.GetStatus(storage.TransactionID(tc.xid)); got != tc.want {
			t.Errorf("GetStatus(%d) = %d, want %d", tc.xid, got, tc.want)
		}
	}
	// XID 7 (== highXID) and beyond stay Unknown — sweep is half-open.
	if got := c.GetStatus(highXID); got != TxnStatusUnknown {
		t.Errorf("GetStatus(%d) = %d, want Unknown", highXID, got)
	}

	// Persistence: re-open the clog and re-verify.
	c2, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("re-OpenCLog: %v", err)
	}
	for _, tc := range cases {
		if got := c2.GetStatus(storage.TransactionID(tc.xid)); got != tc.want {
			t.Errorf("after reopen GetStatus(%d) = %d, want %d", tc.xid, got, tc.want)
		}
	}
}

// TestCLogMarkUnknownAsAbortedZeroBound is a no-op when highXID==0.
func TestCLogMarkUnknownAsAbortedZeroBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg_xact")
	c, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.MarkUnknownAsAborted(0); err != nil {
		t.Fatalf("MarkUnknownAsAborted(0): %v", err)
	}
	// No file write should have grown anything.
	if got := c.GetStatus(storage.TransactionID(1)); got != TxnStatusUnknown {
		t.Errorf("GetStatus(1) = %d, want Unknown", got)
	}
}

// TestCLogIdempotent verifies that writing the same status twice doesn't
// corrupt the file (and doesn't return an error).
func TestCLogIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg_xact")
	c, err := OpenCLog(path)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
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
