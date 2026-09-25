package optimizer

// R46 (K98) pins for the legacy funnel's index-vs-seq competition.
//
// The equality arm used to hand back an IndexScan unconditionally on
// shape match; seqWinsEqualityProbe declines when the sequential
// scan prices cheaper under the same cost functions the search
// uses. Both directions are pinned: a 1-page unique probe must go
// sequential (TPC-DS Q9's `reason`), a large-table probe must keep
// the index (regression guard against a selectivity-1.0
// implementation flipping every legacy probe corpus-wide), and
// anything unmeasurable keeps today's behavior.
import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// probeFixture builds a single-int-column table with a unique btree
// index compatible with seqWinsEqualityProbe's inputs.
func probeFixture(t *testing.T, name string, rows int64, pages int, ndistinct int64) (*catalog.Table, *catalog.Index) {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{
		RowCount: rows,
		Pages:    pages,
		Columns: []catalog.ColumnStats{
			{NDistinct: ndistinct, NDistinctFrac: float64(ndistinct) / float64(rows)},
		},
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: name + "_pkey"}, tbl, []string{"id"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	return tbl, idx
}

func eqProbe(t *testing.T) Expr {
	t.Helper()
	return &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "id"},
		Right: &IntegerConst{Value: 1},
	}
}

func TestSeqWinsTinyUniqueProbe(t *testing.T) {
	// TPC-DS `reason`: 35 rows on 1 page, unique pkey. PG seq-scans;
	// the index descent plus random heap fetch loses to one
	// sequential page under cost_seqscan/cost_index.
	tbl, idx := probeFixture(t, "reasonlike", 35, 1, 35)
	if !seqWinsEqualityProbe(tbl, idx, eqProbe(t), DefaultPlannerSettings()) {
		t.Error("1-page unique probe must decline the index (seq wins)")
	}
}

func TestSeqLosesLargeUniqueProbe(t *testing.T) {
	// Same shape at fact-table scale: the index must win. This is
	// the guard against pricing the probe at selectivity 1.0, which
	// would flip every legacy probe to sequential.
	tbl, idx := probeFixture(t, "factlike", 1_440_000, 25_866, 1_440_000)
	if seqWinsEqualityProbe(tbl, idx, eqProbe(t), DefaultPlannerSettings()) {
		t.Error("large-table unique probe must keep the index")
	}
}

func TestSeqDeclinesWithoutStats(t *testing.T) {
	// Unmeasurable input keeps today's behavior (index), never a
	// fabricated seq win.
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "nostats"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "nostats_pkey"}, tbl, []string{"id"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	if seqWinsEqualityProbe(tbl, idx, eqProbe(t), DefaultPlannerSettings()) {
		t.Error("missing stats must keep the index (today's behavior)")
	}
}

// rewriteFixture builds Filter{SeqScan} carrying `id = 1` for the
// scan-input rewrite pass.
func rewriteFixture(t *testing.T, tbl *catalog.Table) *Filter {
	t.Helper()
	return &Filter{
		Child:     &SeqScan{Table: tbl, schema: Schema{{Name: "id"}}},
		Predicate: eqProbe(t),
	}
}

func TestRewritePassKeepsSeqOnTinyProbe(t *testing.T) {
	// The funnel's decline must be STICKY: the absorber pass rebuilt
	// the declined IndexScan (measured live: funnel verdict=true yet
	// EXPLAIN still showed Index Scan). Now the absorber runs the
	// same competition and leaves Filter+SeqScan.
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "tiny"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{RowCount: 35, Pages: 1, Columns: []catalog.ColumnStats{{NDistinct: 35, NDistinctFrac: 1.0}}}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "tiny_pkey"}, tbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	out := rewriteScanInputsWithSingleTablePredicates(rewriteFixture(t, tbl), cat, DefaultPlannerSettings())
	f, ok := out.(*Filter)
	if !ok {
		t.Fatalf("tiny probe rewritten to %T, want Filter+SeqScan kept", out)
	}
	if _, ok := f.Child.(*SeqScan); !ok {
		t.Errorf("tiny probe child is %T, want *SeqScan", f.Child)
	}
}

func TestRewritePassKeepsIndexOnLargeProbe(t *testing.T) {
	// Same shape at scale: the absorber must still build the index.
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "bigt"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{RowCount: 1_440_000, Pages: 25_866, Columns: []catalog.ColumnStats{{NDistinct: 1_440_000, NDistinctFrac: 1.0}}}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "bigt_pkey"}, tbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	out := rewriteScanInputsWithSingleTablePredicates(rewriteFixture(t, tbl), cat, DefaultPlannerSettings())
	f, ok := out.(*Filter)
	if !ok {
		// Fully absorbed (predicate dropped, bare IndexScan root) is
		// also a keep-index outcome.
		if _, ok := out.(*IndexScan); !ok {
			t.Fatalf("large probe rewritten to %T, want index", out)
		}
		return
	}
	if _, ok := f.Child.(*IndexScan); !ok {
		t.Errorf("large probe child is %T, want *IndexScan", f.Child)
	}
}
