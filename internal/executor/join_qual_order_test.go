package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestJoinFilterOrderedByQualCost pins M0146-0013: every create_*join_plan
// runs order_qual_clauses over its join quals, a stable sort by
// cost_qual_eval's per-tuple cost, so the one-operator `a.z < b.z` is
// evaluated (and printed) before the two-operator OR written ahead of it.
// goopg kept the restriction list's written order. Plan and value are PG
// 18.3's.
func TestJoinFilterOrderedByQualCost(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE oq_a (k int, x int, z int)",
		"CREATE TABLE oq_b (k int, y int, z int)",
		"INSERT INTO oq_a SELECT g % 50, g % 7, g % 13 FROM generate_series(1, 2000) g",
		"INSERT INTO oq_b SELECT g % 50, g % 5, g % 11 FROM generate_series(1, 1000) g",
		"ANALYZE oq_a",
		"ANALYZE oq_b",
	} {
		runSQL(t, ctx, q)
	}
	const q = `SELECT count(*) FROM oq_a a JOIN oq_b b ON a.k = b.k AND (a.x = 1 OR b.y = 2) AND a.z < b.z`
	plan := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+q))
	if countLinesContaining(plan, "Join Filter: ((a.z < b.z) AND ((a.x = 1) OR (b.y = 2)))") != 1 {
		t.Errorf("want PG's cost-ordered Join Filter:\n%s", strings.Join(plan, "\n"))
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, q)), ";"); got != "4840" {
		t.Errorf("rows %q, want PG's 4840", got)
	}
}

// TestJoinFilterECClausesLast pins M0146-0042b against PG 18.3:
// build_joinrel_restrictlist (relnode.c) lists a joinrel's joininfo clauses
// first and generate_join_implied_equalities' equivalence-class equalities
// after them, and order_qual_clauses' stable cost sort keeps that order among
// equal-cost quals. So a one-operator `jb1.x < jb2.y` prints before the EC
// equality whatever the written order, while a dearer `(x + y) > 5` still
// sorts after it.
func TestJoinFilterECClausesLast(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE jb1 (a int, x int)",
		"CREATE TABLE jb2 (b int, y int)",
		"INSERT INTO jb1 SELECT g, g FROM generate_series(1, 1000) g",
		"INSERT INTO jb2 SELECT g, g FROM generate_series(1, 1000) g",
		"ANALYZE jb1",
		"ANALYZE jb2",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashJoin = false
	ps.EnableMergeJoin = false
	for _, c := range []struct{ query, want string }{
		{"select * from jb1, jb2 where jb1.a = jb2.b and jb1.x < jb2.y", "Join Filter: ((jb1.x < jb2.y) AND (jb1.a = jb2.b))"},
		{"select * from jb1, jb2 where jb1.x < jb2.y and jb1.a = jb2.b", "Join Filter: ((jb1.x < jb2.y) AND (jb1.a = jb2.b))"},
		{"select * from jb1 join jb2 on jb1.a = jb2.b and jb1.x + jb2.y > 5", "Join Filter: ((jb1.a = jb2.b) AND ((jb1.x + jb2.y) > 5))"},
	} {
		got := strings.Join(explainLines(t, ctx, ps, "EXPLAIN (COSTS OFF) "+c.query), "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: want PG's %q in:\n%s", c.query, c.want, got)
		}
	}
}

// TestJoinKeyCondShowsVarcharRelabel pins M0146-0042 (Merge/Hash Cond text)
// against PG 18.3: varchar has no `=` of its own, so make_op compares two
// varchar keys as text and EXPLAIN shows each side's RelabelType, exactly as
// a Join Filter does; a char(n) key keeps bpchar's operator and prints bare.
// goopg printed the key pair without the casts (TPC-DS Q47/Q57 Merge Cond).
func TestJoinKeyCondShowsVarcharRelabel(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE vc1 (k varchar(20), c char(10), t text, v int)",
		"CREATE TABLE vc2 (k varchar(20), c char(10), t text, v int)",
		"INSERT INTO vc1 SELECT 'k' || g, 'c' || g, 't' || g, g FROM generate_series(1, 2000) g",
		"INSERT INTO vc2 SELECT 'k' || g, 'c' || g, 't' || g, g FROM generate_series(1, 2000) g",
		"ANALYZE vc1",
		"ANALYZE vc2",
	} {
		runSQL(t, ctx, q)
	}
	merge := optimizer.DefaultPlannerSettings()
	merge.MaxParallelWorkersPerGather = 0
	merge.EnableNestLoop = false
	merge.EnableHashJoin = false
	got := strings.Join(explainLines(t, ctx, merge, "EXPLAIN (COSTS OFF) select count(*) from vc1 join vc2 on vc1.k = vc2.k and vc1.c = vc2.c"), "\n")
	if want := "Merge Cond: (((vc1.k)::text = (vc2.k)::text) AND (vc1.c = vc2.c))"; !strings.Contains(got, want) {
		t.Errorf("want PG's %q in:\n%s", want, got)
	}
	hash := optimizer.DefaultPlannerSettings()
	hash.MaxParallelWorkersPerGather = 0
	hash.EnableNestLoop = false
	hash.EnableMergeJoin = false
	got = strings.Join(explainLines(t, ctx, hash, "EXPLAIN (COSTS OFF) select count(*) from vc1 join vc2 on vc1.k = vc2.k"), "\n")
	if !strings.Contains(got, "Hash Cond: ((vc2.k)::text = (vc1.k)::text)") && !strings.Contains(got, "Hash Cond: ((vc1.k)::text = (vc2.k)::text)") {
		t.Errorf("want the varchar Hash Cond relabelled to text in:\n%s", got)
	}
}
