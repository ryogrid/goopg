package executor

// M0148-0001c3 — partial indexes hold only the rows their predicate admits
// (ExecInsertIndexTuples / ExecCheckIndexConstraints), and a partial index
// scan drops the restrictions its predicate implies (create_indexscan_plan).

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestPartialUniqueIndexConstrainsOnlyAdmittedRows: rows outside the
// predicate never conflict — on INSERT, on UPDATE until the update moves the
// row into the predicate, and as ON CONFLICT arbiter candidates.
func TestPartialUniqueIndexConstrainsOnlyAdmittedRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE pu (a int, b int)")
	runSQL(t, ctx, "CREATE UNIQUE INDEX pu_a ON pu (a) WHERE b > 0")
	runSQL(t, ctx, "INSERT INTO pu VALUES (1, -1), (1, -2), (1, 5)")
	if _, err := runQueryWithErr(ctx, "INSERT INTO pu VALUES (1, 6)"); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("second admitted (1, 6): err = %v, want a duplicate key error", err)
	}
	runSQL(t, ctx, "UPDATE pu SET b = 0 WHERE b = 5")
	runSQL(t, ctx, "UPDATE pu SET b = 7 WHERE b = -1")
	// ON CONFLICT over the partial arbiter: an outside row is inserted, an
	// admitted one conflicts.
	runSQL(t, ctx, "INSERT INTO pu VALUES (1, -9) ON CONFLICT (a) WHERE b > 0 DO NOTHING")
	runSQL(t, ctx, "INSERT INTO pu VALUES (1, 8) ON CONFLICT (a) WHERE b > 0 DO NOTHING")
	got := renderRows(runSQL(t, ctx, "SELECT a, b FROM pu ORDER BY b"))
	if want := []string{"1|-9", "1|-2", "1|0", "1|7"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	// Moving an outside row into the predicate collides with (1, 7) even
	// though its key column is unchanged. Checked last: the fixture runs in
	// one transaction, so a failed statement is not rolled back.
	if _, err := runQueryWithErr(ctx, "UPDATE pu SET b = 9 WHERE b = -2"); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("UPDATE into the predicate: err = %v, want a duplicate key error", err)
	}
}

// TestPartialIndexScanDropsImpliedQuals: the partial index holds no row
// inserted after CREATE INDEX that fails its predicate, so the scan can drop
// the implied `x IS NOT NULL` (PG: `Index Cond: (y < 11)`, no Filter) and
// still return exactly the admitted rows. A strict clause on x proves the
// predicate too but is not implied by it, so it stays the Filter. A FOR
// UPDATE plan keeps the implied qual for its EPQ recheck.
func TestPartialIndexScanDropsImpliedQuals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE pn (x int, y int)")
	runSQL(t, ctx, "INSERT INTO pn SELECT CASE WHEN g % 10 = 0 THEN NULL ELSE g END, g FROM generate_series(1, 2000) g")
	runSQL(t, ctx, "CREATE INDEX pn_y_part ON pn (y) WHERE x IS NOT NULL")
	runSQL(t, ctx, "INSERT INTO pn VALUES (NULL, 3), (7, 4)")
	runSQL(t, ctx, "UPDATE pn SET x = NULL WHERE y = 5")
	runSQL(t, ctx, "UPDATE pn SET x = 55 WHERE y = 10")
	noSeq := optimizer.DefaultPlannerSettings()
	noSeq.EnableSeqScan = false
	explain := func(q string) string {
		return strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, noSeq)), "\n")
	}
	// The scan node depends on the (unanalysed) costs; what is pinned is
	// that it uses the partial index and which quals remain.
	if got := explain("SELECT * FROM pn WHERE x IS NOT NULL AND y < 11"); !strings.Contains(got, "pn_y_part") || strings.Contains(got, "Filter") {
		t.Errorf("implied qual: want pn_y_part and no Filter, got:\n%s", got)
	}
	if got := explain("SELECT * FROM pn WHERE y < 11 AND x > 0"); !strings.Contains(got, "pn_y_part") || !strings.Contains(got, "Filter: (x > 0)") {
		t.Errorf("strict clause proves the predicate: want pn_y_part and Filter: (x > 0), got:\n%s", got)
	}
	if got := explain("SELECT * FROM pn WHERE x IS NOT NULL AND y < 11 FOR UPDATE"); !strings.Contains(got, "Filter: (x IS NOT NULL)") {
		t.Errorf("FOR UPDATE keeps the implied qual for the EPQ recheck:\n%s", got)
	}
	got := renderRows(runSQLWith(t, ctx, "SELECT x, y FROM pn WHERE x IS NOT NULL AND y < 11 ORDER BY y, x", noSeq))
	if want := []string{"1|1", "2|2", "3|3", "4|4", "7|4", "6|6", "7|7", "8|8", "9|9", "55|10"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rows = %v, want %v", got, want)
	}
}
