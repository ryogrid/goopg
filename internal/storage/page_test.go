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
