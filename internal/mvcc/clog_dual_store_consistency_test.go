package mvcc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// G2: CLOG dual-store consistency. The clog persists transaction status in two
// independent encodings — the flat XID-indexed file (goopg-native) and the
// PG-compatible 2-bit-lane SLRU mirror under pg_xact/. A drift between the two
// encoders (a classic sibling-path failure mode) is silent: the in-memory and
// flat-file reads keep passing while the SLRU-derived status is wrong, which
// only surfaces when PG attaches as a standby or after a crash where the SLRU
// is the authoritative source. These tests pin flat-file ↔ SLRU agreement
// across a page boundary and across a TruncateCLOG.

// xidStatus pairs an XID with the terminal status we wrote for it, so a single
// table can drive both the writer and every cross-store assertion.
type xidStatus struct {
	xid  storage.TransactionID
	want TxnStatus
}

// writeStatuses applies each (xid, want) to c via the public Set* API.
func writeStatuses(t *testing.T, c *CLog, set []xidStatus) {
	t.Helper()
	for _, s := range set {
		var err error
		switch s.want {
		case TxnStatusCommitted:
			err = c.SetCommitted(s.xid)
		case TxnStatusAborted:
			err = c.SetAborted(s.xid)
		case TxnStatusSubCommitted:
			err = c.SetSubCommitted(s.xid)
		default:
			t.Fatalf("writeStatuses: unsupported status %d for xid %d", s.want, s.xid)
		}
		if err != nil {
			t.Fatalf("Set(%d, %d): %v", s.xid, s.want, err)
		}
	}
}

// slruSnapshot is an independent, from-scratch decode of the on-disk SLRU
// segment files — deliberately NOT routing through CLog/clogBufferPool, so a
// pool decode bug cannot mask itself in these dual-store-equivalence checks.
// M0117-0006 Part C retired the CLog.loadFromSLRU method these tests used to
// call directly (it existed only to seed the now-deleted "banks" store); this
// is its decode logic preserved verbatim as a test-local sibling-path oracle.
type slruSnapshot map[storage.TransactionID]TxnStatus

// GetStatus mirrors CLog.GetStatus's "no entry ⇒ Unknown" contract.
func (s slruSnapshot) GetStatus(xid storage.TransactionID) TxnStatus {
	if st, ok := s[xid]; ok {
		return st
	}
	return TxnStatusUnknown
}

// freshFromSLRU independently decodes every committed/aborted/sub-committed
// lane in every pg_xact/ segment file under slruDir. This is the exact
// on-disk encoding crash recovery and a PG standby would read.
func freshFromSLRU(t *testing.T, slruDir string) slruSnapshot {
	t.Helper()
	out := make(slruSnapshot)
	entries, err := os.ReadDir(slruDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("readdir %q: %v", slruDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != 4 {
			continue
		}
		var segNo uint64
		valid := true
		for _, ch := range name {
			segNo <<= 4
			switch {
			case ch >= '0' && ch <= '9':
				segNo |= uint64(ch - '0')
			case ch >= 'A' && ch <= 'F':
				segNo |= uint64(ch - 'A' + 10)
			case ch >= 'a' && ch <= 'f':
				segNo |= uint64(ch - 'a' + 10)
			default:
				valid = false
			}
		}
		if !valid {
			continue
		}
		segData, err := os.ReadFile(filepath.Join(slruDir, name))
		if err != nil {
			t.Fatalf("read segment %q: %v", name, err)
		}
		baseXID := segNo * uint64(clogXactsPerSegment)
		for i, b := range segData {
			if b == 0 {
				continue
			}
			pageInSeg := uint64(i) / uint64(storage.BlockSize)
			xidInPageBase := (uint64(i) % uint64(storage.BlockSize)) * uint64(clogXactsPerByte)
			for lane := uint64(0); lane < uint64(clogXactsPerByte); lane++ {
				rawBits := (b >> (lane * uint64(clogBitsPerXact))) & 0x3
				var status TxnStatus
				switch rawBits {
				case pgClogStatusCommitted:
					status = TxnStatusCommitted
				case pgClogStatusAborted:
					status = TxnStatusAborted
				case pgClogStatusInProgress:
					continue
				case pgClogStatusSubCommitted:
					status = TxnStatusSubCommitted
				}
				xid := baseXID + pageInSeg*uint64(clogXactsPerPage) + xidInPageBase + lane
				if xid == 0 {
					continue
				}
				out[storage.TransactionID(xid)] = status
			}
		}
	}
	return out
}

// TestCLogDualStoreConsistency writes a mix of committed and aborted XIDs that
// straddle an SLRU page boundary (clogXactsPerPage == 32768), then asserts that
// all three views agree per-XID: the live in-memory store, a CLog reopened from
// the flat file, and a CLog reconstructed from the SLRU mirror. Any encoder
// drift between the flat file and the SLRU shows up as a mismatch here.
func TestCLogDualStoreConsistency(t *testing.T) {
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

	// A sparse, specific set spanning across the first page boundary so the
	// mirror writes >1 page worth of lanes without a 1M-row loop. Mix of
	// committed/aborted, including adjacent lanes (4,5) to catch bit-shift
	// errors and the page-boundary pair (32767, 32768).
	const pageBoundary = storage.TransactionID(clogXactsPerPage) // 32768: first XID of page 1
	set := []xidStatus{
		{FirstNormalTransactionID, TxnStatusCommitted}, // 3: first normal XID, lane 3 of byte 0
		{4, TxnStatusAborted},                          // adjacent lanes catch wrong bit-shift
		{5, TxnStatusCommitted},
		{6, TxnStatusSubCommitted}, // 0x03 lane adjacent to 4/5 catches bit-shift drift
		{7, TxnStatusSubCommitted}, // two adjacent sub-committed lanes
		{100, TxnStatusAborted},
		{200, TxnStatusSubCommitted}, // sub-committed in the same first page
		{1000, TxnStatusCommitted},
		{pageBoundary - 1, TxnStatusCommitted}, // 32767: last XID of page 0
		{pageBoundary, TxnStatusAborted},       // 32768: first XID of page 1 (crosses boundary)
		{pageBoundary + 1, TxnStatusCommitted},
		{pageBoundary + 2, TxnStatusSubCommitted}, // sub-committed on page 1
		{pageBoundary + 1234, TxnStatusAborted},   // well into page 1
	}
	writeStatuses(t, c, set)

	// View 1: live in-memory store.
	for _, s := range set {
		if got := c.GetStatus(s.xid); got != s.want {
			t.Errorf("in-memory GetStatus(%d) = %d, want %d", s.xid, got, s.want)
		}
	}

	// View 2: SLRU-derived store (durable PG-compatible mirror).
	if err := c.FlushAll(); err != nil { // C2-S1: SLRU bytes reach disk at flush points
		t.Fatalf("FlushAll: %v", err)
	}
	fresh := freshFromSLRU(t, slruDir)
	for _, s := range set {
		if got := fresh.GetStatus(s.xid); got != s.want {
			t.Errorf("SLRU-derived GetStatus(%d) = %d, want %d (flat↔SLRU encoder drift)", s.xid, got, s.want)
		}
	}

	// View 3: full production recovery reopen (OpenCLog + EnablePGSLRUMirror).
	// Under M0117-0006 Part B the flat file is vestigial; recovery reconstructs
	// status from the fsynced SLRU and re-promotes the buffer pool, exactly as a
	// server restart does.
	reopened, err := OpenCLog(flatPath)
	if err != nil {
		t.Fatalf("re-OpenCLog: %v", err)
	}
	if err := reopened.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("re-EnablePGSLRUMirror: %v", err)
	}
	for _, s := range set {
		if got := reopened.GetStatus(s.xid); got != s.want {
			t.Errorf("recovery-reopened GetStatus(%d) = %d, want %d", s.xid, got, s.want)
		}
	}

	// Both page files must exist on disk: page 0 and page 1 live in segment
	// 0000 (slruPagesPerSegment pages per segment file), so a single 0000 file
	// of at least 2 pages proves both pages were materialised.
	fi, err := os.Stat(filepath.Join(slruDir, "0000"))
	if err != nil {
		t.Fatalf("stat SLRU segment 0000: %v", err)
	}
	if min := int64(2) * int64(storage.BlockSize); fi.Size() < min {
		t.Errorf("SLRU segment 0000 size = %d, want >= %d (must span 2 pages)", fi.Size(), min)
	}
}

// TestCLogSubCommittedResolvesViaParent pins the DidCommit resolution of a
// SUB_COMMITTED lane, mirroring PG transam.c TransactionIdDidCommit: a
// sub-committed subtransaction is committed iff its top-level parent is
// committed. The parent link is supplied by a parentOf callback (the role the
// M0117-0003 SubxactMap fills in production).
func TestCLogSubCommittedResolvesViaParent(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact")); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}

	// Parents: top-level XIDs with terminal status.
	const (
		committedParent  = storage.TransactionID(10)
		abortedParent    = storage.TransactionID(20)
		inProgressParent = storage.TransactionID(30) // never gets a CLOG status
	)
	if err := c.SetCommitted(committedParent); err != nil {
		t.Fatalf("SetCommitted(parent): %v", err)
	}
	if err := c.SetAborted(abortedParent); err != nil {
		t.Fatalf("SetAborted(parent): %v", err)
	}

	// Sub-committed children, each linked to a parent above.
	const (
		childOfCommitted  = storage.TransactionID(11)
		childOfAborted    = storage.TransactionID(21)
		childOfInProgress = storage.TransactionID(31)
		orphanChild       = storage.TransactionID(41) // no parent link
	)
	for _, child := range []storage.TransactionID{childOfCommitted, childOfAborted, childOfInProgress, orphanChild} {
		if err := c.SetSubCommitted(child); err != nil {
			t.Fatalf("SetSubCommitted(%d): %v", child, err)
		}
		if got := c.GetStatus(child); got != TxnStatusSubCommitted {
			t.Fatalf("GetStatus(%d) = %d, want SubCommitted (raw lane preserved)", child, got)
		}
	}

	parents := map[storage.TransactionID]storage.TransactionID{
		childOfCommitted:  committedParent,
		childOfAborted:    abortedParent,
		childOfInProgress: inProgressParent,
		// orphanChild deliberately absent → parentOf returns 0.
	}
	parentOf := func(x storage.TransactionID) storage.TransactionID { return parents[x] }

	cases := []struct {
		xid  storage.TransactionID
		want bool
		why  string
	}{
		{committedParent, true, "directly committed top-level"},
		{abortedParent, false, "directly aborted top-level"},
		{childOfCommitted, true, "sub-committed child resolves to committed parent"},
		{childOfAborted, false, "sub-committed child resolves to aborted parent"},
		{childOfInProgress, false, "sub-committed child whose parent never committed"},
		{orphanChild, false, "sub-committed child with no parent link (PG missing-entry branch)"},
		{BootstrapTransactionID, true, "bootstrap XID short-circuits committed"},
		{FrozenTransactionID, true, "frozen XID short-circuits committed"},
		{storage.TransactionID(99999), false, "never-seen XID is not committed"},
	}
	for _, tc := range cases {
		if got := c.DidCommit(tc.xid, parentOf); got != tc.want {
			t.Errorf("DidCommit(%d) = %v, want %v (%s)", tc.xid, got, tc.want, tc.why)
		}
	}

	// A nil parentOf cannot resolve a sub-committed XID → false.
	if c.DidCommit(childOfCommitted, nil) {
		t.Errorf("DidCommit(childOfCommitted, nil) = true, want false (no resolver)")
	}

	// SUB_COMMITTED round-trips through the durable SLRU mirror.
	if err := c.FlushAll(); err != nil { // C2-S1: SLRU bytes reach disk at flush points
		t.Fatalf("FlushAll: %v", err)
	}
	fresh := freshFromSLRU(t, filepath.Join(dir, "pg_xact"))
	for _, child := range []storage.TransactionID{childOfCommitted, childOfAborted, childOfInProgress, orphanChild} {
		if got := fresh.GetStatus(child); got != TxnStatusSubCommitted {
			t.Errorf("SLRU-derived GetStatus(%d) = %d, want SubCommitted (0x03 lane round-trip)", child, got)
		}
	}
}

// TestCLogTruncateKeepsStoresConsistent writes committed/aborted XIDs spanning
// two SLRU segments (clogXactsPerSegment == 1048576), truncates at a cutoff
// inside the second segment, and asserts that:
//
//	(a) every XID >= OldestClogXid() keeps its exact status in BOTH the live
//	    in-memory store AND a fresh SLRU-derived store;
//	(b) the SLRU segment file(s) entirely below the cutoff page are unlinked;
//	(c) GetStatus for a truncated-away XID returns Unknown — the documented
//	    post-truncation invariant (the slot's bank was dropped).
func TestCLogTruncateKeepsStoresConsistent(t *testing.T) {
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

	const seg1 = storage.TransactionID(clogXactsPerSegment) // 1048576: first XID of segment 0001

	// XIDs in segment 0 (will be truncated away) and segment 1 (retained).
	below := []xidStatus{
		{FirstNormalTransactionID, TxnStatusCommitted},
		{50000, TxnStatusAborted},
		{seg1 - 1, TxnStatusCommitted}, // last XID of segment 0
	}
	// Retained set lives in segment 1, straddling the cutoff so we exercise
	// both the dropped prefix and the kept tail within the same segment.
	above := []xidStatus{
		{seg1, TxnStatusAborted},         // first XID of segment 1
		{seg1 + 100, TxnStatusCommitted}, // just below cutoff page boundary
		{seg1 + storage.TransactionID(clogXactsPerPage) + 7, TxnStatusAborted},     // page 1 of seg 1 (>= cutoff)
		{seg1 + storage.TransactionID(clogXactsPerPage)*3 + 9, TxnStatusCommitted}, // deeper into seg 1
	}
	writeStatuses(t, c, below)
	writeStatuses(t, c, above)

	// C2-S1: segment files materialize at flush points, not per commit.
	if err := c.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	// Sanity: both segment files exist before truncation.
	for _, seg := range []string{"0000", "0001"} {
		if _, err := os.Stat(filepath.Join(slruDir, seg)); err != nil {
			t.Fatalf("pre-truncate: expected SLRU segment %s: %v", seg, err)
		}
	}

	// Cutoff = an XID inside segment 1's page 1 (i.e. strictly after seg1's
	// first page). This drops all of segment 0 plus segment 1's page 0, while
	// retaining page 1 and beyond of segment 1.
	cutoff := seg1 + storage.TransactionID(clogXactsPerPage) // 1048576 + 32768
	if err := c.TruncateCLOG(cutoff); err != nil {
		t.Fatalf("TruncateCLOG(%d): %v", cutoff, err)
	}

	oldest := c.OldestClogXid()
	if oldest != cutoff {
		t.Fatalf("OldestClogXid() = %d, want %d after truncate", oldest, cutoff)
	}

	// (a) Retained XIDs (>= oldest) keep their exact status in-memory and in a
	// fresh SLRU-derived store. Only assert the entries at or above the
	// horizon — the cutoff drops segment 1's page 0, so seg1 and seg1+100 are
	// truncated away and excluded here.
	var retained []xidStatus
	for _, s := range above {
		if s.xid >= oldest {
			retained = append(retained, s)
		}
	}
	if len(retained) == 0 {
		t.Fatal("test setup error: no retained XIDs >= cutoff")
	}
	for _, s := range retained {
		if got := c.GetStatus(s.xid); got != s.want {
			t.Errorf("in-memory GetStatus(%d) = %d, want %d (retained XID must survive truncate)", s.xid, got, s.want)
		}
	}
	if err := c.FlushAll(); err != nil { // C2-S1: SLRU bytes reach disk at flush points
		t.Fatalf("FlushAll: %v", err)
	}
	fresh := freshFromSLRU(t, slruDir)
	for _, s := range retained {
		if got := fresh.GetStatus(s.xid); got != s.want {
			t.Errorf("SLRU-derived GetStatus(%d) = %d, want %d (retained XID must survive truncate in SLRU)", s.xid, got, s.want)
		}
	}

	// (b) Segment 0000 (entirely below the cutoff page) is gone; 0001 (holds
	// the cutoff page) is kept.
	if _, err := os.Stat(filepath.Join(slruDir, "0000")); !os.IsNotExist(err) {
		t.Errorf("SLRU segment 0000 should be removed below cutoff, stat err = %v (want IsNotExist)", err)
	}
	if _, err := os.Stat(filepath.Join(slruDir, "0001")); err != nil {
		t.Errorf("SLRU segment 0001 (holds cutoff page) must be retained: %v", err)
	}

	// (c) Truncated-away XIDs return Unknown via GetStatus once their entire
	// bank has been dropped. This is the documented invariant — callers
	// short-circuit XIDs below OldestClogXid() as committed/frozen before
	// consulting GetStatus, so Unknown here is correct, not data loss.
	//
	// Note on granularity: TruncateCLOG drops in-memory banks only when a bank
	// (xidsPerBank == 128K XIDs) lies ENTIRELY below the cutoff page; the bank
	// straddling the cutoff is kept intact. So XIDs in the half-open window
	// [firstDroppedBankBoundary, oldest) that share the retained straddling
	// bank still read their real status from memory — which is exactly why
	// callers must gate on OldestClogXid() rather than trust GetStatus for the
	// pre-horizon range. We therefore only assert Unknown for XIDs whose whole
	// bank was dropped.
	wholeBankDropped := []storage.TransactionID{
		FirstNormalTransactionID, // bank 0
		50000,                    // bank 0
		seg1 - 1,                 // last XID of segment 0, top of the last fully-dropped bank
	}
	for _, xid := range wholeBankDropped {
		if xid >= oldest {
			t.Fatalf("test setup error: wholeBankDropped xid %d >= oldest %d", xid, oldest)
		}
		if got := c.GetStatus(xid); got != TxnStatusUnknown {
			t.Errorf("post-truncate GetStatus(%d) = %d, want Unknown (whole-bank-dropped invariant)", xid, got)
		}
	}

	// Document the straddling-bank behavior explicitly: seg1 / seg1+100 are
	// below the horizon (truncated away logically) yet still readable because
	// their bank straddles the cutoff. They must NOT be consulted via GetStatus
	// for visibility — callers short-circuit them as frozen — but the bytes are
	// (legitimately) retained, so we assert they are still below oldest.
	for _, xid := range []storage.TransactionID{seg1, seg1 + 100} {
		if xid >= oldest {
			t.Fatalf("test setup error: straddling-bank xid %d expected below oldest %d", xid, oldest)
		}
	}
}
