package executor

import (
	"strings"
	"testing"
)

// TestCTEUnderLateralMaterialisesOnce pins M0146-0049d2 against PG 18.3. A
// LATERAL join re-binds the CTE cache per outer tuple so that a CTE body
// reading the outer row is re-materialised; a body that reads no outer value
// is materialised once per statement, as PG clears a CTE's tuplestore on
// rescan only when its plan has changed parameters (ExecReScanCteScan). The
// volatile random() makes a re-materialisation visible: one distinct value
// means one evaluation. The parameterised inner of TPC-DS Q95 scans a
// 1.7M-row statement-level CTE per outer row, which recomputed it each time.
func TestCTEUnderLateralMaterialisesOnce(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, c := range []struct{ sql, want string }{
		// A statement-level CTE scanned inside the lateral: one evaluation.
		{"WITH c AS MATERIALIZED (SELECT random() r) SELECT count(DISTINCT x.r) FROM generate_series(1,5) g, LATERAL (SELECT r FROM c WHERE g > 0) x", "1"},
		// An uncorrelated CTE declared inside the lateral subquery: PG
		// rewinds its tuplestore on every rescan, so it too runs once.
		{"SELECT count(DISTINCT x.r) FROM generate_series(1,5) g, LATERAL (WITH c AS MATERIALIZED (SELECT random() r) SELECT r FROM c) x", "1"},
		// A body reading the outer row is still re-materialised per row.
		{"SELECT sum(x.k) FROM generate_series(1,5) g, LATERAL (WITH c AS MATERIALIZED (SELECT g*2 AS k) SELECT k FROM c) x", "30"},
		// Both kinds in one lateral.
		{"SELECT sum(x.k) FROM generate_series(1,5) g, LATERAL (WITH c AS MATERIALIZED (SELECT g*2 AS k), d AS MATERIALIZED (SELECT 7 AS k) SELECT c.k + d.k AS k FROM c, d) x", "65"},
	} {
		got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";")
		if got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
}
