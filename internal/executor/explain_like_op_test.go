package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestLikePrintsAsTildeOperator pins M0146-0005db against PG 18.3: LIKE is
// the `~~` operator family (textlike / bpcharlike / namelike; NOT LIKE `!~~`,
// ILIKE `~~*`, NOT ILIKE `!~~*`), so ruleutils.c deparses it as an OpExpr:
// the pattern literal is the text Const make_op coerced it to, and a varchar
// operand shows its implicit cast to text. Every want line is PG's output.
func TestLikePrintsAsTildeOperator(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE lk (c char(15), d text, f varchar(9), n name)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ where, want string }{
		{"c LIKE 'Unknown%'", "Filter: (c ~~ 'Unknown%'::text)"},
		{"d LIKE 'a%'", "Filter: (d ~~ 'a%'::text)"},
		{"f LIKE 'a%'", "Filter: ((f)::text ~~ 'a%'::text)"},
		{"d NOT LIKE 'a%'", "Filter: (d !~~ 'a%'::text)"},
		{"d ILIKE 'a%'", "Filter: (d ~~* 'a%'::text)"},
		{"f NOT ILIKE 'a%'", "Filter: ((f)::text !~~* 'a%'::text)"},
		{"n LIKE 'a%'", "Filter: (n ~~ 'a%'::text)"},
		{"d LIKE f", "Filter: (d ~~ (f)::text)"},
		{"upper(f) LIKE 'A%'", "Filter: (upper((f)::text) ~~ 'A%'::text)"},
	} {
		q := "SELECT * FROM lk WHERE " + c.where
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		found := false
		for _, l := range lines {
			if l == c.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s\nwant line %q in:\n%s", q, c.want, strings.Join(lines, "\n"))
		}
	}
}
