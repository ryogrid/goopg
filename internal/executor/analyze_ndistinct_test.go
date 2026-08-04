package executor

import "testing"

// TestNDistinctEstimateMirrorsComputeScalarStats pins ndistinctEstimate branch
// for branch against upstream's `compute_scalar_stats` ndistinct block
// (postgres/src/backend/commands/analyze.c:2588-2648).
//
// The defect it closes (M0127-P5.6-e-iii): goopg stored the SAMPLE's distinct
// count as the table's ndistinct. With the default statistics target the
// sample caps at ~30,000 rows, so a 1.5 M-row unique key reported ndistinct
// 30,000 — 50× too small — and `estimateJoin`'s `|L|*|R|/max(nd)` divided by
// that. The estimate audit (09 §5.3) traced Q9's over-estimate through exactly
// this saturation, and it is why the join-key coordinate correction measured
// as a regression when it landed on its own.
func TestNDistinctEstimateMirrorsComputeScalarStats(t *testing.T) {
	tests := []struct {
		name           string
		sampleDistinct int
		nmultiple      int
		nonNull        int
		nullFrac       float64
		totalRows      int64
		want           float64
	}{
		{
			// The headline case. 30,000 sampled rows of a 1.5 M-row unique
			// key: nothing repeats, so upstream's `nmultiple == 0` arm
			// assumes a unique column and scales with the row count. The old
			// code answered 30000.
			name:           "unique key sampled far below the table",
			sampleDistinct: 30000, nmultiple: 0, nonNull: 30000,
			totalRows: 1500000,
			want:      1500000,
		},
		{
			// Same, discounted for NULLs: upstream stores
			// -1.0 * (1 - stanullfrac), i.e. one distinct value per
			// non-NULL row.
			name:           "unique key with nulls discounts the null fraction",
			sampleDistinct: 15000, nmultiple: 0, nonNull: 15000,
			nullFrac: 0.5, totalRows: 1000000,
			want: 500000,
		},
		{
			// Boolean / enum shape: every sampled value repeated, so the
			// sample holds the column's whole value set and must NOT be
			// scaled. This is the arm that keeps a 6-value column reading 6
			// rather than being inflated to the row count.
			name:           "every sampled value repeated is the whole value set",
			sampleDistinct: 6, nmultiple: 6, nonNull: 30000,
			totalRows: 6000000,
			want:      6,
		},
		{
			// Haas-Stokes Duj1: n*d / (n - f1 + f1*n/N) with
			// f1 = d - nmultiple. n=1000, d=600, f1=500, N=10000:
			// 1000*600 / (500 + 500*0.1) = 600000/550 = 1090.9 → 1091.
			name:           "mixed sample uses the Duj1 estimator",
			sampleDistinct: 600, nmultiple: 100, nonNull: 1000,
			totalRows: 10000,
			want:      1091,
		},
		{
			// Sample == table (n == N): Duj1 degenerates to d exactly, so a
			// fully-sampled relation reports its true distinct count with no
			// scale-up. goopg reaches this whenever the reservoir cap
			// exceeds the row count, which is every small-table test.
			name:           "a fully sampled relation is exact",
			sampleDistinct: 900, nmultiple: 10, nonNull: 1000,
			totalRows: 1000,
			want:      900,
		},
		{
			// No measured row count (the test-only analyzeRelation wrapper,
			// or an unscanned relation): fall back to the sample count, which
			// is at least a lower bound.
			name:           "no row count falls back to the sample count",
			sampleDistinct: 42, nmultiple: 3, nonNull: 100,
			totalRows: 0,
			want:      42,
		},
		{
			name:           "empty column has no distinct values",
			sampleDistinct: 0, nmultiple: 0, nonNull: 0,
			totalRows: 1000,
			want:      0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ndistinctEstimate(tc.sampleDistinct, tc.nmultiple, tc.nonNull, tc.nullFrac, tc.totalRows)
			if got != tc.want {
				t.Fatalf("ndistinctEstimate = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNDistinctEstimateNeverBelowTheSampleCount pins the lower clamp upstream
// spells `if (stadistinct < d) stadistinct = d`: the table cannot hold fewer
// distinct values than the sample already demonstrated.
func TestNDistinctEstimateNeverBelowTheSampleCount(t *testing.T) {
	// A sample that saw 500 distinct values out of 1000 rows drawn from a
	// 1001-row table. Duj1's denominator can push the raw ratio below d on
	// this shape; the clamp keeps it at d or above.
	got := ndistinctEstimate(500, 100, 1000, 0, 1001)
	if got < 500 {
		t.Fatalf("ndistinctEstimate = %v, want >= 500 (the sample's own distinct count)", got)
	}
}
