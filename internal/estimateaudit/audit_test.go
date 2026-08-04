package estimateaudit

import (
	"math"
	"strings"
	"testing"
)

// planQ9Shaped is a TPC-H-Q9-shaped EXPLAIN ANALYZE rendering in goopg's exact
// output format (internal/executor/operators_explain.go: `<indent>->  <label>
// (cost=0.00..0.00 rows=N width=0) (actual time=a..b rows=R loops=L)`), with
// detail lines interleaved so the parser's node/detail discrimination is
// exercised on the real shape rather than a sanitised one.
// Indentation is goopg's own, NOT upstream's: the renderer emits
// `strings.Repeat("  ", depth) + "->  "`, i.e. two spaces per level, while PG
// nests by six. A parser calibrated to PG's width would halve every depth and
// mis-identify the final joinrel.
const planQ9Shaped = `Sort  (cost=0.00..0.00 rows=175 width=0) (actual time=0.100..0.200 rows=175.00 loops=1)
    Sort Key: nation, o_year DESC
  ->  HashAggregate (2 keys)  (cost=0.00..0.00 rows=175 width=0) (actual time=0.090..0.190 rows=175.00 loops=1)
    ->  Hash Join (INNER)  (cost=0.00..0.00 rows=1 width=0) (actual time=0.010..0.080 rows=319404.00 loops=1)
          Hash Cond: (lineitem.l_orderkey = orders.o_orderkey)
      ->  Hash Join (INNER, build=left)  (cost=0.00..0.00 rows=2500 width=0) (actual time=0.005..0.050 rows=319404.00 loops=1)
        ->  Seq Scan on public.lineitem  (cost=0.00..0.00 rows=6001215 width=0) (actual time=0.001..0.030 rows=6001215.00 loops=1)
        ->  Hash  (cost=0.00..0.00 rows=8000 width=0) (actual time=0.001..0.002 rows=8000.00 loops=1)
              Buckets: 8192
      ->  Seq Scan on public.orders  (cost=0.00..0.00 rows=1500000 width=0) (actual time=0.001..0.020 rows=1500000.00 loops=1)`

func TestParseSeparatesNodesFromDetailLines(t *testing.T) {
	nodes := Parse(planQ9Shaped)
	if got, want := len(nodes), 7; got != want {
		t.Fatalf("parsed %d nodes, want %d (detail lines must not parse as nodes)", got, want)
	}
	for _, n := range nodes {
		if strings.Contains(n.Label, "Sort Key") || strings.Contains(n.Label, "Hash Cond") ||
			strings.Contains(n.Label, "Group Key") || strings.Contains(n.Label, "Buckets") {
			t.Errorf("detail line parsed as a node: %q", n.Raw)
		}
	}
	// Root has no arrow and therefore depth 0; the first arrow line sits at
	// depth 1 even though its indentation is two spaces.
	if nodes[0].Label != "Sort" || nodes[0].Depth != 0 {
		t.Errorf("root node = %q depth %d, want \"Sort\" depth 0", nodes[0].Label, nodes[0].Depth)
	}
	if nodes[1].Label != "HashAggregate (2 keys)" || nodes[1].Depth != 1 {
		t.Errorf("node[1] = %q depth %d, want \"HashAggregate (2 keys)\" depth 1", nodes[1].Label, nodes[1].Depth)
	}
}

func TestParseClassifiesJoinLabels(t *testing.T) {
	cases := map[string]bool{
		"Hash Join (inner)":              true,
		"Hash Join (left, build=left)":   true,
		"Nested Loop (semi)":             true,
		"Merge Join (inner)":             true,
		"Multi-Way Hash Join (4 tables)": true,
		"Seq Scan on lineitem":           false,
		"Hash":                           false,
		"HashAggregate":                  false,
		"Index Scan using i on part":     false,
	}
	for label, want := range cases {
		if got := isJoinLabel(label); got != want {
			t.Errorf("isJoinLabel(%q) = %v, want %v", label, got, want)
		}
	}
}

func TestParseReadsTimingOffRendering(t *testing.T) {
	// TIMING OFF drops the "time=a..b " run from the actual parenthetical
	// (operators_explain.go's non-timing branch); both must parse.
	line := `  ->  Hash Join (inner)  (cost=0.00..0.00 rows=42 width=0) (actual rows=1000.00 loops=3)`
	nodes := Parse(line)
	if len(nodes) != 1 {
		t.Fatalf("parsed %d nodes, want 1", len(nodes))
	}
	n := nodes[0]
	if !n.HasAct || n.Actual != 1000 || n.Loops != 3 || n.EstRows != 42 {
		t.Fatalf("got est=%d actual=%v loops=%d hasAct=%v; want 42/1000/3/true",
			n.EstRows, n.Actual, n.Loops, n.HasAct)
	}
	// The printed count is already cumulative across loops in goopg — the
	// tool must NOT multiply by loops (that is the PG-divergence recorded in
	// the package comment; multiplying would report 3000 here).
	if got := n.ActualTotal(); got != 1000 {
		t.Errorf("ActualTotal() = %v, want 1000 (printed value is cumulative, not per-loop)", got)
	}
}

func TestRatioClampsBothSidesAtOne(t *testing.T) {
	// A joinrel that produced no rows at all: the planner's estimate is
	// clamped at 1 by EstimateRows, so the actual must be clamped the same
	// way or an empty joinrel reads as an infinite misestimate.
	empty := Node{EstRows: 1, Actual: 0, HasAct: true}
	if got := empty.Ratio(); got != 1 {
		t.Errorf("ratio of est=1/actual=0 = %v, want 1", got)
	}
	if got := (Node{EstRows: 1000, Actual: 0, HasAct: true}).Ratio(); got != 1000 {
		t.Errorf("ratio of est=1000/actual=0 = %v, want 1000", got)
	}
	if got := (Node{EstRows: 1, Actual: 5000, HasAct: true}).Ratio(); got != 5000 {
		t.Errorf("ratio of est=1/actual=5000 = %v, want 5000", got)
	}
	// A node from a plan captured without ANALYZE has no ratio to report.
	if got := (Node{EstRows: 10}).Ratio(); got != 0 {
		t.Errorf("ratio without ANALYZE = %v, want 0", got)
	}
}

func TestRatioIsDirectionless_DirectionReportedSeparately(t *testing.T) {
	over := Node{EstRows: 1000, Actual: 10, HasAct: true}
	under := Node{EstRows: 10, Actual: 1000, HasAct: true}
	if math.Abs(over.Ratio()-under.Ratio()) > 1e-9 {
		t.Fatalf("ratios differ: %v vs %v — the factor must be direction-free", over.Ratio(), under.Ratio())
	}
	if !over.Overestimated() {
		t.Error("est=1000 actual=10 must report Overestimated")
	}
	if under.Overestimated() {
		t.Error("est=10 actual=1000 must not report Overestimated")
	}
}

func TestAuditPicksOutermostJoinAsFinalJoinrel(t *testing.T) {
	r := Audit("Q9", planQ9Shaped)
	if r.Err != "" {
		t.Fatalf("unexpected error: %s", r.Err)
	}
	if len(r.Joins) != 2 {
		t.Fatalf("found %d joins, want 2", len(r.Joins))
	}
	if r.Final == nil || r.Final.EstRows != 1 {
		t.Fatalf("final joinrel = %+v, want the depth-2 est=1 Hash Join", r.Final)
	}
	// The final joinrel is the one §5's ≤10² bar applies to: est 1 vs
	// actual 319404 is the 3e5× miss the 04 §3 mechanisms exist to close.
	if got := r.Final.Ratio(); got < 3e5 || got > 4e5 {
		t.Errorf("final joinrel ratio = %v, want ~3.2e5", got)
	}
	if r.Worst == nil || r.Worst.Raw != r.Final.Raw {
		t.Errorf("worst joinrel = %+v, want the final one", r.Worst)
	}
}

func TestAuditReportsPlanWithNoNodes(t *testing.T) {
	r := Audit("Q1", "Sort\n  ->  Seq Scan on lineitem")
	if r.Err == "" {
		t.Fatal("a COSTS OFF capture must be reported as an error, not as a clean audit")
	}
}

func TestAuditReportsJoinlessQuery(t *testing.T) {
	r := Audit("Q6", `Aggregate  (cost=0.00..0.00 rows=1 width=0) (actual rows=1.00 loops=1)
  ->  Seq Scan on lineitem  (cost=0.00..0.00 rows=100 width=0) (actual rows=114160.00 loops=1)`)
	if r.Err != "" {
		t.Fatalf("unexpected error: %s", r.Err)
	}
	if len(r.Joins) != 0 || r.Final != nil {
		t.Fatalf("joinless query reported %d joins / final=%v", len(r.Joins), r.Final)
	}
	// A joinless query has no joinrel to violate, even though its scan is
	// badly misestimated — §5's unit is the joinrel.
	if v := Violations([]QueryReport{r}, DefaultThresholds()); len(v) != 0 {
		t.Errorf("joinless query produced violations: %v", v)
	}
}

func TestViolationsAppliesTheTighterFinalJoinBar(t *testing.T) {
	// est=500 vs actual=100000 is 200× — under the 10³ tripwire, but over
	// the 10² bar that applies to the final joinrel of Q9.
	plan := `Hash Join (inner)  (cost=0.00..0.00 rows=500 width=0) (actual rows=100000.00 loops=1)
  ->  Seq Scan on a  (cost=0.00..0.00 rows=100 width=0) (actual rows=100.00 loops=1)`
	t.Run("Q9 final bar bites", func(t *testing.T) {
		v := Violations([]QueryReport{Audit("Q9", plan)}, DefaultThresholds())
		if len(v) != 1 {
			t.Fatalf("got %d violations, want 1: %v", len(v), v)
		}
		if !v[0].Final || v[0].Threshold != DefaultFinalJoinMax {
			t.Errorf("violation = %+v, want the final-joinrel 100x bar", v[0])
		}
	})
	t.Run("same plan under another query name passes", func(t *testing.T) {
		if v := Violations([]QueryReport{Audit("Q5", plan)}, DefaultThresholds()); len(v) != 0 {
			t.Fatalf("Q5 has no per-query override, so 200x is under the 1000x tripwire: %v", v)
		}
	})
}

func TestViolationsSortsWorstFirstAndSkipsUnmeasured(t *testing.T) {
	small := Audit("Q5", `Hash Join (inner)  (cost=0.00..0.00 rows=1 width=0) (actual rows=5000.00 loops=1)
  ->  Seq Scan on a  (cost=0.00..0.00 rows=1 width=0) (actual rows=1.00 loops=1)`)
	big := Audit("Q7", `Hash Join (inner)  (cost=0.00..0.00 rows=1 width=0) (actual rows=900000.00 loops=1)
  ->  Seq Scan on a  (cost=0.00..0.00 rows=1 width=0) (actual rows=1.00 loops=1)`)
	failed := AuditError("Q21", "timeout after 600s")

	reports := []QueryReport{small, big, failed}
	v := Violations(reports, DefaultThresholds())
	if len(v) != 2 {
		t.Fatalf("got %d violations, want 2 (the failed capture is not a violation): %v", len(v), v)
	}
	if v[0].Query != "Q7" {
		t.Errorf("violations[0] = %s, want Q7 (worst first)", v[0].Query)
	}
	u := Unmeasured(reports)
	if len(u) != 1 || u[0].Name != "Q21" {
		t.Fatalf("Unmeasured = %v, want [Q21] — a timed-out query must stay distinguishable from a clean one", u)
	}
}

func TestRenderIsDeterministicAndNamesTheFinalJoinrel(t *testing.T) {
	reports := []QueryReport{Audit("Q9", planQ9Shaped), AuditError("Q21", "timeout")}
	out := Render(reports, DefaultThresholds())
	if out != Render(reports, DefaultThresholds()) {
		t.Fatal("Render is not deterministic — the committed artifact would diff on every run")
	}
	for _, want := range []string{"=== Q9", "F d2", "ratio=", "underestimated", "UNMEASURED Q21", "Q9<=100x"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered report missing %q:\n%s", want, out)
		}
	}
}
