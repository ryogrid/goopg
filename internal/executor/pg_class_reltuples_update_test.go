package executor

// M0146-0064: `UPDATE pg_class SET reltuples` reaches the planner.
//
// Regress groupingsets runs `update pg_class set reltuples = 10 where
// relname = 'gs_data_1'` (and the same for bug_16784). PG then plans with 10
// rows (estimate_rel_size reads rd_rel->reltuples after the relcache
// invalidation the UPDATE causes), so the enable_sort = off CUBE hashes every
// grouping set. goopg's UPDATE rewrote the physical pg_class heap row, which
// neither the planner nor the pg_class view reads: it reported UPDATE 1 and
// planned with the 2000 rows ANALYZE measured.
//
// The in-process fixture keeps no pg_class heap rows for user tables, so this
// drives syncPgClassRelStats, the per-row step both update paths now call,
// with the row the UPDATE would write. The server-level witness is the
// regress groupingsets section, byte-identical to PG 18.3 with the change.

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

func TestUpdatePgClassReltuplesReachesPlanner(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	if err := runDDL(t, ctx, "CREATE TABLE gs (a int, b int)"); err != nil {
		t.Fatal(err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "gs"})
	if !ok {
		t.Fatal("table gs missing")
	}
	pgClass, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok {
		t.Fatal("pg_catalog.pg_class missing")
	}
	col := func(name string) int {
		for i, c := range pgClass.Columns {
			if c.Name == name {
				return i
			}
		}
		t.Fatalf("pg_class has no %s column", name)
		return -1
	}
	oidCol, tuplesCol, hasIdxCol := col("oid"), col("reltuples"), col("relhasindex")

	invalidations := 0
	ctx.OnCommitDDL = func() { invalidations++ }

	// update runs the per-row step for `UPDATE pg_class SET <setCol> = …`
	// over gs's row, whose reltuples the row carries as heapTuples.
	update := func(setCol int, heapTuples int64) {
		t.Helper()
		set := make([]optimizer.Expr, len(pgClass.Columns))
		set[setCol] = &optimizer.IntegerConst{Value: heapTuples}
		op := &updateOp{plan: &optimizer.Update{Table: pgClass, Set: set}, ctx: ctx}
		row := make(Row, len(pgClass.Columns))
		for i := range row {
			row[i] = NullDatum
		}
		row[oidCol] = NewIntDatum(int64(tbl.OID))
		row[tuplesCol] = NewIntDatum(heapTuples)
		row[hasIdxCol] = NewBoolDatum(false)
		op.syncPgClassRelStats(row)
	}
	rows := func() int64 {
		t.Helper()
		if tbl.Stats == nil {
			return -1
		}
		return tbl.Stats.RowCount
	}

	update(tuplesCol, 10)
	if got := rows(); got != 10 {
		t.Fatalf("after UPDATE pg_class SET reltuples = 10: planner row count %d, want 10", got)
	}
	if invalidations != 1 {
		t.Fatalf("plan cache invalidations = %d, want 1: a cached plan would keep the old row count", invalidations)
	}

	// An UPDATE of another column carries the heap row's (possibly stale)
	// reltuples, which must not reach the planner.
	update(hasIdxCol, 7777)
	if got := rows(); got != 10 {
		t.Fatalf("an UPDATE of relhasindex changed the planner row count to %d, want 10", got)
	}

	// Inside an explicit transaction the value is visible at once, and
	// ROLLBACK restores the committed one.
	ctx.Session = NewBasicSession()
	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	update(tuplesCol, 500)
	if got := rows(); got != 500 {
		t.Fatalf("inside the transaction: planner row count %d, want 500", got)
	}
	if err := runTransactionStmt(t, ctx, "ROLLBACK"); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
	if got := rows(); got != 10 {
		t.Fatalf("after ROLLBACK: planner row count %d, want the committed 10", got)
	}

	// reltuples = -1 is PG's "never vacuumed or analyzed".
	update(tuplesCol, -1)
	if tbl.Stats == nil || tbl.Stats.Analyzed {
		t.Fatalf("reltuples = -1 left the relation marked analyzed: %+v", tbl.Stats)
	}
}
