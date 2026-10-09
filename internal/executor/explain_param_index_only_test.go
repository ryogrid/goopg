package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestParameterisedProbeIsIndexOnly pins M0146-0005bq against PG 18.3:
// build_index_paths builds ONE path per index, index-only whenever
// check_index_only finds the index covers every column the query reads from
// the rel — parameterised or not; the visibility map only prices it. PG plans
//
//	Aggregate
//	  ->  Nested Loop
//	        ->  Seq Scan on o  Filter: (b = 7)
//	        ->  Index Only Scan using p_pkey on p
//	              Index Cond: (id = o.a)
//
// count 20, sum(p.id) = sum(o.a) = 356680. goopg built only a plain
// parameterised probe. Bitmap scans are disabled here because goopg's index
// probe still prices above its bitmap twin on this fixture (the parked
// M0142-0005c multiplier), which is a separate divergence.
func TestParameterisedProbeIsIndexOnly(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE o (a int, b int)")
	runSQL(t, ctx, "CREATE TABLE p (id int PRIMARY KEY, x int)")
	runSQL(t, ctx, "INSERT INTO o SELECT i*37, i % 50 FROM generate_series(1,1000) i")
	runSQL(t, ctx, "INSERT INTO p SELECT i, i FROM generate_series(1,100000) i")
	runSQL(t, ctx, "ANALYZE o")
	runSQL(t, ctx, "ANALYZE p")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableBitmapScan = false
	ps.MaxParallelWorkersPerGather = 0
	const q = "SELECT count(*), sum(p.id), sum(o.a) FROM o JOIN p ON p.id = o.a WHERE o.b = 7"

	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Nested Loop") || !strings.Contains(joined, "Index Only Scan using p_pkey on p") {
		t.Fatalf("want PG's parameterised Index Only Scan probe:\n%s", joined)
	}
	rows := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
	if len(rows) != 1 || rows[0] != "20,356680,356680" {
		t.Fatalf("rows = %v, want [20,356680,356680]", rows)
	}
}

// TestIndexOnlyProbePerAlias pins M0146-0005bq-b against PG 18.3: whether an
// index covers a relation is asked of THAT relation's needed columns. p read
// as p1 needs `x`, read as p2 needs only `id`, so PG plans
//
//	->  Index Scan using p_pkey on p p1       Index Cond: (id = o.a)
//	->  Index Only Scan using p_pkey on p p2  Index Cond: (id = (o.a + 1))
//
// count 20, sum(p1.x) 356680. goopg's name-matched needed set counted p1's
// `x` for p2 too (TPC-DS Q18's cd2, Q50's d1).
func TestIndexOnlyProbePerAlias(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE o (a int, b int)")
	runSQL(t, ctx, "CREATE TABLE p (id int PRIMARY KEY, x int)")
	runSQL(t, ctx, "INSERT INTO o SELECT i*37, i % 50 FROM generate_series(1,1000) i")
	runSQL(t, ctx, "INSERT INTO p SELECT i, i FROM generate_series(1,100000) i")
	runSQL(t, ctx, "ANALYZE o")
	runSQL(t, ctx, "ANALYZE p")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableBitmapScan = false
	ps.MaxParallelWorkersPerGather = 0
	const q = "SELECT count(*), sum(p1.x) FROM o JOIN p p1 ON p1.id = o.a JOIN p p2 ON p2.id = o.a + 1 WHERE o.b = 7"

	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Index Only Scan using p_pkey on p p2") ||
		!strings.Contains(joined, "Index Scan using p_pkey on p p1") {
		t.Fatalf("want p2 index-only and p1 a plain probe:\n%s", joined)
	}
	rows := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
	if len(rows) != 1 || rows[0] != "20,356680" {
		t.Fatalf("rows = %v, want [20,356680]", rows)
	}
}

// TestExistsStarProbeIsIndexOnly pins M0146-0005bs against PG 18.3: an
// EXISTS reads no column through its SELECT list (simplify_EXISTS_query
// discards it), so `NOT EXISTS (SELECT * FROM p WHERE p.id = ...)` probes
// p_pkey index-only:
//
//	->  Nested Loop Anti Join
//	      ->  Seq Scan on o  Filter: (b = 7)
//	      ->  Index Only Scan using p_pkey on p  Index Cond: (id = (o.a * 200))
//
// count 19. goopg's needed-column collector declined the whole statement on
// the star.
func TestExistsStarProbeIsIndexOnly(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE o (a int, b int)")
	runSQL(t, ctx, "CREATE TABLE p (id int PRIMARY KEY, x int)")
	runSQL(t, ctx, "INSERT INTO o SELECT i*37, i % 50 FROM generate_series(1,1000) i")
	runSQL(t, ctx, "INSERT INTO p SELECT i, i FROM generate_series(1,100000) i")
	runSQL(t, ctx, "ANALYZE o")
	runSQL(t, ctx, "ANALYZE p")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableBitmapScan = false
	ps.MaxParallelWorkersPerGather = 0
	const q = "SELECT count(*) FROM o WHERE o.b = 7 AND NOT EXISTS (SELECT * FROM p WHERE p.id = o.a * 200)"

	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Anti Join") || !strings.Contains(joined, "Index Only Scan using p_pkey on p") {
		t.Fatalf("want PG's index-only anti-join probe:\n%s", joined)
	}
	rows := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
	if len(rows) != 1 || rows[0] != "19" {
		t.Fatalf("rows = %v, want [19]", rows)
	}
}

// TestIndexOnlySkipProbe pins M0146-0005bt against PG 18.3: a parameterised
// probe binding only the SECOND key of q_pkey(k1, k2) is a skip scan, and it
// is index-only when the index covers the rel (TPC-DS Q94's `wr1`):
//
//	->  Index Only Scan using q_pkey on q  Index Cond: (k2 = (o.a * 200))  -- anti: count 19
//	->  Index Only Scan using q_pkey on q  Index Cond: (k2 = (o.a * 3))    -- inner: count 18, sum(k1) 36
//
// The inner join reads k1 back out of the skipped key column.
func TestIndexOnlySkipProbe(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE o (a int, b int)")
	runSQL(t, ctx, "CREATE TABLE q (k1 int, k2 int, v int, PRIMARY KEY (k1, k2))")
	runSQL(t, ctx, "INSERT INTO o SELECT i*37, i % 50 FROM generate_series(1,1000) i")
	runSQL(t, ctx, "INSERT INTO q SELECT i % 5, i, i FROM generate_series(1,100000) i")
	runSQL(t, ctx, "ANALYZE o")
	runSQL(t, ctx, "ANALYZE q")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableBitmapScan = false
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct {
		q, cond, rows string
	}{
		{"SELECT count(*) FROM o WHERE o.b = 7 AND NOT EXISTS (SELECT * FROM q WHERE q.k2 = o.a * 200)",
			"Index Cond: (k2 = (o.a * 200))", "19"},
		{"SELECT count(*), sum(q.k1) FROM o JOIN q ON q.k2 = o.a * 3 WHERE o.b = 7",
			"Index Cond: (k2 = (o.a * 3))", "18,36"},
	} {
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN "+c.q, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "Index Only Scan using q_pkey on q") || !strings.Contains(joined, c.cond) {
			t.Errorf("%s\nwant PG's index-only skip probe %q:\n%s", c.q, c.cond, joined)
		}
		rows := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, c.q, ps)))
		if len(rows) != 1 || rows[0] != c.rows {
			t.Errorf("%s: rows = %v, want [%s]", c.q, rows, c.rows)
		}
	}
}
