package postmaster

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestExplainSplitAggregateChainCosts pins M0146-0182 against PG 18.3's
// cost_gather / cost_gather_merge.
//
// The partial-aggregate split builds its Gather (Merge), worker Sort and
// Partial aggregate in createplansimple.go rather than through the
// path-to-plan funnel, so those nodes printed the legacy display estimate
// instead of their paths' costs. TPC-DS Q76's Gather Merge printed its
// Sort's own startup, without parallel_setup_cost, and the per-worker group
// count as its rows; Q2's Gather printed a startup above the Finalize's.
// PG's boundary node costs at least its child plus parallel_setup_cost
// (1000) and returns compute_gather_rows — the per-worker rows times the
// parallel divisor.
func TestExplainSplitAggregateChainCosts(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE sc1 (c int, x int)",
		"CREATE TABLE sc2 (c int, x int)",
		"CREATE TABLE sc3 (c int, x int)",
		"INSERT INTO sc1 SELECT g % 50, g FROM generate_series(1, 200000) g",
		"INSERT INTO sc2 SELECT g % 50, g FROM generate_series(1, 200000) g",
		"INSERT INTO sc3 SELECT g % 50, g FROM generate_series(1, 200000) g",
		"ANALYZE sc1",
		"ANALYZE sc2",
		"ANALYZE sc3",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN SELECT c, count(*) FROM (
  SELECT c FROM sc1 WHERE x > 10 UNION ALL
  SELECT c FROM sc2 WHERE x > 10 UNION ALL
  SELECT c FROM sc3 WHERE x > 10) s GROUP BY c ORDER BY c`)
	if e != "" {
		t.Fatal(e)
	}
	nodes := parseExplainCosts(got)
	gm := findExplainNode(nodes, "Gather Merge")
	srt := findExplainNode(nodes, "Sort")
	partial := findExplainNode(nodes, "Partial HashAggregate")
	if gm == nil || srt == nil || partial == nil {
		t.Fatalf("expected PG's Finalize / Gather Merge / Sort / Partial HashAggregate split:\n%s", strings.Join(got, "\n"))
	}
	if gm.startup < srt.startup+1000 {
		t.Errorf("Gather Merge startup %v is below its Sort's %v plus parallel_setup_cost:\n%s", gm.startup, srt.startup, strings.Join(got, "\n"))
	}
	if gm.total < srt.total || gm.rows <= srt.rows {
		t.Errorf("Gather Merge (total %v, rows %v) must exceed its per-worker Sort (total %v, rows %v):\n%s",
			gm.total, gm.rows, srt.total, srt.rows, strings.Join(got, "\n"))
	}
	if srt.startup < partial.total {
		t.Errorf("worker Sort startup %v is below its Partial HashAggregate's total %v:\n%s", srt.startup, partial.total, strings.Join(got, "\n"))
	}

	// The hashed arm: Finalize HashAggregate -> Gather -> Partial
	// HashAggregate. A hash Finalize starts only after its whole input.
	for _, q := range []string{
		"CREATE TABLE sc4 (c int, x int)",
		"INSERT INTO sc4 SELECT g / 60, g FROM generate_series(1, 600000) g",
		"ANALYZE sc4",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e = allRowsQuery(t, conn, "EXPLAIN SELECT c, count(*), sum(x) FROM sc4 GROUP BY c")
	if e != "" {
		t.Fatal(e)
	}
	nodes = parseExplainCosts(got)
	final := findExplainNode(nodes, "Finalize HashAggregate")
	gather := findExplainNode(nodes, "Gather")
	partial = findExplainNode(nodes, "Partial HashAggregate")
	if final == nil || gather == nil || partial == nil {
		t.Fatalf("expected PG's Finalize HashAggregate / Gather / Partial HashAggregate split:\n%s", strings.Join(got, "\n"))
	}
	if gather.startup < partial.startup+1000 || gather.total < partial.total || gather.rows <= partial.rows {
		t.Errorf("Gather (%v..%v rows %v) must cost its Partial (%v..%v rows %v) plus parallel_setup_cost and return all workers' rows:\n%s",
			gather.startup, gather.total, gather.rows, partial.startup, partial.total, partial.rows, strings.Join(got, "\n"))
	}
	if final.startup < gather.total {
		t.Errorf("Finalize HashAggregate startup %v is below its Gather's total %v:\n%s", final.startup, gather.total, strings.Join(got, "\n"))
	}
}

type explainCostNode struct {
	label                string
	startup, total, rows float64
}

var explainCostRE = regexp.MustCompile(`^\s*(?:->\s+)?(.+?)\s+\(cost=([0-9.]+)\.\.([0-9.]+) rows=([0-9]+) `)

// parseExplainCosts returns each plan line's node label and cost figures.
func parseExplainCosts(lines []string) []explainCostNode {
	var out []explainCostNode
	for _, l := range lines {
		m := explainCostRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		s, _ := strconv.ParseFloat(m[2], 64)
		tt, _ := strconv.ParseFloat(m[3], 64)
		r, _ := strconv.ParseFloat(m[4], 64)
		out = append(out, explainCostNode{label: m[1], startup: s, total: tt, rows: r})
	}
	return out
}

func findExplainNode(nodes []explainCostNode, label string) *explainCostNode {
	for i := range nodes {
		if nodes[i].label == label {
			return &nodes[i]
		}
	}
	return nil
}
