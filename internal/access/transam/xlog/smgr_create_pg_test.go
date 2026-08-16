package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeSmgrCreatePGRoutingAndReplay verifies the A9 xl_smgr_create flip:
// the record decodes to the PG header (RmgrStorage/XLOG_SMGR_CREATE) with the
// creating xid, a 16-byte RelFileLocator+forkNum main-data and no block ref;
// it routes to the DECODED replay path (nil native Payload); and replay creates
// the relation file. Both a real DDL xid and a bootstrap xid=0 must route
// correctly.
func TestEncodeSmgrCreatePGRoutingAndReplay(t *testing.T) {
	cases := []struct {
		name string
		rel  storage.RelFileNode
		xid  storage.TransactionID
	}{
		{"ddl-default-tablespace", storage.RelFileNode{DBOid: 5, RelOid: 16400, Fork: storage.MainFork}, 4242},
		{"bootstrap-xid0-default", storage.RelFileNode{DBOid: 5, RelOid: 16401, Fork: storage.MainFork}, 0},
		{"user-tablespace", storage.RelFileNode{TblOid: 16395, DBOid: 5, RelOid: 16402, Fork: storage.MainFork}, 777},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
			defer mgr.Close()

			framed, err := EncodeSmgrCreatePG(tc.rel, tc.xid)
			if err != nil {
				t.Fatal(err)
			}
			rec, _, err := encodeRecordXLog(framed, 0)
			if err != nil {
				t.Fatal(err)
			}
			dec, err := decodeRecordXLogDetailed(rec)
			if err != nil {
				t.Fatal(err)
			}
			if dec.Header.Rmid != RmgrStorage || dec.Header.Info != xlogSmgrCreate {
				t.Fatalf("rmid/info = %d/%#x, want RmgrStorage/SMGR_CREATE", dec.Header.Rmid, dec.Header.Info)
			}
			if dec.Header.XID != uint32(tc.xid) {
				t.Fatalf("header xid = %d, want %d", dec.Header.XID, tc.xid)
			}
			if len(dec.XLog.Blocks) != 0 {
				t.Fatalf("want no block refs, got %d", len(dec.XLog.Blocks))
			}
			if len(dec.XLog.MainData) != 16 {
				t.Fatalf("main-data len = %d, want 16", len(dec.XLog.MainData))
			}
			// Routing: a main-data-only record must decode with a nil native
			// Payload so ApplyRecord dispatches it to replayDecodedXLogRecord
			// (not the native payload[0] switch).
			if dec.Payload != nil {
				t.Fatalf("record routed to the NATIVE path (Payload populated) — expected decoded path")
			}
			// The decoded rel must match the on-disk rel the emitter used.
			gotRel, err := decodeXLogSmgrCreate(dec.XLog.MainData)
			if err != nil {
				t.Fatal(err)
			}
			if gotRel != tc.rel {
				t.Fatalf("decoded rel = %+v, want %+v", gotRel, tc.rel)
			}

			// Replay creates the relfile (idempotent).
			applyPGRecord(t, mgr, framed, 100)
			n, err := mgr.NBlocks(tc.rel)
			if err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("after replay NBlocks = %d, want 1", n)
			}
			// Idempotent second replay.
			applyPGRecord(t, mgr, framed, 200)
			if n, _ := mgr.NBlocks(tc.rel); n != 1 {
				t.Fatalf("second replay not idempotent: NBlocks = %d", n)
			}
		})
	}
}
