package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// Phase C2 — estimation inputs. Pure helpers, unit-tested in isolation; the
// live wiring is C3/C4. The headline is the cold-start fallback (design ch. 05
// §4): baseRelRows must return a block-derived estimate, not 0, when RowCount is
// unrestored after a restart.

func TestTypeWidth_FixedAndVarlena(t *testing.T) {
	cases := []struct {
		t    catalog.Type
		want int
	}{
		{catalog.Type{Name: "int4"}, 4},
		{catalog.Type{Name: "integer"}, 4},
		{catalog.Type{Name: "bigint"}, 8},
		{catalog.Type{Name: "date"}, 4},
		{catalog.Type{Name: "float8"}, 8},
		{catalog.Type{Name: "bool"}, 1},
		{catalog.Type{Name: "text"}, varlenaDefaultWidth},
		{catalog.Type{Name: "char", Args: []int64{10}}, 14},         // n + varlena header
		{catalog.Type{Name: "varchar", Args: []int64{25}}, 29},      // n + header
		{catalog.Type{Name: "numeric", Args: []int64{15, 2}}, 16},   // (15+3)/4*2 + 8
		{catalog.Type{Name: "text", IsArray: true}, varlenaDefaultWidth},
		{catalog.Type{Name: "unknownweirdtype"}, varlenaDefaultWidth},
	}
	for _, c := range cases {
		if got := typeWidth(c.t); got != c.want {
			t.Errorf("typeWidth(%q args=%v array=%v) = %d, want %d", c.t.Name, c.t.Args, c.t.IsArray, got, c.want)
		}
	}
}

func TestTupleWidth_SumWithFloor(t *testing.T) {
	cols := []SchemaColumn{
		{Type: catalog.Type{Name: "int4"}},               // 4
		{Type: catalog.Type{Name: "bigint"}},             // 8
		{Type: catalog.Type{Name: "char", Args: []int64{10}}}, // 14
	}
	if got := tupleWidth(cols); got != 26 {
		t.Fatalf("tupleWidth = %d, want 26", got)
	}
	if got := tupleWidth(nil); got != 1 {
		t.Fatalf("empty tuple width should floor at 1, got %d", got)
	}
}

func TestEstimateRelSizeRows_Density(t *testing.T) {
	// width 100 -> perTuple 128; density = 8168/128 = 63.8125; 1000 blocks.
	got := estimateRelSizeRows(1000, 100)
	want := float64(usableBytesPerBlock) / float64(100+perTupleOverhead) * 1000
	if got != want {
		t.Fatalf("estimateRelSizeRows(1000,100) = %v, want %v", got, want)
	}
	if got < 60000 || got > 65000 {
		t.Fatalf("density estimate off the expected ~63800 range: %v", got)
	}
	if estimateRelSizeRows(0, 100) != 0 {
		t.Fatalf("zero blocks must yield 0 (genuinely unknown)")
	}
	if estimateRelSizeRows(1, 100000) < 1 {
		t.Fatalf("a non-empty relation must estimate at least 1 row")
	}
}

func TestEstScanPages(t *testing.T) {
	// 100000 rows * 100 bytes / 8168 usable = ceil(1224.3) = 1225 pages.
	if got := estScanPages(100000, 100); got != 1225 {
		t.Fatalf("estScanPages(100000,100) = %d, want 1225", got)
	}
	if got := estScanPages(0, 100); got != 1 {
		t.Fatalf("estScanPages floors at 1 page, got %d", got)
	}
}

func TestNodeTupleWidth_NilSafe(t *testing.T) {
	if got := nodeTupleWidth(nil); got != 1 {
		t.Fatalf("nodeTupleWidth(nil) = %d, want 1", got)
	}
}

// TestBaseRelRows_ColdStartFallback is the C2 headline: after a restart RowCount
// is 0 (unrestored, ledger pq-P6), and baseRelRows must return the block-derived
// estimate rather than 0 — the property that makes the milestone
// persistence-independent (design ch. 05 §4).
func TestBaseRelRows_ColdStartFallback(t *testing.T) {
	cold := baseRelRows(0 /*RowCount*/, 90000 /*blocks*/, 100 /*width*/)
	if cold <= 0 {
		t.Fatalf("cold-start baseRelRows must fall back to a block estimate, got %v", cold)
	}
	// Warm ANALYZE'd RowCount wins over the block estimate.
	if got := baseRelRows(500, 90000, 100); got != 500 {
		t.Fatalf("warm RowCount must win, got %v want 500", got)
	}
	// Genuinely unknown: no rows, no blocks.
	if got := baseRelRows(0, 0, 100); got != 0 {
		t.Fatalf("no rows and no blocks must be 0, got %v", got)
	}
}
