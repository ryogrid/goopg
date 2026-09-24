package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// countHeapLPDead counts the LP_DEAD items across every heap block of table,
// and the blocks whose PD_HAS_FREE_LINES hint is set (only VACUUM's second
// heap pass sets it).
func countHeapLPDead(t *testing.T, ctx *Context, table string) (int, int) {
	t.Helper()
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: table})
	if !ok {
		t.Fatalf("no table %s", table)
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	n, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	total, freeLines := 0, 0
	for blk := storage.BlockNumber(0); blk < n; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatal(err)
		}
		s.RLock()
		dead, _ := storage.PageDeadItems(s.Page())
		if storage.MustHeader(s.Page()).Flags()&storage.PDHasFreeLines != 0 {
			freeLines++
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
		total += len(dead)
	}
	return total, freeLines
}

// TestVacuumSecondHeapPassKeepsIndexesExact pins VACUUM's second heap pass
// end to end (M0145-0008v): after DELETE + VACUUM the pruned rows' LP_DEAD
// items become LP_UNUSED and are truncated (capable cluster, btree-only
// table), new rows then reuse those slot numbers, and every index-driven
// query still returns exactly the heap's rows. On a legacy cluster, or with a
// non-btree index whose entries goopg's index vacuum cannot remove (GIN; a
// USING hash index rides the btree substrate and is cleaned), the LP_DEAD
// items stay dead.
func TestVacuumSecondHeapPassKeepsIndexesExact(t *testing.T) {
	for _, tc := range []struct {
		name     string
		capable  bool
		gin      bool
		wantDead bool
	}{
		{"capable-btree", true, false, false},
		{"legacy-cluster", false, false, true},
		{"capable-with-gin-index", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage.SetHeapLinePointerLifecycle(tc.capable)
			defer storage.SetHeapLinePointerLifecycle(false)
			ctx, cleanup := newVMFixture(t)
			defer cleanup()
			ddl := []string{
				"CREATE TABLE lpt (id int, v text, tags int[])",
				"INSERT INTO lpt SELECT g, 'row-' || g, ARRAY[g] FROM generate_series(1, 2000) g",
				"CREATE INDEX lpt_id ON lpt (id)",
			}
			if tc.gin {
				ddl = append(ddl, "CREATE INDEX lpt_tags ON lpt USING gin (tags)")
			}
			ddl = append(ddl, "ANALYZE lpt", "DELETE FROM lpt WHERE id % 3 = 0")
			for _, s := range ddl {
				runSQL(t, ctx, s)
			}
			// Commit the deletes and move the horizon past them, so VACUUM
			// finds them dead to every snapshot.
			commitTx(t, ctx)
			beginTx(t, ctx)
			runSQL(t, ctx, "VACUUM lpt")
			dead, freeLines := countHeapLPDead(t, ctx, "lpt")
			if (dead > 0) != tc.wantDead || (freeLines > 0) == tc.wantDead {
				t.Fatalf("after VACUUM: %d LP_DEAD items, %d pages with free lines; want dead=%v", dead, freeLines, tc.wantDead)
			}
			runSQL(t, ctx, "INSERT INTO lpt SELECT g, 'new-' || g FROM generate_series(3000, 3700) g")
			runSQL(t, ctx, "ANALYZE lpt")
			for _, q := range []struct{ indexed, noIndex string }{
				{"SELECT id, v FROM lpt WHERE id BETWEEN 90 AND 130", "SELECT id, v FROM lpt WHERE id + 0 BETWEEN 90 AND 130"},
				{"SELECT id, v FROM lpt WHERE id = 99", "SELECT id, v FROM lpt WHERE id + 0 = 99"},
				{"SELECT id, v FROM lpt WHERE id BETWEEN 3100 AND 3140", "SELECT id, v FROM lpt WHERE id + 0 BETWEEN 3100 AND 3140"},
			} {
				got := sortedRowStrings(t, ctx, q.indexed)
				want := sortedRowStrings(t, ctx, q.noIndex)
				if strings.Join(got, "|") != strings.Join(want, "|") {
					t.Fatalf("%s: indexed %v, heap %v\nplan:\n%s", q.indexed, got, want,
						strings.Join(runExplainRows(t, ctx, "EXPLAIN "+q.indexed), "\n"))
				}
			}
			if plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN SELECT id, v FROM lpt WHERE id = 99"), "\n"); !strings.Contains(plan, "Index") {
				t.Fatalf("the point query uses no index; nothing is exercised:\n%s", plan)
			}
		})
	}
}
