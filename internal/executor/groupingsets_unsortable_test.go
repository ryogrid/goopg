package executor

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestGroupingSetsUnsortableColumns pins M0148-0001j4 item (4), regress
// groupingsets' failure cases: xid has only a hash opclass and bit only a
// btree one (pg_opclass.dat). preprocess_grouping_sets rejects a set that
// holds both kinds, and an unsortable set beside an ordered aggregate leaves
// no path (create_grouping_paths cannot hash with numOrderedAggs > 0) — both
// 0A000 "could not implement GROUP BY" with PG's DETAIL. Hashing the
// unsortable set without an ordered aggregate, and an ordered aggregate over
// sortable sets, still plan and run.
func TestGroupingSetsUnsortableColumns(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE gstest4 (id integer, v integer, unhashable_col bit(4), unsortable_col xid)",
		"INSERT INTO gstest4 VALUES (1,1,b'0000','1'), (2,2,b'0001','1'), (3,4,b'0010','2'), (4,8,b'0011','2')",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, q := range []string{
		"SELECT count(*) FROM gstest4 GROUP BY ROLLUP(unhashable_col, unsortable_col)",
		"SELECT array_agg(v ORDER BY v) FROM gstest4 GROUP BY GROUPING SETS ((id, unsortable_col), (id))",
	} {
		_, err := runQueryErr(t, ctx, q)
		var pe *optimizer.PlanError
		if !errors.As(err, &pe) || pe.Code != "0A000" || pe.Message != "could not implement GROUP BY" ||
			pe.Detail != "Some of the datatypes only support hashing, while others only support sorting." {
			t.Errorf("%s: err = %v, want 0A000 could not implement GROUP BY with PG's DETAIL", q, err)
		}
	}
	for q, want := range map[string]int{
		"SELECT unsortable_col, count(*) FROM gstest4 GROUP BY GROUPING SETS ((unsortable_col), (unsortable_col))": 4,
		"SELECT id, array_agg(v ORDER BY v) FROM gstest4 GROUP BY GROUPING SETS ((id), ())":                        5,
	} {
		rows, err := runQueryErr(t, ctx, q)
		if err != nil || len(rows) != want {
			t.Errorf("%s: %d rows, err %v; want %d rows", q, len(rows), err, want)
		}
	}
}
