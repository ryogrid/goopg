package executor

import (
	"strings"
	"testing"
)

// M0125-0049 guards. Every expectation below was captured from PostgreSQL
// 18.3 first (`EXPLAIN (COSTS OFF)` on the same statement shape) and only then
// asserted here — the point of the change is the upstream shape, not a shape
// goopg finds convenient.

// cteExplainLines renders EXPLAIN over sql against a fixture holding a table
// `t(a int, b int)`, which is the shape all three PG captures used.
func cteExplainLines(t *testing.T, sql string) []string {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	if err := runDDL(t, ctx, "CREATE TABLE t (a int, b int)"); err != nil {
		t.Fatal(err)
	}
	return runExplain(t, ctx, sql)
}

func countLinesContaining(lines []string, sub string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// TestExplainSharedCTEBodyRendersOnce is the regression this task exists for.
// goopg hands every consumer the SAME body Node (planScanRangeVar's
// `Child: ce.body`), and the walker used to walk that DAG as a tree, so an
// N-times-referenced CTE printed its whole subtree N times — TPC-DS Q67 showed
// 36 `store_sales` mentions for a join that executes once. PG prints the body
// once under a `CTE <name>` heading:
//
//	 Hash Join
//	   CTE x
//	     ->  Seq Scan on t
//	   ->  CTE Scan on x q
//	   ->  Hash
//	         ->  CTE Scan on x p
func TestExplainSharedCTEBodyRendersOnce(t *testing.T) {
	lines := cteExplainLines(t,
		"WITH x AS (SELECT a, b FROM t WHERE a > 5) SELECT * FROM x p JOIN x q ON p.a = q.b")
	joined := strings.Join(lines, "\n")

	if got := countLinesContaining(lines, "CTE x"); got != 1 {
		t.Errorf("want exactly 1 `CTE x` heading, got %d:\n%s", got, joined)
	}
	// The body — the only scan of t in this statement — must appear once,
	// however many references there are.
	if got := countLinesContaining(lines, "Scan on t"); got != 1 {
		t.Errorf("want the CTE body's scan of t exactly once, got %d:\n%s", got, joined)
	}
	// Both references still render, as leaves.
	if got := countLinesContaining(lines, "CTE Scan on x"); got != 2 {
		t.Errorf("want 2 `CTE Scan on x` reference lines, got %d:\n%s", got, joined)
	}
}

// TestExplainCTESectionIndentAndOrder pins the two-line section shape against
// the PG capture: the heading sits at the children's indent WITHOUT an arrow,
// and the body one level below it WITH one.
func TestExplainCTESectionIndentAndOrder(t *testing.T) {
	lines := cteExplainLines(t,
		"WITH x AS (SELECT a, b FROM t) SELECT * FROM x p JOIN x q ON p.a = q.b")
	idx := -1
	for i, l := range lines {
		if strings.HasSuffix(strings.TrimRight(l, " "), "CTE x") {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("no `CTE x` heading:\n%s", strings.Join(lines, "\n"))
	}
	if lines[idx] != "  CTE x" {
		t.Errorf("heading indent: want %q, got %q", "  CTE x", lines[idx])
	}
	if idx == 0 {
		t.Fatalf("`CTE x` cannot be the first line — it hangs off the top node")
	}
	// 4 raw spaces before "->": under PG's cumulative es->indent model
	// (postgres/src/backend/commands/explain.c:1616-1635, ExplainNode),
	// a `plan_name` label (the CTE-heading branch, step 1) is printed at
	// the owning node's own post-processing indent and then bumped by
	// only +1 (not +2 — the +2 for the "->  " marker itself is applied
	// when ExplainNode is re-entered for the body). For this fixture the
	// owning node is the query root, whose own post-processing indent is
	// 1 unit (2 raw spaces, matching the `  CTE x` heading assertion
	// above) — so the body's incoming indent is 2 units, and its "->  "
	// prints at 2*2=4 raw spaces. Verified against a live PostgreSQL
	// capture of this exact statement (`WITH x AS (...) SELECT * FROM x
	// p JOIN x q ON p.a = q.b`, both merge- and hash-joined) and against
	// postgres/src/test/regress/expected/rowsecurity.out:3333-3336
	// (`CTE Scan on cte1` / `CTE cte1` / `->  Seq Scan on t1`, the same
	// single-level-nested shape) — both give 4, not the flat model's
	// coincidental agreement at this shallow depth.
	if idx+1 >= len(lines) || !strings.HasPrefix(lines[idx+1], "    ->  ") {
		t.Errorf("body must follow the heading at the label's childIndent+1 with an arrow, got %q",
			lines[min(idx+1, len(lines)-1)])
	}
}

// TestExplainCTESectionDeclarationOrder: `y` references `x`, so a render-order
// hoist would print `CTE y` first. PG prints declaration order (SS_process_ctes
// walks the WITH list left to right), which is what CTEScan.DeclSeq restores.
func TestExplainCTESectionDeclarationOrder(t *testing.T) {
	lines := cteExplainLines(t,
		"WITH x AS (SELECT a, b FROM t), y AS (SELECT a FROM x) "+
			"SELECT * FROM y z1 JOIN y z2 ON z1.a = z2.a")
	joined := strings.Join(lines, "\n")
	xi, yi := -1, -1
	for i, l := range lines {
		switch {
		case xi < 0 && strings.HasSuffix(l, "CTE x"):
			xi = i
		case yi < 0 && strings.HasSuffix(l, "CTE y"):
			yi = i
		}
	}
	if xi < 0 || yi < 0 {
		t.Fatalf("want both `CTE x` and `CTE y` sections (x=%d y=%d):\n%s", xi, yi, joined)
	}
	if xi > yi {
		t.Errorf("sections out of declaration order: `CTE x` at %d after `CTE y` at %d:\n%s", xi, yi, joined)
	}
	// x's body is a scan of t; y's body is a bare reference to x. Both print
	// once, and y's reference to x is a leaf.
	if got := countLinesContaining(lines, "Scan on t"); got != 1 {
		t.Errorf("want one scan of t, got %d:\n%s", got, joined)
	}
	if got := countLinesContaining(lines, "CTE Scan on x"); got != 1 {
		t.Errorf("want one `CTE Scan on x` (inside y's body), got %d:\n%s", got, joined)
	}
}

// TestExplainGroupingSetsSharedSourceSectioned is the case that rules out a
// body-Node-pointer hoist key. M0125-0040 rewrites a grouping-sets query into a
// UNION ALL of branches over a synthetic `__gs_src_<pos>` CTE, and planSelect
// re-enters on the head operand of that chain — so the one declaration is
// preplanned twice and the references carry DISTINCT (structurally identical)
// body Nodes for the one ctx.CTERowCache buffer they all read. Keyed by
// pointer, only the first body hoisted and TPC-DS Q67 still printed 36
// `store_sales` mentions for a join that scans it once.
//
// The key is the declaration site (CTEScan.DeclKey, M0125-0050), which both
// preplan passes derive identically from the same CommonTableExpr — so this
// still collapses to one section while genuinely distinct declarations that
// happen to share a name no longer do.
func TestExplainGroupingSetsSharedSourceSectioned(t *testing.T) {
	lines := cteExplainLines(t, "SELECT a, b, count(*) FROM t GROUP BY ROLLUP(a, b)")
	joined := strings.Join(lines, "\n")
	if countLinesContaining(lines, "CTE Scan on __gs_src") < 2 {
		t.Skipf("grouping-sets source sharing did not fire; nothing to guard:\n%s", joined)
	}
	if got := countLinesContaining(lines, "Scan on t"); got != 1 {
		t.Errorf("want the shared source scanned once in the plan text, got %d:\n%s", got, joined)
	}
	sections := 0
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, " "), "CTE __gs_src") {
			sections++
		}
	}
	if sections != 1 {
		t.Errorf("want exactly 1 `CTE __gs_src_*` section, got %d:\n%s", sections, joined)
	}
}

// TestExplainSingleReferenceCTEStillSectioned: PG sections a CTE even at one
// reference (the third capture — `CTE Scan on x` at the root with `CTE x`
// beneath it), so goopg must not special-case refs==1 into the old inline
// shape.
func TestExplainSingleReferenceCTEStillSectioned(t *testing.T) {
	lines := cteExplainLines(t, "WITH x AS (SELECT a, b FROM t) SELECT * FROM x")
	joined := strings.Join(lines, "\n")
	if countLinesContaining(lines, "CTE Scan on x") != 1 {
		t.Errorf("want the reference line:\n%s", joined)
	}
	if countLinesContaining(lines, "CTE x") != 1 {
		t.Errorf("want the section heading:\n%s", joined)
	}
	if countLinesContaining(lines, "Scan on t") != 1 {
		t.Errorf("want the body exactly once:\n%s", joined)
	}
}

// TestExplainSameNameDisjointScopesSectionedTwice is the render half of
// M0125-0050. Before it, `collectCTEHoist` claimed one section per CTE NAME,
// so two unrelated `WITH x` declarations in disjoint subqueries collapsed into
// a single `CTE x` heading — which faithfully reflected the runtime, because
// the runtime was wrongly collapsing them too.
//
// Both are keyed by declaration now, so each declaration gets its own section
// and its own body. That is also what PG prints: SS_process_ctes emits one
// subplan per WITH entry and ExplainSubPlans prints a heading per subplan, so
// two same-named declarations give two `CTE x` headings.
func TestExplainSameNameDisjointScopesSectionedTwice(t *testing.T) {
	lines := cteExplainLines(t,
		`SELECT v FROM (WITH x AS (SELECT a AS v FROM t) SELECT v FROM x) p
		 UNION ALL
		 SELECT v FROM (WITH x AS (SELECT b AS v FROM t) SELECT v FROM x) q`)
	joined := strings.Join(lines, "\n")

	sections := 0
	for _, l := range lines {
		if strings.HasSuffix(l, "CTE x") {
			sections++
		}
	}
	if sections != 2 {
		t.Errorf("want 2 `CTE x` sections (one per declaration), got %d:\n%s", sections, joined)
	}
	// One body each — two declarations, two scans of t. A name-keyed hoist
	// printed one section and one scan, hiding the second declaration
	// entirely.
	if got := countLinesContaining(lines, "Scan on t"); got != 2 {
		t.Errorf("want 2 scans of t (one body per declaration), got %d:\n%s", got, joined)
	}
	if got := countLinesContaining(lines, "CTE Scan on x"); got != 2 {
		t.Errorf("want 2 `CTE Scan on x` reference leaves, got %d:\n%s", got, joined)
	}
}
