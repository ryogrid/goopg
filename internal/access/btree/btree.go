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
	"math"
	"sort"
	"strconv"
	"sync"

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
// 0011-0001) with headroom. Wider key encodings would require
// another opaque-format bump.
const MaxHighKeyLen = 32

// btSpecialOffset is where the opaque area starts on every B-tree page.
const btSpecialOffset = storage.BlockSize - SizeOfBTPageOpaque

// PageFlag values for btpo_flags.
const (
	BTLeaf       uint16 = 0x0001
	BTRoot       uint16 = 0x0002
	BTDeleted    uint16 = 0x0004
	BTHasHighKey uint16 = 0x0008
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
func EncodeNumericKey(mantissa int64, scale int8) []byte {
	if mantissa == 0 {
		return []byte{0x01}
	}
	negative := mantissa < 0
	var um uint64
	if mantissa == math.MinInt64 {
		um = 1 << 63
	} else if negative {
		um = uint64(-mantissa)
	} else {
		um = uint64(mantissa)
	}
	// Strip trailing zeros so numerically-equal values normalise to
	// the same digit string.
	s := int32(scale)
	for um%10 == 0 {
		um /= 10
		s--
	}
	digits := strconv.FormatUint(um, 10)
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
		it, err := parseItem(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
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
			bt.unpinR(slot)
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
	rightSlot, rightBlk, err := bt.pool.PinNew(bt.rel)
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

	mid := len(allItems) / 2
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
		return bt.createNewRoot(blk, rightBlk, sepItem.key, op.Level+1)
	}

	// Otherwise, insert the separator into the parent.
	parentBlk := path[len(path)-1]
	parentPath := path[:len(path)-1]
	return bt.insertIntoBlock(parentBlk, parentPath, sepItem)
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
func insertItemSorted(p storage.Page, it item) {
	items, err := pageItems(p)
	if err != nil {
		panic(err)
	}
	items = appendSorted(items, it)
	resetPageItems(p)
	for _, x := range items {
		raw := x.marshal()
		_, err := storage.PageAddItemRaw(p, raw)
		if err != nil {
			panic(err)
		}
	}
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
		if keyExceedsHighKey(op, lo) {
			next := op.Next
			bt.unpinR(slot)
			cur = next
			continue
		}
		items, err := pageItems(slot.Page())
		bt.unpinR(slot)
		if err != nil {
			return err
		}
		for _, it := range items {
			if CompareKeys(it.key, lo) < 0 {
				continue
			}
			if CompareKeys(it.key, hi) > 0 {
				return nil
			}
			ok, err := fn(it.key, it.ptr)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
		}
		cur = op.Next
	}
	return nil
}
