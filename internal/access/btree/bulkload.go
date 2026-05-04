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

	// Phase 1: build leaf level (blocks 1..L).
	leafLinks, err := bt.buildLevel(items, BTLeaf, 0)
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
