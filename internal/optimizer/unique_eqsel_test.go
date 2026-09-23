package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// eqsel's isunique branch (selfuncs.c:338): `col = const` on the sole column
// of a unique index is 1/reltuples even with no column statistics. Without it
// a never-ANALYZEd primary key was priced at 1/200 of the table, so once
// CREATE INDEX published the heap size (index_update_stats) a 1M-row point
// lookup seq-scanned — the multiple-row-versions isolation spec's UPDATE went
// from an index probe to a 200 ms seq scan. PG plans an Index Scan for both.
func TestUniqueColumnEqualityUsesIsUnique(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "txt", Type: catalog.Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "t_pkey"}, tbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	// What CREATE INDEX now publishes for a loaded, never-ANALYZEd heap.
	tbl.Stats = &catalog.TableStats{RowCount: 1000000, Pages: 8850, Analyzed: true}

	for _, q := range []string{
		"SELECT * FROM t WHERE id = 1000000",
		"UPDATE t SET txt = 'b' WHERE id = 1000000",
	} {
		n, err := Plan(parseOne(t, q), c)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		found := false
		for m := n; m != nil && !found; {
			if _, ok := m.(*IndexScan); ok {
				found = true
				break
			}
			kids, _ := planChildNodes(m)
			if len(kids) == 0 {
				break
			}
			m = kids[0]
		}
		if !found {
			t.Fatalf("%s: want an IndexScan on the primary key (PG eqsel isunique), got root %T", q, n)
		}
	}

	// The restriction-selectivity twin sees the stamped UniqueKeys directly.
	scan := &SeqScan{Table: tbl, UniqueKeys: [][]string{{"id"}}}
	eq := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0, Name: "id"}, Right: &IntegerConst{Value: 5}}
	if got := clauseSelectivity(eq, scan); got != 1.0/1000000 {
		t.Fatalf("clauseSelectivity(id = 5) = %v, want 1/reltuples", got)
	}
	if got := clauseSelectivityWithSource(eq, scan); got.value != 1.0/1000000 || !got.reliable {
		t.Fatalf("WithSource twin = %+v, want 1/reltuples, reliable", got)
	}
}
