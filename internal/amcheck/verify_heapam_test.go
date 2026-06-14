package amcheck

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// newPage returns a freshly initialised 8 KiB heap page.
func newPage(t *testing.T) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	return p
}

// itemIDOffset returns the byte offset of the 1-based line pointer slot's
// packed ItemId within the page.
func itemIDOffset(slot uint16) int {
	return storage.SizeOfPageHeaderData + (int(slot)-1)*4
}

// setItemID overwrites the packed ItemId at the 1-based slot. Mirrors the
// pack format in storage/heap.go: offset(15 bits) | flags<<15 | length<<17.
func setItemID(p storage.Page, slot uint16, off uint16, flags storage.ItemIDFlags, length uint16) {
	raw := uint32(off&0x7FFF) | (uint32(flags&0x3) << 15) | (uint32(length&0x7FFF) << 17)
	binary.LittleEndian.PutUint32(p[itemIDOffset(slot):itemIDOffset(slot)+4], raw)
}

// addCleanTuple adds a no-null tuple with `dataLen` data bytes and returns the
// 1-based slot.
func addCleanTuple(t *testing.T, p storage.Page, dataLen int) uint16 {
	t.Helper()
	tup := storage.NewHeapTuple(100, 0, make([]byte, dataLen))
	tup.Header.SetNatts(1)
	slot, err := storage.PageAddHeapTuple(p, tup)
	if err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}
	return slot
}

func TestVerifyHeapPage_NewPageClean(t *testing.T) {
	p := make(storage.Page, storage.BlockSize) // all zero == new
	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("new page reported %d corruptions: %+v", len(reports), reports)
	}
}

func TestVerifyHeapPage_EmptyInitPageClean(t *testing.T) {
	p := newPage(t)
	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("empty page reported %d corruptions: %+v", len(reports), reports)
	}
}

func TestVerifyHeapPage_CleanTuplesNoReports(t *testing.T) {
	p := newPage(t)
	addCleanTuple(t, p, 8)
	addCleanTuple(t, p, 16)
	addCleanTuple(t, p, 0)

	// A clean has-nulls tuple: natts=3 -> bitmap 1 byte -> t_hoff =
	// MAXALIGN(23+1) = 24, which equals the expected hoff for 3 attrs w/ nulls.
	withNulls := storage.NewHeapTupleWithNulls(101, 0, []byte{0x07}, make([]byte, 8))
	withNulls.Header.SetNatts(3)
	if _, err := storage.PageAddHeapTuple(p, withNulls); err != nil {
		t.Fatalf("PageAddHeapTuple(withNulls): %v", err)
	}

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("clean page reported %d corruptions: %+v", len(reports), reports)
	}
}

// wantReport asserts exactly one report at the given offset with the exact msg.
func wantReport(t *testing.T, reports []Report, off uint16, msg string) {
	t.Helper()
	if len(reports) != 1 {
		t.Fatalf("want exactly 1 report, got %d: %+v", len(reports), reports)
	}
	if reports[0].Offset != off || reports[0].Msg != msg {
		t.Fatalf("report mismatch:\n got  off=%d msg=%q\n want off=%d msg=%q",
			reports[0].Offset, reports[0].Msg, off, msg)
	}
}

func TestVerifyHeapPage_UnalignedOffset(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	item, _ := storage.PageGetItemID(p, slot)
	// Nudge the offset by 1 so it is no longer 8-byte aligned.
	setItemID(p, slot, item.Offset+1, storage.ItemIDNormal, item.Length)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, slot,
		"line pointer to page offset "+itoa(int(item.Offset)+1)+" is not maximally aligned")
}

func TestVerifyHeapPage_LengthBelowMinimum(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	item, _ := storage.PageGetItemID(p, slot)
	setItemID(p, slot, item.Offset, storage.ItemIDNormal, 16) // < 24

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, slot,
		"line pointer length 16 is less than the minimum tuple header size 24")
}

func TestVerifyHeapPage_EndsBeyondPage(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	// Aligned offset near the end of the page so off+len overruns BLCKSZ.
	off := uint16(storage.BlockSize - 16) // 8176, 8-byte aligned
	setItemID(p, slot, off, storage.ItemIDNormal, 32)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, slot,
		"line pointer to page offset 8176 with length 32 ends beyond maximum page offset 8192")
}

func TestVerifyHeapPage_HoffBeyondLength(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	item, _ := storage.PageGetItemID(p, slot)
	// Shrink the line pointer length below t_hoff (24) but keep it >= the
	// 24-byte minimum by setting length exactly 24 and t_hoff to 32.
	setItemID(p, slot, item.Offset, storage.ItemIDNormal, 24)
	p[int(item.Offset)+22] = 32 // t_hoff = 32 > lp_len 24

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, slot,
		"data begins at offset 32 beyond the tuple length 24")
}

func TestVerifyHeapPage_HoffMismatchNoNulls(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16) // natts=1, no nulls, hoff should be 24
	item, _ := storage.PageGetItemID(p, slot)
	p[int(item.Offset)+22] = 32 // corrupt t_hoff to 32 (still <= lp_len)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, slot,
		"tuple data should begin at byte 24, but actually begins at byte 32 (1 attribute, no nulls)")
}

// infomaskOffset returns the byte offset of a tuple's t_infomask field, given
// the tuple's 1-based slot (header layout: t_infomask at off+20..22).
func infomaskOffset(t *testing.T, p storage.Page, slot uint16) int {
	t.Helper()
	item, err := storage.PageGetItemID(p, slot)
	if err != nil {
		t.Fatalf("PageGetItemID(%d): %v", slot, err)
	}
	return int(item.Offset) + 20
}

func setInfomask(t *testing.T, p storage.Page, slot uint16, mask uint16) {
	t.Helper()
	binary.LittleEndian.PutUint16(p[infomaskOffset(t, p, slot):infomaskOffset(t, p, slot)+2], mask)
}

func setXmax(t *testing.T, p storage.Page, slot uint16, xmax uint32) {
	t.Helper()
	item, err := storage.PageGetItemID(p, slot)
	if err != nil {
		t.Fatalf("PageGetItemID(%d): %v", slot, err)
	}
	binary.LittleEndian.PutUint32(p[int(item.Offset)+4:int(item.Offset)+8], xmax)
}

func TestVerifyHeapPage_MultixactMarkedCommitted(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	// HEAP_XMAX_COMMITTED | HEAP_XMAX_IS_MULTI — an impossible combination.
	setInfomask(t, p, slot, storage.HeapXmaxCommitted|heapXmaxIsMulti)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, slot, "multixact should not be marked committed")
}

func TestVerifyHeapPage_HotUpdatedXmaxZero(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	// HOT-updated (xmin committed so xmin is valid; xmax-invalid clear) but the
	// raw xmax field is 0 — corruption.
	setInfomask(t, p, slot, storage.HeapHotUpdated|storage.HeapXminCommitted)
	setXmax(t, p, slot, 0)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, slot, "tuple has been HOT updated, but xmax is 0")
}

// A healthy HOT-updated tuple (HOT bit set, valid xmax) must NOT be flagged —
// guards the keystone requirement that a clean relation reports no corruption.
func TestVerifyHeapPage_HealthyHotUpdatedNoReport(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	setInfomask(t, p, slot, storage.HeapHotUpdated|storage.HeapXminCommitted)
	setXmax(t, p, slot, 4242) // valid successor xmax

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("healthy HOT-updated tuple reported %d corruptions: %+v", len(reports), reports)
	}
}

// HEAP_HOT_UPDATED with xmax 0 but the xmax-invalid hint set is NOT corruption:
// IsHotUpdated is false when HEAP_XMAX_INVALID is set, so the check is skipped.
func TestVerifyHeapPage_HotBitWithXmaxInvalidNoReport(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	setInfomask(t, p, slot, storage.HeapHotUpdated|storage.HeapXmaxInvalid|storage.HeapXminCommitted)
	setXmax(t, p, slot, 0)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("xmax-invalid HOT tuple reported %d corruptions: %+v", len(reports), reports)
	}
}

func TestVerifyHeapPage_RedirectOutOfRange(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	// Turn it into a redirect to offset 99, which exceeds maxoff (1).
	setItemID(p, slot, 99, storage.ItemIDRedirect, 0)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, slot,
		"line pointer redirection to item at offset 99 exceeds maximum offset 1")
}

func TestVerifyHeapPage_RedirectToUnused(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16)
	s2 := addCleanTuple(t, p, 16)
	// Make slot 1 redirect to slot 2, then mark slot 2 unused.
	setItemID(p, s1, s2, storage.ItemIDRedirect, 0)
	setItemID(p, s2, 0, storage.ItemIDUnused, 0)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, s1,
		"redirected line pointer points to an unused item at offset 2")
}

func TestVerifyHeapPage_RedirectToRedirect(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16)
	s2 := addCleanTuple(t, p, 16)
	setItemID(p, s1, s2, storage.ItemIDRedirect, 0)
	setItemID(p, s2, 1, storage.ItemIDRedirect, 0)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	// s1 -> s2 (redirect to redirect); s2 -> s1 also reports (redirect to s1,
	// which is itself a redirect). Both are corruptions; assert s1's.
	found := false
	for _, r := range reports {
		if r.Offset == s1 && r.Msg == "redirected line pointer points to another redirected line pointer at offset 2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing expected s1 redirect-to-redirect report: %+v", reports)
	}
}

// setCTID overwrites a tuple's t_ctid (block at off+12..16 as a uint32, offset
// at off+16..18), making it point at a same-page successor for HOT-chain tests.
func setCTID(t *testing.T, p storage.Page, slot uint16, block uint32, offset uint16) {
	t.Helper()
	item, err := storage.PageGetItemID(p, slot)
	if err != nil {
		t.Fatalf("PageGetItemID(%d): %v", slot, err)
	}
	off := int(item.Offset)
	binary.LittleEndian.PutUint32(p[off+12:off+16], block)
	binary.LittleEndian.PutUint16(p[off+16:off+18], offset)
}

// setXmin overwrites a tuple's raw t_xmin (off+0..4).
func setXmin(t *testing.T, p storage.Page, slot uint16, xmin uint32) {
	t.Helper()
	item, err := storage.PageGetItemID(p, slot)
	if err != nil {
		t.Fatalf("PageGetItemID(%d): %v", slot, err)
	}
	binary.LittleEndian.PutUint32(p[int(item.Offset):int(item.Offset)+4], xmin)
}

// makeRedirect converts the 1-based slot into an LP_REDIRECT pointing at target.
func makeRedirect(p storage.Page, slot, target uint16) {
	setItemID(p, slot, target, storage.ItemIDRedirect, 0)
}

// A healthy same-page HOT chain (HOT-updated root -> heap-only successor, linked
// by xmax==xmin) must NOT be flagged — false-positive guard for the chain pass.
func TestVerifyHeapPage_HotChainHealthyNoReport(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16)
	s2 := addCleanTuple(t, p, 16)
	setInfomask(t, p, s1, storage.HeapHotUpdated|storage.HeapXminCommitted)
	setXmax(t, p, s1, 555)
	setCTID(t, p, s1, 0, s2)
	setInfomask(t, p, s2, storage.HeapOnlyTuple|storage.HeapXminCommitted)
	setXmin(t, p, s2, 555)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("healthy HOT chain reported %d corruptions: %+v", len(reports), reports)
	}
}

func TestVerifyHeapPage_NonHeapOnlyUpdateProducedHeapOnly(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16) // not HOT-updated (infomask 0)
	s2 := addCleanTuple(t, p, 16)
	setXmax(t, p, s1, 777)
	setCTID(t, p, s1, 0, s2)
	setInfomask(t, p, s2, storage.HeapOnlyTuple) // successor IS heap-only
	setXmin(t, p, s2, 777)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, s1,
		"non-heap-only update produced a heap-only tuple at offset 2")
}

func TestVerifyHeapPage_HeapOnlyUpdateProducedNonHeapOnly(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16)
	s2 := addCleanTuple(t, p, 16) // NOT heap-only
	setInfomask(t, p, s1, storage.HeapHotUpdated|storage.HeapXminCommitted)
	setXmax(t, p, s1, 888)
	setCTID(t, p, s1, 0, s2)
	setXmin(t, p, s2, 888)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, s1,
		"heap-only update produced a non-heap only tuple at offset 2")
}

func TestVerifyHeapPage_HotChainIntersectionNormal(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16)
	s2 := addCleanTuple(t, p, 16)
	s3 := addCleanTuple(t, p, 16)
	// Both s1 and s2 link to s3 (xmax==s3.xmin); both HOT-updated, s3 heap-only.
	for _, s := range []uint16{s1, s2} {
		setInfomask(t, p, s, storage.HeapHotUpdated|storage.HeapXminCommitted)
		setXmax(t, p, s, 999)
		setCTID(t, p, s, 0, s3)
	}
	setInfomask(t, p, s3, storage.HeapOnlyTuple|storage.HeapXminCommitted)
	setXmin(t, p, s3, 999)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, s2,
		"tuple points to new version at offset 3, but offset 1 also points there")
}

func TestVerifyHeapPage_RedirectToNonHeapOnly(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16)
	s2 := addCleanTuple(t, p, 16) // valid normal target, NOT heap-only
	makeRedirect(p, s1, s2)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, s1,
		"redirected line pointer points to a non-heap-only tuple at offset 2")
}

func TestVerifyHeapPage_RedirectChainIntersection(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16)
	s2 := addCleanTuple(t, p, 16)
	s3 := addCleanTuple(t, p, 16)
	// s1 (normal, HOT-updated) links to s3; s2 (redirect) also points at s3.
	setInfomask(t, p, s1, storage.HeapHotUpdated|storage.HeapXminCommitted)
	setXmax(t, p, s1, 1234)
	setCTID(t, p, s1, 0, s3)
	makeRedirect(p, s2, s3)
	setInfomask(t, p, s3, storage.HeapOnlyTuple|storage.HeapXminCommitted)
	setXmin(t, p, s3, 1234)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	// s1 establishes predecessor[3]=1 first; the s2 redirect then intersects.
	wantReport(t, reports, s2,
		"redirect line pointer points to offset 3, but offset 1 also points there")
}

// A healthy redirect -> heap-only successor must NOT be flagged.
func TestVerifyHeapPage_RedirectToHeapOnlyNoReport(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16)
	s2 := addCleanTuple(t, p, 16)
	makeRedirect(p, s1, s2)
	setInfomask(t, p, s2, storage.HeapOnlyTuple|storage.HeapXminCommitted)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("healthy redirect->heap-only reported %d corruptions: %+v", len(reports), reports)
	}
}

// A tuple whose CTID points to a different block is not a same-page successor,
// so it must not start an in-page chain (no false intersection/flag).
func TestVerifyHeapPage_CtidOnOtherBlockNoChain(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16)
	s2 := addCleanTuple(t, p, 16)
	setInfomask(t, p, s1, storage.HeapHotUpdated|storage.HeapXminCommitted)
	setXmax(t, p, s1, 4321)
	setCTID(t, p, s1, 7, s2) // successor on block 7, not this page (block 0)
	setXmin(t, p, s2, 4321)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("cross-block CTID reported %d corruptions: %+v", len(reports), reports)
	}
}

// setNatts overwrites a tuple's stored attribute count (the low bits of
// t_infomask2 at off+18..20), preserving the high flag bits.
func setNatts(t *testing.T, p storage.Page, slot uint16, natts uint16) {
	t.Helper()
	item, err := storage.PageGetItemID(p, slot)
	if err != nil {
		t.Fatalf("PageGetItemID(%d): %v", slot, err)
	}
	off := int(item.Offset)
	cur := binary.LittleEndian.Uint16(p[off+18 : off+20])
	cur = (cur &^ storage.HeapNattsMask) | (natts & storage.HeapNattsMask)
	binary.LittleEndian.PutUint16(p[off+18:off+20], cur)
}

// A tuple storing more attributes than the table has is corruption — but only
// when a relation descriptor is supplied (verify_heapam.c:check_tuple).
func TestVerifyHeapPage_NattsExceedsTable(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16) // no-null tuple: hoff=24 regardless of natts
	setNatts(t, p, slot, 5)

	reports, err := VerifyHeapPageWithRel(p, 0, RelDesc{Natts: 3})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot,
		"number of attributes 5 exceeds maximum expected for table 3")
}

// A tuple with fewer-or-equal attributes than the table is legitimate (trailing
// columns may have been added after the tuple was written) — no report.
func TestVerifyHeapPage_NattsWithinTableNoReport(t *testing.T) {
	p := newPage(t)
	s1 := addCleanTuple(t, p, 16) // natts=1 < 3
	s2 := addCleanTuple(t, p, 16)
	setNatts(t, p, s2, 3) // natts==3 == table natts (boundary, still ok)

	reports, err := VerifyHeapPageWithRel(p, 0, RelDesc{Natts: 3})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("within-table natts reported %d corruptions: %+v", len(reports), reports)
	}
	_ = s1
}

// Without a relation descriptor the natts check is disabled, even for a tuple
// whose stored count is absurdly high — VerifyHeapPage is page-bytes-only.
func TestVerifyHeapPage_NattsCheckDisabledWithoutRel(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	setNatts(t, p, slot, 5)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("page-bytes-only path reported %d corruptions: %+v", len(reports), reports)
	}
}

// The natts check is gated on the header being clean enough to continue: a tuple
// whose t_hoff overruns its length reports only the header error, not natts
// (mirrors check_tuple bailing out when check_tuple_header returns false).
func TestVerifyHeapPage_NattsSkippedWhenHeaderCorrupt(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	setNatts(t, p, slot, 5)
	item, _ := storage.PageGetItemID(p, slot)
	setItemID(p, slot, item.Offset, storage.ItemIDNormal, 24) // lp_len=24 (>= min)
	p[int(item.Offset)+22] = 32                               // t_hoff=32 > lp_len 24

	reports, err := VerifyHeapPageWithRel(p, 0, RelDesc{Natts: 3})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	wantReport(t, reports, slot,
		"data begins at offset 32 beyond the tuple length 24")
}

// itoa is a tiny strconv.Itoa stand-in kept local to avoid an import solely for
// one assertion-message helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
