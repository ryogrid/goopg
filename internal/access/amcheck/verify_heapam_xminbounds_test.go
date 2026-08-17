package amcheck

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// The xmin numeric-bounds tier (verify_heapam.c:check_tuple_visibility via
// get_xid_status). addCleanTuple stamps xmin=100, so a cluster range of
// [OldestXid=50, NextXid=200) with RelFrozenXid=80 leaves the clean tuple in
// bounds; the cases below move xmin outside one bound at a time.

func TestVerifyHeapPage_XminInFuture(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmin(t, p, slot, 250)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot, "xmin 250 equals or exceeds next valid transaction ID 0:200")
}

// xmin == NextXid is "in future" too: the comparison is >= (upstream's
// FullTransactionIdPrecedesOrEquals(next_fxid, fxid)).
func TestVerifyHeapPage_XminEqualsNextXid(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmin(t, p, slot, 200)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot, "xmin 200 equals or exceeds next valid transaction ID 0:200")
}

func TestVerifyHeapPage_XminPrecedesClusterMin(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmin(t, p, slot, 30)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot, "xmin 30 precedes oldest valid transaction ID 0:50")
}

// An xmin in [OldestXid, RelFrozenXid) is a freeze-threshold violation, not a
// cluster-min one — verifies the check ordering matches get_xid_status.
func TestVerifyHeapPage_XminPrecedesRelMin(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmin(t, p, slot, 70)
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot, "xmin 70 precedes relation freeze threshold 0:80")
}

func TestVerifyHeapPage_XminInBoundsNoReport(t *testing.T) {
	p := newPage(t)
	addCleanTuple(t, p, 8) // xmin=100, within [50,200) and >= 80
	rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
	reports, err := VerifyHeapPageWithRel(p, 0, rel)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("in-bounds xmin reported %d corruptions: %+v", len(reports), reports)
	}
}

// NextXid == 0 is the unset sentinel: the whole tier is disabled, so an
// otherwise out-of-range xmin produces no report (page-bytes-only behaviour).
func TestVerifyHeapPage_XminBoundsDisabledWhenNextXidUnset(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmin(t, p, slot, 999999)
	reports, err := VerifyHeapPageWithRel(p, 0, RelDesc{Natts: 1})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("disabled tier reported %d corruptions: %+v", len(reports), reports)
	}
}

// OldestXid / RelFrozenXid == 0 disable only their own arm; the future-xid arm
// stays active. An xmin below an unset cluster-min must NOT be reported.
func TestVerifyHeapPage_XminBelowUnsetOldestNoReport(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 8)
	setXmin(t, p, slot, 30)
	reports, err := VerifyHeapPageWithRel(p, 0, RelDesc{Natts: 1, NextXid: 200})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("unset OldestXid/RelFrozenXid reported %d corruptions: %+v", len(reports), reports)
	}
}

// Special xids (bootstrap=1, frozen=2) are always in bounds, even below
// OldestXid — get_xid_status's quick check. Covers both the raw frozen xid and
// the bootstrap xid.
func TestVerifyHeapPage_XminSpecialXidsAlwaysInBounds(t *testing.T) {
	for _, xid := range []uint32{1, uint32(storage.FrozenTransactionID)} {
		p := newPage(t)
		slot := addCleanTuple(t, p, 8)
		setXmin(t, p, slot, xid)
		rel := RelDesc{Natts: 1, NextXid: 200, OldestXid: 50, RelFrozenXid: 80}
		reports, err := VerifyHeapPageWithRel(p, 0, rel)
		if err != nil {
			t.Fatalf("VerifyHeapPageWithRel(xid=%d): %v", xid, err)
		}
		if len(reports) != 0 {
			t.Fatalf("special xid %d reported %d corruptions: %+v", xid, len(reports), reports)
		}
	}
}
