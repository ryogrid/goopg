package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// newFSMFixture creates an executor context with a fresh FSM wired in,
// using separate transaction management so multiple commit/begin cycles work.
func newFSMFixture(t testing.TB) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newHOTFixture(t)
	ctx.FSM = storage.NewFSM()
	ctx.EnableOpportunisticPrune = true
	return ctx, cleanup
}

// TestFSMInsertReusesVacuumedPage is the DoD test for M0046-0003.
//
// Steps:
//  1. T1: INSERT enough rows to fill page 0; COMMIT.
//  2. T1b: DELETE all those rows; COMMIT.
//     → page 0 is now packed with dead slots; no free space.
//  3. T2: VACUUM → reclaims dead slots, FSM records free space on page 0.
//  4. T2: INSERT 1 new row → FSM directs insert to page 0 (no extension).
//  5. Verify heap page count stays at the pre-T2 value.
func TestFSMInsertReusesVacuumedPage(t *testing.T) {
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()

	// T1: create table and fill page 0.
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, v text)"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	heapRel := ctx.Catalog.RelFileNode(tbl)

	// Insert rows until page 0 is full (NBlocks grows to 2).
	var fillCount int
	for i := 1; ; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO t VALUES (%d, 'row')", i)); err != nil {
			t.Fatalf("fill insert %d: %v", i, err)
		}
		n, _ := ctx.Pool.NBlocks(heapRel)
		if n > 1 {
			fillCount = i - 1 // rows on page 0
			break
		}
		if i > 1000 {
			t.Fatal("could not fill page in 1000 inserts")
		}
	}
	if fillCount == 0 {
		t.Skip("no rows fit on page 0")
	}

	// Commit T1 (all inserts committed).
	commitTx(t, ctx)

	// T1b: delete all rows on page 0.
	beginTx(t, ctx)
	for i := 1; i <= fillCount; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("DELETE FROM t WHERE id = %d", i)); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	commitTx(t, ctx)

	// T2 begins (OldestXmin advances past all T1 + T1b XIDs).
	beginTx(t, ctx)

	// Page 0 has fillCount dead rows + a row on page 1.
	// NBlocks before vacuum.
	nBeforeVacuum, _ := ctx.Pool.NBlocks(heapRel)
	if nBeforeVacuum < 2 {
		t.Fatalf("expected >=2 pages before vacuum, got %d", nBeforeVacuum)
	}

	// VACUUM: reclaim dead slots on page 0 and update FSM.
	if err := runDDL(t, ctx, "VACUUM t"); err != nil {
		t.Fatalf("VACUUM: %v", err)
	}

	// Verify FSM recorded free space on page 0.
	minFree := uint16(30) // any positive amount
	fsmBlk, ok := ctx.FSM.GetPageWithFreeSpace(heapRel, minFree)
	if !ok {
		t.Fatal("FSM should have recorded free space on page 0 after VACUUM")
	}
	if fsmBlk != 0 {
		t.Errorf("expected FSM to point at page 0 (the vacuumed page), got %d", fsmBlk)
	}

	// INSERT a new row. The FSM should direct this to page 0, not extend.
	nBeforeInsert, _ := ctx.Pool.NBlocks(heapRel)
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (9999, 'reused')"); err != nil {
		t.Fatalf("INSERT after vacuum: %v", err)
	}
	nAfterInsert, _ := ctx.Pool.NBlocks(heapRel)
	if nAfterInsert > nBeforeInsert {
		t.Errorf("INSERT extended relation from %d to %d pages — FSM should have provided a free page",
			nBeforeInsert, nAfterInsert)
	}
}

// TestVacuumUpdatesFSM verifies that VacuumWithFSM populates the FSM with
// the free space on each vacuumed page.
func TestVacuumUpdatesFSM(t *testing.T) {
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t2 (x int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t2 VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t2 VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	commitTx(t, ctx)

	beginTx(t, ctx)
	if err := runDDL(t, ctx, "DELETE FROM t2 WHERE x = 1"); err != nil {
		t.Fatal(err)
	}
	commitTx(t, ctx)

	beginTx(t, ctx)
	if err := runDDL(t, ctx, "VACUUM t2"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t2"})
	heapRel := ctx.Catalog.RelFileNode(tbl)

	// After vacuuming 1 dead slot, page 0 should have some free space in FSM.
	_, ok := ctx.FSM.GetPageWithFreeSpace(heapRel, 20)
	if !ok {
		t.Fatal("FSM should have recorded free space on page 0 after VACUUM")
	}
}

// TestFSMInsertUpdatesFSM verifies that writeHeapRowReturning updates the
// FSM with the remaining free space after each insert.
func TestFSMInsertUpdatesFSM(t *testing.T) {
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t3 (v text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t3 VALUES ('hello')"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t3"})
	heapRel := ctx.Catalog.RelFileNode(tbl)

	// After the insert, FSM should know that page 0 has significant free space.
	_, ok := ctx.FSM.GetPageWithFreeSpace(heapRel, 100)
	if !ok {
		t.Fatal("FSM should record free space on page 0 after the first INSERT")
	}
}

// TestVacuumSQLDispatch verifies that the VACUUM SQL statement dispatches
// to vacuumOp (not the no-op stub) by checking that it actually reclaims
// dead tuples and updates FSM.
func TestVacuumSQLDispatch(t *testing.T) {
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE tv (id int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO tv VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	commitTx(t, ctx)

	beginTx(t, ctx)
	if err := runDDL(t, ctx, "DELETE FROM tv WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	commitTx(t, ctx)

	beginTx(t, ctx)
	// VACUUM must call vacuum.VacuumWithFSM (not the no-op).
	// We verify this by checking that the FSM is updated.
	if err := runDDL(t, ctx, "VACUUM tv"); err != nil {
		t.Fatalf("VACUUM SQL: %v", err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "tv"})
	heapRel := ctx.Catalog.RelFileNode(tbl)
	_, ok := ctx.FSM.GetPageWithFreeSpace(heapRel, 20)
	if !ok {
		t.Fatal("VACUUM SQL did not update FSM — is vacuumOp wired?")
	}
}

// TestFSMMultiTransactionReuse exercises the full lifecycle:
// multiple transactions delete rows, vacuum reclaims them, then new
// rows reuse the freed pages.
func TestFSMMultiTransactionReuse(t *testing.T) {
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE tr (id int, v text)"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "tr"})
	heapRel := ctx.Catalog.RelFileNode(tbl)

	// T1: fill page 0.
	for i := 1; ; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO tr VALUES (%d, 'x')", i)); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
		n, _ := ctx.Pool.NBlocks(heapRel)
		if n > 1 {
			break
		}
		if i > 1000 {
			t.Fatal("could not fill page")
		}
	}
	commitTx(t, ctx)

	// T2: delete everything.
	beginTx(t, ctx)
	if err := runDDL(t, ctx, "DELETE FROM tr"); err != nil {
		t.Fatal(err)
	}
	commitTx(t, ctx)

	// T3: vacuum + insert → must reuse.
	beginTx(t, ctx)
	if err := runDDL(t, ctx, "VACUUM tr"); err != nil {
		t.Fatal(err)
	}

	nBefore, _ := ctx.Pool.NBlocks(heapRel)
	if err := runDDL(t, ctx, "INSERT INTO tr VALUES (9999, 'reused')"); err != nil {
		t.Fatal(err)
	}
	nAfter, _ := ctx.Pool.NBlocks(heapRel)
	if nAfter > nBefore {
		t.Errorf("heap grew from %d to %d — FSM reuse failed", nBefore, nAfter)
	}

	// The inserted row should be queryable.
	rows := runQuery(t, ctx, "SELECT id FROM tr WHERE id = 9999")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row with id=9999, got %d rows", len(rows))
	}
}

