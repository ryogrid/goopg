package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestUniqueifiedSemiInnerRows (M0146-0005dk): an IN / EXISTS whose RHS is
// unique-ified (PG's JOIN_UNIQUE_INNER over UNIQUE_PATH_HASH or _SORT) must
// return exactly the semi join's rows — every outer row once, however many
// RHS duplicates match it, and none for a NULL key. The RHS here repeats
// each key 20 times and carries NULLs; the plain-join reference counts what
// a semi join must return.
func TestUniqueifiedSemiInnerRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE us_outer (k int, v int)")
	runSQL(t, ctx, "CREATE TABLE us_inner (k int, w int)")
	runSQL(t, ctx, "INSERT INTO us_outer SELECT CASE WHEN g % 50 = 0 THEN NULL ELSE g % 400 END, g FROM generate_series(1, 4000) g")
	runSQL(t, ctx, "INSERT INTO us_inner SELECT CASE WHEN g % 37 = 0 THEN NULL ELSE g % 150 END, g FROM generate_series(1, 3000) g")
	want := renderRows(runSQL(t, ctx,
		"SELECT count(*), sum(v) FROM us_outer o WHERE o.k IN (SELECT DISTINCT k FROM us_inner WHERE k IS NOT NULL)"))
	for _, sql := range []string{
		"SELECT count(*), sum(v) FROM us_outer o WHERE o.k IN (SELECT i.k FROM us_inner i)",
		"SELECT count(*), sum(v) FROM us_outer o WHERE EXISTS (SELECT 1 FROM us_inner i WHERE i.k = o.k)",
	} {
		plan := explainText(t, ctx, sql)
		if !strings.Contains(plan, "HashAggregate") && !strings.Contains(plan, "Unique") && !strings.Contains(plan, "Semi Join") {
			t.Fatalf("%s: unexpected plan:\n%s", sql, plan)
		}
		got := renderRows(runSQL(t, ctx, sql))
		if strings.Join(got, ";") != strings.Join(want, ";") {
			t.Fatalf("%s: got %v, want %v\nplan:\n%s", sql, got, want, plan)
		}
		t.Logf("%s\n%s", sql, plan)
	}
}

// TestUniqueifiedSemiJoinrelRows (M0146-0005dy): a semi join whose RHS is two
// relations is unique-ified as a joinrel (PG's create_unique_path on a rel
// equal to syn_righthand), and a unique-ified side must reach every join
// arm. The merge arms read their inputs from the rels' path lists, not
// through nestLoopOuterPaths, so before the fix a forced merge join drove
// from the raw RHS and joined every duplicate (3625 rows where PG returns
// 725 for the single-rel case). Each arm is forced in turn; all must return
// the semi join's rows.
func TestUniqueifiedSemiJoinrelRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE ud_cu (ck int PRIMARY KEY, v int)",
		"CREATE TABLE ud_ss (ck int, dk int, w int)",
		"CREATE TABLE ud_dd (dk int PRIMARY KEY, yr int)",
		"INSERT INTO ud_cu SELECT g, g % 97 FROM generate_series(1, 5000) g",
		"INSERT INTO ud_ss SELECT (g * 31) % 6000 + 1, g % 400 + 1, g FROM generate_series(1, 30000) g",
		"INSERT INTO ud_dd SELECT g, 1998 + g % 5 FROM generate_series(1, 400) g",
		"ANALYZE ud_cu",
		"ANALYZE ud_ss",
		"ANALYZE ud_dd",
	} {
		runSQL(t, ctx, q)
	}
	cases := []struct{ sql, ref string }{
		{
			"SELECT count(*), sum(v) FROM ud_cu c WHERE c.ck IN (SELECT ck FROM ud_ss WHERE dk < 30)",
			"SELECT count(*), sum(v) FROM ud_cu c WHERE c.ck IN (SELECT DISTINCT ck FROM ud_ss WHERE dk < 30 AND ck IS NOT NULL)",
		},
		{
			"SELECT count(*), sum(v) FROM ud_cu c WHERE EXISTS (SELECT 1 FROM ud_ss s, ud_dd d WHERE c.ck = s.ck AND s.dk = d.dk AND d.yr = 2001 AND d.dk < 30)",
			"SELECT count(*), sum(v) FROM ud_cu c WHERE c.ck IN (SELECT DISTINCT s.ck FROM ud_ss s JOIN ud_dd d ON s.dk = d.dk WHERE d.yr = 2001 AND d.dk < 30 AND s.ck IS NOT NULL)",
		},
	}
	arms := []struct {
		name           string
		hash, nestloop bool
	}{
		{"default", true, true},
		{"enable_hashjoin=off", false, true},
		{"enable_nestloop=off", true, false},
		{"merge only", false, false},
	}
	for _, c := range cases {
		want := renderRows(runSQL(t, ctx, c.ref))
		for _, arm := range arms {
			ps := optimizer.DefaultPlannerSettings()
			ps.EnableHashJoin, ps.EnableNestLoop = arm.hash, arm.nestloop
			if got := renderRows(runSQLWith(t, ctx, c.sql, ps)); strings.Join(got, ";") != strings.Join(want, ";") {
				t.Fatalf("[%s] %s: got %v, want %v\nplan:\n%s", arm.name, c.sql, got, want,
					strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN "+c.sql, ps)), "\n"))
			}
		}
	}
	// PG 18.3, enable_hashjoin = off, same data: Nested Loop over
	// HashAggregate (Group Key: s.ck) over the s ⋈ d join, probing
	// ud_cu_pkey — the two-relation RHS unique-ified as one joinrel.
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin = false
	lines := renderRows(runSQLWith(t, ctx, "EXPLAIN "+cases[1].sql, ps))
	agg := -1
	for i, l := range lines {
		if strings.Contains(l, "Group Key: s.ck") {
			agg = i
		}
	}
	if agg < 1 || agg+1 >= len(lines) || !strings.Contains(lines[agg-1], "HashAggregate") ||
		!strings.Contains(lines[agg+1], "Nested Loop") || !strings.Contains(strings.Join(lines, "\n"), "Index Cond: (ck = s.ck)") {
		t.Fatalf("want PG's unique-ified s ⋈ d probing ud_cu_pkey:\n%s", strings.Join(lines, "\n"))
	}
}
