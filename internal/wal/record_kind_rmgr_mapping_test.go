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
		// All B-phase catalog/DDL kinds are retired (index/attrdef/statistics/
		// view/matview). The subxact markers are among the surviving goopg-private
		// kinds that still fall through to RmgrGoopgCatalog.
		RecordKindXactAssignment,
		RecordKindXactSubAbort,
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
		RecordKindXactAssignment, RecordKindXactSubAbort,
	} {
		wantRmgr, wantInfo := recordKindToRmgrInfo(kind)
		rmgr, info, xid := classifyXLogRecord([]byte{kind})
		if rmgr != wantRmgr || info != wantInfo || xid != 0 {
			t.Errorf("classifyXLogRecord([%d]) = (%d, 0x%02x, %d), want (%d, 0x%02x, 0) from recordKindToRmgrInfo",
				kind, rmgr, info, xid, wantRmgr, wantInfo)
		}
	}
}

// TestActiveRecordKindValuesNotRetiredB5IndexAttrdef guards against accidental
// reuse of the retired B5 group A+B WAL record kind byte values (20, 21, 94, 69).
// These values were retired in eb88b8a2 (index) and 7f42e9c3 (attrdef); the
// former CREATE/DROP/RENAME INDEX and ColumnDefaults records now journal as
// real PG-native heap WAL (pg_class/pg_index/pg_attrdef) and a PG standby
// replays them natively. A new RecordKind constant must never be assigned one
// of these values — it would be indistinguishable from a retired record in
// legacy WAL, and real PG standbys would FATAL on the rmid-128 record.
//
// This is a hardcoded enumeration of every active RecordKind constant. If you
// add a new RecordKind, add it to the activeKinds slice below. If it is assigned
// a retired value, this test catches it at compilation time (the constant is
// directly referenced in the slice literal, so a duplicate-value collision
// surfaces as a test failure rather than a silent WAL corruption).
func TestActiveRecordKindValuesNotRetiredB5IndexAttrdef(t *testing.T) {
	// retiredB5GroupAB holds the byte values retired in B5 groups A (index:
	// 20=CreateIndex, 21=DropIndex, 94=RenameIndex) and B (attrdef:
	// 69=ColumnDefaults). These must never appear in activeKinds below.
	retiredB5GroupAB := map[byte]string{
		20: "CreateIndex (retired eb88b8a2)",
		21: "DropIndex (retired eb88b8a2)",
		69: "ColumnDefaults (retired 7f42e9c3)",
		94: "RenameIndex (retired eb88b8a2)",
	}

	// activeKinds enumerates every RecordKind* constant currently emitted
	// or dispatched. This list is maintained manually; a missing constant
	// is not an error here (TestRecordKindToRmgrInfoAnalogTable covers
	// PG-analog dispatch), but a newly-added constant that reuses a
	// retired value IS an error.
	activeKinds := []struct {
		value byte
		name  string
	}{
		{RecordKindPageImage, "PageImage"},
		{RecordKindCheckpoint, "Checkpoint"},
		{RecordKindBtreeSplit, "BtreeSplit"},
		{RecordKindHeapInsert, "HeapInsert"},
		{RecordKindBtreeInsert, "BtreeInsert"},
		{RecordKindHeapDelete, "HeapDelete"},
		{RecordKindHeapVacuum, "HeapVacuum"},
		{RecordKindXactCommit, "XactCommit"},
		{RecordKindXactAbort, "XactAbort"},
		{RecordKindHeapLock, "HeapLock"},
		{RecordKindSmgrCreate, "SmgrCreate"},
		{RecordKindSmgrTruncate, "SmgrTruncate"},
		{RecordKindHeapHotUpdate, "HeapHotUpdate"},
		{RecordKindHeapPruneOpt, "HeapPruneOpt"},
		{RecordKindXactAssignment, "XactAssignment"},
		{RecordKindXactRollbackTo, "XactRollbackTo"},
		{RecordKindXactSubAbort, "XactSubAbort"},
		{RecordKindBtreeVacuum, "BtreeVacuum"},
		{RecordKindBtreeUnlinkPage, "BtreeUnlinkPage"},
		{RecordKindBtreeNewRoot, "BtreeNewRoot"},
		{RecordKindBtreeMarkPageHalfDead, "BtreeMarkPageHalfDead"},
		{RecordKindHeapFreeze, "HeapFreeze"},
		{RecordKindHeapUpdate, "HeapUpdate"},
		{RecordKindHeapMultiInsert, "HeapMultiInsert"},
		{RecordKindHeapVisible, "HeapVisible"},
		{RecordKindBtreeReusePage, "BtreeReusePage"},
		{RecordKindBtreeMetaCleanup, "BtreeMetaCleanup"},
		{RecordKindClogTruncate, "ClogTruncate"},
	}

	seen := make(map[byte]string)
	for _, k := range activeKinds {
		// Duplicate-value guard (two constants assigned the same byte).
		if prev, ok := seen[k.value]; ok {
			t.Errorf("RecordKind value %d assigned to both %s and %s", k.value, k.name, prev)
		}
		seen[k.value] = k.name

		// Retired-value guard.
		if retired, ok := retiredB5GroupAB[k.value]; ok {
			t.Errorf("RecordKind %s uses retired byte value %d (%s)", k.name, k.value, retired)
		}
	}

	// Verify retired kinds have NO PG-analog mapping (they fall through to
	// RmgrGoopgCatalog). An active PG-analog mapping for a retired kind
	// would mean a code path is still producing PG-classified records for
	// what should be legacy-only bytes.
	for kind, label := range retiredB5GroupAB {
		gotRmgr, _ := recordKindToRmgrInfo(kind)
		if gotRmgr != RmgrGoopgCatalog {
			t.Errorf("retired kind %d (%s) maps to rmgr %d, want RmgrGoopgCatalog(%d) — "+
				"a PG-analog mapping suggests an emit site still exists",
				kind, label, gotRmgr, RmgrGoopgCatalog)
		}
	}
}

// TestNativeApplyRecordKindKnownRejectsRetiredB5IndexAttrdef verifies that
// nativeApplyRecordKindKnown (the PG-decoded-record gate in ApplyRecord) returns
// false for the retired B5 group A+B kind bytes. A true return would route a
// retired-kind record to the native replay switch, which has no arms for these
// kinds and would silently drop the record. The correct path for a legacy WAL
// record carrying a retired kind is false → replayDecodedXLogRecord (the
// general PG-xlog path), which FATALs with "resource manager 128" on a real PG
// standby — exactly the behaviour we want for records that should never be
// emitted in new WAL.
func TestNativeApplyRecordKindKnownRejectsRetiredB5IndexAttrdef(t *testing.T) {
	retiredKinds := map[byte]string{
		20: "CreateIndex (retired eb88b8a2)",
		21: "DropIndex (retired eb88b8a2)",
		69: "ColumnDefaults (retired 7f42e9c3)",
		94: "RenameIndex (retired eb88b8a2)",
	}
	for kind, label := range retiredKinds {
		if nativeApplyRecordKindKnown(kind) {
			t.Errorf("retired kind %d (%s): nativeApplyRecordKindKnown returned true — "+
				"a gate arm still exists for a retired kind", kind, label)
		}
	}
}
