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
