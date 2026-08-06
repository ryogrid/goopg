package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// M0125-0048 — the single-pass grouping-sets aggregate.
//
// goopg used to expand GROUP BY ROLLUP/CUBE/GROUPING SETS into a UNION ALL of
// one plain-GROUP-BY branch per set (the SQL:1999 definition), which read the
// source once per set and resolved GROUPING(...) to a per-branch integer
// literal. PostgreSQL computes every level in ONE pass with one hash table per
// set. These tests pin the observable consequences of the switch. Every
// expected value in this file was captured from PostgreSQL 18.3 on the
// reference cluster (port 65432) on 2026-08-06, not derived from the goopg
// implementation.

func gsFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	if err := runDDL(t, ctx, "CREATE TABLE t (dept text, region text, amt int)"); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES ('a','x',10), ('a','y',20), ('b','x',5)"); err != nil {
		cleanup()
		t.Fatal(err)
	}
	return ctx, cleanup
}

// TestGroupingSetsEmptyInputStillEmitsGrandTotal pins the level that only the
// single-pass shape makes obvious: the grand-total grouping set produces a row
// even when the source is empty, exactly as an ungrouped aggregate does, while
// the same query's detail level produces none. PG 18.3:
//
//	SELECT dept, count(*) FROM empty GROUP BY ROLLUP(dept)   -> 1 row (NULL, 0)
//	SELECT dept, count(*) FROM empty GROUP BY dept           -> 0 rows
func TestGroupingSetsEmptyInputStillEmitsGrandTotal(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE e (dept text, amt int)"); err != nil {
		t.Fatal(err)
	}

	rows := runQuery(t, ctx, "SELECT dept, count(*) FROM e GROUP BY ROLLUP(dept)")
	if len(rows) != 1 {
		t.Fatalf("ROLLUP over empty input: rows=%d want 1: %+v", len(rows), rows)
	}
	if !rows[0][0].IsNull() {
		t.Fatalf("grand-total dept=%+v want NULL", rows[0][0])
	}
	if rows[0][1].IsNull() || rows[0][1].Int != 0 {
		t.Fatalf("grand-total count=%+v want 0", rows[0][1])
	}

	if rows := runQuery(t, ctx, "SELECT dept, count(*) FROM e GROUP BY dept"); len(rows) != 0 {
		t.Fatalf("plain GROUP BY over empty input: rows=%d want 0: %+v", len(rows), rows)
	}
}

// TestGroupingSetsDuplicateSetsAreNotMerged pins that two identical grouping
// sets stay two sets. PG 18.3 returns a,b,a,b for
// `GROUP BY GROUPING SETS ((dept),(dept))` — the sets are positional, not a
// set-of-sets, so a hash table keyed on the group columns alone would silently
// halve the answer.
func TestGroupingSetsDuplicateSetsAreNotMerged(t *testing.T) {
	ctx, cleanup := gsFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx, "SELECT dept, count(*) FROM t GROUP BY GROUPING SETS ((dept),(dept))")
	if len(rows) != 4 {
		t.Fatalf("rows=%d want 4 (two copies of the two dept groups): %+v", len(rows), rows)
	}
	want := []struct {
		dept  string
		count int64
	}{{"a", 2}, {"b", 1}, {"a", 2}, {"b", 1}}
	for i, w := range want {
		if rows[i][0].IsNull() || rows[i][0].StringValue() != w.dept {
			t.Fatalf("row[%d] dept=%+v want %q", i, rows[i][0], w.dept)
		}
		if rows[i][1].Int != w.count {
			t.Fatalf("row[%d] count=%+v want %d", i, rows[i][1], w.count)
		}
	}
}

// TestGroupingFuncColumnIsNamedGrouping pins the output column name of a bare
// GROUPING(...) target. PG names it "grouping" (FigureColname's GroupingFunc
// case in parse_target.c); the retired expansion rewrote the call to an
// integer literal, which would have named it "?column?".
func TestGroupingFuncColumnIsNamedGrouping(t *testing.T) {
	ctx, cleanup := gsFixture(t)
	defer cleanup()

	stmts, err := parser.Parse("SELECT dept, GROUPING(dept) FROM t GROUP BY ROLLUP(dept)")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	out := plan.Output()
	if len(out) != 2 {
		t.Fatalf("schema=%+v want 2 columns", out)
	}
	if out[1].Name != "grouping" {
		t.Fatalf("GROUPING() column name=%q want \"grouping\"", out[1].Name)
	}
}

// TestGroupingFuncRejectsNonGroupingArgument pins PG's 42803 for
// `GROUPING(region)` under `GROUP BY ROLLUP(dept)`. Under the retired
// expansion the bitmask was computed against the branch's active set and an
// unknown argument silently contributed a 1 bit.
func TestGroupingFuncRejectsNonGroupingArgument(t *testing.T) {
	ctx, cleanup := gsFixture(t)
	defer cleanup()

	_, err := runQueryErr(t, ctx, "SELECT dept, GROUPING(region) FROM t GROUP BY ROLLUP(dept)")
	if err == nil {
		t.Fatal("GROUPING(region) under GROUP BY ROLLUP(dept) was accepted; PG raises 42803")
	}
	const want = "arguments to GROUPING must be grouping expressions of the associated query level"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v\nwant it to contain %q", err, want)
	}
}

// TestGroupingSetsRejectFunctionalDependency pins the interaction PostgreSQL
// spells out in parse_agg.c: a functional dependency may only be proven
// against groupClauseCommonVars — the columns present in EVERY grouping set —
// so `SELECT id, name ... GROUP BY ROLLUP(id)` is an error even though id is
// the primary key. The grand-total level groups by nothing and cannot carry a
// name. Captured from PG 18.3:
//
//	ERROR:  column "gsp.name" must appear in the GROUP BY clause or be used in
//	        an aggregate function
func TestGroupingSetsRejectFunctionalDependency(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE p (id int primary key, name text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO p VALUES (1,'p'), (2,'q')"); err != nil {
		t.Fatal(err)
	}

	// The dependency IS accepted under a plain GROUP BY on the primary key —
	// that half must keep working.
	if rows := runQuery(t, ctx, "SELECT id, name, count(*) FROM p GROUP BY id"); len(rows) != 2 {
		t.Fatalf("plain GROUP BY on the PK: rows=%d want 2: %+v", len(rows), rows)
	}

	_, err := runQueryErr(t, ctx, "SELECT id, name, count(*) FROM p GROUP BY ROLLUP(id)")
	if err == nil {
		t.Fatal("functionally-dependent column accepted under ROLLUP; PG raises 42803")
	}
	if !strings.Contains(err.Error(), "must appear in the GROUP BY clause") {
		t.Fatalf("error = %v\nwant the 42803 ungrouped-column message", err)
	}
}

// TestGroupingSetsPlanIsOneAggregateOverOneScan is the shape assertion: the
// whole point of M0125-0048 is that a ROLLUP no longer builds one branch per
// set. PG's plan for this query is a single MixedAggregate over a single Seq
// Scan; goopg's is a single HashAggregate over a single Seq Scan.
func TestGroupingSetsPlanIsOneAggregateOverOneScan(t *testing.T) {
	ctx, cleanup := gsFixture(t)
	defer cleanup()

	plan := explainText(t, ctx, "SELECT dept, region, sum(amt) FROM t GROUP BY ROLLUP(dept, region)")
	if n := strings.Count(plan, "Seq Scan"); n != 1 {
		t.Fatalf("plan scans the source %d times, want 1:\n%s", n, plan)
	}
	if strings.Contains(plan, "Append") || strings.Contains(plan, "CTE") {
		t.Fatalf("plan still carries the retired UNION ALL / shared-source shape:\n%s", plan)
	}
	if !strings.Contains(plan, "3 grouping sets") {
		t.Fatalf("plan does not name the grouping sets:\n%s", plan)
	}
}

// TestGroupingSetsHavingOnGroupingFunc pins GROUPING(...) used as a HAVING
// predicate: it is one output column shared by the target list and HAVING, and
// the filter runs above the aggregate. PG 18.3 returns exactly the two detail
// levels (a,30) and (b,5).
func TestGroupingSetsHavingOnGroupingFunc(t *testing.T) {
	ctx, cleanup := gsFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx,
		"SELECT dept, sum(amt) FROM t GROUP BY ROLLUP(dept) HAVING GROUPING(dept) = 0 ORDER BY dept")
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2 (grand total filtered out): %+v", len(rows), rows)
	}
	if rows[0][0].StringValue() != "a" || rows[0][1].Int != 30 {
		t.Fatalf("row[0]=%+v want (a,30)", rows[0])
	}
	if rows[1][0].StringValue() != "b" || rows[1][1].Int != 5 {
		t.Fatalf("row[1]=%+v want (b,5)", rows[1])
	}
}

// TestGroupingSetsCrossProductWithPlainColumn pins `GROUP BY a, ROLLUP(b)` —
// the cross product of a plain grouping element with a construct. Every set
// keeps dept, so this is also the shape whose gset_common is non-empty.
// PG 18.3: (a,x,1) (a,y,1) (a,NULL,2) (b,x,1) (b,NULL,1).
func TestGroupingSetsCrossProductWithPlainColumn(t *testing.T) {
	ctx, cleanup := gsFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx,
		`SELECT dept, region, count(*) FROM t GROUP BY dept, ROLLUP(region)
		 ORDER BY dept, region NULLS LAST`)
	want := []struct {
		dept, region string
		nullRegion   bool
		count        int64
	}{
		{dept: "a", region: "x", count: 1},
		{dept: "a", region: "y", count: 1},
		{dept: "a", nullRegion: true, count: 2},
		{dept: "b", region: "x", count: 1},
		{dept: "b", nullRegion: true, count: 1},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i][0].IsNull() || rows[i][0].StringValue() != w.dept {
			t.Fatalf("row[%d] dept=%+v want %q", i, rows[i][0], w.dept)
		}
		if w.nullRegion {
			if !rows[i][1].IsNull() {
				t.Fatalf("row[%d] region=%+v want NULL", i, rows[i][1])
			}
		} else if rows[i][1].IsNull() || rows[i][1].StringValue() != w.region {
			t.Fatalf("row[%d] region=%+v want %q", i, rows[i][1], w.region)
		}
		if rows[i][2].Int != w.count {
			t.Fatalf("row[%d] count=%+v want %d", i, rows[i][2], w.count)
		}
	}
}
