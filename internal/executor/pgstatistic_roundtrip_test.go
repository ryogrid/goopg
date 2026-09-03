package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// pg_statistic encode/decode round-trip — planner-refactor-take2, the
// histogram-lost-on-restart finding.
//
// Measured on the TPC-H bench cluster: after ANALYZE, lineitem.l_shipdate has
// 100 MCV entries and 101 histogram bounds; after a server restart both read
// back EMPTY, and the planner's estimate for a date-range predicate reverts to
// exactly rows/3 — DEFAULT_INEQ_SEL. Narrow columns behaved differently
// (l_returnflag kept its MCV), so the failure is size-dependent, not a plain
// "the slot is never written".
//
// The writer (buildUserPGStatisticRow) and the decoder
// (catalog.DecodePGStatisticPhysicalRow) each look correct in isolation, which
// is exactly why this test exists: it drives the actual encode -> decode path
// at realistic sizes rather than reasoning about either side alone.
func TestPGStatisticRoundTripPreservesHistogram(t *testing.T) {
	mkStrings := func(n int, prefix string) []string {
		out := make([]string, n)
		for i := range out {
			// Ten characters, like an ISO date — the shape that failed.
			out[i] = prefix + "1998-01-0" + string(rune('0'+i%10))
		}
		return out
	}

	cases := []struct {
		name    string
		nMCV    int
		nHist   int
		corrSet bool
	}{
		{name: "tiny", nMCV: 1, nHist: 2, corrSet: true},
		{name: "l_quantity-shaped", nMCV: 0, nHist: 49, corrSet: true},
		{name: "l_shipdate-shaped", nMCV: 100, nHist: 101, corrSet: true},
		{name: "histogram only", nMCV: 0, nHist: 101, corrSet: false},
		{name: "mcv only", nMCV: 100, nHist: 0, corrSet: true},
	}

	cols := pgStatisticColumnsPG18()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := catalog.ColumnStats{
				NDistinct: 1234,
				NullFrac:  0.0,
				AvgWidth:  10,
			}
			for _, v := range mkStrings(tc.nMCV, "") {
				cs.MCV = append(cs.MCV, catalog.MCVEntry{Value: v, Frequency: 1.0 / float64(tc.nMCV+1)})
			}
			cs.Histogram = mkStrings(tc.nHist, "")
			if tc.corrSet {
				cs.Correlation = 0.75
			}

			row := buildUserPGStatisticRow(42, 1, cs)
			data, err := EncodeRowPG(cols, row)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			// EncodeRowPG produces the physical tuple body and NullBitmapPG the
			// accompanying bitmap — exactly the pair the startup reload feeds
			// to the decoder from a heap tuple (initdb/open.go:3879).
			decoded, err := catalog.DecodePGStatisticPhysicalRow(data, NullBitmapPG(row))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if got := len(decoded.HistBounds); got != tc.nHist {
				t.Errorf("histogram bounds: round-tripped %d, wrote %d", got, tc.nHist)
			}
			if got := len(decoded.MCVValues); got != tc.nMCV {
				t.Errorf("MCV values: round-tripped %d, wrote %d", got, tc.nMCV)
			}
			if tc.corrSet && decoded.Correlation == 0 {
				t.Errorf("correlation round-tripped as 0, wrote 0.75")
			}
		})
	}
}

// TestNDistinctOverrideBeatsTheSampledFraction pins take2 P1-07.
//
// `ALTER TABLE … ALTER COLUMN … SET (n_distinct = N)` wrote only
// ColumnStats.NDistinct, while ColumnStats.StaDistinct() consults
// NDistinctFrac FIRST whenever it exceeds 0.1. ANALYZE sets that fraction from
// the sample, so on any column whose sampled distinct fraction exceeds 10 % —
// which is most keys — the manual override was written into a field nothing
// subsequently read, and the planner kept using the measured value.
func TestNDistinctOverrideBeatsTheSampledFraction(t *testing.T) {
	// The state ANALYZE leaves behind on a high-cardinality column: a fraction
	// well above the 0.1 threshold that makes StaDistinct prefer it.
	cs := catalog.ColumnStats{NDistinct: 1234, NDistinctFrac: 0.9}
	if got := cs.StaDistinct(); got != -0.9 {
		t.Fatalf("precondition: StaDistinct()=%v, expected the fraction to win at 0.9", got)
	}

	// Applying an override must make it win. This mirrors what
	// analyzeRelationWith now does.
	cs.NDistinct = 500
	cs.NDistinctFrac = 0
	if got := cs.StaDistinct(); got != 500 {
		t.Errorf("after an n_distinct override StaDistinct()=%v, want 500 — the "+
			"override is still being shadowed by the sampled fraction", got)
	}
}

// TestAnalyzeMCVListMatchesUpstream pins take2 P1-08 against
// postgres/src/backend/commands/analyze.c:2980. goopg previously used a
// `mcvFreqMargin = 1.25` ratio rule under a comment claiming it was upstream's;
// PG 18.3 contains no 1.25 in analyze.c at all.
func TestAnalyzeMCVListMatchesUpstream(t *testing.T) {
	t.Run("whole table sampled keeps the list", func(t *testing.T) {
		// samplerows == totalrows short-circuits, and also guards the
		// division by zero in the variance term.
		counts := []int{5, 4, 3}
		if got := analyzeMCVList(counts, 3, 3, 0, 100, 100); got != 3 {
			t.Errorf("got %d, want 3 (entire table sampled)", got)
		}
	})

	t.Run("near-uniform column admits nothing", func(t *testing.T) {
		// 1000 distinct values in a 1e6-row table, every one seen twice in a
		// 2000-row sample. No value is significantly more common than the
		// non-MCV selectivity, so the whole list is trimmed away. This is the
		// case the 1.25 ratio rule got wrong: it would admit entries and each
		// one displaces a histogram bound.
		counts := make([]int, 100)
		for i := range counts {
			counts[i] = 2
		}
		if got := analyzeMCVList(counts, len(counts), 1000, 0, 2000, 1e6); got != 0 {
			t.Errorf("got %d, want 0 — a near-uniform column has no most-common values", got)
		}
	})

	t.Run("a genuinely skewed value is kept", func(t *testing.T) {
		// One value covering a quarter of the sample, against 1000 distinct
		// values. It is far outside the confidence interval, so it survives.
		counts := []int{500, 2, 2, 2}
		if got := analyzeMCVList(counts, len(counts), 1000, 0, 2000, 1e6); got < 1 {
			t.Errorf("got %d, want at least 1 — a value covering 25%% of the sample "+
				"is significantly more common than non-MCV membership predicts", got)
		}
	})
}
