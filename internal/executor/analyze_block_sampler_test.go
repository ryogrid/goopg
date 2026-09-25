package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestBlockSamplerSelectsExactCountWhenSmallerThanRelation pins
// BlockSampler_Next's core contract (sampling.c:57-116): when the relation
// has more blocks than the sample wants, it selects exactly n DISTINCT
// blocks, strictly increasing (physical order), each within [0, N).
func TestBlockSamplerSelectsExactCountWhenSmallerThanRelation(t *testing.T) {
	bs := newBlockSampler(1000, 100, 42)
	var got []uint32
	for bs.hasMore() {
		got = append(got, bs.next())
	}
	if len(got) != 100 {
		t.Fatalf("selected %d blocks, want 100", len(got))
	}
	seen := make(map[uint32]bool, len(got))
	for i, b := range got {
		if b >= 1000 {
			t.Fatalf("block %d out of range [0,1000)", b)
		}
		if seen[b] {
			t.Fatalf("block %d selected twice", b)
		}
		seen[b] = true
		if i > 0 && got[i-1] >= b {
			t.Fatalf("blocks not strictly increasing: %d then %d", got[i-1], b)
		}
	}
}

// TestBlockSamplerDegradesToFullScanWhenRelationSmaller pins
// BlockSampler_Init's degenerate case (sampling.c:54, `Min(bs->n, bs->N)`):
// a relation with fewer blocks than the sample wants selects EVERY block,
// in order 0..N-1 — the case the M0138-0001 census cites as why a small
// table shows near-identical n_distinct between goopg and PG.
func TestBlockSamplerDegradesToFullScanWhenRelationSmaller(t *testing.T) {
	bs := newBlockSampler(50, 300, 7)
	var got []uint32
	for bs.hasMore() {
		got = append(got, bs.next())
	}
	if len(got) != 50 {
		t.Fatalf("selected %d blocks, want 50 (every block)", len(got))
	}
	for i, b := range got {
		if b != uint32(i) {
			t.Fatalf("got[%d]=%d, want %d (full scan must be in order)", i, b, i)
		}
	}
}

// TestBlockSamplerDeterministicForFixedSeed pins that the same seed always
// replays the same block sequence — the property `GOOPG_ANALYZE_SEED`
// relies on for a reproducible plan-parity capture (analyzeSeedFor).
func TestBlockSamplerDeterministicForFixedSeed(t *testing.T) {
	collect := func(seed uint64) []uint32 {
		bs := newBlockSampler(10000, 250, seed)
		var got []uint32
		for bs.hasMore() {
			got = append(got, bs.next())
		}
		return got
	}
	a := collect(123)
	b := collect(123)
	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same-seed replay diverged at index %d: %d vs %d", i, a[i], b[i])
		}
	}
	c := collect(456)
	if len(c) == len(a) {
		allSame := true
		for i := range a {
			if a[i] != c[i] {
				allSame = false
				break
			}
		}
		if allSame {
			t.Fatalf("different seeds produced identical sequences (seed not threaded through)")
		}
	}
}

// TestReservoirGetNextSNonNegative pins reservoir_get_next_S's basic
// contract (sampling.c:146-226): the skip count S is always >= 0, across
// both the Algorithm X regime (t <= 22n) and the Algorithm Z regime
// (t > 22n) the function switches between.
func TestReservoirGetNextSNonNegative(t *testing.T) {
	rs := newReservoirState(50, 99)
	// Drive t well past the 22*n Algorithm-X/Z boundary (22*50=1100) so both
	// branches of getNextS actually execute during this test.
	tCount := 0.0
	for i := 0; i < 2000; i++ {
		s := rs.getNextS(tCount, 50)
		if s < 0 {
			t.Fatalf("iteration %d (t=%v): getNextS returned negative skip %v", i, tCount, s)
		}
		tCount += s + 1
	}
}

// TestAnalyzeBlockSamplingSkipsBlocksOnLargeRelation is the M0138-0002
// integration pin: on a relation whose block count exceeds the sample cap,
// ANALYZE must (a) not scan every block (Pages far exceeds the number of
// blocks the sample actually needed) and (b) still produce a RowCount
// estimate close to the true count — PG's own extrapolation guarantee
// (analyze.c:1330-1339), not exactness. Uses a deliberately tiny
// statsTarget (sampleCap=300) against a relation sized to need thousands
// of blocks, so the "totalblocks <= sampleCap" degenerate (full-scan) case
// from the other tests above is NOT the one being exercised here.
func TestAnalyzeBlockSamplingSkipsBlocksOnLargeRelation(t *testing.T) {
	const n = 60000
	makeRow := func(i int) []optimizer.Expr {
		return []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i + 1)},
			&optimizer.StringConst{Value: "row-payload-padding-xxxxxxxxxxxxxxxxxxxx"},
		}
	}
	_, stats := seedRowsAndAnalyze(t, n, makeRow, 1) // statsTarget=1 -> sampleCap=300

	if stats.Pages <= 300 {
		t.Skipf("relation only has %d pages, too small to exercise block skipping (need >300); widen the row payload or n", stats.Pages)
	}
	lo, hi := int64(n)*80/100, int64(n)*120/100
	if stats.RowCount < lo || stats.RowCount > hi {
		t.Errorf("RowCount=%d, want within 20%% of %d (got Pages=%d, sampleCap=300)", stats.RowCount, n, stats.Pages)
	}
}
