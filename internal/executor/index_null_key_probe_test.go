package executor

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// Index scans must not lose rows whose index key has a NULL column.
//
// goopg's btree stores no entry whose key has a NULL column (see
// collectBTreeEntries in operators_ddl.go and indexBuildEntryKey), while
// PostgreSQL stores them (index tuples carry a null bitmap). Until goopg stores
// them too, no scan may use an index in a way that relies on those entries: a
// probe that leaves a nullable key column unbound, or a full ordered scan over
// a nullable key column, would silently skip every row with a NULL there.
// Filed as the M-NIGHTLY WRONG RESULTS item "an index-only prefix probe on a
// composite index skips entries whose trailing key column is NULL".
//
// Each case runs twice: as written, where the planner may use r2_ab, and with
// the indexed column hidden behind `+ 0`, where it cannot. The answers must be
// the same multiset of rows.
func TestIndexProbesKeepNullKeyedRows(t *testing.T) {
	// Both capability states: off, the planner guard must keep the unsafe
	// indexes out; on (store-null-keys S3), the entries exist and the guard
	// lets the indexes back in — the answers must be the same either way.
	for _, capable := range []bool{false, true} {
		t.Run(fmt.Sprintf("capable=%v", capable), func(t *testing.T) {
			catalog.SetNullKeyedIndexEntries(capable)
			defer catalog.SetNullKeyedIndexEntries(false)
			indexProbesKeepNullKeyedRows(t)
		})
	}
}

func indexProbesKeepNullKeyedRows(t *testing.T) {
	ctx, cleanup := newVMFixture(t)

	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE r2 (a int, b int, c int)")
	runSQL(t, ctx, "CREATE INDEX r2_ab ON r2 (a, b)")
	runSQL(t, ctx, "INSERT INTO r2 SELECT g / 3, g % 5, g FROM generate_series(1, 5000) g")
	runSQL(t, ctx, "INSERT INTO r2 VALUES (10, NULL, -1), (11, NULL, -2), (NULL, 3, -3), (NULL, NULL, -4)")
	runSQL(t, ctx, "CREATE TABLE r2o (x int)")
	runSQL(t, ctx, "INSERT INTO r2o VALUES (10), (11), (12)")
	runSQL(t, ctx, "VACUUM r2")
	runSQL(t, ctx, "ANALYZE r2")
	runSQL(t, ctx, "ANALYZE r2o")

	prefixPlan := strings.Join(runExplainRows(t, ctx, "EXPLAIN SELECT a, b, c FROM r2 WHERE a = 10"), "\n")
	if usesIdx := strings.Contains(prefixPlan, "r2_ab"); usesIdx != catalog.NullKeyedIndexEntries() {
		t.Errorf("capable=%v: prefix probe uses r2_ab = %v, want %v (the NULL-key guard):\n%s",
			catalog.NullKeyedIndexEntries(), usesIdx, catalog.NullKeyedIndexEntries(), prefixPlan)
	}
	for _, tc := range []struct{ name, indexed, noIndex string }{
		{"prefix-eq", "SELECT a, b, c FROM r2 WHERE a = 10", "SELECT a, b, c FROM r2 WHERE a + 0 = 10"},
		{"prefix-eq-index-only", "SELECT a FROM r2 WHERE a = 10", "SELECT a FROM r2 WHERE a + 0 = 10"},
		{"prefix-count", "SELECT count(*) FROM r2 WHERE a = 10", "SELECT count(*) FROM r2 WHERE a + 0 = 10"},
		{"prefix-in", "SELECT a, b, c FROM r2 WHERE a IN (10, 11)", "SELECT a, b, c FROM r2 WHERE a + 0 IN (10, 11)"},
		{"prefix-range", "SELECT a, b, c FROM r2 WHERE a BETWEEN 10 AND 11", "SELECT a, b, c FROM r2 WHERE a + 0 BETWEEN 10 AND 11"},
		{"trailing-is-null", "SELECT a, b, c FROM r2 WHERE a = 10 AND b IS NULL", "SELECT a, b, c FROM r2 WHERE a + 0 = 10 AND b IS NULL"},
		{"prefix-or", "SELECT a, b, c FROM r2 WHERE a = 10 OR a = 11", "SELECT a, b, c FROM r2 WHERE a + 0 = 10 OR a + 0 = 11"},
		{"ordered-limit", "SELECT a, b, c FROM (SELECT a, b, c FROM r2 ORDER BY a, b LIMIT 5000) s WHERE a >= 1600 OR a IS NULL", "SELECT a, b, c FROM (SELECT a, b, c FROM r2 ORDER BY a + 0, b + 0 LIMIT 5000) s WHERE a >= 1600 OR a IS NULL"},
		{"nested-loop-probe", "SELECT o.x, r.b, r.c FROM r2o o JOIN r2 r ON r.a = o.x", "SELECT o.x, r.b, r.c FROM r2o o JOIN r2 r ON r.a + 0 = o.x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sortedRowStrings(t, ctx, tc.indexed)
			want := sortedRowStrings(t, ctx, tc.noIndex)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("%s:\n  indexed  %d rows %v\n  no index %d rows %v\nplan:\n%s", tc.indexed,
					len(got), got, len(want), want, strings.Join(runExplainRows(t, ctx, "EXPLAIN "+tc.indexed), "\n"))
			}
		})
	}
}

func sortedRowStrings(t *testing.T, ctx *Context, sql string) []string {
	t.Helper()
	var out []string
	for _, r := range runQuery(t, ctx, sql) {
		cells := make([]string, len(r))
		for i, d := range r {
			if d.IsNull() {
				cells[i] = "NULL"
			} else {
				cells[i] = fmt.Sprint(d.Int)
			}
		}
		out = append(out, strings.Join(cells, ","))
	}
	sort.Strings(out)
	return out
}

// TestIndexFullScansKeepNullKeyedRows: the full-scan index producers (an
// ORDER BY served by index order, GROUP BY over index order, an ordered
// merge-join input) read the whole index, so a NULL in ANY key column drops
// the row. Each case runs under settings that favour the index shape, and is
// compared with the same statement over a copy with no index.
func TestIndexFullScansKeepNullKeyedRows(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(fmt.Sprintf("capable=%v", capable), func(t *testing.T) {
			catalog.SetNullKeyedIndexEntries(capable)
			defer catalog.SetNullKeyedIndexEntries(false)
			indexFullScansKeepNullKeyedRows(t)
		})
	}
}

func indexFullScansKeepNullKeyedRows(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, tbl := range []string{"f1", "f1_noidx"} {
		runSQL(t, ctx, "CREATE TABLE "+tbl+" (a int, b int)")
		runSQL(t, ctx, "INSERT INTO "+tbl+" SELECT g % 50, g FROM generate_series(1, 2000) g")
		runSQL(t, ctx, "INSERT INTO "+tbl+" VALUES (NULL, -1), (NULL, -2)")
	}
	runSQL(t, ctx, "CREATE INDEX f1_a ON f1 (a)")
	runSQL(t, ctx, "CREATE TABLE f2 (a int)")
	runSQL(t, ctx, "INSERT INTO f2 SELECT g FROM generate_series(1, 10) g")
	runSQL(t, ctx, "VACUUM f1")
	runSQL(t, ctx, "ANALYZE f1")
	runSQL(t, ctx, "ANALYZE f1_noidx")
	runSQL(t, ctx, "ANALYZE f2")
	for _, tc := range []struct {
		name string
		set  func(*optimizer.PlannerSettings)
		q    string
	}{
		{"seqscan-off-order-by", func(ps *optimizer.PlannerSettings) { ps.EnableSeqScan = false }, "SELECT a FROM %s ORDER BY a"},
		{"group-by-index-order", func(ps *optimizer.PlannerSettings) { ps.EnableHashAgg = false; ps.EnableSeqScan = false }, "SELECT a, count(*) FROM %s GROUP BY a"},
		{"merge-left-join", func(ps *optimizer.PlannerSettings) {
			ps.EnableHashJoin = false
			ps.EnableNestLoop = false
			ps.EnableSeqScan = false
		}, "SELECT x.a, x.b, f2.a FROM %s x LEFT JOIN f2 ON x.a = f2.a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(sql string) ([]string, optimizer.Node) {
				advanceStmtCounter(ctx)
				stmts, err := parser.Parse(sql)
				if err != nil {
					t.Fatalf("parse: %v", err)
				}
				ps := optimizer.DefaultPlannerSettings()
				tc.set(&ps)
				node, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
				if err != nil {
					t.Fatalf("plan %q: %v", sql, err)
				}
				rows := drainPlan(t, ctx, node)
				sort.Strings(rows)
				return rows, node
			}
			got, node := run(fmt.Sprintf(tc.q, "f1"))
			want, _ := run(fmt.Sprintf(tc.q, "f1_noidx"))
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("%s: indexed %d rows, no-index %d rows; plan root %T (%s)", tc.q, len(got), len(want), node, planTypes(node))
			}
		})
	}
}

// planTypes lists a plan's node types, depth first, for failure messages.
func planTypes(n optimizer.Node) string {
	var parts []string
	var walk func(optimizer.Node)
	walk = func(n optimizer.Node) {
		if n == nil {
			return
		}
		parts = append(parts, fmt.Sprintf("%T", n))
		for _, c := range planChildren(n) {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(parts, " > ")
}
