package executor

import (
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestM0068SortExternalSpills constructs a sortOp with a tiny chunk
// limit so a small input forces multiple spill rounds. The test
// asserts that (a) at least one spill file was created during Open,
// (b) the merged output is fully sorted in ascending key order, and
// (c) the row count is preserved end-to-end. This pins the M0068-0006
// external-sort contract: peak memory is bounded by chunkLimitBytes
// regardless of input size.
func TestM0068SortExternalSpills(t *testing.T) {
	const N = 4096
	rng := rand.New(rand.NewSource(0xC0FFEE))
	rows := make([]Row, 0, N)
	for i := 0; i < N; i++ {
		rows = append(rows, Row{NewIntDatum(rng.Int63())})
	}
	src := &fakeBorrowSource{rows: rows}
	s := &sortOp{
		child: src,
		keys: []optimizer.SortKey{
			{Expr: &optimizer.ColumnRef{Index: 0}, Desc: false},
		},
		chunkLimitBytes: 1024, // tiny, forces many spills
	}
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.spillFiles) == 0 {
		t.Fatalf("expected ≥ 1 spill file with chunkLimitBytes=1024 over %d rows; got 0", N)
	}
	emitted := 0
	var prev int64
	for {
		slot, err := s.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		row := slot.Row()
		if emitted > 0 && row[0].Int < prev {
			t.Fatalf("merge order violated at %d: %d < %d", emitted, row[0].Int, prev)
		}
		prev = row[0].Int
		emitted++
	}
	if emitted != N {
		t.Fatalf("emitted = %d, want %d", emitted, N)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestM0068SortNoSpillBelowChunk confirms that a small input that
// fits within the default chunk limit does NOT spill — preserving
// the in-memory fast path for typical TPC-H sorts.
func TestM0068SortNoSpillBelowChunk(t *testing.T) {
	rows := make([]Row, 0, 64)
	for i := int64(63); i >= 0; i-- {
		rows = append(rows, Row{NewIntDatum(i)})
	}
	src := &fakeBorrowSource{rows: rows}
	s := &sortOp{
		child: src,
		keys: []optimizer.SortKey{
			{Expr: &optimizer.ColumnRef{Index: 0}, Desc: false},
		},
	}
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.spillFiles) != 0 {
		t.Fatalf("unexpected spill: %d files", len(s.spillFiles))
	}
	emitted := 0
	for {
		slot, err := s.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		row := slot.Row()
		if got, want := row[0].Int, int64(emitted); got != want {
			t.Fatalf("row %d: got %d, want %d", emitted, got, want)
		}
		emitted++
	}
	if emitted != 64 {
		t.Fatalf("emitted = %d, want 64", emitted)
	}
	_ = s.Close()
}
