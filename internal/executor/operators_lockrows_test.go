package executor

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// runForUpdate plans + executes a SELECT … FOR UPDATE statement
// end-to-end through parser→analyzer→planner→executor. Returns
// the rows the SELECT produced (rows are useful for verifying
// FOR UPDATE doesn't change row shape) and any error.
func runForUpdate(t *testing.T, ctx *Context, sql string) ([]Row, error) {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		return nil, err
	}
	op, err := Build(node)
	if err != nil {
		return nil, err
	}
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return nil, err
	}
	defer op.Close()
	var rows []Row
	for {
		r, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// TestLockRowsAcquiresRowShareLock — the headline correctness
// property of Stage A: opening a `SELECT … FOR UPDATE` plan
// acquires `RowShareLock` on the target relation, recorded by
// the lock manager so concurrent schema-change operations
// (which take AccessExclusiveLock) will conflict.
func TestLockRowsAcquiresRowShareLock(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	lm := lockmgr.New()
	ctx.LockMgr = lm
	ctx.BackendID = 1
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	if _, err := runForUpdate(t, ctx, "SELECT id FROM items WHERE id = 1 FOR UPDATE"); err != nil {
		t.Fatalf("runForUpdate: %v", err)
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	holders := lm.Holders(tag)
	if len(holders) == 0 {
		t.Fatalf("Holders=%v, want at least 1", holders)
	}
	// The locked relation should report RowShareLock granted to
	// our backend. Holders is a map[BackendID]Mask; check the
	// RowShareLock bit is set on backend 1.
	if mask, ok := holders[1]; !ok || mask&(1<<lockmgr.RowShareLock) == 0 {
		t.Errorf("backend 1 mask=0b%b, want RowShareLock bit set", mask)
	}
}

// TestLockRowsForShareAlsoUsesRowShareLock — both FOR UPDATE
// and FOR SHARE acquire RowShareLock at the relation level
// (matches upstream — tuple-level distinguishing lands in the
// follow-up tuple-locking task). Pins the documented Stage A
// scope.
func TestLockRowsForShareAlsoUsesRowShareLock(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	lm := lockmgr.New()
	ctx.LockMgr = lm
	ctx.BackendID = 1
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	if _, err := runForUpdate(t, ctx, "SELECT id FROM items FOR SHARE"); err != nil {
		t.Fatalf("runForUpdate: %v", err)
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	if mask, ok := lm.Holders(tag)[1]; !ok || mask&(1<<lockmgr.RowShareLock) == 0 {
		t.Errorf("FOR SHARE backend 1 mask=0b%b, want RowShareLock bit set", mask)
	}
}

// TestLockRowsRejectsNoWait — Stage A scope guard: NOWAIT and
// SKIP LOCKED parse and analyze, but the executor refuses to
// silently downgrade to default-blocking. Returns SQLSTATE 0A000
// pointing at the locking clause so users see the specific
// "Stage A executor follow-up" message.
func TestLockRowsRejectsNoWait(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.LockMgr = lockmgr.New()
	ctx.BackendID = 1
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	_ = catalog.Catalog(cat)
	if _, err := runForUpdate(t, ctx, "SELECT id FROM items FOR UPDATE NOWAIT"); err == nil {
		t.Fatal("expected NOWAIT rejection, got nil")
	} else {
		ee, ok := err.(*ExecError)
		if !ok {
			t.Fatalf("err = %T, want *ExecError", err)
		}
		if ee.Code != "0A000" {
			t.Errorf("code = %q, want 0A000", ee.Code)
		}
	}
}

// TestLockRowsBlocksOnExclusiveLock — Stage A's coarse-grained
// blocking guarantee: a separate backend holding ExclusiveLock
// on the items relation makes our SELECT FOR UPDATE wait for
// the lock to be released. We verify by holding the lock,
// running FOR UPDATE in a goroutine, asserting it waits, then
// releasing the lock and verifying the goroutine completes.
func TestLockRowsBlocksOnExclusiveLock(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	lm := lockmgr.New()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	ctx.LockMgr = lm
	ctx.BackendID = 2

	// Backend 1 holds ExclusiveLock on items — blocks anyone
	// trying to acquire RowShareLock (RowShareLock conflicts
	// with ExclusiveLock per upstream's lock matrix).
	rel := ctx.Catalog.RelFileNode(tbl)
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	if err := lm.Acquire(context.Background(), 1, tag, lockmgr.ExclusiveLock); err != nil {
		t.Fatalf("blocker Acquire: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := runForUpdate(t, ctx, "SELECT id FROM items FOR UPDATE")
		done <- err
	}()

	// Wait briefly for the goroutine to register as a waiter.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(lm.Waiters(tag)) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := lm.Waiters(tag); len(got) != 1 {
		// Release blocker so the goroutine can finish even on
		// a failed assertion — keeps the test from hanging.
		lm.Release(1, tag, lockmgr.ExclusiveLock)
		<-done
		t.Fatalf("Waiters=%v, want 1 waiter (lockRowsOp pending)", got)
	}

	// Release blocker; the goroutine should now complete.
	lm.Release(1, tag, lockmgr.ExclusiveLock)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("FOR UPDATE after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("goroutine did not unblock after lock release")
	}
}
