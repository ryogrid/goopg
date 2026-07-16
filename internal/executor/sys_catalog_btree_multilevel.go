package executor

// Multi-level descent + insert / rebuild for the PG18-canonical system btrees
// touched by user CREATE TABLE (M0106-0010 batched-41).
//
// Background (batched-40 diagnosis): production bootstrap of
// `pg_class_relname_nsp_index` (OID 2663) produces a multi-level btree (meta
// + 2 leaves + internal root) because the 161 catalog entries × 80 B/tuple
// overflow the single-leaf cap (~97 tuples). The legacy
// `insertCanonicalSysBtreeLeaf` hard-coded block 1 as the leaf-root and
// `splitLeafRootAndInsert` re-rooted block 1 in place — orphaning the
// already-existing sibling leaf (block 2) and original internal root (block
// 3). PG-standby parse-analyze of `public.bench_log` then 42P01s because the
// runtime entry sits on an unreachable leaf.
//
// This file adds the navigation primitives needed for correct multi-level
// inserts:
//
//   - readSysBtreeMeta: parse btm_root / btm_level from the metapage.
//   - descendSysBtreeToLeaf: walk internal pages to the target leaf.
//   - collectAllLeafTuples: descend leftmost then follow btpo_next across
//     every leaf, returning data tuples (high keys are skipped).
//   - rebuildSysBtreeWithNewEntry: union existing tuples with the new tuple,
//     run a fresh bulk-build layout, and overwrite pages 0..N-1 in place.
//   - buildBulkSysBtreeLayout: in-package mirror of
//     `internal/initdb/btree_index_bootstrap.go::pgBuildBtreeBulkLoadSized`,
//     duplicated because executor → initdb would form an import cycle.
//
// `insertCanonicalSysBtreeLeaf` (sys_catalog_index_insert.go) dispatches on
// btm_level: the single-leaf-root branch keeps the lightweight
// `splitLeafRootAndInsert` path; the multi-level branch descends, attempts
// an in-place insert into the target leaf, and falls back to a full rebuild
// when the leaf is full (the only case that requires propagating a downlink
// up the parent chain).

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// readSysBtreeMeta returns (btm_root, btm_level) from the metapage at block
// 0 of the system btree relation.
func readSysBtreeMeta(ctx *Context, rel storage.RelFileNode) (rootBlk uint32, level uint32, err error) {
	slot, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if perr != nil {
		return 0, 0, fmt.Errorf("pin metapage: %w", perr)
	}
	slot.Lock()
	page := slot.Page()
	le := binary.LittleEndian
	base := storage.SizeOfPageHeaderData
	rootBlk = le.Uint32(page[base+8 : base+12])
	level = le.Uint32(page[base+12 : base+16])
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	return rootBlk, level, nil
}

// decodeChildFromTID extracts the child block number from the ItemPointer
// header (bytes 0..4) of an internal-page downlink.
func decodeChildFromTID(raw []byte) uint32 {
	le := binary.LittleEndian
	hi := le.Uint16(raw[0:2])
	lo := le.Uint16(raw[2:4])
	return (uint32(hi) << 16) | uint32(lo)
}

// readLeafNextSibling returns btpo_next from the special-area trailer of the
// passed page. P_NONE (0) means the leaf is rightmost.
func readLeafNextSibling(page storage.Page) uint32 {
	off := storage.BlockSize - sizeOfBTPageOpaque
	return binary.LittleEndian.Uint32(page[off+4 : off+8])
}

// descendSysBtreeToLeaf walks internal pages from the metapage's root down
// to the leaf that should hold newKey, choosing the largest downlink slot
// whose key is ≤ newKey at each level (and slot 1's minus-infinity downlink
// when newKey is smaller than every key on the page).
func descendSysBtreeToLeaf(ctx *Context, rel storage.RelFileNode, rootBlk uint32, level uint32, newKey []byte, cmp keyCompareFn) (storage.BlockNumber, error) {
	if level == 0 {
		return storage.BlockNumber(rootBlk), nil
	}
	cur := rootBlk
	for lvl := int(level); lvl >= 1; lvl-- {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(cur)})
		if err != nil {
			return 0, fmt.Errorf("pin internal blk %d: %w", cur, err)
		}
		slot.Lock()
		page := slot.Page()
		count, cerr := storage.PageLinePointerCount(page)
		if cerr != nil {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			return 0, fmt.Errorf("line ptr count blk %d: %w", cur, cerr)
		}
		// Slot 1 holds the minus-infinity downlink; its child is the
		// leftmost candidate. Subsequent slots hold real (key, child)
		// pairs; pick the last slot whose key is ≤ newKey.
		raw1, err := storage.PageGetItemRawNoCopy(page, 1)
		if err != nil {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			return 0, fmt.Errorf("read slot 1 of blk %d: %w", cur, err)
		}
		next := decodeChildFromTID(raw1)
		for i := uint16(2); i <= uint16(count); i++ {
			raw, err := storage.PageGetItemRawNoCopy(page, i)
			if err != nil {
				slot.Unlock()
				ctx.Pool.Unpin(slot)
				return 0, fmt.Errorf("read slot %d of blk %d: %w", i, cur, err)
			}
			if len(raw) <= sysIndexTupleHoff {
				continue
			}
			downlinkKey := raw[sysIndexTupleHoff:]
			if cmp(newKey, downlinkKey) < 0 {
				break
			}
			next = decodeChildFromTID(raw)
		}
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		cur = next
	}
	return storage.BlockNumber(cur), nil
}

// collectAllLeafTuples descends to the leftmost leaf via slot-1 minus-
// infinity downlinks and follows the btpo_next chain across every leaf,
// returning all data tuples (slot 1's P_HIKEY is skipped on non-rightmost
// leaves). Tuples are returned in btree-sorted order because the chain
// itself is sorted.
func collectAllLeafTuples(ctx *Context, rel storage.RelFileNode, rootBlk uint32, level uint32, meta btreeIndexKeyMeta) ([][]byte, error) {
	cur := rootBlk
	// Descend leftmost: slot 1 at every internal level is the minus-
	// infinity downlink to the leftmost child.
	for lvl := int(level); lvl >= 1; lvl-- {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(cur)})
		if err != nil {
			return nil, fmt.Errorf("pin internal blk %d: %w", cur, err)
		}
		slot.Lock()
		page := slot.Page()
		raw, err := storage.PageGetItemRawNoCopy(page, 1)
		if err != nil {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			return nil, fmt.Errorf("read slot 1 of blk %d: %w", cur, err)
		}
		next := decodeChildFromTID(raw)
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		cur = next
	}
	var tuples [][]byte
	for cur != pNoneBlock {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(cur)})
		if err != nil {
			return nil, fmt.Errorf("pin leaf blk %d: %w", cur, err)
		}
		slot.Lock()
		page := slot.Page()
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			return nil, fmt.Errorf("line ptr count leaf blk %d: %w", cur, err)
		}
		btpoNext := readLeafNextSibling(page)
		startSlot := uint16(1)
		if btpoNext != pNoneBlock {
			startSlot = 2 // skip P_HIKEY
		}
		for i := startSlot; i <= uint16(count); i++ {
			raw, err := storage.PageGetItemRawNoCopy(page, i)
			if err != nil {
				slot.Unlock()
				ctx.Pool.Unpin(slot)
				return nil, fmt.Errorf("read slot %d of leaf blk %d: %w", i, cur, err)
			}
			if len(raw) != meta.tupleSize {
				continue
			}
			buf := make([]byte, len(raw))
			copy(buf, raw)
			tuples = append(tuples, buf)
		}
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		cur = btpoNext
	}
	return tuples, nil
}

// buildBulkSysBtreeLayout mirrors
// `internal/initdb/btree_index_bootstrap.go::pgBuildBtreeBulkLoadSized`,
// duplicated here because executor → initdb would form an import cycle.
// Returns a contiguous byte slice containing the full on-disk image of the
// new index file: meta (block 0), one or more leaves (blocks 1..nLeaves),
// and an optional internal root (block nLeaves+1) when the tree spans more
// than a single leaf-root.
func buildBulkSysBtreeLayout(sortedTuples [][]byte, tupleSize int, nkeyatts uint16) ([]byte, error) {
	for i, t := range sortedTuples {
		if len(t) != tupleSize {
			return nil, fmt.Errorf("bulk layout: tuple %d size=%d, want %d", i, len(t), tupleSize)
		}
	}
	const pageHeader = storage.SizeOfPageHeaderData
	leafPayloadBytes := storage.BlockSize - pageHeader - sizeOfBTPageOpaque
	bytesPerSlot := tupleSize + 4 // tuple + ItemId
	maxPerSingle := leafPayloadBytes / bytesPerSlot
	maxPerNonRM := (leafPayloadBytes - bytesPerSlot) / bytesPerSlot

	if len(sortedTuples) <= maxPerSingle {
		// Single leaf-root: meta(0) + leaf-root(1).
		leaf, err := buildSysBtreeLeafPage(sortedTuples, nil, pNoneBlock, pNoneBlock, true)
		if err != nil {
			return nil, fmt.Errorf("bulk layout: leaf-root: %w", err)
		}
		meta := make([]byte, storage.BlockSize)
		if err := writeSysBtreeMetapageInPlace(meta, 1, 0); err != nil {
			return nil, fmt.Errorf("bulk layout: meta: %w", err)
		}
		out := make([]byte, 0, 2*storage.BlockSize)
		out = append(out, meta...)
		out = append(out, leaf...)
		return out, nil
	}

	// Multi-leaf layout: pack leaves of `maxPerNonRM` tuples each until
	// the remainder fits in one rightmost leaf (≤ maxPerSingle).
	var leafGroups [][][]byte
	pos := 0
	for pos < len(sortedTuples) {
		remaining := len(sortedTuples) - pos
		if remaining <= maxPerSingle {
			leafGroups = append(leafGroups, sortedTuples[pos:])
			break
		}
		end := pos + maxPerNonRM
		leafGroups = append(leafGroups, sortedTuples[pos:end])
		pos = end
	}
	nLeaves := len(leafGroups)
	leaves := make([][]byte, nLeaves)
	for li, group := range leafGroups {
		isRightmost := li == nLeaves-1
		prev := uint32(pNoneBlock)
		next := uint32(pNoneBlock)
		if li > 0 {
			prev = uint32(li) // previous leaf block = li (1-based: leaves at 1..nLeaves)
		}
		if !isRightmost {
			next = uint32(li + 2) // next leaf block = li+2
		}
		var highKey []byte
		if !isRightmost {
			highKey = buildSysBtreeLeafHighKey(leafGroups[li+1][0], nkeyatts)
		}
		page, err := buildSysBtreeLeafPage(group, highKey, prev, next, false)
		if err != nil {
			return nil, fmt.Errorf("bulk layout: leaf %d: %w", li, err)
		}
		leaves[li] = page
	}
	downlinks := make([][]byte, nLeaves)
	downlinks[0] = buildSysBtreeMinusInfDownlink(1)
	for li := 1; li < nLeaves; li++ {
		downlinks[li] = buildSysBtreeInternalDownlink(leafGroups[li][0], uint32(li+1), nkeyatts)
	}
	root, err := buildSysBtreeInternalRootPage(downlinks)
	if err != nil {
		return nil, fmt.Errorf("bulk layout: internal root: %w", err)
	}
	rootBlock := uint32(nLeaves + 1)
	meta := make([]byte, storage.BlockSize)
	if err := writeSysBtreeMetapageInPlace(meta, rootBlock, 1); err != nil {
		return nil, fmt.Errorf("bulk layout: meta (multi): %w", err)
	}
	out := make([]byte, 0, (nLeaves+2)*storage.BlockSize)
	out = append(out, meta...)
	for _, leaf := range leaves {
		out = append(out, leaf...)
	}
	out = append(out, root...)
	return out, nil
}

// rebuildSysBtreeWithNewEntry is the fallback when an in-place leaf insert
// returns ErrNoSpaceInPage on a multi-level tree. It re-collects every data
// tuple in the index, merges the new tuple in sorted order, runs the bulk-
// build layout, and overwrites pages 0..N-1 in place via the buffer pool.
//
// If the new layout has fewer pages than the existing on-disk relation, the
// trailing pages remain on disk but are unreachable from the (rewritten)
// metapage — equivalent to dead pages, harmless for redo and for PG's
// post-replay relcache.
func rebuildSysBtreeWithNewEntry(ctx *Context, indexOID uint32, rel storage.RelFileNode, newTuple []byte, cmp keyCompareFn) error {
	keyMeta, ok := keyMetaForSysBtree(indexOID)
	if !ok {
		return fmt.Errorf("rebuild: unsupported OID %d", indexOID)
	}
	if len(newTuple) != keyMeta.tupleSize {
		return fmt.Errorf("rebuild: newTuple size %d, want %d", len(newTuple), keyMeta.tupleSize)
	}
	rootBlk, level, err := readSysBtreeMeta(ctx, rel)
	if err != nil {
		return fmt.Errorf("rebuild: read meta: %w", err)
	}
	existing, err := collectAllLeafTuples(ctx, rel, rootBlk, level, keyMeta)
	if err != nil {
		return fmt.Errorf("rebuild: collect leaves: %w", err)
	}
	merged, _ := mergeSortedSlice(existing, newTuple, cmp)
	imageBytes, err := buildBulkSysBtreeLayout(merged, keyMeta.tupleSize, keyMeta.nkeyatts)
	if err != nil {
		return fmt.Errorf("rebuild: bulk-build: %w", err)
	}
	nPages := len(imageBytes) / storage.BlockSize
	if nPages*storage.BlockSize != len(imageBytes) {
		return fmt.Errorf("rebuild: image size %d not page-aligned", len(imageBytes))
	}

	curBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return fmt.Errorf("rebuild: nblocks: %w", err)
	}

	for blk := 0; blk < nPages; blk++ {
		var slot *storage.Slot
		if storage.BlockNumber(blk) < curBlocks {
			s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(blk)})
			if perr != nil {
				return fmt.Errorf("rebuild: pin blk %d: %w", blk, perr)
			}
			slot = s
		} else {
			s, newBlk, perr := ctx.Pool.PinNew(rel)
			if perr != nil {
				return fmt.Errorf("rebuild: extend at blk %d: %w", blk, perr)
			}
			if int(newBlk) != blk {
				ctx.Pool.Unpin(s)
				return fmt.Errorf("rebuild: PinNew returned blk %d, expected %d", newBlk, blk)
			}
			slot = s
		}
		slot.Lock()
		src := imageBytes[blk*storage.BlockSize : (blk+1)*storage.BlockSize]
		copy(slot.Page(), src)
		ctx.Pool.MarkDirty(slot)
		slot.Unlock()
		ctx.Pool.Unpin(slot)
	}
	return nil
}

// insertIntoExistingLeaf inserts indexTuple into the leaf page at leafBlk,
// preserving the leaf's existing high-key (slot 1 on non-rightmost leaves).
// Returns ErrNoSpaceInPage on overflow so the caller can fall back to a full
// rebuild.
func insertIntoExistingLeaf(ctx *Context, indexOID uint32, rel storage.RelFileNode, leafBlk storage.BlockNumber, indexTuple []byte, cmp keyCompareFn) error {
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: leafBlk})
	if err != nil {
		return fmt.Errorf("pin leaf blk %d: %w", leafBlk, err)
	}
	slot.Lock()
	page := slot.Page()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return fmt.Errorf("line pointer count leaf blk %d: %w", leafBlk, err)
	}
	hasHighKey := readLeafNextSibling(page) != pNoneBlock
	dataStart := uint16(1)
	if hasHighKey {
		dataStart = 2
	}
	newKey := indexTuple[sysIndexTupleHoff:]
	insertSlot := uint16(count + 1)
	for i := dataStart; i <= uint16(count); i++ {
		raw, err := storage.PageGetItemRawNoCopy(page, i)
		if err != nil {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			return fmt.Errorf("read item slot %d leaf %d: %w", i, leafBlk, err)
		}
		if len(raw) <= sysIndexTupleHoff {
			continue
		}
		existingKey := raw[sysIndexTupleHoff:]
		if cmp(newKey, existingKey) < 0 {
			insertSlot = i
			break
		}
	}
	if _, err := storage.PageInsertItemRawAt(page, insertSlot, indexTuple); err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	ctx.Pool.MarkDirty(slot)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	_ = indexOID // retained for log/error context if needed in the future.
	return nil
}
