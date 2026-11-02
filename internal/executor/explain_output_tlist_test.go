package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0148-0001f: PG's ExplainNode prints VERBOSE's show_plan_tlist right after
// the node line (and `Disabled:`), before every qual and key, and qualifies
// its columns by `useprefix = es->rtable_size > 1` only — VERBOSE alone does
// not, unlike the quals and keys (explain.c). A single-relation scan prints
// `Output: a, b` above a qualified `Filter: (oa.a > 1)`; a join's scans print
// `Output: oa.a, oa.b`.
func TestExplainVerboseOutputPlacementAndPrefix(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE oa (a int, b int)")
	runSQL(t, ctx, "CREATE TABLE ob (a int, c int)")
	ps := optimizer.DefaultPlannerSettings()
	render := func(q string) []string { return renderRows(runSQLWith(t, ctx, q, ps)) }

	single := render("EXPLAIN (VERBOSE, COSTS OFF) SELECT a, b FROM oa WHERE a > 1")
	if want := []string{"Seq Scan on public.oa", "  Output: a, b", "  Filter: (oa.a > 1)"}; strings.Join(single, "\n") != strings.Join(want, "\n") {
		t.Errorf("single relation:\n got:\n%s\nwant (PG 18.3):\n%s", strings.Join(single, "\n"), strings.Join(want, "\n"))
	}

	join := strings.Join(render("EXPLAIN (VERBOSE, COSTS OFF) SELECT oa.a, ob.c FROM oa JOIN ob ON oa.a = ob.a"), "\n")
	for _, want := range []string{"Output: oa.a, oa.b", "Output: ob.a, ob.c"} {
		if !strings.Contains(join, want) {
			t.Errorf("join: want %q (PG 18.3); got:\n%s", want, join)
		}
	}
	lines := strings.Split(join, "\n")
	for i, l := range lines {
		if strings.Contains(l, "Hash Join") || strings.Contains(l, "Merge Join") || strings.Contains(l, "Nested Loop") {
			if i+1 >= len(lines) || !strings.Contains(lines[i+1], "Output:") {
				t.Errorf("join: Output must directly follow the node line:\n%s", join)
			}
		}
	}
}
