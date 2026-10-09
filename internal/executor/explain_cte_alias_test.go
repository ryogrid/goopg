package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRepeatedCTEReferenceTakesSuffix pins M0146-0005cp against PG 18.3: a
// CTE referenced twice without aliases is two range-table entries with one
// name, and set_rtable_names suffixes the later one, so PG prints
// `CTE Scan on c` and `CTE Scan on c c_1` (TPC-DS Q24's `ssales ssales_1`).
func TestRepeatedCTEReferenceTakesSuffix(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE cc (x int, y int)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	const q = "WITH c AS MATERIALIZED (SELECT x, y FROM cc) SELECT * FROM c WHERE x > (SELECT avg(x) FROM c)"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r[0].StringValue()), "->")))
		}
	}
	plain, suffixed := false, false
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "CTE Scan on c" {
			plain = true
		}
		if l == "CTE Scan on c c_1" {
			suffixed = true
		}
	}
	if !plain || !suffixed {
		t.Fatalf("want `CTE Scan on c` and `CTE Scan on c c_1`:\n%s", strings.Join(lines, "\n"))
	}
}
