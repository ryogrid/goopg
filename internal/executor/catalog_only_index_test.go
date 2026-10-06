package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestCatalogOnlyIndexIsNeverScanned pins M0146-0069. goopg registers gist,
// spgist, gin and brin indexes in the catalog only: they have no physical
// storage. The planner's bitmap, restriction, ordered and index-only path
// producers still offered scans of them, so whenever such a path won (here
// with enable_seqscan off) the query failed with `short read at block`. That
// happened in regress create_index (GIN) and stats (BRIN). PG scans these
// indexes; goopg must at least answer the query. Every want is PG 18.3's
// result.
func TestCatalogOnlyIndexIsNeverScanned(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE ge (i int4[])",
		"INSERT INTO ge VALUES ('{47,77}'), ('{1}'), (NULL)",
		"CREATE INDEX ON ge USING gin (i)",
		"CREATE TABLE br (a int, b text)",
		"INSERT INTO br SELECT g, 'x' || g FROM generate_series(1, 200) g",
		"CREATE INDEX br_a ON br USING brin (a)",
		"CREATE TABLE gs (p point, id int)",
		"INSERT INTO gs VALUES (point(1,1), 1), (point(5,5), 2)",
		"CREATE INDEX ON gs USING gist (p)",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableSeqScan = false
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ sql, want string }{
		{"SELECT * FROM ge WHERE i = '{47,77}'", "{47,77}"},
		{"SELECT count(*) FROM br WHERE a = 2", "1"},
		{"SELECT a FROM br WHERE a BETWEEN 10 AND 12 ORDER BY a", "10;11;12"},
		{"SELECT min(a), max(a) FROM br", "1|200"},
		{"SELECT id FROM gs WHERE p <@ box '((0,0),(2,2))'", "1"},
	} {
		stmts, err := parser.Parse(c.sql)
		if err != nil {
			t.Fatalf("parse %q: %v", c.sql, err)
		}
		plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if err != nil {
			t.Fatalf("plan %q: %v", c.sql, err)
		}
		for _, slab := range []bool{false, true} {
			advanceStmtCounter(ctx)
			var op Operator
			if slab {
				op, err = BuildFastIterator(plan)
			} else {
				op, err = Build(plan)
			}
			if err != nil {
				t.Fatalf("build %q: %v", c.sql, err)
			}
			rows, err := Run(op, ctx)
			if err != nil {
				t.Fatalf("run %q (slab=%v): %v", c.sql, slab, err)
			}
			if got := strings.Join(renderRows(rows), ";"); got != c.want {
				t.Errorf("%s (slab=%v)\ngot  %s\nwant PG's %s", c.sql, slab, got, c.want)
			}
		}
	}
}
