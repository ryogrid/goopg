package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// setFixtureStats publishes the statistics ANALYZE would record for a fixture
// table: its row count and the distinct counts of the named columns (others
// stay unmeasured). Executor fixtures load rows through paths the fixture's
// ANALYZE does not count (writeHeapRow, same-transaction INSERT), and a join
// key without statistics gets PG's default 0.1 hash bucket
// (estimate_hash_bucket_stats), which prices out the hash joins these tests
// exercise — M0146-0005c. Pages keep whatever the table already reports.
func setFixtureStats(t *testing.T, ctx *Context, name string, rows int64, ndistinct map[string]int64) {
	t.Helper()
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
	if !ok {
		t.Fatalf("setFixtureStats: table %s not found", name)
	}
	pages := 1
	if tbl.Stats != nil && tbl.Stats.Pages > 0 {
		pages = tbl.Stats.Pages
	}
	cols := make([]catalog.ColumnStats, len(tbl.Columns))
	for i, c := range tbl.Columns {
		if nd, ok := ndistinct[c.Name]; ok {
			cols[i] = catalog.ColumnStats{NDistinct: nd}
		}
	}
	ctx.Catalog.SetTableStats(tbl, &catalog.TableStats{RowCount: rows, Pages: pages, Analyzed: true, Columns: cols})
}
