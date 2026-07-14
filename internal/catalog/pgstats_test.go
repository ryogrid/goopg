package catalog

import (
	"strings"
	"testing"
)

// TestPGStatsViewRegistered asserts the pg_stats view resolves with its
// canonical 17-column PG18 shape.
func TestPGStatsViewRegistered(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.ns(DefaultDBOid).tables["pg_catalog.pg_stats"]
	if !ok {
		t.Fatal("pg_stats not registered")
	}
	if tbl.OID != 9160 {
		t.Fatalf("pg_stats OID = %d, want 9160", tbl.OID)
	}
	want := []string{
		"schemaname", "tablename", "attname", "inherited", "null_frac",
		"avg_width", "n_distinct", "most_common_vals", "most_common_freqs",
		"histogram_bounds", "correlation", "most_common_elems",
		"most_common_elem_freqs", "elem_count_histogram",
		"range_length_histogram", "range_empty_frac", "range_bounds_histogram",
	}
	if len(tbl.Columns) != len(want) {
		t.Fatalf("pg_stats column count = %d, want %d", len(tbl.Columns), len(want))
	}
	for i, n := range want {
		if tbl.Columns[i].Name != n {
			t.Errorf("column %d = %q, want %q", i, tbl.Columns[i].Name, n)
		}
	}
}

// TestPGStatsRowsProjectsColumnStats asserts one row per analyzed column, with
// MCV/histogram rendered as array literals and uncollected slots NULL.
func TestPGStatsRowsProjectsColumnStats(t *testing.T) {
	c := NewInMemory()
	tbl := &Table{
		Schema: "public", Name: "t", OID: 40001,
		Columns: []Column{
			{Name: "a", Type: Type{Name: "int4"}, Ordinal: 0},
			{Name: "b", Type: Type{Name: "text"}, Ordinal: 1},
		},
	}
	tbl.Stats = &TableStats{
		RowCount: 100,
		Columns: []ColumnStats{
			{
				NDistinct: 3,
				NullFrac:  0.25,
				MCV:       []MCVEntry{{Value: "1", Frequency: 0.5}, {Value: "2", Frequency: 0.3}},
				Histogram: []string{"1", "5", "9"},
			},
			{NDistinct: 10, NullFrac: 0},
		},
	}
	c.ns(DefaultDBOid).tables["public.t"] = tbl

	rows := c.PGStatsRowsForDBOid(DefaultDBOid)
	if len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}

	// Row for column "a": full MCV + histogram.
	a := rows[0]
	if a[0] != "public" || a[1] != "t" || a[2] != "a" {
		t.Errorf("identity = %v, want public/t/a", a[:3])
	}
	if a[3] != "f" {
		t.Errorf("inherited = %q, want f", a[3])
	}
	if a[4] != "0.25" {
		t.Errorf("null_frac = %q, want 0.25", a[4])
	}
	if a[6] != "3" {
		t.Errorf("n_distinct = %q, want 3", a[6])
	}
	if a[7] != "{1,2}" {
		t.Errorf("most_common_vals = %q, want {1,2}", a[7])
	}
	if a[8] != "{0.5,0.3}" {
		t.Errorf("most_common_freqs = %q, want {0.5,0.3}", a[8])
	}
	if a[9] != "{1,5,9}" {
		t.Errorf("histogram_bounds = %q, want {1,5,9}", a[9])
	}
	// Uncollected slots are NULL.
	for _, i := range []int{10, 11, 12, 13, 14, 15, 16} {
		if a[i] != VirtualNull {
			t.Errorf("column index %d = %q, want NULL", i, a[i])
		}
	}

	// Row for column "b": no MCV, no histogram → those cells NULL.
	b := rows[1]
	if b[2] != "b" {
		t.Errorf("second row attname = %q, want b", b[2])
	}
	if b[7] != VirtualNull || b[8] != VirtualNull || b[9] != VirtualNull {
		t.Errorf("empty-stats column should have NULL mcv/freqs/hist, got %q/%q/%q", b[7], b[8], b[9])
	}
}

// TestPGStatsSkipsUnanalyzedAndDisabled asserts a table with no Stats produces no
// rows, and a column with SET STATISTICS 0 is omitted.
func TestPGStatsSkipsUnanalyzedAndDisabled(t *testing.T) {
	c := NewInMemory()
	// Unanalyzed table (Stats nil) → no rows.
	c.ns(DefaultDBOid).tables["public.noanalyze"] = &Table{
		Schema: "public", Name: "noanalyze", OID: 40002,
		Columns: []Column{{Name: "x", Type: Type{Name: "int4"}, Ordinal: 0}},
	}
	zero := 0
	analyzed := &Table{
		Schema: "public", Name: "hasstats", OID: 40003,
		Columns: []Column{
			{Name: "keep", Type: Type{Name: "int4"}, Ordinal: 0},
			{Name: "off", Type: Type{Name: "int4"}, Ordinal: 1, StatTarget: &zero},
		},
	}
	analyzed.Stats = &TableStats{Columns: []ColumnStats{{NDistinct: 1}, {NDistinct: 1}}}
	c.ns(DefaultDBOid).tables["public.hasstats"] = analyzed

	rows := c.PGStatsRowsForDBOid(DefaultDBOid)
	for _, r := range rows {
		if r[1] == "noanalyze" {
			t.Errorf("unanalyzed table should not appear: %v", r)
		}
		if r[1] == "hasstats" && r[2] == "off" {
			t.Errorf("SET STATISTICS 0 column should be omitted: %v", r)
		}
	}
	// Exactly the one enabled column of hasstats survives.
	got := 0
	for _, r := range rows {
		if r[1] == "hasstats" {
			got++
			if !strings.EqualFold(r[2], "keep") {
				t.Errorf("surviving column = %q, want keep", r[2])
			}
		}
	}
	if got != 1 {
		t.Errorf("hasstats row count = %d, want 1", got)
	}
}
