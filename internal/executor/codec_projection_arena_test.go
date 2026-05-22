package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mctx"
)

// TestDecodeRowProjectionArenaProjectedKindArena pins
// that projected varchar columns emit mctx-backed KindString (ArenaID≠0)
// when arena != nil. (M0074-0004.)
func TestDecodeRowProjectionArenaProjectedKindArena(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}, Ordinal: 1},
		{Name: "price", Type: catalog.Type{Name: "numeric"}, Ordinal: 2},
	}
	row := Row{
		{Kind: KindInt, Int: 42},
		NewStringDatum("hello"),
		{Kind: KindInt, Int: 1234},
	}
	data, err := EncodeRow(cols, row)
	if err != nil {
		t.Fatalf("EncodeRow: %v", err)
	}

	keep := []bool{false, true, false} // only "name" is projected
	dst := make(Row, len(cols))
	sctx := mctx.Acquire(nil, mctx.KindStmt)
	defer sctx.Release()
	if err := DecodeRowProjectionIntoArena(dst, cols, data, keep, sctx); err != nil {
		t.Fatalf("DecodeRowProjectionIntoArena: %v", err)
	}
	// Skipped columns get NullDatum (marker, not SQL NULL).
	if dst[0].Kind != KindNull {
		t.Errorf("col 0 (skipped) Kind = %v, want KindNull", dst[0].Kind)
	}
	if dst[2].Kind != KindNull {
		t.Errorf("col 2 (skipped) Kind = %v, want KindNull", dst[2].Kind)
	}
	// Projected varchar must be mctx-backed KindString (ArenaID≠0, M0107-0002).
	// payload variant from M0073-0001).
	if dst[1].Kind != KindString || dst[1].ArenaID == 0 {
		t.Errorf("col 1 (projected varchar) Kind = %v ArenaID = %d, want KindString+ArenaID≠0", dst[1].Kind, dst[1].ArenaID)
	}
	if dst[1].StringValue() != "hello" {
		t.Errorf("col 1 StringValue = %q, want %q", dst[1].StringValue(), "hello")
	}
}

// TestDecodeRowProjectionArenaBackwardCompat pins that
// arena == nil produces the same output as the original
// DecodeRowProjection. (M0074-0004.)
func TestDecodeRowProjectionArenaBackwardCompat(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}, Ordinal: 1},
		{Name: "price", Type: catalog.Type{Name: "numeric"}, Ordinal: 2},
	}
	row := Row{
		{Kind: KindInt, Int: 42},
		NewStringDatum("world"),
		{Kind: KindInt, Int: 5678},
	}
	data, err := EncodeRow(cols, row)
	if err != nil {
		t.Fatalf("EncodeRow: %v", err)
	}
	keep := []bool{true, true, true}

	// Path 1: legacy DecodeRowProjection (no arena).
	dstA := make(Row, len(cols))
	if err := DecodeRowProjection(dstA, cols, data, keep); err != nil {
		t.Fatalf("legacy decode error: %v", err)
	}
	// Path 2: DecodeRowProjectionIntoArena with arena=nil — must
	// be byte-for-byte identical to legacy path.
	dstB := make(Row, len(cols))
	if err := DecodeRowProjectionIntoArena(dstB, cols, data, keep, nil); err != nil {
		t.Fatalf("arena=nil decode error: %v", err)
	}
	if dstA[0].Kind != dstB[0].Kind || dstA[0].Int != dstB[0].Int {
		t.Errorf("col 0: A=%v B=%v", dstA[0], dstB[0])
	}
	if dstA[1].Kind != dstB[1].Kind {
		t.Errorf("col 1 Kind: A=%v B=%v", dstA[1].Kind, dstB[1].Kind)
	}
	if dstA[1].StringValue() != dstB[1].StringValue() {
		t.Errorf("col 1 string: A=%q B=%q", dstA[1].StringValue(), dstB[1].StringValue())
	}
	if dstA[2].Kind != dstB[2].Kind {
		t.Errorf("col 2 Kind: A=%v B=%v", dstA[2].Kind, dstB[2].Kind)
	}
}

// TestDecodeRowProjectionArenaSkippedColumnsNullDatum pins
// that skipped columns produce NullDatum even when arena
// is bound (no payload alloc on the skip path). (M0074-0004.)
func TestDecodeRowProjectionArenaSkippedColumnsNullDatum(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "varchar", Args: []int64{20}}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "varchar", Args: []int64{20}}, Ordinal: 1},
	}
	row := Row{NewStringDatum("alpha"), NewStringDatum("beta")}
	data, err := EncodeRow(cols, row)
	if err != nil {
		t.Fatalf("EncodeRow: %v", err)
	}
	keep := []bool{false, false} // skip both
	dst := make(Row, len(cols))
	sctx := mctx.Acquire(nil, mctx.KindStmt)
	defer sctx.Release()
	if err := DecodeRowProjectionIntoArena(dst, cols, data, keep, sctx); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	for i := range dst {
		if dst[i].Kind != KindNull {
			t.Errorf("col %d (skipped) Kind = %v, want KindNull", i, dst[i].Kind)
		}
	}
}

// TestDecodeRowProjectionArenaResetThenReuse pins that
// the same arena can decode multiple rows with Reset
// between them — mirrors the per-page Reset pattern in
// operators_ddl.go's collectBTreeEntries / backfillBTree.
// (M0074-0004.)
func TestDecodeRowProjectionArenaResetThenReuse(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "varchar", Args: []int64{20}}, Ordinal: 0},
	}
	row1 := Row{NewStringDatum("first")}
	row2 := Row{NewStringDatum("second")}
	data1, err := EncodeRow(cols, row1)
	if err != nil {
		t.Fatalf("EncodeRow row1: %v", err)
	}
	data2, err := EncodeRow(cols, row2)
	if err != nil {
		t.Fatalf("EncodeRow row2: %v", err)
	}

	keep := []bool{true}
	sctx := mctx.Acquire(nil, mctx.KindStmt)
	defer sctx.Release()

	// Decode row1, materialise its string, then Reset.
	dst1 := make(Row, 1)
	if err := DecodeRowProjectionIntoArena(dst1, cols, data1, keep, sctx); err != nil {
		t.Fatalf("decode row1: %v", err)
	}
	v1 := dst1[0].StringValue()
	if v1 != "first" {
		t.Errorf("row1 string = %q, want %q", v1, "first")
	}
	// Materialise into a Go string (copy off arena) before Reset.
	v1Copy := string([]byte(v1))
	sctx.Reset()

	// Decode row2 into the same mctx.
	dst2 := make(Row, 1)
	if err := DecodeRowProjectionIntoArena(dst2, cols, data2, keep, sctx); err != nil {
		t.Fatalf("decode row2: %v", err)
	}
	if dst2[0].StringValue() != "second" {
		t.Errorf("row2 string = %q, want %q", dst2[0].StringValue(), "second")
	}
	// The earlier copy of v1 must remain readable (independent of arena).
	if v1Copy != "first" {
		t.Errorf("v1 copy after Reset = %q, want %q", v1Copy, "first")
	}
}
