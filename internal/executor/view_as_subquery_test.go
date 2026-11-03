package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestViewPlansAsSubqueryRTE pins M0148-0001i1: a view reference is expanded
// into a subquery RTE of the statement (fireRIRrules), so its ordering
// reaches the outer query — no redundant Sort above a grouped view whose
// GroupAggregate already delivers the ORDER BY — and its relations are named
// in the statement's own range table (`Group Key: vt.y`, qualified because
// the range table holds more than one entry), as PG 18.3 prints.
func TestViewPlansAsSubqueryRTE(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE vt (x int, y int)")
	runSQL(t, ctx, "INSERT INTO vt SELECT g, g % 5 FROM generate_series(1, 100) g")
	runSQL(t, ctx, "CREATE VIEW vv AS SELECT y, count(*) AS c FROM vt GROUP BY y")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashAgg = false
	got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM vv ORDER BY y", ps)), "\n")
	want := strings.Join([]string{
		"GroupAggregate",
		"  Group Key: vt.y",
		"  ->  Sort",
		"        Sort Key: vt.y",
		"        ->  Seq Scan on vt",
	}, "\n")
	if got != want {
		t.Errorf("got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
	rows := renderRows(runSQL(t, ctx, "SELECT * FROM vv ORDER BY y"))
	if len(rows) != 5 || rows[0] != "0|20" {
		t.Errorf("rows = %v", rows)
	}
}
