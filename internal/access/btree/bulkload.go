package btree

import (
	"fmt"
	"sort"

	"github.com/goopg/goopg/internal/storage"
)

// BulkEntry is one (encoded-key, heap-pointer) pair for bulk index building.
// The key must already be encoded with the same EncodeXxx functions that the
// normal Insert path uses — BulkCreate treats keys as opaque byte slices and
// compares them with CompareKeys (bytewise lexicographic order).
type BulkEntry struct {
	Key []byte
	Ptr storage.ItemPointer
}

// BulkCreate builds a new B-tree index on rel using sort-then-build
// (M0047-0001). It is a drop-in replacement for:
//
//	tree, _ := Create(pool, rel)
//	for _, e := range entries { tree.Insert(e.Key, e.Ptr) }
//
// The advantages over repeated Insert:
//   - O(n) page writes instead of O(n log n) tree traversals + splits.
//   - Sequential writes ⇒ fewer random I/Os.
//   - Leaves are packed to ~100% capacity; Insert leaves them ~75% full
//     after splits.
//
// entries need not be pre-sorted; BulkCreate sorts them internally.
// The returned *BTree is ready for Insert / RangeScan.
func BulkCreate(pool *storage.Pool, rel storage.RelFileNode, entries []BulkEntry) (*BTree, error) {
	return BulkCreateWithOptions(pool, rel, entries, Options{LogSplit: adaptPoolLogSplit(pool)})
}

// BulkCreateNoDedup builds a B-tree without posting-list deduplication.
// All entries are stored as individual items even when keys repeat.
// Used in tests and benchmarks to measure the space savings from dedup.
func BulkCreateNoDedup(pool *storage.Pool, rel storage.RelFileNode, entries []BulkEntry) (*BTree, error) {
	bt := &BTree{pool: pool, rel: rel, logSplit: adaptPoolLogSplit(pool)}
	metaSlot, metaBlk, err := pool.PinNew(rel)
	if err != nil {
		return nil, fmt.Errorf("btree bulk noDedup: alloc meta: %w", err)
	}
	if metaBlk != MetaBlock {
		pool.Unpin(metaSlot)
		return nil, fmt.Errorf("btree bulk noDedup: meta not block 0")
	}
	metaSlot.Lock()

	if len(entries) == 0 {
		rootSlot, rootBlk, err := pool.PinNew(rel)
		if err != nil {
			metaSlot.Unlock(); pool.Unpin(metaSlot)
			return nil, err
		}
		rootSlot.Lock()
		initPage(rootSlot.Page(), BTPageOpaque{
			Prev: storage.InvalidBlockNumber, Next: storage.InvalidBlockNumber,
			Level: 0, Flags: BTLeaf | BTRoot,
		})
		writeMeta(metaSlot.Page(), BTreeMeta{Magic: btreeMagic, Version: btreeVersion,
			Root: rootBlk, Level: 0, FastRoot: rootBlk, FastLevel: 0})
		_ = bt.markDirtyWithPageRecord(rootSlot, rootBlk)
		rootSlot.Unlock(); pool.Unpin(rootSlot)
		_ = bt.markDirtyWithPageRecord(metaSlot, MetaBlock)
		metaSlot.Unlock(); pool.Unpin(metaSlot)
		return bt, nil
	}

	items := make([]item, len(entries))
	for i, e := range entries {
		items[i] = item{keyLen: uint16(len(e.Key)), ptr: e.Ptr, key: append([]byte(nil), e.Key...)}
	}
	sort.SliceStable(items, func(i, j int) bool { return CompareKeys(items[i].key, items[j].key) < 0 })

	// Build without dedup: each entry becomes a regular item.
	leafRaws := itemsToRawItems(items)
	leafLinks, err := bt.buildLevelRaw(leafRaws, BTLeaf, 0)
	if err != nil {
		metaSlot.Unlock(); pool.Unpin(metaSlot)
		return nil, err
	}
	rootBlk := leafLinks[0].blk
	rootLevel := uint32(0)
	if len(leafLinks) > 1 {
		upLinks := leafLinks
		level := uint32(1)
		for len(upLinks) > 1 {
			internalItems := linksToInternalItems(upLinks)
			upLinks, err = bt.buildLevel(internalItems, 0, level)
			if err != nil {
				metaSlot.Unlock(); pool.Unpin(metaSlot)
				return nil, err
			}
			level++
		}
		rootBlk = upLinks[0].blk
		rootLevel = level - 1
	}
	rootSlot2, err := bt.pinW(rootBlk)
	if err != nil {
		metaSlot.Unlock(); pool.Unpin(metaSlot)
		return nil, err
	}
	rootOp := readOpaque(rootSlot2.Page())
	rootOp.Flags |= BTRoot
	writeOpaque(rootSlot2.Page(), rootOp)
	_ = bt.markDirtyWithPageRecord(rootSlot2, rootBlk)
	bt.unpinW(rootSlot2)
	writeMeta(metaSlot.Page(), BTreeMeta{Magic: btreeMagic, Version: btreeVersion,
		Root: rootBlk, Level: rootLevel, FastRoot: rootBlk, FastLevel: rootLevel})
	_ = bt.markDirtyWithPageRecord(metaSlot, MetaBlock)
	metaSlot.Unlock(); pool.Unpin(metaSlot)
	return bt, nil
}

// BulkCreateWithOptions is BulkCreate with explicit Options.
func BulkCreateWithOptions(pool *storage.Pool, rel storage.RelFileNode, entries []BulkEntry, opts Options) (*BTree, error) {
	bt := &BTree{pool: pool, rel: rel, logSplit: opts.LogSplit}

	// Block 0: metapage (identical layout to Create).
	metaSlot, metaBlk, err := pool.PinNew(rel)
	if err != nil {
		return nil, fmt.Errorf("btree bulk: alloc metapage: %w", err)
	}
	if metaBlk != MetaBlock {
		pool.Unpin(metaSlot)
		return nil, fmt.Errorf("btree bulk: expected meta at block 0, got %d", metaBlk)
	}
	metaSlot.Lock()

	if len(entries) == 0 {
		// Empty index: write metapage + one empty leaf (= initial root).
		rootSlot, rootBlk, err := pool.PinNew(rel)
		if err != nil {
			metaSlot.Unlock()
			pool.Unpin(metaSlot)
			return nil, err
		}
		rootSlot.Lock()
		initPage(rootSlot.Page(), BTPageOpaque{
			Prev:  storage.InvalidBlockNumber,
			Next:  storage.InvalidBlockNumber,
			Level: 0,
			Flags: BTLeaf | BTRoot,
		})
		writeMeta(metaSlot.Page(), BTreeMeta{
			Magic:     btreeMagic,
			Version:   btreeVersion,
			Root:      rootBlk,
			Level:     0,
			FastRoot:  rootBlk,
			FastLevel: 0,
		})
		if err := bt.markDirtyWithPageRecord(rootSlot, rootBlk); err != nil {
			rootSlot.Unlock()
			pool.Unpin(rootSlot)
			metaSlot.Unlock()
			pool.Unpin(metaSlot)
			return nil, err
		}
		rootSlot.Unlock()
		pool.Unpin(rootSlot)
		if err := bt.markDirtyWithPageRecord(metaSlot, MetaBlock); err != nil {
			metaSlot.Unlock()
			pool.Unpin(metaSlot)
			return nil, err
		}
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
		return bt, nil
	}

	// Convert entries to internal items and sort by key.
	items := make([]item, len(entries))
	for i, e := range entries {
		items[i] = item{
			keyLen: uint16(len(e.Key)),
			ptr:    e.Ptr,
			key:    append([]byte(nil), e.Key...),
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return CompareKeys(items[i].key, items[j].key) < 0
	})

	// Phase 1: build leaf level with deduplication (M0047-0003).
	// Same-key entries are grouped into posting-list items so a key
	// with N duplicates occupies one page slot instead of N.
	leafRaws := deduplicateToRawItems(items)
	leafLinks, err := bt.buildLevelRaw(leafRaws, BTLeaf, 0)
	if err != nil {
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
		return nil, err
	}

	// Phase 2: build internal levels until we have a single root.
	rootBlk := leafLinks[0].blk
	rootLevel := uint32(0)

	if len(leafLinks) > 1 {
		upLinks := leafLinks
		level := uint32(1)
		for len(upLinks) > 1 {
			internalItems := linksToInternalItems(upLinks)
			upLinks, err = bt.buildLevel(internalItems, 0 /* internal page, no BTLeaf */, level)
			if err != nil {
				metaSlot.Unlock()
				pool.Unpin(metaSlot)
				return nil, err
			}
			level++
		}
		rootBlk = upLinks[0].blk
		rootLevel = level - 1
	}

	// Set BTRoot flag on the root page so that Insert knows to create a
	// new root when the root page splits (insertIntoBlock checks op.IsRoot()).
	rootSlot2, err := bt.pinW(rootBlk)
	if err != nil {
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
		return nil, fmt.Errorf("btree bulk: pin root to set BTRoot: %w", err)
	}
	rootOp := readOpaque(rootSlot2.Page())
	rootOp.Flags |= BTRoot
	writeOpaque(rootSlot2.Page(), rootOp)
	if err := bt.markDirtyWithPageRecord(rootSlot2, rootBlk); err != nil {
		bt.unpinW(rootSlot2)
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
		return nil, err
	}
	bt.unpinW(rootSlot2)

	// Write metapage with the final root.
	writeMeta(metaSlot.Page(), BTreeMeta{
		Magic:     btreeMagic,
		Version:   btreeVersion,
		Root:      rootBlk,
		Level:     rootLevel,
		FastRoot:  rootBlk,
		FastLevel: rootLevel,
	})
	if err := bt.markDirtyWithPageRecord(metaSlot, MetaBlock); err != nil {
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
		return nil, err
	}
	metaSlot.Unlock()
	pool.Unpin(metaSlot)
	return bt, nil
}

// bulkLink records a page and its "highKey" — the first key of the NEXT
// sibling at the same level. nil means this is the rightmost page.
type bulkLink struct {
	blk     storage.BlockNumber
	highKey []byte
}

// linksToInternalItems converts a set of child-page links into items for
// the parent internal page. The leftmost item carries a nil key (by
// v0 convention); subsequent items carry the highKey of their left
// sibling as the separator key.
func linksToInternalItems(links []bulkLink) []item {
	out := make([]item, len(links))
	out[0] = item{
		keyLen: 0,
		ptr:    storage.ItemPointer{Block: links[0].blk},
		key:    nil,
	}
	for i := 1; i < len(links); i++ {
		sep := links[i-1].highKey // separator = first key of this child = highKey of prev sibling
		out[i] = item{
			keyLen: uint16(len(sep)),
			ptr:    storage.ItemPointer{Block: links[i].blk},
			key:    append([]byte(nil), sep...),
		}
	}
	return out
}

// buildLevel writes items into as many pages as needed at the given
// B-tree level, linking them via the Prev/Next fields in the opaque
// area. It returns one bulkLink per page built.
//
// flags must contain BTLeaf for leaf pages and be 0 for internal pages.
func (bt *BTree) buildLevel(items []item, flags uint16, level uint32) ([]bulkLink, error) {
	var result []bulkLink
	var (
		curSlot *storage.Slot
		curBlk  storage.BlockNumber
		prevBlk = storage.InvalidBlockNumber
	)

	// startNewPage allocates and initialises a fresh page, linking its
	// Prev pointer to the page that was just flushed.
	startNewPage := func(prev storage.BlockNumber) error {
		slot, blk, err := bt.pool.PinNew(bt.rel)
		if err != nil {
			return fmt.Errorf("btree bulk: PinNew at level %d: %w", level, err)
		}
		slot.Lock()
		initPage(slot.Page(), BTPageOpaque{
			Prev:  prev,
			Next:  storage.InvalidBlockNumber,
			Level: level,
			Flags: flags,
		})
		curSlot = slot
		curBlk = blk
		return nil
	}

	// flushPage writes the opaque area (setting Next + optional HighKey),
	// marks the page dirty, and releases it.
	flushPage := func(highKey []byte, nextBlk storage.BlockNumber) error {
		op := readOpaque(curSlot.Page())
		op.Next = nextBlk
		if len(highKey) > 0 {
			op.Flags |= BTHasHighKey
			op.HighKey = append([]byte(nil), highKey...)
		}
		writeOpaque(curSlot.Page(), op)
		result = append(result, bulkLink{blk: curBlk, highKey: highKey})
		err := bt.markDirtyWithPageRecord(curSlot, curBlk)
		curSlot.Unlock()
		bt.pool.Unpin(curSlot)
		prevBlk = curBlk
		curSlot = nil
		return err
	}

	for i, it := range items {
		// Allocate a page if we don't have one open.
		if curSlot == nil {
			if err := startNewPage(prevBlk); err != nil {
				return nil, err
			}
		}

		if !pageHasSpaceFor(curSlot.Page(), it) {
			// Current page is full. Allocate the next page first so we can
			// write its block number into the current page's Next field.
			nextSlot, nextBlk, err := bt.pool.PinNew(bt.rel)
			if err != nil {
				curSlot.Unlock()
				bt.pool.Unpin(curSlot)
				return nil, fmt.Errorf("btree bulk: PinNew next at level %d: %w", level, err)
			}

			// highKey of the current page = key of the first item on the next page.
			highKey := items[i].key

			// Flush and release current page, linking it to nextBlk.
			if err := flushPage(highKey, nextBlk); err != nil {
				// nextSlot is pinned but not locked yet; release it.
				bt.pool.Unpin(nextSlot)
				return nil, err
			}

			// Initialise next page (Prev = old curBlk, already updated in prevBlk).
			nextSlot.Lock()
			initPage(nextSlot.Page(), BTPageOpaque{
				Prev:  prevBlk, // set by flushPage above
				Next:  storage.InvalidBlockNumber,
				Level: level,
				Flags: flags,
			})
			curSlot = nextSlot
			curBlk = nextBlk
		}

		// Append item to the current page.
		if _, err := storage.PageAddItemRaw(curSlot.Page(), it.marshal()); err != nil {
			curSlot.Unlock()
			bt.pool.Unpin(curSlot)
			return nil, fmt.Errorf("btree bulk: PageAddItemRaw: %w", err)
		}
	}

	// Flush the final (rightmost) page at this level.
	if curSlot != nil {
		if err := flushPage(nil /* no highKey */, storage.InvalidBlockNumber); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// rawItem is a pre-serialised B-tree page entry together with its decoded
// key, used by buildLevelRaw. It supports both regular items (marshalled
// via item.marshal()) and posting-list items (marshalled via marshalPosting).
type rawItem struct {
	raw []byte // bytes written into the page via PageAddItemRaw
	key []byte // key used for highKey / boundary comparisons
}

// itemsToRawItems converts items to rawItems without deduplication,
// one rawItem per input item. Used for baseline comparisons in tests.
func itemsToRawItems(items []item) []rawItem {
	out := make([]rawItem, len(items))
	for i, it := range items {
		out[i] = rawItem{raw: it.marshal(), key: it.key}
	}
	return out
}

// maxRawItemSize bounds an individual on-page item so that
//  1. it satisfies storage.PageAddItemRaw's 15-bit Length field (0x7FFF), and
//  2. it physically fits on an 8 KiB page after page header (24 B), B-tree
//     opaque trailer (48 B, includes HighKey reserve) and the new line
//     pointer (4 B). 8192 - 24 - 48 - 4 = 8116 — round down for safety.
//
// M0053-0005 uses this constant to split oversized posting-list runs into
// multiple smaller posting items so each fits on a single page.
const maxRawItemSize = 8000

// deduplicateToRawItems groups consecutive same-key items into
// posting-list rawItems (M0047-0003). Items must already be sorted by key.
//
//   - A run of 1: marshalled as a normal item (8+keyLen bytes).
//   - A run of N≥2: marshalled as a posting-list item (4+N×6+keyLen bytes).
//
// M0053-0005: When a single run would produce a posting item exceeding
// the 15-bit page line-pointer limit (0x7FFF bytes), the run is split
// into multiple posting items each containing a chunk of TIDs.
// PostgreSQL's btree readers handle multiple same-key items on a page
// without issue (they iterate items sequentially), so correctness is
// preserved at the cost of slightly higher index size for very
// high-cardinality keys (e.g. LINEITEM(L_LINENUMBER) at TPC-H SF=1
// where one value can have ~1.5M duplicates).
func deduplicateToRawItems(items []item) []rawItem {
	out := make([]rawItem, 0, len(items))
	i := 0
	for i < len(items) {
		j := i + 1
		for j < len(items) && CompareKeys(items[j].key, items[i].key) == 0 {
			j++
		}
		key := items[i].key
		if j-i == 1 {
			out = append(out, rawItem{raw: items[i].marshal(), key: key})
		} else {
			tids := make([]storage.ItemPointer, j-i)
			for k := i; k < j; k++ {
				tids[k-i] = items[k].ptr
			}
			// Compute the maximum number of TIDs that fit in one posting
			// item: maxRawItemSize - postingHeaderSize - len(key) bytes
			// available for TIDs at 6 bytes each.
			maxTIDsPerChunk := (maxRawItemSize - postingHeaderSize - len(key)) / 6
			if maxTIDsPerChunk < 1 {
				// Pathological: key alone exceeds the page limit. Fall
				// back to single-item-per-TID encoding so each entry
				// still fits (it.marshal() uses 8+keyLen bytes; if even
				// that overflows, the underlying PageAddItemRaw will
				// reject and the caller sees a clean error).
				for k := i; k < j; k++ {
					out = append(out, rawItem{raw: items[k].marshal(), key: key})
				}
			} else if len(tids) <= maxTIDsPerChunk {
				out = append(out, rawItem{raw: marshalPosting(key, tids), key: key})
			} else {
				for off := 0; off < len(tids); off += maxTIDsPerChunk {
					end := off + maxTIDsPerChunk
					if end > len(tids) {
						end = len(tids)
					}
					chunk := tids[off:end]
					out = append(out, rawItem{raw: marshalPosting(key, chunk), key: key})
				}
			}
		}
		i = j
	}
	return out
}

// buildLevelRaw is like buildLevel but accepts pre-serialised rawItems.
// Used for the leaf level where deduplication may produce posting-list
// items alongside regular items.
func (bt *BTree) buildLevelRaw(raws []rawItem, flags uint16, level uint32) ([]bulkLink, error) {
	var result []bulkLink
	var (
		curSlot *storage.Slot
		curBlk  storage.BlockNumber
		prevBlk = storage.InvalidBlockNumber
	)

	startNewPage := func(prev storage.BlockNumber) error {
		slot, blk, err := bt.pool.PinNew(bt.rel)
		if err != nil {
			return fmt.Errorf("btree bulk raw: PinNew at level %d: %w", level, err)
		}
		slot.Lock()
		initPage(slot.Page(), BTPageOpaque{
			Prev:  prev,
			Next:  storage.InvalidBlockNumber,
			Level: level,
			Flags: flags,
		})
		curSlot = slot
		curBlk = blk
		return nil
	}

	flushPage := func(highKey []byte, nextBlk storage.BlockNumber) error {
		op := readOpaque(curSlot.Page())
		op.Next = nextBlk
		if len(highKey) > 0 {
			op.Flags |= BTHasHighKey
			op.HighKey = append([]byte(nil), highKey...)
		}
		writeOpaque(curSlot.Page(), op)
		result = append(result, bulkLink{blk: curBlk, highKey: highKey})
		err := bt.markDirtyWithPageRecord(curSlot, curBlk)
		curSlot.Unlock()
		bt.pool.Unpin(curSlot)
		prevBlk = curBlk
		curSlot = nil
		return err
	}

	for i, ri := range raws {
		if curSlot == nil {
			if err := startNewPage(prevBlk); err != nil {
				return nil, err
			}
		}

		// Capacity check: need room for a line pointer + the raw bytes.
		h := storage.MustHeader(curSlot.Page())
		free := int(h.Upper()) - int(h.Lower())
		const itemIDSize = 4
		if free < itemIDSize+len(ri.raw) {
			nextSlot, nextBlk, err := bt.pool.PinNew(bt.rel)
			if err != nil {
				curSlot.Unlock()
				bt.pool.Unpin(curSlot)
				return nil, fmt.Errorf("btree bulk raw: PinNew next: %w", err)
			}
			highKey := raws[i].key
			if err := flushPage(highKey, nextBlk); err != nil {
				bt.pool.Unpin(nextSlot)
				return nil, err
			}
			nextSlot.Lock()
			initPage(nextSlot.Page(), BTPageOpaque{
				Prev:  prevBlk,
				Next:  storage.InvalidBlockNumber,
				Level: level,
				Flags: flags,
			})
			curSlot = nextSlot
			curBlk = nextBlk
		}

		if _, err := storage.PageAddItemRaw(curSlot.Page(), ri.raw); err != nil {
			curSlot.Unlock()
			bt.pool.Unpin(curSlot)
			return nil, fmt.Errorf("btree bulk raw: PageAddItemRaw: %w", err)
		}
	}

	if curSlot != nil {
		if err := flushPage(nil, storage.InvalidBlockNumber); err != nil {
			return nil, err
		}
	}
	return result, nil
}
