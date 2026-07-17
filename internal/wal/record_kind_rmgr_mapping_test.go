package wal

import "testing"

// TestRecordKindToRmgrInfoAnalogTable pins recordKindToRmgrInfo's output
// against every row of doc 04 §3.1's "Records with a PG analog" table
// (docs/design/wal-native-pg-format/04-remove-canonical-and-pg-rmgr-
// dispatch.md), confirmed against the PostgreSQL 18.3 source under
// postgres/src/include/access/{heapam_xlog,nbtxlog}.h,
// postgres/src/include/catalog/{storage_xlog,pg_control}.h, and
// postgres/src/include/access/clog.h. RecordKindCheckpoint and the
// segment-pad NOOP are intentionally excluded — see recordKindToRmgrInfo's
// doc comment for why neither is ever dispatched through this table.
func TestRecordKindToRmgrInfoAnalogTable(t *testing.T) {
	cases := []struct {
		name string
		kind byte
		rmgr Rmgr
		info uint8
	}{
		{"PageImage", RecordKindPageImage, RmgrXLog, 0xB0},                          // XLOG_FPI
		{"BtreeSplit", RecordKindBtreeSplit, RmgrBtree, 0x30},                       // XLOG_BTREE_SPLIT_L
		{"HeapInsert", RecordKindHeapInsert, RmgrHeap, 0x00},                        // XLOG_HEAP_INSERT
		{"BtreeInsert", RecordKindBtreeInsert, RmgrBtree, 0x00},                     // XLOG_BTREE_INSERT_LEAF
		{"HeapDelete", RecordKindHeapDelete, RmgrHeap, 0x10},                        // XLOG_HEAP_DELETE
		{"HeapVacuum", RecordKindHeapVacuum, RmgrHeap2, 0x20},                       // XLOG_HEAP2_PRUNE_VACUUM_SCAN
		{"XactCommit", RecordKindXactCommit, RmgrXact, 0x00},                        // XLOG_XACT_COMMIT
		{"XactAbort", RecordKindXactAbort, RmgrXact, 0x20},                          // XLOG_XACT_ABORT
		{"HeapLock", RecordKindHeapLock, RmgrHeap, 0x60},                            // XLOG_HEAP_LOCK
		{"SmgrCreate", RecordKindSmgrCreate, RmgrStorage, 0x10},                     // XLOG_SMGR_CREATE
		{"HeapHotUpdate", RecordKindHeapHotUpdate, RmgrHeap, 0x40},                  // XLOG_HEAP_HOT_UPDATE
		{"HeapPruneOpt", RecordKindHeapPruneOpt, RmgrHeap2, 0x10},                   // XLOG_HEAP2_PRUNE_ON_ACCESS
		{"BtreeVacuum", RecordKindBtreeVacuum, RmgrBtree, 0xC0},                     // XLOG_BTREE_VACUUM
		{"BtreeUnlinkPage", RecordKindBtreeUnlinkPage, RmgrBtree, 0x80},             // XLOG_BTREE_UNLINK_PAGE
		{"BtreeNewRoot", RecordKindBtreeNewRoot, RmgrBtree, 0xA0},                   // XLOG_BTREE_NEWROOT
		{"BtreeMarkPageHalfDead", RecordKindBtreeMarkPageHalfDead, RmgrBtree, 0xB0}, // XLOG_BTREE_MARK_PAGE_HALFDEAD
		{"HeapFreeze", RecordKindHeapFreeze, RmgrHeap2, 0x30},                       // XLOG_HEAP2_PRUNE_VACUUM_CLEANUP
		{"ClogTruncate", RecordKindClogTruncate, RmgrCLOG, 0x10},                    // CLOG_TRUNCATE
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRmgr, gotInfo := recordKindToRmgrInfo(tc.kind)
			if gotRmgr != tc.rmgr || gotInfo != tc.info {
				t.Errorf("recordKindToRmgrInfo(%s=%d) = (%d, 0x%02x), want (%d, 0x%02x)",
					tc.name, tc.kind, gotRmgr, gotInfo, tc.rmgr, tc.info)
			}
		})
	}
}

// TestRecordKindToRmgrInfoCustomDefault pins doc 04 §3.2: every
// goopg-private catalog/DDL RecordKind with no PG analog classifies under
// the shared custom resource manager RmgrGoopgCatalog. Sampled across the
// numeric range (18, low; 94, mid; 129, high) rather than enumerating all
// ~110 kinds — the mapping is a single default-case fallthrough, so one
// kind from each region is enough to catch a range-boundary mistake.
func TestRecordKindToRmgrInfoCustomDefault(t *testing.T) {
	for _, kind := range []byte{
		RecordKindCreateDatabase,
		RecordKindCreateStatistics,
		RecordKindRenameIndex,
		RecordKindDropUserMapping,
	} {
		gotRmgr, _ := recordKindToRmgrInfo(kind)
		if gotRmgr != RmgrGoopgCatalog {
			t.Errorf("recordKindToRmgrInfo(%d) rmgr = %d, want RmgrGoopgCatalog(%d)", kind, gotRmgr, RmgrGoopgCatalog)
		}
	}
}

// TestClassifyXLogRecordWiredToRecordKindToRmgrInfo pins doc 04 §5.4's
// wiring: classifyXLogRecord's native-record catch-all now delegates to
// recordKindToRmgrInfo instead of the old blanket RmgrXLog/xlogInfoDefault
// (M0105-0007, superseded) for every kind.
func TestClassifyXLogRecordWiredToRecordKindToRmgrInfo(t *testing.T) {
	for _, kind := range []byte{
		RecordKindHeapInsert, RecordKindXactCommit, RecordKindPageImage,
		RecordKindCreateStatistics, RecordKindRenameIndex,
	} {
		wantRmgr, wantInfo := recordKindToRmgrInfo(kind)
		rmgr, info, xid := classifyXLogRecord([]byte{kind})
		if rmgr != wantRmgr || info != wantInfo || xid != 0 {
			t.Errorf("classifyXLogRecord([%d]) = (%d, 0x%02x, %d), want (%d, 0x%02x, 0) from recordKindToRmgrInfo",
				kind, rmgr, info, xid, wantRmgr, wantInfo)
		}
	}
}
