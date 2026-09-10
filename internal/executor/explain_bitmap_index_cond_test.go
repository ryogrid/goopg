package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// R49 Slice A — probe-test-first.
//
// The NLI-bitmap arm binds probe keys per outer row already
// (`createNestLoopBitmapJoinPlan` overwrites `bis.Key/Keys`),
// but the renderer printed NOTHING from a `BitmapIndexScan`
// (0/40 TPC-DS `Bitmap Index Scan`s carry an `Index Cond:`).
// Slice A renders the bound keys as `Index Cond:` from
// keys+columns via `formatIndexCondParts` — NOT from
// `bis.Pred`, which is in SEARCH coordinates.
//
// Hermetic like `TestExplainIndexCondQualifiesOuterProbeKey`:
// a hand-built probe node plus a two-binding name table, run
// through BOTH walkers (the edit point is the shared
// `emitNodeDetailLines`, so one passing walker must never be
// read as proof of the other). The pinned text is PG's own
// DS-Q3 shape: inner column first, outer ref qualified —
// `(ss_item_sk = item.i_item_sk)`.

func renderBitmapProbeCond(t *testing.T, n optimizer.Node, nm *explainNames, analyze bool) []string {
	t.Helper()
	var rows []Row
	reg := &subPlanReg{rel: nm}
	if analyze {
		walkPlanAnalyzeFiltered(n, 0, &rows, parser.ExplainOptions{Analyze: true},
			nil, nil, nil, nil, nil, nil, nil, nil, 0, reg)
	} else {
		walkPlanFiltered(n, 0, &rows, parser.ExplainOptions{}, nil, nil, reg)
	}
	var lines []string
	for _, r := range rows {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	return lines
}

func bitmapProbeNames() *explainNames {
	return &explainNames{
		bySource: map[int32]string{1: "item", 2: "store_sales"},
		bySrc:    map[int16]int32{1: 1, 2: 2},
		cols: map[int32]map[string]bool{
			1: {"i_item_sk": true},
			2: {"ss_item_sk": true},
		},
	}
}

func TestExplainBitmapIndexScanRendersProbeIndexCond(t *testing.T) {
	tbl := &catalog.Table{Name: "store_sales", Schema: "public"}
	idx := &catalog.Index{Name: "store_sales_pkey", Columns: []string{"ss_item_sk"}}
	key := &optimizer.ColumnRef{Name: "i_item_sk", Index: 0, SourceTableIdx: 1}
	n := &optimizer.BitmapIndexScan{Table: tbl, Index: idx, Key: key}
	nm := bitmapProbeNames()

	for _, analyze := range []bool{false, true} {
		lines := renderBitmapProbeCond(t, n, nm, analyze)
		got := findLine(lines, "Index Cond:")
		if got != "Index Cond: (ss_item_sk = item.i_item_sk)" {
			t.Errorf("analyze=%v: Index Cond = %q, want %q\nplan:\n%s",
				analyze, got, "Index Cond: (ss_item_sk = item.i_item_sk)",
				strings.Join(lines, "\n"))
		}
	}
}

// A key-less bitmap (the pre-Slice-B corpus shape) must stay silent:
// no `Index Cond: ()` line appears on unparameterized bitmaps.
func TestExplainBitmapIndexScanWithoutKeyRendersNoIndexCond(t *testing.T) {
	tbl := &catalog.Table{Name: "store_sales", Schema: "public"}
	idx := &catalog.Index{Name: "store_sales_pkey", Columns: []string{"ss_item_sk"}}
	n := &optimizer.BitmapIndexScan{Table: tbl, Index: idx}
	nm := bitmapProbeNames()

	for _, analyze := range []bool{false, true} {
		lines := renderBitmapProbeCond(t, n, nm, analyze)
		if got := findLine(lines, "Index Cond:"); got != "" {
			t.Errorf("analyze=%v: key-less bitmap rendered %q, want no Index Cond line\nplan:\n%s",
				analyze, got, strings.Join(lines, "\n"))
		}
	}
}
