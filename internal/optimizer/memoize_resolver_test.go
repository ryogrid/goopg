package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestMemoizeResolverFamilyCrossesIndexProbe keeps Memoize in lockstep across
// the base-column and filtered-row walkers. A cache changes execution, never
// the identity or distribution of a tuple it returns, so a grouping key above
// it must retain the child index probe's catalog statistics.
func TestMemoizeResolverFamilyCrossesIndexProbe(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "item"}, []catalog.Column{
		{Name: "i_item_sk", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cat.SetTableStats(tbl, &catalog.TableStats{RowCount: 1000, Columns: []catalog.ColumnStats{{NDistinct: 1000}}})
	idx := &IndexScan{Table: tbl, schema: tableSchemaWithSource(tbl, 1)}
	memo := &Memoize{Child: idx}

	ref, ok := resolveBaseColumn(0, memo)
	if !ok || ref.table != tbl || ref.col != "i_item_sk" {
		t.Fatalf("resolveBaseColumn through Memoize = (%+v, %v), want item.i_item_sk", ref, ok)
	}
	if rows, ok := relFilteredRows(memo, idx); !ok || rows != 1000 {
		t.Fatalf("relFilteredRows through Memoize = (%v, %v), want (1000, true)", rows, ok)
	}
}
