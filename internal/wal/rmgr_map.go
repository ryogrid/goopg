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
//
// The opcode constants themselves (xlogHeapLock, xlogHeap2Prune*,
// xlogBtree*, xlogSmgrCreate, xlogClogTruncate, xlogXLogFPI, ...) live in
// pg_xlog_decode.go, shared with the decode path.

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
		return RmgrHeap2, xlogHeap2PruneVacuumClean
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
		return RmgrXLog, xlogXLogFPI
	default:
		// goopg-private catalog/DDL records: custom rmgr, body-keyed.
		return RmgrGoopgCatalog, 0
	}
}
