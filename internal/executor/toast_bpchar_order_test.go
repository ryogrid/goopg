package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestToastPadsBpcharBeforeDeciding pins M0143-0007b slice 2: a width-carrying
// bpchar is blank-padded BEFORE the TOAST decision, which is upstream's order
// and not merely a detail.
//
// PostgreSQL pads at input (`bpchar_input`,
// postgres/src/backend/utils/adt/varchar.c), so by the time it decides whether
// to toast, the padding is part of the value and a wide `char(N)` compresses
// like any other oversized datum. goopg pads in `coerceTextLikeDatum`, which
// runs inside `encodeValuePGCtx` — AFTER this decision — so slice 1 left a
// short datum skipping the check and the encoder then writing the full padded
// width inline.
//
// Measured against PG 18.3, 200 rows of `char(3000)` holding 'x':
//
//	PG 18.3            heap 16384 bytes   (pg_column_size 45 — padding compresses)
//	goopg, slice 1     heap 819200 bytes  (50x PG, stored raw inline)
//	goopg, slice 2     heap 16384 bytes   (exact match)
//
// A unit test is the right witness because no corpus gate sees this: the
// SF0.25 sweep, tpch-spotcheck and TPC-H acceptance arm were all green at
// 819 KB, since the VALUES were correct throughout — only the bytes on disk
// were wrong, and that is what R23 is about.
func TestToastPadsBpcharBeforeDeciding(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	// 3000 > ToastThreshold, but only once the padding is counted: the value
	// as written is a single character.
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "c", Type: catalog.Type{Name: "char", Args: []int64{3000}}, Ordinal: 1},
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 917, Fork: storage.MainFork}
	if err := ctx.MaterializeWriterXID(); err != nil {
		t.Fatalf("MaterializeWriterXID: %v", err)
	}

	row := Row{{Kind: KindInt, Int: 1}, NewStringDatum("x")}
	out, err := ToastLargeColumnsIfNeeded(ctx, rel, cols, row)
	if err != nil {
		t.Fatalf("ToastLargeColumnsIfNeeded: %v", err)
	}
	if out[1].Kind != KindToastPointer {
		t.Fatalf("char(3000) holding 'x' was NOT toasted (kind=%v). The padding must be "+
			"applied before the threshold check, or a wide bpchar is written raw inline "+
			"and the heap grows 50x past PostgreSQL's", out[1].Kind)
	}

	// The input row must not be mutated — every other caller of this function
	// relies on copy-on-write, and padding in place would be a silent
	// aliasing bug rather than a loud one.
	if row[1].StringValue() != "x" {
		t.Errorf("input row was mutated to %q; ToastLargeColumnsIfNeeded must copy on write",
			row[1].StringValue())
	}

	// A NARROW bpchar must still be left alone: padding it cannot push it over
	// the threshold, and the K41 witnesses this whole task is about
	// (`customer`/`item`, char(10)/char(16)) are all in this class.
	narrowCols := []catalog.Column{
		{Name: "c", Type: catalog.Type{Name: "char", Args: []int64{10}}, Ordinal: 0},
	}
	narrow := Row{NewStringDatum("ab")}
	nOut, err := ToastLargeColumnsIfNeeded(ctx, rel, narrowCols, narrow)
	if err != nil {
		t.Fatalf("ToastLargeColumnsIfNeeded(narrow): %v", err)
	}
	if nOut[0].Kind == KindToastPointer {
		t.Errorf("char(10) was toasted; a padded narrow bpchar is still far below the threshold")
	}
}
