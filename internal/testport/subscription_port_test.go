// Package testport — D-004 subscription TAP test ports (M0094-0004).
//
// Run all:
//
//	go test -v -run TestPort_Subscription ./internal/testport/
//
// Run one:
//
//	go test -v -run TestPort_Subscription001RepChanges ./internal/testport/
package testport

import (
	"bytes"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// TestPort_Subscription001RepChanges ports postgres/src/test/subscription/t/001_rep_changes.pl
// Upstream: the canonical logical replication smoke test — publisher + subscriber,
// verifies INSERT, UPDATE, and DELETE are replicated end-to-end.
//
// Adaptation: goopg v0 subscriber server does not auto-start the logical receiver
// on CREATE SUBSCRIPTION, so this port drives the pgoutput pipeline in-process:
// publisher catalog → PgOutput encoder → DecodeMessage → ApplyWorker → subscriber
// storage. The in-process approach exercises the identical code path that the
// wire-level receiver uses once connected.
func TestPort_Subscription001RepChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping logical replication test in short mode")
	}
	// upstream: postgres/src/test/subscription/t/001_rep_changes.pl

	tabCols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
	}
	pubCat, pubRel, snap := subMakePub(t, "tab_rep", tabCols)
	sub := newSubDB(t)
	if _, err := sub.cat.CreateTable(parser.ObjectName{Name: "tab_rep"}, tabCols); err != nil {
		t.Fatal(err)
	}
	_ = pubCat
	w := executor.NewApplyWorker(sub.cat, sub.pool, sub.txnMgr)

	drive := func(xid uint32, changes ...wal.Change) {
		t.Helper()
		subDrive(t, snap, w, xid, changes)
	}

	// INSERT (1, 10) and (2, 20) — mirrors upstream's initial INSERTs.
	drive(1, wal.Change{Kind: wal.ChangeInsert, Rel: pubRel, NewTuple: subTuple2(t, 1, 10)})
	drive(2, wal.Change{Kind: wal.ChangeInsert, Rel: pubRel, NewTuple: subTuple2(t, 2, 20)})

	rows := subScanInt2(t, sub, "tab_rep", tabCols)
	if len(rows) != 2 {
		t.Fatalf("after 2 INSERTs: got %d rows, want 2; rows=%v", len(rows), rows)
	}

	// DELETE (id=1).
	drive(3, wal.Change{Kind: wal.ChangeDelete, Rel: pubRel, OldTuple: subTuple2(t, 1, 10)})
	rows = subScanInt2(t, sub, "tab_rep", tabCols)
	if len(rows) != 1 {
		t.Fatalf("after DELETE id=1: got %d rows, want 1; rows=%v", len(rows), rows)
	}
	if rows[0][0] != 2 {
		t.Errorf("remaining row id=%d want 2", rows[0][0])
	}

	// UPDATE (id=2, a=20) → (id=2, a=99).
	drive(4, wal.Change{
		Kind:     wal.ChangeUpdate,
		Rel:      pubRel,
		OldTuple: subTuple2(t, 2, 20),
		NewTuple: subTuple2(t, 2, 99),
	})
	rows = subScanInt2(t, sub, "tab_rep", tabCols)
	if len(rows) != 1 {
		t.Fatalf("after UPDATE id=2: got %d rows, want 1", len(rows))
	}
	if rows[0][1] != 99 {
		t.Errorf("updated value a=%d want 99", rows[0][1])
	}
}

// TestPort_Subscription004Sync ports postgres/src/test/subscription/t/004_sync.pl
// Upstream: tests initial table synchronisation — CREATE SUBSCRIPTION against a
// non-empty publisher copies pre-existing rows (initial COPY phase), then
// hands off to streaming with no gaps and no duplicates.
//
// Adaptation: initial COPY is driven as a batch of INSERT messages (the
// pgoutput representation of the COPY phase). The handoff to streaming
// INSERT is tested by appending a further INSERT after the batch.
// The no-duplicate invariant is verified by counting final rows.
func TestPort_Subscription004Sync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription sync test in short mode")
	}
	// upstream: postgres/src/test/subscription/t/004_sync.pl

	tabCols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	}
	pubCat, pubRel, snap := subMakePub(t, "tab_rep", tabCols)
	sub := newSubDB(t)
	if _, err := sub.cat.CreateTable(parser.ObjectName{Name: "tab_rep"}, tabCols); err != nil {
		t.Fatal(err)
	}
	_ = pubCat
	w := executor.NewApplyWorker(sub.cat, sub.pool, sub.txnMgr)

	// Simulate initial table sync: apply N pre-existing rows as a single batch
	// (mirrors upstream's INSERT...generate_series(1,10) before subscription).
	const N = 10
	initChanges := make([]wal.Change, N)
	for i := 0; i < N; i++ {
		initChanges[i] = wal.Change{
			Kind:     wal.ChangeInsert,
			Rel:      pubRel,
			NewTuple: subTuple1(t, i+1),
		}
	}
	subDrive(t, snap, w, 1, initChanges)

	// Verify initial sync: N rows present, no gaps.
	vals := subScanInt1(t, sub, "tab_rep", tabCols)
	if len(vals) != N {
		t.Fatalf("initial sync: got %d rows, want %d", len(vals), N)
	}
	seen := make(map[int]bool)
	for _, v := range vals {
		if seen[v] {
			t.Errorf("duplicate row value %d after initial sync", v)
		}
		seen[v] = true
	}

	// Streaming INSERT after sync handoff — no gap, no duplicate.
	subDrive(t, snap, w, 2, []wal.Change{{
		Kind:     wal.ChangeInsert,
		Rel:      pubRel,
		NewTuple: subTuple1(t, N+1),
	}})
	vals = subScanInt1(t, sub, "tab_rep", tabCols)
	if len(vals) != N+1 {
		t.Fatalf("after streaming INSERT: got %d rows, want %d", len(vals), N+1)
	}
	maxVal := vals[0]
	for _, v := range vals[1:] {
		if v > maxVal {
			maxVal = v
		}
	}
	if seen[maxVal] {
		t.Errorf("streaming INSERT produced duplicate row value %d", maxVal)
	}
}

// TestPort_Subscription026Stats ports postgres/src/test/subscription/t/026_stats.pl
// Upstream: verifies pg_stat_subscription and pg_stat_replication are populated
// during active replication — subenabled=t, received_lsn non-zero,
// last_msg_receipt_time non-null.
//
// Adaptation: pg_stat_subscription is backed by wal.Subscribers (in-process
// registry). This port registers a wal.Subscriber, drives messages through
// ApplyWorker.SetStatHandle, and verifies that after a commit:
// - received_lsn is non-zero (AdvanceReceivedLSN fired on C message)
// - last_msg_receipt_time is non-zero (MarkMessage fired on each frame)
// - Subscribers.Snapshot() returns the expected entry
// This confirms the observability path that pg_stat_subscription renders.
func TestPort_Subscription026Stats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription stats test in short mode")
	}
	// upstream: postgres/src/test/subscription/t/026_stats.pl

	tabCols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	}
	pubCat, pubRel, snap := subMakePub(t, "stat_t", tabCols)
	sub := newSubDB(t)
	if _, err := sub.cat.CreateTable(parser.ObjectName{Name: "stat_t"}, tabCols); err != nil {
		t.Fatal(err)
	}
	_ = pubCat
	w := executor.NewApplyWorker(sub.cat, sub.pool, sub.txnMgr)

	// Register a subscriber in the stats registry (mirrors LogicalReceiver.Run()
	// calling subs.Register on startup). pg_stat_subscription renders this as a row.
	subs := wal.NewSubscribers()
	handle := subs.Register(wal.SubscriberState{
		SubID:   42,
		SubName: "sub_stats_test",
	})
	defer subs.Unregister(handle)
	w.SetStatHandle(handle)

	// Drive an INSERT + commit through the apply worker with a non-zero EndLSN.
	const commitLSN = uint64(0xABCD)
	var buf bytes.Buffer
	po := wal.NewPgOutput(snap, &buf)
	if err := po.Begin(storage.TransactionID(1), commitLSN); err != nil {
		t.Fatal(err)
	}
	if err := po.Change(wal.Change{
		Kind:     wal.ChangeInsert,
		Rel:      pubRel,
		NewTuple: subTuple1(t, 100),
	}); err != nil {
		t.Fatal(err)
	}
	if err := po.Commit(storage.TransactionID(1), commitLSN); err != nil {
		t.Fatal(err)
	}

	// Feed each message with EndLSN set so MarkMessage / AdvanceReceivedLSN fire.
	payload := buf.Bytes()
	for len(payload) > 0 {
		m, err := wal.DecodeMessage(payload)
		if err != nil {
			t.Fatalf("DecodeMessage: %v", err)
		}
		m.EndLSN = commitLSN // simulate publisher's end-of-WAL report
		if _, err := w.ApplyMessage(m); err != nil {
			t.Fatalf("ApplyMessage(kind=%q): %v", m.Kind, err)
		}
		payload = payload[logicalRepMsgLen(m, payload):]
	}

	// Verify stats: received_lsn non-zero, last_msg_receipt_time non-null.
	states := subs.Snapshot()
	if len(states) != 1 {
		t.Fatalf("Snapshot: got %d entries, want 1", len(states))
	}
	st := states[0]
	if st.SubName != "sub_stats_test" {
		t.Errorf("SubName=%q want sub_stats_test", st.SubName)
	}
	// Mirrors upstream's check that received_lsn > 0 in pg_stat_subscription.
	if st.ReceivedLSN == 0 {
		t.Error("ReceivedLSN=0: pg_stat_subscription.received_lsn should be non-zero after commit")
	}
	// Mirrors upstream's check that last_msg_receipt_time is non-null.
	if st.LastMsgReceiptTime.IsZero() {
		t.Error("LastMsgReceiptTime is zero: pg_stat_subscription.last_msg_receipt_time should be non-null")
	}
	t.Logf("received_lsn=0x%X last_msg_receipt_time=%v",
		st.ReceivedLSN, st.LastMsgReceiptTime.Format(time.RFC3339))
}

// --- helpers shared within the subscription port tests ---

// subDB is a minimal in-process subscriber database.
type subDB struct {
	cat    catalog.Catalog
	pool   *storage.Pool
	txnMgr *mvcc.Manager
}

func newSubDB(t *testing.T) *subDB {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return &subDB{
		cat:    catalog.NewInMemory(),
		pool:   pool,
		txnMgr: mvcc.NewManager(),
	}
}

// subMakePub creates a publisher catalog + relation + catalog snapshot.
func subMakePub(t *testing.T, tableName string, cols []catalog.Column) (catalog.Catalog, storage.RelFileNode, *wal.CatalogSnapshot) {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: tableName}, cols)
	if err != nil {
		t.Fatal(err)
	}
	return cat, cat.RelFileNode(tbl), wal.BuildCatalogSnapshot(cat, nil)
}

// subDrive applies a batch of changes as one transaction to the apply worker.
func subDrive(t *testing.T, snap *wal.CatalogSnapshot, w *executor.ApplyWorker, xid uint32, changes []wal.Change) {
	t.Helper()
	commitLSN := uint64(xid) * 100

	applyPayload := func(payload []byte) {
		t.Helper()
		for len(payload) > 0 {
			m, err := wal.DecodeMessage(payload)
			if err != nil {
				t.Fatalf("DecodeMessage: %v", err)
			}
			if _, err := w.ApplyMessage(m); err != nil {
				t.Fatalf("ApplyMessage(kind=%q): %v", m.Kind, err)
			}
			payload = payload[logicalRepMsgLen(m, payload):]
		}
	}

	encode := func(fn func(*wal.PgOutput) error) []byte {
		t.Helper()
		var buf bytes.Buffer
		po := wal.NewPgOutput(snap, &buf)
		if err := fn(po); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	applyPayload(encode(func(po *wal.PgOutput) error {
		return po.Begin(storage.TransactionID(xid), commitLSN)
	}))
	for _, c := range changes {
		applyPayload(encode(func(po *wal.PgOutput) error { return po.Change(c) }))
	}
	applyPayload(encode(func(po *wal.PgOutput) error {
		return po.Commit(storage.TransactionID(xid), commitLSN)
	}))
}

// subTuple1 encodes a single int4 column as a v0 heap tuple.
func subTuple1(t *testing.T, v int) []byte {
	t.Helper()
	body := logicalRepEncodeBody([]any{v}, []string{"int4"})
	ht := storage.NewHeapTuple(1, 0, body)
	ht.Header.SetNatts(1)
	tup, err := ht.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return tup
}

// subTuple2 encodes two int4 columns as a v0 heap tuple.
func subTuple2(t *testing.T, v1, v2 int) []byte {
	t.Helper()
	body := logicalRepEncodeBody([]any{v1, v2}, []string{"int4", "int4"})
	ht := storage.NewHeapTuple(1, 0, body)
	ht.Header.SetNatts(2)
	tup, err := ht.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return tup
}

// subScanInt1 scans a single-int4-column table and returns the values.
func subScanInt1(t *testing.T, db *subDB, tableName string, cols []catalog.Column) []int {
	t.Helper()
	tbl, _ := db.cat.LookupTable(parser.ObjectName{Name: tableName})
	if tbl == nil {
		t.Fatalf("subScanInt1: table %q not found", tableName)
	}
	rel := db.cat.RelFileNode(tbl)
	return scanIntCol(t, db, rel, cols, 0)
}

// subScanInt2 scans a two-int4-column table and returns (col0, col1) pairs.
func subScanInt2(t *testing.T, db *subDB, tableName string, cols []catalog.Column) [][2]int {
	t.Helper()
	tbl, _ := db.cat.LookupTable(parser.ObjectName{Name: tableName})
	if tbl == nil {
		t.Fatalf("subScanInt2: table %q not found", tableName)
	}
	rel := db.cat.RelFileNode(tbl)
	tx, err := db.txnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	defer db.txnMgr.Rollback(tx)
	snap, _ := db.txnMgr.SnapshotFor(tx)

	var out [][2]int
	nBlocks, _ := db.pool.NBlocks(rel)
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, _ := db.pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			db.pool.Unpin(s)
			continue
		}
		count, _ := storage.PageLinePointerCount(page)
		for slot := uint16(1); slot <= uint16(count); slot++ {
			tup, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if !mvcc.TupleVisible(tup.Header, snap, tx.XID, storage.InvalidCommandId, nil, nil) {
				continue
			}
			row, _ := executor.DecodeRow(cols, tup.Data)
			if len(row) >= 2 {
				out = append(out, [2]int{int(row[0].Int), int(row[1].Int)})
			}
		}
		s.RUnlock()
		db.pool.Unpin(s)
	}
	return out
}

// scanIntCol scans the heap of rel and returns the int value of colIdx for each visible row.
func scanIntCol(t *testing.T, db *subDB, rel storage.RelFileNode, cols []catalog.Column, colIdx int) []int {
	t.Helper()
	tx, err := db.txnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	defer db.txnMgr.Rollback(tx)
	snap, _ := db.txnMgr.SnapshotFor(tx)

	var out []int
	nBlocks, _ := db.pool.NBlocks(rel)
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, _ := db.pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			db.pool.Unpin(s)
			continue
		}
		count, _ := storage.PageLinePointerCount(page)
		for slot := uint16(1); slot <= uint16(count); slot++ {
			tup, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if !mvcc.TupleVisible(tup.Header, snap, tx.XID, storage.InvalidCommandId, nil, nil) {
				continue
			}
			row, _ := executor.DecodeRow(cols, tup.Data)
			if len(row) > colIdx {
				out = append(out, int(row[colIdx].Int))
			}
		}
		s.RUnlock()
		db.pool.Unpin(s)
	}
	return out
}
