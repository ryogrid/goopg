package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBpcharStoredPaddedAndRenderBoundariesStayInert is M0143-0007b slice 1's
// own contract plus its sibling audit, in one place because the two only mean
// something together.
//
// The contract: a width-carrying bpchar is STORED blank-padded to its declared
// width, as upstream's bpchar_input does
// (postgres/src/backend/utils/adt/varchar.c). goopg stored it trimmed until
// 2026-09-21, and that was the whole of the K41 `relpages` gap M0143-0007
// measured on `customer`/`item`.
//
// The sibling audit: the render boundaries that used to PUT the padding back
// must now be inert, and must still be there. `catalog.PadBpchar` pads only a
// short value, so every caller becomes a no-op on a padded datum — which is
// exactly what lets pre-existing TRIMMED heap images (written before the flip)
// keep rendering correctly alongside newly written padded ones. Deleting those
// calls as "now redundant" would break every old row, so this test pins them
// as inert rather than absent.
func TestBpcharStoredPaddedAndRenderBoundariesStayInert(t *testing.T) {
	typ := catalog.Type{Name: "char", Args: []int64{10}}

	stored, err := coerceTextLikeDatum(typ, NewStringDatum("ab"))
	if err != nil {
		t.Fatalf("coerceTextLikeDatum: %v", err)
	}
	if stored != "ab        " || len(stored) != 10 {
		t.Fatalf("stored %q (%d bytes), want a 10-byte blank-padded image", stored, len(stored))
	}

	// Render boundary 1: PadBpchar itself, the helper all four callers share.
	if got := catalog.PadBpchar(typ, stored); got != stored {
		t.Errorf("PadBpchar on a padded datum = %q, want it unchanged — the render "+
			"callers must be no-ops now, not double-padders", got)
	}

	// Render boundary 2: COPY … TO (FORMAT binary). PG writes a full-width
	// field for a char(10); so must goopg, from either image.
	wire, err := datumToCopyBinary(typ, NewStringDatum(stored))
	if err != nil {
		t.Fatalf("datumToCopyBinary: %v", err)
	}
	if len(wire) != 10 {
		t.Errorf("COPY binary field is %d bytes, want 10", len(wire))
	}

	// The pre-flip image must still render at full width. This is the case
	// that makes removing the PadBpchar calls a data-losing change rather than
	// a cleanup: rows written before 2026-09-21 are on disk trimmed.
	legacy, err := datumToCopyBinary(typ, NewStringDatum("ab"))
	if err != nil {
		t.Fatalf("datumToCopyBinary(legacy trimmed image): %v", err)
	}
	if len(legacy) != 10 {
		t.Errorf("COPY binary field for a pre-flip TRIMMED image is %d bytes, want 10 — "+
			"old rows must keep rendering at the declared width", len(legacy))
	}

	// An UNBOUNDED bpchar has no width to pad to and is stored verbatim:
	// trailing blanks in it are data. Measured on PG 18.3, `bpchar` holding
	// 'ab  ' is octet_length 4 where char(6) holding the same is 6.
	unbounded := catalog.Type{Name: "bpchar"}
	got, err := coerceTextLikeDatum(unbounded, NewStringDatum("ab  "))
	if err != nil {
		t.Fatalf("coerceTextLikeDatum(bpchar, \"ab  \"): %v", err)
	}
	if got != "ab  " {
		t.Errorf("unbounded bpchar stored %q, want %q verbatim", got, "ab  ")
	}
}
