package executor

import (
	"github.com/goopg/goopg/internal/catalog"
	"testing"

	"github.com/goopg/goopg/internal/parser" //nolint:revive
	"github.com/goopg/goopg/internal/storage"
)

// newVMFixture creates a context with both FSM and VM wired in.
func newVMFixture(t testing.TB) (*Context, func()) {
	t.Helper()
	ctx, cleanup := newFSMFixture(t)
	ctx.VM = storage.NewVisibilityMap()
	// The hook initdb's cluster open installs (internal/initdb/open.go): the
	// planner's allvisfrac and pg_class.relallvisible read the VM through it.
	// Without it a VACUUMed fixture table still priced every index-only scan
	// with all its heap fetches — the M0145-0029 slice 5 finding.
	vm := ctx.VM
	prevVis := catalog.RelAllVisibleFunc
	catalog.RelAllVisibleFunc = func(dbOid, relOid uint32) int32 {
		return vm.CountAllVisible(storage.RelFileNode{DBOid: dbOid, RelOid: relOid})
	}
	return ctx, func() {
		catalog.RelAllVisibleFunc = prevVis
		cleanup()
	}
}

// TestIndexOnlyScanAfterVacuum is the DoD test for M0046-0004.
//
// Steps:
//  1. T1: INSERT rows, CREATE INDEX, COMMIT.
//  2. T2: VACUUM → sets VM ALL_VISIBLE for every heap page
//     (all tuples have committed xmin < OldestXmin, no xmax).
//  3. T3: SELECT covered_col FROM t WHERE covered_col = ? →
//     IndexOnlyScan uses VM fast-path (zero heap reads) and
//     returns correct result.
func TestIndexOnlyScanAfterVacuum(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (3)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_id ON t (id)"); err != nil {
		t.Fatal(err)
	}

	// Commit T1.
	commitTx(t, ctx)

	// T2: VACUUM sets ALL_VISIBLE for every page with committed tuples.
	beginTx(t, ctx)
	if err := runDDL(t, ctx, "VACUUM t"); err != nil {
		t.Fatal(err)
	}

	// Verify VM was updated.
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	heapRel := ctx.Catalog.RelFileNode(tbl)
	if !ctx.VM.AllVisible(heapRel, 0) {
		t.Fatal("VACUUM should have set ALL_VISIBLE for page 0")
	}

	// T3: SELECT id FROM t WHERE id = 2 → IndexOnlyScan fast-path.
	rows := runQuery(t, ctx, "SELECT id FROM t WHERE id = 2")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 2 {
		t.Errorf("expected id=2, got %+v", rows[0][0])
	}
}

// TestVMClearedOnInsert verifies that inserting into a page clears its
// ALL_VISIBLE bit so future index-only scans fall back to heap fetch.
func TestVMClearedOnInsert(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t2 (id int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t2 VALUES (10)"); err != nil {
		t.Fatal(err)
	}
	commitTx(t, ctx)

	beginTx(t, ctx)
	if err := runDDL(t, ctx, "VACUUM t2"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t2"})
	heapRel := ctx.Catalog.RelFileNode(tbl)
	if !ctx.VM.AllVisible(heapRel, 0) {
		t.Fatal("VM should be set after VACUUM")
	}

	// Insert clears the bit.
	if err := runDDL(t, ctx, "INSERT INTO t2 VALUES (11)"); err != nil {
		t.Fatal(err)
	}
	if ctx.VM.AllVisible(heapRel, 0) {
		t.Fatal("VM bit should be cleared after INSERT")
	}
}

// TestPageAllVisibleIntegration verifies PageAllVisible with realistic
// tuples inserted by the executor and committed to a page.
func TestPageAllVisibleIntegration(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE pav (x int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO pav VALUES (42)"); err != nil {
		t.Fatal(err)
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "pav"})
	heapRel := ctx.Catalog.RelFileNode(tbl)
	nBlocks, _ := ctx.Pool.NBlocks(heapRel)
	if nBlocks == 0 {
		t.Fatal("expected at least 1 heap block")
	}
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	slot.RLock()
	horizon := ctx.TxnMgr.OldestXmin()
	allVis := storage.PageAllVisible(slot.Page(), horizon)
	slot.RUnlock()
	ctx.Pool.Unpin(slot)

	if !allVis {
		t.Errorf("committed page should be ALL_VISIBLE (horizon=%d)", horizon)
	}
}

// TestIndexOnlyScanFallbackWithoutVM verifies that without a VM,
// the index scan correctly falls back to a heap fetch and still
// returns correct results (graceful degradation).
func TestIndexOnlyScanFallbackWithoutVM(t *testing.T) {
	ctx, _, cleanup := newHOTFixture(t) // no VM
	defer cleanup()
	ctx.FSM = storage.NewFSM()
	// ctx.VM is nil — ensures heap fallback path is exercised

	if err := runDDL(t, ctx, "CREATE TABLE t3 (id int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t3 VALUES (7)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t3_id ON t3 (id)"); err != nil {
		t.Fatal(err)
	}

	// With no VM, SELECT id FROM t3 WHERE id=7 still uses the
	// IndexOnlyScan plan but falls back to heap fetch for MVCC.
	rows := runQuery(t, ctx, "SELECT id FROM t3 WHERE id = 7")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 7 {
		t.Errorf("expected id=7, got %+v", rows[0][0])
	}
}

// TestPageAllVisibleFollowsVM pins M0122-0012 C47's producer and invalidation
// halves end to end. VACUUM sets the heap page's PD_ALL_VISIBLE together with
// the visibility map's ALL_VISIBLE bit (lazy_scan_prune), and every DML kind
// clears it again (heap_insert, heap_update on both a HOT and a non-HOT path,
// heap_delete) while a row lock keeps it (heap_lock_tuple). The seq scan's
// per-page visibility skip is only safe because the page bit is never left set
// over a tuple some snapshot cannot see.
func TestPageAllVisibleFollowsVM(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, sql := range []string{
		"CREATE TABLE pav (id int, v int)",
		"CREATE INDEX pav_id ON pav (id)",
		"INSERT INTO pav VALUES (1, 10)",
		"INSERT INTO pav VALUES (2, 20)",
		"INSERT INTO pav VALUES (3, 30)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	commitTx(t, ctx)
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "pav"})
	rel := ctx.Catalog.RelFileNode(tbl)
	pageBit := func() bool {
		t.Helper()
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
		if err != nil {
			t.Fatal(err)
		}
		defer ctx.Pool.Unpin(slot)
		slot.RLock()
		defer slot.RUnlock()
		return storage.PageIsAllVisible(slot.Page())
	}
	vacuum := func(when string) {
		t.Helper()
		beginTx(t, ctx)
		if err := runDDL(t, ctx, "VACUUM pav"); err != nil {
			t.Fatal(err)
		}
		commitTx(t, ctx)
		if !ctx.VM.AllVisible(rel, 0) || !pageBit() {
			t.Fatalf("%s: VACUUM left VM all-visible=%v, PD_ALL_VISIBLE=%v; want both set",
				when, ctx.VM.AllVisible(rel, 0), pageBit())
		}
	}
	if pageBit() {
		t.Fatal("PD_ALL_VISIBLE set before any VACUUM")
	}
	vacuum("first pass")
	for _, tc := range []struct {
		name    string
		sql     string
		cleared bool
	}{
		{"insert", "INSERT INTO pav VALUES (4, 40)", true},
		{"hot update", "UPDATE pav SET v = 11 WHERE id = 1", true},
		{"non-hot update", "UPDATE pav SET id = 22 WHERE id = 2", true},
		{"delete", "DELETE FROM pav WHERE id = 3", true},
		{"row lock", "SELECT id FROM pav WHERE id = 1 FOR UPDATE", false},
	} {
		beginTx(t, ctx)
		if _, err := runQueryWithErr(ctx, tc.sql); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := !pageBit(); got != tc.cleared {
			t.Fatalf("%s: PD_ALL_VISIBLE cleared = %v, want %v", tc.name, got, tc.cleared)
		}
		if tc.cleared && ctx.VM.AllVisible(rel, 0) {
			t.Fatalf("%s: page bit cleared but the VM still says all-visible", tc.name)
		}
		commitTx(t, ctx)
		vacuum("after " + tc.name)
	}
}
