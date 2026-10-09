package executor

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// M-NIGHTLY (AI-20260928-004845-004 follow-up) — any-height internal levels
// for the rebuilt system btrees.
//
// buildBulkSysBtreeLayout and its variable twin used to stop at ONE internal
// root. pg_class_relname_nsp_index's 80-byte downlinks fit ~97 per page, so
// once a catalog outgrew ~97 leaves (a long regress session creates that many
// relations) every CREATE failed with "internal-root overflow inserting
// downlink 97" — the rest of the session saw "relation does not exist".
// PostgreSQL's btree has no height limit: _bt_uppershutdown / _bt_buildadd
// (nbtsort.c) add internal levels until one page holds them all.
//
// layoutSysBtreeInternalLevels builds those levels bottom-up, in PG's page
// format:
//
//   - every internal page's FIRST downlink is minus infinity (its key is
//     truncated away, nbtsort.c _bt_sortaddtup);
//   - a non-rightmost page carries a high key at P_HIKEY (slot 1): the low
//     key of its right sibling, as a pivot tuple;
//   - sibling links (btpo_prev / btpo_next) chain each level; btpo_level is
//     the level number; only the single top page is BTP_ROOT.
//
// A child's LOW key is the key its parent's downlink carries: a leaf's first
// data tuple, and for an internal page the low key of its first child.
// Blocks are numbered after the leaves, level by level, so a two-level tree
// comes out byte-identical to the old single-root layout (root at
// nLeaves+1).

// sysBtreeLevelChild is one child page as its parent level sees it.
type sysBtreeLevelChild struct {
	block  uint32
	lowKey []byte // data-tuple form; nil for the leftmost child (minus infinity)
}

// layoutSysBtreeInternalLevels returns the internal pages (in block order,
// starting at firstBlock), the root block and the tree level of the root.
func layoutSysBtreeInternalLevels(children []sysBtreeLevelChild, firstBlock uint32, nkeyatts uint16) ([][]byte, uint32, uint32, error) {
	if len(children) < 2 {
		return nil, 0, 0, fmt.Errorf("internal levels need at least two children, got %d", len(children))
	}
	payload := storage.BlockSize - storage.SizeOfPageHeaderData - sizeOfBTPageOpaque
	var pages [][]byte
	next := firstBlock
	level := uint32(1)
	for {
		// Group this level's children into pages. A non-rightmost page
		// reserves room for its high key (the next page's first child's
		// low key, pivot form, same size as a keyed downlink).
		maxDownlink := 0
		for i, c := range children {
			size := sizeOfIndexTupleHdr
			if i > 0 {
				size = len(c.lowKey)
			}
			if size > maxDownlink {
				maxDownlink = size
			}
		}
		var groups [][]sysBtreeLevelChild
		pos := 0
		for pos < len(children) {
			// Everything left fits on one (rightmost) page?
			rest := sizeOfIndexTupleHdr + 4
			for i := pos + 1; i < len(children); i++ {
				rest += len(children[i].lowKey) + 4
			}
			if rest <= payload {
				groups = append(groups, children[pos:])
				break
			}
			budget := payload - (maxDownlink + 4)
			used := sizeOfIndexTupleHdr + 4 // first downlink is minus infinity
			end := pos + 1
			for end < len(children) && used+len(children[end].lowKey)+4 <= budget {
				used += len(children[end].lowKey) + 4
				end++
			}
			if end-pos < 2 && end < len(children) {
				return nil, 0, 0, fmt.Errorf("internal level %d: downlink too large for a page", level)
			}
			groups = append(groups, children[pos:end])
			pos = end
		}
		isRootLevel := len(groups) == 1
		var parents []sysBtreeLevelChild
		for gi, g := range groups {
			blk := next + uint32(gi)
			downlinks := make([][]byte, len(g))
			downlinks[0] = buildSysBtreeMinusInfDownlink(g[0].block)
			for i := 1; i < len(g); i++ {
				downlinks[i] = buildSysBtreeInternalDownlink(g[i].lowKey, g[i].block, nkeyatts)
			}
			var highKey []byte
			prev, nextSib := uint32(pNoneBlock), uint32(pNoneBlock)
			if gi > 0 {
				prev = blk - 1
			}
			if gi < len(groups)-1 {
				nextSib = blk + 1
				highKey = buildSysBtreeLeafHighKey(groups[gi+1][0].lowKey, nkeyatts)
			}
			page, err := buildSysBtreeInternalPage(downlinks, highKey, prev, nextSib, level, isRootLevel)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("internal level %d page %d: %w", level, gi, err)
			}
			pages = append(pages, page)
			parents = append(parents, sysBtreeLevelChild{block: blk, lowKey: g[0].lowKey})
		}
		next += uint32(len(groups))
		if isRootLevel {
			return pages, parents[0].block, level, nil
		}
		// The leftmost page of the next level up points at a page whose
		// low key is minus infinity too.
		parents[0].lowKey = nil
		children = parents
		level++
	}
}

// buildSysBtreeInternalPage assembles one internal btree page: an optional
// high key at P_HIKEY, then the downlinks (the first of which must already be
// minus infinity), with the given sibling links, level and root flag.
// With no high key, no siblings, level 1 and isRoot it is byte-identical to
// buildSysBtreeInternalRootPage.
func buildSysBtreeInternalPage(downlinks [][]byte, highKey []byte, prev, next uint32, level uint32, isRoot bool) ([]byte, error) {
	page := make([]byte, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return nil, err
	}
	h := storage.MustHeader(storage.Page(page))
	h.SetSpecial(uint16(storage.BlockSize - sizeOfBTPageOpaque))
	upper := storage.BlockSize - sizeOfBTPageOpaque
	lower := storage.SizeOfPageHeaderData
	write := func(item []byte, what string) error {
		if len(item)%8 != 0 {
			return fmt.Errorf("%s not MAXALIGN'd: len=%d", what, len(item))
		}
		newUpper := upper - len(item)
		if newUpper-lower < 4 {
			return fmt.Errorf("internal page overflow inserting %s", what)
		}
		copy(page[newUpper:upper], item)
		raw := uint32(uint16(newUpper)&0x7FFF) |
			(uint32(uint8(storage.ItemIDNormal)&0x3) << 15) |
			(uint32(uint16(len(item))&0x7FFF) << 17)
		binary.LittleEndian.PutUint32(page[lower:lower+4], raw)
		lower += 4
		upper = newUpper
		return nil
	}
	if highKey != nil {
		if err := write(highKey, "high key"); err != nil {
			return nil, err
		}
	}
	for i, dl := range downlinks {
		if err := write(dl, fmt.Sprintf("downlink %d", i)); err != nil {
			return nil, err
		}
	}
	h.SetLower(uint16(lower))
	h.SetUpper(uint16(upper))
	off := storage.BlockSize - sizeOfBTPageOpaque
	le := binary.LittleEndian
	le.PutUint32(page[off+0:off+4], prev)
	le.PutUint32(page[off+4:off+8], next)
	le.PutUint32(page[off+8:off+12], level)
	var flags uint16
	if isRoot {
		flags = btpRootFlag
	}
	le.PutUint16(page[off+12:off+14], flags)
	return page, nil
}

// firstDataSlot is the first data item of a btree page: 2 when the page
// carries a high key at P_HIKEY (it is not rightmost), else 1.
func firstDataSlot(page storage.Page) uint16 {
	if readLeafNextSibling(page) != pNoneBlock {
		return 2
	}
	return 1
}
