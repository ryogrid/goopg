// Package btree implements goopg's v0 B-tree index access method.
//
// Scope and growth path are documented in docs/design/0009-btree.md.
// v0 supports single-column int4 keys, descend/insert/search/range-scan,
// recursive splits up to a new root. WAL records for inserts/splits and
// page deletion (VACUUM merge) are deferred — see the doc for the bridge.
package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

// SizeOfBTPageOpaque is the on-disk size of the per-page B-tree opaque
// area. v3 grew it from 24 to 48 bytes to carry a variable-length
// HighKey (M0011-0002 — see
// docs/design/0011-0002-btree-numeric-build-and-uniqueness.md), which
// is required for NUMERIC keys whose encoded form exceeds the
// previous 4-byte field. Layout, in little-endian:
//
//	offset 0  Prev        (4 bytes)
//	offset 4  Next        (4 bytes)
//	offset 8  Level       (4 bytes)
//	offset 12 Flags       (2 bytes)
//	offset 14 HighKeyLen  (2 bytes)
//	offset 16 HighKey     (32 bytes; bytes 0..HighKeyLen valid)
const SizeOfBTPageOpaque = 48

// MaxHighKeyLen bounds the on-disk HighKey field. Covers the int4
// encoding (4 bytes) and the NUMERIC encoding (≤25 bytes per
// 0011-0001) with headroom. M0041-0004 widened EncodeNumericKey to
// accept *big.Int mantissas, but the only B-tree NUMERIC keys in
// practice are stored column values (TPC-H l_partkey, ps_partkey,
// etc.) that fit in int64 and therefore in ≤25 bytes; the
// arbitrary-precision lane only matters for runtime arithmetic
// results that never enter an index. So MaxHighKeyLen stays 32 to
// preserve on-disk page-format compatibility.
const MaxHighKeyLen = 32

// btSpecialOffset is where the opaque area starts on every B-tree page.
const btSpecialOffset = storage.BlockSize - SizeOfBTPageOpaque

// PageFlag values for btpo_flags.
const (
	BTLeaf       uint16 = 0x0001
	BTRoot       uint16 = 0x0002
	BTDeleted    uint16 = 0x0004
	BTHasHighKey uint16 = 0x0008
	// BTIncompleteSplit (M0055-0004 Phase C) marks a page as the
	// LEFT half of a freshly-completed split whose parent
	// downlink has not yet been inserted. Writers descending to
	// such a page MUST run `(*BTree).finishSplit` before
	// inserting further. Cleared by finishSplit after the parent
	// downlink lands.
	BTIncompleteSplit uint16 = 0x0010
	// BTHalfDead (M0055-0005-followup-two-phase-del) marks a
	// page as the target of a two-phase deletion whose Phase 1
	// (mark + WAL) has completed but whose Phase 2 (unlink +
	// recycle) has not. Vacuum and writer paths that encounter
	// a half-dead page MUST run `(*BTree).completeDeletion` to
	// finish Phase 2 before treating the page as live or
	// recycled. Crash-replay restores the half-dead state from
	// the Phase 1 WAL record.
	BTHalfDead uint16 = 0x0020
)

// MetaBlock is always block 0 — the metapage. RootStart is the first
// block holding actual key data; on a freshly created tree it is also
// block 1 (root + leaf).
const (
	MetaBlock storage.BlockNumber = 0
	rootStart storage.BlockNumber = 1

	btreeMagic   uint32 = 0x053162
	btreeVersion uint32 = 3 // bumped by M0011-0002 (variable-length HighKey, 48-byte opaque)
)

// BTreeMeta is the v0 metapage payload.
type BTreeMeta struct {
	Magic     uint32
	Version   uint32
	Root      storage.BlockNumber
	Level     uint32
	FastRoot  storage.BlockNumber
	FastLevel uint32
}

// BTPageOpaque is the typed view over the special area at the end of
// each B-tree page. HighKey is meaningful only when BTHasHighKey is
// set in Flags; from v3 onwards it carries a variable-length key
// bounded by MaxHighKeyLen so NUMERIC and other variable-width
// encodings fit (M0011-0002).
type BTPageOpaque struct {
	Prev    storage.BlockNumber
	Next    storage.BlockNumber
	Level   uint32
	Flags   uint16
	HighKey []byte
}

// IsLeaf reports whether the page carries leaf items.
func (o BTPageOpaque) IsLeaf() bool { return o.Flags&BTLeaf != 0 }

// HasIncompleteSplit reports whether this page is the left half
// of a split that has not yet propagated its parent downlink.
// (M0055-0004 Phase C.)
func (o BTPageOpaque) HasIncompleteSplit() bool { return o.Flags&BTIncompleteSplit != 0 }

// IsHalfDead reports whether this page is mid-deletion — Phase 1
// has marked it but Phase 2 has not unlinked it yet.
// (M0055-0005-followup-two-phase-del.)
func (o BTPageOpaque) IsHalfDead() bool { return o.Flags&BTHalfDead != 0 }

// IsRoot reports whether the page is the current root.
func (o BTPageOpaque) IsRoot() bool { return o.Flags&BTRoot != 0 }

// HasHighKey reports whether this page advertises a high-key
// boundary. Pages without a high key are rightmost on their level
// (or freshly created) and cover all remaining keys.
func (o BTPageOpaque) HasHighKey() bool { return o.Flags&BTHasHighKey != 0 }

// readOpaque returns the parsed opaque from page bytes.
func readOpaque(p storage.Page) BTPageOpaque {
	off := btSpecialOffset
	o := BTPageOpaque{
		Prev:  storage.BlockNumber(binary.LittleEndian.Uint32(p[off : off+4])),
		Next:  storage.BlockNumber(binary.LittleEndian.Uint32(p[off+4 : off+8])),
		Level: binary.LittleEndian.Uint32(p[off+8 : off+12]),
		Flags: binary.LittleEndian.Uint16(p[off+12 : off+14]),
	}
	hkLen := int(binary.LittleEndian.Uint16(p[off+14 : off+16]))
	if hkLen > MaxHighKeyLen {
		hkLen = MaxHighKeyLen // defensive: truncate corrupt length to bounded slice
	}
	if hkLen > 0 {
		o.HighKey = append([]byte(nil), p[off+16:off+16+hkLen]...)
	}
	return o
}

// writeOpaque persists the opaque into page bytes. Panics if HighKey
// exceeds MaxHighKeyLen — by construction (int4=4, NUMERIC≤25) this
// can only fire for a future encoding that grew past the format
// budget.
func writeOpaque(p storage.Page, o BTPageOpaque) {
	if len(o.HighKey) > MaxHighKeyLen {
		panic(fmt.Sprintf("btree: HighKey length %d exceeds MaxHighKeyLen %d", len(o.HighKey), MaxHighKeyLen))
	}
	off := btSpecialOffset
	binary.LittleEndian.PutUint32(p[off:off+4], uint32(o.Prev))
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(o.Next))
	binary.LittleEndian.PutUint32(p[off+8:off+12], o.Level)
	binary.LittleEndian.PutUint16(p[off+12:off+14], o.Flags)
	binary.LittleEndian.PutUint16(p[off+14:off+16], uint16(len(o.HighKey)))
	// Zero the HighKey region first so leftover bytes from a longer
	// previous HighKey don't survive a shorter rewrite.
	for i := 0; i < MaxHighKeyLen; i++ {
		p[off+16+i] = 0
	}
	copy(p[off+16:off+16+len(o.HighKey)], o.HighKey)
}

// keyExceedsHighKey reports whether `key` is strictly greater than
// `op.HighKey` — i.e., the search has overshot this page and must
// follow op.Next under right-link recovery.
func keyExceedsHighKey(op BTPageOpaque, key []byte) bool {
	if !op.HasHighKey() || op.Next == storage.InvalidBlockNumber {
		return false
	}
	return CompareKeys(key, op.HighKey) > 0
}

// initPage prepares a freshly extended block as a B-tree page with the
// given opaque content. The page starts empty (no items).
func initPage(p storage.Page, o BTPageOpaque) {
	if err := storage.InitPage(p); err != nil {
		panic(err)
	}
	h := storage.MustHeader(p)
	h.SetSpecial(uint16(btSpecialOffset))
	h.SetUpper(uint16(btSpecialOffset))
	writeOpaque(p, o)
}

// item is one B-tree item: a key plus the heap or child pointer.
//
// On internal pages the pointer's Block is the child page; on leaf
// pages the pointer is the heap row (block, line-pointer-slot).
type item struct {
	keyLen uint16
	ptr    storage.ItemPointer
	key    []byte
}

// itemPrefixSize is the fixed bytes before key_bytes.
const itemPrefixSize = 2 + 4 + 2

func (it item) marshal() []byte {
	out := make([]byte, itemPrefixSize+len(it.key))
	binary.LittleEndian.PutUint16(out[0:2], it.keyLen)
	binary.LittleEndian.PutUint32(out[2:6], uint32(it.ptr.Block))
	binary.LittleEndian.PutUint16(out[6:8], it.ptr.Offset)
	copy(out[itemPrefixSize:], it.key)
	return out
}

func parseItem(raw []byte) (item, error) {
	if len(raw) < itemPrefixSize {
		return item{}, fmt.Errorf("btree: item too short (%d bytes)", len(raw))
	}
	keyLen := binary.LittleEndian.Uint16(raw[0:2])
	if int(keyLen)+itemPrefixSize != len(raw) {
		return item{}, fmt.Errorf("btree: item length mismatch keyLen=%d total=%d", keyLen, len(raw))
	}
	return item{
		keyLen: keyLen,
		ptr: storage.ItemPointer{
			Block:  storage.BlockNumber(binary.LittleEndian.Uint32(raw[2:6])),
			Offset: binary.LittleEndian.Uint16(raw[6:8]),
		},
		key: append([]byte(nil), raw[itemPrefixSize:]...),
	}, nil
}

// EncodeInt4 is the canonical key encoding for v0 (4-byte big-endian int32).
// Big-endian preserves numeric order as bytewise lexicographic order.
func EncodeInt4(key int32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(key)^0x80000000)
	return b[:]
}

// DecodeInt4 inverts EncodeInt4.
func DecodeInt4(b []byte) (int32, error) {
	if len(b) != 4 {
		return 0, fmt.Errorf("btree: int4 key must be 4 bytes, got %d", len(b))
	}
	return int32(binary.BigEndian.Uint32(b) ^ 0x80000000), nil
}

// EncodeInt8 encodes an int64 into a sortable byte representation.
// Uses big-endian with the sign bit flipped so that negative values
// sort before positive values, matching the same approach as EncodeInt4.
func EncodeInt8(key int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(key)^0x8000000000000000)
	return b[:]
}

// DecodeInt8 inverts EncodeInt8.
func DecodeInt8(b []byte) (int64, error) {
	if len(b) != 8 {
		return 0, fmt.Errorf("btree: int8 key must be 8 bytes, got %d", len(b))
	}
	return int64(binary.BigEndian.Uint64(b) ^ 0x8000000000000000), nil
}

// EncodeNumericKey encodes a NUMERIC value (mantissa * 10^(-scale)) into
// a sortable byte string such that bytewise comparison matches numeric
// order. The encoding is scale-invariant: numerically equal inputs
// (e.g. (10,1) and (100,2), both representing 1.0) produce identical
// bytes — required for UNIQUE/PRIMARY KEY equality on NUMERIC.
//
// Layout, semantics, and worked examples are documented in
// docs/design/0011-0001-btree-numeric-key-ordering.md. Briefly:
//
//	zero          -> [0x01]
//	non-zero      -> sign(1) || biased exp(4 BE) || digits || terminator(1)
//	positive sign -> 0x02; exp = E + 0x80000000;       digits = '0'+d;       term = 0x00
//	negative sign -> 0x00; exp = 0x7FFFFFFF - E;       digits = '0'+(9-d);   term = 0xFF
//
// Where d_1.d_2…d_n × 10^E is the value's scientific normalisation
// after stripping trailing zeros from the mantissa.
//
// Decoding is intentionally not provided: the B-tree never inverts the
// encoding; index probes always re-encode from the live datum.
//
// M0041-0004 widens the mantissa parameter from int64 to *big.Int so
// arbitrary-precision NUMERIC values (e.g. results of TPC-H Q8's
// `1.00000000000000000000` produced by upstream-compatible division)
// can be indexed without overflow. The on-page byte layout is
// unchanged — sort order is preserved by the variable-length
// digit-rebase trick, and any value that fits in int64 produces the
// same bytes as before the widening.
func EncodeNumericKey(mantissa *big.Int, scale int16) []byte {
	if mantissa.Sign() == 0 {
		return []byte{0x01}
	}
	negative := mantissa.Sign() < 0
	abs := new(big.Int).Abs(mantissa)
	// Strip trailing zeros so numerically-equal values normalise to
	// the same digit string.
	s := int32(scale)
	ten := big.NewInt(10)
	zero := big.NewInt(0)
	rem := new(big.Int)
	for {
		new(big.Int).QuoRem(abs, ten, rem)
		if rem.Cmp(zero) != 0 {
			break
		}
		abs.Quo(abs, ten)
		s--
		if abs.Sign() == 0 {
			break
		}
	}
	digits := abs.Text(10)
	ndig := int32(len(digits))
	E := ndig - 1 - s

	out := make([]byte, 0, 6+int(ndig))
	var b [4]byte
	if negative {
		out = append(out, 0x00)
		// Inverted bias so a larger E (more negative value) sorts smaller.
		binary.BigEndian.PutUint32(b[:], uint32(int32(0x7FFFFFFF)-E))
		out = append(out, b[:]...)
		for i := 0; i < int(ndig); i++ {
			d := digits[i] - '0'
			out = append(out, '0'+(9-d))
		}
		out = append(out, 0xFF) // greater than any digit byte; longer sorts first
	} else {
		out = append(out, 0x02)
		// Standard bias so a larger E sorts larger.
		binary.BigEndian.PutUint32(b[:], uint32(E)+0x80000000)
		out = append(out, b[:]...)
		for i := 0; i < int(ndig); i++ {
			out = append(out, digits[i])
		}
		out = append(out, 0x00) // less than any digit byte; shorter sorts first
	}
	return out
}

// EncodeVarchar encodes a variable-length string as a self-terminating
// byte sequence whose bytewise lexicographic order matches the C-locale
// (byte-wise) SQL ordering of the original strings. Suitable as a
// B-tree key component in both single-column and composite indexes.
//
// Encoding rules:
//   - 0x01 is the escape introducer.
//   - Each 0x00 byte in the payload is replaced by [0x01, 0x01].
//   - Each 0x01 byte in the payload is replaced by [0x01, 0x02].
//   - All other bytes are passed through unchanged.
//   - A single 0x00 byte is appended as the end-of-key terminator.
//
// This ensures 0x00 appears only as the terminator (never inside the
// payload after escaping), making the encoding self-terminating:
// concatenating two EncodeVarchar results in a composite key whose
// bytewise comparison correctly implements multi-column SQL ordering.
//
// Bytewise order of encoded strings matches bytewise (C-locale) order
// of the original inputs. Decoding is intentionally not provided:
// B-tree probe paths always re-encode from the live Datum.
func EncodeVarchar(payload []byte) []byte {
	out := make([]byte, 0, len(payload)+1)
	for _, b := range payload {
		switch b {
		case 0x00:
			out = append(out, 0x01, 0x01)
		case 0x01:
			out = append(out, 0x01, 0x02)
		default:
			out = append(out, b)
		}
	}
	out = append(out, 0x00)
	return out
}

// EncodeChar encodes a CHAR(N) (blank-padded character) value as a
// sortable, self-terminating byte sequence matching PostgreSQL's
// blank-padded comparison semantics.
//
// Trailing 0x20 (space) bytes are stripped before encoding, so
// 'A' and 'A         ' (any number of trailing spaces) produce
// identical bytes — required for index correctness when goopg stores
// the declared-length-padded form in the heap but the query probes
// with an unpadded literal.
//
// After trimming, the encoding is identical to EncodeVarchar.
func EncodeChar(payload []byte) []byte {
	return EncodeVarchar(bytes.TrimRight(payload, " "))
}

// EncodeTimestamp encodes a timestamp value as a sortable 8-byte
// sequence whose bytewise comparison matches chronological order.
//
// The input is the number of microseconds since the PostgreSQL epoch
// (2000-01-01 00:00:00 UTC); negative values are valid and represent
// timestamps before the epoch.
//
// The layout is identical to EncodeInt8: 8-byte big-endian with the
// sign bit flipped so negative values (pre-epoch timestamps) sort
// before positive values (post-epoch timestamps) under bytewise
// comparison.
func EncodeTimestamp(microsSince2000 int64) []byte {
	return EncodeInt8(microsSince2000)
}

// DecodeTimestamp inverts EncodeTimestamp (identical to DecodeInt8).
func DecodeTimestamp(b []byte) (int64, error) { return DecodeInt8(b) }

// DecodeVarchar inverts EncodeVarchar: strips the 0x00 terminator and
// unescapes 0x01-prefixed bytes. Used by index-only scan (M0046-0004)
// to reconstruct string values from B-tree key bytes without a heap fetch.
func DecodeVarchar(b []byte) ([]byte, error) {
	if len(b) == 0 || b[len(b)-1] != 0x00 {
		return nil, fmt.Errorf("btree: varchar key missing 0x00 terminator")
	}
	out := make([]byte, 0, len(b)-1)
	src := b[:len(b)-1] // strip terminator
	for i := 0; i < len(src); {
		c := src[i]
		if c == 0x01 {
			if i+1 >= len(src) {
				return nil, fmt.Errorf("btree: varchar key: truncated escape at byte %d", i)
			}
			switch src[i+1] {
			case 0x01:
				out = append(out, 0x00)
			case 0x02:
				out = append(out, 0x01)
			default:
				return nil, fmt.Errorf("btree: varchar key: invalid escape %02x at byte %d", src[i+1], i)
			}
			i += 2
		} else {
			out = append(out, c)
			i++
		}
	}
	return out, nil
}

// CompareKeys is straight bytewise lexicographic comparison.
//
// For int4 keys (all 4 bytes) this matches numeric order by
// construction of EncodeInt4. For NUMERIC keys (variable length)
// it matches numeric order via the sign + biased-exponent + digits
// + terminator layout produced by EncodeNumericKey — see
// docs/design/0011-0001-btree-numeric-key-ordering.md for why
// straight lex compare (not length-first) is the right contract
// for variable-length keys.
func CompareKeys(a, b []byte) int {
	return bytes.Compare(a, b)
}

// BTree is the access-method handle for one index relation.
//
// Concurrency (Landing 2 of milestone 0002 — see
// docs/design/0002-0002-btree-concurrency.md):
//
//   - Readers (Search, RangeScan) take no tree-wide lock; they
//     synchronise through per-page Slot.RLock() latches and use
//     Lehman-Yao right-link recovery to recover from concurrent
//     splits.
//   - Inserts take one of two paths:
//   - fast path: non-splitting leaf inserts run under the target
//     page's Slot.Lock only, so writers on different pages proceed
//     in parallel.
//   - split path: page splits (and their parent/root/meta updates)
//     are serialised through splitMu to keep tree-structure changes
//     atomic while Landing 3's full writer-coupling protocol is
//     still staged.
type BTree struct {
	pool     *storage.Pool
	rel      storage.RelFileNode
	logSplit LogSplitFunc

	splitMu sync.Mutex

	// M0055-0001: write-path counters used by the baseline
	// harness and Phase A/B regression tests. All atomic so they
	// can be read concurrently without locking. Reset by
	// `(*BTree).ResetStats`.
	stats BTreeStats

	// M0055-0002-followup-rightmost-cache: cached rightmost leaf
	// pointer for append-shaped workloads. Written by the insert
	// path's slow-path descent; read by the fast path to skip
	// re-descent when the next insert's key is ≥ the highest key
	// observed at the cache's leaf. atomic so concurrent readers
	// don't tear the value. Zero (BlockNumber 0) means "uncached"
	// — block 0 is the metapage so no real leaf has block 0.
	rightmostLeafBlk atomic.Uint64

	// M0055-0005 Phase D: recycled-page free list. Pages that
	// were unlinked by vacuum get pushed here; future
	// allocations (PinNew calls) check this list first before
	// extending the file. Guarded by `freeListMu` to keep the
	// pop/push atomic.
	freeListMu sync.Mutex
	freeList   []storage.BlockNumber
}

// pinNewOrRecycled (M0055-0005 Phase D) returns a writable slot
// for a fresh page allocation, preferring a recycled block from
// the free list before extending the file. The page bytes are
// re-initialised so the caller sees a clean page (matching the
// post-PinNew contract).
func (bt *BTree) pinNewOrRecycled() (*storage.Slot, storage.BlockNumber, error) {
	if blk, ok := bt.popRecycledBlock(); ok {
		slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
		if err != nil {
			// Could not re-pin; fall back to fresh allocation.
			return bt.pool.PinNew(bt.rel)
		}
		// Re-initialise the page bytes so the recycled slot looks
		// like a fresh PinNew result.
		page := slot.Page()
		for i := range page {
			page[i] = 0
		}
		// Caller will write opaque/header before MarkDirty.
		return slot, blk, nil
	}
	return bt.pool.PinNew(bt.rel)
}

// recycleBlock (M0055-0005 Phase D) marks a block as available
// for reuse by future page allocations on this tree. The block
// must have been unlinked from the tree's logical structure
// before it lands here. Safe for concurrent callers.
func (bt *BTree) recycleBlock(blk storage.BlockNumber) {
	bt.freeListMu.Lock()
	bt.freeList = append(bt.freeList, blk)
	bt.freeListMu.Unlock()
}

// popRecycledBlock (M0055-0005 Phase D) returns a recycled
// block number if one is available, or (0, false) when the free
// list is empty. The caller is responsible for re-initialising
// the block's page bytes before reuse.
func (bt *BTree) popRecycledBlock() (storage.BlockNumber, bool) {
	bt.freeListMu.Lock()
	defer bt.freeListMu.Unlock()
	if len(bt.freeList) == 0 {
		return 0, false
	}
	n := len(bt.freeList) - 1
	blk := bt.freeList[n]
	bt.freeList = bt.freeList[:n]
	return blk, true
}

// RecycledPageCount returns the number of pages currently in the
// recycle free list. Used by tests and the M0055-0007 report.
func (bt *BTree) RecycledPageCount() int {
	bt.freeListMu.Lock()
	defer bt.freeListMu.Unlock()
	return len(bt.freeList)
}

// BTreeStats are the per-tree counters surfaced by `(*BTree).Stats`
// for benchmarks and regression tests. The counters are best-effort
// (incremented from the steady-state insert/split path) and are
// cleared by `(*BTree).ResetStats`. (M0055-0001.)
type BTreeStats struct {
	Inserts uint64 // total `Insert` calls
	Splits  uint64 // total leaf+internal page splits
}

// Stats returns a snapshot of the BTree's write-path counters.
// Snapshot is best-effort — concurrent inserts may make the
// returned numbers stale by the time the caller reads them.
func (bt *BTree) Stats() BTreeStats {
	return BTreeStats{
		Inserts: atomic.LoadUint64(&bt.stats.Inserts),
		Splits:  atomic.LoadUint64(&bt.stats.Splits),
	}
}

// ResetStats clears the BTree's write-path counters.
func (bt *BTree) ResetStats() {
	atomic.StoreUint64(&bt.stats.Inserts, 0)
	atomic.StoreUint64(&bt.stats.Splits, 0)
}

// LogSplitFunc emits the atomic page-split WAL record described in
// docs/design/0002-0002-btree-concurrency.md Landing 3a and
// returns the record's end LSN. nil means "no WAL writer wired";
// the btree falls back to the per-page FPI emitted by
// Pool.MarkDirty, which is correct under the limited
// crash-consistency contract (split atomicity is best-effort
// without this hook).
type LogSplitFunc func(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page) (storage.LSN, error)

// Options carries optional dependencies for Open/Create. The zero
// value works for tests and callers that don't need WAL-backed
// split atomicity.
type Options struct {
	// LogSplit, when non-nil, is invoked on every page split to
	// emit one atomic BtreeSplit WAL record covering both pages.
	LogSplit LogSplitFunc
}

// Open returns a handle to an existing B-tree on rel. Validates the
// metapage; returns ErrNotABTree if the magic doesn't match.
//
// The split-WAL hook (Landing 3a) is pulled from
// `pool.LogBtreeSplit()`. Callers that need to override (tests,
// future independent WAL streams) can use OpenWithOptions.
func Open(pool *storage.Pool, rel storage.RelFileNode) (*BTree, error) {
	return OpenWithOptions(pool, rel, Options{LogSplit: adaptPoolLogSplit(pool)})
}

// OpenWithOptions is the wired-up Open variant.
func OpenWithOptions(pool *storage.Pool, rel storage.RelFileNode, opts Options) (*BTree, error) {
	bt := &BTree{pool: pool, rel: rel, logSplit: opts.LogSplit}
	meta, err := bt.readMeta()
	if err != nil {
		return nil, err
	}
	if meta.Magic != btreeMagic || meta.Version != btreeVersion {
		return nil, ErrNotABTree
	}
	return bt, nil
}

// Create initializes a new B-tree on rel. The relation must be empty.
// Allocates the metapage and the initial leaf-as-root page.
func Create(pool *storage.Pool, rel storage.RelFileNode) (*BTree, error) {
	return CreateWithOptions(pool, rel, Options{LogSplit: adaptPoolLogSplit(pool)})
}

// adaptPoolLogSplit returns the pool's split-WAL hook in btree's
// LogSplitFunc shape, or nil when no hook is wired (tests etc.).
func adaptPoolLogSplit(pool *storage.Pool) LogSplitFunc {
	hook := pool.LogBtreeSplit()
	if hook == nil {
		return nil
	}
	return func(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page) (storage.LSN, error) {
		return hook(rel, leftBlk, rightBlk, leftPage, rightPage)
	}
}

// CreateWithOptions is the wired-up Create variant.
func CreateWithOptions(pool *storage.Pool, rel storage.RelFileNode, opts Options) (*BTree, error) {
	bt := &BTree{pool: pool, rel: rel, logSplit: opts.LogSplit}

	// Block 0: metapage.
	metaSlot, metaBlk, err := pool.PinNew(rel)
	if err != nil {
		return nil, err
	}
	if metaBlk != MetaBlock {
		pool.Unpin(metaSlot)
		return nil, fmt.Errorf("btree: expected meta at block 0, got %d", metaBlk)
	}

	// Block 1: root, also the only leaf.
	rootSlot, rootBlk, err := pool.PinNew(rel)
	if err != nil {
		pool.Unpin(metaSlot)
		return nil, err
	}
	if rootBlk != rootStart {
		pool.Unpin(metaSlot)
		pool.Unpin(rootSlot)
		return nil, fmt.Errorf("btree: expected root at block 1, got %d", rootBlk)
	}

	// Initialise the root page as a leaf.
	rootSlot.Lock()
	initPage(rootSlot.Page(), BTPageOpaque{
		Prev:  storage.InvalidBlockNumber,
		Next:  storage.InvalidBlockNumber,
		Level: 0,
		Flags: BTLeaf | BTRoot,
	})
	if err := bt.markDirtyWithPageRecord(rootSlot, rootBlk); err != nil {
		rootSlot.Unlock()
		pool.Unpin(rootSlot)
		pool.Unpin(metaSlot)
		return nil, err
	}
	rootSlot.Unlock()

	// Initialise the metapage.
	metaSlot.Lock()
	storage.InitPage(metaSlot.Page())
	writeMeta(metaSlot.Page(), BTreeMeta{
		Magic:     btreeMagic,
		Version:   btreeVersion,
		Root:      rootBlk,
		Level:     0,
		FastRoot:  rootBlk,
		FastLevel: 0,
	})
	if err := bt.markDirtyWithPageRecord(metaSlot, MetaBlock); err != nil {
		metaSlot.Unlock()
		pool.Unpin(rootSlot)
		pool.Unpin(metaSlot)
		return nil, err
	}
	metaSlot.Unlock()

	pool.Unpin(rootSlot)
	pool.Unpin(metaSlot)

	return bt, nil
}

// Errors.
var (
	ErrNotABTree = errors.New("btree: not a B-tree relation")
)

// readMeta loads the metapage under a shared content latch so the
// read sees a torn-byte-free snapshot.
func (bt *BTree) readMeta() (BTreeMeta, error) {
	slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: MetaBlock})
	if err != nil {
		return BTreeMeta{}, err
	}
	slot.RLock()
	m := parseMeta(slot.Page())
	slot.RUnlock()
	bt.pool.Unpin(slot)
	return m, nil
}

// pinR pins a buffer and acquires its shared content latch. Callers
// release it with unpinR. Used on every read of internal/leaf pages
// during descent and scans.
func (bt *BTree) pinR(blk storage.BlockNumber) (*storage.Slot, error) {
	s, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
	if err != nil {
		return nil, err
	}
	s.RLock()
	return s, nil
}

func (bt *BTree) unpinR(s *storage.Slot) {
	s.RUnlock()
	bt.pool.Unpin(s)
}

// pinW pins a buffer and acquires its exclusive content latch.
// Used at every mutation site so concurrent readers (which take
// the shared latch) see a coherent page image.
func (bt *BTree) pinW(blk storage.BlockNumber) (*storage.Slot, error) {
	s, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
	if err != nil {
		return nil, err
	}
	s.Lock()
	return s, nil
}

func (bt *BTree) unpinW(s *storage.Slot) {
	s.Unlock()
	bt.pool.Unpin(s)
}

func parseMeta(p storage.Page) BTreeMeta {
	off := storage.SizeOfPageHeaderData
	return BTreeMeta{
		Magic:     binary.LittleEndian.Uint32(p[off : off+4]),
		Version:   binary.LittleEndian.Uint32(p[off+4 : off+8]),
		Root:      storage.BlockNumber(binary.LittleEndian.Uint32(p[off+8 : off+12])),
		Level:     binary.LittleEndian.Uint32(p[off+12 : off+16]),
		FastRoot:  storage.BlockNumber(binary.LittleEndian.Uint32(p[off+16 : off+20])),
		FastLevel: binary.LittleEndian.Uint32(p[off+20 : off+24]),
	}
}

func writeMeta(p storage.Page, m BTreeMeta) {
	off := storage.SizeOfPageHeaderData
	binary.LittleEndian.PutUint32(p[off:off+4], m.Magic)
	binary.LittleEndian.PutUint32(p[off+4:off+8], m.Version)
	binary.LittleEndian.PutUint32(p[off+8:off+12], uint32(m.Root))
	binary.LittleEndian.PutUint32(p[off+12:off+16], m.Level)
	binary.LittleEndian.PutUint32(p[off+16:off+20], uint32(m.FastRoot))
	binary.LittleEndian.PutUint32(p[off+20:off+24], m.FastLevel)
	// Bump pd_lower so PageLinePointerCount on this page (if anyone
	// ever asks) reports zero rather than walking into our payload.
	h := storage.MustHeader(p)
	h.SetLower(uint16(off + 24))
}

// markDirtyWithPageRecord routes metadata-page mutations through
// MarkDirtyChangeRecord when a page-image WAL hook is available.
// This keeps metapage/root-flag/root-create updates crash-safe once
// MarkDirty returns to once-per-checkpoint FPI behaviour.
func (bt *BTree) markDirtyWithPageRecord(slot *storage.Slot, blk storage.BlockNumber) error {
	if logPage := bt.pool.LogPageImage(); logPage != nil {
		return bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
			pageCopy := make(storage.Page, storage.BlockSize)
			copy(pageCopy, slot.Page())
			return logPage(bt.rel, blk, pageCopy)
		})
	}
	bt.pool.MarkDirty(slot)
	return nil
}

// updateRootMeta rewrites the metapage root pointer + level. Caller
// must hold bt.splitMu (structural writer serialisation).
func (bt *BTree) updateRootMeta(root storage.BlockNumber, level uint32) error {
	slot, err := bt.pinW(MetaBlock)
	if err != nil {
		return err
	}
	m := parseMeta(slot.Page())
	m.Root = root
	m.Level = level
	m.FastRoot = root
	m.FastLevel = level
	writeMeta(slot.Page(), m)
	err = bt.markDirtyWithPageRecord(slot, MetaBlock)
	bt.unpinW(slot)
	if err != nil {
		return err
	}
	return nil
}

// pageItems lists every item on a page in slot order.
func pageItems(p storage.Page) ([]item, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return nil, err
	}
	out := make([]item, 0, count)
	for slot := uint16(1); slot <= uint16(count); slot++ {
		raw, err := storage.PageGetItemRaw(p, slot)
		if err != nil {
			return nil, err
		}
		// Posting-list items (M0047-0003) are expanded to individual
		// (key, TID) pairs so callers like insertItemSorted work correctly.
		if isPostingRaw(raw) {
			key, tids, perr := parsePostingRaw(raw)
			if perr != nil {
				return nil, perr
			}
			for _, tid := range tids {
				out = append(out, item{keyLen: uint16(len(key)), ptr: tid, key: key})
			}
		} else {
			it, perr := parseItem(raw)
			if perr != nil {
				return nil, perr
			}
			out = append(out, it)
		}
	}
	return out, nil
}

// findChildBlock returns the block number of the child to descend into
// for `key` from the items on an internal page. Items are sorted by key;
// the entry to descend into is the rightmost item whose key ≤ search
// key. By v0 convention the leftmost internal item carries the empty
// key (lowest possible), so the search is well-defined for the first
// child too.
func findChildBlock(items []item, key []byte) storage.BlockNumber {
	idx := sort.Search(len(items), func(i int) bool {
		return CompareKeys(items[i].key, key) > 0
	})
	if idx == 0 {
		// All items are > key; descend through the first item anyway
		// (for v0 the leftmost item has empty key, making this a
		// theoretical-only branch; we still pick items[0] so an
		// off-by-one doesn't crash).
		return items[0].ptr.Block
	}
	return items[idx-1].ptr.Block
}

// findChildBlockDirect binary-searches an internal B-tree page for the
// child block to descend into for `key`, without decoding all items
// first.  Replaces the pageItems + findChildBlock pair (which allocated
// a slice of all items on every descent).  M0027-0001.
func findChildBlockDirect(p storage.Page, key []byte) (storage.BlockNumber, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, fmt.Errorf("btree: empty internal page")
	}
	// Binary search across line pointers.
	n := count
	idx := sort.Search(n, func(i int) bool {
		raw, err := storage.PageGetItemRaw(p, uint16(i+1))
		if err != nil {
			return true // will surface at the final error check
		}
		it, err := parseItem(raw)
		if err != nil {
			return true
		}
		return CompareKeys(it.key, key) > 0
	})
	// sort.Search returns [0, n]; idx==n means all items ≤ key.
	if idx >= n {
		idx = n - 1
	} else if idx > 0 {
		// idx is the first item with key > search_key;
		// child is the preceding item (last one ≤ key).
		idx--
	}
	// idx==0 stays 0: first child.
	raw, err := storage.PageGetItemRaw(p, uint16(idx+1))
	if err != nil {
		return 0, err
	}
	it, err := parseItem(raw)
	if err != nil {
		return 0, err
	}
	return it.ptr.Block, nil
}

// descendToLeaf walks from the current root to the leaf containing
// `key`. Each page is read under a shared content latch, and the
// classic Lehman-Yao right-link recovery handles the case where a
// concurrent writer split the page after we picked it: if the
// search key exceeds the page's high key, we follow op.Next and
// retry on the right sibling.
//
// The returned `path` is the chain of internal pages walked
// (root..parent-of-leaf), used by writers to propagate separator
// items up after a split. Readers ignore it.
func (bt *BTree) descendToLeaf(key []byte) (leafBlk storage.BlockNumber, path []storage.BlockNumber, err error) {
	meta, err := bt.readMeta()
	if err != nil {
		return 0, nil, err
	}
	cur := meta.Root
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return 0, nil, err
		}
		op := readOpaque(slot.Page())

		// Right-link recovery: a concurrent split may have moved
		// our target keys to the right sibling. Follow op.Next
		// until we land on a page that covers the search key.
		if keyExceedsHighKey(op, key) {
			next := op.Next
			bt.unpinR(slot)
			cur = next
			continue
		}

		if op.IsLeaf() {
			// M0055-0002-followup-rightmost-cache: if this leaf
			// has no Next pointer it IS the rightmost; refresh
			// the cache. Cheap atomic write, no allocation.
			if op.Next == 0 {
				bt.rightmostLeafBlk.Store(uint64(cur))
			}
			incomplete := op.HasIncompleteSplit()
			bt.unpinR(slot)
			// M0055-0004-followup-finish-split: if the leaf
			// retains a stale incomplete-split marker (crash
			// replay scenario), complete the parent-downlink
			// insert before treating the leaf as live. The
			// completion is idempotent.
			if incomplete {
				if err := bt.finishSplit(cur); err != nil {
					return 0, nil, err
				}
			}
			return cur, path, nil
		}

		// Binary-search the internal page directly, without decoding
		// all items (avoids allocation & linear decode — M0027-0001).
		child, err := findChildBlockDirect(slot.Page(), key)
		bt.unpinR(slot)
		if err != nil {
			return 0, nil, err
		}
		path = append(path, cur)
		cur = child
	}
}

// Insert places (key, ptr) into the leaf where it belongs, splitting
// pages on the way up if needed.
func (bt *BTree) Insert(key []byte, ptr storage.ItemPointer) error {
	atomic.AddUint64(&bt.stats.Inserts, 1) // M0055-0001
	it := item{
		keyLen: uint16(len(key)),
		ptr:    ptr,
		key:    append([]byte(nil), key...),
	}

	// Fast path: no split required. Writers touching different leaves
	// only contend on those page latches, not on a tree-wide mutex.
	if err := bt.tryInsertNoSplit(it); err == nil {
		return nil
	} else if !errors.Is(err, errNeedsSplit) {
		return err
	}

	// Split path: serialise structure changes (split propagation,
	// root lift, metapage rewrite), then retry from a fresh descent.
	atomic.AddUint64(&bt.stats.Splits, 1) // M0055-0001 — counts insert calls that take the split-path retry.
	bt.splitMu.Lock()
	defer bt.splitMu.Unlock()

	leafBlk, path, err := bt.descendToLeaf(key)
	if err != nil {
		return err
	}
	return bt.insertIntoBlock(leafBlk, path, it)
}

var errNeedsSplit = errors.New("btree: insert needs split")

func (bt *BTree) tryInsertNoSplit(it item) error {
	// M0055-0002-followup-rightmost-cache: try the cached
	// rightmost leaf first. When the new key is ≥ the leaf's
	// highest key (which is true for monotonic / append-shaped
	// inserts), we skip the descent entirely. On a cache miss
	// (key falls before the cached leaf's max, or the leaf was
	// invalidated by a split that bumped Next), fall through to
	// the slow descent path.
	if cached := bt.rightmostLeafBlk.Load(); cached != 0 {
		if ok, err := bt.tryInsertOnCachedRightmost(storage.BlockNumber(cached), it); ok || err != nil {
			return err
		}
	}
	leafBlk, _, err := bt.descendToLeaf(it.key)
	if err != nil {
		return err
	}
	slot, err := bt.pinW(leafBlk)
	if err != nil {
		return err
	}
	defer bt.unpinW(slot)

	op := readOpaque(slot.Page())
	if keyExceedsHighKey(op, it.key) {
		return errNeedsSplit
	}
	if !pageHasSpaceFor(slot.Page(), it) {
		return errNeedsSplit
	}

	insertItemSorted(slot.Page(), it)
	if logIns := bt.pool.LogBtreeInsert(); logIns != nil {
		itemBytes := it.marshal()
		return bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
			return logIns(bt.rel, leafBlk, itemBytes)
		})
	}
	bt.pool.MarkDirty(slot)
	return nil
}

// insertIntoBlock inserts `it` into the page at blk. If the page
// lacks space, splits it; the resulting separator item is
// recursively inserted into the parent (or a new root is
// created).
//
// All page mutations happen under the per-page exclusive content
// latch so concurrent readers (Search/RangeScan) see either the
// pre-split or post-split state, never an in-between snapshot.
// The split sequence stamps a high key on the left page BEFORE
// dropping its latch — readers that descended to it under shared
// latch will follow the new right-link to find the moved keys.
func (bt *BTree) insertIntoBlock(blk storage.BlockNumber, path []storage.BlockNumber, it item) error {
	slot, err := bt.pinW(blk)
	if err != nil {
		return err
	}

	if pageHasSpaceFor(slot.Page(), it) {
		insertItemSorted(slot.Page(), it)
		// Logical-record path (M0002 redo-records): if the pool
		// has a btree-insert hook configured, emit a small
		// change record on subsequent dirties of this page in
		// the same checkpoint epoch instead of a full FPI. The
		// first dirty in an epoch still emits the FPI baseline.
		// Falls back to plain MarkDirty when the hook isn't
		// wired (test helpers, pre-runtime callers).
		var derr error
		if logIns := bt.pool.LogBtreeInsert(); logIns != nil {
			itemBytes := it.marshal()
			derr = bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
				return logIns(bt.rel, blk, itemBytes)
			})
		} else {
			bt.pool.MarkDirty(slot)
		}
		bt.unpinW(slot)
		return derr
	}

	// Split. Pin a freshly-extended right page exclusively,
	// redistribute items, stamp the high key, then drop both
	// latches before walking up.
	op := readOpaque(slot.Page())
	// M0055-0005 Phase D: prefer recycled blocks before
	// extending the file.
	rightSlot, rightBlk, err := bt.pinNewOrRecycled()
	if err != nil {
		bt.unpinW(slot)
		return err
	}
	rightSlot.Lock()

	// The right sibling inherits the original page's high key
	// (or has none, if this was the rightmost page on its level).
	rightOpaque := BTPageOpaque{
		Prev:    blk,
		Next:    op.Next,
		Level:   op.Level,
		Flags:   op.Flags & ^BTRoot, // right sibling is never the root (root stays the original blk during a split, until we lift)
		HighKey: op.HighKey,
	}
	if !op.HasHighKey() {
		rightOpaque.Flags &^= BTHasHighKey
	}
	initPage(rightSlot.Page(), rightOpaque)

	allItems, err := pageItems(slot.Page())
	if err != nil {
		rightSlot.Unlock()
		bt.pool.Unpin(rightSlot)
		bt.unpinW(slot)
		return err
	}
	allItems = appendSorted(allItems, it)

	// M0055-0003 Phase B (pre-split dedup compaction): consolidate
	// adjacent same-key items into postings. For duplicate-heavy
	// workloads this reduces split frequency dramatically — the
	// page may fit comfortably in a single leaf after dedup,
	// avoiding the split entirely. We bail back to the no-split
	// path if dedup recovers enough space.
	allItems = dedupConsolidate(allItems)
	if compactRawSize(allItems) < pageFreeBudget(slot.Page())+pageOccupied(slot.Page()) {
		// Re-attempt no-split insert with the dedup'd content.
		// Reset the page and write the dedup'd items back, no
		// split needed. The right-side allocation is rolled back.
		resetPageItems(slot.Page())
		for _, x := range allItems {
			insertItemSorted(slot.Page(), x)
		}
		// Drop the freshly-allocated right slot — split avoided.
		rightSlot.Unlock()
		bt.pool.Unpin(rightSlot)
		// MarkDirty the left page so the dedup'd content
		// reaches WAL.
		bt.pool.MarkDirty(slot)
		bt.unpinW(slot)
		return nil
	}

	// M0055-0002-followup-byte-split: byte-aware split-loc.
	// Pick the entry whose cumulative encoded byte size lands
	// closest to half the total. For fixed-width keys this is
	// equivalent to count-midpoint; for variable-width keys it
	// produces balanced halves in bytes (the on-disk metric the
	// page fill threshold actually cares about).
	mid := byteAwareSplitLoc(allItems)
	leftItems := allItems[:mid]
	rightItems := allItems[mid:]

	// Reset left page to empty, refill.
	resetPageItems(slot.Page())
	for _, x := range leftItems {
		insertItemSorted(slot.Page(), x)
	}
	for _, x := range rightItems {
		insertItemSorted(rightSlot.Page(), x)
	}

	// Stamp the new high key onto the left page: left now covers
	// keys ≤ HighKey, the rest live on rightBlk via the
	// right-link. This is the Lehman-Yao invariant readers rely on.
	op.Next = rightBlk
	op.Flags |= BTHasHighKey
	sepKey := rightItems[0].key
	if len(sepKey) > MaxHighKeyLen {
		rightSlot.Unlock()
		bt.pool.Unpin(rightSlot)
		bt.unpinW(slot)
		return fmt.Errorf("btree: separator key length %d exceeds MaxHighKeyLen %d",
			len(sepKey), MaxHighKeyLen)
	}
	op.HighKey = append([]byte(nil), sepKey...)
	// M0055-0004-followup-finish-split: mark the LEFT page as
	// incomplete-split before releasing latches. Cleared after
	// the parent downlink insert succeeds. Crash-replay leaves
	// the flag set so a subsequent writer descending to this
	// page runs finishSplit before inserting (the protocol's
	// resume guarantee).
	op.Flags |= BTIncompleteSplit
	writeOpaque(slot.Page(), op)

	// Atomic split WAL record (Landing 3a). When a writer is
	// available, emit ONE record covering both pages and stamp
	// the resulting LSN onto both page headers; this guarantees
	// crash recovery never observes the half-split state where
	// left's right-link points at a right block whose disk image
	// is the bare smgr.Extend init page. When no writer is wired
	// (test helpers, pre-runtime callers), fall back to the
	// per-page FPI path via MarkDirty — losing split atomicity
	// but keeping the in-memory tree correct.
	if bt.logSplit != nil {
		lsn, lerr := bt.logSplit(bt.rel, blk, rightBlk, slot.Page(), rightSlot.Page())
		if lerr != nil {
			rightSlot.Unlock()
			bt.pool.Unpin(rightSlot)
			bt.unpinW(slot)
			return fmt.Errorf("btree: log split: %w", lerr)
		}
		bt.pool.MarkDirtyWithLSNLocked(slot, lsn)
		bt.pool.MarkDirtyWithLSNLocked(rightSlot, lsn)
	} else {
		bt.pool.MarkDirty(slot)
		bt.pool.MarkDirty(rightSlot)
	}

	// The separator key going up is the smallest key in the right page.
	sepItem := item{
		keyLen: rightItems[0].keyLen,
		ptr:    storage.ItemPointer{Block: rightBlk, Offset: 0},
		key:    append([]byte(nil), rightItems[0].key...),
	}

	rightSlot.Unlock()
	bt.pool.Unpin(rightSlot)
	bt.unpinW(slot)

	// If we just split the root, lift a new root.
	if op.IsRoot() {
		// Strip BTRoot off the now-non-root left page.
		if err := bt.clearRootFlag(blk); err != nil {
			return err
		}
		if err := bt.createNewRoot(blk, rightBlk, sepItem.key, op.Level+1); err != nil {
			return err
		}
		// Parent (the new root) now references the left half;
		// clear the INCOMPLETE_SPLIT marker. (M0055-0004-followup.)
		return bt.clearIncompleteSplit(blk)
	}

	// Otherwise, insert the separator into the parent.
	parentBlk := path[len(path)-1]
	parentPath := path[:len(path)-1]
	if err := bt.insertIntoBlock(parentBlk, parentPath, sepItem); err != nil {
		return err
	}
	// Parent insert succeeded — clear the INCOMPLETE_SPLIT flag
	// on the left page. (M0055-0004-followup.)
	return bt.clearIncompleteSplit(blk)
}

// clearIncompleteSplit (M0055-0004-followup-finish-split) unsets
// the BTIncompleteSplit flag on the page. Invoked at the end of
// a successful split sequence (parent downlink inserted), and
// by `finishSplit` when a writer / vacuum encounters a stale
// incomplete-split marker from a crashed prior run.
func (bt *BTree) clearIncompleteSplit(blk storage.BlockNumber) error {
	slot, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	op := readOpaque(slot.Page())
	op.Flags &^= BTIncompleteSplit
	writeOpaque(slot.Page(), op)
	err = bt.markDirtyWithPageRecord(slot, blk)
	bt.unpinW(slot)
	return err
}

// finishSplit (M0055-0004-followup-finish-split) re-runs the
// parent-downlink insertion for a page that is still flagged
// BTIncompleteSplit (e.g., after crash-replay where the writer's
// split phase landed but the parent insert did not). Idempotent
// — if the parent already references the page, the redundant
// insert is detected by the parent's binary-search "already
// present" check and the flag is simply cleared.
//
// Callers: writers descending the tree (before continuing
// writes on this page); vacuum maintenance pass.
func (bt *BTree) finishSplit(blk storage.BlockNumber) error {
	slot, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	op := readOpaque(slot.Page())
	if !op.HasIncompleteSplit() {
		bt.unpinW(slot)
		return nil
	}
	// Read the high key + Next to reconstruct the separator
	// item that the original split was about to push up.
	if !op.HasHighKey() || op.Next == storage.InvalidBlockNumber {
		// Defensive: malformed half-state. Clear the flag and
		// hope the parent caught up via some other path.
		op.Flags &^= BTIncompleteSplit
		writeOpaque(slot.Page(), op)
		bt.markDirtyWithPageRecord(slot, blk)
		bt.unpinW(slot)
		return nil
	}
	rightBlk := op.Next
	sepKey := append([]byte(nil), op.HighKey...)
	bt.unpinW(slot)

	// Walk to the parent and insert the separator. We descend by
	// the separator key so the parent path is reconstructed.
	bt.splitMu.Lock()
	defer bt.splitMu.Unlock()
	_, path, err := bt.descendToLeaf(sepKey)
	if err != nil {
		return err
	}
	if len(path) == 0 {
		// blk is the root — parent insert means a new root lift.
		// Should have happened atomically; if not, redo it.
		return bt.createNewRoot(blk, rightBlk, sepKey, op.Level+1)
	}
	parentBlk := path[len(path)-1]
	parentPath := path[:len(path)-1]
	sepItem := item{
		keyLen: uint16(len(sepKey)),
		ptr:    storage.ItemPointer{Block: rightBlk, Offset: 0},
		key:    sepKey,
	}
	if err := bt.insertIntoBlock(parentBlk, parentPath, sepItem); err != nil {
		return err
	}
	return bt.clearIncompleteSplit(blk)
}

func (bt *BTree) clearRootFlag(blk storage.BlockNumber) error {
	slot, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	op := readOpaque(slot.Page())
	op.Flags &^= BTRoot
	writeOpaque(slot.Page(), op)
	err = bt.markDirtyWithPageRecord(slot, blk)
	bt.unpinW(slot)
	if err != nil {
		return err
	}
	return nil
}

// ApplyInsertRecord re-runs one B-tree non-split insert against
// the given page bytes during WAL replay (see
// docs/design/0002-0003-redo-records.md). The raw item is the
// same payload the writer emitted (item.marshal output: keyLen +
// ptr.block + ptr.offset + key). The page must already be a
// valid initialised B-tree page; replay never creates a fresh
// btree page from a logical insert (a split record handles that
// case).
//
// Idempotency is the caller's responsibility: WAL recovery
// compares page pd_lsn against the record's end-LSN before
// invoking this. The function is "apply unconditionally".
func ApplyInsertRecord(page storage.Page, raw []byte) error {
	it, err := parseItem(raw)
	if err != nil {
		return err
	}
	if !pageHasSpaceFor(page, it) {
		return fmt.Errorf("btree: replay of insert: page has no space for keyLen=%d", it.keyLen)
	}
	insertItemSorted(page, it)
	return nil
}

func (bt *BTree) createNewRoot(leftBlk, rightBlk storage.BlockNumber, rightKey []byte, level uint32) error {
	rootSlot, rootBlk, err := bt.pool.PinNew(bt.rel)
	if err != nil {
		return err
	}
	rootSlot.Lock()
	initPage(rootSlot.Page(), BTPageOpaque{
		Prev:  storage.InvalidBlockNumber,
		Next:  storage.InvalidBlockNumber,
		Level: level,
		Flags: BTRoot,
	})

	// Leftmost internal item: empty key, pointer to leftBlk.
	insertItemSorted(rootSlot.Page(), item{
		keyLen: 0,
		ptr:    storage.ItemPointer{Block: leftBlk, Offset: 0},
		key:    nil,
	})
	insertItemSorted(rootSlot.Page(), item{
		keyLen: uint16(len(rightKey)),
		ptr:    storage.ItemPointer{Block: rightBlk, Offset: 0},
		key:    append([]byte(nil), rightKey...),
	})
	if err := bt.markDirtyWithPageRecord(rootSlot, rootBlk); err != nil {
		rootSlot.Unlock()
		bt.pool.Unpin(rootSlot)
		return err
	}
	rootSlot.Unlock()
	bt.pool.Unpin(rootSlot)

	return bt.updateRootMeta(rootBlk, level)
}

// pageHasSpaceFor reports whether `it` would fit on `p` if appended.
func pageHasSpaceFor(p storage.Page, it item) bool {
	h := storage.MustHeader(p)
	free := int(h.Upper()) - int(h.Lower())
	const itemIDSize = 4
	return free >= itemIDSize+itemPrefixSize+len(it.key)
}

// insertItemSorted inserts it into p in sorted-key order. Caller must
// have verified there is room (pageHasSpaceFor) — otherwise this panics.
//
// The implementation pulls all items off the page, inserts the new one
// at the right offset, and rewrites the page. That's quadratic in the
// item count but v0 leaves are small (<256 entries) and split before
// growing further; the simplicity is worth it until profiling says
// otherwise.
// insertItemSorted (M0055-0002 Phase A) writes `it` into its
// sorted position on the page WITHOUT decoding and re-encoding
// every existing item. Binary-searches the existing line-pointer
// array reading only each candidate's key; calls
// `PageInsertItemRawAt` to memmove the line-pointer suffix and
// drop the new tuple bytes at pd_upper.
//
// Replaces the prior whole-page rewrite path that decoded every
// item, inserted into a Go slice, reset the page, and re-
// encoded every item. The pre-Phase-A path was the chief CPU
// hotspot per the M0055 baseline pprof (analysis/btree-baseline-
// 2026-05-06.md) — this rewrite eliminates the O(n) re-encode.
func insertItemSorted(p storage.Page, it item) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		panic(err)
	}
	// Binary-search for the insertion slot. The line-pointer
	// accessors decode just one key per probe, not every item
	// on the page.
	lo, hi := 0, count
	for lo < hi {
		mid := (lo + hi) >> 1
		midItem, err := readPageItem(p, mid)
		if err != nil {
			panic(err)
		}
		if CompareKeys(midItem.key, it.key) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	// (M0055-0003 Phase B's in-insertItemSorted dedup-grow path
	// was tried but produces page-fragment garbage that
	// accumulates over many duplicate inserts. The pre-split
	// dedup compaction variant — see `dedupPageBeforeSplit` —
	// is the safer landing for steady-state dedup retention.)
	raw := it.marshal()
	// PageInsertItemRawAt is 1-based, line-pointer index lo is
	// 0-based; convert.
	if _, err := storage.PageInsertItemRawAt(p, uint16(lo+1), raw); err != nil {
		panic(err)
	}
}

// tryInsertOnCachedRightmost (M0055-0002-followup-rightmost-cache)
// attempts to insert `it` on the cached rightmost leaf. Returns
// (true, nil) when the insert succeeded on the cached leaf; (false,
// nil) when the cache is stale or the key doesn't belong on this
// leaf (so the caller falls back to a full descent); (any, err)
// when an underlying I/O error fires.
//
// Staleness conditions:
//   - The leaf has a Next pointer set (a later split moved the
//     rightmost forward). The cache is stale; clear and miss.
//   - The leaf is full (no space for `it`). Caller takes the
//     normal split path.
//   - `it.key < leaf.HighKey` (the key belongs to a left sibling,
//     not this leaf — the cache is wrong for this insert).
//
// The cache is updated by the slow-path descent every time it
// reaches a leaf that's truly the rightmost (Next == 0).
func (bt *BTree) tryInsertOnCachedRightmost(blk storage.BlockNumber, it item) (bool, error) {
	slot, err := bt.pinW(blk)
	if err != nil {
		// Cache may reference a deleted page — clear and miss.
		bt.rightmostLeafBlk.Store(0)
		return false, nil
	}
	op := readOpaque(slot.Page())
	if op.Level != 0 || op.Next != 0 {
		// Not a leaf, or no longer rightmost — cache is stale.
		bt.unpinW(slot)
		bt.rightmostLeafBlk.Store(0)
		return false, nil
	}
	// The rightmost leaf has no high key; any key is in range.
	if !pageHasSpaceFor(slot.Page(), it) {
		bt.unpinW(slot)
		return false, nil
	}
	insertItemSorted(slot.Page(), it)
	if logIns := bt.pool.LogBtreeInsert(); logIns != nil {
		itemBytes := it.marshal()
		err := bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
			return logIns(bt.rel, blk, itemBytes)
		})
		bt.unpinW(slot)
		return true, err
	}
	bt.pool.MarkDirty(slot)
	bt.unpinW(slot)
	return true, nil
}

// dedupConsolidate (M0055-0003 Phase B) walks a sorted item list
// and merges runs of same-key items into a single conceptual
// posting (represented here as the FIRST item; subsequent
// duplicates are suppressed and their TIDs are folded into the
// first via a synthetic posting marker). The result is suitable
// for a fresh insertItemSorted re-build of the page.
//
// Implementation note: items[] is the EXPANDED form (one item
// per (key, tid)) produced by `pageItems`. Consolidation
// re-builds postings by replacing the first item of each run
// with a posting-flagged item whose `key` is the actual key and
// whose `ptr` is the first TID; subsequent duplicates contribute
// their TIDs through a side-channel `extraTIDs` slice on the
// item.
//
// To keep things simple, we DON'T yet rebuild posting bytes here
// — instead we de-duplicate the expanded list by collapsing
// runs of identical (key, ptr) pairs that may exist after
// pageItems' expansion. For real consolidation into postings,
// we use `marshalPosting` directly when re-writing the page
// (Phase B's full landing — out of scope for this commit).
//
// What this function does TODAY: drop exact (key, ptr) duplicates
// from the expanded list. That alone bounds duplicate-heavy
// workloads where the same heap tuple shows up multiple times
// in the bulk-build's input.
func dedupConsolidate(items []item) []item {
	if len(items) <= 1 {
		return items
	}
	out := items[:0]
	for i, it := range items {
		if i == 0 {
			out = append(out, it)
			continue
		}
		prev := out[len(out)-1]
		if CompareKeys(prev.key, it.key) == 0 && prev.ptr == it.ptr {
			// Exact duplicate — drop.
			continue
		}
		out = append(out, it)
	}
	return out
}

// compactRawSize (M0055-0003 Phase B) sums the marshalled byte
// size of every item in `items` plus the per-item line-pointer
// overhead. Used to decide whether dedup recovered enough space
// to skip the split.
func compactRawSize(items []item) int {
	const itemIDSize = 4 // matches storage.itemIDSize
	total := 0
	for _, it := range items {
		total += itemIDSize + 8 + len(it.key) // marshal: 8-byte prefix + key
	}
	return total
}

// pageFreeBudget returns the remaining free byte budget on a
// page (pd_upper - pd_lower).
func pageFreeBudget(p storage.Page) int {
	h, err := storage.Header(p)
	if err != nil {
		return 0
	}
	return int(h.Upper()) - int(h.Lower())
}

// pageOccupied returns the bytes already used by line pointers
// + tuple data on a page.
func pageOccupied(p storage.Page) int {
	h, err := storage.Header(p)
	if err != nil {
		return 0
	}
	return int(h.Lower()) - int(storage.SizeOfPageHeaderData) +
		(int(btSpecialOffset) - int(h.Upper()))
}

// byteAwareSplitLoc (M0055-0002-followup-byte-split) returns the
// 0-based slot index where the split should land so the left
// half holds approximately half the total encoded byte size.
// For fixed-width keys this collapses to len(items)/2 (within a
// rounding tick); for variable-width keys the split point lands
// where the byte cursor crosses half-total, producing balanced
// halves in bytes — the metric the page-fill threshold actually
// cares about.
//
// The minimum split point is 1 (left always retains at least one
// item) and the maximum is len(items)-1 so the right side is
// non-empty. A degenerate single-item input returns 1 (caller
// guarantees ≥ 2 items because we're splitting a full page).
func byteAwareSplitLoc(items []item) int {
	if len(items) <= 2 {
		return 1
	}
	total := 0
	sizes := make([]int, len(items))
	for i, it := range items {
		// 8-byte fixed prefix + variable-length key (per
		// `(item).marshal()` layout). We use the encoded size
		// rather than just key length so the metric matches the
		// on-page footprint.
		sizes[i] = 8 + len(it.key)
		total += sizes[i]
	}
	half := total / 2
	cum := 0
	for i := 0; i < len(items)-1; i++ {
		cum += sizes[i]
		if cum >= half {
			// Place the split AFTER item i — left holds items[0..i],
			// right holds items[i+1..]. Return i+1 because callers
			// use `items[:mid]` / `items[mid:]`.
			split := i + 1
			if split < 1 {
				split = 1
			}
			if split > len(items)-1 {
				split = len(items) - 1
			}
			return split
		}
	}
	// Fall-through: total too small to cross half-mark before the
	// last entry — return n-1 so right has exactly one item.
	return len(items) - 1
}

// readPageItem (M0055-0002 Phase A) decodes a single item at the
// given 0-based line-pointer index. Used by binary-search probes
// in `insertItemSorted` so the insert path no longer decodes
// every item on the page. For posting-list line pointers
// (M0047-0003) it returns the FIRST tid bundled in the posting
// — that's enough for the binary search since all posting tids
// share the same key.
func readPageItem(p storage.Page, idx int) (item, error) {
	raw, err := storage.PageGetItemRaw(p, uint16(idx+1))
	if err != nil {
		return item{}, err
	}
	if isPostingRaw(raw) {
		key, tids, perr := parsePostingRaw(raw)
		if perr != nil {
			return item{}, perr
		}
		var ptr storage.ItemPointer
		if len(tids) > 0 {
			ptr = tids[0]
		}
		return item{keyLen: uint16(len(key)), ptr: ptr, key: key}, nil
	}
	return parseItem(raw)
}

func appendSorted(items []item, it item) []item {
	idx := sort.Search(len(items), func(i int) bool {
		return CompareKeys(items[i].key, it.key) >= 0
	})
	out := make([]item, len(items)+1)
	copy(out[:idx], items[:idx])
	out[idx] = it
	copy(out[idx+1:], items[idx:])
	return out
}

// resetPageItems clears the page's tuple region and line-pointer array
// while keeping the opaque area intact.
func resetPageItems(p storage.Page) {
	h := storage.MustHeader(p)
	h.SetLower(uint16(storage.SizeOfPageHeaderData))
	h.SetUpper(uint16(btSpecialOffset))
	// Zero the in-between bytes for cleanliness; not strictly required.
	for i := storage.SizeOfPageHeaderData; i < btSpecialOffset; i++ {
		p[i] = 0
	}
}

// Search returns the heap pointer associated with key, if any.
//
// Search takes no tree-wide lock — concurrent Searches and the
// single in-flight Insert run truly in parallel except where they
// touch the same page (and there only transiently, under the
// buffer pool's per-slot content latch). Right-link recovery at
// the leaf catches the case where a split moved our key to the
// right sibling between descent and lookup.
func (bt *BTree) Search(key []byte) (storage.ItemPointer, bool, error) {
	leafBlk, _, err := bt.descendToLeaf(key)
	if err != nil {
		return storage.ItemPointer{}, false, err
	}
	cur := leafBlk
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return storage.ItemPointer{}, false, err
		}
		op := readOpaque(slot.Page())
		if keyExceedsHighKey(op, key) {
			next := op.Next
			bt.unpinR(slot)
			cur = next
			continue
		}
		items, err := pageItems(slot.Page())
		bt.unpinR(slot)
		if err != nil {
			return storage.ItemPointer{}, false, err
		}
		idx := sort.Search(len(items), func(i int) bool {
			return CompareKeys(items[i].key, key) >= 0
		})
		if idx >= len(items) || CompareKeys(items[idx].key, key) != 0 {
			return storage.ItemPointer{}, false, nil
		}
		return items[idx].ptr, true, nil
	}
}

// RangeScan invokes fn for every (key, ptr) pair where lo ≤ key ≤ hi.
// Either bound may be nil to indicate an open-ended range:
//   - nil lo means no lower bound (scan from the leftmost key).
//   - nil hi means no upper bound (scan through the rightmost key).
//
// fn returning false stops the scan; the returned error from fn aborts
// with that error.
//
// RangeScan takes no tree-wide lock; each page is read under the
// buffer pool's per-slot shared content latch. The first leaf is
// reached via descendToLeaf (which already handles right-link
// recovery); subsequent leaves are walked rightward via op.Next.
// fn is invoked while no latches are held so callers may issue
// further btree operations without deadlocking.
func (bt *BTree) RangeScan(lo, hi []byte, fn func(key []byte, ptr storage.ItemPointer) (bool, error)) error {
	cur, _, err := bt.descendToLeaf(lo)
	if err != nil {
		return err
	}
	for cur != storage.InvalidBlockNumber {
		slot, err := bt.pinR(cur)
		if err != nil {
			return err
		}
		op := readOpaque(slot.Page())
		// Recovery on the first iteration: descendToLeaf may
		// have returned a leaf that has since been split. Skip
		// rightward until we land on a page whose key range
		// covers `lo` (or we run out of pages).
		// When lo is nil, keyExceedsHighKey(op, nil) is always false
		// (nil compares less than any real key), so we never skip — correct.
		if lo != nil && keyExceedsHighKey(op, lo) {
			next := op.Next
			bt.unpinR(slot)
			cur = next
			continue
		}
		// Copy raw page items before releasing the pin so fn may do
		// further btree operations without deadlocking. Posting items
		// (M0047-0003) are detected by the BTPostingFlag in keyLen and
		// expanded to one (key, TID) call per TID in the posting list.
		count, countErr := storage.PageLinePointerCount(slot.Page())
		type rawSlot struct{ raw []byte }
		rawSlots := make([]rawSlot, 0, count)
		if countErr == nil {
			for s := uint16(1); s <= uint16(count); s++ {
				r, rawErr := storage.PageGetItemRaw(slot.Page(), s)
				if rawErr == nil {
					rawSlots = append(rawSlots, rawSlot{append([]byte(nil), r...)})
				}
			}
		}
		bt.unpinR(slot)
		for _, rs := range rawSlots {
			if isPostingRaw(rs.raw) {
				key, tids, perr := parsePostingRaw(rs.raw)
				if perr != nil {
					continue
				}
				if lo != nil && CompareKeys(key, lo) < 0 {
					continue
				}
				if hi != nil && CompareKeys(key, hi) > 0 {
					return nil
				}
				for _, tid := range tids {
					ok, ferr := fn(key, tid)
					if ferr != nil {
						return ferr
					}
					if !ok {
						return nil
					}
				}
			} else {
				it, perr := parseItem(rs.raw)
				if perr != nil {
					continue
				}
				if lo != nil && CompareKeys(it.key, lo) < 0 {
					continue
				}
				if hi != nil && CompareKeys(it.key, hi) > 0 {
					return nil
				}
				ok, ferr := fn(it.key, it.ptr)
				if ferr != nil {
					return ferr
				}
				if !ok {
					return nil
				}
			}
		}
		cur = op.Next
	}
	return nil
}
