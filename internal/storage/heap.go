package storage

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	itemIDSize = 4

	// SizeOfHeapTupleHeaderData mirrors the fixed-size header fields in
	// postgres/src/include/access/htup_details.h.
	SizeOfHeapTupleHeaderData = 23

	// DefaultHeapTupleHoff is the aligned tuple data offset used by v0
	// tuples without null bitmap/OID.
	DefaultHeapTupleHoff = 24

	// MaxHeapTuplesPerPage is the upper bound on the number of line pointers
	// a heap page can hold, mirroring PG's macro of the same name
	// (postgres/src/include/access/htup_details.h:629): every tuple must be
	// maxaligned and carry one line pointer. 291 for an 8 KiB page.
	//
	// It is also the only correct bound for a HOT/CTID chain walk: a chain
	// visits distinct slots on one page, so a walk that has taken more than
	// MaxHeapTuplesPerPage steps has provably hit a cycle. Chain walkers used
	// an arbitrary 64 until M0131-S32, which silently made the 65th version of
	// a row unreachable through the index (docs/design/0131-0025).
	// MAXALIGN(SizeofHeapTupleHeader) is spelled out arithmetically because Go
	// const initialisers cannot call a helper: (23+7)/8*8 = 24.
	MaxHeapTuplesPerPage = (BlockSize - SizeOfPageHeaderData) /
		((SizeOfHeapTupleHeaderData+7)/8*8 + itemIDSize)
)

var (
	ErrNoSpaceInPage   = errors.New("storage: not enough free space in page")
	ErrInvalidSlot     = errors.New("storage: invalid tuple slot")
	ErrUnsupportedItem = errors.New("storage: unsupported line pointer state")
	ErrCorruptTuple    = errors.New("storage: corrupt heap tuple")
)

// TransactionID is a 32-bit xid. Mirrors PostgreSQL's TransactionId.
type TransactionID uint32

const (
	// InvalidTransactionID is xid 0.
	InvalidTransactionID TransactionID = 0

	// FrozenTransactionID is the permanent-visibility XID (= 2). Any tuple
	// whose xmin is rewritten to FrozenTransactionID by VACUUM FREEZE is
	// visible to all past, present, and future snapshots — it is exempt from
	// normal MVCC visibility rules. Mirrors PostgreSQL's FrozenTransactionId.
	// Because FrozenTransactionID(2) < FirstNormalTransactionID(3) ≤ every
	// snapshot's Xmin, the existing SeesCommittedXID check already returns
	// true for frozen tuples without any code change to TupleVisible.
	FrozenTransactionID TransactionID = 2
)

// XIDPrecedes reports whether a is logically before b in modulo-2^32 XID space
// (the half of the circle older than b). Mirrors PostgreSQL's
// TransactionIdPrecedes (`postgres/src/backend/access/transam/transam.c`):
//
//	int32 diff = (int32) (id1 - id2);
//	return diff < 0;
//
// Plain unsigned `<` is WRONG for XID horizon comparisons because the XID space
// wraps at 2^32: after wraparound a freshly assigned XID has a *smaller* numeric
// value than an older one, so `<` would treat the newer XID as older and pick
// the wrong VACUUM/CLOG-truncation horizon. The signed-difference trick orders
// XIDs by their position on the modular circle relative to b, which is correct
// for any two XIDs less than 2^31 apart — the invariant PG maintains via the
// wraparound-prevention (anti-wraparound autovacuum) machinery.
//
// XID 0 (InvalidTransactionID) and the other low sentinels are NOT special-
// cased here; callers must screen them out (e.g. via TransactionIdIsNormal-
// style `xid >= FirstNormalTransactionID` checks) before comparing, exactly as
// upstream does. This is the single source of truth for modular XID ordering;
// internal/mvcc's txnPrecedes delegates to it so the two cannot drift.
func XIDPrecedes(a, b TransactionID) bool {
	return int32(a-b) < 0
}

// HeapTupleHeader infomask flag bits. Values mirror upstream's
// `postgres/src/include/access/htup_details.h` so future
// pg_waldump compat / on-disk format work doesn't have to
// translate. v0 only consumes the lock-related bits today
// (M0021 follow-up — tuple-level pessimistic locking step 1):
//
//   - HeapXmaxInvalid: xmax is not a delete (set when xmax is
//     known invalid; complement of HeapXmaxCommitted).
//   - HeapXmaxCommitted: xmax committed (cached hint).
//   - HeapXmaxLockOnly: xmax represents a row lock, not a
//     delete. The MVCC visibility helper recognises this bit
//     and treats a lock-only xmax as "no deleter" — readers
//     still see the tuple as live.
//   - HeapXmaxKeyShrLock / HeapXmaxExclLock: row-lock strength
//     bits (KEY SHARE / EXCLUSIVE). FOR UPDATE uses ExclLock;
//     FOR SHARE will use the SHR composite (KeyShrLock |
//     ExclLock) when MultiXact-aware multi-holder support
//     lands. v0 only emits ExclLock.
const (
	HeapXminCommitted uint16 = 0x0100 // xmin is a committed transaction (cached hint)
	HeapXminInvalid   uint16 = 0x0200 // xmin is invalid / rolled-back (cached hint)
	HeapXmaxInvalid   uint16 = 0x0800
	HeapXmaxCommitted uint16 = 0x0400
	// HeapXmaxIsMulti indicates xmax is a MultiXactId (a *set* of
	// transactions resolved via the multixact member store) rather than a
	// single TransactionID. When set, readers must resolve xmax through
	// internal/multixact's Store.Members to learn the lock holders and
	// (at most one) updater. Mirrors PostgreSQL's HEAP_XMAX_IS_MULTI
	// (0x1000, htup_details.h:209).
	HeapXmaxIsMulti    uint16 = 0x1000
	HeapXmaxLockOnly   uint16 = 0x0080
	HeapXmaxKeyShrLock uint16 = 0x0010
	HeapXmaxExclLock   uint16 = 0x0040
	// HeapXmaxShrLock is the composite "FOR SHARE" lock — both
	// the KEY SHARE bit and the EXCLUSIVE bit set. Mirrors
	// upstream's HEAP_XMAX_SHR_LOCK macro.
	HeapXmaxShrLock = HeapXmaxKeyShrLock | HeapXmaxExclLock

	// HeapXmaxLockMask covers every row-lock-strength bit;
	// `(infomask & HeapXmaxLockMask) != 0` is the
	// "this is a row-lock holding xmax" predicate.
	HeapXmaxLockMask = HeapXmaxKeyShrLock | HeapXmaxExclLock

	// HeapHotUpdated indicates this tuple was HOT-updated: xmax is
	// stamped and a new version was written to the same heap page.
	// The CTID field points to the successor version's slot. Callers
	// following an index pointer must walk the chain (same page, follow
	// CTID.Offset) until they find a tuple without this bit set.
	// Mirrors PostgreSQL's HEAP_HOT_UPDATED (0x4000).
	//
	// M0131-S11: this bit lives in **t_infomask2**, not t_infomask —
	// HeapTupleHeaderSetHotUpdated writes `tup->t_infomask2 |=
	// HEAP_HOT_UPDATED` (htup_details.h:550) and the reader at :542 tests
	// the same field. goopg kept it in t_infomask until 2026-08-11, which
	// made every HOT chain mutually unreadable across engines: goopg read
	// zero rows from a PG-authored chain, and a PG reading a goopg page saw
	// 0x4000/0x8000 in t_infomask as HEAP_MOVED_OFF/HEAP_MOVED_IN and took
	// the pre-9.0 t_xvac visibility path (heapam_visibility.c:183/:202).
	// Access the bits through IsHotUpdated/SetHotUpdated/IsHeapOnly/
	// SetHeapOnly below rather than naming the field, so the reader and
	// writer siblings can never drift apart again.
	HeapHotUpdated uint16 = 0x4000
	// HeapOnlyTuple indicates this tuple is a HOT-only version: it was
	// inserted as the successor in a HOT update chain and has no direct
	// index entry. Mirrors PostgreSQL's HEAP_ONLY_TUPLE (0x8000), and like
	// HeapHotUpdated it lives in t_infomask2 (htup_details.h:568).
	HeapOnlyTuple uint16 = 0x8000

	// HEAP_HASNULL indicates the tuple contains NULL values (null bitmap
	// present in t_hoff). Mirrors PG's HEAP_HASNULL (0x0001).
	HeapHasNull uint16 = 0x0001
	// HEAP_HASVARWIDTH indicates the tuple has variable-width columns.
	// Mirrors PG's HEAP_HASVARWIDTH (0x0002).
	HeapHasVarWidth uint16 = 0x0002
	// HEAP_HASEXTERNAL indicates the tuple has external stored attribute(s)
	// (a VARATT_EXTERNAL / TOAST pointer varlena). PG's heap_fill_tuple stamps
	// this bit so nocachegetattr and heap_deform_tuple skip the toast-pointer
	// column correctly when computing attribute offsets. Mirrors PG's
	// HEAP_HASEXTERNAL (0x0004).
	HeapHasExternal uint16 = 0x0004
	// HeapKeysUpdated is set in t_infomask2 when an UPDATE changes an indexed
	// (key) column. FOR KEY SHARE only conflicts with xmax that has this bit
	// set. Mirrors PostgreSQL's HEAP_KEYS_UPDATED (0x2000).
	HeapKeysUpdated uint16 = 0x2000
	// HEAP_NATTS_MASK is the bit mask for number of attributes in
	// t_infomask2 (bits 0-10). Mirrors PG's HEAP_NATTS_MASK (0x07FF).
	HeapNattsMask uint16 = 0x07FF

	// HeapComboCID indicates that t_field3 (the Xvac/t_cid union at bytes
	// 8-11) holds a combo CID rather than a plain cmin/cmax. When set,
	// the raw t_cid value must be resolved through the backend's
	// ComboCIDStore to recover the real cmin and cmax. Mirrors
	// PostgreSQL's HEAP_COMBOCID (0x0020, htup_details.h:197).
	HeapComboCID uint16 = 0x0020

	// HeapMovedOff / HeapMovedIn are the pre-9.0 VACUUM FULL relocation bits
	// (htup_details.h:211-217). goopg never sets them — no goopg tuple can
	// have come from a pre-9.0 cluster — but they are named here because
	// upstream's redo routines clear them: heap_xlog_lock turns off
	// HEAP_XMAX_BITS *and* HEAP_MOVED before restamping xmax, since t_field3
	// is the t_xvac/t_cid union and a MOVED tuple's field3 must not be read
	// back as a cmax (HeapTupleHeaderSetCmax even asserts on it,
	// htup_details.h:436). A goopg cluster hosting a PG cold-start
	// (M0131) can be handed such a tuple by a pg_upgrade'd cluster's WAL, so
	// the clear is reproduced rather than assumed away.
	HeapMovedOff uint16 = 0x4000
	HeapMovedIn  uint16 = 0x8000
	HeapMoved           = HeapMovedOff | HeapMovedIn

	// HeapXmaxBits is upstream's HEAP_XMAX_BITS (htup_details.h:284): every
	// infomask bit that describes the CURRENT xmax and therefore has to be
	// turned off before a different xmax is stamped in its place.
	HeapXmaxBits = HeapXmaxCommitted | HeapXmaxInvalid | HeapXmaxIsMulti | HeapXmaxLockMask | HeapXmaxLockOnly
)

// isHeapXmaxLockedOnlyPG is upstream's HEAP_XMAX_IS_LOCKED_ONLY in full
// (htup_details.h:230-234), including the pre-9.3 clause the exported
// IsHeapTupleLockOnly deliberately omits: a bare HEAP_XMAX_EXCL_LOCK with
// neither HEAP_XMAX_LOCK_ONLY nor HEAP_XMAX_IS_MULTI also means "locked, not
// deleted".
//
// The narrowed predicate is correct for tuples goopg itself wrote (its lock
// stamps always set HEAP_XMAX_LOCK_ONLY), but redo of a real-PG record must
// classify a tuple PG wrote, so the redo path uses the full expression. Keep
// the two in sync: this one is the reference, IsHeapTupleLockOnly is the
// goopg-tuple shortcut.
func isHeapXmaxLockedOnlyPG(infomask uint16) bool {
	return infomask&HeapXmaxLockOnly != 0 ||
		infomask&(HeapXmaxIsMulti|HeapXmaxLockMask) == HeapXmaxExclLock
}

// IsHeapTupleLockOnly reports whether `infomask` indicates the
// tuple's xmax represents a row lock (not a delete). Mirrors
// upstream's HEAP_XMAX_IS_LOCKED_ONLY macro.
func IsHeapTupleLockOnly(infomask uint16) bool {
	return infomask&HeapXmaxLockOnly != 0
}

// IsHeapTupleXmaxMulti reports whether the tuple's xmax is a MultiXactId
// (HEAP_XMAX_IS_MULTI set) rather than a single TransactionID. When true,
// xmax must be resolved through the multixact member store (Store.Members)
// to enumerate the lock holders and the at-most-one updater; a single-xid
// reader (e.g. plain WaitForXID / TransactionIdIsCurrent) must not be applied
// to it directly. Mirrors testing PostgreSQL's HEAP_XMAX_IS_MULTI bit.
//
// Note: HEAP_XMAX_LOCK_ONLY is the orthogonal "this xmax is a lock, not an
// update" predicate (IsHeapTupleLockOnly). The multixact encoder stamps
// HEAP_XMAX_LOCK_ONLY whenever a multi has no updater, so for goopg's
// from-initdb tuples (no pg_upgrade legacy) IsHeapTupleLockOnly alone
// correctly classifies multi xmax as locked-only vs. updated without needing
// upstream's pre-9.3 EXCL_LOCK fallback clause.
func IsHeapTupleXmaxMulti(infomask uint16) bool {
	return infomask&HeapXmaxIsMulti != 0
}

// IsHeapComboCID reports whether the tuple's t_field3 holds a combo CID
// (HEAP_COMBOCID set in infomask). When true, the raw t_cid must be resolved
// through the backend's ComboCIDStore to recover the real cmin and cmax.
// Mirrors the HEAP_COMBOCID bit test in PostgreSQL.
func IsHeapComboCID(infomask uint16) bool {
	return infomask&HeapComboCID != 0
}

// HeapTupleHeader cmin / cmax getters and setters.
//
// The goopg HeapTupleHeader carries a 4-byte field at offset 8-11 (Xvac)
// that mirrors PostgreSQL's t_cid/t_xvac union (t_field3 in htup_details.h).
// When HEAP_COMBOCID is clear, Xvac directly holds the inserting CommandId
// (cmin) when interpreting as cmin, or both cmin and cmax are equal and the
// field holds that shared value. When HEAP_COMBOCID is set, the raw value is a
// combo CID — a synthetic identifier that the ComboCIDStore resolves to the
// real (cmin, cmax) pair.
//
// SetCmin stamps the inserting command id and clears HEAP_COMBOCID. Mirrors
// PostgreSQL's HeapTupleHeaderSetCmin (htup_details.h).
func (h *HeapTupleHeader) SetCmin(cid CommandId) {
	h.Xvac = TransactionID(cid)
	h.Infomask &^= HeapComboCID
}

// SetCmax stamps the deleting command id. When isCombo is true, HEAP_COMBOCID
// is set (the caller resolved (cmin, cmax) to a combo CID and passed it).
// When isCombo is false, the raw cid is the plain cmax and HEAP_COMBOCID is
// clear. Mirrors PostgreSQL's HeapTupleHeaderSetCmax (htup_details.h).
func (h *HeapTupleHeader) SetCmax(cid CommandId, isCombo bool) {
	h.Xvac = TransactionID(cid)
	if isCombo {
		h.Infomask |= HeapComboCID
	} else {
		h.Infomask &^= HeapComboCID
	}
}

// GetRawCommandId returns the raw 4-byte t_field3 value, irrespective of
// whether it represents a plain CommandId or a combo CID. Callers that need
// the real cmin/cmax must pass this through ComboCIDStore resolution when
// IsHeapComboCID(h.Infomask) is true. Mirrors PostgreSQL's
// HeapTupleHeaderGetRawCommandId (htup_details.h).
func (h *HeapTupleHeader) GetRawCommandId() CommandId {
	return CommandId(h.Xvac)
}

// ResolveMultiUpdater, when non-nil, resolves an updater-bearing multixact xmax
// to its updater transaction id. When a tuple's xmax has HEAP_XMAX_IS_MULTI set
// and HEAP_XMAX_LOCK_ONLY clear, the raw xmax field holds a MultiXactId — an
// index into the member store — not a transaction id; comparing it against an
// xid horizon (oldestXmin / freezeBelow) or treating it as a deleter would be a
// category error. The vacuum/freeze/prune read paths in this package must funnel
// such an xmax through this hook before reasoning about it as an xid.
//
// The storage package cannot import internal/multixact (multixact imports
// storage, so the dependency would cycle); instead the process owner wires this
// callback from the process-shared multixact.Store at startup (cmd/goopg/main.go),
// mirroring how mvcc.TupleVisible / executor.isConcurrentlyUpdated take the Store
// directly. The three return values mirror Store.Members + GetUpdateXid:
//
//   - resolved == false: the MultiXactId was not found in the member store, or
//     no resolver is installed. The caller cannot interpret xmax and must apply
//     its own conservative default (never freeze/prune a tuple it cannot judge).
//   - resolved == true, hasUpdater == false: the multi holds only lockers (no
//     update/delete member) — the tuple is still live.
//   - resolved == true, hasUpdater == true: `updater` is the updater member's
//     transaction id; reason about it exactly as a plain xmax xid.
//
// Lock-only multis (all lockers) carry HEAP_XMAX_LOCK_ONLY (see multixact.HintBits),
// so callers gate on IsHeapTupleXmaxMulti && !IsHeapTupleLockOnly before invoking
// this — an all-locker multi never reaches the hook.
var ResolveMultiUpdater func(xmax TransactionID) (updater TransactionID, hasUpdater bool, resolved bool)

// XidCommitted, when non-nil, reports whether xid COMMITTED (false for
// aborted, unknown, or in-progress). Installed by initdb.Open from the
// CLOG (same injection pattern as ResolveMultiUpdater — storage cannot
// import mvcc). Consulted by TupleDeadToAll (C3-S3 blocker fix B): an
// ABORTED deleter's xmax stamp survives on the tuple and the oldestXmin
// horizon advances past the aborted xid freely, so without the commit
// check prune/VACUUM/the index kill oracle could reclaim a LIVE row (PG's
// HeapTupleSatisfiesVacuum checks TransactionIdDidCommit). nil (tests,
// bootstrap) => conservatively NOT dead.
var XidCommitted func(xid TransactionID) bool

// MovedPartitionsOffsetNumber is the special t_ctid.ip_posid value
// PostgreSQL stamps on a tuple whose UPDATE moved the row to a
// different partition (the old version's CTID can't point to the new
// version because it lives in another relation entirely). Combined
// with InvalidBlockNumber it marks a "moved to another partition"
// tombstone; EPQ retries that follow xmax to this sentinel must raise
// `tuple to be locked was already moved to another partition due to
// concurrent update`. Mirrors upstream's `MovedPartitionsOffsetNumber`
// (`postgres/src/include/storage/itemptr.h`).
const MovedPartitionsOffsetNumber uint16 = 0xFFFD

// ItemPointer identifies a tuple location (block, line-pointer slot).
type ItemPointer struct {
	Block  BlockNumber
	Offset uint16
}

// IsMovedToAnotherPartition reports whether `ctid` carries the
// upstream "moved to another partition" sentinel (block ==
// InvalidBlockNumber, offset == MovedPartitionsOffsetNumber).
func IsMovedToAnotherPartition(ctid ItemPointer) bool {
	return ctid.Block == InvalidBlockNumber && ctid.Offset == MovedPartitionsOffsetNumber
}

// HeapTupleHeader is the fixed tuple header subset used in milestone 5.
// It carries xmin/xmax visibility metadata and ctid linkage.
type HeapTupleHeader struct {
	Xmin      TransactionID
	Xmax      TransactionID
	Xvac      TransactionID
	CTID      ItemPointer
	Infomask  uint16
	Infomask2 uint16
	Hoff      uint8
}

// HeapTuple is one on-page tuple body.
//
// Bitmap is the PG null bitmap (bit=1 means NOT NULL, matching PG's
// heap_fill_tuple), nil when the tuple has no nulls. Data is the column
// data area only — the bitmap is stored separately so MarshalBinary can
// place it at the canonical PG location (right after the fixed header,
// before the t_hoff-aligned data region).
type HeapTuple struct {
	Header HeapTupleHeader
	Bitmap []byte
	Data   []byte
}

// NewHeapTuple constructs a tuple with v0 defaults.
// SetNatts sets t_infomask2 to the given number of attributes,
// mirroring PG's HeapTupleHeaderSetNatts. Caller must also set
// HeapHasNull in infomask if the null bitmap is present.
func (h *HeapTupleHeader) SetNatts(natts int) {
	h.Infomask2 = (h.Infomask2 &^ HeapNattsMask) | (uint16(natts) & HeapNattsMask)
}

// IsHotUpdated mirrors HeapTupleHeaderIsHotUpdated
// (postgres/src/include/access/htup_details.h:542) minus the xmin/xmax hint
// screening, which callers that need it apply themselves (internal/amcheck).
// The bit is read from t_infomask2 — see HeapHotUpdated for why.
func (h *HeapTupleHeader) IsHotUpdated() bool { return h.Infomask2&HeapHotUpdated != 0 }

// SetHotUpdated mirrors HeapTupleHeaderSetHotUpdated (htup_details.h:550).
func (h *HeapTupleHeader) SetHotUpdated() { h.Infomask2 |= HeapHotUpdated }

// IsHeapOnly mirrors HeapTupleHeaderIsHeapOnly (htup_details.h:562).
func (h *HeapTupleHeader) IsHeapOnly() bool { return h.Infomask2&HeapOnlyTuple != 0 }

// SetHeapOnly mirrors HeapTupleHeaderSetHeapOnly (htup_details.h:568).
func (h *HeapTupleHeader) SetHeapOnly() { h.Infomask2 |= HeapOnlyTuple }

func NewHeapTuple(xmin, xmax TransactionID, data []byte) HeapTuple {
	out := make([]byte, len(data))
	copy(out, data)
	return HeapTuple{
		Header: HeapTupleHeader{
			Xmin: xmin,
			Xmax: xmax,
			Xvac: InvalidTransactionID,
			CTID: ItemPointer{Block: InvalidBlockNumber, Offset: 0},
			Hoff: DefaultHeapTupleHoff,
		},
		Data: out,
	}
}

// maxAlign8 rounds n up to the nearest multiple of 8 (PG's MAXALIGN on
// 64-bit platforms).
func maxAlign8(n int) int {
	return (n + 7) &^ 7
}

// NewHeapTupleWithNulls constructs a tuple whose payload includes a PG
// null bitmap (bit=1 means NOT NULL, matching PG's heap_fill_tuple).
// data is the column-data area only — without a bitmap and without
// header-to-data alignment padding. The constructor stamps HEAP_HASNULL
// in infomask and computes t_hoff = MAXALIGN(SizeofHeapTupleHeader +
// len(bitmap)) so PG's heap_deform_tuple finds the column data at the
// expected offset.
func NewHeapTupleWithNulls(xmin, xmax TransactionID, bitmap, data []byte) HeapTuple {
	hoff := maxAlign8(SizeOfHeapTupleHeaderData + len(bitmap))
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	bitmapCopy := make([]byte, len(bitmap))
	copy(bitmapCopy, bitmap)
	return HeapTuple{
		Header: HeapTupleHeader{
			Xmin:     xmin,
			Xmax:     xmax,
			Xvac:     InvalidTransactionID,
			CTID:     ItemPointer{Block: InvalidBlockNumber, Offset: 0},
			Infomask: HeapHasNull,
			Hoff:     uint8(hoff),
		},
		Bitmap: bitmapCopy,
		Data:   dataCopy,
	}
}

// MarshalBinary encodes the tuple into the on-page layout. When Bitmap
// is non-nil, it is written immediately after the fixed header (byte
// SizeOfHeapTupleHeaderData..SizeOfHeapTupleHeaderData+len(Bitmap));
// the gap between the bitmap and Data (starting at byte t_hoff) is
// alignment padding required by PG.
func (t HeapTuple) MarshalBinary() ([]byte, error) {
	hoff := int(t.Header.Hoff)
	if hoff == 0 {
		hoff = DefaultHeapTupleHoff
	}
	if hoff < SizeOfHeapTupleHeaderData || hoff > 255 {
		return nil, fmt.Errorf("invalid t_hoff=%d", hoff)
	}
	if len(t.Bitmap) > 0 && SizeOfHeapTupleHeaderData+len(t.Bitmap) > hoff {
		return nil, fmt.Errorf("null bitmap of %d bytes does not fit under t_hoff=%d", len(t.Bitmap), hoff)
	}
	out := make([]byte, hoff+len(t.Data))
	binary.LittleEndian.PutUint32(out[0:4], uint32(t.Header.Xmin))
	binary.LittleEndian.PutUint32(out[4:8], uint32(t.Header.Xmax))
	binary.LittleEndian.PutUint32(out[8:12], uint32(t.Header.Xvac))
	binary.LittleEndian.PutUint32(out[12:16], uint32(t.Header.CTID.Block))
	binary.LittleEndian.PutUint16(out[16:18], t.Header.CTID.Offset)
	binary.LittleEndian.PutUint16(out[18:20], t.Header.Infomask2)
	binary.LittleEndian.PutUint16(out[20:22], t.Header.Infomask)
	out[22] = byte(hoff)
	if len(t.Bitmap) > 0 {
		copy(out[SizeOfHeapTupleHeaderData:SizeOfHeapTupleHeaderData+len(t.Bitmap)], t.Bitmap)
	}
	copy(out[hoff:], t.Data)
	return out, nil
}

// ParseHeapTuple decodes one on-page tuple payload, copying the
// data section so the returned tuple is independent of `raw`.
func ParseHeapTuple(raw []byte) (HeapTuple, error) {
	t, err := parseHeapTupleAlias(raw)
	if err != nil {
		return HeapTuple{}, err
	}
	t.Data = append([]byte(nil), t.Data...)
	if len(t.Bitmap) > 0 {
		t.Bitmap = append([]byte(nil), t.Bitmap...)
	}
	return t, nil
}

// parseHeapTupleAlias is the no-copy decode used by
// PageGetHeapTupleNoCopy (M0092-0006). The returned tuple's Data
// field aliases `raw`; caller must hold the page pin AND a content
// RLock for the lifetime of the returned tuple.
func parseHeapTupleAlias(raw []byte) (HeapTuple, error) {
	if len(raw) < SizeOfHeapTupleHeaderData {
		return HeapTuple{}, fmt.Errorf("%w: raw len=%d", ErrCorruptTuple, len(raw))
	}
	hoff := int(raw[22])
	if hoff < SizeOfHeapTupleHeaderData || hoff > len(raw) {
		return HeapTuple{}, fmt.Errorf("%w: invalid t_hoff=%d len=%d", ErrCorruptTuple, hoff, len(raw))
	}
	infomask := binary.LittleEndian.Uint16(raw[20:22])
	infomask2 := binary.LittleEndian.Uint16(raw[18:20])
	var bitmap []byte
	if infomask&HeapHasNull != 0 {
		natts := int(infomask2 & HeapNattsMask)
		bmLen := (natts + 7) / 8
		if SizeOfHeapTupleHeaderData+bmLen > hoff {
			return HeapTuple{}, fmt.Errorf("%w: null bitmap of %d bytes overruns t_hoff=%d", ErrCorruptTuple, bmLen, hoff)
		}
		bitmap = raw[SizeOfHeapTupleHeaderData : SizeOfHeapTupleHeaderData+bmLen]
	}
	return HeapTuple{
		Header: HeapTupleHeader{
			Xmin:      TransactionID(binary.LittleEndian.Uint32(raw[0:4])),
			Xmax:      TransactionID(binary.LittleEndian.Uint32(raw[4:8])),
			Xvac:      TransactionID(binary.LittleEndian.Uint32(raw[8:12])),
			CTID:      ItemPointer{Block: BlockNumber(binary.LittleEndian.Uint32(raw[12:16])), Offset: binary.LittleEndian.Uint16(raw[16:18])},
			Infomask2: infomask2,
			Infomask:  infomask,
			Hoff:      uint8(hoff),
		},
		Bitmap: bitmap,
		Data:   raw[hoff:],
	}, nil
}

// ItemIDFlags mirrors PostgreSQL ItemId state bits.
type ItemIDFlags uint8

const (
	ItemIDUnused   ItemIDFlags = 0
	ItemIDNormal   ItemIDFlags = 1
	ItemIDRedirect ItemIDFlags = 2
	ItemIDDead     ItemIDFlags = 3
)

// ItemID is one 4-byte line pointer.
type ItemID struct {
	Offset uint16
	Flags  ItemIDFlags
	Length uint16
}

func (i ItemID) pack() (uint32, error) {
	if i.Offset > 0x7FFF {
		return 0, fmt.Errorf("itemid offset out of range: %d", i.Offset)
	}
	if i.Length > 0x7FFF {
		return 0, fmt.Errorf("itemid length out of range: %d", i.Length)
	}
	if i.Flags > 0x3 {
		return 0, fmt.Errorf("itemid flags out of range: %d", i.Flags)
	}
	raw := uint32(i.Offset&0x7FFF) |
		(uint32(i.Flags&0x3) << 15) |
		(uint32(i.Length&0x7FFF) << 17)
	return raw, nil
}

func unpackItemID(raw uint32) ItemID {
	return ItemID{
		Offset: uint16(raw & 0x7FFF),
		Flags:  ItemIDFlags((raw >> 15) & 0x3),
		Length: uint16((raw >> 17) & 0x7FFF),
	}
}

// PageLinePointerCount returns the number of line pointers present.
func PageLinePointerCount(p Page) (int, error) {
	h, err := Header(p)
	if err != nil {
		return 0, err
	}
	lower := int(h.Lower())
	if lower < SizeOfPageHeaderData {
		return 0, fmt.Errorf("invalid pd_lower=%d", lower)
	}
	if (lower-SizeOfPageHeaderData)%itemIDSize != 0 {
		return 0, fmt.Errorf("invalid line pointer area size: lower=%d", lower)
	}
	return (lower - SizeOfPageHeaderData) / itemIDSize, nil
}

// PageAddHeapTuple appends a tuple to the page and returns the 1-based
// line-pointer slot number.
func PageAddHeapTuple(p Page, t HeapTuple) (uint16, error) {
	h, err := Header(p)
	if err != nil {
		return 0, err
	}
	raw, err := t.MarshalBinary()
	if err != nil {
		return 0, err
	}
	if len(raw) > 0x7FFF {
		return 0, fmt.Errorf("tuple too large for line pointer len=%d", len(raw))
	}
	lower := int(h.Lower())
	upper := int(h.Upper())
	// PG MAXALIGNs the tuple offset (PageAddItemExtended: alignedSize =
	// MAXALIGN(size); upper -= alignedSize). On a PG18 standby, a tuple
	// whose offset is not 8-byte aligned causes the backend's
	// heap_deform_tuple to dereference alignment-sensitive offsets at
	// the wrong base, segfaulting on the first SELECT. The line-pointer
	// Length still reports the actual tuple length so ParseHeapTuple
	// reads exactly the tuple bytes; the trailing 0..7 bytes are
	// padding (zero from InitPage). M0106-0010 batched-36.
	alignedSize := maxAlign8(len(raw))
	needed := itemIDSize + alignedSize
	if upper-lower < needed {
		return 0, ErrNoSpaceInPage
	}

	newUpper := upper - alignedSize
	copy(p[newUpper:newUpper+len(raw)], raw)

	count, err := PageLinePointerCount(p)
	if err != nil {
		return 0, err
	}
	item := ItemID{Offset: uint16(newUpper), Flags: ItemIDNormal, Length: uint16(len(raw))}
	if err := writeItemID(p, count, item); err != nil {
		return 0, err
	}
	h.SetLower(uint16(lower + itemIDSize))
	h.SetUpper(uint16(newUpper))
	return uint16(count + 1), nil
}

// PageGetHeapTuple reads the tuple stored in a 1-based line-pointer slot.
func PageGetHeapTuple(p Page, slot uint16) (HeapTuple, error) {
	if slot == 0 {
		return HeapTuple{}, ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return HeapTuple{}, err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return HeapTuple{}, ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return HeapTuple{}, err
	}
	if item.Flags != ItemIDNormal {
		return HeapTuple{}, fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	ln := int(item.Length)
	if off < 0 || ln < 0 || off+ln > len(p) {
		return HeapTuple{}, fmt.Errorf("%w: slot=%d off=%d len=%d", ErrCorruptTuple, slot, off, ln)
	}
	raw := append([]byte(nil), p[off:off+ln]...)
	return ParseHeapTuple(raw)
}

// PageGetHeapTupleNoCopy reads the tuple at the 1-based slot, with
// the returned tuple's Data field ALIASING the page bytes (zero
// copy). Caller MUST hold the page pin AND a content RLock for the
// lifetime of the returned tuple (so the page can't be written
// underneath us). Used by the indexScanOp hot read path
// (M0092-0006); other callers should keep using PageGetHeapTuple.
func PageGetHeapTupleNoCopy(p Page, slot uint16) (HeapTuple, error) {
	if slot == 0 {
		return HeapTuple{}, ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return HeapTuple{}, err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return HeapTuple{}, ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return HeapTuple{}, err
	}
	if item.Flags != ItemIDNormal {
		return HeapTuple{}, fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	ln := int(item.Length)
	if off < 0 || ln < 0 || off+ln > len(p) {
		return HeapTuple{}, fmt.Errorf("%w: slot=%d off=%d len=%d", ErrCorruptTuple, slot, off, ln)
	}
	return parseHeapTupleAlias(p[off : off+ln])
}

// PageAddItemRaw appends an arbitrary blob as a new item on the page,
// returning its 1-based slot number. This is the access-method-neutral
// counterpart of PageAddHeapTuple — index AMs (e.g. btree) carry their
// own per-AM payload formats and need a way to drop a pre-encoded item
// in without going through the heap-specific tuple parser.
//
// The caller is responsible for the contents; `raw` must be small
// enough that the line pointer's 15-bit length field can express it.
//
// Item bodies are tracked by pd_upper / pd_special exactly as for heap
// tuples, so a page may freely mix heap and index-shaped items as long
// as the access method consuming them knows what to expect — in
// practice, each relation file holds one or the other.
//
// pd_special bounds the tuple region: the new item is placed at
// pd_upper - len(raw); if that would land below pd_special's start the
// page is treated as full.
// PageRemoveHeapTuple marks the 1-based slot as LP_UNUSED, freeing the line
// pointer. If the slot is the last line pointer, pd_lower is decremented to
// reclaim the array entry, and pd_upper is raised back over the item body when
// that body is the topmost one — so removing the item that was just appended
// restores the page exactly (M0131-S30.5). For any other slot the tuple body
// bytes in the upper region become garbage; they are reclaimed by the next
// VACUUM / page compaction (VacuumHeapPageBySlots). Returns ErrInvalidSlot for
// out-of-range slot numbers and ErrUnsupportedItem if the slot is not
// LP_NORMAL.
//
// This is the inverse of PageAddHeapTuple for the orphan-cleanup path: when
// tryApplyHOTUpdate writes a new tuple to the page but the subsequent old-slot
// stamp fails (e.g. PagePruneOpt invalidated the old slot), the orphan
// HEAP_ONLY_TUPLE must be removed to prevent unbounded line-pointer growth.
func PageRemoveHeapTuple(p Page, slot uint16) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	// Zero the line pointer — mark slot LP_UNUSED so it is never read by
	// PageGetHeapTuple (which rejects non-LP_NORMAL slots).
	if err := writeItemID(p, idx, ItemID{Flags: ItemIDUnused}); err != nil {
		return err
	}
	// If this was the last entry, shrink the line pointer array.
	if idx == count-1 {
		h := MustHeader(p)
		h.SetLower(uint16(SizeOfPageHeaderData + idx*itemIDSize))
		// M0131-S30.5: also give pd_upper back when the removed item's body
		// is the topmost one, i.e. it sits exactly at pd_upper. Together with
		// the pd_lower shrink above that makes this call an EXACT undo of the
		// PageAddHeapTuple that placed it, which is what the orphan-cleanup
		// caller needs: the add emitted no WAL, so the page must return to
		// its last-logged state or replay rebuilds a page that disagrees with
		// the one the runtime flushed. Restoring only pd_lower left pd_upper
		// permanently lowered — an unlogged free-space loss that accumulates
		// on the page and is exactly the runtime/WAL divergence family
		// M0131-S30.3 is chasing. The vacated bytes fall back inside
		// [pd_lower, pd_upper), i.e. PG's FPI hole, so they are not part of
		// the page image either way (postgres/src/backend/access/transam/
		// xloginsert.c XLogRecordAssemble).
		if int(item.Offset) == int(h.Upper()) {
			h.SetUpper(uint16(int(item.Offset) + maxAlign8(int(item.Length))))
		}
	}
	// Tuple body is NOT zeroed — the bytes in [pd_upper, pd_special) are
	// garbage; VacuumHeapPageBySlots repacks survivors and reclaims the space.
	return nil
}

func PageAddItemRaw(p Page, raw []byte) (uint16, error) {
	h, err := Header(p)
	if err != nil {
		return 0, err
	}
	if len(raw) > 0x7FFF {
		return 0, fmt.Errorf("item too large for line pointer len=%d", len(raw))
	}
	lower := int(h.Lower())
	upper := int(h.Upper())
	// MAXALIGNed placement, exactly as PageAddItemExtended does it
	// (postgres/src/backend/storage/page/bufpage.c): the allocation is
	// MAXALIGN(size) but the line pointer records the UNALIGNED size, so a
	// reader still recovers the item's true length from lp_len (and, for a
	// B-tree blob key, its key length as lp_len - SizeOfIndexTupleData).
	// Keeping pd_upper 8-byte aligned is what lets a real PG backend deform
	// an item in place: index_deform_tuple / heap_deform_tuple read
	// alignment-sensitive datums directly off the page. M0130-S11.4 3b-3a.
	alignedSize := maxAlign8(len(raw))
	needed := itemIDSize + alignedSize
	if upper-lower < needed {
		return 0, ErrNoSpaceInPage
	}
	newUpper := upper - alignedSize
	copy(p[newUpper:newUpper+len(raw)], raw)
	count, err := PageLinePointerCount(p)
	if err != nil {
		return 0, err
	}
	item := ItemID{Offset: uint16(newUpper), Flags: ItemIDNormal, Length: uint16(len(raw))}
	if err := writeItemID(p, count, item); err != nil {
		return 0, err
	}
	h.SetLower(uint16(lower + itemIDSize))
	h.SetUpper(uint16(newUpper))
	return uint16(count + 1), nil
}

// PageInsertItemRawAt (M0055-0002 Phase A) inserts `raw` at the
// 1-based slot, shifting any existing line pointers at [slot, count]
// to [slot+1, count+1]. New tuple bytes are placed at the
// pd_upper - len(raw) boundary. The 1-based slot number returned
// equals the input `slot`. Returns `ErrNoSpaceInPage` when the
// page lacks free space.
//
// This is the in-place upstream-aligned counterpart to the
// O(n) decode+rewrite path used previously by btree.insertItemSorted.
// Call sites that know the insertion offset (e.g. via binary search
// on existing line pointers) can avoid re-encoding every item on
// the page on each insert.
func PageInsertItemRawAt(p Page, slot uint16, raw []byte) (uint16, error) {
	h, err := Header(p)
	if err != nil {
		return 0, err
	}
	if len(raw) > 0x7FFF {
		return 0, fmt.Errorf("item too large for line pointer len=%d", len(raw))
	}
	lower := int(h.Lower())
	upper := int(h.Upper())
	// MAXALIGNed placement — see PageAddItemRaw. M0130-S11.4 3b-3a.
	alignedSize := maxAlign8(len(raw))
	needed := itemIDSize + alignedSize
	if upper-lower < needed {
		return 0, ErrNoSpaceInPage
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return 0, err
	}
	if int(slot) < 1 || int(slot) > count+1 {
		return 0, fmt.Errorf("PageInsertItemRawAt: slot %d out of range [1,%d]", slot, count+1)
	}
	// Append the new tuple bytes at upper-MAXALIGN(len(raw)); existing tuple
	// data on the page does NOT need to move (each line pointer
	// references its tuple by absolute offset, so existing items
	// remain addressable).
	newUpper := upper - alignedSize
	copy(p[newUpper:newUpper+len(raw)], raw)
	// Shift the line-pointer array's [slot-1 .. count) entries right
	// by one slot (line pointers are 0-based in the array but
	// 1-based externally).
	idx := int(slot) - 1
	if idx < count {
		// Read line pointers from idx..count-1 and write them at idx+1..count.
		// Each ItemID is itemIDSize bytes; use a memmove by copying the
		// bytes via the line-pointer accessors.
		for j := count; j > idx; j-- {
			id, err := readItemID(p, j-1)
			if err != nil {
				return 0, err
			}
			if err := writeItemID(p, j, id); err != nil {
				return 0, err
			}
		}
	}
	// Write the new line pointer at idx.
	newID := ItemID{Offset: uint16(newUpper), Flags: ItemIDNormal, Length: uint16(len(raw))}
	if err := writeItemID(p, idx, newID); err != nil {
		return 0, err
	}
	h.SetLower(uint16(lower + itemIDSize))
	h.SetUpper(uint16(newUpper))
	return slot, nil
}

// PageReplaceItemRaw (M0055-0003) replaces the line pointer's
// payload at the 1-based slot with `raw`. When the new bytes fit
// in the existing line pointer's slot, it overwrites in place.
// When `raw` is larger AND the page has free space, it allocates
// a fresh tuple-region location at pd_upper-len and updates the
// line pointer to reference the new location (the old bytes
// become garbage, reclaimed by the next vacuum / repack). Returns
// `ErrNoSpaceInPage` when neither the existing slot nor a fresh
// allocation fits.
//
// Used by the steady-state-insert dedup path: when an inserted key
// matches an existing posting-list line pointer, the new payload
// (existing TIDs + new TID) is written via this helper so the
// page line-pointer count stays the same.
func PageReplaceItemRaw(p Page, slot uint16, raw []byte) error {
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	if int(slot) < 1 || int(slot) > count {
		return fmt.Errorf("PageReplaceItemRaw: slot %d out of range [1,%d]", slot, count)
	}
	id, err := readItemID(p, int(slot)-1)
	if err != nil {
		return err
	}
	if len(raw) > 0x7FFF {
		return fmt.Errorf("item too large for line pointer len=%d", len(raw))
	}
	if int(id.Length) >= len(raw) {
		// Fits in the existing slot — overwrite in place. Update
		// the line pointer's length to the (possibly shorter) new
		// payload; the trailing bytes become garbage.
		copy(p[id.Offset:int(id.Offset)+len(raw)], raw)
		newID := ItemID{Offset: id.Offset, Flags: id.Flags, Length: uint16(len(raw))}
		return writeItemID(p, int(slot)-1, newID)
	}
	// Need to grow — allocate fresh space at pd_upper-len.
	h, err := Header(p)
	if err != nil {
		return err
	}
	lower := int(h.Lower())
	upper := int(h.Upper())
	// MAXALIGNed placement — see PageAddItemRaw. The in-place branch above
	// deliberately does NOT reuse this item's alignment padding: a page
	// written before M0130-S11.4 3b-3a has no padding to reuse, so growing
	// into it would clobber the neighbouring item.
	alignedSize := maxAlign8(len(raw))
	if upper-lower < alignedSize {
		return ErrNoSpaceInPage
	}
	newUpper := upper - alignedSize
	copy(p[newUpper:newUpper+len(raw)], raw)
	newID := ItemID{Offset: uint16(newUpper), Flags: id.Flags, Length: uint16(len(raw))}
	if err := writeItemID(p, int(slot)-1, newID); err != nil {
		return err
	}
	h.SetUpper(uint16(newUpper))
	return nil
}

// HeapPageVacuumStats reports the outcome of a single-page vacuum.
type HeapPageVacuumStats struct {
	Dead int // line pointers transitioned LP_NORMAL -> LP_UNUSED
	Live int // tuples that survived this pass
}

// VacuumHeapPage prunes dead tuples in-place.
//
// For every LP_NORMAL line pointer the predicate `isDead` is invoked
// with the tuple header. Dead slots are marked LP_UNUSED (length and
// offset zeroed); live tuples are repacked against pd_special so the
// in-page free-space window is contiguous again. Slot numbers are
// preserved — external pointers (B-tree leaf items, future ctids) into
// reclaimed slots simply observe LP_UNUSED on the next probe and treat
// the entry as absent.
//
// The line-pointer array itself is NOT truncated; future inserts can
// reuse LP_UNUSED slots, and stable slot numbering is the invariant
// that lets the index AM keep working without an in-band invalidation
// channel.
//
// On any structural error (corrupt header, invalid line pointer) the
// page is left untouched and the error is returned.
func VacuumHeapPage(p Page, isDead func(HeapTupleHeader) bool) (HeapPageVacuumStats, error) {
	deadSlots, err := CollectDeadHeapSlots(p, isDead)
	if err != nil {
		return HeapPageVacuumStats{}, err
	}
	return VacuumHeapPageBySlots(p, deadSlots)
}

// CollectDeadHeapSlots walks p and returns the 1-based slot numbers
// whose LP_NORMAL tuple satisfies isDead. The page itself is not
// modified. The returned slot list is in ascending order, matching
// the order VacuumHeapPageBySlots needs for deterministic replay.
//
// VACUUM uses this to compute the dead set once at original-mutation
// time, then both prune the page (via VacuumHeapPageBySlots) and
// emit a logical redo record carrying that slot list. Replay applies
// the same slot list against the existing page bytes, so the prune
// is bit-exact whether it's the original write or recovery.
func CollectDeadHeapSlots(p Page, isDead func(HeapTupleHeader) bool) ([]uint16, error) {
	count, err := PageLinePointerCount(p)
	if err != nil {
		return nil, err
	}
	var dead []uint16
	for idx := 0; idx < count; idx++ {
		item, err := readItemID(p, idx)
		if err != nil {
			return nil, err
		}
		if item.Flags != ItemIDNormal {
			continue
		}
		off := int(item.Offset)
		ln := int(item.Length)
		if off < 0 || ln < 0 || off+ln > len(p) {
			return nil, fmt.Errorf("%w: slot=%d off=%d len=%d", ErrCorruptTuple, idx+1, off, ln)
		}
		t, err := ParseHeapTuple(p[off : off+ln])
		if err != nil {
			return nil, err
		}
		if isDead(t.Header) {
			dead = append(dead, uint16(idx+1))
		}
	}
	return dead, nil
}

// VacuumHeapPageBySlots applies a vacuum prune to p with an explicit
// list of 1-based LP_NORMAL slot numbers to reclaim. It is the
// deterministic kernel shared by:
//
//   - the live VACUUM path, which collects the slot list via
//     CollectDeadHeapSlots, applies it here, and emits the same
//     slot list as a redo record; and
//   - WAL replay, which decodes the slot list from the redo record
//     and applies it here.
//
// deadSlots must be ascending and reference only LP_NORMAL slots;
// any out-of-range or non-LP_NORMAL entry returns an error and the
// page is left untouched.
func VacuumHeapPageBySlots(p Page, deadSlots []uint16) (HeapPageVacuumStats, error) {
	h, err := Header(p)
	if err != nil {
		return HeapPageVacuumStats{}, err
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return HeapPageVacuumStats{}, err
	}
	deadSet := make(map[int]struct{}, len(deadSlots))
	for _, s := range deadSlots {
		if s == 0 || int(s) > count {
			return HeapPageVacuumStats{}, fmt.Errorf("%w: dead slot %d out of range (count=%d)", ErrInvalidSlot, s, count)
		}
		deadSet[int(s)-1] = struct{}{}
	}
	type live struct {
		idx  int
		body []byte
	}
	var survivors []live
	stats := HeapPageVacuumStats{}
	for idx := 0; idx < count; idx++ {
		item, err := readItemID(p, idx)
		if err != nil {
			return HeapPageVacuumStats{}, err
		}
		if item.Flags != ItemIDNormal {
			continue
		}
		off := int(item.Offset)
		ln := int(item.Length)
		if off < 0 || ln < 0 || off+ln > len(p) {
			return HeapPageVacuumStats{}, fmt.Errorf("%w: slot=%d off=%d len=%d", ErrCorruptTuple, idx+1, off, ln)
		}
		if _, isDead := deadSet[idx]; isDead {
			if err := writeItemID(p, idx, ItemID{Flags: ItemIDUnused}); err != nil {
				return HeapPageVacuumStats{}, err
			}
			stats.Dead++
			continue
		}
		survivors = append(survivors, live{idx: idx, body: append([]byte(nil), p[off:off+ln]...)})
	}
	stats.Live = len(survivors)

	// Repack: zero the tuple region and rewrite live bodies down from
	// pd_special, updating each surviving line pointer's offset.
	special := int(h.Special())
	lineEnd := SizeOfPageHeaderData + count*itemIDSize
	for i := lineEnd; i < special; i++ {
		p[i] = 0
	}
	upper := special
	for _, e := range survivors {
		// MAXALIGN the tuple offset, exactly as the insert path does
		// (PageAddHeapTuple: alignedSize = maxAlign8(len(raw))). The repack
		// is the insert path's sibling: a survivor re-laid at a non-8-byte
		// boundary segfaults a PG18 standby's heap_deform_tuple on the first
		// SELECT and trips amcheck's "not maximally aligned" check. The
		// line-pointer Length still reports the real body length; the trailing
		// 0..7 padding bytes were already zeroed above. Survivors were placed
		// with this same alignment by the insert path, so the aligned footprint
		// never exceeds the space they originally occupied.
		upper -= maxAlign8(len(e.body))
		copy(p[upper:upper+len(e.body)], e.body)
		item, err := readItemID(p, e.idx)
		if err != nil {
			return HeapPageVacuumStats{}, err
		}
		item.Offset = uint16(upper)
		if err := writeItemID(p, e.idx, item); err != nil {
			return HeapPageVacuumStats{}, err
		}
	}
	h.SetUpper(uint16(upper))
	return stats, nil
}

// PageSetHeapTupleXmax overwrites the xmax field of the heap tuple
// at the given 1-based slot. This is the executor's MVCC delete /
// update-old-image path: marking xmax doesn't move the tuple bytes,
// so existing index pointers and concurrent readers stay valid.
//
// Lock-only bookkeeping carry-over: if the tuple was previously
// row-locked by a SELECT FOR UPDATE (HeapXmaxLockOnly + a
// lock-strength bit set in infomask), a DELETE / UPDATE that
// supersedes the lock must clear those bits before re-stamping
// xmax. Otherwise mvcc.TupleVisible's lock-only short-circuit
// would mistake our delete-stamp xmax for a still-locked tuple
// and erroneously keep the tuple visible. Mirrors upstream's
// HEAP_LOCK_MASK clearing inside heap_delete / heap_update.
//
// Returns ErrUnsupportedItem if the slot isn't LP_NORMAL, ErrInvalidSlot
// for out-of-range slot numbers.
func PageSetHeapTupleXmax(p Page, slot uint16, xmax TransactionID) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(xmax))
	// Clear lock-only metadata so future readers treat this xmax
	// as a real delete, not a lingering row-lock from a
	// since-superseded SELECT FOR UPDATE. No-op when no bits
	// were set (the pre-M0021 case).
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	// Clear lock-only bits AND HeapXmaxInvalid so isConcurrentlyUpdated
	// sees a real delete/update stamp. HeapXmaxInvalid is set by PG
	// physical-format inserts (writeHeapRowReturningPG) to signal "xmax is
	// not a deleter" on fresh rows. Failing to clear it here causes
	// isConcurrentlyUpdated to return false for any tuple written via that
	// path, silently skipping the EPQ wait loop on concurrent
	// DELETE/UPDATE. Mirrors PG's heap_update / heap_delete which clear
	// HEAP_XMAX_INVALID before re-stamping xmax.
	infomask &^= HeapXmaxLockOnly | HeapXmaxLockMask | HeapXmaxInvalid | HeapXmaxIsMulti
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	// Advance pd_prune_xid so opportunistic pruning knows when
	// this page first became prunable (M0046-0002).
	if pruneXID := MustHeader(p).PruneXID(); xmax > TransactionID(pruneXID) {
		MustHeader(p).SetPruneXID(uint32(xmax))
	}
	return nil
}

// PageSetHeapTupleXmaxCommitted sets the HEAP_XMAX_COMMITTED hint bit on the
// tuple at the 1-based slot. The caller must already have set xmax via
// PageSetHeapTupleXmax (or the tuple was written with a non-zero xmax).
// This is used by catalog-row stamping (stampCatalogRows) so that runtime
// seq-scan visibility (TupleVisibleSubxact) takes the fast
// HeapXmaxCommitted path, avoiding a snapshot-based fallthrough that may
// incorrectly hold a re-synced catalog row visible after the deleting
// transaction committed. DU-002.
func PageSetHeapTupleXmaxCommitted(p Page, slot uint16) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	infomask |= HeapXmaxCommitted
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	return nil
}

// PageSetHeapTupleCmax writes the deleting CommandId (cmax) into the on-page
// tuple header at offset 8-11 (the t_cid/t_xvac union field) and manages the
// HEAP_COMBOCID infomask bit. When isCombo is true, HEAP_COMBOCID is set
// (the raw cid is a combo CID, resolved via ComboCIDStore). When false, the
// raw cid is the plain cmax and HEAP_COMBOCID is clear.
//
// This mirrors PostgreSQL's HeapTupleHeaderSetCmax + the in-place update at
// heap_delete/heap_update after the buffer is locked.
func PageSetHeapTupleCmax(p Page, slot uint16, cid CommandId, isCombo bool) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+23 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	// Write the raw CommandId at offset 8-11 (t_cid/t_xvac union).
	binary.LittleEndian.PutUint32(p[off+8:off+12], uint32(cid))
	// Manage HEAP_COMBOCID in infomask (offset 20-21).
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	if isCombo {
		infomask |= HeapComboCID
	} else {
		infomask &^= HeapComboCID
	}
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	return nil
}

// PageSetHeapTupleKeysUpdated sets the HeapKeysUpdated bit in t_infomask2 of
// the tuple at slot. Called by updateViaIndex when at least one indexed (key)
// column is being modified, so FOR KEY SHARE knows it must conflict.
func PageSetHeapTupleKeysUpdated(p Page, slot uint16) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+20 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	infomask2 := binary.LittleEndian.Uint16(p[off+18 : off+20])
	infomask2 |= HeapKeysUpdated
	binary.LittleEndian.PutUint16(p[off+18:off+20], infomask2)
	return nil
}

// PageSetHeapTupleLockKeysUpdated sets or clears the HeapKeysUpdated bit in
// t_infomask2 of the tuple at slot. The row-lock producer calls it right after
// PageSetHeapTupleLockOnly when stamping a single-holder lock-only xmax: FOR
// UPDATE reserves the key (bit set) exactly as a key-changing UPDATE writes it,
// while FOR KEY SHARE / FOR SHARE / FOR NO KEY UPDATE leave it clear. Clearing
// on the weaker strengths prevents a stale bit (from a prior FOR UPDATE lock on
// the same line pointer) from mis-decoding a later FOR NO KEY UPDATE holder as
// FOR UPDATE. Mirrors heap_lock_tuple's new_infomask2 handling. M0118-0003.
func PageSetHeapTupleLockKeysUpdated(p Page, slot uint16, keysUpdated bool) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+20 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	infomask2 := binary.LittleEndian.Uint16(p[off+18 : off+20])
	if keysUpdated {
		infomask2 |= HeapKeysUpdated
	} else {
		infomask2 &^= HeapKeysUpdated
	}
	binary.LittleEndian.PutUint16(p[off+18:off+20], infomask2)
	return nil
}

// PageSetHeapTupleMovedPartition stamps xmax on the tuple at
// `slot` and writes the upstream "moved to another partition"
// sentinel (block=InvalidBlockNumber, offset=MovedPartitionsOffsetNumber)
// into its t_ctid. Used by cross-partition UPDATE: the old version is
// deleted in the source partition and the new version lives in a
// different relation entirely, so the CTID can't carry a successor
// pointer. EPQ retries that hit this sentinel raise the upstream
// `tuple to be locked was already moved to another partition due to
// concurrent update` error rather than silently skipping the row.
//
// Like PageSetHeapTupleXmax it clears HeapXmaxLockOnly/Mask bits so
// readers see the xmax as a real delete (not a lingering row lock).
// Returns ErrUnsupportedItem if the slot isn't LP_NORMAL,
// ErrInvalidSlot for out-of-range slot numbers.
func PageSetHeapTupleMovedPartition(p Page, slot uint16, xmax TransactionID) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(xmax))
	// t_ctid sits at off+12 (block, 4 bytes) and off+16 (offset, 2 bytes).
	binary.LittleEndian.PutUint32(p[off+12:off+16], uint32(InvalidBlockNumber))
	binary.LittleEndian.PutUint16(p[off+16:off+18], MovedPartitionsOffsetNumber)
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	// Also clear HeapXmaxInvalid — canonical-WAL inserts set it to mark
	// "xmax is not a deleter"; a moved-partition stamp IS a real xmax and
	// must clear the flag so isConcurrentlyUpdated detects the concurrent
	// update. Mirrors PageSetHeapTupleXmax and PG's heap_update behaviour.
	infomask &^= HeapXmaxLockOnly | HeapXmaxLockMask | HeapXmaxInvalid | HeapXmaxIsMulti
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	if pruneXID := MustHeader(p).PruneXID(); xmax > TransactionID(pruneXID) {
		MustHeader(p).SetPruneXID(uint32(xmax))
	}
	return nil
}

// PageSetHeapTupleCtid overwrites only the t_ctid field of the heap tuple
// at the given 1-based slot. Used by non-HOT cross-page UPDATE: after the
// new tuple version is written elsewhere (different page or a different
// relfile entirely), the old tuple's t_ctid is updated to point at the
// successor so that EvalPlanQual chain followers can locate the latest
// version. Visibility (xmin/xmax) is untouched. Caller must hold the
// page write lock.
func PageSetHeapTupleCtid(p Page, slot uint16, ctid ItemPointer) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+18 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	binary.LittleEndian.PutUint32(p[off+12:off+16], uint32(ctid.Block))
	binary.LittleEndian.PutUint16(p[off+16:off+18], ctid.Offset)
	return nil
}

// PageSetHeapTupleLockOnly stamps xmax + sets the
// HEAP_XMAX_LOCK_ONLY and lock-strength infomask bits on the heap
// tuple at the given 1-based slot. Companion to PageSetHeapTupleXmax
// for the row-lock path: lock-only xmax doesn't make the tuple
// invisible — `mvcc.TupleVisible` recognises the bit and lets
// readers through. Used by the SELECT FOR UPDATE / FOR SHARE
// runtime to record per-row lock holders without deleting the
// tuple.
//
// `lockStrength` selects which lock-mode bit gets set in infomask:
//
//   - HeapXmaxExclLock for FOR UPDATE (write-intent lock).
//   - HeapXmaxShrLock  for FOR SHARE (read-intent lock).
//   - HeapXmaxKeyShrLock for FOR KEY SHARE (out of v0 scope but
//     accepted at the encoding layer for forward-compat).
//
// Existing infomask bits OTHER than the lock-strength group are
// preserved; lock-strength bits in HeapXmaxLockMask are cleared
// before OR-ing the new mode in so a re-stamp from a stronger
// lock to a weaker (or vice versa) doesn't accumulate stale
// bits.
//
// Returns ErrUnsupportedItem if the slot isn't LP_NORMAL,
// ErrInvalidSlot for out-of-range slot numbers,
// ErrCorruptTuple for tuples whose Hoff is too small to hold
// the infomask bytes.
func PageSetHeapTupleLockOnly(p Page, slot uint16, xmax TransactionID, lockStrength uint16) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	if lockStrength&HeapXmaxLockMask == 0 {
		return fmt.Errorf("PageSetHeapTupleLockOnly: lockStrength %#x has no lock-mode bit set", lockStrength)
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	// Need bytes 0..21 of the header (Xmax at 4..8, Infomask at
	// 20..22 — note the on-disk order Infomask2 then Infomask
	// per ParseHeapTuple / MarshalBinary).
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(xmax))
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	infomask &^= HeapXmaxLockMask
	infomask &^= HeapXmaxInvalid // xmax is now a real (lock-only) value
	infomask &^= HeapXmaxIsMulti // single-holder xmax: clear any prior multi bit
	infomask |= HeapXmaxLockOnly
	infomask |= lockStrength & HeapXmaxLockMask
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	return nil
}

// PageApplyHeapLockRedo re-applies a PostgreSQL XLOG_HEAP_LOCK record's tuple
// mutation, mirroring heap_xlog_lock (heapam_xlog.c) byte for byte. M0131-S21a-2.
//
// It is the redo sibling of PageSetHeapTupleLockOnly, and deliberately NOT the
// same function: the runtime helper stamps a lock goopg is taking right now
// (single xid, always lock-only, strength bit required), whereas redo must
// reproduce whatever PG decided — a multixact xmax, an updater's key-share
// lock, or a lock-only stamp — from the record's infobits, and additionally
// resets t_ctid and cmax the way upstream does.
//
// infomaskBits / infomask2Bits are the OUTPUT of upstream's
// fix_infomask_from_infobits, i.e. the caller (the WAL layer, which owns
// knowledge of the XLHL_* wire bits) has already translated xl_heap_lock's
// infobits_set byte. Only the four xmax-classification bits and
// HEAP_KEYS_UPDATED are honoured; anything else in the arguments is ignored.
//
// Order of operations, all from heap_xlog_lock:
//
//  1. clear HEAP_XMAX_BITS and HEAP_MOVED in t_infomask, HEAP_KEYS_UPDATED in
//     t_infomask2, then OR in the record's bits;
//  2. if the resulting xmax is locked-only, clear HEAP_HOT_UPDATED and point
//     t_ctid at the tuple itself — a locker must not leave a forward chain
//     link behind, or a reader would follow it to a version that does not
//     exist;
//  3. stamp xmax, and set cmax = FirstCommandId with HEAP_COMBOCID cleared
//     (the combo cid of the locking backend is meaningless after a crash).
//
// blk is the page's own block number, needed for the self-pointing t_ctid.
// Callers hold the page write lock and set pd_lsn themselves.
func PageApplyHeapLockRedo(p Page, slot uint16, xmax TransactionID, infomaskBits, infomask2Bits uint16, blk BlockNumber) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	// Upstream elog(PANIC, "invalid lp") here; goopg returns the error so the
	// caller can name the record that carried the bad offset.
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	infomask2 := binary.LittleEndian.Uint16(p[off+18 : off+20])
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])

	infomask &^= HeapXmaxBits | HeapMoved
	infomask2 &^= HeapKeysUpdated
	infomask |= infomaskBits & (HeapXmaxIsMulti | HeapXmaxLockOnly | HeapXmaxExclLock | HeapXmaxKeyShrLock)
	infomask2 |= infomask2Bits & HeapKeysUpdated

	if isHeapXmaxLockedOnlyPG(infomask) {
		infomask2 &^= HeapHotUpdated
		binary.LittleEndian.PutUint32(p[off+12:off+16], uint32(blk))
		binary.LittleEndian.PutUint16(p[off+16:off+18], slot)
	}
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(xmax))
	binary.LittleEndian.PutUint32(p[off+8:off+12], uint32(FirstCommandId))
	infomask &^= HeapComboCID

	binary.LittleEndian.PutUint16(p[off+18:off+20], infomask2)
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	return nil
}

// PageApplyHeapLockUpdatedRedo re-applies a PostgreSQL XLOG_HEAP2_LOCK_UPDATED
// record's tuple mutation, mirroring heap_xlog_lock_updated (heapam_xlog.c)
// byte for byte. M0131-S21a-2 part 4.
//
// It is XLOG_HEAP_LOCK's near-sibling — emitted by heap_lock_updated_tuple_rec
// when a tuple-lock request (SELECT ... FOR UPDATE/SHARE, an FK RI check, an
// UPDATE about to rewrite its target row) discovers the row it locked was
// concurrently updated and re-locks the newest visible version of the chain —
// but upstream's redo is deliberately smaller than PageApplyHeapLockRedo's:
// there is no "locked-only" t_ctid/HOT_UPDATED fixup (a lock taken on an
// already-updated tuple can never legitimately claim to be the chain's head)
// and no cmax stamp (a re-locked older version was never the transaction's
// own command target, so FirstCommandId would be a fabricated cmax).
//
// infomaskBits / infomask2Bits are xlogHeapLockInfomaskBits' output — the
// caller has already translated xl_heap_lock_updated's infobits_set wire byte
// (the struct is byte-identical to xl_heap_lock's).
//
// blk/slot/p semantics match PageApplyHeapLockRedo. Callers hold the page
// write lock and set pd_lsn themselves.
func PageApplyHeapLockUpdatedRedo(p Page, slot uint16, xmax TransactionID, infomaskBits, infomask2Bits uint16) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	// Upstream elog(PANIC, "invalid lp") here; goopg returns the error so the
	// caller can name the record that carried the bad offset.
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	infomask2 := binary.LittleEndian.Uint16(p[off+18 : off+20])
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])

	infomask &^= HeapXmaxBits | HeapMoved
	infomask2 &^= HeapKeysUpdated
	infomask |= infomaskBits & (HeapXmaxIsMulti | HeapXmaxLockOnly | HeapXmaxExclLock | HeapXmaxKeyShrLock)
	infomask2 |= infomask2Bits & HeapKeysUpdated
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(xmax))

	binary.LittleEndian.PutUint16(p[off+18:off+20], infomask2)
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	return nil
}

// PageSetHeapTupleXmaxMulti stamps the given heap tuple's xmax with a
// MultiXactId and the caller-computed hint bits (see internal/multixact.HintBits).
// It is the multixact sibling of PageSetHeapTupleLockOnly: where that helper
// records a single-transaction row lock, this records a *set* of lock holders
// (HEAP_XMAX_IS_MULTI). The xmax value is therefore a MultiXactId resolved
// through the multixact member store, never a plain TransactionID — readers must
// gate on IsHeapTupleXmaxMulti before interpreting it.
//
// infomaskBits / infomask2Bits are the values returned by multixact.HintBits:
//   - infomaskBits carries HEAP_XMAX_IS_MULTI, the strongest holder's lock-mode
//     bit(s), and HEAP_XMAX_LOCK_ONLY when the multi has no updater.
//   - infomask2Bits carries HEAP_KEYS_UPDATED when a member reserves key columns.
//
// Pre-existing xmax-classification bits (lock mask, lock-only, invalid, committed,
// is-multi) and the keys-updated bit are cleared before the new ones are OR-ed in,
// so a tuple previously carrying a single-xid lock-only xmax is cleanly re-stamped
// as a multi. Other infomask bits (xmin hints, HEAP_HASNULL, …) are preserved.
func PageSetHeapTupleXmaxMulti(p Page, slot uint16, multi TransactionID, infomaskBits, infomask2Bits uint16) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, slot, off)
	}
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(multi))
	// t_infomask2 at [18:20], t_infomask at [20:22] (on-disk order Infomask2
	// then Infomask, per ParseHeapTuple / MarshalBinary).
	infomask2 := binary.LittleEndian.Uint16(p[off+18 : off+20])
	infomask2 &^= HeapKeysUpdated
	infomask2 |= infomask2Bits & HeapKeysUpdated
	binary.LittleEndian.PutUint16(p[off+18:off+20], infomask2)

	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	infomask &^= HeapXmaxLockMask | HeapXmaxLockOnly | HeapXmaxInvalid | HeapXmaxCommitted | HeapXmaxIsMulti
	infomask |= infomaskBits
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	return nil
}

// PageStampHotOldTuple stamps a HOT-update on the old-image tuple at
// oldSlot: writes xmax, sets HeapHotUpdated in infomask, and updates the
// CTID to (blk, newSlot) so index-scan callers can walk the HOT chain
// to the live successor version. Mirrors the actions upstream performs
// inside heap_update when it detects no indexed column changed.
//
// The caller must hold the page's exclusive content lock across the
// entire HOT-update sequence (new tuple insert + this stamp) so the
// chain is never torn between two page modifications.
func PageStampHotOldTuple(p Page, oldSlot uint16, xmax TransactionID, blk BlockNumber, newSlot uint16) error {
	if oldSlot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(oldSlot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, oldSlot, item.Flags)
	}
	off := int(item.Offset)
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, oldSlot, off)
	}
	// Set xmax.
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(xmax))
	// Set CTID to (blk, newSlot) — same-page chain link.
	binary.LittleEndian.PutUint32(p[off+12:off+16], uint32(blk))
	binary.LittleEndian.PutUint16(p[off+16:off+18], newSlot)
	// Update t_infomask at [20:22]: clear lock-only bits (a delete supersedes
	// any lingering row-lock).
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	infomask &^= HeapXmaxLockOnly | HeapXmaxLockMask | HeapXmaxInvalid | HeapXmaxIsMulti
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	// Mark the HOT chain in t_infomask2 at [18:20] — HEAP_HOT_UPDATED lives
	// there, not in t_infomask (M0131-S11; htup_details.h:550).
	infomask2 := binary.LittleEndian.Uint16(p[off+18 : off+20])
	infomask2 |= HeapHotUpdated
	binary.LittleEndian.PutUint16(p[off+18:off+20], infomask2)
	// Advance pd_prune_xid (M0046-0002): the old HOT tuple is dead
	// once xmax is committed and xmax < OldestXmin.
	if pruneXID := MustHeader(p).PruneXID(); xmax > TransactionID(pruneXID) {
		MustHeader(p).SetPruneXID(uint32(xmax))
	}
	return nil
}

// PageStampUpdatedOldTuple is the NON-HOT sibling of PageStampHotOldTuple
// (B0.2, doc 02a §3): stamps a plain heap UPDATE on the old-image tuple at
// oldSlot — xmax, forward t_ctid to (blk, newSlot) which may live on a
// DIFFERENT page, clear the xmax-invalid/lock bits — WITHOUT setting
// HeapHotUpdated (the successor is reached via indexes, not a HOT chain).
// Mirrors upstream heap_update's old-tuple treatment when an indexed column
// changed. The caller holds the page's exclusive content lock.
func PageStampUpdatedOldTuple(p Page, oldSlot uint16, xmax TransactionID, blk BlockNumber, newSlot uint16) error {
	if oldSlot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(oldSlot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, oldSlot, item.Flags)
	}
	off := int(item.Offset)
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, oldSlot, off)
	}
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(xmax))
	binary.LittleEndian.PutUint32(p[off+12:off+16], uint32(blk))
	binary.LittleEndian.PutUint16(p[off+16:off+18], newSlot)
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	infomask &^= HeapXmaxLockOnly | HeapXmaxLockMask | HeapXmaxInvalid | HeapXmaxIsMulti
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	if pruneXID := MustHeader(p).PruneXID(); xmax > TransactionID(pruneXID) {
		MustHeader(p).SetPruneXID(uint32(xmax))
	}
	return nil
}

// PageStampHotOldTupleMulti is the MultiXact-bearing sibling of
// PageStampHotOldTuple: it stamps a HOT-update on the old-image tuple at
// oldSlot where the new xmax is a MultiXactId naming {updater + surviving
// lockers} rather than the updater's single TransactionID. It is used by the
// UPDATE producer when the old tuple already carried a non-conflicting
// foreign lock-only xmax (e.g. a FOR KEY SHARE locker) that must be preserved
// into the multi instead of being clobbered by the plain single-xid stamp
// (mirrors upstream heap_update → MultiXactIdCreate/Expand on the
// HEAP_XMAX_IS_LOCKED_ONLY pre-existing-locker path).
//
// multi is the MultiXactId; infomaskBits / infomask2Bits are the hint bits
// computed by multixact.HintBits for the member set (they carry
// HEAP_XMAX_IS_MULTI and the strongest holder's lock-mode bits, and — because
// the set has an updater — clear HEAP_XMAX_LOCK_ONLY). updaterXID is the real
// update member (the current writer's XID); it is used only to advance
// pd_prune_xid, never written to xmax. blk/newSlot link the HOT chain to the
// live successor, exactly as PageStampHotOldTuple does.
//
// The caller must hold the page's exclusive content lock across the entire
// HOT-update sequence (new tuple insert + this stamp) so the chain is never
// torn between two page modifications.
func PageStampHotOldTupleMulti(p Page, oldSlot uint16, multi TransactionID, infomaskBits, infomask2Bits uint16, updaterXID TransactionID, blk BlockNumber, newSlot uint16) error {
	if oldSlot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(oldSlot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, oldSlot, item.Flags)
	}
	off := int(item.Offset)
	if off+22 > len(p) {
		return fmt.Errorf("%w: slot=%d off=%d", ErrCorruptTuple, oldSlot, off)
	}
	// Set xmax to the MultiXactId.
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(multi))
	// Set CTID to (blk, newSlot) — same-page HOT chain link.
	binary.LittleEndian.PutUint32(p[off+12:off+16], uint32(blk))
	binary.LittleEndian.PutUint16(p[off+16:off+18], newSlot)
	// t_infomask2 at [18:20]: refresh HEAP_KEYS_UPDATED from the multi hint,
	// and mark the HOT chain — HEAP_HOT_UPDATED lives in this field, not in
	// t_infomask (M0131-S11; htup_details.h:550).
	infomask2 := binary.LittleEndian.Uint16(p[off+18 : off+20])
	infomask2 &^= HeapKeysUpdated
	infomask2 |= infomask2Bits & HeapKeysUpdated
	infomask2 |= HeapHotUpdated
	binary.LittleEndian.PutUint16(p[off+18:off+20], infomask2)
	// t_infomask at [20:22]: clear xmax-classification bits, then OR in the
	// multi hint bits (which set HEAP_XMAX_IS_MULTI and clear
	// HEAP_XMAX_LOCK_ONLY for an updater-bearing multi).
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	infomask &^= HeapXmaxLockMask | HeapXmaxLockOnly | HeapXmaxInvalid | HeapXmaxCommitted | HeapXmaxIsMulti
	infomask |= infomaskBits
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	// Advance pd_prune_xid (M0046-0002) using the real update member: the old
	// HOT tuple is dead once the updater commits and is < OldestXmin. The multi
	// id itself is not a TransactionID, so prune tracking uses updaterXID.
	if pruneXID := MustHeader(p).PruneXID(); updaterXID > TransactionID(pruneXID) {
		MustHeader(p).SetPruneXID(uint32(updaterXID))
	}
	return nil
}

// PageGetItemID returns the raw ItemID for a 1-based line pointer slot.
// Used by opportunistic pruning and HOT chain following to inspect line
// pointer flags without attempting to decode a full heap tuple.
func PageGetItemID(p Page, slot uint16) (ItemID, error) {
	if slot == 0 {
		return ItemID{}, ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return ItemID{}, err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ItemID{}, ErrInvalidSlot
	}
	return readItemID(p, idx)
}

// PageSetItemIDRedirect converts the line pointer at 1-based slot into an
// ItemIDRedirect pointing to targetSlot. Used by opportunistic pruning when a
// dead HOT-chain root is freed: the line pointer is kept (so the index entry
// remains valid) but redirected to the live chain tip.
//
// The Offset field of the resulting ItemID carries the 1-based target slot;
// Length is set to zero (the tuple data will be freed by the compaction pass).
func PageSetItemIDRedirect(p Page, slot uint16, targetSlot uint16) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	return writeItemID(p, idx, ItemID{Offset: targetSlot, Flags: ItemIDRedirect, Length: 0})
}

// PageGetItemRaw returns the raw item bytes at the 1-based slot. The
// returned slice is a copy.
func PageGetItemRaw(p Page, slot uint16) ([]byte, error) {
	if slot == 0 {
		return nil, ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return nil, err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return nil, ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return nil, err
	}
	if item.Flags != ItemIDNormal {
		return nil, fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	ln := int(item.Length)
	if off < 0 || ln < 0 || off+ln > len(p) {
		return nil, fmt.Errorf("%w: slot=%d off=%d len=%d", ErrCorruptTuple, slot, off, ln)
	}
	return append([]byte(nil), p[off:off+ln]...), nil
}

// PageGetItemRawNoCopy is the alias-the-page variant of
// PageGetItemRaw. The returned slice REFERENCES the page bytes
// directly — no allocation, but the caller MUST hold the page pin
// for the lifetime of the returned slice and MUST NOT retain it
// across the unpin. Used by btree.RangeScan's CAT-1 callers per
// M0091-0002.
func PageGetItemRawNoCopy(p Page, slot uint16) ([]byte, error) {
	if slot == 0 {
		return nil, ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return nil, err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return nil, ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return nil, err
	}
	if item.Flags != ItemIDNormal {
		return nil, fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	ln := int(item.Length)
	if off < 0 || ln < 0 || off+ln > len(p) {
		return nil, fmt.Errorf("%w: slot=%d off=%d len=%d", ErrCorruptTuple, slot, off, ln)
	}
	return p[off : off+ln], nil
}

// PageItemIsDead reports whether slot's line pointer carries ItemIDDead
// (C3: btree LP_DEAD on-access cleanup — hint that the referenced heap
// tuple is dead to all snapshots). slot is 1-based.
func PageItemIsDead(p Page, slot uint16) (bool, error) {
	if slot == 0 {
		return false, ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return false, err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return false, ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return false, err
	}
	return item.Flags == ItemIDDead, nil
}

// PageSetItemIDDead flips slot's line pointer to ItemIDDead IN PLACE:
// offset and length are preserved, so the item bytes remain valid for
// binary-search ordering until a purge compacts the page (PG keeps
// LP_DEAD index tuples' storage the same way). The caller must hold the
// page's exclusive latch; the mark is an UNLOGGED hint (C3 design D2) and
// the caller must NOT bump pd_lsn for it (D7 — the deferred-mark re-verify
// keys on the captured page LSN). Btree callers must only mark LEAF
// entries (C3-S1 review: internal-page structural walks index by
// first/live item and would corrupt structure on a marked downlink; the
// S3 marking entry point asserts IsLeaf). slot is 1-based.
func PageSetItemIDDead(p Page, slot uint16) error {
	if slot == 0 {
		return ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return err
	}
	if item.Flags != ItemIDNormal && item.Flags != ItemIDDead {
		return fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	item.Flags = ItemIDDead
	return writeItemID(p, idx, item)
}

// PageGetItemRawAllowDead is PageGetItemRaw except it also reads
// ItemIDDead slots (a Dead btree item keeps valid key bytes for ordering
// until a purge removes it — binary-search probes need them). Callers
// that must not RETURN dead entries check PageItemIsDead separately.
func PageGetItemRawAllowDead(p Page, slot uint16) ([]byte, error) {
	if slot == 0 {
		return nil, ErrInvalidSlot
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return nil, err
	}
	idx := int(slot) - 1
	if idx < 0 || idx >= count {
		return nil, ErrInvalidSlot
	}
	item, err := readItemID(p, idx)
	if err != nil {
		return nil, err
	}
	if item.Flags != ItemIDNormal && item.Flags != ItemIDDead {
		return nil, fmt.Errorf("%w: slot=%d flags=%d", ErrUnsupportedItem, slot, item.Flags)
	}
	off := int(item.Offset)
	ln := int(item.Length)
	if off < 0 || ln < 0 || off+ln > len(p) {
		return nil, fmt.Errorf("%w: slot=%d off=%d len=%d", ErrCorruptTuple, slot, off, ln)
	}
	return append([]byte(nil), p[off:off+ln]...), nil
}

func readItemID(p Page, idx int) (ItemID, error) {
	off := SizeOfPageHeaderData + idx*itemIDSize
	if off < 0 || off+itemIDSize > len(p) {
		return ItemID{}, fmt.Errorf("line pointer index out of range: idx=%d", idx)
	}
	raw := binary.LittleEndian.Uint32(p[off : off+itemIDSize])
	return unpackItemID(raw), nil
}

func writeItemID(p Page, idx int, item ItemID) error {
	off := SizeOfPageHeaderData + idx*itemIDSize
	if off < 0 || off+itemIDSize > len(p) {
		return fmt.Errorf("line pointer index out of range: idx=%d", idx)
	}
	raw, err := item.pack()
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(p[off:off+itemIDSize], raw)
	return nil
}

// heapTupleInfomaskOff is the byte offset of t_infomask within the on-page
// heap tuple header. Layout: Xmin(4)+Xmax(4)+Xvac(4)+CTID(6)+Infomask2(2) = 20.
const heapTupleInfomaskOff = 20

// SetXminHintBit OR-s HeapXminCommitted (committed=true) or HeapXminInvalid
// (committed=false) into the on-page infomask of the tuple at the given slot.
// The caller must hold the page's content write lock.
func SetXminHintBit(page Page, slot uint16, committed bool) {
	item, err := PageGetItemID(page, slot)
	if err != nil {
		return
	}
	off := int(item.Offset) + heapTupleInfomaskOff
	if off+2 > len(page) {
		return
	}
	old := binary.LittleEndian.Uint16(page[off : off+2])
	var bit uint16
	if committed {
		bit = HeapXminCommitted
	} else {
		bit = HeapXminInvalid
	}
	binary.LittleEndian.PutUint16(page[off:off+2], old|bit)
}
