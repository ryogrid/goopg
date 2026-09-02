package executor

import (
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// Walker agreement on the row estimate — planner-refactor-take2 P0-04b.
//
// goopg renders a Filter wrapper by COLLAPSING it into the scan or join line it
// wraps, matching PG, which has no separate Filter node at all — a restriction
// clause lives on the scan and `set_baserel_size_estimates` (costsize.c) stores
// `rel->rows` already multiplied by `clauselist_selectivity(baserestrictinfo)`.
// goopg splits the two across two nodes, so the renderer has to put them back
// together: the LINE comes from the scan, the ROW ESTIMATE from the wrapper.
//
// The plain walker did that. The ANALYZE walker did not — it read the estimate
// off the collapsed-into node and so printed the UNFILTERED count. `EXPLAIN` and
// `EXPLAIN ANALYZE` therefore reported different `rows=` for the same scan, the
// ANALYZE one overstating the planner's estimate by exactly one selectivity
// factor, on every filtered scan and every HAVING. Every artefact captured with
// ANALYZE — which is most of them, since actual row counts are the point —
// carried the wrong estimate on the nodes where the estimate matters most.
//
// A test that pins ONE walker proves nothing about the other; that is already
// recorded at the walkers' own sibling-pair comments. So this one renders the
// same plan through both and compares.

var explainRowsRe = regexp.MustCompile(`rows=(\d+)`)

func renderPlain(t *testing.T, n optimizer.Node) string {
	t.Helper()
	var rows []Row
	walkPlanFiltered(n, 0, &rows, parser.ExplainOptions{}, nil, nil,
		&subPlanReg{rel: newExplainNames(n), cte: collectCTEHoist(n)})
	return joinRowText(rows)
}

func renderAnalyze(t *testing.T, n optimizer.Node) string {
	t.Helper()
	var rows []Row
	walkPlanAnalyzeFiltered(n, 0, &rows, parser.ExplainOptions{Analyze: true},
		nil, nil, nil, nil, nil, nil, 0,
		&subPlanReg{rel: newExplainNames(n), cte: collectCTEHoist(n)})
	return joinRowText(rows)
}

func joinRowText(rows []Row) string {
	var lines []string
	for _, r := range rows {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, r[0].StringValue())
		}
	}
	return strings.Join(lines, "\n")
}

// TestWalkersAgreeOnRowEstimate renders a filtered scan through both text
// walkers and requires the rows= sequence to be identical.
func TestWalkersAgreeOnRowEstimate(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	// EstRelRows gives the scan a base cardinality; without one both walkers
	// print rows=1 and the comparison is vacuous whichever node they read.
	scan := &optimizer.SeqScan{Table: tbl, EstRelRows: 10000}

	// The predicate must be SELECTIVE. A `true` constant estimates at
	// selectivity 1.0, so the filtered and unfiltered counts coincide and this
	// test passes even with the defect present — established by running the
	// negative control, which is the only reason this comment exists.
	filtered := &optimizer.Filter{
		Child: scan,
		Predicate: &optimizer.BinaryOp{
			Op:    parser.OpEq,
			Left:  &optimizer.ColumnRef{Name: "a"},
			Right: &optimizer.IntegerConst{Value: 42},
		},
	}
	if base, got := optimizer.EstimateRows(scan), optimizer.EstimateRows(filtered); base == got {
		t.Fatalf("predicate is not selective (scan=%d, filtered=%d): the comparison below "+
			"would pass with the defect present", base, got)
	}

	plain := explainRowsRe.FindAllStringSubmatch(renderPlain(t, filtered), -1)
	analyze := explainRowsRe.FindAllStringSubmatch(renderAnalyze(t, filtered), -1)

	if len(plain) == 0 {
		t.Fatalf("plain walker emitted no rows= field:\n%s", renderPlain(t, filtered))
	}
	if len(plain) != len(analyze) {
		t.Fatalf("walkers emitted a different number of rows= fields: plain=%d analyze=%d\n"+
			"plain:\n%s\nanalyze:\n%s",
			len(plain), len(analyze), renderPlain(t, filtered), renderAnalyze(t, filtered))
	}
	for i := range plain {
		if plain[i][1] != analyze[i][1] {
			t.Errorf("node %d: plain EXPLAIN says rows=%s, EXPLAIN ANALYZE says rows=%s — "+
				"the two modes disagree on the same node's estimate\nplain:\n%s\nanalyze:\n%s",
				i, plain[i][1], analyze[i][1], renderPlain(t, filtered), renderAnalyze(t, filtered))
		}
	}
}

// TestNoNodeRendersZeroCost pins P0-02/P0-03's outcome: no rendered node prints
// the literal `cost=0.00..0.00` that both walkers hard-coded before.
//
// The assertion is deliberately about the STRING, not about the numbers being
// right. A plan mixing real costs with 0.00 is worse than one where every cost
// is 0.00 — with all-zero a reader knows nothing is priced, with a mixture a
// genuinely free node and an unpriced one look identical — so the property that
// has to hold is that NOTHING renders the sentinel.
func TestNoNodeRendersZeroCost(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	scan := &optimizer.SeqScan{Table: tbl, EstRelRows: 10000}
	// A stack of above-the-seam nodes: each is built by the legacy rewriter
	// and carries no Path, so each exercises DeriveLegacyDisplayCost.
	plan := &optimizer.Limit{
		Child: &optimizer.Sort{
			Child: &optimizer.Filter{
				Child: scan,
				Predicate: &optimizer.BinaryOp{
					Op:    parser.OpEq,
					Left:  &optimizer.ColumnRef{Name: "a"},
					Right: &optimizer.IntegerConst{Value: 42},
				},
			},
		},
		Limit: &optimizer.IntegerConst{Value: 10},
	}
	for name, text := range map[string]string{
		"plain":   renderPlain(t, plan),
		"analyze": renderAnalyze(t, plan),
	} {
		if strings.Contains(text, "cost=0.00..0.00") {
			t.Errorf("%s walker still renders the zero-cost sentinel:\n%s", name, text)
		}
		if !strings.Contains(text, "cost=") {
			t.Errorf("%s walker rendered no cost at all:\n%s", name, text)
		}
	}
}

// TestLegacyDisplayCostIsMonotone pins the one property
// DeriveLegacyDisplayCost is allowed to claim: a parent costs at least its
// child. It is not a cost model and nothing plans against it, but a
// non-monotone cost column is actively misleading in a diff.
func TestLegacyDisplayCostIsMonotone(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	scan := &optimizer.SeqScan{Table: tbl, EstRelRows: 10000}
	sorted := &optimizer.Sort{Child: scan}
	limited := &optimizer.Limit{Child: sorted, Limit: &optimizer.IntegerConst{Value: 10}}

	scanCost := optimizer.DeriveLegacyDisplayCost(scan, optimizer.EstimateRows(scan))
	sortCost := optimizer.DeriveLegacyDisplayCost(sorted, optimizer.EstimateRows(sorted))
	limitCost := optimizer.DeriveLegacyDisplayCost(limited, optimizer.EstimateRows(limited))

	if sortCost.TotalCost < scanCost.TotalCost {
		t.Errorf("Sort total %.2f < child scan total %.2f", sortCost.TotalCost, scanCost.TotalCost)
	}
	if limitCost.TotalCost < sortCost.TotalCost {
		t.Errorf("Limit total %.2f < child sort total %.2f", limitCost.TotalCost, sortCost.TotalCost)
	}
	// Sort is blocking: PG's cost_tuplesort puts the whole input cost into
	// startup, so nothing emerges until the child is fully consumed.
	if sortCost.StartupCost < scanCost.TotalCost {
		t.Errorf("Sort is blocking: startup %.2f should be >= child total %.2f",
			sortCost.StartupCost, scanCost.TotalCost)
	}
}
