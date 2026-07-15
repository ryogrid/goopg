package wal

// PG-compatible resource-manager opcode constants and the goopg
// RecordKind → (xl_rmid, xl_info) mapping.
//
// goopg emits ordinary-PostgreSQL-shaped WAL: every record's header
// carries the real PG resource manager id and opcode (xl_info high
// nibble), rather than the historical RM_XLOG/0xF0 "skip-me" tag.
// Records that have a genuine PG analog (heap / heap2 / btree / xact /
// smgr / clog / xlog-FPI) use the upstream rmgr + opcode; goopg-private
// catalog/DDL records that have no PG WAL analog use goopg's custom
// resource manager (RmgrGoopgCatalog, in PG's reserved 128..255 range),
// so a stock PG safely skips them.
//
// The record *body* remains goopg-native for now (the PG-struct content
// rewrite is tracked in docs/design/wal-native-pg-format/01+03), so PG
// tools will misparse the analog records' bodies — that is expected and
// out of scope here. goopg↔goopg recovery keys on this same mapping.
// See docs/design/wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md.
const (
	// RM_HEAP opcodes (xl_info & 0x70). xlogHeapInsert/Delete/HotUpdate
	// are already defined in pg_xlog_decode.go.
	xlogHeapLock uint8 = 0x60 // XLOG_HEAP_LOCK

	// RM_HEAP2 opcodes.
	xlogHeap2PruneOnAccess     uint8 = 0x10 // XLOG_HEAP2_PRUNE_ON_ACCESS
	xlogHeap2PruneVacuumScan   uint8 = 0x20 // XLOG_HEAP2_PRUNE_VACUUM_SCAN
	xlogHeap2PruneVacuumCleanp uint8 = 0x30 // XLOG_HEAP2_PRUNE_VACUUM_CLEANUP

	// RM_BTREE opcodes.
	xlogBtreeInsertLeaf       uint8 = 0x00 // XLOG_BTREE_INSERT_LEAF
	xlogBtreeSplitL           uint8 = 0x30 // XLOG_BTREE_SPLIT_L
	xlogBtreeVacuum           uint8 = 0xC0 // XLOG_BTREE_VACUUM
	xlogBtreeUnlinkPage       uint8 = 0x80 // XLOG_BTREE_UNLINK_PAGE
	xlogBtreeNewRoot          uint8 = 0xA0 // XLOG_BTREE_NEWROOT
	xlogBtreeMarkPageHalfDead uint8 = 0xB0 // XLOG_BTREE_MARK_PAGE_HALFDEAD

	// RM_SMGR opcode.
	xlogSmgrCreate uint8 = 0x10 // XLOG_SMGR_CREATE

	// RM_CLOG opcode.
	xlogClogTruncate uint8 = 0x10 // CLOG_TRUNCATE

	// RM_XLOG opcode for full-page images (XLOG_FPI). Checkpoints
	// (0x00/0x10) are classified separately by the 88-byte branch.
	xlogFPI uint8 = 0xB0 // XLOG_FPI
)

// recordKindToRmgrInfo maps a goopg RecordKind byte (payload[0]) to the
// PG-compatible (xl_rmid, xl_info) it is emitted with. Records with no
// PG analog fall through to the custom RmgrGoopgCatalog rmgr, where the
// RecordKind byte itself remains the replay discriminator.
//
// Note: PageImage/Checkpoint are normally reached via the length-based
// branches in classifyXLogRecord before this table; they are included
// for completeness. Where one opcode covers several kinds (XactCommit vs
// XactCommitInval), recovery re-discriminates on payload[0].
func recordKindToRmgrInfo(kind byte) (Rmgr, uint8) {
	switch kind {
	// Heap (RM_HEAP)
	case RecordKindHeapInsert:
		return RmgrHeap, xlogHeapInsert
	case RecordKindHeapDelete:
		return RmgrHeap, xlogHeapDelete
	case RecordKindHeapHotUpdate:
		return RmgrHeap, xlogHeapHotUpdate
	case RecordKindHeapLock:
		return RmgrHeap, xlogHeapLock
	// Heap2 (RM_HEAP2)
	case RecordKindHeapPruneOpt:
		return RmgrHeap2, xlogHeap2PruneOnAccess
	case RecordKindHeapVacuum:
		return RmgrHeap2, xlogHeap2PruneVacuumScan
	case RecordKindHeapFreeze:
		return RmgrHeap2, xlogHeap2PruneVacuumCleanp
	// Btree (RM_BTREE)
	case RecordKindBtreeInsert:
		return RmgrBtree, xlogBtreeInsertLeaf
	case RecordKindBtreeSplit:
		return RmgrBtree, xlogBtreeSplitL
	case RecordKindBtreeVacuum:
		return RmgrBtree, xlogBtreeVacuum
	case RecordKindBtreeUnlinkPage:
		return RmgrBtree, xlogBtreeUnlinkPage
	case RecordKindBtreeNewRoot:
		return RmgrBtree, xlogBtreeNewRoot
	case RecordKindBtreeMarkPageHalfDead:
		return RmgrBtree, xlogBtreeMarkPageHalfDead
	// Xact (RM_XACT) — Commit and CommitInval share XLOG_XACT_COMMIT;
	// recovery re-keys on payload[0].
	case RecordKindXactCommit, RecordKindXactCommitInval:
		return RmgrXact, xlogXactCommit
	case RecordKindXactAbort:
		return RmgrXact, xlogXactAbort
	// Storage (RM_SMGR)
	case RecordKindSmgrCreate:
		return RmgrStorage, xlogSmgrCreate
	// CLOG (RM_CLOG)
	case RecordKindClogTruncate:
		return RmgrCLOG, xlogClogTruncate
	// XLOG full-page image
	case RecordKindPageImage:
		return RmgrXLog, xlogFPI
	default:
		// goopg-private catalog/DDL records: custom rmgr, body-keyed.
		return RmgrGoopgCatalog, 0
	}
}
