package storage

import (
	"bytes"
	"testing"
)

// TestRawItemPlacementIsMaxAligned pins M0130-S11.4 3b-3a: the three raw
// item writers that B-tree and system-catalog index pages go through place
// item bodies exactly as upstream's PageAddItemExtended does
// (postgres/src/backend/storage/page/bufpage.c) —
//
//	alignedSize = MAXALIGN(size); upper -= alignedSize;
//	ItemIdSetNormal(itemId, upper, size)
//
// i.e. the ALLOCATION is aligned but lp_len records the UNALIGNED size. Both
// halves matter and they pull in opposite directions:
//
//   - pd_upper must stay 8-byte aligned or a real PG backend deforming an item
//     in place reads alignment-sensitive datums off a misaligned base.
//   - lp_len must NOT be rounded up, because a B-tree blob key's length is
//     recoverable only as lp_len - SizeOfIndexTupleData. Rounding it is what
//     the S11.4 slice-2 ledger row warned would corrupt the blob path; keeping
//     the size exact is what makes the placement half safe to land alone.
func TestRawItemPlacementIsMaxAligned(t *testing.T) {
	// Lengths chosen to hit every residue mod 8, including an exact multiple.
	lens := []int{1, 5, 7, 8, 9, 15, 16, 23}

	check := func(t *testing.T, p Page, slot int, want []byte) {
		t.Helper()
		id, err := readItemID(p, slot-1)
		if err != nil {
			t.Fatalf("readItemID(%d): %v", slot, err)
		}
		if int(id.Length) != len(want) {
			t.Fatalf("slot %d: lp_len = %d, want the UNALIGNED %d", slot, id.Length, len(want))
		}
		if id.Offset%8 != 0 {
			t.Fatalf("slot %d: lp_off = %d is not MAXALIGNed", slot, id.Offset)
		}
		if got := p[id.Offset : int(id.Offset)+len(want)]; !bytes.Equal(got, want) {
			t.Fatalf("slot %d: body = %x, want %x", slot, got, want)
		}
		if u := MustHeader(p).Upper(); u%8 != 0 {
			t.Fatalf("slot %d: pd_upper = %d is not MAXALIGNed", slot, u)
		}
	}

	body := func(n int, fill byte) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = fill
		}
		return b
	}

	t.Run("PageAddItemRaw", func(t *testing.T) {
		p := make(Page, BlockSize)
		if err := InitPage(p); err != nil {
			t.Fatal(err)
		}
		for i, n := range lens {
			raw := body(n, byte(0xA0+i))
			slot, err := PageAddItemRaw(p, raw)
			if err != nil {
				t.Fatalf("add len=%d: %v", n, err)
			}
			check(t, p, int(slot), raw)
		}
		// Every earlier item must still read back — alignment padding may not
		// have been written over a neighbour.
		for i, n := range lens {
			raw, err := PageGetItemRaw(p, uint16(i+1))
			if err != nil {
				t.Fatalf("get slot %d: %v", i+1, err)
			}
			if !bytes.Equal(raw, body(n, byte(0xA0+i))) {
				t.Fatalf("slot %d: body = %x, want fill %#x len %d", i+1, raw, 0xA0+i, n)
			}
		}
	})

	t.Run("PageInsertItemRawAt", func(t *testing.T) {
		p := make(Page, BlockSize)
		if err := InitPage(p); err != nil {
			t.Fatal(err)
		}
		// Always insert at the front, so the line-pointer shift runs too.
		for i, n := range lens {
			raw := body(n, byte(0xB0+i))
			if _, err := PageInsertItemRawAt(p, 1, raw); err != nil {
				t.Fatalf("insert len=%d: %v", n, err)
			}
			check(t, p, 1, raw)
		}
	})

	t.Run("PageReplaceItemRaw_grow", func(t *testing.T) {
		p := make(Page, BlockSize)
		if err := InitPage(p); err != nil {
			t.Fatal(err)
		}
		if _, err := PageAddItemRaw(p, body(4, 0x11)); err != nil {
			t.Fatal(err)
		}
		if _, err := PageAddItemRaw(p, body(4, 0x22)); err != nil {
			t.Fatal(err)
		}
		// Grow slot 1 past its allocation: it must be re-placed at an aligned
		// pd_upper rather than expanded over slot 2's bytes.
		grown := body(13, 0x33)
		if err := PageReplaceItemRaw(p, 1, grown); err != nil {
			t.Fatalf("replace: %v", err)
		}
		check(t, p, 1, grown)
		got, err := PageGetItemRaw(p, 2)
		if err != nil {
			t.Fatalf("get slot 2: %v", err)
		}
		if !bytes.Equal(got, body(4, 0x22)) {
			t.Fatalf("slot 2 clobbered by the grow: %x", got)
		}
	})
}
