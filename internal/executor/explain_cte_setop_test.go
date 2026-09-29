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

// TestExplainInlinedGroupedCTEKeepsSubqueryScan pins M0146-0005av against
// PG 18.3: inline_cte makes a single-reference CTE an ordinary subquery, and
// a grouped body is one pull_up_subqueries refuses, so the reference is a
// SubqueryScan. setrefs keeps it when the member's target list computes
// anything (TPC-DS Q5's `'store' || s_store_id`), and deletes it when the
// member reads the columns as they are.
func TestExplainInlinedGroupedCTEKeepsSubqueryScan(t *testing.T) {
	kept := cteExplainLines(t,
		`WITH s AS (SELECT a, count(*) AS c FROM t GROUP BY a)
		 SELECT 'x' AS ch, a + 1 AS id, c FROM s UNION ALL SELECT 'y', 1, 1`)
	if got := countLinesContaining(kept, "Subquery Scan on s"); got != 1 {
		t.Errorf("computed member target list: want 1 `Subquery Scan on s`, got %d:\n%s", got, strings.Join(kept, "\n"))
	}
	stripped := cteExplainLines(t,
		`WITH s AS (SELECT a, count(*) AS c FROM t GROUP BY a)
		 SELECT a, c FROM s UNION ALL SELECT 1, 1`)
	if got := countLinesContaining(stripped, "Subquery Scan on s"); got != 0 {
		t.Errorf("identity member target list: want the scan stripped, got %d:\n%s", got, strings.Join(stripped, "\n"))
	}
	// A qual moved into the body (M0146-0007b) leaves a constant-true
	// Filter on the reference; that is no scan qual, and under a join the
	// physical tlist makes the scan trivial (TPC-DS Q78's ss/ws/cs).
	joined := cteExplainLines(t,
		`WITH s AS (SELECT a, count(*) AS c FROM t GROUP BY a)
		 SELECT s.c, t.b FROM s JOIN t ON s.a = t.a WHERE s.a = 1`)
	if got := countLinesContaining(joined, "Subquery Scan on s"); got != 0 {
		t.Errorf("moved qual under a join: want the scan stripped, got %d:\n%s", got, strings.Join(joined, "\n"))
	}
}
