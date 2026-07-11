package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPgStatsEndToEnd drives a real SELECT through the planner and executor to
// confirm pg_stats resolves as a virtual view, the fetchStatsRows valuesOp branch
// fires, and a column's ANALYZE statistics (MCV + histogram) are projected as
// array literals with the uncollected slots NULL. M0122-0003.
func TestPgStatsEndToEnd(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "items"})
	if !ok {
		t.Fatal("items table missing from fixture")
	}
	// Attach ANALYZE-shaped stats: column 0 (id) gets an MCV list + histogram,
	// column 1 (label) is left counter-only.
	tbl.Stats = &catalog.TableStats{
		RowCount: 3,
		Columns: []catalog.ColumnStats{
			{
				NDistinct: 3,
				NullFrac:  0.1,
				MCV:       []catalog.MCVEntry{{Value: "1", Frequency: 0.5}},
				Histogram: []string{"1", "2", "3"},
			},
			{NDistinct: 2, NullFrac: 0},
		},
	}

	rows := runQueryRows(t, ctx,
		"SELECT schemaname, tablename, attname, null_frac, n_distinct, "+
			"most_common_vals, most_common_freqs, histogram_bounds, correlation "+
			"FROM pg_stats WHERE tablename = 'items' AND attname = 'id'")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1 (items.id)", len(rows))
	}
	r := rows[0]
	if got := r[0].Format(); got != "public" {
		t.Errorf("schemaname = %q, want public", got)
	}
	if got := r[2].Format(); got != "id" {
		t.Errorf("attname = %q, want id", got)
	}
	if got := r[4].Format(); got != "3" {
		t.Errorf("n_distinct = %q, want 3", got)
	}
	if got := r[5].Format(); got != "{1}" {
		t.Errorf("most_common_vals = %q, want {1}", got)
	}
	if got := r[6].Format(); got != "{0.5}" {
		t.Errorf("most_common_freqs = %q, want {0.5}", got)
	}
	if got := r[7].Format(); got != "{1,2,3}" {
		t.Errorf("histogram_bounds = %q, want {1,2,3}", got)
	}
	// correlation is a slot goopg does not collect → NULL.
	if !r[8].IsNull() {
		t.Errorf("correlation = %q, want NULL", r[8].Format())
	}
}

// TestPgStatsOmitsUnanalyzedTable confirms a table with no ANALYZE stats produces
// no pg_stats rows (matches upstream: no pg_statistic rows → no pg_stats rows).
func TestPgStatsOmitsUnanalyzedTable(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT attname FROM pg_stats WHERE tablename = 'items'")
	if len(rows) != 0 {
		t.Fatalf("unanalyzed items produced %d pg_stats rows, want 0", len(rows))
	}
}
