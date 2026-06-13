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
	reports, err := VerifyHeapPage(p)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("new page reported %d corruptions: %+v", len(reports), reports)
	}
}

func TestVerifyHeapPage_EmptyInitPageClean(t *testing.T) {
	p := newPage(t)
	reports, err := VerifyHeapPage(p)
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

	reports, err := VerifyHeapPage(p)
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

	reports, err := VerifyHeapPage(p)
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

	reports, err := VerifyHeapPage(p)
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

	reports, err := VerifyHeapPage(p)
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

	reports, err := VerifyHeapPage(p)
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

	reports, err := VerifyHeapPage(p)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	wantReport(t, reports, slot,
		"tuple data should begin at byte 24, but actually begins at byte 32 (1 attribute, no nulls)")
}

func TestVerifyHeapPage_RedirectOutOfRange(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	// Turn it into a redirect to offset 99, which exceeds maxoff (1).
	setItemID(p, slot, 99, storage.ItemIDRedirect, 0)

	reports, err := VerifyHeapPage(p)
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

	reports, err := VerifyHeapPage(p)
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

	reports, err := VerifyHeapPage(p)
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
