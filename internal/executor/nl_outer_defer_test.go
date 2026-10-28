package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestNLOuterDeferMatchesEager pins M0122-0012 C38: a streaming nested loop
// whose outer seq scan decodes only the columns the join predicate reads, and
// completes a row (finishOuterRow) before any other use, returns exactly the
// rows — order included — the eager decode returns. The outer table puts its
// predicate column first and last, carries NULLs, numeric, char(n) and toasted
// text columns, and most of its rows match nothing; the queries cover inner,
// left (null-extended outer rows), right, full, semi and anti nested loops,
// a predicate on a late outer column, a projection between the loop and its
// scan, a cross join (every pair matches: the scan disarms mid-scan), and
// emits under both a sort (cloning scan) and a borrowing aggregate.
func TestNLOuterDeferMatchesEager(t *testing.T) {
	arms := 0
	old := nlOuterDeferArmed
	nlOuterDeferArmed = func() { arms++ }
	t.Cleanup(func() { nlOuterDeferArmed = old; probeDeferDisabled = false })
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE nlo (k int, g char(1), m char(10), q numeric(10,2), s text, big text, last int)")
	runSQL(t, ctx, "INSERT INTO nlo SELECT CASE WHEN x % 89 = 0 THEN NULL ELSE x END, chr(65 + x % 3), 'm' || (x % 7), x * 0.5, 's' || x, CASE WHEN x % 701 = 0 THEN repeat('z', 9000) ELSE 'b' END, x % 50 FROM generate_series(1, 6000) x")
	runSQL(t, ctx, "CREATE TABLE nli (k int, w text, lo int)")
	runSQL(t, ctx, "INSERT INTO nli VALUES (17, 'a', 3), (4000, 'b', 7), (178, 'c', 11), (99999, 'd', 49), (NULL, 'e', 0)")
	runSQL(t, ctx, "CREATE TABLE nlall (k int, z text)")
	runSQL(t, ctx, "INSERT INTO nlall SELECT x, 'z' || x FROM generate_series(1, 3) x")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin = false
	ps.EnableMergeJoin = false
	ps.MaxParallelWorkersPerGather = 0
	queries := []string{
		"SELECT o.k, o.g, o.m, o.q, o.s, length(o.big), i.w FROM nlo o JOIN nli i ON o.k = i.k ORDER BY o.k",
		"SELECT o.g, o.m, count(*), sum(o.q) FROM nlo o JOIN nli i ON o.k = i.k GROUP BY 1, 2 ORDER BY 1, 2",
		"SELECT o.k, o.s, i.w FROM nlo o LEFT JOIN nli i ON o.k = i.k WHERE o.last < 3 ORDER BY o.k NULLS FIRST, o.s",
		"SELECT count(*), sum(o.q), count(i.w) FROM nlo o LEFT JOIN nli i ON o.k = i.k",
		"SELECT o.k, o.s, i.w FROM nlo o RIGHT JOIN nli i ON o.k = i.k ORDER BY i.w",
		"SELECT o.k, o.m, i.w FROM nlo o FULL JOIN nli i ON o.k = i.k AND o.last < 10 WHERE o.k IS NULL OR o.k < 200 ORDER BY o.k NULLS FIRST, i.w",
		"SELECT count(*), sum(o.q), max(o.s) FROM nlo o WHERE EXISTS (SELECT 1 FROM nli i WHERE i.lo = o.last)",
		"SELECT count(*), sum(o.q), max(o.m) FROM nlo o WHERE NOT EXISTS (SELECT 1 FROM nli i WHERE i.lo = o.last)",
		"SELECT o.k, o.g, o.q, i.w FROM nlo o JOIN nli i ON o.last = i.lo AND o.k < 300 ORDER BY o.k, i.w",
		"SELECT x.k, x.s, i.w FROM (SELECT k, s, q FROM nlo) x JOIN nli i ON x.k = i.k ORDER BY x.k",
		"SELECT count(*), sum(o.q), max(o.s), min(a.z) FROM nlo o CROSS JOIN nlall a",
		"SELECT o.k, a.z, o.m FROM nlo o JOIN nlall a ON o.k > a.k ORDER BY o.k DESC, a.z LIMIT 25",
	}
	for _, q := range queries {
		before := arms
		got := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		n := arms - before
		probeDeferDisabled = true
		want := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		probeDeferDisabled = false
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s (armed %d): deferred outer differs from eager\ndeferred %v\neager    %v", q, n, got, want)
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q)
		}
		t.Logf("armed=%d rows=%d %s", n, len(got), q)
	}
	if arms == 0 {
		t.Fatal("no nested loop armed the outer deferral")
	}
}
