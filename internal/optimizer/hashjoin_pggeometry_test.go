package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func TestPGHashGeometryPG18Vectors(t *testing.T) {
	const mib = int64(1 << 20)
	for _, tc := range []struct {
		name                   string
		rows                   float64
		width                  int
		memory                 int64
		wantBuckets, wantBatch int64
	}{
		// Q96 household_demographics build: 16 + 16 + MAXALIGN(48).
		{"q96 floor", 720, 48, 128 * mib, 1024, 1},
		// At 64 KiB, width 48's packed tuple fits 650 rows in one 1024-bucket
		// table, while MAXALIGN(49) grows it from 48 to 56 bytes and needs a
		// second batch. An implementation which failed to MAXALIGN would keep
		// both cases at 1024 x 1.
		{"maxalign 48", 650, 48, 64 << 10, 1024, 1},
		{"maxalign 49", 650, 49, 64 << 10, 1024, 2},
		// For packed 136-byte tuples at this tight memory limit, five skew MCV
		// reservations consume 1100 bytes. At 421 rows that moves PG from the
		// otherwise-fitting 1024 x 1 layout to 512 x 2.
		{"skew reservation", 421, 100, 64 << 10, 512, 2},
		// 64KiB geometry: packed 136-byte tuples, five skew MCV reservations,
		// and a multi-batch table whose initial four batches do not walk back.
		{"multi batch", 1000, 100, 64 << 10, 512, 4},
		// Same geometry with a large build: PG18's walk-back changes the initial
		// 512/256 solution into 4096 buckets and 32 batches.
		{"walk back", 100000, 100, 64 << 10, 4096, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pgHashGeometry(tc.rows, tc.width, tc.memory)
			if !ok {
				t.Fatal("geometry declined")
			}
			if got.numBuckets != tc.wantBuckets || got.numBatches != tc.wantBatch {
				t.Fatalf("geometry = %+v, want buckets=%d batches=%d", got, tc.wantBuckets, tc.wantBatch)
			}
			if got.virtualBuckets != tc.wantBuckets*tc.wantBatch || got.virtualBuckets <= 0 {
				t.Fatalf("virtual buckets = %d, want positive %d", got.virtualBuckets, tc.wantBuckets*tc.wantBatch)
			}
		})
	}
	if _, ok := pgHashGeometry(10, 0, 128*mib); ok {
		t.Fatal("zero emitted width must decline PG geometry")
	}
}

func TestPGHashGeometryDeclinesUnsafeArithmetic(t *testing.T) {
	const kib = int64(1 << 10)
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name   string
		rows   float64
		width  int
		memory int64
	}{
		{"nan rows", math.NaN(), 48, 64 * kib},
		{"positive infinite rows", math.Inf(1), 48, 64 * kib},
		{"negative infinite rows", math.Inf(-1), 48, 64 * kib},
		{"max finite rows", math.MaxFloat64, 48, 64 * kib},
		{"max int width", 1, maxInt, 64 * kib},
		{"zero memory", 1, 48, 0},
		{"negative memory", 1, 48, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := pgHashGeometry(tc.rows, tc.width, tc.memory); ok {
				t.Fatalf("unsafe input produced geometry %+v; want safe decline", got)
			}
		})
	}

	// A maximal permitted memory budget must either be represented safely or
	// declined. The current PG-derived arithmetic represents it as a normal
	// one-batch table; keep the multiplication assertion explicit so a future
	// overflow cannot silently become a usable virtual-bucket denominator.
	got, ok := pgHashGeometry(1, 48, math.MaxInt64)
	if !ok || got.virtualBuckets <= 0 || got.numBuckets > math.MaxInt64/got.numBatches {
		t.Fatalf("max-memory geometry = %+v, ok=%v; want positive nonoverflowing result", got, ok)
	}
}

func TestPGHashSpillPagesUsesHeapTuplePageSizeNotHashTupleSize(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows       float64
		width      int
		wantPages  float64
	}{
		// HeapTupleHeader is 24 bytes. Width 48 therefore occupies 72 bytes,
		// so 113 rows fit in one 8KiB page but the 114th does not.
		{"width 48 one page", 113, 48, 1},
		{"width 48 boundary", 114, 48, 2},
		// MAXALIGN(49) is 56, producing 80-byte heap tuples.
		{"width 49 boundary", 103, 49, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pgHashSpillPages(tc.rows, tc.width)
			if !ok || got != tc.wantPages {
				t.Fatalf("pgHashSpillPages(%v,%d) = %v, ok=%v; want %v,true", tc.rows, tc.width, got, ok, tc.wantPages)
			}
		})
	}
	for _, tc := range []struct {
		rows  float64
		width int
	}{
		{math.NaN(), 48}, {math.Inf(1), 48}, {-1, 48}, {1, 0},
	} {
		if got, ok := pgHashSpillPages(tc.rows, tc.width); ok {
			t.Fatalf("unsafe page-size input (%v,%d) = %v, want decline", tc.rows, tc.width, got)
		}
	}
}

func TestPathWidthUsesEmittedIndexOnlySchema(t *testing.T) {
	covered := []catalog.Column{
		{Name: "narrow_i", Type: catalog.Type{Name: "int4"}},
		{Name: "narrow_b", Type: catalog.Type{Name: "int8"}},
	}
	wantWidth := TupleWidth([]SchemaColumn{
		{Name: "narrow_i", Type: catalog.Type{Name: "int4"}},
		{Name: "narrow_b", Type: catalog.Type{Name: "int8"}},
	})
	rel := &RelOptInfo{Width: 700, NCols: 29, AvgVarBytes: 500}
	indexOnly := &Path{Rel: rel, NCols: len(covered), AvgVarBytes: 1, OutputWidth: indexOnlyOutputWidth(covered)}
	if got := pathWidth(indexOnly); got != wantWidth {
		t.Fatalf("index-only path width = %d, want TupleWidth(covered) = %d", got, wantWidth)
	}
	if got := pathWidth(&Path{Rel: rel}); got != rel.Width {
		t.Fatalf("ordinary path width = %d, want rel width %d", got, rel.Width)
	}
	narrow, ok := pgHashGeometry(100000, pathWidth(indexOnly), 64<<10)
	if !ok {
		t.Fatal("narrow geometry declined")
	}
	full, ok := pgHashGeometry(100000, pathWidth(&Path{Rel: rel}), 64<<10)
	if !ok {
		t.Fatal("full geometry declined")
	}
	if narrow == full {
		t.Fatalf("emitted width did not reach geometry: narrow=%+v full=%+v", narrow, full)
	}
}
