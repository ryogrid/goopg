package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// hashNodeFixture loads two tables whose equi-join plans as a hash join
// building on h2.
func hashNodeFixture(t *testing.T) (*Context, optimizer.PlannerSettings) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE h1 (a int, b int)")
	runSQL(t, ctx, "CREATE TABLE h2 (a int, c int)")
	runSQL(t, ctx, "INSERT INTO h1 SELECT i, i % 100 FROM generate_series(1,20000) i")
	runSQL(t, ctx, "INSERT INTO h2 SELECT i, i FROM generate_series(1,5000) i")
	runSQL(t, ctx, "ANALYZE h1")
	runSQL(t, ctx, "ANALYZE h2")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	return ctx, ps
}

func explainLines(t *testing.T, ctx *Context, ps optimizer.PlannerSettings, q string) []string {
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, r[0].StringValue())
		}
	}
	return lines
}

// TestHashJoinPrintsHashNode pins M0146-0005ck against PG 18.3:
// create_hashjoin_plan puts a Hash node over the build input, so EXPLAIN
// prints `->  Hash` between the join and the inner scan. goopg's join
// operator builds the table itself and printed the scan directly under the
// join. The want block is PG's own COSTS OFF output, indentation included.
func TestHashJoinPrintsHashNode(t *testing.T) {
	ctx, ps := hashNodeFixture(t)
	const q = "SELECT count(*) FROM h1 JOIN h2 ON h1.a = h2.a WHERE h2.c > 10"
	want := strings.Join([]string{
		"Aggregate",
		"  ->  Hash Join",
		"        Hash Cond: (h1.a = h2.a)",
		"        ->  Seq Scan on h1",
		"        ->  Hash",
		"              ->  Seq Scan on h2",
		"                    Filter: (c > 10)",
	}, "\n")
	if got := strings.Join(explainLines(t, ctx, ps, "EXPLAIN (COSTS OFF) "+q), "\n"); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	// The Hash node's cost is its input's total at both ends.
	var hashLine, scanLine string
	for _, l := range explainLines(t, ctx, ps, "EXPLAIN "+q) {
		switch {
		case strings.Contains(l, "->  Hash  ("):
			hashLine = l
		case strings.Contains(l, "Seq Scan on h2"):
			scanLine = l
		}
	}
	total := scanLine[strings.Index(scanLine, "..")+2 : strings.Index(scanLine, " rows=")]
	if !strings.Contains(hashLine, "(cost="+total+".."+total+" ") {
		t.Fatalf("Hash cost should be the input's total %s at both ends:\n%s\n%s", total, hashLine, scanLine)
	}

	// ANALYZE prints the hash table's Buckets line under the Hash node.
	al := explainLines(t, ctx, ps, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) "+q)
	for i, l := range al {
		if strings.Contains(l, "->  Hash (") {
			if i+1 >= len(al) || !strings.HasPrefix(strings.TrimSpace(al[i+1]), "Buckets:") {
				t.Fatalf("want Buckets line under the Hash node:\n%s", strings.Join(al, "\n"))
			}
			return
		}
	}
	t.Fatalf("no Hash node under ANALYZE:\n%s", strings.Join(al, "\n"))
}

// TestHashJoinJSONPrintsHashNode pins the FORMAT JSON twin: the build input
// sits under a "Hash" plan object.
func TestHashJoinJSONPrintsHashNode(t *testing.T) {
	ctx, ps := hashNodeFixture(t)
	got := strings.Join(explainLines(t, ctx, ps,
		"EXPLAIN (FORMAT JSON, COSTS OFF) SELECT count(*) FROM h1 JOIN h2 ON h1.a = h2.a"), "\n")
	i := strings.Index(got, `"Node Type": "Hash"`)
	j := strings.Index(got, `"Node Type": "Seq Scan on h2"`)
	if i < 0 || j < i {
		t.Fatalf("want a Hash object wrapping the h2 scan:\n%s", got)
	}
}
