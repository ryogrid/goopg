package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0148-0001k: an inheritance / partition appendrel prints as PG prints it
// (expected text from PG 18.3):
//   - every member is an expanded child RTE aliased with the parent's
//     refname, so set_rtable_names prints `<parent>_N` once the Append (the
//     parent RTE) has claimed the bare name — the inherited parent's own
//     scan included (expand_single_inheritance_child);
//   - a restriction on the table filters at every member scan, translated
//     to the member's columns (set_append_rel_size, adjust_appendrel_attrs),
//     with no Filter left on the Append;
//   - a key above the Append names the parent (`Group Key: ih.f1`), never
//     the first member.
func TestExplainAppendRelMembers(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table ih (f1 int)",
		"create table ihc () inherits (ih)",
		"create table ihp (a int, b int) partition by range (a)",
		"create table ihp1 partition of ihp for values from (0) to (10)",
		"create table ihp2 partition of ihp for values from (10) to (20)",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	for _, tc := range []struct{ q, want string }{
		{"explain (costs off) select * from ih where f1 > 1",
			"Append\n  ->  Seq Scan on ih ih_1\n        Filter: (f1 > 1)\n  ->  Seq Scan on ihc ih_2\n        Filter: (f1 > 1)"},
		{"explain (costs off) select * from ihp where b > 1",
			"Append\n  ->  Seq Scan on ihp1 ihp_1\n        Filter: (b > 1)\n  ->  Seq Scan on ihp2 ihp_2\n        Filter: (b > 1)"},
		{"explain (costs off) select f1 from ih group by f1",
			"HashAggregate\n  Group Key: ih.f1\n  ->  Append\n        ->  Seq Scan on ih ih_1\n        ->  Seq Scan on ihc ih_2"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, tc.q, ps)), "\n"); got != tc.want {
			t.Errorf("%s\n got:\n%s\nwant (PG 18.3):\n%s", tc.q, got, tc.want)
		}
	}
	// The pushed restriction still filters the right rows.
	runSQL(t, ctx, "insert into ih values (1), (5)")
	runSQL(t, ctx, "insert into ihc values (2), (7)")
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "select f1 from ih where f1 > 1 order by 1", ps)), ","); got != "2,5,7" {
		t.Errorf("filtered rows = %q, want 2,5,7", got)
	}
}
