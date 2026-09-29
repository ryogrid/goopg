package executor

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var explainTotalCostRE = regexp.MustCompile(`cost=[0-9.]+\.\.([0-9.]+) `)

// explainTotalCost returns the total cost printed on the first line that
// contains sub.
func explainTotalCost(t *testing.T, lines []string, sub string) float64 {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(l, sub) {
			continue
		}
		m := explainTotalCostRE.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("no cost on line %q", l)
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	t.Fatalf("no line contains %q:\n%s", sub, strings.Join(lines, "\n"))
	return 0
}

// TestExplainUnionBranchesShareOneCTE: a CTE read by two UNION ALL branches
// is referenced twice, so PG keeps it — one `CTE x` section and a CTE Scan
// per branch. The leftmost branch used to preplan the WITH list again into a
// scope of its own, so each branch saw a single reference and inlined its
// own copy of the body.
func TestExplainUnionBranchesShareOneCTE(t *testing.T) {
	lines := cteExplainLines(t,
		`WITH x AS (SELECT a, count(*) AS c FROM t GROUP BY a)
		 SELECT a FROM x UNION ALL SELECT a FROM x`)
	joined := strings.Join(lines, "\n")
	if got := countLinesContaining(lines, "CTE x"); got != 1 {
		t.Errorf("want exactly 1 `CTE x` section, got %d:\n%s", got, joined)
	}
	if got := countLinesContaining(lines, "CTE Scan on x"); got != 2 {
		t.Errorf("want 2 `CTE Scan on x` leaves, got %d:\n%s", got, joined)
	}
	if got := countLinesContaining(lines, "Seq Scan on t"); got != 1 {
		t.Errorf("want the body planned once (1 scan of t), got %d:\n%s", got, joined)
	}
}

// TestExplainInlinedCTEPricesItsBody: a single-reference CTE is an ordinary
// subquery in PG (inline_cte), priced by cost_subqueryscan over its body. The
// reference used to be priced as a bare CTE Scan, so an Append over it cost
// less than the aggregate it printed beneath it (TPC-DS Q5: 3.90 over three
// ~20000 branches).
func TestExplainInlinedCTEPricesItsBody(t *testing.T) {
	lines := cteExplainLines(t,
		`WITH x AS (SELECT a, count(*) AS c FROM t GROUP BY a)
		 SELECT a FROM x UNION ALL SELECT 1`)
	joined := strings.Join(lines, "\n")
	if got := countLinesContaining(lines, "CTE x"); got != 0 {
		t.Fatalf("single-reference CTE should render inlined:\n%s", joined)
	}
	appendTotal := explainTotalCost(t, lines, "Append")
	aggTotal := explainTotalCost(t, lines, "Aggregate")
	if appendTotal < aggTotal {
		t.Errorf("Append total %.2f < its inlined aggregate branch %.2f:\n%s", appendTotal, aggTotal, joined)
	}
}
