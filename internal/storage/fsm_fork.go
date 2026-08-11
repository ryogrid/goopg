package storage

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FSM fork page layout (mirrors PG fsm_internals.h / fsmpage.c).
//
// Each FSM page is BlockSize bytes with a standard PageHeaderData followed by
// FSMPageData (fp_next_slot + fp_nodes binary tree).
//
//	const (
//		fsmPageHeaderSize  = SizeOfPageHeaderData // 24
//		fsmNextSlotSize    = 4                    // int32
//		fsmNodesPerPage    = BlockSize - fsmPageHeaderSize - fsmNextSlotSize
//		fsmNonLeafNodes    = BlockSize/2 - 1
//		fsmLeafNodes       = fsmNodesPerPage - fsmNonLeafNodes
//		fsmSlotsPerPage    = fsmLeafNodes
//	)
//
// The fp_nodes array is a binary tree stored in heap order:
//   - Internal nodes at indices 0..fsmNonLeafNodes-1 (each = max of children)
//   - Leaf nodes at indices fsmNonLeafNodes..fsmNodesPerPage-1 (category per slot)
//   - left_child(n) = 2*n+1, right_child(n) = 2*n+2
//
// Multi-level structure:
//   - Level 0: leaf pages, each covering up to fsmSlotsPerPage heap pages
//   - Level 1+: each page summarizes fsmSlotsPerPage pages from the level below
//   - Physical order: level 0 first, then level 1, ..., root last

const (
	// FSM_CATEGORIES is the number of free-space categories.
	FSM_CATEGORIES = 256
	// FSM_CAT_STEP is the byte range each category covers.
	FSM_CAT_STEP = BlockSize / FSM_CATEGORIES // 32 for 8k pages

	fsmPageHeaderSize = SizeOfPageHeaderData // 24
	fsmNextSlotSize   = 4                    // int32 fp_next_slot
	fsmNodesPerPage   = BlockSize - fsmPageHeaderSize - fsmNextSlotSize
	fsmNonLeafNodes   = BlockSize/2 - 1
	fsmLeafNodes      = fsmNodesPerPage - fsmNonLeafNodes
	fsmSlotsPerPage   = fsmLeafNodes

	// MaxFSMRequestSize is the maximum heap tuple size — the largest free-space
	// request the FSM can satisfy. Category 255 represents >= this value.
	// PG: MaxHeapTupleSize = BLCKSZ - SizeOfPageHeaderData - sizeof(ItemIdData).
	MaxFSMRequestSize = BlockSize - SizeOfPageHeaderData - 4 // 8164 for 8k pages
)

// fsmSpaceAvailToCat converts available free bytes to an FSM category (0–255).
// Mirrors PG's fsm_space_avail_to_cat.
func fsmSpaceAvailToCat(avail uint16) uint8 {
	if avail >= MaxFSMRequestSize {
		return 255
	}
	cat := avail / FSM_CAT_STEP
	if cat > 254 {
		cat = 254
	}
	return uint8(cat)
}

// fsmSpaceCatToAvail returns the lower bound of free space for a category.
// Mirrors PG's fsm_space_cat_to_avail.
func fsmSpaceCatToAvail(cat uint8) uint16 {
	if cat == 255 {
		return MaxFSMRequestSize
	}
	return uint16(cat) * FSM_CAT_STEP
}

// buildFSMPage builds a single FSM page from leaf categories. Returns a
// BlockSize-byte slice with a valid page header and FSM tree. numSlots
// specifies how many leaf slots to populate (≤ fsmSlotsPerPage); remaining
// slots stay zero.
func buildFSMPage(categories []uint8, numSlots int) []byte {
	p := make([]byte, BlockSize)

	// Init page header (zeros page, sets lower/upper/special/pagesize).
	_ = InitPage(p) // error only when len(p) != BlockSize — we just allocated it

	// fp_next_slot at offset SizeOfPageHeaderData (24), 4 bytes, LE.
	fpNextSlot := uint32(0) // round-robin starts at 0
	binary.LittleEndian.PutUint32(p[fsmPageHeaderSize:fsmPageHeaderSize+fsmNextSlotSize], fpNextSlot)

	// fp_nodes starts at offset 28.
	nodesStart := fsmPageHeaderSize + fsmNextSlotSize

	// Write leaf values into the leaf portion of the nodes array.
	leafStart := fsmNonLeafNodes
	for i := 0; i < numSlots && i < fsmSlotsPerPage; i++ {
		p[nodesStart+leafStart+i] = categories[i]
	}

	// Compute internal nodes bottom-up: max of children.
	// Some internal-node children fall beyond the end of fp_nodes
	// (the tree is larger than the data it holds); treat those as 0.
	for i := fsmNonLeafNodes - 1; i >= 0; i-- {
		leftIdx := 2*i + 1
		rightIdx := 2*i + 2
		var left, right uint8
		if leftIdx < fsmNodesPerPage {
			left = p[nodesStart+leftIdx]
		}
		if rightIdx < fsmNodesPerPage {
			right = p[nodesStart+rightIdx]
		}
		if left > right {
			p[nodesStart+i] = left
		} else {
			p[nodesStart+i] = right
		}
	}

	return p
}

// parseFSMPage reads leaf categories from an FSM page. Returns the slice of
// leaf-node category values (up to fsmSlotsPerPage entries). Also returns the
// maximum category stored anywhere in the page (internal or leaf nodes).
func parseFSMPage(p []byte) (cats []uint8, maxCat uint8) {
	if len(p) != BlockSize {
		return nil, 0
	}
	nodesStart := fsmPageHeaderSize + fsmNextSlotSize
	leafStart := fsmNonLeafNodes
	cats = make([]uint8, fsmSlotsPerPage)
	maxCat = uint8(0)
	for i := 0; i < fsmSlotsPerPage; i++ {
		c := p[nodesStart+leafStart+i]
		cats[i] = c
		if c > maxCat {
			maxCat = c
		}
	}
	// Also check internal nodes for max (belt-and-suspenders).
	for i := 0; i < fsmNonLeafNodes; i++ {
		if c := p[nodesStart+i]; c > maxCat {
			maxCat = c
		}
	}
	return cats, maxCat
}

// RelForkPath returns the absolute filesystem path for a relation fork file.
// Mirrors Manager.relPath but is a free function usable without a Manager
// instance (e.g. during checkpoint save).
func RelForkPath(dataDir string, rfn RelFileNode) string {
	base := filepath.Join(dataDir, relDir(rfn))
	switch rfn.Fork {
	case MainFork:
		return filepath.Join(base, fmt.Sprint(rfn.RelOid))
	case FSMFork:
		return filepath.Join(base, fmt.Sprintf("%d_fsm", rfn.RelOid))
	case VisibilityMapFork:
		return filepath.Join(base, fmt.Sprintf("%d_vm", rfn.RelOid))
	case InitFork:
		return filepath.Join(base, fmt.Sprintf("%d_init", rfn.RelOid))
	}
	return filepath.Join(base, fmt.Sprintf("%d_fork%d", rfn.RelOid, rfn.Fork))
}

// WriteFSMFork writes a PG-compatible FSM fork file for one relation.
// freeSpace is a slice indexed by heap block number; each entry is the
// approximate free bytes available on that page.
//
// The file is written atomically (temp + rename) to path.
func WriteFSMFork(path string, freeSpace []uint16) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("fsm fork: mkdir: %w", err)
	}

	// Convert free bytes to categories.
	cats := make([]uint8, len(freeSpace))
	for i, free := range freeSpace {
		cats[i] = fsmSpaceAvailToCat(free)
	}

	// Build multi-level FSM tree.
	pages := buildFSMTree(cats)

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fsm fork: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if _, err := os.Stat(tmpName); err == nil {
			_ = os.Remove(tmpName)
		}
	}()

	for _, pg := range pages {
		if _, err := tmp.Write(pg); err != nil {
			return fmt.Errorf("fsm fork: write page: %w", err)
		}
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("fsm fork: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fsm fork: close: %w", err)
	}
	closed = true
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("fsm fork: rename: %w", err)
	}
	return nil
}

// buildFSMTree builds the complete multi-level FSM page tree from leaf categories.
// Returns pages in physical order: level 0 pages first, then level 1, ..., root last.
func buildFSMTree(cats []uint8) [][]byte {
	if len(cats) == 0 {
		return nil
	}

	// Build level 0: leaf pages.
	type levelInfo struct {
		pages   [][]byte
		maxCats []uint8 // max category for each page
	}
	var levels []levelInfo

	// Level 0: from actual heap page categories.
	l0 := levelInfo{}
	for i := 0; i < len(cats); i += fsmSlotsPerPage {
		end := i + fsmSlotsPerPage
		if end > len(cats) {
			end = len(cats)
		}
		pg := buildFSMPage(cats[i:end], end-i)
		l0.pages = append(l0.pages, pg)
		_, maxCat := parseFSMPage(pg)
		l0.maxCats = append(l0.maxCats, maxCat)
	}
	levels = append(levels, l0)

	// Build higher levels until we reach a single root page.
	for len(levels[len(levels)-1].pages) > 1 {
		prev := levels[len(levels)-1]
		cur := levelInfo{}
		for i := 0; i < len(prev.maxCats); i += fsmSlotsPerPage {
			end := i + fsmSlotsPerPage
			if end > len(prev.maxCats) {
				end = len(prev.maxCats)
			}
			pg := buildFSMPage(prev.maxCats[i:end], end-i)
			cur.pages = append(cur.pages, pg)
			_, maxCat := parseFSMPage(pg)
			cur.maxCats = append(cur.maxCats, maxCat)
		}
		levels = append(levels, cur)
	}

	// Flatten: level 0 first, then level 1, ..., root last.
	var all [][]byte
	for _, l := range levels {
		all = append(all, l.pages...)
	}
	return all
}

// ReadFSMFork reads a PG-compatible FSM fork file and returns the free-space
// estimates indexed by heap block number. Returns nil if the file does not
// exist (fresh cluster / no FSM data yet).
func ReadFSMFork(path string) ([]uint16, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fsm fork: read %q: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	if len(data)%BlockSize != 0 {
		return nil, fmt.Errorf("fsm fork: %q: size %d not a multiple of page size %d", path, len(data), BlockSize)
	}

	numPages := len(data) / BlockSize
	if numPages == 0 {
		return nil, nil
	}

	// Determine the tree structure. The first page is a level-0 leaf page.
	// We need to figure out how many level-0 pages there are.
	// Walk from root backwards: the last page is the root, preceding pages
	// are at decreasing levels.

	// Strategy: reconstruct the level structure.
	// Level 0 pages have fsmSlotsPerPage leaf slots each.
	// If we have N pages total, and the root is at the end, we can work
	// backwards: root = 1 page. Its parent level has fsmSlotsPerPage times
	// as many pages as the root has... but that's not right either.

	// Simpler approach: we know the root is the last page. We can reconstruct
	// from the root downward.

	// Find the level structure by working backwards from the root.
	type levelPages struct {
		count  int
		offset int // byte offset of first page at this level
	}
	var levels []levelPages

	// Root is the last page.
	remainingPages := numPages - 1 // pages before root
	levelCount := 1               // root = 1 page
	rootOffset := (numPages - 1) * BlockSize
	levels = append(levels, levelPages{count: levelCount, offset: rootOffset})

	// Work backwards: each level L has enough pages to cover level L+1's pages.
	for remainingPages > 0 {
		// Level l needs ceil(levelCount / fsmSlotsPerPage) pages.
		need := (levelCount + fsmSlotsPerPage - 1) / fsmSlotsPerPage
		if need > remainingPages {
			// Shouldn't happen with well-formed files.
			need = remainingPages
		}
		remainingPages -= need
		offset := remainingPages * BlockSize
		levels = append(levels, levelPages{count: need, offset: offset})
		levelCount = need
	}

	// levels[0] is root, levels[1] is root's parent, ..., levels[len-1] is leaf.
	// The last level is level 0 (leaf).
	leafLevel := levels[len(levels)-1]
	totalSlots := leafLevel.count * fsmSlotsPerPage
	freeSpace := make([]uint16, 0, totalSlots)

	// Read all leaf pages.
	for i := 0; i < leafLevel.count; i++ {
		pageOff := leafLevel.offset + i*BlockSize
		cats, _ := parseFSMPage(data[pageOff : pageOff+BlockSize])
		for _, c := range cats {
			freeSpace = append(freeSpace, fsmSpaceCatToAvail(c))
		}
	}

	return freeSpace, nil
}

// DeleteFSMFork removes the _fsm fork file for a relation.
func DeleteFSMFork(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fsm fork: remove %q: %w", path, err)
	}
	return nil
}

// FSMSaveForks writes per-relation FSM fork files for all tracked relations
// under dataDir. Also removes stale fork files for relations no longer tracked.
func (f *FSM) FSMSaveForks(dataDir string, prevKeys map[fsmKey]bool) error {
	if f == nil {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()

	for key, pages := range f.pages {
		rfn := RelFileNode{DBOid: key.DBOid, RelOid: key.RelOid, Fork: FSMFork}
		path := RelForkPath(dataDir, rfn)
		if err := WriteFSMFork(path, pages); err != nil {
			return fmt.Errorf("fsm: save fork for %d/%d: %w", key.DBOid, key.RelOid, err)
		}
	}

	// Remove stale fork files for relations no longer tracked.
	if prevKeys != nil {
		for key := range prevKeys {
			if _, ok := f.pages[key]; ok {
				continue // still tracked
			}
			rfn := RelFileNode{DBOid: key.DBOid, RelOid: key.RelOid, Fork: FSMFork}
			path := RelForkPath(dataDir, rfn)
			if err := DeleteFSMFork(path); err != nil {
				return fmt.Errorf("fsm: delete stale fork for %d/%d: %w", key.DBOid, key.RelOid, err)
			}
		}
	}

	return nil
}

// FSMRelations returns the set of relation keys currently tracked by the FSM.
func (f *FSM) FSMRelations() map[fsmKey]bool {
	if f == nil {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	keys := make(map[fsmKey]bool, len(f.pages))
	for k := range f.pages {
		keys[k] = true
	}
	return keys
}

// FSMLoadForks scans dataDir for _fsm fork files and loads them into the FSM.
func (f *FSM) FSMLoadForks(dataDir string) error {
	if f == nil {
		return nil
	}

	loaded := make(map[fsmKey][]uint16)

	// Scan base/ directories for _fsm files.
	baseDir := filepath.Join(dataDir, "base")
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // fresh cluster
		}
		return fmt.Errorf("fsm: read base dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dbOid, err := parseUint32(entry.Name())
		if err != nil {
			continue
		}

		dbDir := filepath.Join(baseDir, entry.Name())
		files, err := os.ReadDir(dbDir)
		if err != nil {
			continue
		}

		for _, file := range files {
			if !strings.HasSuffix(file.Name(), "_fsm") {
				continue
			}
			relOidStr := file.Name()[:len(file.Name())-4] // strip "_fsm"
			relOid, err := parseUint32(relOidStr)
			if err != nil {
				continue
			}

			path := filepath.Join(dbDir, file.Name())
			freeSpace, err := ReadFSMFork(path)
			if err != nil {
				return fmt.Errorf("fsm: load %q: %w", path, err)
			}
			if freeSpace != nil {
				key := fsmKey{DBOid: dbOid, RelOid: relOid}
				loaded[key] = freeSpace
			}
		}
	}

	// Also scan global/ for shared relations.
	globalDir := filepath.Join(dataDir, "global")
	if globalFiles, err := os.ReadDir(globalDir); err == nil {
		for _, file := range globalFiles {
			if !strings.HasSuffix(file.Name(), "_fsm") {
				continue
			}
			relOidStr := file.Name()[:len(file.Name())-4]
			relOid, err := parseUint32(relOidStr)
			if err != nil {
				continue
			}
			path := filepath.Join(globalDir, file.Name())
			freeSpace, err := ReadFSMFork(path)
			if err != nil {
				return fmt.Errorf("fsm: load %q: %w", path, err)
			}
			if freeSpace != nil {
				key := fsmKey{DBOid: 0, RelOid: relOid}
				loaded[key] = freeSpace
			}
		}
	}

	f.mu.Lock()
	f.pages = loaded
	f.mu.Unlock()
	return nil
}

// parseUint32 parses a non-negative uint32 from a decimal string.
func parseUint32(s string) (uint32, error) {
	var v uint32
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("not a uint32: %q", s)
		}
		v = v*10 + uint32(ch-'0')
	}
	return v, nil
}
