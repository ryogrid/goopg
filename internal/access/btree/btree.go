// Package btree implements goopg's v0 B-tree index access method.
//
// Scope and growth path are documented in docs/design/0009-btree.md.
// v0 supports single-column int4 keys, descend/insert/search/range-scan,
// recursive splits up to a new root. WAL records for inserts/splits and
// page deletion (VACUUM merge) are deferred — see the doc for the bridge.
package btree

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// SizeOfBTPageOpaque is the on-disk size of the per-page B-tree opaque
// area, mirroring upstream's BTPageOpaqueData layout.
const SizeOfBTPageOpaque = 16

// btSpecialOffset is where the opaque area starts on every B-tree page.
const btSpecialOffset = storage.BlockSize - SizeOfBTPageOpaque

// PageFlag values for btpo_flags.
const (
	BTLeaf    uint16 = 0x0001
	BTRoot    uint16 = 0x0002
	BTDeleted uint16 = 0x0004
)

// MetaBlock is always block 0 — the metapage. RootStart is the first
// block holding actual key data; on a freshly created tree it is also
// block 1 (root + leaf).
const (
	MetaBlock storage.BlockNumber = 0
	rootStart storage.BlockNumber = 1

	btreeMagic   uint32 = 0x053162
	btreeVersion uint32 = 1
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
// each B-tree page.
type BTPageOpaque struct {
	Prev  storage.BlockNumber
	Next  storage.BlockNumber
	Level uint32
	Flags uint16
}

// IsLeaf reports whether the page carries leaf items.
func (o BTPageOpaque) IsLeaf() bool { return o.Flags&BTLeaf != 0 }

// IsRoot reports whether the page is the current root.
func (o BTPageOpaque) IsRoot() bool { return o.Flags&BTRoot != 0 }

// readOpaque returns the parsed opaque from page bytes.
func readOpaque(p storage.Page) BTPageOpaque {
	off := btSpecialOffset
	return BTPageOpaque{
		Prev:  storage.BlockNumber(binary.LittleEndian.Uint32(p[off : off+4])),
		Next:  storage.BlockNumber(binary.LittleEndian.Uint32(p[off+4 : off+8])),
		Level: binary.LittleEndian.Uint32(p[off+8 : off+12]),
		Flags: binary.LittleEndian.Uint16(p[off+12 : off+14]),
	}
}

// writeOpaque persists the opaque into page bytes.
func writeOpaque(p storage.Page, o BTPageOpaque) {
	off := btSpecialOffset
	binary.LittleEndian.PutUint32(p[off:off+4], uint32(o.Prev))
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(o.Next))
	binary.LittleEndian.PutUint32(p[off+8:off+12], o.Level)
	binary.LittleEndian.PutUint16(p[off+12:off+14], o.Flags)
	binary.LittleEndian.PutUint16(p[off+14:off+16], 0)
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

// CompareKeys is bytewise comparison; matches numeric order for
// EncodeInt4 by construction.
func CompareKeys(a, b []byte) int {
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// BTree is the access-method handle for one index relation.
type BTree struct {
	pool *storage.Pool
	rel  storage.RelFileNode

	mu sync.Mutex
}

// Open returns a handle to an existing B-tree on rel. Validates the
// metapage; returns ErrNotABTree if the magic doesn't match.
func Open(pool *storage.Pool, rel storage.RelFileNode) (*BTree, error) {
	bt := &BTree{pool: pool, rel: rel}
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
	bt := &BTree{pool: pool, rel: rel}
	bt.mu.Lock()
	defer bt.mu.Unlock()

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
	initPage(rootSlot.Page(), BTPageOpaque{
		Prev:  storage.InvalidBlockNumber,
		Next:  storage.InvalidBlockNumber,
		Level: 0,
		Flags: BTLeaf | BTRoot,
	})
	pool.MarkDirty(rootSlot)

	// Initialise the metapage.
	storage.InitPage(metaSlot.Page())
	writeMeta(metaSlot.Page(), BTreeMeta{
		Magic:     btreeMagic,
		Version:   btreeVersion,
		Root:      rootBlk,
		Level:     0,
		FastRoot:  rootBlk,
		FastLevel: 0,
	})
	pool.MarkDirty(metaSlot)

	pool.Unpin(rootSlot)
	pool.Unpin(metaSlot)

	return bt, nil
}

// Errors.
var (
	ErrNotABTree = errors.New("btree: not a B-tree relation")
)

// readMeta loads the metapage. Caller must already hold bt.mu if
// concurrency matters; readers tolerate stale snapshots since meta
// updates are serialised.
func (bt *BTree) readMeta() (BTreeMeta, error) {
	slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: MetaBlock})
	if err != nil {
		return BTreeMeta{}, err
	}
	defer bt.pool.Unpin(slot)
	return parseMeta(slot.Page()), nil
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

// updateRootMeta rewrites the metapage root pointer + level under the
// big mutex.
func (bt *BTree) updateRootMeta(root storage.BlockNumber, level uint32) error {
	slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: MetaBlock})
	if err != nil {
		return err
	}
	m := parseMeta(slot.Page())
	m.Root = root
	m.Level = level
	m.FastRoot = root
	m.FastLevel = level
	writeMeta(slot.Page(), m)
	bt.pool.MarkDirty(slot)
	bt.pool.Unpin(slot)
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

// descendToLeaf walks from the current root to the leaf containing
// `key`. Returns the leaf block number; pinning is the caller's
// responsibility. The returned slice is the descent path (root..leaf,
// excluding leaf) — callers performing splits walk it in reverse.
func (bt *BTree) descendToLeaf(key []byte) (leafBlk storage.BlockNumber, path []storage.BlockNumber, err error) {
	meta, err := bt.readMeta()
	if err != nil {
		return 0, nil, err
	}
	cur := meta.Root
	for {
		slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: cur})
		if err != nil {
			return 0, nil, err
		}
		op := readOpaque(slot.Page())
		if op.IsLeaf() {
			bt.pool.Unpin(slot)
			return cur, path, nil
		}
		items, err := pageItems(slot.Page())
		bt.pool.Unpin(slot)
		if err != nil {
			return 0, nil, err
		}
		if len(items) == 0 {
			return 0, nil, fmt.Errorf("btree: empty internal page %d", cur)
		}
		path = append(path, cur)
		cur = findChildBlock(items, key)
	}
}

// Insert places (key, ptr) into the leaf where it belongs, splitting
// pages on the way up if needed.
func (bt *BTree) Insert(key []byte, ptr storage.ItemPointer) error {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	leafBlk, path, err := bt.descendToLeaf(key)
	if err != nil {
		return err
	}
	return bt.insertIntoBlock(leafBlk, path, item{
		keyLen: uint16(len(key)),
		ptr:    ptr,
		key:    append([]byte(nil), key...),
	})
}

// insertIntoBlock inserts `it` into the page at blk. If the page lacks
// space, splits it; the resulting separator item is recursively
// inserted into the parent (or a new root is created).
func (bt *BTree) insertIntoBlock(blk storage.BlockNumber, path []storage.BlockNumber, it item) error {
	slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
	if err != nil {
		return err
	}

	if pageHasSpaceFor(slot.Page(), it) {
		insertItemSorted(slot.Page(), it)
		bt.pool.MarkDirty(slot)
		bt.pool.Unpin(slot)
		return nil
	}

	// Split. We pin a freshly extended right page, redistribute items,
	// then propagate the right page's smallest key up.
	op := readOpaque(slot.Page())
	rightSlot, rightBlk, err := bt.pool.PinNew(bt.rel)
	if err != nil {
		bt.pool.Unpin(slot)
		return err
	}
	initPage(rightSlot.Page(), BTPageOpaque{
		Prev:  blk,
		Next:  op.Next,
		Level: op.Level,
		Flags: op.Flags & ^BTRoot, // right sibling is never the root (root stays the original blk during a split, until we lift)
	})

	allItems, err := pageItems(slot.Page())
	if err != nil {
		bt.pool.Unpin(slot)
		bt.pool.Unpin(rightSlot)
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

	// Fix sibling pointer of the page that used to be op.Next.
	if op.Next != storage.InvalidBlockNumber {
		nbrSlot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: op.Next})
		if err == nil {
			no := readOpaque(nbrSlot.Page())
			no.Prev = rightBlk
			writeOpaque(nbrSlot.Page(), no)
			bt.pool.MarkDirty(nbrSlot)
			bt.pool.Unpin(nbrSlot)
		}
	}

	// Update left's opaque to point right.
	op.Next = rightBlk
	writeOpaque(slot.Page(), op)

	bt.pool.MarkDirty(slot)
	bt.pool.MarkDirty(rightSlot)

	// The separator key going up is the smallest key in the right page.
	sepItem := item{
		keyLen: rightItems[0].keyLen,
		ptr:    storage.ItemPointer{Block: rightBlk, Offset: 0},
		key:    append([]byte(nil), rightItems[0].key...),
	}

	bt.pool.Unpin(slot)
	bt.pool.Unpin(rightSlot)

	// If we just split the root, lift a new root.
	if op.IsRoot() {
		// Strip BT_ROOT off the now-non-root left page.
		err := bt.clearRootFlag(blk)
		if err != nil {
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
	slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: blk})
	if err != nil {
		return err
	}
	op := readOpaque(slot.Page())
	op.Flags &^= BTRoot
	writeOpaque(slot.Page(), op)
	bt.pool.MarkDirty(slot)
	bt.pool.Unpin(slot)
	return nil
}

func (bt *BTree) createNewRoot(leftBlk, rightBlk storage.BlockNumber, rightKey []byte, level uint32) error {
	rootSlot, rootBlk, err := bt.pool.PinNew(bt.rel)
	if err != nil {
		return err
	}
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
	bt.pool.MarkDirty(rootSlot)
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
func (bt *BTree) Search(key []byte) (storage.ItemPointer, bool, error) {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	leafBlk, _, err := bt.descendToLeaf(key)
	if err != nil {
		return storage.ItemPointer{}, false, err
	}
	slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: leafBlk})
	if err != nil {
		return storage.ItemPointer{}, false, err
	}
	defer bt.pool.Unpin(slot)
	items, err := pageItems(slot.Page())
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

// RangeScan invokes fn for every (key, ptr) pair where lo ≤ key ≤ hi.
// fn returning false stops the scan; the returned error from fn aborts
// with that error.
func (bt *BTree) RangeScan(lo, hi []byte, fn func(key []byte, ptr storage.ItemPointer) (bool, error)) error {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	cur, _, err := bt.descendToLeaf(lo)
	if err != nil {
		return err
	}
	for cur != storage.InvalidBlockNumber {
		slot, err := bt.pool.Pin(storage.BufferTag{Rel: bt.rel, Block: cur})
		if err != nil {
			return err
		}
		items, err := pageItems(slot.Page())
		op := readOpaque(slot.Page())
		bt.pool.Unpin(slot)
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
