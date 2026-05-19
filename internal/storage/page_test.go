package storage

import (
	"testing"
)

// TestPageHeaderConstantsMatchPG verifies that the page header constants
// match PostgreSQL 18 upstream values (bufpage.h).
func TestPageHeaderConstantsMatchPG(t *testing.T) {
	if SizeOfPageHeaderData != 24 {
		t.Errorf("SizeOfPageHeaderData=%d, want 24 (PG offsetof(PageHeaderData, pd_linp))", SizeOfPageHeaderData)
	}
	if BlockSize != 8192 {
		t.Errorf("BlockSize=%d, want 8192 (PG BLCKSZ)", BlockSize)
	}
	if pgPageLayoutVersion != 4 {
		t.Errorf("pgPageLayoutVersion=%d, want 4 (PG_PAGE_LAYOUT_VERSION)", pgPageLayoutVersion)
	}
}

// TestInitPageWritesCorrectHeader verifies that InitPage writes a
// page header byte-identical to PG's PageInit.
func TestInitPageWritesCorrectHeader(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	h := MustHeader(p)

	if v := h.LSN(); v != 0 {
		t.Errorf("pd_lsn=%d, want 0", v)
	}
	if v := h.Checksum(); v != 0 {
		t.Errorf("pd_checksum=%#x, want 0", v)
	}
	if v := h.Flags(); v != 0 {
		t.Errorf("pd_flags=%#x, want 0", v)
	}
	if v := h.Lower(); v != 24 {
		t.Errorf("pd_lower=%d, want %d (SizeOfPageHeaderData)", v, SizeOfPageHeaderData)
	}
	if v := h.Upper(); v != BlockSize {
		t.Errorf("pd_upper=%d, want %d (BlockSize)", v, BlockSize)
	}
	if v := h.Special(); v != BlockSize {
		t.Errorf("pd_special=%d, want %d (BlockSize)", v, BlockSize)
	}
	if v := h.PagesizeVersion(); v != pdPagesizeVersion {
		t.Errorf("pd_pagesize_version=%#x, want %#x (BlockSize | pgPageLayoutVersion)", v, pdPagesizeVersion)
	}
	if v := h.PruneXID(); v != 0 {
		t.Errorf("pd_prune_xid=%d, want 0", v)
	}
}

// TestPageFlagsMatchPG verifies pd_flags bit definitions match PG18 bufpage.h.
func TestPageFlagsMatchPG(t *testing.T) {
	if PDHasFreeLines != 0x0001 {
		t.Errorf("PDHasFreeLines=%#x, want 0x0001 (PG PD_HAS_FREE_LINES)", PDHasFreeLines)
	}
	if PDPageFull != 0x0002 {
		t.Errorf("PDPageFull=%#x, want 0x0002 (PG PD_PAGE_FULL)", PDPageFull)
	}
	if PDAllVisible != 0x0004 {
		t.Errorf("PDAllVisible=%#x, want 0x0004 (PG PD_ALL_VISIBLE)", PDAllVisible)
	}
}

// TestPagesizeVersionEncoding verifies that pd_pagesize_version encodes
// BlockSize in the high byte and pgPageLayoutVersion in the low byte,
// matching PG's PageGetPageSize / PageGetPageLayoutVersion macros.
func TestPagesizeVersionEncoding(t *testing.T) {
	v := pdPagesizeVersion

	// PG extracts page size via pd_pagesize_version & 0xFF00.
	pageSize := int(v & 0xFF00)
	if pageSize != BlockSize {
		t.Errorf("pd_pagesize_version & 0xFF00 = %d, want %d (BlockSize)", pageSize, BlockSize)
	}

	// PG extracts layout version via pd_pagesize_version & 0x00FF.
	layoutVer := int(v & 0x00FF)
	if layoutVer != pgPageLayoutVersion {
		t.Errorf("pd_pagesize_version & 0x00FF = %d, want %d (pgPageLayoutVersion)", layoutVer, pgPageLayoutVersion)
	}
}

// TestPagesizeVersionSetGetRoundTrip verifies round-trip through the
// accessor methods.
func TestPagesizeVersionSetGetRoundTrip(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	h := MustHeader(p)
	if v := h.PagesizeVersion(); v != pdPagesizeVersion {
		t.Fatalf("initial pd_pagesize_version=%#x", v)
	}
	h.SetPagesizeVersion(0xABCD)
	if v := h.PagesizeVersion(); v != 0xABCD {
		t.Errorf("after SetPagesizeVersion(0xABCD): got %#x", v)
	}
}

// TestIsNewPage verifies that a zero-filled page reports IsNew=true
// and an InitPage'd page reports IsNew=false.
func TestIsNewPage(t *testing.T) {
	zeroed := make(Page, BlockSize)
	if !IsNew(zeroed) {
		t.Error("IsNew(zeroed page) = false, want true")
	}

	inited := make(Page, BlockSize)
	if err := InitPage(inited); err != nil {
		t.Fatal(err)
	}
	if IsNew(inited) {
		t.Error("IsNew(InitPage'd page) = true, want false")
	}
}

// TestLSNSetGet round-trips an LSN value through the accessor.
func TestLSNSetGet(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	h := MustHeader(p)
	h.SetLSN(0xDEADBEEFCAFEBABE)
	if v := h.LSN(); v != 0xDEADBEEFCAFEBABE {
		t.Errorf("LSN=%#x, want 0xDEADBEEFCAFEBABE", v)
	}
}

// TestLSNOnDiskLayoutMatchesPG18 pins the byte layout of pd_lsn to
// PG18's two-uint32 PageXLogRecPtr (high at offset 0, low at offset 4,
// each LE), so a basebackup-shipped page is readable by PG's
// PageGetLSN.  Regression for M0106-0010 batched-54: the previous u64-LE
// encoding shipped LSN 0/0x010307B0 with bytes [B0 07 03 01 00 00 00 00],
// which PG read as 0x010307B0/0 — far past flushedUpto — triggering
// XX000 "xlog flush request <high>/0 is not satisfied".
func TestLSNOnDiskLayoutMatchesPG18(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	h := MustHeader(p)

	// Value chosen to differentiate the two halves: high = 0x12345678,
	// low = 0xCAFEBABE.  PG's PageXLogRecPtrSet writes:
	//   bytes[0..3] = LE(high) = 78 56 34 12
	//   bytes[4..7] = LE(low)  = BE BA FE CA
	const v LSN = 0x12345678CAFEBABE
	h.SetLSN(v)

	want := [8]byte{0x78, 0x56, 0x34, 0x12, 0xBE, 0xBA, 0xFE, 0xCA}
	var got [8]byte
	copy(got[:], h.Page[0:8])
	if got != want {
		t.Errorf("pd_lsn bytes = % x, want % x", got, want)
	}

	if rt := h.LSN(); rt != v {
		t.Errorf("LSN()=%#x, want %#x", uint64(rt), uint64(v))
	}
}

// TestLSNLowOnlyValueLandsAtOffset4 exercises the M0106-0010
// batched-54 smoking-gun value: an LSN whose high 32 bits are zero
// must serialise as [00 00 00 00 ... LE(low)] so PG reads it back at
// the same numeric value rather than as <low>/0.
func TestLSNLowOnlyValueLandsAtOffset4(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	h := MustHeader(p)

	const v LSN = 0x010307B0 // upstream notation: 0/10307B0
	h.SetLSN(v)

	for i := 0; i < 4; i++ {
		if h.Page[i] != 0 {
			t.Errorf("offset %d = %#x, want 0 (high half is zero)", i, h.Page[i])
		}
	}
	want := [4]byte{0xB0, 0x07, 0x03, 0x01}
	var got [4]byte
	copy(got[:], h.Page[4:8])
	if got != want {
		t.Errorf("low-half bytes = % x, want % x (LE of 0x010307B0)", got, want)
	}
}


// TestPageFlagAccessors verifies SetFlags / Flags round-trip.
func TestPageFlagAccessors(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	h := MustHeader(p)
	h.SetFlags(PDHasFreeLines | PDAllVisible)
	if v := h.Flags(); v != PDHasFreeLines|PDAllVisible {
		t.Errorf("Flags()=%#x, want %#x", v, PDHasFreeLines|PDAllVisible)
	}
}

// TestFreeSpace verifies the FreeSpace calculation.
func TestFreeSpace(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	h := MustHeader(p)
	// After InitPage: free space = upper - lower = BlockSize - 24.
	want := int(BlockSize) - int(SizeOfPageHeaderData)
	if v := h.FreeSpace(); v != want {
		t.Errorf("FreeSpace=%d, want %d", v, want)
	}
}
