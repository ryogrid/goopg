package executor

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// TestApplyWorkerInsertsRowFromPgoutputStream pins the M0008 /
// 0008-0004 end-to-end contract: a publisher-side `B → R → I →
// C` pgoutput byte sequence drives the subscriber-side
// ApplyWorker to insert exactly the right row into the local
// table. Confirms encoder/decoder/apply symmetry in one shot.
func TestApplyWorkerInsertsRowFromPgoutputStream(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})

	// Publisher: encode B → R → I → C through the existing
	// PgOutput plugin against a snapshot of the publisher's
	// schema.
	snap := wal.BuildCatalogSnapshot(pubCat.(*catalog.InMemory))
	var buf bytes.Buffer
	po := wal.NewPgOutput(snap, &buf)
	if err := po.Begin(42, 0xCAFE); err != nil {
		t.Fatal(err)
	}
	body := encodeBodyV0([]any{7, "alpha"}, []string{"int4", "text"})
	tuple := wrapAsHeapTuple(t, body)
	if err := po.Change(wal.Change{
		Kind:     wal.ChangeInsert,
		Rel:      pubCat.RelFileNode(pubTbl),
		NewTuple: tuple,
	}); err != nil {
		t.Fatal(err)
	}
	if err := po.Commit(42, 0xCAFE); err != nil {
		t.Fatal(err)
	}

	// Subscriber: independent storage fixture with the same
	// schema. The publisher and subscriber both have a table
	// named "items"; in real wiring the subscriber would also
	// have to provide it — in tests the fixture pre-creates it.
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	defer w.SafeRollback()

	stream := buf.Bytes()
	consumed := 0
	commitLSN := uint64(0)
	for consumed < len(stream) {
		end, err := pgoutputMessageLength(stream[consumed:])
		if err != nil {
			t.Fatal(err)
		}
		m, err := wal.DecodeMessage(stream[consumed : consumed+end])
		if err != nil {
			t.Fatal(err)
		}
		lsn, err := w.ApplyMessage(m)
		if err != nil {
			t.Fatalf("ApplyMessage(kind=%q): %v", m.Kind, err)
		}
		if lsn != 0 {
			commitLSN = lsn
		}
		consumed += end
	}

	if commitLSN != 0xCAFE {
		t.Errorf("commit LSN returned by apply=%x want 0xCAFE", commitLSN)
	}

	// Read the row back via SeqScan against the subscriber's
	// catalog. The publisher used the same fixture's pre-existing
	// transaction, so the apply worker's just-committed row
	// is visible to a fresh SeqScan against the subscriber Pool.
	tx, _ := subCtx.TxnMgr.Begin(0)
	defer subCtx.TxnMgr.Rollback(tx)
	snap2, _ := subCtx.TxnMgr.SnapshotFor(tx)
	scanCtx := NewContext()
	scanCtx.Pool = subCtx.Pool
	scanCtx.Catalog = subCat
	scanCtx.TxnMgr = subCtx.TxnMgr
	scanCtx.Tx = tx
	scanCtx.Snap = snap2
	scan := newSeqScanOp(&planner.SeqScan{Table: subTbl})
	if err := scan.Open(scanCtx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("subscriber scan returned %d rows want 1", len(rows))
	}
	if rows[0][0].Int != 7 {
		t.Errorf("col[0]=%v want 7", rows[0][0])
	}
	if rows[0][1].String != "alpha" {
		t.Errorf("col[1]=%q want alpha", rows[0][1].String)
	}
}

// TestApplyWorkerCommitOutsideXactIsNoop pins the tolerant
// behaviour: a `C` with no preceding `B` doesn't crash. v0
// pgoutput emits commit-only sequences only when every change
// was filtered.
func TestApplyWorkerCommitOutsideXactIsNoop(t *testing.T) {
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	lsn, err := w.ApplyMessage(&wal.DecodedMessage{Kind: 'C', CommitLSN: 0xDEAD})
	if err != nil {
		t.Errorf("commit-only err=%v want nil", err)
	}
	if lsn != 0xDEAD {
		t.Errorf("lsn=%x want 0xDEAD", lsn)
	}
}

// TestApplyWorkerInsertWithoutRelationFails: an `I` for a
// rel_oid the worker hasn't seen `R` for surfaces an apply
// error rather than silently dropping the row.
func TestApplyWorkerInsertWithoutRelationFails(t *testing.T) {
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	if _, err := w.ApplyMessage(&wal.DecodedMessage{Kind: 'B', XID: 42}); err != nil {
		t.Fatal(err)
	}
	defer w.SafeRollback()
	_, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind:   'I',
		RelOID: 99999,
	})
	if err == nil {
		t.Errorf("INSERT for unknown rel_oid accepted")
	}
}

// encodeBodyV0 mirrors the executor codec's null-flag-then-value
// frame so tests can construct on-disk bytes the encoder will
// embed in HeapInsert tuples. Duplicated here from
// internal/wal/pgoutput_test.go because Go-test packages can't
// share helpers across packages.
func encodeBodyV0(values []any, types []string) []byte {
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
		case "int8":
			var tmp [8]byte
			binary.BigEndian.PutUint64(tmp[:], uint64(v.(int64)))
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

func wrapAsHeapTuple(t *testing.T, body []byte) []byte {
	t.Helper()
	tup, err := storage.NewHeapTuple(42, 0, body).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return tup
}

// pgoutputMessageLength is the test-side helper to chunk the
// concatenated encoder output into individual messages. Mirrors
// the upstream framing the apply worker will get from CopyData
// frames in production.
func pgoutputMessageLength(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	switch buf[0] {
	case 'B':
		return 21, nil
	case 'C':
		return 26, nil
	case 'R':
		// kind | oid(4) | nspname\0 | relname\0 | replident
		// | natts(2) | per-attr.
		off := 1 + 4
		i := off
		for i < len(buf) && buf[i] != 0 {
			i++
		}
		off = i + 1
		i = off
		for i < len(buf) && buf[i] != 0 {
			i++
		}
		off = i + 1 + 1
		natts := int(buf[off])<<8 | int(buf[off+1])
		off += 2
		for j := 0; j < natts; j++ {
			off++
			i = off
			for i < len(buf) && buf[i] != 0 {
				i++
			}
			off = i + 1 + 4 + 4
		}
		return off, nil
	case 'I', 'D':
		off := 1 + 4 + 1
		natts := int(buf[off])<<8 | int(buf[off+1])
		off += 2
		for j := 0; j < natts; j++ {
			st := buf[off]
			off++
			if st == 't' {
				ln := int(buf[off])<<24 | int(buf[off+1])<<16 | int(buf[off+2])<<8 | int(buf[off+3])
				off += 4 + ln
			}
		}
		return off, nil
	}
	return 0, nil
}
