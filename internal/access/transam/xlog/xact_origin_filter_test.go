package xlog

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
)

// TestCommitOriginIsFilteredByOrigin pins M0149-0062a against PG 18.3's
// XactLogCommitRecord / DecodeCommit / pgoutput_origin_filter: a commit by
// a transaction applying changes from a replication origin names the
// origin in the record header (XLR_BLOCK_ID_ORIGIN) and carries
// xinfo HAS_ORIGIN + xl_xact_origin{origin_lsn, origin_timestamp} last; a
// decoder whose origin filter rejects that origin forgets the transaction
// (nothing reaches the plugin), while a local commit (origin 0) and an
// unfiltered decoder deliver it.
func TestCommitOriginIsFilteredByOrigin(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 16402}
	decode := func(framed []byte) Record {
		t.Helper()
		record, _, err := encodeRecordXLog(framed, 0)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := decodeRecordXLogDetailed(record)
		if err != nil {
			t.Fatal(err)
		}
		return Record{XLog: dec.XLog, EndLSN: 200}
	}
	insert := func(xid storage.TransactionID) Record {
		t.Helper()
		tup := storage.NewHeapTuple(xid, storage.InvalidTransactionID, []byte("payload"))
		tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
		tuple, err := tup.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		framed, err := EncodeHeapInsertPG(rel, 0, 1, tuple, false)
		if err != nil {
			t.Fatal(err)
		}
		r := decode(framed)
		r.EndLSN = 100
		return r
	}
	commit := func(xid storage.TransactionID, origin transam.TxnOrigin) Record {
		t.Helper()
		framed, err := EncodeXactCommitPGWithOriginAt(1, xid, nil, nil, nil, false, origin)
		if err != nil {
			t.Fatal(err)
		}
		return decode(framed)
	}

	remote := commit(60, transam.TxnOrigin{ID: 7, LSN: 0x1234, Time: 99})
	if remote.XLog.RecordOrigin != 7 {
		t.Fatalf("record origin = %d, want 7", remote.XLog.RecordOrigin)
	}
	md := remote.XLog.MainData
	if xinfo := binary.LittleEndian.Uint32(md[8:12]); xinfo&xactXinfoHasOrigin == 0 {
		t.Errorf("xinfo = %#x, want HAS_ORIGIN", xinfo)
	}
	if lsn, ts := binary.LittleEndian.Uint64(md[len(md)-16:]), binary.LittleEndian.Uint64(md[len(md)-8:]); lsn != 0x1234 || ts != 99 {
		t.Errorf("xl_xact_origin = {%#x, %d}, want {0x1234, 99}", lsn, ts)
	}
	if local := commit(61, transam.TxnOrigin{}); local.XLog.RecordOrigin != 0 {
		t.Errorf("a local commit names origin %d", local.XLog.RecordOrigin)
	}

	none := func(origin uint16) bool { return origin != 0 }
	for _, tc := range []struct {
		name   string
		xid    storage.TransactionID
		origin transam.TxnOrigin
		filter func(uint16) bool
		want   []string
	}{
		{"origin none, remote commit", 62, transam.TxnOrigin{ID: 7}, none, nil},
		{"origin none, local commit", 63, transam.TxnOrigin{}, none, []string{"Begin", "Change", "Commit"}},
		{"origin any, remote commit", 64, transam.TxnOrigin{ID: 7}, nil, []string{"Begin", "Change", "Commit"}},
	} {
		p := &recordingPlugin{}
		d := NewDecoder(p)
		d.SetOriginFilter(tc.filter)
		if err := Classify(d, insert(tc.xid)); err != nil {
			t.Fatal(err)
		}
		if err := Classify(d, commit(tc.xid, tc.origin)); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(p.calls, tc.want) {
			t.Errorf("%s: plugin calls = %v, want %v", tc.name, p.calls, tc.want)
		}
		if d.Active() != 0 {
			t.Errorf("%s: %d transactions left in the reorder buffer", tc.name, d.Active())
		}
	}
}
