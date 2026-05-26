package testport

import (
	"bytes"
	"context"
	"database/sql"
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

	// makeTuple encodes a row as a PG-physical heap-tuple byte slice
	// for OldTuple / NewTuple in a wal.Change. SetNatts must be called
	// so encodePgoTuplePhysical knows how many columns to decode.
	makeTuple := func(t *testing.T, id int, val string) []byte {
		t.Helper()
		body := logicalRepEncodeBody([]any{id, val}, []string{"int4", "text"})
		ht := storage.NewHeapTuple(1, 0, body)
		ht.Header.SetNatts(2)
		raw, err := ht.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		return raw
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

// TestE2E_PhysicalReplicationSync verifies synchronous streaming replication
// between two goopg nodes. After the standby connects, the primary is
// configured with synchronous_standby_names so that a subsequent INSERT
// with synchronous_commit='remote_apply' blocks until the standby has
// applied the WAL. The row must be visible on the standby immediately after
// the INSERT returns.
func TestE2E_PhysicalReplicationSync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replication test in short mode")
	}

	const appName = "goopg_sync_standby"
	baseDir := t.TempDir()
	rc, err := replcluster.New("e2e_phys_sync", replcluster.Options{
		RepoRoot:        repoRoot(t),
		BaseDir:         baseDir,
		SlotName:        "e2e_phys_sync_slot",
		ApplicationName: appName,
		// Set before first primary boot so it is effective from startup.
		PrimaryExtraConf: []string{
			"synchronous_standby_names = '" + appName + "'",
		},
		StartupWait:  30 * time.Second,
		ShutdownWait: 10 * time.Second,
		PreCloneHook: func(primary *cluster.Cluster) error {
			_, err := primary.Query(context.Background(), "CREATE TABLE sync_t (id int)")
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Setup(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Stop() }()

	// Wait for the standby to be registered as 'sync' in pg_stat_replication.
	syncQuery := fmt.Sprintf(
		"SELECT sync_state FROM pg_stat_replication WHERE application_name = '%s'",
		appName)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := rc.Primary.Query(context.Background(), syncQuery)
		if err == nil && len(rows) > 0 && rows[0][0] == "sync" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Verify sync state was achieved; if not, the INSERT below will still
	// exercise the path (the standby may just be 'potential').
	rows, _ := rc.Primary.Query(context.Background(), syncQuery)
	if len(rows) == 0 || rows[0][0] != "sync" {
		t.Fatalf("standby did not reach sync_state='sync' within 20s (got %v)", rows)
	}

	// INSERT with synchronous_commit='remote_apply' on a persistent connection
	// so the SET persists for the duration of the INSERT.
	host, port, err := netSplitHostPort(rc.Primary.ListenAddr())
	if err != nil {
		t.Fatalf("split primary addr: %v", err)
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s sslmode=disable",
		host, port, "postgres", "postgres")
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "SET synchronous_commit = 'remote_apply'"); err != nil {
		t.Fatalf("SET synchronous_commit: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO sync_t VALUES (99)"); err != nil {
		t.Fatalf("sync INSERT: %v", err)
	}
	// sync_commit='remote_apply' guarantees the standby applied the WAL before
	// the INSERT returned, so the row must be immediately visible.
	standbyRows, err := rc.Standby.Query(context.Background(),
		"SELECT id FROM sync_t WHERE id = 99")
	if err != nil {
		t.Fatalf("standby query: %v", err)
	}
	if len(standbyRows) == 0 || standbyRows[0][0] != "99" {
		t.Fatalf("standby did not have sync row immediately: rows=%v", standbyRows)
	}
}

// logicalRepEncodeBody encodes values in PG-physical format for use in
// logical replication test helpers. Mirrors executor.encodeRowPG: no
// null-flag bytes, PG alignment, little-endian fixed-width values, and
// varlena headers for text. NULL values are skipped (no bytes emitted),
// matching the behaviour of encodeRowPG. The caller must stamp the natts
// into the HeapTuple's Infomask2 so the pgoutput encoder can walk columns.
func logicalRepEncodeBody(values []any, types []string) []byte {
	var out []byte
	off := 0
	pad := func(align int) {
		for off%align != 0 {
			out = append(out, 0)
			off++
		}
	}
	for i, v := range values {
		if v == nil {
			continue // NULL: no bytes, bitmap handled at tuple level
		}
		switch types[i] {
		case "int4":
			pad(4)
			var tmp [4]byte
			binary.LittleEndian.PutUint32(tmp[:], uint32(int32(v.(int))))
			out = append(out, tmp[:]...)
			off += 4
		case "text":
			pad(4) // varlena is int-aligned in PG
			s := v.(string)
			total := len(s) + 1 // 1-byte short header
			if total <= 127 {
				out = append(out, byte(total<<1)|1)
				out = append(out, []byte(s)...)
				off += total
			} else {
				total = len(s) + 4 // 4-byte header
				var hdr [4]byte
				binary.LittleEndian.PutUint32(hdr[:], uint32(total)<<2)
				out = append(out, hdr[:]...)
				out = append(out, []byte(s)...)
				off += total
			}
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
