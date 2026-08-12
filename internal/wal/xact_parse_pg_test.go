package wal

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0131-S22 guards for ParseXactRecord — the decoder that recovers a real PG
// commit/abort record's subtransaction list. Every guard here is written so it
// FAILS if the chunk walk drifts (a skipped dbinfo chunk, a chunk read in the
// wrong order, a count word read as the first XID).

// decodeMainData round-trips an encoder's framed envelope back to (info,
// mainData) the way the recovery path sees it.
func decodeMainData(t *testing.T, payload []byte) (uint8, []byte) {
	t.Helper()
	rmid, info, _, body, ok := unframePGAssembled(payload)
	if !ok {
		t.Fatalf("payload is not a pre-assembled PG envelope")
	}
	if rmid != RmgrXact {
		t.Fatalf("rmid = %d, want RmgrXact", rmid)
	}
	dec, err := parseXLogRecordData(XLogRecord{Rmid: rmid, Info: info}, body)
	if err != nil {
		t.Fatalf("decode assembled body: %v", err)
	}
	return info, dec.XLog.MainData
}

// TestParseXactRecordMinimalCommit: goopg's own minimal commit (xact_time only,
// XLOG_XACT_HAS_INFO clear) parses to an empty tree.
func TestParseXactRecordMinimalCommit(t *testing.T) {
	payload, err := EncodeXactCommitPG(42, false)
	if err != nil {
		t.Fatal(err)
	}
	info, mainData := decodeMainData(t, payload)
	parsed, err := ParseXactRecord(info, mainData)
	if err != nil {
		t.Fatalf("ParseXactRecord: %v", err)
	}
	if parsed.Xinfo != 0 || len(parsed.Subxacts) != 0 {
		t.Fatalf("parsed = %+v, want zero xinfo and no subxacts", parsed)
	}
}

// TestParseXactRecordCommitSubxacts is the core guard: a savepoint-using
// transaction's commit record must yield every subtransaction XID, in order.
func TestParseXactRecordCommitSubxacts(t *testing.T) {
	subs := []storage.TransactionID{101, 102, 103}
	payload, err := EncodeXactCommitPGWithSubxacts(100, subs, false)
	if err != nil {
		t.Fatal(err)
	}
	info, mainData := decodeMainData(t, payload)
	if info&xlogXactHasInfo == 0 {
		t.Fatalf("info %#x: subxact-carrying commit must set XLOG_XACT_HAS_INFO", info)
	}
	parsed, err := ParseXactRecord(info, mainData)
	if err != nil {
		t.Fatalf("ParseXactRecord: %v", err)
	}
	if parsed.Xinfo&xactXinfoHasSubxacts == 0 {
		t.Fatalf("xinfo %#x lacks XACT_XINFO_HAS_SUBXACTS", parsed.Xinfo)
	}
	if len(parsed.Subxacts) != len(subs) {
		t.Fatalf("got %d subxacts %v, want %d", len(parsed.Subxacts), parsed.Subxacts, len(subs))
	}
	for i, want := range subs {
		if parsed.Subxacts[i] != uint32(want) {
			t.Errorf("subxact[%d] = %d, want %d", i, parsed.Subxacts[i], want)
		}
	}
}

// TestParseXactRecordCommitSubxactsWithInvals: subxacts and invals in the same
// record. The invals chunk follows the subxact array, so a walk that reads the
// chunks out of order returns the nmsgs word as an XID.
func TestParseXactRecordCommitSubxactsWithInvals(t *testing.T) {
	payload, err := EncodeXactCommitPGWithSubxacts(200, []storage.TransactionID{201}, true)
	if err != nil {
		t.Fatal(err)
	}
	info, mainData := decodeMainData(t, payload)
	if !xactCommitCarriesInvals(info, mainData) {
		t.Fatalf("xactCommitCarriesInvals = false; the HAS_INVALS signal must survive the added subxact chunk")
	}
	parsed, err := ParseXactRecord(info, mainData)
	if err != nil {
		t.Fatalf("ParseXactRecord: %v", err)
	}
	if len(parsed.Subxacts) != 1 || parsed.Subxacts[0] != 201 {
		t.Fatalf("subxacts = %v, want [201]", parsed.Subxacts)
	}
}

// TestParseXactRecordSkipsDbinfo: a real PG commit sets XACT_XINFO_HAS_DBINFO
// (any transaction that dropped a relation or ran in a non-default context), so
// the subxact chunk sits 8 bytes further in. Hand-build that body — goopg's own
// encoder never emits dbinfo — and prove the walk skips it.
func TestParseXactRecordSkipsDbinfo(t *testing.T) {
	var mainData []byte
	mainData = append(mainData, make([]byte, minSizeOfXactCommit)...) // xact_time
	mainData = binary.LittleEndian.AppendUint32(mainData, xactXinfoHasDbinfo|xactXinfoHasSubxacts)
	mainData = binary.LittleEndian.AppendUint32(mainData, 5)    // dbId
	mainData = binary.LittleEndian.AppendUint32(mainData, 1663) // tsId
	mainData = binary.LittleEndian.AppendUint32(mainData, 2)    // nsubxacts
	mainData = binary.LittleEndian.AppendUint32(mainData, 777)
	mainData = binary.LittleEndian.AppendUint32(mainData, 778)

	parsed, err := ParseXactRecord(xlogXactCommit|xlogXactHasInfo, mainData)
	if err != nil {
		t.Fatalf("ParseXactRecord: %v", err)
	}
	if len(parsed.Subxacts) != 2 || parsed.Subxacts[0] != 777 || parsed.Subxacts[1] != 778 {
		t.Fatalf("subxacts = %v, want [777 778] (dbinfo chunk must be skipped, not read as XIDs)", parsed.Subxacts)
	}
}

// TestParseXactRecordTruncated: a body that ends mid-chunk is an error, never a
// half-decoded tree. Half a tree is worse than none — the undecoded half gets
// swept ABORTED while the decoded half is stamped committed.
func TestParseXactRecordTruncated(t *testing.T) {
	full, err := EncodeXactCommitPGWithSubxacts(300, []storage.TransactionID{301, 302}, false)
	if err != nil {
		t.Fatal(err)
	}
	info, mainData := decodeMainData(t, full)
	for _, cut := range []int{len(mainData) - 4, len(mainData) - 8, minSizeOfXactCommit + 2, 3} {
		if cut < 0 {
			continue
		}
		if _, perr := ParseXactRecord(info, mainData[:cut]); perr == nil {
			t.Errorf("ParseXactRecord on a %d-byte body: got nil error, want a truncation error", cut)
		}
	}
}

// TestParseXactRecordAbortSubxacts: the abort twin. xl_xact_abort shares the
// chunk prefix, so the same walk must serve both.
func TestParseXactRecordAbortSubxacts(t *testing.T) {
	payload, err := EncodeXactAbortPGWithSubxacts(400, []storage.TransactionID{401, 402})
	if err != nil {
		t.Fatal(err)
	}
	info, mainData := decodeMainData(t, payload)
	if info&xlogXactOpMask != xlogXactAbort {
		t.Fatalf("info %#x is not XLOG_XACT_ABORT", info)
	}
	parsed, err := ParseXactRecord(info, mainData)
	if err != nil {
		t.Fatalf("ParseXactRecord: %v", err)
	}
	if len(parsed.Subxacts) != 2 || parsed.Subxacts[0] != 401 {
		t.Fatalf("subxacts = %v, want [401 402]", parsed.Subxacts)
	}
}
