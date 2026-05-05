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
	HeapXmaxInvalid    uint16 = 0x0800
	HeapXmaxCommitted  uint16 = 0x0400
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
	HeapHotUpdated uint16 = 0x4000
	// HeapOnlyTuple indicates this tuple is a HOT-only version: it was
	// inserted as the successor in a HOT update chain and has no direct
	// index entry. Mirrors PostgreSQL's HEAP_ONLY_TUPLE (0x8000).
	HeapOnlyTuple uint16 = 0x8000
)

// IsHeapTupleLockOnly reports whether `infomask` indicates the
// tuple's xmax represents a row lock (not a delete). Mirrors
// upstream's HEAP_XMAX_IS_LOCKED_ONLY macro.
func IsHeapTupleLockOnly(infomask uint16) bool {
	return infomask&HeapXmaxLockOnly != 0
}

// ItemPointer identifies a tuple location (block, line-pointer slot).
type ItemPointer struct {
	Block  BlockNumber
	Offset uint16
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
type HeapTuple struct {
	Header HeapTupleHeader
	Data   []byte
}

// NewHeapTuple constructs a tuple with v0 defaults.
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

// MarshalBinary encodes the tuple into the on-page layout.
func (t HeapTuple) MarshalBinary() ([]byte, error) {
	hoff := int(t.Header.Hoff)
	if hoff == 0 {
		hoff = DefaultHeapTupleHoff
	}
	if hoff < SizeOfHeapTupleHeaderData || hoff > 255 {
		return nil, fmt.Errorf("invalid t_hoff=%d", hoff)
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
	copy(out[hoff:], t.Data)
	return out, nil
}

// ParseHeapTuple decodes one on-page tuple payload.
func ParseHeapTuple(raw []byte) (HeapTuple, error) {
	if len(raw) < SizeOfHeapTupleHeaderData {
		return HeapTuple{}, fmt.Errorf("%w: raw len=%d", ErrCorruptTuple, len(raw))
	}
	hoff := int(raw[22])
	if hoff < SizeOfHeapTupleHeaderData || hoff > len(raw) {
		return HeapTuple{}, fmt.Errorf("%w: invalid t_hoff=%d len=%d", ErrCorruptTuple, hoff, len(raw))
	}
	t := HeapTuple{
		Header: HeapTupleHeader{
			Xmin:      TransactionID(binary.LittleEndian.Uint32(raw[0:4])),
			Xmax:      TransactionID(binary.LittleEndian.Uint32(raw[4:8])),
			Xvac:      TransactionID(binary.LittleEndian.Uint32(raw[8:12])),
			CTID:      ItemPointer{Block: BlockNumber(binary.LittleEndian.Uint32(raw[12:16])), Offset: binary.LittleEndian.Uint16(raw[16:18])},
			Infomask2: binary.LittleEndian.Uint16(raw[18:20]),
			Infomask:  binary.LittleEndian.Uint16(raw[20:22]),
			Hoff:      uint8(hoff),
		},
		Data: append([]byte(nil), raw[hoff:]...),
	}
	return t, nil
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
	needed := itemIDSize + len(raw)
	if upper-lower < needed {
		return 0, ErrNoSpaceInPage
	}

	newUpper := upper - len(raw)
	copy(p[newUpper:upper], raw)

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
	needed := itemIDSize + len(raw)
	if upper-lower < needed {
		return 0, ErrNoSpaceInPage
	}
	newUpper := upper - len(raw)
	copy(p[newUpper:upper], raw)
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
	needed := itemIDSize + len(raw)
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
	// Append the new tuple bytes at upper-len(raw); existing tuple
	// data on the page does NOT need to move (each line pointer
	// references its tuple by absolute offset, so existing items
	// remain addressable).
	newUpper := upper - len(raw)
	copy(p[newUpper:upper], raw)
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
		upper -= len(e.body)
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
	infomask &^= HeapXmaxLockOnly | HeapXmaxLockMask
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	// Advance pd_prune_xid so opportunistic pruning knows when
	// this page first became prunable (M0046-0002).
	if pruneXID := MustHeader(p).PruneXID(); xmax > TransactionID(pruneXID) {
		MustHeader(p).SetPruneXID(uint32(xmax))
	}
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
	infomask |= HeapXmaxLockOnly
	infomask |= lockStrength & HeapXmaxLockMask
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
	// Update infomask: clear lock-only bits (a delete supersedes any
	// lingering row-lock), then set HeapHotUpdated.
	infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
	infomask &^= HeapXmaxLockOnly | HeapXmaxLockMask
	infomask |= HeapHotUpdated
	binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
	// Advance pd_prune_xid (M0046-0002): the old HOT tuple is dead
	// once xmax is committed and xmax < OldestXmin.
	if pruneXID := MustHeader(p).PruneXID(); xmax > TransactionID(pruneXID) {
		MustHeader(p).SetPruneXID(uint32(xmax))
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
