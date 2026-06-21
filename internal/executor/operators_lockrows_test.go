package executor

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
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
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, slot.Row())
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

// TestLockRowsNoWaitSucceedsUncontended — NOWAIT is supported
// (M0021-0003 follow-up): when the relation lock is immediately
// grantable, NOWAIT acquires it and proceeds normally.
func TestLockRowsNoWaitSucceedsUncontended(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.LockMgr = lockmgr.New()
	ctx.BackendID = 1
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	_ = catalog.Catalog(cat)
	if _, err := runForUpdate(t, ctx, "SELECT id FROM items FOR UPDATE NOWAIT"); err != nil {
		t.Fatalf("uncontended NOWAIT: %v", err)
	}
}

// TestLockRowsNoWaitFailsOnContention — when another backend
// holds a conflicting lock, NOWAIT surfaces SQLSTATE 55P03
// instead of waiting. Pins the canonical upstream diagnostic
// for "could not obtain lock immediately".
func TestLockRowsNoWaitFailsOnContention(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	// Seed BEFORE wiring the lockmgr — keeps the seed INSERT
	// from leaving a RowExclusiveLock held on backend 0 that
	// would block our blocker's ExclusiveLock acquisition.
	seedItems(t, ctx, tbl)

	lm := lockmgr.New()
	rel := ctx.Catalog.RelFileNode(tbl)
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	if err := lm.Acquire(context.Background(), 1, tag, lockmgr.ExclusiveLock); err != nil {
		t.Fatalf("blocker Acquire: %v", err)
	}
	defer lm.Release(1, tag, lockmgr.ExclusiveLock)

	ctx.LockMgr = lm
	ctx.BackendID = 2
	_, err := runForUpdate(t, ctx, "SELECT id FROM items FOR UPDATE NOWAIT")
	if err == nil {
		t.Fatal("expected 55P03 on contended NOWAIT, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err = %T, want *ExecError", err)
	}
	if ee.Code != "55P03" {
		t.Errorf("code = %q, want 55P03", ee.Code)
	}
}

// TestTupleLockConflicts pins the row-lock conflict matrix used by NOWAIT's
// cross-statement conflict detection (M0118-0003). FOR UPDATE
// (HeapXmaxExclLock) conflicts with every held row lock; a shared request
// (FOR SHARE / KEY SHARE) conflicts only with a pure-exclusive FOR UPDATE
// holder; an unlocked (no-lock-bits) infomask never conflicts.
func TestTupleLockConflicts(t *testing.T) {
	const (
		excl   = storage.HeapXmaxExclLock   // FOR UPDATE
		shr    = storage.HeapXmaxShrLock    // FOR SHARE
		keyshr = storage.HeapXmaxKeyShrLock // FOR KEY SHARE
	)
	cases := []struct {
		name      string
		requested uint16
		held      uint16
		want      bool
	}{
		{"update vs none", excl, 0, false},
		{"update vs update", excl, excl, true},
		{"update vs share", excl, shr, true},
		{"update vs keyshare", excl, keyshr, true},
		{"share vs update", shr, excl, true},
		{"share vs share", shr, shr, false},
		{"share vs keyshare", shr, keyshr, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tupleLockConflicts(tc.requested, tc.held); got != tc.want {
				t.Errorf("tupleLockConflicts(%#x, %#x) = %v, want %v",
					tc.requested, tc.held, got, tc.want)
			}
		})
	}
}

// TestLockRowsStampsTupleLockOnlyXmax — the headline tuple-
// level locking step 2 contract. SELECT FOR UPDATE pulls each
// scanned row through lockRowsOp's two-pass drain-then-stamp
// path; after EOF, the underlying heap tuple carries
// xmax = our xid + HeapXmaxLockOnly + HeapXmaxExclLock infomask
// bits. Mirrors the on-page state upstream's heap_lock_tuple
// produces.
func TestLockRowsStampsTupleLockOnlyXmax(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.LockMgr = lockmgr.New()
	ctx.BackendID = 1
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	rows, err := runForUpdate(t, ctx, "SELECT id FROM items FOR UPDATE")
	if err != nil {
		t.Fatalf("runForUpdate: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("FOR UPDATE returned no rows; expected 3 from seedItems")
	}
	// Page state: every visible row was stamped with xmax == cur
	// + HeapXmaxLockOnly + HeapXmaxExclLock. Read block 0 back
	// and verify each line pointer's tuple infomask.
	rel := ctx.Catalog.RelFileNode(tbl)
	pinned, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Pool.Unpin(pinned)
	pinned.RLock()
	defer pinned.RUnlock()
	page := pinned.Page()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatal(err)
	}
	stampedCount := 0
	for slot := uint16(1); slot <= uint16(count); slot++ {
		raw, err := storage.PageGetItemRaw(page, slot)
		if err != nil {
			continue
		}
		parsed, err := storage.ParseHeapTuple(raw)
		if err != nil {
			continue
		}
		if parsed.Header.Xmax != ctx.Tx.XID {
			continue
		}
		if parsed.Header.Infomask&storage.HeapXmaxLockOnly == 0 {
			t.Errorf("slot %d: xmax stamped but HeapXmaxLockOnly not set (Infomask=%#x)", slot, parsed.Header.Infomask)
			continue
		}
		if parsed.Header.Infomask&storage.HeapXmaxExclLock == 0 {
			t.Errorf("slot %d: HeapXmaxExclLock not set (Infomask=%#x)", slot, parsed.Header.Infomask)
			continue
		}
		stampedCount++
	}
	if stampedCount != len(rows) {
		t.Errorf("stamped %d tuples, want %d (FOR UPDATE returned that many)", stampedCount, len(rows))
	}
}

// TestUpdateBlocksOnForeignTupleLock — the headline tuple-level
// step 2b property: a SELECT FOR UPDATE in session 1 stamps a
// tuple lock; a UPDATE in session 2 hitting the same row blocks
// in the lockmgr until session 1's xact ends. We verify by
// running the UPDATE in a goroutine, asserting it registers as a
// waiter on the tuple-level tag, then releasing session 1's
// holdings (LockMgr.ReleaseAll) and confirming the goroutine
// completes.
//
// The fixture seeds rows under a separately-committed xact (so
// sessions 1 and 2 see them as live), then begins independent
// xacts for each session. Without that separation, session 2's
// snapshot would treat the still-running seed xact as
// in-progress and the tuple would be invisible — the conflict
// detection would never fire because no row would be reached.
func TestUpdateBlocksOnForeignTupleLock(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// Seed under the fixture xact and commit so future xacts see
	// the rows. Replace ctx.Tx/Snap with fresh post-commit ones —
	// the rest of this test never reuses the seed xact.
	seedItems(t, ctx, tbl)
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	lm := lockmgr.New()

	// Session 1: SELECT FOR UPDATE — stamps tuple lock on every
	// returned row.
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

	// Session 2: UPDATE the same row.
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
		stmts, err := parser.Parse("UPDATE items SET label = 'updated' WHERE id = 1")
		if err != nil {
			done <- err
			return
		}
		node, err := planner.Plan(stmts[0], s2.Catalog)
		if err != nil {
			done <- err
			return
		}
		op, err := Build(node)
		if err != nil {
			done <- err
			return
		}
		if err := op.Open(s2); err != nil {
			done <- err
			return
		}
		_, err = op.Next()
		_ = op.Close()
		if err != nil && err != EOF {
			done <- err
			return
		}
		done <- nil
	}()

	rel := ctx.Catalog.RelFileNode(tbl)
	deadline := time.Now().Add(2 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		// seedItems inserts 3 rows on block 0. id=1 is the first
		// row, slot 1 — encoded as Block=1, Offset=2 by
		// tupleLockTag's +1 shift.
		w := lm.Waiters(lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid, Block: 1, Offset: 2})
		if len(w) > 0 {
			registered = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !registered {
		lm.ReleaseAll(1)
		<-done
		t.Fatal("session 2 did not register as a tuple-tag waiter within 2s")
	}

	// Release session 1's transaction-scoped locks; session 2
	// should now wake and complete.
	lm.ReleaseAll(1)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("UPDATE after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session 2 did not unblock after release")
	}
}

// TestLockRowsStampsLockOnlyXmaxIndexScan — M0021 step 2c
// counterpart to TestLockRowsStampsTupleLockOnlyXmax. With a
// unique index on items.id, the planner picks IndexScan for
// `WHERE id = N`; lockRowsOp must traverse past Project →
// IndexScan via findScanLeaf and stamp lock-only xmax on the
// row IndexScan emits. Pins the per-row stamping property
// regardless of whether the executor reached the row via
// SeqScan or IndexScan.
func TestLockRowsStampsLockOnlyXmaxIndexScan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX items_pkey_idxscan ON items (id)"); err != nil {
		t.Fatal(err)
	}

	ctx.LockMgr = lockmgr.New()
	ctx.BackendID = 1

	rows, err := runForUpdate(t, ctx, "SELECT id FROM items WHERE id = 2 FOR UPDATE")
	if err != nil {
		t.Fatalf("runForUpdate: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("FOR UPDATE returned %d rows, want 1 (id=2)", len(rows))
	}

	rel := ctx.Catalog.RelFileNode(tbl)
	pinned, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Pool.Unpin(pinned)
	pinned.RLock()
	defer pinned.RUnlock()
	page := pinned.Page()
	count, _ := storage.PageLinePointerCount(page)
	stampedCount := 0
	for slot := uint16(1); slot <= uint16(count); slot++ {
		raw, err := storage.PageGetItemRaw(page, slot)
		if err != nil {
			continue
		}
		parsed, err := storage.ParseHeapTuple(raw)
		if err != nil {
			continue
		}
		if parsed.Header.Xmax != ctx.Tx.XID {
			continue
		}
		if parsed.Header.Infomask&storage.HeapXmaxLockOnly == 0 {
			t.Errorf("slot %d: xmax stamped but HeapXmaxLockOnly not set (Infomask=%#x)", slot, parsed.Header.Infomask)
			continue
		}
		stampedCount++
	}
	if stampedCount != 1 {
		t.Errorf("stamped %d tuples, want 1 (only id=2 should be locked via index probe)", stampedCount)
	}
}

// TestUpdateViaIndexScanBlocksOnForeignTupleLock — M0021 step 2d
// integration. With a unique index on items.id, planUpdate picks
// the IndexScan path for `UPDATE … WHERE id = N`. The runtime
// still goes through scanMatching (which seq-scans the heap)
// but the per-tuple foreign-lock detection from step 2b must
// continue to fire — the IndexScan promotion shouldn't bypass
// the blocking. Verifies by holding a SELECT FOR UPDATE in
// session 1 and asserting that session 2's UPDATE registers as
// a tuple-tag waiter when the planner picks IndexScan.
func TestUpdateViaIndexScanBlocksOnForeignTupleLock(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX items_pk_2d_block ON items (id)"); err != nil {
		t.Fatal(err)
	}
	// Commit the seed so subsequent xacts see the rows.
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	lm := lockmgr.New()

	s1tx, _ := ctx.TxnMgr.Begin(0)
	s1snap, _ := ctx.TxnMgr.SnapshotFor(s1tx)
	s1 := makeCtx(lm, 1)
	s1.Pool = ctx.Pool
	s1.Catalog = ctx.Catalog
	s1.TxnMgr = ctx.TxnMgr
	s1.Tx = s1tx
	s1.Snap = s1snap
	if _, err := runForUpdate(t, s1, "SELECT id FROM items WHERE id = 1 FOR UPDATE"); err != nil {
		t.Fatalf("session-1 FOR UPDATE: %v", err)
	}

	s2tx, _ := ctx.TxnMgr.Begin(0)
	s2snap, _ := ctx.TxnMgr.SnapshotFor(s2tx)
	s2 := makeCtx(lm, 2)
	s2.Pool = ctx.Pool
	s2.Catalog = ctx.Catalog
	s2.TxnMgr = ctx.TxnMgr
	s2.Tx = s2tx
	s2.Snap = s2snap
	defer ctx.TxnMgr.Rollback(s2tx)

	done := make(chan error, 1)
	go func() {
		stmts, err := parser.Parse("UPDATE items SET label = 'idx-updated' WHERE id = 1")
		if err != nil {
			done <- err
			return
		}
		node, err := planner.Plan(stmts[0], s2.Catalog)
		if err != nil {
			done <- err
			return
		}
		op, err := Build(node)
		if err != nil {
			done <- err
			return
		}
		if err := op.Open(s2); err != nil {
			done <- err
			return
		}
		_, err = op.Next()
		_ = op.Close()
		if err != nil && err != EOF {
			done <- err
			return
		}
		done <- nil
	}()

	rel := ctx.Catalog.RelFileNode(tbl)
	deadline := time.Now().Add(2 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		w := lm.Waiters(lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid, Block: 1, Offset: 2})
		if len(w) > 0 {
			registered = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !registered {
		lm.ReleaseAll(1)
		<-done
		t.Fatal("session 2 did not register as a tuple-tag waiter via the IndexScan-driven UPDATE path")
	}
	lm.ReleaseAll(1)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("UPDATE after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session 2 did not unblock after release")
	}
}

// TestForShareCompatibleMultipleHolders — M0021 step 4:
// multiple SELECT FOR SHARE sessions on the same row should
// coexist without blocking, and a subsequent UPDATE / FOR
// UPDATE should wait for all of them to release. Achieves
// PostgreSQL FOR SHARE multi-holder semantics via lockmgr
// modes (RowShareLock vs ExclusiveLock) without needing
// MultiXact infrastructure: RowShareLock is compatible with
// itself and conflicts with ExclusiveLock per the lockmgr's
// conflict matrix.
func TestForShareCompatibleMultipleHolders(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	lm := lockmgr.New()

	// Two sessions concurrently take FOR SHARE on the same row.
	s1tx, _ := ctx.TxnMgr.Begin(0)
	s1snap, _ := ctx.TxnMgr.SnapshotFor(s1tx)
	s1 := makeCtx(lm, 1)
	s1.Pool = ctx.Pool
	s1.Catalog = ctx.Catalog
	s1.TxnMgr = ctx.TxnMgr
	s1.Tx = s1tx
	s1.Snap = s1snap

	s2tx, _ := ctx.TxnMgr.Begin(0)
	s2snap, _ := ctx.TxnMgr.SnapshotFor(s2tx)
	s2 := makeCtx(lm, 2)
	s2.Pool = ctx.Pool
	s2.Catalog = ctx.Catalog
	s2.TxnMgr = ctx.TxnMgr
	s2.Tx = s2tx
	s2.Snap = s2snap

	if _, err := runForUpdate(t, s1, "SELECT id FROM items WHERE id = 1 FOR SHARE"); err != nil {
		t.Fatalf("session-1 FOR SHARE: %v", err)
	}
	// Second FOR SHARE must succeed without blocking — pin
	// against the multi-holder property by running it
	// synchronously and asserting it returns promptly. If the
	// first FOR SHARE's lock blocked the second, the test would
	// hang.
	if _, err := runForUpdate(t, s2, "SELECT id FROM items WHERE id = 1 FOR SHARE"); err != nil {
		t.Fatalf("session-2 FOR SHARE (must be compatible): %v", err)
	}

	// Both sessions hold the tuple tag; verify the tag has 2
	// holders (RowShareLock from each backend).
	rel := ctx.Catalog.RelFileNode(tbl)
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid, Block: 1, Offset: 2}
	holders := lm.Holders(tag)
	if len(holders) != 2 {
		t.Fatalf("Holders=%d, want 2 (both FOR SHARE sessions)", len(holders))
	}

	// A third session attempting UPDATE on the same row must
	// block until BOTH FOR SHARE holders release.
	s3tx, _ := ctx.TxnMgr.Begin(0)
	s3snap, _ := ctx.TxnMgr.SnapshotFor(s3tx)
	s3 := makeCtx(lm, 3)
	s3.Pool = ctx.Pool
	s3.Catalog = ctx.Catalog
	s3.TxnMgr = ctx.TxnMgr
	s3.Tx = s3tx
	s3.Snap = s3snap
	defer ctx.TxnMgr.Rollback(s3tx)

	done := make(chan error, 1)
	go func() {
		stmts, _ := parser.Parse("UPDATE items SET label = 'updated' WHERE id = 1")
		node, err := planner.Plan(stmts[0], s3.Catalog)
		if err != nil {
			done <- err
			return
		}
		op, _ := Build(node)
		if err := op.Open(s3); err != nil {
			done <- err
			return
		}
		_, err = op.Next()
		_ = op.Close()
		if err != nil && err != EOF {
			done <- err
			return
		}
		done <- nil
	}()

	// Wait briefly for session 3 to register as a waiter.
	deadline := time.Now().Add(2 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		if len(lm.Waiters(tag)) >= 1 {
			registered = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !registered {
		lm.ReleaseAll(1)
		lm.ReleaseAll(2)
		<-done
		t.Fatal("session 3 UPDATE did not register as waiter")
	}

	// Release session 1; session 3 must still be blocked
	// (session 2 still holds RowShareLock).
	lm.ReleaseAll(1)
	select {
	case err := <-done:
		t.Fatalf("session 3 unblocked too early — session 2 still holds: %v", err)
	case <-time.After(100 * time.Millisecond):
		// expected — still blocked
	}
	// Release session 2; session 3 should now wake.
	lm.ReleaseAll(2)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("session 3 UPDATE: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session 3 did not unblock after both holders released")
	}
}

// TestLockRowsSkipLockedNoContention — SKIP LOCKED is now honored
// (M0118-0003). With no concurrent locker there is nothing to skip,
// so `FOR UPDATE SKIP LOCKED` must succeed and return every row, just
// like plain FOR UPDATE. The per-row skip semantics (dropping rows a
// concurrent transaction holds) are exercised end-to-end by the
// skip-locked isolation spec (TestPort_IsolationSkipLocked).
func TestLockRowsSkipLockedNoContention(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.LockMgr = lockmgr.New()
	ctx.BackendID = 1
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	_ = catalog.Catalog(cat)
	rows, err := runForUpdate(t, ctx, "SELECT id FROM items FOR UPDATE SKIP LOCKED")
	if err != nil {
		t.Fatalf("SKIP LOCKED with no contention: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("rows = %d, want 3 (nothing locked → nothing skipped)", len(rows))
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
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	// Seed BEFORE wiring the lockmgr so the seed insert doesn't
	// leave a RowExclusiveLock that conflicts with backend 1's
	// ExclusiveLock acquisition below.
	seedItems(t, ctx, tbl)

	lm := lockmgr.New()
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

// TestLockRowsStampsXmaxOnPartitionedTableLeaf verifies that a FOR UPDATE
// on a partitioned parent table correctly stamps the lock-only xmax on the
// heap tuple in the leaf partition, not just on the parent.
//
// Before the M0100-0005 follow-up fix, findScanLeaf returned nil for setOp
// (the UNION ALL that scans leaf partitions), so drainAndStamp skipped the
// per-row stamp pass — leaving the leaf tuple's xmax at InvalidTransactionID.
// findInProgressConflict Case 3 then missed the FOR UPDATE lock, causing a
// concurrent INSERT ON CONFLICT to proceed without waiting (as observed in
// TestPort_IsolationInsertConflictDoUpdate4).
func TestLockRowsStampsXmaxOnPartitionedTableLeaf(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.LockMgr = lockmgr.New()
	ctx.BackendID = 1

	im := cat.(*catalog.InMemory)

	// Create parent partitioned table and a single leaf partition.
	parent, err := im.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "val", Type: catalog.Type{Name: "text"}},
	})
	if err != nil {
		t.Fatalf("CreateTable parent: %v", err)
	}
	parent.PartitionKey = []string{"id"}

	leaf, err := im.CreateTable(parser.ObjectName{Name: "part_1"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "val", Type: catalog.Type{Name: "text"}},
	})
	if err != nil {
		t.Fatalf("CreateTable leaf: %v", err)
	}
	im.RegisterPartitionChild(parent.OID, leaf.OID)

	// Insert a row into the leaf partition.
	leafRel := cat.RelFileNode(leaf)
	ptr, err := writeHeapRowReturning(ctx, leafRel, leaf.Columns, Row{
		{Kind: KindInt, Int: 1},
		{Kind: KindString, Buf: []byte("hello")},
	})
	if err != nil {
		t.Fatalf("writeHeapRowReturning: %v", err)
	}
	_ = ptr

	// Run SELECT * FROM part FOR UPDATE.
	rows, err := runForUpdate(t, ctx, "SELECT * FROM part FOR UPDATE")
	if err != nil {
		t.Fatalf("runForUpdate: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row from FOR UPDATE, got %d", len(rows))
	}

	// Verify the leaf tuple has HeapXmaxLockOnly stamped.
	pinned, err := ctx.Pool.Pin(storage.BufferTag{Rel: leafRel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	pinned.RLock()
	tuple, err := storage.PageGetHeapTuple(pinned.Page(), 1)
	pinned.RUnlock()
	ctx.Pool.Unpin(pinned)
	if err != nil {
		t.Fatalf("PageGetHeapTuple: %v", err)
	}
	if tuple.Header.Xmax == storage.InvalidTransactionID {
		t.Error("leaf tuple xmax is still InvalidTransactionID — FOR UPDATE did not stamp the leaf")
	}
	if tuple.Header.Xmax != ctx.Tx.XID {
		t.Errorf("leaf tuple xmax=%d, want our XID=%d", tuple.Header.Xmax, ctx.Tx.XID)
	}
	if tuple.Header.Infomask&storage.HeapXmaxLockOnly == 0 {
		t.Errorf("HeapXmaxLockOnly not set on leaf tuple (Infomask=%#x)", tuple.Header.Infomask)
	}
}
