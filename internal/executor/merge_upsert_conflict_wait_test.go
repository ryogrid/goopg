package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage/lmgr"
	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// These tests prove the M0119-0009 wiring: mergeApplyUpdate, mergeApplyDelete,
// and upsertOp.applyUpdate must wait for a still-active foreign row lock that
// conflicts with the write before stamping the old tuple's xmax, mirroring
// the plain UPDATE/DELETE write-path wait (waitForConflictingRowLock,
// M0118-0003) that all three previously skipped — see
// docs/design/0118-0011-update-multixact-locker-preserving-producer.md's
// "Also still deferred" note and the M0118-0004 ledger row appended loop #44.
//
// Session 2's Context never sets Ctx (the stdlib context.Context field), so
// epqWait's WaitForXID call is skipped and waitForConflictingRowLock instead
// busy-spins its outer retry loop until session 1 releases — the same
// test-only quirk TestUpdateBlocksOnForeignTupleLock already relies on (real
// server dispatch always sets Ctx, so production blocking is a real
// WaitForXID sleep, not a spin).

func TestMergeApplyUpdateWaitsOnForeignConflictingLock(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}
	ctx.MultiXact = multixact.NewStore()
	rel := ctx.Catalog.RelFileNode(tbl)

	lm := lmgr.New()

	s1tx, err := ctx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	s1snap, err := ctx.TxnMgr.SnapshotFor(s1tx)
	if err != nil {
		t.Fatal(err)
	}
	s1 := makeCtx(lm, 1)
	s1.Pool = ctx.Pool
	s1.Catalog = ctx.Catalog
	s1.TxnMgr = ctx.TxnMgr
	s1.MultiXact = ctx.MultiXact
	s1.Tx = s1tx
	s1.Snap = s1snap
	if _, err := runForUpdate(t, s1, "SELECT id FROM items WHERE id = 1 FOR UPDATE"); err != nil {
		t.Fatalf("session-1 FOR UPDATE: %v", err)
	}

	s2tx, err := ctx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	s2snap, err := ctx.TxnMgr.SnapshotFor(s2tx)
	if err != nil {
		t.Fatal(err)
	}
	s2 := makeCtx(lm, 2)
	s2.Pool = ctx.Pool
	s2.Catalog = ctx.Catalog
	s2.TxnMgr = ctx.TxnMgr
	s2.MultiXact = ctx.MultiXact
	s2.Tx = s2tx
	s2.Snap = s2snap
	defer ctx.TxnMgr.Rollback(s2tx)

	// Called directly (bypassing MERGE's SQL grammar/join logic — the row-lock
	// wait, not MERGE's matching, is under test) on id=1 (block 0, slot 1).
	newRow := Row{NewIntDatum(1), NewStringDatum("merged")}
	done := make(chan error, 1)
	go func() {
		done <- mergeApplyUpdate(s2, rel, tbl, tbl.Columns, 0, 1, newRow, nil, rel, tbl.Columns, 0)
	}()

	select {
	case err := <-done:
		t.Fatalf("mergeApplyUpdate returned early (err=%v) — did not wait for session 1's conflicting lock", err)
	case <-time.After(300 * time.Millisecond):
		// still blocked, as expected
	}

	if err := ctx.TxnMgr.Rollback(s1tx); err != nil {
		t.Fatalf("rollback session 1: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("mergeApplyUpdate after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mergeApplyUpdate did not unblock after session 1 released its lock")
	}
}

func TestMergeApplyDeleteWaitsOnForeignConflictingLock(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}
	ctx.MultiXact = multixact.NewStore()
	rel := ctx.Catalog.RelFileNode(tbl)

	lm := lmgr.New()

	s1tx, err := ctx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	s1snap, err := ctx.TxnMgr.SnapshotFor(s1tx)
	if err != nil {
		t.Fatal(err)
	}
	s1 := makeCtx(lm, 1)
	s1.Pool = ctx.Pool
	s1.Catalog = ctx.Catalog
	s1.TxnMgr = ctx.TxnMgr
	s1.MultiXact = ctx.MultiXact
	s1.Tx = s1tx
	s1.Snap = s1snap
	// FOR SHARE still conflicts with a MERGE DELETE (always StatusUpdate).
	if _, err := runForUpdate(t, s1, "SELECT id FROM items WHERE id = 2 FOR SHARE"); err != nil {
		t.Fatalf("session-1 FOR SHARE: %v", err)
	}

	s2tx, err := ctx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	s2snap, err := ctx.TxnMgr.SnapshotFor(s2tx)
	if err != nil {
		t.Fatal(err)
	}
	s2 := makeCtx(lm, 2)
	s2.Pool = ctx.Pool
	s2.Catalog = ctx.Catalog
	s2.TxnMgr = ctx.TxnMgr
	s2.MultiXact = ctx.MultiXact
	s2.Tx = s2tx
	s2.Snap = s2snap
	defer ctx.TxnMgr.Rollback(s2tx)

	// id=2 is block 0, slot 2.
	done := make(chan error, 1)
	go func() {
		done <- mergeApplyDelete(s2, rel, tbl, tbl.Columns, 0, 2, nil, 0)
	}()

	select {
	case err := <-done:
		t.Fatalf("mergeApplyDelete returned early (err=%v) — did not wait for session 1's conflicting lock", err)
	case <-time.After(300 * time.Millisecond):
		// still blocked, as expected
	}

	if err := ctx.TxnMgr.Rollback(s1tx); err != nil {
		t.Fatalf("rollback session 1: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("mergeApplyDelete after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mergeApplyDelete did not unblock after session 1 released its lock")
	}
}

func TestUpsertOnConflictDoUpdateWaitsOnForeignConflictingLock(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	upsertSeed(t, ctx) // seeds id=1,2,3 + unique index on id
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	lm := lmgr.New()

	s1tx, err := ctx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	s1snap, err := ctx.TxnMgr.SnapshotFor(s1tx)
	if err != nil {
		t.Fatal(err)
	}
	s1 := makeCtx(lm, 1)
	s1.Pool = ctx.Pool
	s1.Catalog = ctx.Catalog
	s1.TxnMgr = ctx.TxnMgr
	s1.Tx = s1tx
	s1.Snap = s1snap
	if _, err := runForUpdate(t, s1, "SELECT id FROM items WHERE id = 2 FOR UPDATE"); err != nil {
		t.Fatalf("session-1 FOR UPDATE: %v", err)
	}

	s2tx, err := ctx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	s2snap, err := ctx.TxnMgr.SnapshotFor(s2tx)
	if err != nil {
		t.Fatal(err)
	}
	s2 := makeCtx(lm, 2)
	s2.Pool = ctx.Pool
	s2.Catalog = ctx.Catalog
	s2.TxnMgr = ctx.TxnMgr
	s2.Tx = s2tx
	s2.Snap = s2snap
	defer ctx.TxnMgr.Rollback(s2tx)

	done := make(chan error, 1)
	go func() {
		stmts, perr := parser.Parse("INSERT INTO items (id, label) VALUES (2, 'beta-conflict') ON CONFLICT (id) DO UPDATE SET label = excluded.label")
		if perr != nil {
			done <- perr
			return
		}
		node, perr := optimizer.Plan(stmts[0], s2.Catalog)
		if perr != nil {
			done <- perr
			return
		}
		op, perr := Build(node)
		if perr != nil {
			done <- perr
			return
		}
		if perr := op.Open(s2); perr != nil {
			done <- perr
			return
		}
		_, perr = op.Next()
		_ = op.Close()
		if perr != nil && perr != EOF {
			done <- perr
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		t.Fatalf("ON CONFLICT DO UPDATE returned early (err=%v) — did not wait for session 1's conflicting lock", err)
	case <-time.After(300 * time.Millisecond):
		// still blocked, as expected
	}

	if err := ctx.TxnMgr.Rollback(s1tx); err != nil {
		t.Fatalf("rollback session 1: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ON CONFLICT DO UPDATE after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ON CONFLICT DO UPDATE did not unblock after session 1 released its lock")
	}
}
