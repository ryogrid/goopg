package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestExplainKeywordFuncsPrintUppercase pins M0146-0042 against PG 18.3:
// COALESCE, NULLIF, GREATEST and LEAST are their own expression nodes, which
// ruleutils.c prints by keyword in capitals (TPC-DS Q75's Group Key
// `(store_sales.ss_quantity - COALESCE(store_returns.sr_return_quantity, 0))`).
// The want line is PG's output for the statement.
func TestExplainKeywordFuncsPrintUppercase(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE cz (a int, b int)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	q := "EXPLAIN (COSTS OFF) SELECT * FROM cz WHERE coalesce(a, 0) > nullif(b, 1) AND greatest(a, b) < least(a, 3)"
	want := "Filter: ((COALESCE(a, 0) > NULLIF(b, 1)) AND (GREATEST(a, b) < LEAST(a, 3)))"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)) {
		lines = append(lines, strings.TrimSpace(r[0].StringValue()))
	}
	for _, l := range lines {
		if l == want {
			return
		}
	}
	t.Errorf("want PG's line %q in:\n%s", want, strings.Join(lines, "\n"))
}
