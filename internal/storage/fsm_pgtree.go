package storage

// fsmTree is PostgreSQL's free space map as a search structure: the
// three-level tree of FSM pages, each page a binary max-tree of one-byte
// categories with a fp_next_slot search hint (freespace.c, fsmpage.c;
// M0146-0216). The flat FSM.pages array answers goopg's own first-fit
// lookups; this tree answers the lookups that must choose the same page PG
// chooses — RelationGetBufferForTuple's GetPageWithFreeSpace and
// RecordAndGetPageWithFreeSpace — because which qualifying page PG returns
// depends on state the flat array does not keep:
//
//   - a leaf page searches from its fp_next_slot, rightwards with wraparound,
//     and leaves the hint just past the slot it returned;
//   - RecordAndGetPageWithFreeSpace looks on the old page's own leaf first;
//   - the upper levels are NOT maintained by RecordPageWithFreeSpace: they move
//     only when FreeSpaceMapVacuumRange recomputes them (and fsm_vacuum_page
//     resets the hint of every page it visits to 0), or when fsm_search finds a
//     stale upper slot and corrects it.
//
// Pages are addressed logically (level, logpageno) as in freespace.c. A page
// "exists" when its physical block is below the fork's size, which grows only
// through fsm_readbuf(extend=true) — fsm_set_and_search — so a search sees the
// same missing pages PG's would.
type fsmTree struct {
	pages map[fsmAddr]*fsmTreePage
	// nblocks is the FSM fork's size in physical pages (smgrnblocks).
	nblocks uint32
}

type fsmAddr struct {
	level    int
	logpageno uint32
}

type fsmTreePage struct {
	nextSlot int
	nodes    [fsmNodesPerPage]uint8
}

const (
	// fsmTreeDepth is FSM_TREE_DEPTH: three levels address 2^32 heap blocks
	// when a page holds at least 1626 slots.
	fsmTreeDepth   = 3
	fsmRootLevel   = fsmTreeDepth - 1
	fsmBottomLevel = 0
	// pgMaxFSMRequestSize is MaxFSMRequestSize = MaxHeapTupleSize
	// (BLCKSZ - MAXALIGN(SizeOfPageHeaderData + sizeof(ItemIdData))).
	pgMaxFSMRequestSize = BlockSize - (SizeOfPageHeaderData+4+7)&^7
	// fsmTreeMaxRestarts is fsm_search's restart bound.
	fsmTreeMaxRestarts = 10000
)

var fsmRootAddress = fsmAddr{level: fsmRootLevel}

func newFSMTree() *fsmTree { return &fsmTree{pages: make(map[fsmAddr]*fsmTreePage)} }

// fsmAvailToCat is fsm_space_avail_to_cat.
func fsmAvailToCat(avail int) uint8 {
	if avail >= pgMaxFSMRequestSize {
		return 255
	}
	if avail < 0 {
		avail = 0
	}
	cat := avail / FSM_CAT_STEP
	if cat > 254 {
		cat = 254
	}
	return uint8(cat)
}

// fsmNeededToCat is fsm_space_needed_to_cat: requests round up.
func fsmNeededToCat(needed int) uint8 {
	if needed > pgMaxFSMRequestSize {
		needed = pgMaxFSMRequestSize
	}
	if needed <= 0 {
		return 1
	}
	cat := (needed + FSM_CAT_STEP - 1) / FSM_CAT_STEP
	if cat > 255 {
		cat = 255
	}
	return uint8(cat)
}

// fsmLogicalToPhysical is fsm_logical_to_physical.
func fsmLogicalToPhysical(addr fsmAddr) uint32 {
	leafno := uint64(addr.logpageno)
	for l := 0; l < addr.level; l++ {
		leafno *= fsmSlotsPerPage
	}
	var pages uint64
	for l := 0; l < fsmTreeDepth; l++ {
		pages += leafno + 1
		leafno /= fsmSlotsPerPage
	}
	pages -= uint64(addr.level)
	return uint32(pages - 1)
}

// fsmGetLocation is fsm_get_location.
func fsmGetLocation(heapBlk BlockNumber) (fsmAddr, int) {
	return fsmAddr{level: fsmBottomLevel, logpageno: uint32(heapBlk) / fsmSlotsPerPage}, int(uint32(heapBlk) % fsmSlotsPerPage)
}

// fsmGetHeapBlk is fsm_get_heap_blk.
func fsmGetHeapBlk(addr fsmAddr, slot int) BlockNumber {
	return BlockNumber(addr.logpageno*fsmSlotsPerPage + uint32(slot))
}

// fsmGetParent is fsm_get_parent.
func fsmGetParent(child fsmAddr) (fsmAddr, int) {
	return fsmAddr{level: child.level + 1, logpageno: child.logpageno / fsmSlotsPerPage}, int(child.logpageno % fsmSlotsPerPage)
}

// fsmGetChild is fsm_get_child.
func fsmGetChild(parent fsmAddr, slot int) fsmAddr {
	return fsmAddr{level: parent.level - 1, logpageno: parent.logpageno*fsmSlotsPerPage + uint32(slot)}
}

// readbuf is fsm_readbuf: nil when the page lies past the fork's end and
// extend is false; extending creates every page up to it (zeroed, as
// fsm_extend's new pages are).
func (t *fsmTree) readbuf(addr fsmAddr, extend bool) *fsmTreePage {
	phys := fsmLogicalToPhysical(addr)
	if phys >= t.nblocks {
		if !extend {
			return nil
		}
		t.nblocks = phys + 1
	}
	p := t.pages[addr]
	if p == nil {
		p = &fsmTreePage{}
		t.pages[addr] = p
	}
	return p
}

func fsmParentOf(x int) int  { return (x - 1) / 2 }
func fsmLeftChild(x int) int { return 2*x + 1 }

// fsmRightNeighbor is fsmpage.c's rightneighbor: the next node on the same
// level, wrapping to the level's first node.
func fsmRightNeighbor(x int) int {
	x++
	if ((x + 1) & x) == 0 {
		x = fsmParentOf(x)
	}
	return x
}

// setAvail is fsm_set_avail.
func (p *fsmTreePage) setAvail(slot int, value uint8) bool {
	nodeno := fsmNonLeafNodes + slot
	old := p.nodes[nodeno]
	if old == value && value <= p.nodes[0] {
		return false
	}
	p.nodes[nodeno] = value
	for nodeno > 0 {
		nodeno = fsmParentOf(nodeno)
		l := fsmLeftChild(nodeno)
		nv := p.nodes[l]
		if r := l + 1; r < fsmNodesPerPage {
			nv = max(nv, p.nodes[r])
		}
		if p.nodes[nodeno] == nv {
			break
		}
		p.nodes[nodeno] = nv
	}
	if value > p.nodes[0] {
		p.rebuild()
	}
	return true
}

func (p *fsmTreePage) getAvail(slot int) uint8 { return p.nodes[fsmNonLeafNodes+slot] }
func (p *fsmTreePage) maxAvail() uint8        { return p.nodes[0] }

// rebuild is fsm_rebuild_page.
func (p *fsmTreePage) rebuild() {
	for nodeno := fsmNonLeafNodes - 1; nodeno >= 0; nodeno-- {
		l := fsmLeftChild(nodeno)
		var nv uint8
		if l < fsmNodesPerPage {
			nv = p.nodes[l]
		}
		if r := l + 1; r < fsmNodesPerPage {
			nv = max(nv, p.nodes[r])
		}
		p.nodes[nodeno] = nv
	}
}

// truncateAvail is fsm_truncate_avail.
func (p *fsmTreePage) truncateAvail(nslots int) {
	changed := false
	for i := fsmNonLeafNodes + nslots; i < fsmNodesPerPage; i++ {
		if p.nodes[i] != 0 {
			changed = true
		}
		p.nodes[i] = 0
	}
	if changed {
		p.rebuild()
	}
}

// searchAvail is fsm_search_avail: from fp_next_slot, move right (wrapping)
// and climb until a node holds minValue, then descend preferring the left
// child. The hint is left on the found slot, one past it at the bottom level.
func (p *fsmTreePage) searchAvail(minValue uint8, advanceNext bool) int {
	for {
		if p.nodes[0] < minValue {
			return -1
		}
		target := p.nextSlot
		if target < 0 || target >= fsmLeafNodes {
			target = 0
		}
		nodeno := target + fsmNonLeafNodes
		for nodeno > 0 {
			if p.nodes[nodeno] >= minValue {
				break
			}
			nodeno = fsmParentOf(fsmRightNeighbor(nodeno))
		}
		corrupt := false
		for nodeno < fsmNonLeafNodes {
			child := fsmLeftChild(nodeno)
			if child < fsmNodesPerPage && p.nodes[child] >= minValue {
				nodeno = child
				continue
			}
			child++
			if child < fsmNodesPerPage && p.nodes[child] >= minValue {
				nodeno = child
				continue
			}
			corrupt = true
			break
		}
		if corrupt {
			// The parent promised space neither child has: rebuild and
			// restart, as PG does for a torn page.
			p.rebuild()
			continue
		}
		slot := nodeno - fsmNonLeafNodes
		p.nextSlot = slot
		if advanceNext {
			p.nextSlot++
		}
		return slot
	}
}

// setAndSearch is fsm_set_and_search.
func (t *fsmTree) setAndSearch(addr fsmAddr, slot int, newValue, minValue uint8) int {
	p := t.readbuf(addr, true)
	p.setAvail(slot, newValue)
	if minValue != 0 {
		return p.searchAvail(minValue, addr.level == fsmBottomLevel)
	}
	return -1
}

// record is RecordPageWithFreeSpace: the leaf only, upper levels untouched.
func (t *fsmTree) record(heapBlk BlockNumber, spaceAvail int) {
	addr, slot := fsmGetLocation(heapBlk)
	t.setAndSearch(addr, slot, fsmAvailToCat(spaceAvail), 0)
}

// recordAndGet is RecordAndGetPageWithFreeSpace: the old page's own leaf
// first, then the whole tree.
func (t *fsmTree) recordAndGet(oldPage BlockNumber, oldSpaceAvail, spaceNeeded int, relBlocks BlockNumber) (BlockNumber, bool) {
	addr, slot := fsmGetLocation(oldPage)
	searchCat := fsmNeededToCat(spaceNeeded)
	if s := t.setAndSearch(addr, slot, fsmAvailToCat(oldSpaceAvail), searchCat); s != -1 {
		if blk := fsmGetHeapBlk(addr, s); blk < relBlocks {
			return blk, true
		}
	}
	return t.search(searchCat, relBlocks)
}

// get is GetPageWithFreeSpace.
func (t *fsmTree) get(spaceNeeded int, relBlocks BlockNumber) (BlockNumber, bool) {
	return t.search(fsmNeededToCat(spaceNeeded), relBlocks)
}

// search is fsm_search: descend from the root; a stale upper slot that
// promised space its child no longer has is corrected to the child's maximum
// and the search restarts; a slot past the heap's end is zeroed.
func (t *fsmTree) search(minCat uint8, relBlocks BlockNumber) (BlockNumber, bool) {
	restarts := 0
	addr := fsmRootAddress
	for {
		slot := -1
		var maxAvail uint8
		p := t.readbuf(addr, false)
		if p != nil {
			slot = p.searchAvail(minCat, addr.level == fsmBottomLevel)
			if slot == -1 {
				maxAvail = p.maxAvail()
			}
		}
		switch {
		case slot != -1 && addr.level == fsmBottomLevel:
			blk := fsmGetHeapBlk(addr, slot)
			if blk < relBlocks {
				return blk, true
			}
			p.setAvail(slot, 0)
			if restarts++; restarts > fsmTreeMaxRestarts {
				return 0, false
			}
			addr = fsmRootAddress
		case slot != -1:
			addr = fsmGetChild(addr, slot)
		case addr.level == fsmRootLevel:
			return 0, false
		default:
			parent, parentSlot := fsmGetParent(addr)
			t.setAndSearch(parent, parentSlot, maxAvail, 0)
			if restarts++; restarts > fsmTreeMaxRestarts {
				return 0, false
			}
			addr = fsmRootAddress
		}
	}
}

// vacuumRange is FreeSpaceMapVacuumRange: recompute the upper-level slots
// covering heap blocks [start, end) from the pages below them.
func (t *fsmTree) vacuumRange(start, end BlockNumber) {
	if end > start {
		eof := false
		t.vacuumPage(fsmRootAddress, start, end, &eof)
	}
}

// vacuumPage is fsm_vacuum_page.
func (t *fsmTree) vacuumPage(addr fsmAddr, start, end BlockNumber, eof *bool) uint8 {
	p := t.readbuf(addr, false)
	if p == nil {
		*eof = true
		return 0
	}
	*eof = false
	if addr.level > fsmBottomLevel {
		fsmStart, startSlot := fsmGetLocation(start)
		fsmEnd, endSlot := fsmGetLocation(end - 1)
		for fsmStart.level < addr.level {
			fsmStart, startSlot = fsmGetParent(fsmStart)
			fsmEnd, endSlot = fsmGetParent(fsmEnd)
		}
		switch {
		case fsmStart.logpageno == addr.logpageno:
		case fsmStart.logpageno > addr.logpageno:
			startSlot = fsmSlotsPerPage
		default:
			startSlot = 0
		}
		switch {
		case fsmEnd.logpageno == addr.logpageno:
		case fsmEnd.logpageno > addr.logpageno:
			endSlot = fsmSlotsPerPage - 1
		default:
			endSlot = -1
		}
		childEOF := false
		for slot := startSlot; slot <= endSlot; slot++ {
			var childAvail uint8
			if !childEOF {
				childAvail = t.vacuumPage(fsmGetChild(addr, slot), start, end, &childEOF)
			}
			if p.getAvail(slot) != childAvail {
				p.setAvail(slot, childAvail)
			}
		}
	}
	p.nextSlot = 0
	return p.maxAvail()
}

// truncate is FreeSpaceMapPrepareTruncateRel followed by the FSM fork's
// truncation and the FreeSpaceMapVacuumRange(nblocks, InvalidBlockNumber)
// RelationTruncate runs after it.
func (t *fsmTree) truncate(nblocks BlockNumber) {
	addr, slot := fsmGetLocation(nblocks)
	var newN uint32
	if slot > 0 {
		p := t.readbuf(addr, false)
		if p == nil {
			return
		}
		p.truncateAvail(slot)
		newN = fsmLogicalToPhysical(addr) + 1
	} else {
		newN = fsmLogicalToPhysical(addr)
		if t.nblocks <= newN {
			return
		}
	}
	if newN < t.nblocks {
		for a := range t.pages {
			if fsmLogicalToPhysical(a) >= newN {
				delete(t.pages, a)
			}
		}
		t.nblocks = newN
	}
	t.vacuumRange(nblocks, InvalidBlockNumber)
}
