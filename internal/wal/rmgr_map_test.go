package wal

import "testing"

// TestEmittedRecordKindsClassifyPGCompatible pins the (xl_rmid, xl_info) that
// every currently-emitted goopg RecordKind maps to, guards against a
// regression back to the RM_XLOG/0xF0 skip-tag, and verifies the round-trip
// invariant headerMatchesEmittedKind uses to recognise goopg-native records
// during recovery. See docs/design/wal-native-pg-format/04.
func TestEmittedRecordKindsClassifyPGCompatible(t *testing.T) {
	cases := []struct {
		name string
		kind byte
		rmid Rmgr
		info uint8
	}{
		{"HeapInsert", RecordKindHeapInsert, RmgrHeap, xlogHeapInsert},
		{"HeapDelete", RecordKindHeapDelete, RmgrHeap, xlogHeapDelete},
		{"HeapHotUpdate", RecordKindHeapHotUpdate, RmgrHeap, xlogHeapHotUpdate},
		{"HeapLock", RecordKindHeapLock, RmgrHeap, xlogHeapLock},
		{"HeapPruneOpt", RecordKindHeapPruneOpt, RmgrHeap2, xlogHeap2PruneOnAccess},
		{"HeapVacuum", RecordKindHeapVacuum, RmgrHeap2, xlogHeap2PruneVacuumScan},
		{"HeapFreeze", RecordKindHeapFreeze, RmgrHeap2, xlogHeap2PruneVacuumClean},
		{"BtreeInsert", RecordKindBtreeInsert, RmgrBtree, xlogBtreeInsertLeaf},
		{"BtreeSplit", RecordKindBtreeSplit, RmgrBtree, xlogBtreeSplitL},
		{"BtreeVacuum", RecordKindBtreeVacuum, RmgrBtree, xlogBtreeVacuum},
		{"BtreeUnlinkPage", RecordKindBtreeUnlinkPage, RmgrBtree, xlogBtreeUnlinkPage},
		{"BtreeNewRoot", RecordKindBtreeNewRoot, RmgrBtree, xlogBtreeNewRoot},
		{"BtreeMarkPageHalfDead", RecordKindBtreeMarkPageHalfDead, RmgrBtree, xlogBtreeMarkPageHalfDead},
		{"XactCommit", RecordKindXactCommit, RmgrXact, xlogXactCommit},
		{"XactAbort", RecordKindXactAbort, RmgrXact, xlogXactAbort},
		{"SmgrCreate", RecordKindSmgrCreate, RmgrStorage, xlogSmgrCreate},
		{"ClogTruncate", RecordKindClogTruncate, RmgrCLOG, xlogClogTruncate},
		{"PageImage", RecordKindPageImage, RmgrXLog, xlogXLogFPI},
		// A goopg-private catalog/DDL record → custom rmgr, body-keyed.
		{"CreateDatabase(private)", RecordKindCreateDatabase, RmgrGoopgCatalog, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRmid, gotInfo := recordKindToRmgrInfo(tc.kind)
			if gotRmid != tc.rmid || gotInfo != tc.info {
				t.Fatalf("recordKindToRmgrInfo(%s) = (%d, 0x%02x), want (%d, 0x%02x)",
					tc.name, gotRmid, gotInfo, tc.rmid, tc.info)
			}
			// No emitted kind may regress to the old RM_XLOG/0xF0 skip-tag.
			if gotRmid == RmgrXLog && gotInfo == xlogInfoDefault {
				t.Fatalf("%s classifies as the removed RM_XLOG/0xF0 skip-tag", tc.name)
			}
			// The custom rmgr must be decodable (header guard accepts >=128).
			if _, err := DecodeXLogRecordHeader(headerBytesFor(gotRmid, gotInfo)); err != nil {
				t.Fatalf("%s header (rmid=%d) rejected by DecodeXLogRecordHeader: %v", tc.name, gotRmid, err)
			}
			// Round-trip: a Record whose header matches its kind is recognised
			// as goopg-native (drives recovery dispatch to the payload[0] switch).
			rec := Record{
				Payload: []byte{tc.kind, 0x01, 0x02, 0x03},
				XLog:    &XLogDecodedRecord{Header: XLogRecord{Rmid: gotRmid, Info: gotInfo}},
			}
			if !headerMatchesEmittedKind(rec) {
				t.Fatalf("%s: headerMatchesEmittedKind = false, want true", tc.name)
			}
			// A header that does NOT match the kind (e.g. a checkpoint whose
			// payload[0] collides) must be rejected as non-native.
			mismatch := Record{
				Payload: []byte{tc.kind},
				XLog:    &XLogDecodedRecord{Header: XLogRecord{Rmid: RmgrXLog, Info: xlogCheckpointShutdown}},
			}
			if tc.rmid == RmgrXLog && tc.info == xlogCheckpointShutdown {
				return // degenerate: kind genuinely maps to the checkpoint header
			}
			if headerMatchesEmittedKind(mismatch) {
				t.Fatalf("%s: headerMatchesEmittedKind(mismatched header) = true, want false", tc.name)
			}
		})
	}
}

// headerBytesFor builds a minimal valid 24-byte XLogRecord header carrying the
// given rmid/info (zero padding, no framework bits) for the decode guard check.
func headerBytesFor(rmid Rmgr, info uint8) []byte {
	b := make([]byte, SizeOfXLogRecord)
	b[16] = info
	b[17] = byte(rmid)
	return b
}
