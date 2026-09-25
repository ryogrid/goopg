package executor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// M0125-0037(i) — set operations were opaque to EXPLAIN.
//
// Before this change `describePlan` had no `*planner.SetOp` case (so the
// node printed as the raw Go type name `*planner.SetOp`) and `planChildren`
// had none either (so the branches were never walked). TPC-DS Q5/Q18/Q67
// therefore rendered as four-line plans with the whole query body invisible,
// which is why M0125-0026's plan capture could not classify them at all.
//
// The expected spellings below were captured from PostgreSQL 18.3 on the
// TPC-DS reference cluster (port 65438), not derived from explain.c by
// reading alone:
//
//	UNION ALL  -> Append              (two Seq Scan children)
//	UNION      -> HashAggregate over Append
//	INTERSECT  -> HashSetOp Intersect (two Seq Scan children, no Append)
//	EXCEPT ALL -> HashSetOp Except All
//
// Since M0141-S2b-4a goopg plans UNION (distinct) in PG's shape: one distinct
// step above an n-ary Append of every same-kind branch, instead of the old
// fused `HashSetOp Union`. The hashed step renders `HashAggregate` with a
// `Group Key:` line, as PG's does (M0141-S2b-4d).

// setopExplainFixture creates two same-shaped tables and returns the
// EXPLAIN lines for sql.
func setopExplainLines(t *testing.T, sql string) []string {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)

	runSQL(t, ctx, "CREATE TABLE eso_a (id int, y int)")
	runSQL(t, ctx, "CREATE TABLE eso_b (id int, y int)")
	runSQL(t, ctx, "CREATE TABLE eso_c (id int, y int)")
	runSQL(t, ctx, "INSERT INTO eso_a VALUES (1, 1), (2, 2)")
	runSQL(t, ctx, "INSERT INTO eso_b VALUES (2, 2), (3, 3)")
	runSQL(t, ctx, "INSERT INTO eso_c VALUES (4, 4)")

	return runExplainRows(t, ctx, sql)
}

// TestExplainUnionAllRendersAppendWithBranches is the core regression: a
// UNION ALL must render as PG's `Append` and its branches must be visible.
func TestExplainUnionAllRendersAppendWithBranches(t *testing.T) {
	lines := setopExplainLines(t,
		"EXPLAIN SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b")

	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "optimizer.SetOp") {
		t.Fatalf("EXPLAIN still prints the raw Go type name:\n%s", joined)
	}
	if !strings.Contains(lines[0], "Append") {
		t.Fatalf("expected root line to be Append, got %q (all: %v)", lines[0], lines)
	}
	for _, tbl := range []string{"eso_a", "eso_b"} {
		if !strings.Contains(joined, "Seq Scan on "+tbl) {
			t.Errorf("branch scan on %s missing from plan:\n%s", tbl, joined)
		}
	}
}

// TestExplainUnionAllChainFlattensToOneAppend: goopg builds
// `a UNION ALL b UNION ALL c` as SetOp(SetOp(a,b),c), but PG plans one
// Append with three children. Without flattening, TPC-DS Q5's five-branch
// union would render five Append levels deep and no longer diff against
// PG's plan.
func TestExplainUnionAllChainFlattensToOneAppend(t *testing.T) {
	lines := setopExplainLines(t,
		"EXPLAIN SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b UNION ALL SELECT id FROM eso_c")

	appends := 0
	for _, l := range lines {
		if strings.Contains(l, "Append") {
			appends++
		}
	}
	if appends != 1 {
		t.Errorf("expected exactly 1 Append line for a 3-branch UNION ALL, got %d:\n%s",
			appends, strings.Join(lines, "\n"))
	}
	joined := strings.Join(lines, "\n")
	for _, tbl := range []string{"eso_a", "eso_b", "eso_c"} {
		if !strings.Contains(joined, "Seq Scan on "+tbl) {
			t.Errorf("branch scan on %s missing from plan:\n%s", tbl, joined)
		}
	}
}

// TestExplainIntersectExceptRenderHashSetOp covers the commands PG spells
// with an explicit SetOp node. The `All` suffix is part of PG's label
// (explain.c SETOPCMD_INTERSECT_ALL / SETOPCMD_EXCEPT_ALL).
func TestExplainIntersectExceptRenderHashSetOp(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT id FROM eso_a INTERSECT SELECT id FROM eso_b", "HashSetOp Intersect"},
		{"SELECT id FROM eso_a INTERSECT ALL SELECT id FROM eso_b", "HashSetOp Intersect All"},
		{"SELECT id FROM eso_a EXCEPT SELECT id FROM eso_b", "HashSetOp Except"},
		{"SELECT id FROM eso_a EXCEPT ALL SELECT id FROM eso_b", "HashSetOp Except All"},
		// PG plans a hashed UNION as HashAggregate over Append; goopg plans
		// the same shape (M0141-S2b-4a) and, since M0141-S2b-4d, labels its
		// hashed Distinct the same way.
		{"SELECT id FROM eso_a UNION SELECT id FROM eso_b", "HashAggregate"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			lines := setopExplainLines(t, "EXPLAIN "+tc.sql)
			if !strings.Contains(lines[0], tc.want) {
				t.Fatalf("expected root line to contain %q, got %q", tc.want, lines[0])
			}
			joined := strings.Join(lines, "\n")
			for _, tbl := range []string{"eso_a", "eso_b"} {
				if !strings.Contains(joined, "Seq Scan on "+tbl) {
					t.Errorf("branch scan on %s missing from plan:\n%s", tbl, joined)
				}
			}
		})
	}
}

// TestExplainSetOpChildrenIndentUnderParent guards the property that made
// Q5 unreadable: the branches must be rendered as CHILDREN (one "->" level
// deeper), not as siblings of the set-op line.
func TestExplainSetOpChildrenIndentUnderParent(t *testing.T) {
	lines := setopExplainLines(t,
		"EXPLAIN SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b")

	rootIndent := len(lines[0]) - len(strings.TrimLeft(lines[0], " "))
	for _, l := range lines[1:] {
		if !strings.Contains(l, "Seq Scan on eso_") {
			continue
		}
		ind := len(l) - len(strings.TrimLeft(l, " "))
		if ind <= rootIndent {
			t.Errorf("branch line is not indented under the Append root:\n%s",
				strings.Join(lines, "\n"))
		}
	}
}

// TestExplainSetOpJSONMatchesUpstreamProperties: unlike the TEXT format,
// upstream's JSON does not fold the command into the node name — it emits
// "Node Type": "SetOp" plus separate "Strategy" and "Command" properties
// (verified against PG 18.3). A UNION ALL has no SetOp node upstream at
// all and keeps the plain "Append".
func TestExplainSetOpJSONMatchesUpstreamProperties(t *testing.T) {
	decode := func(t *testing.T, lines []string) map[string]any {
		t.Helper()
		var doc []map[string]any
		if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &doc); err != nil {
			t.Fatalf("EXPLAIN (FORMAT JSON) is not valid JSON: %v\n%s", err, strings.Join(lines, "\n"))
		}
		if len(doc) != 1 {
			t.Fatalf("expected 1 JSON plan object, got %d", len(doc))
		}
		plan, ok := doc[0]["Plan"].(map[string]any)
		if !ok {
			t.Fatalf("no Plan object in %v", doc[0])
		}
		return plan
	}

	intersect := decode(t, setopExplainLines(t,
		"EXPLAIN (FORMAT JSON) SELECT id FROM eso_a INTERSECT ALL SELECT id FROM eso_b"))
	if got := intersect["Node Type"]; got != "SetOp" {
		t.Errorf(`Node Type = %v, want "SetOp"`, got)
	}
	if got := intersect["Strategy"]; got != "Hashed" {
		t.Errorf(`Strategy = %v, want "Hashed"`, got)
	}
	if got := intersect["Command"]; got != "Intersect All" {
		t.Errorf(`Command = %v, want "Intersect All"`, got)
	}
	if plans, ok := intersect["Plans"].([]any); !ok || len(plans) != 2 {
		t.Errorf("expected 2 child plans under the SetOp, got %v", intersect["Plans"])
	}

	unionAll := decode(t, setopExplainLines(t,
		"EXPLAIN (FORMAT JSON) SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b"))
	if got := unionAll["Node Type"]; got != "Append" {
		t.Errorf(`Node Type = %v, want "Append"`, got)
	}
	if _, present := unionAll["Command"]; present {
		t.Errorf("Append must not carry a Command property: %v", unionAll)
	}
}

// setopFixtureCtx is setopExplainLines' fixture, returned for queries that
// check rows as well as plans.
func setopFixtureCtx(t *testing.T) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE eso_a (id int, y int)")
	runSQL(t, ctx, "CREATE TABLE eso_b (id int, y int)")
	runSQL(t, ctx, "CREATE TABLE eso_c (id int, y int)")
	runSQL(t, ctx, "INSERT INTO eso_a VALUES (1, 1), (2, 2)")
	runSQL(t, ctx, "INSERT INTO eso_b VALUES (2, 2), (3, 3)")
	runSQL(t, ctx, "INSERT INTO eso_c VALUES (4, 4), (1, 1)")
	return ctx
}

func countLines(lines []string, sub string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// TestUnionDistinctChainFoldsIntoOneAppend pins M0141-S2b-4a's
// plan_union_children port (prepunion.c:1269): same-kind UNION children fold
// into ONE Append under one distinct step — TPC-DS Q49's shape. A UNION ALL
// child folds into a UNION parent (the distinct step removes its duplicates
// anyway); a UNION child under a UNION ALL parent does not.
func TestUnionDistinctChainFoldsIntoOneAppend(t *testing.T) {
	ctx := setopFixtureCtx(t)
	cases := []struct {
		name, sql string
		appends   int
		distincts int
	}{
		{"union-union", "SELECT id FROM eso_a UNION SELECT id FROM eso_b UNION SELECT id FROM eso_c", 1, 1},
		{"unionall-union", "SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b UNION SELECT id FROM eso_c", 1, 1},
		{"union-unionall", "SELECT id FROM eso_a UNION SELECT id FROM eso_b UNION ALL SELECT id FROM eso_c", 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := runExplainRows(t, ctx, "EXPLAIN "+tc.sql)
			joined := strings.Join(lines, "\n")
			if got := countLines(lines, "Append"); got != tc.appends {
				t.Errorf("Append lines = %d, want %d:\n%s", got, tc.appends, joined)
			}
			if got := countLines(lines, "HashAggregate") + countLines(lines, "Unique"); got != tc.distincts {
				t.Errorf("distinct steps = %d, want %d:\n%s", got, tc.distincts, joined)
			}
			if strings.Contains(joined, "HashSetOp Union") {
				t.Errorf("old fused spelling still present:\n%s", joined)
			}
		})
	}
}

// TestUnionDistinctChainRows: the folded plans return PG's answers.
func TestUnionDistinctChainRows(t *testing.T) {
	ctx := setopFixtureCtx(t)
	cases := []struct {
		sql  string
		want int
	}{
		// {1,2} u {2,3} u {4,1} = {1,2,3,4}
		{"SELECT id FROM eso_a UNION SELECT id FROM eso_b UNION SELECT id FROM eso_c", 4},
		// ALL child folded under UNION: still deduplicated
		{"SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b UNION SELECT id FROM eso_c", 4},
		// UNION then UNION ALL: {1,2,3} ++ {4,1} = 5 rows
		{"SELECT id FROM eso_a UNION SELECT id FROM eso_b UNION ALL SELECT id FROM eso_c", 5},
		// multi-column rows dedup on the whole row
		{"SELECT id, y FROM eso_a UNION SELECT id, y FROM eso_b UNION SELECT id, y FROM eso_c", 4},
	}
	for _, tc := range cases {
		if got := len(runQuery(t, ctx, tc.sql)); got != tc.want {
			t.Errorf("%s: %d rows, want %d", tc.sql, got, tc.want)
		}
	}
}

// TestZeroColumnUnionBothStrategies: `SELECT FROM … UNION SELECT FROM …` has
// no output columns, so the sorted candidate has nothing to sort by. PG's
// generate_union_paths skips the Sort when groupList is NIL and returns one
// row; goopg's first cut built a key-less Sort path and crashed the server
// in upstream's union.sql (M0141-S2b-4a). Both strategies must return PG's
// single row.
func TestZeroColumnUnionBothStrategies(t *testing.T) {
	ctx := setopFixtureCtx(t)
	for _, on := range []bool{true, false} {
		restore := hashAggSeed(on)
		rows, err := runQueryWithErr(ctx, "select from generate_series(1,5) union select from generate_series(1,3)")
		restore()
		if err != nil {
			t.Fatalf("enable_hashagg=%v: %v", on, err)
		}
		if len(rows) != 1 {
			t.Errorf("enable_hashagg=%v: %d rows, want 1", on, len(rows))
		}
	}
}

// TestUnionMergeAppendArm pins M0141-S2b-4c, generate_union_paths' third
// distinct candidate (prepunion.c): Unique straight over a Merge Append of
// branches each sorted on the union pathkeys. With hashing disabled PG 18.3
// plans `Unique -> Merge Append -> Sort, Sort` for a two-branch UNION, and so
// must goopg. The rows check covers what the merge itself must get right:
// branch columns with different names (the keys are evaluated against each
// branch's own row), NULLs (sorted last, then collapsed), and duplicates
// across and within branches.
func TestUnionMergeAppendArm(t *testing.T) {
	ctx := setopFixtureCtx(t)
	runSQL(t, ctx, "CREATE TABLE eso_m1 (k int, s text)")
	runSQL(t, ctx, "CREATE TABLE eso_m2 (j int, u text)")
	runSQL(t, ctx, "INSERT INTO eso_m1 VALUES (3, 'c'), (1, 'a'), (NULL, 'n'), (1, 'a'), (5, NULL)")
	runSQL(t, ctx, "INSERT INTO eso_m2 VALUES (2, 'b'), (1, 'a'), (NULL, 'n'), (4, 'd'), (5, NULL)")
	const sql = "SELECT k, s FROM eso_m1 UNION SELECT j, u FROM eso_m2"

	restore := hashAggSeed(false)
	defer restore()
	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+sql)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Unique", "Merge Append", "Sort Key: eso_m1.k, eso_m1.s"} {
		if !strings.Contains(joined, want) {
			t.Errorf("plan lacks %q:\n%s", want, joined)
		}
	}
	if got := countLines(lines, "->  Sort"); got != 2 {
		t.Errorf("per-branch Sorts = %d, want 2:\n%s", got, joined)
	}
	if strings.Contains(joined, "->  Append") {
		t.Errorf("plain Append under the merge arm:\n%s", joined)
	}

	var got []string
	for _, r := range runQuery(t, ctx, sql) {
		got = append(got, fmt.Sprint(r))
	}
	restore()
	var want []string
	for _, r := range runQuery(t, ctx, sql+" ORDER BY 1, 2") {
		want = append(want, fmt.Sprint(r))
	}
	// {1a,2b,3c,4d,5-NULL,NULL-n}: six distinct rows, in merge order.
	if len(want) != 6 || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("merge-arm rows = %v, want %v", got, want)
	}
}

// TestSelectDistinctOrderByPGShape pins M0141-S2b-4d. PG plans SELECT
// DISTINCT over the unsorted input; its sorted candidate sorts on the
// distinct clause, ORDER BY items first (transformDistinctClause), so the
// ORDER BY needs no second Sort; its hashed candidate prints as
// HashAggregate with a Group Key. Rows must come back deduplicated and in
// the requested order, including DESC, NULLS placement and an ORDER BY on a
// non-leading column.
func TestSelectDistinctOrderByPGShape(t *testing.T) {
	ctx := setopFixtureCtx(t)
	runSQL(t, ctx, "CREATE TABLE eso_d (a int, b text)")
	runSQL(t, ctx, "INSERT INTO eso_d VALUES (1,'x'),(2,'y'),(1,'x'),(NULL,'z'),(3,'y'),(2,'y'),(NULL,'z')")

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT DISTINCT a, b FROM eso_d ORDER BY b DESC")
	joined := strings.Join(lines, "\n")
	// Either PG candidate may win on this tiny table, each in PG's form:
	// Unique over one Sort on the distinct clause, or a Sort over the
	// HashAggregate whose Group Key lists the distinct clause (b first).
	sortedShape := strings.HasPrefix(strings.TrimSpace(lines[0]), "Unique") &&
		countLines(lines, "Sort Key: b DESC, a") == 1 && countLines(lines, "->  Sort") == 1
	hashedShape := strings.HasPrefix(strings.TrimSpace(lines[0]), "Sort") &&
		countLines(lines, "Sort Key: b DESC") == 1 && countLines(lines, "->  HashAggregate") == 1 &&
		countLines(lines, "Group Key: b, a") == 1
	if !sortedShape && !hashedShape {
		t.Errorf("want Unique over a (b DESC, a) Sort, or Sort over HashAggregate (Group Key: b, a):\n%s", joined)
	}

	restore := hashAggSeed(true)
	defer restore()
	lines = runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT id FROM eso_a UNION SELECT id FROM eso_b")
	joined = strings.Join(lines, "\n")
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "HashAggregate") || countLines(lines, "Group Key:") != 1 {
		t.Errorf("want HashAggregate with a Group Key for the hashed UNION:\n%s", joined)
	}

	for _, tc := range []struct {
		sql  string
		want string
	}{
		{"SELECT DISTINCT a, b FROM eso_d ORDER BY b DESC, a", "[<nil> z]|[2 y]|[3 y]|[1 x]"},
		{"SELECT DISTINCT a FROM eso_d ORDER BY a DESC", "[<nil>]|[3]|[2]|[1]"},
		{"SELECT DISTINCT a FROM eso_d ORDER BY a NULLS FIRST", "[<nil>]|[1]|[2]|[3]"},
		{"SELECT DISTINCT a FROM eso_d ORDER BY a", "[1]|[2]|[3]|[<nil>]"},
	} {
		var got []string
		for _, r := range runQuery(t, ctx, tc.sql) {
			cells := make([]string, len(r))
			for i, d := range r {
				switch {
				case d.IsNull():
					cells[i] = "<nil>"
				case d.Kind == KindString || d.ArenaID != 0 || len(d.Buf) > 0:
					cells[i] = d.StringValue()
				default:
					cells[i] = fmt.Sprint(d.Int)
				}
			}
			got = append(got, "["+strings.Join(cells, " ")+"]")
		}
		if strings.Join(got, "|") != tc.want {
			t.Errorf("%s: rows %s, want %s", tc.sql, strings.Join(got, "|"), tc.want)
		}
	}
}
