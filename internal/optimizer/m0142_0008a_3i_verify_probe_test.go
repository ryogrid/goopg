package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// analyzedThreeTablesCatalog mirrors threeTablesCatalog (t1(x), t2(y,z),
// t3(a,b)) but seeds RowCount stats so EstimateRows produces a real
// (non-zero) number for the sanity checks below — threeTablesCatalog's
// tables are deliberately un-ANALYZEd for its own (unrelated) tests, and
// EstimateRows correctly returns 0 for an un-ANALYZEd base table (no fallback
// GUC is set in this package's tests).
func analyzedThreeTablesCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, cols []catalog.Column, rows int64) {
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatal(err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows}
	}
	mk("t1", []catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}}, 1000)
	mk("t2", []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}},
		{Name: "z", Type: catalog.Type{Name: "int4"}},
	}, 500)
	mk("t3", []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}, 200)
	return c
}

// analyzedUniqueThreeTablesCatalog is analyzedThreeTablesCatalog with every
// column unique, as PG's ANALYZE records for generate_series-filled tables
// (M0146-0005dy). Without column stats the default 200 groups make the
// two-relation semi-join RHS worth unique-ifying (create_unique_path), and
// PG elects that too; with them PG 18.3 elects Hash Semi Join for both
// `t1.x IN (SELECT y FROM t2, t3 WHERE t2.z = t3.a)` and its EXISTS twin.
// The tests that need a planned Semi join use this one.
func analyzedUniqueThreeTablesCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, cols []catalog.Column, rows int64, pages int) {
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatal(err)
		}
		colStats := make([]catalog.ColumnStats, len(cols))
		for i := range colStats {
			colStats[i] = catalog.ColumnStats{NDistinct: rows, NDistinctFrac: 1, AvgWidth: 4}
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows, Pages: pages, Columns: colStats, Analyzed: true}
	}
	mk("t1", []catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}}, 1000, 5)
	mk("t2", []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}},
		{Name: "z", Type: catalog.Type{Name: "int4"}},
	}, 500, 3)
	mk("t3", []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}, 200, 1)
	return c
}
