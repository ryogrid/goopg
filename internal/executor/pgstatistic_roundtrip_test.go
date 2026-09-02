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
