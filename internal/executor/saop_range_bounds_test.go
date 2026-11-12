package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestSAOPRangeBoundsExactAndElementOrder pins M0148-0001c3a4 (c) against
// PG 18.3: a bound behind a leading ScalarArrayOp is an exact index qual —
// each element's descent is bounded by indexRangeBounds (the DESC swap and
// the NULL-group stop), so no Filter recheck remains — and the elements
// descend in index order, each once (_bt_sort_array_elements), so the
// index ordering the path claims holds for an unsorted, duplicated list.
// Rows are compared with a twin whose `+ 0` keeps the index out.
func TestSAOPRangeBoundsExactAndElementOrder(t *testing.T) {
	for _, capable := range []bool{false, true} {
		catalog.SetNullKeyedIndexEntries(capable)
		ctx, cleanup := newVMFixture(t)
		runSQL(t, ctx, "CREATE TABLE sr (thousand int NOT NULL, tenthous int, pad text)")
		runSQL(t, ctx, "INSERT INTO sr SELECT g%1000, CASE WHEN g%7=0 THEN NULL ELSE g END, repeat('x',30) FROM generate_series(0, 9999) g")
		ps := optimizer.DefaultPlannerSettings()
		ps.EnableBitmapScan = false
		for _, ddl := range []string{"CREATE INDEX sr_i ON sr (thousand, tenthous)",
			"CREATE INDEX sr_i ON sr (thousand, tenthous DESC)",
			"CREATE INDEX sr_i ON sr (thousand DESC, tenthous)"} {
			runSQL(t, ctx, "DROP INDEX IF EXISTS sr_i")
			runSQL(t, ctx, ddl)
			runSQL(t, ctx, "ANALYZE sr")
			if ddl == "CREATE INDEX sr_i ON sr (thousand, tenthous)" {
				q := "EXPLAIN (COSTS OFF) SELECT thousand, tenthous, pad FROM sr WHERE thousand IN (182, 183) AND tenthous > 7550 ORDER BY 1,2"
				want := "Index Scan using sr_i on sr\n  Index Cond: ((thousand = ANY ('{182,183}'::integer[])) AND (tenthous > 7550))"
				if got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n"); got != want {
					t.Errorf("capable=%v %s: plan\n%s\nwant (PG 18.3):\n%s", capable, ddl, got, want)
				}
			}
			for _, w := range []string{
				"thousand IN (182, 183) AND tenthous > 7550",
				"thousand IN (182, 183, 5) AND tenthous >= 8005",
				"thousand IN (183, 182) AND tenthous < 3183",
				"thousand IN (182, 183) AND tenthous <= 3182",
				"thousand IN (182, 183, 182) AND tenthous BETWEEN 2183 AND 7182",
				"thousand IN (182, NULL, 183) AND tenthous > 5000",
				"thousand IN (182, 183) AND tenthous > NULL",
				"thousand IN (999, 5, 182, 5, 183)",
			} {
				q := "SELECT thousand, tenthous FROM sr WHERE " + w + " ORDER BY 1, 2 LIMIT 7"
				twin := "SELECT thousand, tenthous FROM sr WHERE " + strings.ReplaceAll(strings.ReplaceAll(w, "thousand IN", "thousand + 0 IN"), "tenthous ", "tenthous + 0 ") + " ORDER BY 1, 2 LIMIT 7"
				got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), ";")
				want := strings.Join(renderRows(runSQLWith(t, ctx, twin, ps)), ";")
				if got != want {
					t.Errorf("capable=%v %s: %s\n got %s\nwant %s", capable, ddl, w, got, want)
				}
				if plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n"); strings.Contains(plan, "Filter: (tenthous") {
					t.Errorf("capable=%v %s: %s keeps the bound as a Filter recheck:\n%s", capable, ddl, w, plan)
				}
			}
		}
		cleanup()
	}
	catalog.SetNullKeyedIndexEntries(false)
}
