package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// These tests are the UPDATE...FROM / DELETE...USING siblings of
// TestUpdateBlocksOnForeignTupleLock (operators_lockrows_test.go): the plain
// updateViaIndex/updateOp.Next(seqscan)/deleteOp.Next paths already waited
// for a still-active foreign conflicting row lock before stamping
// (waitForConflictingRowLock, M0118-0003), but updateWithFrom/deleteWithUsing
// never got the same wait wired in — a gap surfaced while closing M0119-0009
// (see the ledger row and docs/design/0118-0011-update-multixact-locker-preserving-producer.md).

func TestUpdateFromBlocksOnForeignConflictingLock(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE items_src (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	runSQL(t, ctx, "INSERT INTO items_src VALUES (1, 'fromsrc')")
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	lm := lockmgr.New()

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
	s2.Tx = s2tx
	s2.Snap = s2snap
	defer ctx.TxnMgr.Rollback(s2tx)

	done := make(chan error, 1)
	go func() {
		stmts, perr := parser.Parse("UPDATE items SET label = items_src.label FROM items_src WHERE items.id = items_src.id AND items.id = 1")
		if perr != nil {
			done <- perr
			return
		}
		node, perr := planner.Plan(stmts[0], s2.Catalog)
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
		t.Fatalf("UPDATE...FROM returned early (err=%v) — did not wait for session 1's conflicting lock", err)
	case <-time.After(300 * time.Millisecond):
		// still blocked, as expected
	}

	lm.ReleaseAll(1)
	if err := ctx.TxnMgr.Rollback(s1tx); err != nil {
		t.Fatalf("rollback session 1: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("UPDATE...FROM after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("UPDATE...FROM did not unblock after session 1 released its lock")
	}
}

func TestDeleteUsingBlocksOnForeignConflictingLock(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE items_src (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	runSQL(t, ctx, "INSERT INTO items_src VALUES (2, 'usingsrc')")
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	lm := lockmgr.New()

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
	// FOR SHARE still conflicts with a DELETE (always StatusUpdate).
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
	s2.Tx = s2tx
	s2.Snap = s2snap
	defer ctx.TxnMgr.Rollback(s2tx)

	done := make(chan error, 1)
	go func() {
		stmts, perr := parser.Parse("DELETE FROM items USING items_src WHERE items.id = items_src.id AND items.id = 2")
		if perr != nil {
			done <- perr
			return
		}
		node, perr := planner.Plan(stmts[0], s2.Catalog)
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
		t.Fatalf("DELETE...USING returned early (err=%v) — did not wait for session 1's conflicting lock", err)
	case <-time.After(300 * time.Millisecond):
		// still blocked, as expected
	}

	lm.ReleaseAll(1)
	if err := ctx.TxnMgr.Rollback(s1tx); err != nil {
		t.Fatalf("rollback session 1: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("DELETE...USING after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DELETE...USING did not unblock after session 1 released its lock")
	}
}
