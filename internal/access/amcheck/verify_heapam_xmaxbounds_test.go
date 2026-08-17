package amcheck

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// The xmax numeric-bounds tier (verify_heapam.c:check_tuple_visibility's
// plain-XID xmax sanity check, lines 1466-1496, via get_xid_status).
// addCleanTuple stamps xmin=100, xmax=0 and infomask=0 (no HEAP_XMAX_INVALID,
// no lock-only, no multi), so once setXmax installs an out-of-range xmax the
// gate is open and checkXmaxBounds fires. A cluster range of [OldestXid=50,
// NextXid=200) with RelFrozenXid=80 leaves xmin=100 in bounds, so the only
// report in each case below is the xmax one.

func TestVerifyHeapPage_XmaxInFuture(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmax(t, p, slot, 250)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot, "xmax 250 equals or exceeds next valid transaction ID 0:200")
}

// xmax == NextXid is "in future" too: the comparison is >= (upstream's
// FullTransactionIdPrecedesOrEquals(next_fxid, fxid)).
func TestVerifyHeapPage_XmaxEqualsNextXid(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmax(t, p, slot, 200)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot, "xmax 200 equals or exceeds next valid transaction ID 0:200")
}

func TestVerifyHeapPage_XmaxPrecedesClusterMin(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmax(t, p, slot, 30)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot, "xmax 30 precedes oldest valid transaction ID 0:50")
}

// An xmax in [OldestXid, RelFrozenXid) is a freeze-threshold violation, not a
// cluster-min one — verifies the check ordering matches get_xid_status.
func TestVerifyHeapPage_XmaxPrecedesRelMin(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmax(t, p, slot, 70)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot, "xmax 70 precedes relation freeze threshold 0:80")
}

func TestVerifyHeapPage_XmaxInBoundsNoReport(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmax(t, p, slot, 120) // within [50,200) and >= 80
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("in-bounds xmax reported %d corruptions: %+v", len(reports), reports)
	}
}

// xmax == 0 (InvalidTransactionId) is get_xid_status's XID_INVALID arm: the
// tuple is live and nothing is reported, even when out of range would otherwise
// fire. The default clean tuple already has xmax 0.
func TestVerifyHeapPage_XmaxZeroNoReport(t *testing.T) {
	p := newPage(t)
	addCleanTuple(t, p, 8) // xmax=0
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("xmax 0 reported %d corruptions: %+v", len(reports), reports)
	}
}

// HEAP_XMAX_INVALID set: upstream returns early (tuple live) before the xmax
// bounds check, so an out-of-range raw xmax must NOT be reported.
func TestVerifyHeapPage_XmaxInvalidBitSkips(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setInfomask(t, p, slot, storage.HeapXmaxInvalid)
	setXmax(t, p, slot, 250)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("HEAP_XMAX_INVALID tuple reported %d corruptions: %+v", len(reports), reports)
	}
}

// HEAP_XMAX_IS_LOCKED_ONLY: the xmax is a row lock, not a delete — upstream
// returns early before the bounds check, so an out-of-range lock xmax is not
// reported.
func TestVerifyHeapPage_XmaxLockedOnlySkips(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setInfomask(t, p, slot, storage.HeapXmaxLockOnly|storage.HeapXmaxExclLock)
	setXmax(t, p, slot, 250)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("lock-only tuple reported %d corruptions: %+v", len(reports), reports)
	}
}

// HEAP_XMAX_IS_MULTI: goopg cannot resolve the multixact's update xid
// page-structurally, so the plain-XID xmax bounds check is skipped (the
// multixact path is deferred). An out-of-range raw value behind the multi bit
// is therefore not reported by this tier.
func TestVerifyHeapPage_XmaxMultiSkips(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setInfomask(t, p, slot, heapXmaxIsMulti)
	setXmax(t, p, slot, 250)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("multixact xmax reported %d corruptions: %+v", len(reports), reports)
	}
}

// NextXid == 0 is the unset sentinel: the whole tier is disabled, so an
// otherwise out-of-range xmax produces no report (page-bytes-only behaviour).
func TestVerifyHeapPage_XmaxBoundsDisabledWhenNextXidUnset(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmax(t, p, slot, 999999)
	reports, err := VerifyHeapPageWithRel(p, 0, RelDesc{Natts: 1})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("disabled tier reported %d corruptions: %+v", len(reports), reports)
	}
}

// OldestXid / RelFrozenXid == 0 disable only their own arm; the future-xid arm
// stays active. An xmax below an unset cluster-min must NOT be reported.
func TestVerifyHeapPage_XmaxBelowUnsetOldestNoReport(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmax(t, p, slot, 30)
	reports, err := VerifyHeapPageWithRel(p, 0, RelDesc{Natts: 1, NextXid: 200})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("unset OldestXid/RelFrozenXid reported %d corruptions: %+v", len(reports), reports)
	}
}

// Special xids (bootstrap=1, frozen=2) are always in bounds, even below
// OldestXid — get_xid_status's quick check applies to xmax too.
func TestVerifyHeapPage_XmaxSpecialXidsAlwaysInBounds(t *testing.T) {
	for _, xid := range []uint32{1, uint32(storage.FrozenTransactionID)} {
		p := newPage(t)
		slot := addCleanTuple(t, p, 8)
		setXmax(t, p, slot, xid)
		rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
		reports, err := VerifyHeapPageWithRel(p, 0, rel)
		if err != nil {
			t.Fatalf("VerifyHeapPageWithRel(xid=%d): %v", xid, err)
		}
		if len(reports) != 0 {
			t.Fatalf("special xmax %d reported %d corruptions: %+v", xid, len(reports), reports)
		}
	}
}
