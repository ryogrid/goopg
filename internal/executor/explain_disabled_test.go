package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0148-0001f: PG 18's ExplainNode prints `Disabled: true` right after a
// node's line, in TEXT format, when plan_is_disabled — the node's own
// disabled_nodes exceed its children's sum, i.e. an enable_* GUC turned THIS
// node off. Non-text formats always carry the "Disabled" property. A Sort
// over a disabled Seq Scan is not itself disabled (enable_sort on), and the
// Incremental Sort cost_incremental_sort builds never counts enable_sort.
func TestExplainDisabledNodes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE dn (a int, b int)")
	render := func(q string, ps optimizer.PlannerSettings) string {
		return strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n")
	}
	noSeq := optimizer.DefaultPlannerSettings()
	noSeq.EnableSeqScan = false
	if got, want := render("EXPLAIN (COSTS OFF) SELECT * FROM dn ORDER BY a", noSeq), strings.Join([]string{
		"Sort",
		"  Sort Key: a",
		"  ->  Seq Scan on dn",
		"        Disabled: true",
	}, "\n"); got != want {
		t.Errorf("enable_seqscan = off:\n got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
	noSort := optimizer.DefaultPlannerSettings()
	noSort.EnableSort = false
	if got, want := render("EXPLAIN (COSTS OFF) SELECT * FROM dn ORDER BY a", noSort), strings.Join([]string{
		"Sort",
		"  Disabled: true",
		"  Sort Key: a",
		"  ->  Seq Scan on dn",
	}, "\n"); got != want {
		t.Errorf("enable_sort = off:\n got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
	if got := render("EXPLAIN (COSTS OFF) SELECT * FROM dn ORDER BY a", optimizer.DefaultPlannerSettings()); strings.Contains(got, "Disabled") {
		t.Errorf("default settings print no Disabled line:\n%s", got)
	}
	if got := render("EXPLAIN (COSTS OFF, FORMAT JSON) SELECT * FROM dn", noSeq); !strings.Contains(got, `"Disabled": true`) {
		t.Errorf("FORMAT JSON carries the Disabled property:\n%s", got)
	}
	if got := render("EXPLAIN (COSTS OFF, FORMAT JSON) SELECT * FROM dn", optimizer.DefaultPlannerSettings()); !strings.Contains(got, `"Disabled": false`) {
		t.Errorf("FORMAT JSON carries Disabled false on an enabled node:\n%s", got)
	}
}
