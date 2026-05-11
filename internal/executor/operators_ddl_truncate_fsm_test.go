package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestTruncateClearsFSMEntries pins M0090-0001: TRUNCATE must call
// FSM.DropRelation so a subsequent INSERT does NOT consult a stale
// fsmBlk that points past the now-truncated file's EOF.
//
// Pre-fix flow that this test guards against:
//  1. CREATE TABLE + INSERT N rows: FSM records free space for the
//     new heap pages.
//  2. TRUNCATE TABLE: nblocks → 0 on disk, buffer-pool slots
//     invalidated. **FSM entries persist.**
//  3. INSERT a new row: writeHeapRowReturning calls
//     `ctx.FSM.GetPageWithFreeSpace(rel, _)` and gets back a stale
//     block number. tryAppendToBlock then calls
//     `Pool.Pin({rel, blk=<stale>})` → `Manager.ReadBlock` →
//     `ErrShortRead` because `blk >= r.nblocks` (nblocks=0
//     post-truncate). The transaction aborts.
//
// Post-fix: TRUNCATE invokes `ctx.FSM.DropRelation(rel)` (and
// `ctx.VM.DropRelation(rel)`), so the next INSERT sees an empty
// FSM, extends a fresh block, and succeeds.
func TestTruncateClearsFSMEntries(t *testing.T) {
	ctx, _, cleanup := newDDLFixtureWithFSMVM(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	rel := ctx.Catalog.RelFileNode(tbl)

	seedItems(t, ctx, tbl)

	// Sanity: pre-truncate, FSM has at least one block recorded.
	if blk, ok := ctx.FSM.GetPageWithFreeSpace(rel, 1); !ok {
		t.Fatalf("pre-truncate: FSM has no entry for rel %v (expected after INSERT)", rel)
	} else {
		t.Logf("pre-truncate FSM blk=%d", blk)
	}

	if err := runDDL(t, ctx, "TRUNCATE TABLE items"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}

	// Post-truncate, FSM must be empty for this rel.
	if blk, ok := ctx.FSM.GetPageWithFreeSpace(rel, 1); ok {
		t.Errorf("FSM still answers GetPageWithFreeSpace post-truncate (blk=%d) — TRUNCATE did not call DropRelation", blk)
	}

	// VM should also be cleared. (ClearBlock would re-create
	// entries, but we never called it post-truncate; AllVisible
	// for any block should return false on an empty vmKey.)
	if ctx.VM.AllVisible(rel, 0) {
		t.Errorf("VM.AllVisible reports true post-truncate — VM.DropRelation not called")
	}

	// The real proof: a subsequent INSERT must succeed without
	// `short read at block`. Pre-fix this errors; post-fix it
	// works because FSM is empty so writeHeapRow falls through to
	// the extend path.
	if err := writeHeapRow(ctx, rel, tbl.Columns,
		Row{{Kind: KindInt, Int: 99}, {Kind: KindString, Buf: []byte("post-truncate")}}); err != nil {
		t.Fatalf("post-truncate INSERT failed: %v (expected to succeed once FSM is cleared)", err)
	}
}

// newDDLFixtureWithFSMVM is `newDDLFixture` plus a wired-up FSM and
// VisibilityMap on the Context. Used by tests that exercise the
// TRUNCATE → INSERT path which consults these maps.
func newDDLFixtureWithFSMVM(t *testing.T) (*Context, catalog.Catalog, func()) {
	t.Helper()
	ctx, cat, cleanup := newDDLFixture(t)
	ctx.FSM = storage.NewFSM()
	ctx.VM = storage.NewVisibilityMap()
	return ctx, cat, cleanup
}
