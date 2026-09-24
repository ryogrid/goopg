package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/commands/vacuum"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// lpUnusedCount is the LP_UNUSED item count of the last countHeapLPDead call.
var lpUnusedCount int

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
	lpUnusedCount = 0
	for blk := storage.BlockNumber(0); blk < n; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatal(err)
		}
		s.RLock()
		dead, _ := storage.PageDeadItems(s.Page())
		if cnt, err := storage.PageLinePointerCount(s.Page()); err == nil {
			for off := uint16(1); int(off) <= cnt; off++ {
				if id, err := storage.PageGetItemID(s.Page(), off); err == nil && id.Flags == storage.ItemIDUnused {
					lpUnusedCount++
				}
			}
		}
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
			unusedBefore := lpUnusedCount
			runSQL(t, ctx, "INSERT INTO lpt SELECT g, 'new-' || g FROM generate_series(3000, 3700) g")
			countHeapLPDead(t, ctx, "lpt")
			// S3b: on a capable btree-only table the inserts fill the freed
			// lines instead of appending new ones.
			if !tc.wantDead && lpUnusedCount >= unusedBefore {
				t.Fatalf("freed lines not reused: %d LP_UNUSED before the inserts, %d after", unusedBefore, lpUnusedCount)
			}
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

// TestAutovacuumSequenceCleansIndexesAndFreesItems pins the autovacuum path
// (M0145-0008v): the launcher's sequence — heap pass, VacuumRelationIndexes,
// VacuumDeadItems — leaves no LP_DEAD item on a capable cluster and keeps the
// index exact, exactly like manual VACUUM. VacuumRelationIndexes resolves the
// table's own catalog namespace; a table it cannot confirm answers false.
func TestAutovacuumSequenceCleansIndexesAndFreesItems(t *testing.T) {
	storage.SetHeapLinePointerLifecycle(true)
	defer storage.SetHeapLinePointerLifecycle(false)
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE avt (id int, v text)",
		"INSERT INTO avt SELECT g, 'row-' || g FROM generate_series(1, 1500) g",
		"CREATE INDEX avt_id ON avt (id)",
		"ANALYZE avt",
		"DELETE FROM avt WHERE id % 4 = 0",
	} {
		runSQL(t, ctx, s)
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "avt"})
	rel := ctx.Catalog.RelFileNode(tbl)
	release, ok := vacuum.TryLockRelationForVacuum(rel)
	if !ok {
		t.Fatal("relation VACUUM gate unexpectedly held")
	}
	stats, err := vacuum.VacuumWithOptions(ctx.Pool, ctx.TxnMgr, rel, vacuum.VacuumOptions{})
	if err != nil || len(stats.DeadTIDs) == 0 {
		release()
		t.Fatalf("heap pass: %d dead TIDs, %v", len(stats.DeadTIDs), err)
	}
	if !VacuumRelationIndexes(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, stats.DeadTIDs) {
		release()
		t.Fatal("VacuumRelationIndexes did not report the btree-only table clean")
	}
	if n, err := vacuum.VacuumDeadItems(ctx.Pool, rel, stats.DeadTIDs); err != nil || n != len(stats.DeadTIDs) {
		release()
		t.Fatalf("second pass freed %d of %d (%v)", n, len(stats.DeadTIDs), err)
	}
	release()
	if dead, _ := countHeapLPDead(t, ctx, "avt"); dead != 0 {
		t.Fatalf("%d LP_DEAD items left", dead)
	}
	if VacuumRelationIndexes(ctx.Pool, ctx.TxnMgr, ctx.Catalog, &catalog.Table{OID: tbl.OID, Name: "impostor"}, stats.DeadTIDs) {
		t.Fatal("a table the catalog cannot confirm by identity must not be reported clean")
	}
	runSQL(t, ctx, "INSERT INTO avt SELECT g, 'new-' || g FROM generate_series(5000, 5400) g")
	got := sortedRowStrings(t, ctx, "SELECT id, v FROM avt WHERE id BETWEEN 30 AND 60")
	want := sortedRowStrings(t, ctx, "SELECT id, v FROM avt WHERE id + 0 BETWEEN 30 AND 60")
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("indexed %v != heap %v", got, want)
	}
}
