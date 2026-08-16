package btree

import (
	"fmt"
	"sort"

	"github.com/goopg/goopg/internal/storage"
)

// separatorReserve is the page-space budget the bulk loader holds back for the
// separator it will have to install when it closes the page (M0130-S11.4 slice
// 3b-3d; it replaced the constant `bulkHighKeyReserve`, a worst-case
// MaxHighKeyLen body).
//
// A page's high key is the FIRST KEY OF THE NEXT PAGE, so it does not exist
// while the page is being filled. The P_HIKEY line pointer is reserved up front
// by pgReserveHiKeySlot (upstream's pd_lower bump in _bt_blnewpage) and is
// therefore already outside `free` below, but its PAYLOAD is not: without a
// reserve a page can fill so completely that its own separator no longer fits.
//
// goopg can reserve EXACTLY where upstream cannot. `_bt_buildadd` is a streaming
// writer that has not seen the next tuple yet, so it reserves the worst case it
// can bound — MAXALIGN(sizeof(ItemPointerData)), the most _bt_truncate can add —
// and pays for the rest by MOVING the page's last tuple to the new page and
// overwriting its slot with the separator. goopg's loader has the whole sorted
// run in hand and does not move anything, so it forms the actual separator for
// the next boundary and reserves its body. `sep` is the very tuple flushPage
// will write, which is what makes the reserve exact rather than merely safe.
//
// `sep == nil` means the page will be the level's rightmost: no high key, no
// reserve (upstream's _bt_slideleft ending).
func (f indexFormat) separatorReserve(sep []byte) int {
	if sep == nil {
		return 0
	}
	return MaxAlign(f.bodySize(sep))
}

// pageHasSpaceForBulk is pageHasSpaceFor plus the reserve for `sep`, the
// separator that would be installed if the NEXT item started a new page.
//
// A fresh page always passes: CheckPGBTThirdPage caps a data item at
// BTMaxItemSize and a separator at BTMaxItemSizeNoHeapTid, and 2704+4 + 2712 is
// well under the 8148 bytes a freshly initialised page has left after its
// header, its special area and the reserved P_HIKEY line pointer. So the loader
// cannot loop allocating pages that reject their own first item.
func (f indexFormat) pageHasSpaceForBulk(p storage.Page, it item, sep []byte) bool {
	h := storage.MustHeader(p)
	free := int(h.Upper()) - int(h.Lower())
	return free >= f.itemEncodedSize(it)+f.separatorReserve(sep)
}

// BulkEntry is one (encoded-key, heap-pointer) pair for bulk index building.
// The key must already be encoded with the same EncodeXxx functions that the
// normal Insert path uses — BulkCreate treats keys as opaque byte slices and
// compares them with the tree's indexFormat (bytewise lexicographic order
// while the index has no key descriptor — see pgkeycmp.go).
type BulkEntry struct {
	Key []byte
	Ptr storage.ItemPointer
	// KeyDesc is the rendered "Key (cols)=(vals)" description for this
	// entry's key values, captured at entry-construction time so the
	// executor's post-sort duplicate walk can build PG's
	// `Key (…)=(…) is duplicated.` DETAIL (BuildIndexValueDescription,
	// postgres/src/backend/access/index/genam.c:178-276) without the
	// source heap row. Empty for non-unique builds (never read).
	KeyDesc string
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

// BulkCreateWithXID is BulkCreate with the creating transaction's xid stamped
// onto the index relfile's smgr-create WAL record (A9). CREATE INDEX passes
// ctx.Tx.XID.
func BulkCreateWithXID(pool *storage.Pool, rel storage.RelFileNode, entries []BulkEntry, xid storage.TransactionID) (*BTree, error) {
	return BulkCreateWithOptions(pool, rel, entries, Options{LogSplit: adaptPoolLogSplit(pool), CreateXID: xid})
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
			metaSlot.Unlock()
			pool.Unpin(metaSlot)
			return nil, err
		}
		rootSlot.Lock()
		initPage(rootSlot.Page(), BTPageOpaque{
			Prev: storage.InvalidBlockNumber, Next: storage.InvalidBlockNumber,
			Level: 0, Flags: BTLeaf | BTRoot,
		})
		if err := initMetaPage(metaSlot.Page(), rootBlk, 0); err != nil {
			rootSlot.Unlock()
			pool.Unpin(rootSlot)
			metaSlot.Unlock()
			pool.Unpin(metaSlot)
			return nil, err
		}
		_ = bt.markDirtyWithPageRecord(rootSlot, rootBlk)
		rootSlot.Unlock()
		pool.Unpin(rootSlot)
		_ = bt.markDirtyWithPageRecord(metaSlot, MetaBlock)
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
		return bt, nil
	}

	items := make([]item, len(entries))
	for i, e := range entries {
		items[i] = item{ptr: e.Ptr, key: append([]byte(nil), e.Key...)}
	}
	sort.SliceStable(items, func(i, j int) bool { return bt.format().compare(items[i].key, items[j].key) < 0 })

	// Build without dedup: each entry becomes a regular item.
	leafRaws := bt.format().itemsToRawItems(items)
	leafLinks, err := bt.buildLevelRaw(leafRaws, BTLeaf, 0)
	if err != nil {
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
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
				metaSlot.Unlock()
				pool.Unpin(metaSlot)
				return nil, err
			}
			level++
		}
		rootBlk = upLinks[0].blk
		rootLevel = level - 1
	}
	rootSlot2, err := bt.pinW(rootBlk)
	if err != nil {
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
		return nil, err
	}
	rootOp := readOpaque(rootSlot2.Page())
	rootOp.Flags |= BTRoot
	writeOpaque(rootSlot2.Page(), rootOp)
	_ = bt.markDirtyWithPageRecord(rootSlot2, rootBlk)
	bt.unpinW(rootSlot2)
	if err := initMetaPage(metaSlot.Page(), rootBlk, rootLevel); err != nil {
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
		return nil, err
	}
	_ = bt.markDirtyWithPageRecord(metaSlot, MetaBlock)
	metaSlot.Unlock()
	pool.Unpin(metaSlot)
	return bt, nil
}

// BulkCreateWithOptions is BulkCreate with explicit Options.
func BulkCreateWithOptions(pool *storage.Pool, rel storage.RelFileNode, entries []BulkEntry, opts Options) (*BTree, error) {
	bt := &BTree{pool: pool, rel: rel, logSplit: opts.LogSplit, keyFmt: indexFormat{desc: opts.KeyDesc}}

	// Ensure the relation file starts at block 0.  A previous failed
	// bulk build or WAL replay (recovery after a crash that left WAL
	// records without a committed catalog entry) can leave the
	// relation file with nblocks > 0 and a stale Manager.files
	// cache entry, causing PinNew to return a non-zero block.
	// Truncating + invalidating guarantees a clean slate.
	mgr := pool.Manager()
	if err := mgr.TruncateRelation(rel); err != nil {
		return nil, fmt.Errorf("btree bulk: truncate relation: %w", err)
	}
	pool.InvalidateRel(rel)

	// Block 0: metapage (identical layout to Create). A9: this creates the
	// index relfile — pass the creating xid for its smgr-create WAL record.
	metaSlot, metaBlk, err := pool.PinNewWithXID(rel, opts.CreateXID)
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
		if err := initMetaPage(metaSlot.Page(), rootBlk, 0); err != nil {
			rootSlot.Unlock()
			pool.Unpin(rootSlot)
			metaSlot.Unlock()
			pool.Unpin(metaSlot)
			return nil, err
		}
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
		items[i] = item{ptr: e.Ptr, key: append([]byte(nil), e.Key...)}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return bt.format().compare(items[i].key, items[j].key) < 0
	})

	// Phase 1: build leaf level with deduplication (M0047-0003).
	// Same-key entries are grouped into posting-list items so a key
	// with N duplicates occupies one page slot instead of N.
	leafRaws := deduplicateToRawItems(bt.format(), items)
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
	if err := initMetaPage(metaSlot.Page(), rootBlk, rootLevel); err != nil {
		metaSlot.Unlock()
		pool.Unpin(metaSlot)
		return nil, err
	}
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
	out[0] = downlinkItem(nil, links[0].blk)
	for i := 1; i < len(links); i++ {
		sep := links[i-1].highKey // separator = first key of this child = highKey of prev sibling
		out[i] = downlinkItem(append([]byte(nil), sep...), links[i].blk)
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
		// _bt_blnewpage: reserve the P_HIKEY line pointer before any data
		// item, because whether this page ends up rightmost is not known
		// until the level is finished. Data therefore starts at P_FIRSTKEY
		// and flushPage resolves the placeholder.
		if err := pgReserveHiKeySlot(slot.Page()); err != nil {
			slot.Unlock()
			bt.pool.Unpin(slot)
			return fmt.Errorf("btree bulk: reserve P_HIKEY at level %d: %w", level, err)
		}
		curSlot = slot
		curBlk = blk
		return nil
	}

	// flushPage writes the opaque area (setting Next + optional HighKey),
	// marks the page dirty, and releases it.
	flushPage := func(highKey []byte, nextBlk storage.BlockNumber) error {
		op := readOpaque(curSlot.Page())
		op.Next = nextBlk
		writeOpaque(curSlot.Page(), op)
		// _bt_buildadd's endgame: the P_HIKEY slot reserved by startNewPage
		// either receives the separator (non-rightmost page) or is slid away
		// again (_bt_slideleft) so the rightmost page of the level starts its
		// data at P_HIKEY like upstream expects.
		if len(highKey) > 0 {
			if err := pgSetHighKeyRaw(curSlot.Page(), bt.format().marshal(highKeyItem(highKey))); err != nil {
				curSlot.Unlock()
				bt.pool.Unpin(curSlot)
				curSlot = nil
				return err
			}
		} else if err := pgSlideLeft(curSlot.Page()); err != nil {
			curSlot.Unlock()
			bt.pool.Unpin(curSlot)
			curSlot = nil
			return err
		}
		result = append(result, bulkLink{blk: curBlk, highKey: highKey})
		err := bt.markDirtyWithPageRecord(curSlot, curBlk)
		curSlot.Unlock()
		bt.pool.Unpin(curSlot)
		prevBlk = curBlk
		curSlot = nil
		return err
	}

	// separatorAt(i) is the high key the page ending just before items[i] must
	// carry: the separator between the last item on it and the first item of the
	// next page. `_bt_buildadd` truncates that separator on a LEAF level only
	// (nbtsort.c: the internal levels re-use the pivot the level below produced),
	// which is also what truncateSeparator does when handed a pivot operand — the
	// level check here is the readable half of the same statement.
	// M0130-S11.4 slice 3b-3c.
	separatorAt := func(i int) []byte {
		if i <= 0 || i >= len(items) {
			return nil
		}
		if flags&BTLeaf != 0 {
			return bt.format().truncateSeparator(items[i-1], items[i])
		}
		return items[i].key
	}

	// pendingSep is separatorAt(i) for the item about to be examined, computed
	// one iteration earlier so the space check and the flush that consumes the
	// reserve agree on the exact bytes rather than on two independent estimates
	// (3b-3d). nil on the first item and past the last: no boundary there.
	var pendingSep []byte

	for i, it := range items {
		// Allocate a page if we don't have one open.
		if curSlot == nil {
			if err := startNewPage(prevBlk); err != nil {
				return nil, err
			}
		}

		// _bt_buildadd's `_bt_check_third_page` call.
		if err := CheckPGBTThirdPage(flags&BTLeaf != 0, MaxAlign(bt.format().bodySize(it.key))); err != nil {
			curSlot.Unlock()
			bt.pool.Unpin(curSlot)
			return nil, err
		}
		nextSep := separatorAt(i + 1)

		if !bt.format().pageHasSpaceForBulk(curSlot.Page(), it, nextSep) {
			// Current page is full. Allocate the next page first so we can
			// write its block number into the current page's Next field.
			nextSlot, nextBlk, err := bt.pool.PinNew(bt.rel)
			if err != nil {
				curSlot.Unlock()
				bt.pool.Unpin(curSlot)
				return nil, fmt.Errorf("btree bulk: PinNew next at level %d: %w", level, err)
			}

			// The separator the page reserved space for while it was filling.
			highKey := pendingSep
			if highKey == nil {
				// i == 0: a fresh page rejecting its very first item. Bounded
				// away by pageHasSpaceForBulk's size argument, but a wrong
				// answer here would be a silent structural break, so fall back
				// to the untruncated key rather than write no high key at all.
				highKey = items[i].key
			}

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
			if err := pgReserveHiKeySlot(nextSlot.Page()); err != nil {
				nextSlot.Unlock()
				bt.pool.Unpin(nextSlot)
				return nil, fmt.Errorf("btree bulk: reserve P_HIKEY at level %d: %w", level, err)
			}
			curSlot = nextSlot
			curBlk = nextBlk
		}

		// Append item to the current page.
		if _, err := storage.PageAddItemRaw(curSlot.Page(), bt.format().marshal(it)); err != nil {
			curSlot.Unlock()
			bt.pool.Unpin(curSlot)
			return nil, fmt.Errorf("btree bulk: PageAddItemRaw: %w", err)
		}
		pendingSep = nextSep
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
func (f indexFormat) itemsToRawItems(items []item) []rawItem {
	out := make([]rawItem, len(items))
	for i, it := range items {
		out[i] = rawItem{raw: f.marshal(it), key: it.key}
	}
	return out
}

// maxRawItemSize bounds an individual on-page item so that
//  1. it satisfies storage.PageAddItemRaw's 15-bit Length field (0x7FFF), and
//  2. it physically fits on an 8 KiB page after page header (24 B), B-tree
//     opaque trailer (272 B, includes 256-byte HighKey reserve) and the new
//     line pointer (4 B). 8192 - 24 - 272 - 4 = 7892 — round down for safety.
//
// M0053-0005 uses this constant to split oversized posting-list runs into
// multiple smaller posting items so each fits on a single page.
const maxRawItemSize = 7800

// deduplicateToRawItems groups consecutive same-key items into
// posting-list rawItems (M0047-0003). Items must already be sorted by key.
//
//   - A run of 1: marshalled as a normal item (8+keyLen bytes).
//   - A run of N≥2: marshalled as an upstream posting-list tuple
//     (8+keyLen bytes then N×6 bytes of ItemPointerData).
//
// M0053-0005: When a single run would produce a posting item exceeding
// the 15-bit page line-pointer limit (0x7FFF bytes), the run is split
// into multiple posting items each containing a chunk of TIDs.
// PostgreSQL's btree readers handle multiple same-key items on a page
// without issue (they iterate items sequentially), so correctness is
// preserved at the cost of slightly higher index size for very
// high-cardinality keys (e.g. LINEITEM(L_LINENUMBER) at TPC-H SF=1
// where one value can have ~1.5M duplicates).
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-vi: the run is closed by
// `compareKeyAttrs`, not by the tree's ORDERING. The two are the same function
// in the blob format, so nothing here moves today — but they are not the same
// question, and under the tuple format the ordering breaks ties on the heap
// TID, which no two entries of a heapkeyspace tree ever share. Grouping by the
// ordering would therefore close every run at length 1 and silently disable
// deduplication altogether: same rows, same order, one line pointer per TID,
// an index several times its proper size. That is invisible to every
// row-count gate, which is why it gets a structural guard
// (pgposting_format_test.go) instead. Upstream draws the same line:
// `_bt_load` closes a posting run with `_bt_keep_natts_fast` (nbtutils.c),
// never with `_bt_compare`.
func deduplicateToRawItems(f indexFormat, items []item) []rawItem {
	out := make([]rawItem, 0, len(items))
	i := 0
	for i < len(items) {
		j := i + 1
		for j < len(items) && f.compareKeyAttrs(items[j].key, items[i].key) == 0 {
			j++
		}
		key := items[i].key
		if j-i == 1 {
			out = append(out, rawItem{raw: f.marshal(items[i]), key: key})
		} else {
			tids := make([]storage.ItemPointer, j-i)
			for k := i; k < j; k++ {
				tids[k-i] = items[k].ptr
			}
			// Compute the maximum number of TIDs that fit in one posting
			// item: the bytes left after the key material at 6 bytes each
			// (the TID array starts at the posting offset). The offset is
			// format-dependent — 8+len(key) for a blob payload, but
			// MAXALIGN(len(key)) for a tuple, which already includes its
			// own header — so it is asked rather than recomputed here.
			maxTIDsPerChunk := (maxRawItemSize - f.postingOffsetFor(key)) / 6
			if maxTIDsPerChunk < 1 {
				// Pathological: key alone exceeds the page limit. Fall
				// back to single-item-per-TID encoding so each entry
				// still fits (it.marshal() uses 8+keyLen bytes; if even
				// that overflows, the underlying PageAddItemRaw will
				// reject and the caller sees a clean error).
				for k := i; k < j; k++ {
					out = append(out, rawItem{raw: f.marshal(items[k]), key: key})
				}
			} else if len(tids) <= maxTIDsPerChunk {
				out = append(out, rawItem{raw: f.marshalPosting(key, tids), key: key})
			} else {
				for off := 0; off < len(tids); off += maxTIDsPerChunk {
					end := off + maxTIDsPerChunk
					if end > len(tids) {
						end = len(tids)
					}
					chunk := tids[off:end]
					if len(chunk) == 1 {
						// A posting list is defined to hold >= 2 TIDs
						// (nbtree.h's BTreeTupleSetPosting asserts it), so a
						// trailing one-TID remainder goes back to a plain
						// item rather than a degenerate posting tuple.
						out = append(out, rawItem{raw: f.marshal(item{ptr: chunk[0], key: key}), key: key})
						continue
					}
					out = append(out, rawItem{raw: f.marshalPosting(key, chunk), key: key})
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
		// _bt_blnewpage: reserve the P_HIKEY line pointer before any data
		// item, because whether this page ends up rightmost is not known
		// until the level is finished. Data therefore starts at P_FIRSTKEY
		// and flushPage resolves the placeholder.
		if err := pgReserveHiKeySlot(slot.Page()); err != nil {
			slot.Unlock()
			bt.pool.Unpin(slot)
			return fmt.Errorf("btree bulk: reserve P_HIKEY at level %d: %w", level, err)
		}
		curSlot = slot
		curBlk = blk
		return nil
	}

	flushPage := func(highKey []byte, nextBlk storage.BlockNumber) error {
		op := readOpaque(curSlot.Page())
		op.Next = nextBlk
		writeOpaque(curSlot.Page(), op)
		// _bt_buildadd's endgame: the P_HIKEY slot reserved by startNewPage
		// either receives the separator (non-rightmost page) or is slid away
		// again (_bt_slideleft) so the rightmost page of the level starts its
		// data at P_HIKEY like upstream expects.
		if len(highKey) > 0 {
			if err := pgSetHighKeyRaw(curSlot.Page(), bt.format().marshal(highKeyItem(highKey))); err != nil {
				curSlot.Unlock()
				bt.pool.Unpin(curSlot)
				curSlot = nil
				return err
			}
		} else if err := pgSlideLeft(curSlot.Page()); err != nil {
			curSlot.Unlock()
			bt.pool.Unpin(curSlot)
			curSlot = nil
			return err
		}
		result = append(result, bulkLink{blk: curBlk, highKey: highKey})
		err := bt.markDirtyWithPageRecord(curSlot, curBlk)
		curSlot.Unlock()
		bt.pool.Unpin(curSlot)
		prevBlk = curBlk
		curSlot = nil
		return err
	}

	// `_bt_buildadd`'s separator, truncated on a leaf level exactly as in
	// buildLevel above (M0130-S11.4 slice 3b-3c). This is the path where the
	// tiebreaker branch earns its keep: a duplicate run chunked across pages puts
	// two entries with IDENTICAL key attributes on either side of the boundary,
	// and a separator without lastleft's heap TID would sort BELOW the left
	// page's own entries.
	separatorAt := func(i int) []byte {
		if i <= 0 || i >= len(raws) {
			return nil
		}
		if flags&BTLeaf != 0 {
			return bt.format().truncateSeparator(
				bt.format().rawSeparatorOperand(raws[i-1]),
				item{key: raws[i].key})
		}
		return raws[i].key
	}
	// See buildLevel: computed one iteration ahead so the reserve and the flush
	// that spends it are the same bytes (3b-3d).
	var pendingSep []byte

	for i, ri := range raws {
		if curSlot == nil {
			if err := startNewPage(prevBlk); err != nil {
				return nil, err
			}
		}

		// _bt_buildadd's `_bt_check_third_page` call. The raw path's item is
		// already marshalled, so its body is exactly len(ri.raw).
		//
		// POSTING TUPLES ARE EXEMPT, and that exemption is a recorded gap, not
		// a design choice. Upstream never has to check them because the writer
		// that builds them bounds them first: `_bt_dedup_pass` caps a posting
		// list at `dstate->maxpostingsize` (<= BTMaxItemSize, nbtdedup.c) and
		// starts a new one when the cap is reached. goopg's dedup
		// (deduplicateToRawItems) has no such cap, so it hands this loop
		// posting tuples of several thousand bytes; rejecting them here would
		// break bulk index creation on duplicate-heavy columns rather than fix
		// anything. See the deferral ledger — the fix belongs to the missing
		// `_bt_dedup_pass`, not to the size gate.
		if !BTreeTupleIsPosting(ri.raw) {
			if err := CheckPGBTThirdPage(flags&BTLeaf != 0, MaxAlign(len(ri.raw))); err != nil {
				curSlot.Unlock()
				bt.pool.Unpin(curSlot)
				return nil, err
			}
		}
		nextSep := separatorAt(i + 1)

		// Capacity check: need room for a line pointer + the raw bytes, plus the
		// body of the separator this page will have to carry.
		h := storage.MustHeader(curSlot.Page())
		free := int(h.Upper()) - int(h.Lower())
		const itemIDSize = 4
		if free < itemIDSize+MaxAlign(len(ri.raw))+bt.format().separatorReserve(nextSep) {
			nextSlot, nextBlk, err := bt.pool.PinNew(bt.rel)
			if err != nil {
				curSlot.Unlock()
				bt.pool.Unpin(curSlot)
				return nil, fmt.Errorf("btree bulk raw: PinNew next: %w", err)
			}
			highKey := pendingSep
			if highKey == nil {
				highKey = raws[i].key // i == 0; see buildLevel
			}
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
			if err := pgReserveHiKeySlot(nextSlot.Page()); err != nil {
				nextSlot.Unlock()
				bt.pool.Unpin(nextSlot)
				return nil, fmt.Errorf("btree bulk raw: reserve P_HIKEY at level %d: %w", level, err)
			}
			curSlot = nextSlot
			curBlk = nextBlk
		}

		if _, err := storage.PageAddItemRaw(curSlot.Page(), ri.raw); err != nil {
			curSlot.Unlock()
			bt.pool.Unpin(curSlot)
			return nil, fmt.Errorf("btree bulk raw: PageAddItemRaw: %w", err)
		}
		pendingSep = nextSep
	}

	if curSlot != nil {
		if err := flushPage(nil, storage.InvalidBlockNumber); err != nil {
			return nil, err
		}
	}
	return result, nil
}
