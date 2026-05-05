package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

const (
	// RecordKindPageImage is a full-page image redo record.
	RecordKindPageImage byte = 1
	// RecordKindCheckpoint marks a consistent recovery boundary.
	RecordKindCheckpoint byte = 2
	// RecordKindBtreeSplit captures a B-tree page split atomically:
	// the post-split image of the left page plus the freshly
	// populated right page in one record. Replay applies both,
	// guaranteeing crash recovery never lands in the torn state
	// where left advertises a right-link to a page that's still
	// the bare smgr.Extend zero/init image. See
	// docs/design/0002-0002-btree-concurrency.md Landing 3a.
	RecordKindBtreeSplit byte = 3
	// RecordKindHeapInsert is a logical change record for one
	// heap insert. The full record is "kind | rel(9) | blk(4) |
	// lineSlot(2) | tuple-bytes". Replay reads the existing page
	// (or InitPage if missing) and re-applies the insert at the
	// recorded slot, keyed off pd_lsn idempotency. See
	// docs/design/0002-0003-redo-records.md.
	RecordKindHeapInsert byte = 4
	// RecordKindBtreeInsert is a logical change record for one
	// B-tree non-split insert. Format:
	// "kind | rel(9) | blk(4) | item-bytes". The item bytes are
	// the same shape internal/access/btree's `item.marshal`
	// produces (keyLen + ptr.block + ptr.offset + key). Replay
	// is idempotent via pd_lsn and applies the item to the
	// existing page in sorted order. See
	// docs/design/0002-0003-redo-records.md.
	RecordKindBtreeInsert byte = 5
	// RecordKindHeapDelete is a logical change record stamping
	// xmax on an existing heap tuple. The MVCC update path
	// emits one for the old image (followed by a HeapInsert for
	// the new); the DELETE path emits one per visible match.
	// Format: "kind | rel(9) | blk(4) | lineSlot(2) | xmax(4)"
	// = 20 bytes, fixed. Replay is idempotent via pd_lsn.
	RecordKindHeapDelete byte = 6
	// RecordKindHeapVacuum is a logical change record for one
	// heap page-prune. VACUUM emits one per pruned page,
	// carrying the 1-based LP_NORMAL slot numbers it reclaimed
	// to LP_UNUSED. Replay calls
	// storage.VacuumHeapPageBySlots with the same list, so
	// the post-replay page is bit-exact with the original
	// post-prune image. Format:
	// "kind | rel(9) | blk(4) | count(2) | slots[count](2 each)"
	// = 16 + 2*count bytes. Replay is idempotent via pd_lsn.
	RecordKindHeapVacuum byte = 7
	// RecordKindXactCommit marks the boundary that releases a
	// transaction's queued changes from the M0008 reorder buffer
	// to the output plugin. Carries the xid so the logical
	// decoder can route it. Crash-recovery is a no-op for this
	// kind — the per-record idempotency in the data records is
	// sufficient to bring storage back to a consistent state;
	// the commit/abort markers exist purely so the logical
	// decoder can make commit/abort decisions. Format:
	// "kind(1) | xid(4)" = 5 bytes. See
	// docs/design/0008-0001-logical-decoding-pipeline.md.
	RecordKindXactCommit byte = 8
	// RecordKindXactAbort marks the boundary that drops a
	// transaction's queued changes from the M0008 reorder buffer
	// without emission. Same format / recovery semantics as
	// RecordKindXactCommit.
	RecordKindXactAbort byte = 9
	// RecordKindHeapLock is the row-lock redo record (M0021
	// tuple-level locking step 3). Stamps an xmax + the
	// HEAP_XMAX_LOCK_ONLY infomask bit + a lock-strength bit on
	// an existing heap tuple. Mirrors upstream's xl_heap_lock
	// record. The record is idempotent via pd_lsn — re-applying
	// after a crash is a no-op when the page already advertises
	// an LSN >= record.endLSN. Format:
	// "kind(1) | rel(9) | blk(4) | lineSlot(2) | xmax(4) |
	//  lockStrength(2)" = 22 bytes.
	RecordKindHeapLock byte = 10
	// RecordKindHeapHotUpdate is a logical HOT-update record (M0046-0001).
	// Encodes the old-slot xmax stamp + HeapHotUpdated infomask + CTID
	// chain linkage + the new tuple bytes (which carry HeapOnlyTuple in
	// their infomask) — all on the same heap page. Replay inserts the
	// new tuple (obtaining newSlot), then stamps the old slot.
	// Format: "kind(1) | DBOid(4) | RelOid(4) | Fork(1) | Block(4) |
	//          oldSlot(2) | xmax(4) | newTupleBytes(var)" = 20 bytes fixed.
	RecordKindHeapHotUpdate byte = 13
	// RecordKindHeapPruneOpt is a logical opportunistic-pruning record
	// (M0046-0002). Mirrors PostgreSQL's XLOG_HEAP2_PRUNE. Emitted when
	// the HOT-update path reclaims dead tuple slots inline (without a
	// full VACUUM pass). Same format as RecordKindHeapVacuum so the
	// replay path is identical. Format:
	// "kind(1) | DBOid(4) | RelOid(4) | Fork(1) | Block(4) |
	//  count(2) | slots[count](2 each)" = 16 + 2*count bytes.
	RecordKindHeapPruneOpt byte = 14
	// RecordKindSmgrCreate logs the first extension of a relation file
	// (mirrors upstream's XLOG_SMGR_CREATE in
	// postgres/src/include/catalog/storage_xlog.h). Emitted by the buffer
	// pool when Pool.PinNew creates block 0 of a new relation. Redo:
	// ensure the relfile exists with at least one initialised block.
	// Format: "kind(1) | DBOid(4) | RelOid(4) | Fork(1)" = 10 bytes.
	RecordKindSmgrCreate byte = 11
	// RecordKindSmgrTruncate logs a relation-file truncation (mirrors
	// upstream's XLOG_SMGR_TRUNCATE). Emitted by TRUNCATE TABLE. Redo:
	// truncate the relfile to 0 blocks. Same format as SmgrCreate.
	RecordKindSmgrTruncate byte = 12

	// RecordKindXactAssignment records the first lazy XID allocation
	// for one or more subtransactions (M0050-0003). Emitted when a
	// subxact writes for the first time; replay populates the
	// subxact-to-parent map in mvcc.Manager. Format:
	//   kind(1) | parentXid(4) | count(2) | subXids[count](4 each)
	RecordKindXactAssignment byte = 15

	// RecordKindXactRollbackTo records ROLLBACK TO SAVEPOINT (M0050-0003).
	// Replay marks each listed subXid as individually aborted in
	// mvcc.Manager. Format:
	//   kind(1) | parentXid(4) | count(2) | abortedSubXids[count](4 each)
	RecordKindXactRollbackTo byte = 16

	// RecordKindXactSubAbort records a single subxact abort triggered
	// without a named savepoint (M0050-0003). Format:
	//   kind(1) | subXid(4) = 5 bytes total.
	RecordKindXactSubAbort byte = 17

	// RecordKindCreateDatabase records a `CREATE DATABASE <name>` event
	// so the catalog's per-instance database list can be reconstructed
	// after a crash (M0054-0001). The redo path does NOT touch on-disk
	// storage — goopg v0 has no per-database file namespacing — so
	// applyRecord returns (false, nil); the recovery driver in
	// internal/initdb scans the WAL for these records after physical
	// replay and re-registers each database name in the catalog.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes)
	RecordKindCreateDatabase byte = 18

	// RecordKindDropDatabase records a `DROP DATABASE <name>` event.
	// Counterpart to RecordKindCreateDatabase. Same format / replay
	// semantics; the recovery driver removes the name from the
	// catalog instead of adding it.
	RecordKindDropDatabase byte = 19

	// smgrRecordSize: kind(1) + DBOid(4) + RelOid(4) + Fork(1) = 10 bytes.
	smgrRecordSize = 10

	pageImageHeaderSize = 14
	// btreeSplitHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + LeftBlk(4) + RightBlk(4) = 18.
	btreeSplitHeaderSize = 18
	// heapInsertHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) + LineSlot(2) = 16.
	heapInsertHeaderSize = 16
	// btreeInsertHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) = 14.
	btreeInsertHeaderSize = 14
	// heapDeleteSize: kind(1) + DBOid(4) + RelOid(4) + Fork(1)
	// + Block(4) + LineSlot(2) + Xmax(4) = 20.
	heapDeleteSize = 20
	// heapVacuumHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) + Count(2) = 16.
	heapVacuumHeaderSize = 16
	// xactRecordSize: kind(1) + xid(4) = 5. Shared by
	// RecordKindXactCommit and RecordKindXactAbort.
	xactRecordSize = 5
	// heapLockSize: kind(1) + DBOid(4) + RelOid(4) + Fork(1)
	// + Block(4) + LineSlot(2) + Xmax(4) + LockStrength(2) = 22.
	heapLockSize = 22
	// heapHotUpdateHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) + OldSlot(2) + Xmax(4) = 20. Variable
	// new-tuple bytes follow.
	heapHotUpdateHeaderSize = 20
)

// EncodeCreateDatabase encodes a CREATE DATABASE event (M0054-0001).
// Format: kind(1) | nameLen(2) | name(nameLen bytes).
func EncodeCreateDatabase(name string) []byte {
	if len(name) > 0xFFFF {
		// goopg's identifier length cap is far below 64 KiB; truncating
		// here is defensive — this branch is unreachable under normal
		// CREATE DATABASE syntax.
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindCreateDatabase
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeCreateDatabase decodes a RecordKindCreateDatabase payload.
func DecodeCreateDatabase(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: create-database payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateDatabase {
		return "", fmt.Errorf("wal: record kind %d is not create-database", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: create-database payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeDropDatabase encodes a DROP DATABASE event (M0054-0001).
// Format identical to EncodeCreateDatabase.
func EncodeDropDatabase(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropDatabase
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropDatabase decodes a RecordKindDropDatabase payload.
func DecodeDropDatabase(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-database payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropDatabase {
		return "", fmt.Errorf("wal: record kind %d is not drop-database", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-database payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeXactAssignment encodes a subxact XID assignment record (M0050-0003).
// parentXid is the top-level transaction; subXids lists the subxact XIDs
// that are now children of parentXid. Replay calls Manager.RegisterSubXid
// for each entry. Format: kind(1) | parentXid(4) | count(2) | subXids[].
func EncodeXactAssignment(parentXid storage.TransactionID, subXids []storage.TransactionID) []byte {
	out := make([]byte, 7+4*len(subXids))
	out[0] = RecordKindXactAssignment
	binary.LittleEndian.PutUint32(out[1:5], uint32(parentXid))
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(subXids)))
	for i, s := range subXids {
		binary.LittleEndian.PutUint32(out[7+4*i:], uint32(s))
	}
	return out
}

// DecodeXactAssignment decodes a RecordKindXactAssignment payload.
func DecodeXactAssignment(payload []byte) (parentXid storage.TransactionID, subXids []storage.TransactionID, err error) {
	if len(payload) < 7 {
		return 0, nil, fmt.Errorf("wal: xact-assignment payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindXactAssignment {
		return 0, nil, fmt.Errorf("wal: record kind %d is not xact-assignment", payload[0])
	}
	parentXid = storage.TransactionID(binary.LittleEndian.Uint32(payload[1:5]))
	count := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+4*count {
		return 0, nil, fmt.Errorf("wal: xact-assignment payload truncated (need %d bytes)", 7+4*count)
	}
	subXids = make([]storage.TransactionID, count)
	for i := range subXids {
		subXids[i] = storage.TransactionID(binary.LittleEndian.Uint32(payload[7+4*i:]))
	}
	return parentXid, subXids, nil
}

// EncodeXactRollbackTo encodes a ROLLBACK TO SAVEPOINT record (M0050-0003).
// parentXid is the top-level transaction; abortedSubXids lists the subxact
// XIDs that were individually rolled back. Replay calls Manager.MarkSubxactAborted
// for each. Format: kind(1) | parentXid(4) | count(2) | abortedSubXids[].
func EncodeXactRollbackTo(parentXid storage.TransactionID, abortedSubXids []storage.TransactionID) []byte {
	out := make([]byte, 7+4*len(abortedSubXids))
	out[0] = RecordKindXactRollbackTo
	binary.LittleEndian.PutUint32(out[1:5], uint32(parentXid))
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(abortedSubXids)))
	for i, s := range abortedSubXids {
		binary.LittleEndian.PutUint32(out[7+4*i:], uint32(s))
	}
	return out
}

// DecodeXactRollbackTo decodes a RecordKindXactRollbackTo payload.
func DecodeXactRollbackTo(payload []byte) (parentXid storage.TransactionID, abortedSubXids []storage.TransactionID, err error) {
	if len(payload) < 7 {
		return 0, nil, fmt.Errorf("wal: xact-rollback-to payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindXactRollbackTo {
		return 0, nil, fmt.Errorf("wal: record kind %d is not xact-rollback-to", payload[0])
	}
	parentXid = storage.TransactionID(binary.LittleEndian.Uint32(payload[1:5]))
	count := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+4*count {
		return 0, nil, fmt.Errorf("wal: xact-rollback-to payload truncated (need %d bytes)", 7+4*count)
	}
	abortedSubXids = make([]storage.TransactionID, count)
	for i := range abortedSubXids {
		abortedSubXids[i] = storage.TransactionID(binary.LittleEndian.Uint32(payload[7+4*i:]))
	}
	return parentXid, abortedSubXids, nil
}

// EncodeXactSubAbort encodes a single subxact abort record (M0050-0003).
// Replay calls Manager.MarkSubxactAborted(subXid). Format: kind(1)|subXid(4).
func EncodeXactSubAbort(subXid storage.TransactionID) []byte {
	out := make([]byte, xactRecordSize)
	out[0] = RecordKindXactSubAbort
	binary.LittleEndian.PutUint32(out[1:5], uint32(subXid))
	return out
}

// DecodeXactSubAbort decodes a RecordKindXactSubAbort payload.
func DecodeXactSubAbort(payload []byte) (subXid storage.TransactionID, err error) {
	if len(payload) != xactRecordSize {
		return 0, fmt.Errorf("wal: xact-subabort payload len %d (want %d)", len(payload), xactRecordSize)
	}
	if payload[0] != RecordKindXactSubAbort {
		return 0, fmt.Errorf("wal: record kind %d is not xact-subabort", payload[0])
	}
	return storage.TransactionID(binary.LittleEndian.Uint32(payload[1:5])), nil
}

// EncodeXactCommit encodes a logical-decoding commit marker for
// xid. Crash recovery skips this kind; only the M0008 logical
// decoder consumes it. See
// docs/design/0008-0001-logical-decoding-pipeline.md.
func EncodeXactCommit(xid storage.TransactionID) []byte {
	out := make([]byte, xactRecordSize)
	out[0] = RecordKindXactCommit
	binary.LittleEndian.PutUint32(out[1:5], uint32(xid))
	return out
}

// EncodeXactAbort encodes a logical-decoding abort marker.
func EncodeXactAbort(xid storage.TransactionID) []byte {
	out := make([]byte, xactRecordSize)
	out[0] = RecordKindXactAbort
	binary.LittleEndian.PutUint32(out[1:5], uint32(xid))
	return out
}

// DecodeXactMarker returns the xid carried by a commit or abort
// marker payload. The caller already knows the kind from the
// payload's first byte; this helper just unpacks the xid.
func DecodeXactMarker(payload []byte) (storage.TransactionID, error) {
	if len(payload) != xactRecordSize {
		return 0, fmt.Errorf("wal: invalid xact-marker payload len %d (want %d)", len(payload), xactRecordSize)
	}
	if payload[0] != RecordKindXactCommit && payload[0] != RecordKindXactAbort {
		return 0, fmt.Errorf("wal: record kind %d is not an xact marker", payload[0])
	}
	return storage.TransactionID(binary.LittleEndian.Uint32(payload[1:5])), nil
}

// ReplayStats summarizes one replay run.
type ReplayStats struct {
	Records       int
	Applied       int
	CheckpointLSN uint64
}

// EncodeCheckpoint encodes a checkpoint marker record payload.
func EncodeCheckpoint() []byte {
	return []byte{RecordKindCheckpoint}
}

// EncodePageImage encodes one full-page image record payload.
func EncodePageImage(rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) ([]byte, error) {
	if len(page) != storage.BlockSize {
		return nil, fmt.Errorf("wal: page image is %d bytes, want %d", len(page), storage.BlockSize)
	}
	out := make([]byte, pageImageHeaderSize+storage.BlockSize)
	out[0] = RecordKindPageImage
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	copy(out[pageImageHeaderSize:], page)
	return out, nil
}

// EncodeHeapInsert encodes one logical heap-insert redo record.
// `lineSlot` is the 1-based line-pointer slot returned by
// PageAddHeapTuple at original mutation time; replay restores the
// same line-pointer assignment by inserting at that slot.
func EncodeHeapInsert(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, tuple []byte) []byte {
	out := make([]byte, heapInsertHeaderSize+len(tuple))
	out[0] = RecordKindHeapInsert
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], lineSlot)
	copy(out[heapInsertHeaderSize:], tuple)
	return out
}

// DecodeHeapInsert returns the rel + block + lineSlot + tuple
// bytes carried by a HeapInsert record payload.
func DecodeHeapInsert(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, tuple []byte, err error) {
	if len(payload) < heapInsertHeaderSize {
		err = fmt.Errorf("wal: invalid heap-insert payload len %d (want >= %d)", len(payload), heapInsertHeaderSize)
		return
	}
	if payload[0] != RecordKindHeapInsert {
		err = fmt.Errorf("wal: record kind %d is not heap-insert", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	lineSlot = binary.LittleEndian.Uint16(payload[14:16])
	tuple = make([]byte, len(payload)-heapInsertHeaderSize)
	copy(tuple, payload[heapInsertHeaderSize:])
	return
}

// EncodeHeapDelete encodes one logical heap-delete (xmax stamp)
// redo record.
func EncodeHeapDelete(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID) []byte {
	out := make([]byte, heapDeleteSize)
	out[0] = RecordKindHeapDelete
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], lineSlot)
	binary.LittleEndian.PutUint32(out[16:20], uint32(xmax))
	return out
}

// DecodeHeapDelete decodes a HeapDelete record payload.
func DecodeHeapDelete(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, err error) {
	if len(payload) != heapDeleteSize {
		err = fmt.Errorf("wal: invalid heap-delete payload len %d (want %d)", len(payload), heapDeleteSize)
		return
	}
	if payload[0] != RecordKindHeapDelete {
		err = fmt.Errorf("wal: record kind %d is not heap-delete", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	lineSlot = binary.LittleEndian.Uint16(payload[14:16])
	xmax = storage.TransactionID(binary.LittleEndian.Uint32(payload[16:20]))
	return
}

// EncodeHeapLock encodes one row-lock redo record (M0021
// tuple-level locking step 3). `xmax` is the locking xact's xid;
// `lockStrength` carries the HeapXmax* lock-mode bits to OR into
// the tuple's infomask alongside HEAP_XMAX_LOCK_ONLY. Mirrors
// upstream's xl_heap_lock at the level of detail goopg's replay
// path needs; XID-tracking and MultiXact handling are deferred.
func EncodeHeapLock(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, lockStrength uint16) []byte {
	out := make([]byte, heapLockSize)
	out[0] = RecordKindHeapLock
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], lineSlot)
	binary.LittleEndian.PutUint32(out[16:20], uint32(xmax))
	binary.LittleEndian.PutUint16(out[20:22], lockStrength)
	return out
}

// DecodeHeapLock decodes a HeapLock record payload.
func DecodeHeapLock(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, lockStrength uint16, err error) {
	if len(payload) != heapLockSize {
		err = fmt.Errorf("wal: invalid heap-lock payload len %d (want %d)", len(payload), heapLockSize)
		return
	}
	if payload[0] != RecordKindHeapLock {
		err = fmt.Errorf("wal: record kind %d is not heap-lock", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	lineSlot = binary.LittleEndian.Uint16(payload[14:16])
	xmax = storage.TransactionID(binary.LittleEndian.Uint32(payload[16:20]))
	lockStrength = binary.LittleEndian.Uint16(payload[20:22])
	return
}

// EncodeHeapHotUpdate encodes one atomic HOT-update redo record
// (M0046-0001). The record captures the old-slot xmax stamp and the
// new tuple bytes (which carry HeapOnlyTuple in their infomask) — both
// on the same heap page. Replay inserts the new tuple (getting
// newSlot), then stamps the old slot via PageStampHotOldTuple.
func EncodeHeapHotUpdate(rel storage.RelFileNode, blk storage.BlockNumber, oldSlot uint16, xmax storage.TransactionID, tupleBytes []byte) []byte {
	out := make([]byte, heapHotUpdateHeaderSize+len(tupleBytes))
	out[0] = RecordKindHeapHotUpdate
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], oldSlot)
	binary.LittleEndian.PutUint32(out[16:20], uint32(xmax))
	copy(out[heapHotUpdateHeaderSize:], tupleBytes)
	return out
}

// DecodeHeapHotUpdate decodes a HeapHotUpdate record payload.
func DecodeHeapHotUpdate(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, oldSlot uint16, xmax storage.TransactionID, tupleBytes []byte, err error) {
	if len(payload) < heapHotUpdateHeaderSize {
		err = fmt.Errorf("wal: invalid heap-hot-update payload len %d (want >= %d)", len(payload), heapHotUpdateHeaderSize)
		return
	}
	if payload[0] != RecordKindHeapHotUpdate {
		err = fmt.Errorf("wal: record kind %d is not heap-hot-update", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	oldSlot = binary.LittleEndian.Uint16(payload[14:16])
	xmax = storage.TransactionID(binary.LittleEndian.Uint32(payload[16:20]))
	tupleBytes = make([]byte, len(payload)-heapHotUpdateHeaderSize)
	copy(tupleBytes, payload[heapHotUpdateHeaderSize:])
	return
}

// heapPruneOptHdrSize: kind(1) + rel(9) + blk(4) + nRedirects(2) + nUnused(2) = 18.
const heapPruneOptHdrSize = 18

// EncodeHeapPruneOpt encodes one opportunistic page-pruning redo record
// (M0046-0002, mirrors PostgreSQL's XLOG_HEAP2_PRUNE). Carries two lists:
//   - redirects: (oldSlot, newSlot) pairs for HOT chain root slots that were
//     converted to ItemIDRedirect so the index entry stays valid.
//   - unused: slot numbers marked ItemIDUnused (HOT-only and standalone dead).
//
// Format:
//   kind(1) | rel(9) | blk(4) | nRedirects(2) | nUnused(2) |
//   redirects[nRedirects*4] | unusedSlots[nUnused*2]
func EncodeHeapPruneOpt(rel storage.RelFileNode, blk storage.BlockNumber, redirects [][2]uint16, unused []uint16) []byte {
	sz := heapPruneOptHdrSize + 4*len(redirects) + 2*len(unused)
	out := make([]byte, sz)
	out[0] = RecordKindHeapPruneOpt
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], uint16(len(redirects)))
	binary.LittleEndian.PutUint16(out[16:18], uint16(len(unused)))
	off := heapPruneOptHdrSize
	for _, r := range redirects {
		binary.LittleEndian.PutUint16(out[off:off+2], r[0])
		binary.LittleEndian.PutUint16(out[off+2:off+4], r[1])
		off += 4
	}
	for _, s := range unused {
		binary.LittleEndian.PutUint16(out[off:off+2], s)
		off += 2
	}
	return out
}

// DecodeHeapPruneOpt returns the rel + block + redirect pairs + unused slots
// carried by a HeapPruneOpt record payload.
func DecodeHeapPruneOpt(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, redirects [][2]uint16, unused []uint16, err error) {
	if len(payload) < heapPruneOptHdrSize {
		err = fmt.Errorf("wal: invalid heap-prune-opt payload len %d (want >= %d)", len(payload), heapPruneOptHdrSize)
		return
	}
	if payload[0] != RecordKindHeapPruneOpt {
		err = fmt.Errorf("wal: record kind %d is not heap-prune-opt", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	nRedirects := int(binary.LittleEndian.Uint16(payload[14:16]))
	nUnused := int(binary.LittleEndian.Uint16(payload[16:18]))
	want := heapPruneOptHdrSize + 4*nRedirects + 2*nUnused
	if len(payload) != want {
		err = fmt.Errorf("wal: heap-prune-opt payload len %d want %d (nRedirects=%d nUnused=%d)", len(payload), want, nRedirects, nUnused)
		return
	}
	off := heapPruneOptHdrSize
	redirects = make([][2]uint16, nRedirects)
	for i := range redirects {
		redirects[i][0] = binary.LittleEndian.Uint16(payload[off : off+2])
		redirects[i][1] = binary.LittleEndian.Uint16(payload[off+2 : off+4])
		off += 4
	}
	unused = make([]uint16, nUnused)
	for i := range unused {
		unused[i] = binary.LittleEndian.Uint16(payload[off : off+2])
		off += 2
	}
	return
}

// EncodeSmgrCreate encodes a relation-file creation record.
// Mirrors upstream's XLOG_SMGR_CREATE. The redo handler ensures
// the relfile has at least one initialised block (idempotent).
func EncodeSmgrCreate(rel storage.RelFileNode) []byte {
	out := make([]byte, smgrRecordSize)
	out[0] = RecordKindSmgrCreate
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	return out
}

// DecodeSmgrCreate decodes a SmgrCreate record payload.
func DecodeSmgrCreate(payload []byte) (rel storage.RelFileNode, err error) {
	if len(payload) < smgrRecordSize {
		err = fmt.Errorf("wal: invalid smgr-create payload len %d (want %d)", len(payload), smgrRecordSize)
		return
	}
	if payload[0] != RecordKindSmgrCreate {
		err = fmt.Errorf("wal: record kind %d is not smgr-create", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	return
}

// EncodeSmgrTruncate encodes a relation-file truncation record.
// Mirrors upstream's XLOG_SMGR_TRUNCATE. The redo handler truncates
// the relfile to 0 blocks.
func EncodeSmgrTruncate(rel storage.RelFileNode) []byte {
	out := make([]byte, smgrRecordSize)
	out[0] = RecordKindSmgrTruncate
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	return out
}

// DecodeSmgrTruncate decodes a SmgrTruncate record payload.
func DecodeSmgrTruncate(payload []byte) (rel storage.RelFileNode, err error) {
	if len(payload) < smgrRecordSize {
		err = fmt.Errorf("wal: invalid smgr-truncate payload len %d (want %d)", len(payload), smgrRecordSize)
		return
	}
	if payload[0] != RecordKindSmgrTruncate {
		err = fmt.Errorf("wal: record kind %d is not smgr-truncate", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	return
}

// EncodeHeapVacuum encodes one logical heap-vacuum (page prune)
// redo record. `deadSlots` carries the 1-based LP_NORMAL slot
// numbers to reclaim, in any order — replay treats the list as
// a set.
func EncodeHeapVacuum(rel storage.RelFileNode, blk storage.BlockNumber, deadSlots []uint16) []byte {
	out := make([]byte, heapVacuumHeaderSize+2*len(deadSlots))
	out[0] = RecordKindHeapVacuum
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], uint16(len(deadSlots)))
	for i, s := range deadSlots {
		binary.LittleEndian.PutUint16(out[heapVacuumHeaderSize+2*i:heapVacuumHeaderSize+2*i+2], s)
	}
	return out
}

// DecodeHeapVacuum returns the rel + block + dead-slot list
// carried by a HeapVacuum record payload.
func DecodeHeapVacuum(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, deadSlots []uint16, err error) {
	if len(payload) < heapVacuumHeaderSize {
		err = fmt.Errorf("wal: invalid heap-vacuum payload len %d (want >= %d)", len(payload), heapVacuumHeaderSize)
		return
	}
	if payload[0] != RecordKindHeapVacuum {
		err = fmt.Errorf("wal: record kind %d is not heap-vacuum", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	count := int(binary.LittleEndian.Uint16(payload[14:16]))
	want := heapVacuumHeaderSize + 2*count
	if len(payload) != want {
		err = fmt.Errorf("wal: heap-vacuum payload len %d does not match count=%d (want %d)", len(payload), count, want)
		return
	}
	deadSlots = make([]uint16, count)
	for i := 0; i < count; i++ {
		deadSlots[i] = binary.LittleEndian.Uint16(payload[heapVacuumHeaderSize+2*i : heapVacuumHeaderSize+2*i+2])
	}
	return
}

// EncodeBtreeInsert encodes one logical B-tree non-split insert
// redo record. The opaque `item` payload is whatever bytes the
// caller stored on the page (in v0,
// internal/access/btree.item.marshal output: keyLen + ptr.block
// + ptr.offset + key).
func EncodeBtreeInsert(rel storage.RelFileNode, blk storage.BlockNumber, item []byte) []byte {
	out := make([]byte, btreeInsertHeaderSize+len(item))
	out[0] = RecordKindBtreeInsert
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	copy(out[btreeInsertHeaderSize:], item)
	return out
}

// DecodeBtreeInsert returns the rel + block + raw item bytes
// carried by a BtreeInsert record payload.
func DecodeBtreeInsert(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, item []byte, err error) {
	if len(payload) < btreeInsertHeaderSize {
		err = fmt.Errorf("wal: invalid btree-insert payload len %d (want >= %d)", len(payload), btreeInsertHeaderSize)
		return
	}
	if payload[0] != RecordKindBtreeInsert {
		err = fmt.Errorf("wal: record kind %d is not btree-insert", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	item = make([]byte, len(payload)-btreeInsertHeaderSize)
	copy(item, payload[btreeInsertHeaderSize:])
	return
}

// EncodeBtreeSplit encodes one atomic B-tree split record. Both
// pages must be exactly storage.BlockSize bytes; the record
// embeds them in left-then-right order so replay applies the new
// right page before any reader could follow left's right-link to
// it.
func EncodeBtreeSplit(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page) ([]byte, error) {
	if len(leftPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split left page is %d bytes, want %d", len(leftPage), storage.BlockSize)
	}
	if len(rightPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split right page is %d bytes, want %d", len(rightPage), storage.BlockSize)
	}
	out := make([]byte, btreeSplitHeaderSize+2*storage.BlockSize)
	out[0] = RecordKindBtreeSplit
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(leftBlk))
	binary.LittleEndian.PutUint32(out[14:18], uint32(rightBlk))
	copy(out[btreeSplitHeaderSize:btreeSplitHeaderSize+storage.BlockSize], leftPage)
	copy(out[btreeSplitHeaderSize+storage.BlockSize:], rightPage)
	return out, nil
}

// DecodeBtreeSplit returns the rel + (left,right) blocks + page
// images carried by a BtreeSplit record payload.
func DecodeBtreeSplit(payload []byte) (rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page, err error) {
	want := btreeSplitHeaderSize + 2*storage.BlockSize
	if len(payload) != want {
		err = fmt.Errorf("wal: invalid btree-split payload len %d (want %d)", len(payload), want)
		return
	}
	if payload[0] != RecordKindBtreeSplit {
		err = fmt.Errorf("wal: record kind %d is not btree-split", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	leftBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	rightBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[14:18]))
	leftPage = make(storage.Page, storage.BlockSize)
	copy(leftPage, payload[btreeSplitHeaderSize:btreeSplitHeaderSize+storage.BlockSize])
	rightPage = make(storage.Page, storage.BlockSize)
	copy(rightPage, payload[btreeSplitHeaderSize+storage.BlockSize:])
	return
}

// DecodePageImage decodes a full-page image record payload.
func DecodePageImage(payload []byte) (storage.RelFileNode, storage.BlockNumber, storage.Page, error) {
	if len(payload) != pageImageHeaderSize+storage.BlockSize {
		return storage.RelFileNode{}, storage.InvalidBlockNumber, nil,
			fmt.Errorf("wal: invalid page-image payload len %d", len(payload))
	}
	if payload[0] != RecordKindPageImage {
		return storage.RelFileNode{}, storage.InvalidBlockNumber, nil,
			fmt.Errorf("wal: record kind %d is not page image", payload[0])
	}
	rel := storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk := storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	page := make(storage.Page, storage.BlockSize)
	copy(page, payload[pageImageHeaderSize:])
	return rel, blk, page, nil
}

// ReplayRecords replays decoded WAL records into storage.
//
// M0045-0002: replay starts FROM the last checkpoint (inclusive),
// not up to it. The checkpoint marks a point where all dirty pages
// were flushed; only records AFTER the checkpoint need to be applied
// to recover pages that may not have been flushed before the crash.
// Records before the checkpoint are already on disk and ApplyRecord's
// per-page LSN check makes re-application a safe no-op, but we skip
// them as an optimisation.
//
// If no checkpoint record exists, all records are replayed from the
// start (safe for fresh clusters or WAL without checkpoints).
func ReplayRecords(mgr *storage.Manager, records []Record) (ReplayStats, error) {
	stats := ReplayStats{Records: len(records)}
	startIdx, checkpointLSN := replayStart(records)
	stats.CheckpointLSN = checkpointLSN

	for i, r := range records[startIdx:] {
		applied, err := ApplyRecord(mgr, r)
		if err != nil {
			return stats, fmt.Errorf("wal: replay record %d lsn[%d,%d]: %w", startIdx+i, r.StartLSN, r.EndLSN, err)
		}
		if applied {
			stats.Applied++
		}
	}
	return stats, nil
}

// ApplyRecord applies a single decoded WAL record to storage. It is
// the per-record kernel shared by `ReplayRecords` (crash recovery
// from a slice already trimmed to the last checkpoint) and
// `StreamReplayer` (continuous standby replay driven by a streaming
// `RecordIterator`). Returns `applied=true` when a real page mutation
// happened, `applied=false` for marker-only records (Checkpoint).
//
// All physical and logical applies are individually idempotent via
// `pd_lsn`: re-applying a record that was already persisted is a
// no-op, which means the stream replayer can resume from any point
// the local WAL writer's `WrittenLSN` advertises without bookkeeping
// a separate "apply cursor" — a record that finished on disk but
// crashed before the storage write is re-attempted on restart, and
// one that finished both is silently skipped.
func ApplyRecord(mgr *storage.Manager, r Record) (bool, error) {
	if len(r.Payload) == 0 {
		return false, errors.New("wal: empty record payload")
	}
	switch r.Payload[0] {
	case RecordKindPageImage:
		if err := replayPageImage(mgr, r.Payload); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindBtreeSplit:
		if err := replayBtreeSplit(mgr, r.Payload); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapInsert:
		if err := replayHeapInsert(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindBtreeInsert:
		if err := replayBtreeInsert(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapDelete:
		if err := replayHeapDelete(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapLock:
		if err := replayHeapLock(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapVacuum:
		if err := replayHeapVacuum(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindCheckpoint:
		return false, nil
	case RecordKindXactCommit, RecordKindXactAbort:
		// Logical-decoding markers — physical recovery is a
		// no-op. The per-record idempotency in the data records
		// already brings storage to a consistent state; the
		// markers exist purely so the M0008 logical decoder can
		// drive its reorder buffer. See
		// docs/design/0008-0001-logical-decoding-pipeline.md.
		return false, nil
	case RecordKindCreateDatabase, RecordKindDropDatabase:
		// CREATE/DROP DATABASE records (M0054-0001) carry only a database
		// name; goopg v0 has no per-database file namespacing, so the
		// physical replay path has nothing to do. The recovery driver in
		// internal/initdb/open.go scans the WAL for these records after
		// physical replay and re-applies them to the catalog's database
		// list.
		return false, nil
	case RecordKindXactAssignment, RecordKindXactRollbackTo, RecordKindXactSubAbort:
		// Subxact markers (M0050-0003) — physical page recovery is
		// a no-op. The mvcc.Manager rebuilds its subxact-to-parent
		// map from these records during recovery; the physical replay
		// path (ReplayRecords) has no access to mvcc.Manager. The
		// full integration is wired by the recovery driver in
		// internal/initdb/open.go (M0050-0004).
		return false, nil
	case RecordKindHeapHotUpdate:
		if err := replayHeapHotUpdate(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapPruneOpt:
		if err := replayHeapPruneOpt(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindSmgrCreate:
		if err := replaySmgrCreate(mgr, r.Payload); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindSmgrTruncate:
		if err := replaySmgrTruncate(mgr, r.Payload); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported kind %d", r.Payload[0])
	}
}

// ReplayFromDir reads records from <dataDir>/pg_wal and replays them.
func ReplayFromDir(dataDir string, segmentSize int64) (ReplayStats, error) {
	records, err := ReadAll(filepath.Join(dataDir, "pg_wal"), segmentSize)
	if err != nil {
		return ReplayStats{}, err
	}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer func() { _ = mgr.Close() }()
	return ReplayRecords(mgr, records)
}

// ReplayFromDirWithMgr replays the WAL segments under walDir into
// the supplied Manager. Used by initdb.Open at startup so the
// runtime's single Manager handles both the replay phase and
// subsequent normal I/O. A missing or empty walDir is treated as
// "nothing to replay" (a freshly initdb'd cluster). segmentSize
// of 0 means use the default DefaultSegmentSize.
func ReplayFromDirWithMgr(mgr *storage.Manager, walDir string, segmentSize int64) (ReplayStats, error) {
	if segmentSize == 0 {
		segmentSize = DefaultSegmentSize
	}
	records, err := ReadAll(walDir, segmentSize)
	if err != nil {
		// Missing pg_wal on a fresh data dir is fine — no records
		// to replay.
		if errors.Is(err, fs.ErrNotExist) {
			return ReplayStats{}, nil
		}
		return ReplayStats{}, err
	}
	return ReplayRecords(mgr, records)
}

// replayHeapVacuum applies one logical heap-vacuum prune record.
// The page must already exist; idempotent via pd_lsn.
func replayHeapVacuum(mgr *storage.Manager, r Record) error {
	rel, blk, deadSlots, err := DecodeHeapVacuum(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-vacuum replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-vacuum replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if _, err := storage.VacuumHeapPageBySlots(page, deadSlots); err != nil {
		return fmt.Errorf("wal: heap-vacuum apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayHeapLock applies one row-lock redo record. The page must
// already exist (a HeapInsert or earlier mutation produced it).
// Idempotent via pd_lsn — re-applying a record after a crash is a
// no-op when the page already advertises an LSN >= record.endLSN.
func replayHeapLock(mgr *storage.Manager, r Record) error {
	rel, blk, lineSlot, xmax, lockStrength, err := DecodeHeapLock(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-lock replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-lock replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if err := storage.PageSetHeapTupleLockOnly(page, lineSlot, xmax, lockStrength); err != nil {
		return fmt.Errorf("wal: heap-lock apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayHeapDelete applies one logical xmax-stamp record. The
// page must already exist (HeapInsert or an earlier mutation
// produced it). Idempotent via pd_lsn.
func replayHeapDelete(mgr *storage.Manager, r Record) error {
	rel, blk, lineSlot, xmax, err := DecodeHeapDelete(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-delete replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-delete replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if err := storage.PageSetHeapTupleXmax(page, lineSlot, xmax); err != nil {
		return fmt.Errorf("wal: heap-delete apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayBtreeInsert applies one logical B-tree non-split insert
// to the data file. Idempotent via pd_lsn: skipped if the page
// already advertises an LSN >= record.endLSN. The page must
// already exist; bt-insert is never the first record for a page
// (a split or initial Create produced the page first), so a
// missing block is a hard error.
func replayBtreeInsert(mgr *storage.Manager, r Record) error {
	rel, blk, item, err := DecodeBtreeInsert(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: btree-insert replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: btree-insert replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if err := btree.ApplyInsertRecord(page, item); err != nil {
		return fmt.Errorf("wal: btree-insert apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayHeapInsert applies one logical heap-insert record to the
// data file. Idempotent via pd_lsn: if the page already carries an
// LSN >= record.endLSN, the change is already persisted and the
// apply is skipped. Otherwise, decode, InitPage if the page is
// missing, PageAddHeapTuple at the recorded slot, set pd_lsn,
// write back.
func replayHeapInsert(mgr *storage.Manager, r Record) error {
	rel, blk, lineSlot, tuple, err := DecodeHeapInsert(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	page := make(storage.Page, storage.BlockSize)
	switch {
	case blk < nblocks:
		if err := mgr.ReadBlock(rel, blk, page); err != nil {
			return err
		}
		if !storage.IsNew(page) {
			if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
				return nil // already applied
			}
		} else {
			if err := storage.InitPage(page); err != nil {
				return err
			}
		}
	case blk == nblocks:
		// Page doesn't exist yet — Extend with an InitPage'd
		// blank, then we'll add the tuple.
		if err := storage.InitPage(page); err != nil {
			return err
		}
		got, err := mgr.Extend(rel, page)
		if err != nil {
			return err
		}
		if got != blk {
			return fmt.Errorf("wal: heap-insert extend returned block %d, want %d", got, blk)
		}
	default:
		return fmt.Errorf("wal: heap-insert replay gap block=%d nblocks=%d", blk, nblocks)
	}

	// Place the tuple at the recorded slot.
	tup, err := storage.ParseHeapTuple(tuple)
	if err != nil {
		return fmt.Errorf("wal: heap-insert decode tuple: %w", err)
	}
	got, err := storage.PageAddHeapTuple(page, tup)
	if err != nil {
		return fmt.Errorf("wal: heap-insert apply: %w", err)
	}
	if got != lineSlot {
		// Slot mismatch is a sign of replay drift — earlier records
		// produced a different layout than original. v0 doesn't
		// support out-of-order slot assignment, so this is a hard
		// error rather than a silent fix-up.
		return fmt.Errorf("wal: heap-insert replay slot drift: got %d, want %d (block %d)", got, lineSlot, blk)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayHeapHotUpdate applies one atomic HOT-update record (M0046-0001).
// The page must already exist (the old tuple is already on it). Idempotent
// via pd_lsn. Replay:
//  1. Insert the new tuple (which carries HeapOnlyTuple in infomask).
//  2. Stamp the old slot: xmax + HeapHotUpdated + CTID = (blk, newSlot).
func replayHeapHotUpdate(mgr *storage.Manager, r Record) error {
	rel, blk, oldSlot, xmax, tupleBytes, err := DecodeHeapHotUpdate(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-hot-update replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-hot-update replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	tup, err := storage.ParseHeapTuple(tupleBytes)
	if err != nil {
		return fmt.Errorf("wal: heap-hot-update decode new tuple: %w", err)
	}
	newSlot, err := storage.PageAddHeapTuple(page, tup)
	if err != nil {
		return fmt.Errorf("wal: heap-hot-update insert new tuple: %w", err)
	}
	if err := storage.PageStampHotOldTuple(page, oldSlot, xmax, blk, newSlot); err != nil {
		return fmt.Errorf("wal: heap-hot-update stamp old tuple: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayHeapPruneOpt applies one opportunistic page-pruning record
// (M0046-0002). Applies redirect pairs (ItemIDRedirect) then marks unused
// slots via VacuumHeapPageBySlots. Idempotent via pd_lsn.
func replayHeapPruneOpt(mgr *storage.Manager, r Record) error {
	rel, blk, redirects, unused, err := DecodeHeapPruneOpt(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-prune-opt replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-prune-opt replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	// Apply redirect line pointer conversions first.
	for _, redir := range redirects {
		if err := storage.PageSetItemIDRedirect(page, redir[0], redir[1]); err != nil {
			return fmt.Errorf("wal: heap-prune-opt redirect slot=%d→%d: %w", redir[0], redir[1], err)
		}
	}
	// Compact the page: mark unused slots and repack live tuples.
	if _, err := storage.VacuumHeapPageBySlots(page, unused); err != nil {
		return fmt.Errorf("wal: heap-prune-opt vacuum: %w", err)
	}
	storage.MustHeader(page).SetPruneXID(0)
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayBtreeSplit applies the left then right page images carried
// by an atomic split record. The right page is applied via Extend
// when the relation is one block short of containing it (the
// freshly-allocated case at original record time) and via
// WriteBlock when the file is already long enough (replay re-run
// or the right page somehow already on disk). Apply order is
// left → right so a reader following left's right-link from the
// post-replay state always finds a real right page on disk.
func replayBtreeSplit(mgr *storage.Manager, payload []byte) error {
	rel, leftBlk, rightBlk, leftPage, rightPage, err := DecodeBtreeSplit(payload)
	if err != nil {
		return err
	}
	if err := writeBlockOrExtend(mgr, rel, leftBlk, leftPage); err != nil {
		return fmt.Errorf("apply left block %d: %w", leftBlk, err)
	}
	if err := writeBlockOrExtend(mgr, rel, rightBlk, rightPage); err != nil {
		return fmt.Errorf("apply right block %d: %w", rightBlk, err)
	}
	return nil
}

// writeBlockOrExtend installs `page` at the given block number,
// extending the relation if the block is exactly one past the end.
// It is the shared kernel for replayPageImage and the per-side
// apply in replayBtreeSplit.
func writeBlockOrExtend(mgr *storage.Manager, rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) error {
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	switch {
	case blk < nblocks:
		return mgr.WriteBlock(rel, blk, page)
	case blk == nblocks:
		got, err := mgr.Extend(rel, page)
		if err != nil {
			return err
		}
		if got != blk {
			return fmt.Errorf("wal: extend returned block %d, want %d", got, blk)
		}
		return nil
	default:
		return fmt.Errorf("wal: replay gap block=%d nblocks=%d", blk, nblocks)
	}
}

func replayPageImage(mgr *storage.Manager, payload []byte) error {
	rel, blk, page, err := DecodePageImage(payload)
	if err != nil {
		return err
	}
	return writeBlockOrExtend(mgr, rel, blk, page)
}

// replayStart returns the index of the LAST checkpoint record in
// records plus its EndLSN. Crash recovery should replay records
// starting from this index: everything before the checkpoint was
// already flushed to disk by the checkpoint operation.
//
// If no checkpoint is found, returns (0, 0) — replay all records
// from the beginning (correct for fresh clusters or early startup).
func replayStart(records []Record) (int, uint64) {
	startIdx := 0
	var checkpointLSN uint64
	for i, r := range records {
		if len(r.Payload) == 0 {
			continue
		}
		if r.Payload[0] == RecordKindCheckpoint {
			startIdx = i // start FROM this checkpoint (inclusive)
			checkpointLSN = r.EndLSN
		}
	}
	return startIdx, checkpointLSN
}

// DiscoverLastCheckpointLSN scans the WAL directory for the most
// recent checkpoint record and returns its EndLSN. This is needed
// for M0045-0002's startup replay: begin replay from the last
// checkpoint so post-checkpoint dirty pages are recovered without
// re-reading the entire WAL history.
//
// Because WAL retention removes pre-checkpoint segments, the scan
// must tolerate a non-zero first segment (M0045-0001). ReadAll already
// starts from the first retained segment after the readStream fix.
//
// Returns (0, nil) for a fresh cluster (no WAL segments present).
// Returns an error if WAL segments exist but no checkpoint is found —
// this indicates an unrecoverable cluster state that requires
// re-initialization.
func DiscoverLastCheckpointLSN(walDir string, segmentSize int64) (uint64, error) {
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}
	records, err := ReadAll(walDir, segmentSize)
	if err != nil {
		return 0, fmt.Errorf("wal: discover checkpoint: %w", err)
	}
	if len(records) == 0 {
		return 0, nil // fresh cluster or empty WAL directory
	}
	// Scan for the LAST checkpoint record in the retained range.
	var lastLSN uint64
	for _, r := range records {
		if len(r.Payload) > 0 && r.Payload[0] == RecordKindCheckpoint {
			lastLSN = r.EndLSN
		}
	}
	if lastLSN == 0 {
		return 0, fmt.Errorf("wal: no checkpoint record found in %s; "+
			"the cluster may need to be re-initialized with 'goopg init'", walDir)
	}
	return lastLSN, nil
}

// replaySmgrCreate ensures the relation file identified by the record has at
// least one initialised block. Idempotent: if the file already has blocks,
// this is a no-op. Mirrors XLOG_SMGR_CREATE redo semantics.
func replaySmgrCreate(mgr *storage.Manager, payload []byte) error {
	rel, err := DecodeSmgrCreate(payload)
	if err != nil {
		return err
	}
	n, err := mgr.NBlocks(rel)
	if err != nil {
		return fmt.Errorf("wal: smgr-create replay NBlocks: %w", err)
	}
	if n > 0 {
		return nil // already exists — idempotent
	}
	page := make([]byte, storage.BlockSize)
	if initErr := storage.InitPage(storage.Page(page)); initErr != nil {
		return initErr
	}
	_, err = mgr.Extend(rel, page)
	return err
}

// replaySmgrTruncate truncates the relfile to 0 blocks. Idempotent: if the
// file is already empty (0 blocks), this is a no-op. Mirrors
// XLOG_SMGR_TRUNCATE redo semantics.
func replaySmgrTruncate(mgr *storage.Manager, payload []byte) error {
	rel, err := DecodeSmgrTruncate(payload)
	if err != nil {
		return err
	}
	n, err := mgr.NBlocks(rel)
	if err != nil {
		return fmt.Errorf("wal: smgr-truncate replay NBlocks: %w", err)
	}
	if n == 0 {
		return nil // already empty — idempotent
	}
	return mgr.TruncateRelation(rel)
}
