package testport

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/replcluster"
	"github.com/goopg/goopg/internal/wal"
)

// TestE2E_PhysicalReplication tests a primary ↔ standby pair end-to-end.
// The table is created via PreCloneHook (before the standby data-dir copy),
// so it is present on the standby from the start. After streaming begins,
// an INSERT on the primary is verified to appear on the standby.
func TestE2E_PhysicalReplication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replication test in short mode")
	}

	baseDir := t.TempDir()
	rc, err := replcluster.New("e2e_phys_repl", replcluster.Options{
		RepoRoot:     repoRoot(t),
		BaseDir:      baseDir,
		SlotName:     "e2e_phys_slot",
		StartupWait:  30 * time.Second,
		ShutdownWait: 10 * time.Second,
		PreCloneHook: func(primary *cluster.Cluster) error {
			_, err := primary.Query(context.Background(), "CREATE TABLE repl_t (id int)")
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := rc.Setup(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = rc.Stop()
	}()

	// Insert on primary after standby is streaming.
	if err := runSQLSimple(t, rc.Primary, "INSERT INTO repl_t VALUES (42)"); err != nil {
		t.Fatal(err)
	}

	// Wait up to 15 s for standby to replay the insert.
	var lastErr error
	for i := 0; i < 30; i++ {
		time.Sleep(500 * time.Millisecond)
		rows, err := rc.Standby.Query(context.Background(), "SELECT id FROM repl_t WHERE id = 42")
		if err == nil && len(rows) > 0 && rows[0][0] == "42" {
			return
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		t.Fatalf("standby never saw the row after ~15s: %v", lastErr)
	}
	t.Fatal("standby never saw the row after ~15s: timeout")
}

// TestE2E_LogicalReplication tests the logical replication pipeline
// end-to-end: INSERT + DELETE + UPDATE on the publisher side, verified
// on the subscriber side.
//
// Uses in-process publisher and subscriber instances (no separate
// cluster processes). Pipeline exercised:
// PgOutput encoder → pgoutput wire bytes → DecodeMessage →
// ApplyWorker → subscriber storage → direct read-back.
func TestE2E_LogicalReplication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	// Publisher schema (catalog only — no server process needed).
	pubCat := catalog.NewInMemory()
	pubCols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "val", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}
	pubTbl, err := pubCat.CreateTable(parser.ObjectName{Name: "pub_t"}, pubCols)
	if err != nil {
		t.Fatal(err)
	}
	pubRel := pubCat.RelFileNode(pubTbl)
	snap := wal.BuildCatalogSnapshot(pubCat)

	// Subscriber catalog + storage + transaction manager + apply worker.
	subDir := t.TempDir()
	subMgr := storage.NewManager(storage.ManagerConfig{DataDir: subDir})
	defer subMgr.Close()
	subPool, err := storage.NewPool(subMgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer subPool.Close()
	subCat := catalog.NewInMemory()
	if _, err := subCat.CreateTable(parser.ObjectName{Name: "pub_t"}, pubCols); err != nil {
		t.Fatal(err)
	}
	subTxnMgr := mvcc.NewManager()
	applyWorker := executor.NewApplyWorker(subCat, subPool, subTxnMgr)

	// applyMsg decodes and applies a single pgoutput message.
	applyMsg := func(t *testing.T, payload []byte) {
		t.Helper()
		m, err := wal.DecodeMessage(payload)
		if err != nil {
			t.Fatalf("DecodeMessage: %v", err)
		}
		if _, err := applyWorker.ApplyMessage(m); err != nil {
			t.Fatalf("ApplyMessage(kind=%q): %v", m.Kind, err)
		}
	}

	// emitMsg encodes one pgoutput message into its own buffer using fn.
	emitMsg := func(t *testing.T, fn func(po *wal.PgOutput) error) []byte {
		t.Helper()
		var buf bytes.Buffer
		po := wal.NewPgOutput(snap, &buf)
		if err := fn(po); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	// emitChange encodes a Change; the first call for a relation also
	// emits an 'R' message ahead of the change kind byte.
	emitChange := func(t *testing.T, c wal.Change) []byte {
		return emitMsg(t, func(po *wal.PgOutput) error { return po.Change(c) })
	}

	// makeTuple encodes a row as a v0 heap-tuple byte slice suitable
	// for OldTuple / NewTuple in a wal.Change.
	makeTuple := func(t *testing.T, id int, val string) []byte {
		t.Helper()
		body := logicalRepEncodeBody([]any{id, val}, []string{"int4", "text"})
		tup, err := storage.NewHeapTuple(1, 0, body).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		return tup
	}

	// driveXact wraps changes in Begin → changes → Commit, applying each
	// pgoutput message one at a time to the apply worker.
	driveXact := func(t *testing.T, xid uint32, changes [][]byte) {
		t.Helper()
		applyMsg(t, emitMsg(t, func(po *wal.PgOutput) error {
			return po.Begin(storage.TransactionID(xid), uint64(xid)*100)
		}))
		for _, payload := range changes {
			// The change payload may start with 'R' (relation) followed
			// by the actual change kind. Split and apply each message.
			for len(payload) > 0 {
				m, err := wal.DecodeMessage(payload)
				if err != nil {
					t.Fatalf("DecodeMessage in driveXact: %v", err)
				}
				if _, err := applyWorker.ApplyMessage(m); err != nil {
					t.Fatalf("ApplyMessage(kind=%q): %v", m.Kind, err)
				}
				payload = payload[logicalRepMsgLen(m, payload):]
			}
		}
		applyMsg(t, emitMsg(t, func(po *wal.PgOutput) error {
			return po.Commit(storage.TransactionID(xid), uint64(xid)*100)
		}))
	}

	// Tx 1: INSERT (id=1, val='hello') → subscriber should have 1 row.
	driveXact(t, 1, [][]byte{
		emitChange(t, wal.Change{
			Kind:     wal.ChangeInsert,
			Rel:      pubRel,
			NewTuple: makeTuple(t, 1, "hello"),
		}),
	})

	// Tx 2: DELETE (id=1) → subscriber should have 0 rows.
	driveXact(t, 2, [][]byte{
		emitChange(t, wal.Change{
			Kind:     wal.ChangeDelete,
			Rel:      pubRel,
			OldTuple: makeTuple(t, 1, "hello"),
		}),
	})

	// Tx 3: INSERT (id=2, val='world') → 1 row.
	driveXact(t, 3, [][]byte{
		emitChange(t, wal.Change{
			Kind:     wal.ChangeInsert,
			Rel:      pubRel,
			NewTuple: makeTuple(t, 2, "world"),
		}),
	})

	// Tx 4: UPDATE (id=2, val='world') → (id=2, val='updated').
	driveXact(t, 4, [][]byte{
		emitChange(t, wal.Change{
			Kind:     wal.ChangeUpdate,
			Rel:      pubRel,
			OldTuple: makeTuple(t, 2, "world"),
			NewTuple: makeTuple(t, 2, "updated"),
		}),
	})

	// Verify subscriber: expect exactly one visible row: (2, 'updated').
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "pub_t"})
	subRel := subCat.RelFileNode(subTbl)
	tx, err := subTxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	defer subTxnMgr.Rollback(tx)
	subSnap, _ := subTxnMgr.SnapshotFor(tx)

	var rows [][]string
	nBlocks, _ := subPool.NBlocks(subRel)
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, _ := subPool.Pin(storage.BufferTag{Rel: subRel, Block: blk})
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			subPool.Unpin(s)
			continue
		}
		count, _ := storage.PageLinePointerCount(page)
		for slot := uint16(1); slot <= uint16(count); slot++ {
			tup, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if !mvcc.TupleVisible(tup.Header, subSnap, tx.XID) {
				continue
			}
			row, _ := executor.DecodeRow(pubCols, tup.Data)
			rows = append(rows, []string{
				fmt.Sprintf("%d", row[0].Int),
				row[1].StringValue(),
			})
		}
		s.RUnlock()
		subPool.Unpin(s)
	}
	if len(rows) != 1 {
		t.Fatalf("subscriber has %d rows, want 1; rows=%v", len(rows), rows)
	}
	if rows[0][0] != "2" || rows[0][1] != "updated" {
		t.Errorf("subscriber row = %v, want [2 updated]", rows[0])
	}
}

// logicalRepEncodeBody encodes values in the v0 executor codec format
// (null-flag then big-endian value bytes) for use in logical replication
// test helpers. Mirrors the encoder in internal/executor/codec.go.
func logicalRepEncodeBody(values []any, types []string) []byte {
	var out []byte
	for i, v := range values {
		if v == nil {
			out = append(out, 1)
			continue
		}
		out = append(out, 0)
		switch types[i] {
		case "int4":
			var tmp [4]byte
			binary.BigEndian.PutUint32(tmp[:], uint32(int32(v.(int))))
			out = append(out, tmp[:]...)
		case "text":
			s := v.(string)
			var ln [4]byte
			binary.BigEndian.PutUint32(ln[:], uint32(len(s)))
			out = append(out, ln[:]...)
			out = append(out, []byte(s)...)
		}
	}
	return out
}

// logicalRepMsgLen returns the byte length of the pgoutput message
// starting at payload[0]. Used to advance through a multi-message
// buffer (e.g. R + I emitted by a single po.Change call).
func logicalRepMsgLen(m *wal.DecodedMessage, payload []byte) int {
	// Re-encode the message to the same byte form and measure.
	// Simpler than a manual length parser: DecodeMessage succeeds,
	// so we know the start; the next message starts where the
	// reader would be. We binary-search by trying successive lengths.
	for n := 1; n <= len(payload); n++ {
		_, err := wal.DecodeMessage(payload[:n])
		if err == nil {
			// Check that the NEXT byte (if any) starts a different or
			// self-consistent message — i.e., we haven't over-consumed.
			// Since each message is self-delimiting by kind + fixed/variable
			// fields, the first successful decode at minimum length is correct.
			_ = m
			return n
		}
	}
	return len(payload)
}
