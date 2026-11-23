package xlog

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestParseTwoPhaseStatePG18File reads a pg_twophase file PG 18.3 wrote for
//
//	BEGIN; INSERT; SAVEPOINT a; INSERT; SAVEPOINT b; INSERT;
//	PREPARE TRANSACTION 'gx1'; CHECKPOINT;
//
// (xid 753 with subtransactions 754 and 755, database 5, owner 10). The file
// is the state buffer plus a CRC-32C.
func TestParseTwoPhaseStatePG18File(t *testing.T) {
	raw, err := os.ReadFile("testdata/pg18_twophase_00000000000002F1")
	if err != nil {
		t.Fatal(err)
	}
	st, err := ParseTwoPhaseState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if st.XID != 753 || st.Database != 5 || st.Owner != 10 || st.GID != "gx1" {
		t.Fatalf("header = xid %d db %d owner %d gid %q", st.XID, st.Database, st.Owner, st.GID)
	}
	if len(st.Subxacts) != 2 || st.Subxacts[0] != 754 || st.Subxacts[1] != 755 {
		t.Fatalf("subxacts = %v", st.Subxacts)
	}
	if int(st.TotalLen) != len(raw) {
		t.Fatalf("total_len %d, file is %d bytes", st.TotalLen, len(raw))
	}
}

// TestEncodeTwoPhaseStateMatchesPG18Layout encodes the same transaction and
// compares the fixed part with PG's file byte for byte, skipping the
// prepared_at timestamp and total_len (PG also wrote lock and pgstat
// records goopg does not). Padding bytes (53, the gid tail) are not
// compared: PG leaves them
// uninitialised.
func TestEncodeTwoPhaseStateMatchesPG18Layout(t *testing.T) {
	raw, err := os.ReadFile("testdata/pg18_twophase_00000000000002F1")
	if err != nil {
		t.Fatal(err)
	}
	got := EncodeTwoPhaseState(TwoPhaseState{
		XID: 753, Database: 5, Owner: 10, GID: "gx1",
		PreparedAt: int64(binary.LittleEndian.Uint64(raw[16:])),
		Subxacts:   []storage.TransactionID{754, 755},
	})
	type span struct{ from, to int }
	for _, sp := range []span{{0, 4}, {8, 53}, {54, 72}, {72, 76}, {80, 88}} {
		for i := sp.from; i < sp.to; i++ {
			if got[i] != raw[i] {
				t.Fatalf("byte %d: goopg %#x, PG %#x", i, got[i], raw[i])
			}
		}
	}
	// Our buffer: header 72, gid 8, subxacts 8, end sentinel 8; +4 CRC.
	if n := binary.LittleEndian.Uint32(got[4:]); n != 72+8+8+8+4 || int(n) != len(got)+4 {
		t.Fatalf("total_len = %d, buffer %d", n, len(got))
	}
	back, err := ParseTwoPhaseState(got)
	if err != nil || back.GID != "gx1" || len(back.Subxacts) != 2 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

// TestEncodeXactCommitPreparedRoundTrip: the COMMIT PREPARED record parses
// back to the prepared xid (xl_xact_twophase) and its subtransactions, with
// header xid 0 — what replay and the logical decoder key on.
func TestEncodeXactCommitPreparedRoundTrip(t *testing.T) {
	for _, commit := range []bool{true, false} {
		var payload []byte
		var err error
		subs := []storage.TransactionID{11, 12}
		if commit {
			payload, err = EncodeXactCommitPreparedPGAt(1, 10, subs, nil, true)
		} else {
			payload, err = EncodeXactAbortPreparedPGAt(1, 10, subs, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _, hdrXid, _, ok := unframePGAssembled(payload)
		if !ok || hdrXid != 0 {
			t.Fatalf("header xid %d (ok=%v), want 0", hdrXid, ok)
		}
		info, mainData := decodeMainData(t, payload)
		p, err := ParseXactRecord(info, mainData)
		if err != nil {
			t.Fatal(err)
		}
		if p.TwophaseXid != 10 || len(p.Subxacts) != 2 {
			t.Fatalf("commit=%v parsed %+v", commit, p)
		}
	}
}
