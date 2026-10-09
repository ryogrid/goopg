package executor

import (
	"strings"
	"testing"
)

// TestExplainNeqJoinReadsColumnStats pins M0146-0005bo against PG 18.3:
// `a.h <> b.h` is neqjoinsel, 1 - eqjoinsel over the two columns'
// statistics (get_join_variables examines each operand's own relation for
// every operator). With h holding 5 distinct values the factor is 0.8, and
// PG prints
//
//	Hash Join  (cost=27.50..113.75 rows=4000 width=4)
//	  Hash Cond: (a.o = b.o)
//	  Join Filter: (a.h <> b.h)
//
// goopg resolved a non-equijoin's operands to no relation, so `<>` came out
// as 1 - DEFAULT_EQ_SEL (4975 rows here; TPC-DS Q95's ws_wh 2168680 where
// PG has 1752341).
func TestExplainNeqJoinReadsColumnStats(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, sql := range []string{
		"CREATE TABLE w (o int, h int)",
		"INSERT INTO w SELECT i/5, i%5 FROM generate_series(0,999) i",
		"ANALYZE w",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	lines := runExplain(t, ctx, `SELECT 1 FROM w a, w b WHERE a.o = b.o AND a.h <> b.h`)
	if !strings.Contains(lines[0], "Join") || !strings.Contains(lines[0], " rows=4000 ") {
		t.Fatalf("want PG's 4000-row join estimate:\n%s", strings.Join(lines, "\n"))
	}
}
