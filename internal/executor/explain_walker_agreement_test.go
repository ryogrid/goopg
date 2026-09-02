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
