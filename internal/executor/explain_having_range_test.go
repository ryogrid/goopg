package executor

import (
	"strings"
	"testing"
)

// TestExplainHavingBoundsAreNotARange pins M0146-0005bl against PG 18.3:
// clauselist_selectivity pairs `x >= a AND x <= b` into a range only when the
// clause is over exactly one relation (NumRelids == 1). A HAVING bound on an
// aggregate is an Aggref over none, so each bound is its own DEFAULT_INEQ_SEL
// (1/3) factor: 200 default groups x 1/9 = 22 rows, which PG prints as
//
//	HashAggregate  (cost=43.90..46.90 rows=22 width=12)
//	  Filter: ((count(*) >= 15) AND (count(*) <= 20))
//
// Pairing them (DEFAULT_RANGE_INEQ_SEL, 0.005) gave 1 row (TPC-DS Q34).
func TestExplainHavingBoundsAreNotARange(t *testing.T) {
	lines := cteExplainLines(t, `SELECT a, count(*) FROM t GROUP BY a HAVING count(*) >= 15 AND count(*) <= 20`)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(lines[0], "Aggregate") || !strings.Contains(lines[0], " rows=22 ") {
		t.Fatalf("want the grouped node estimated at PG's 22 rows:\n%s", joined)
	}
}
