package storage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// fsmKey identifies a heap relation (without fork) for FSM lookups.
type fsmKey struct{ DBOid, RelOid uint32 }

func fsmKeyFor(rel RelFileNode) fsmKey { return fsmKey{rel.DBOid, rel.RelOid} }

// FSM is the in-memory free-space map for heap relations (M0046-0003).
//
// For each heap page it records an approximate count of free bytes,
// allowing writeHeapRow to find an existing page with enough room before
// extending the relation. Entries are populated by VACUUM after reclaiming
// dead tuples, and updated (decremented) after each successful tuple insert.
//
// Thread-safe: all methods serialise through a RWMutex.
type FSM struct {
	mu    sync.RWMutex
	pages map[fsmKey][]uint16 // indexed by BlockNumber
}

// NewFSM allocates an empty FSM.
func NewFSM() *FSM { return &FSM{pages: make(map[fsmKey][]uint16)} }

// GetPageWithFreeSpace returns the block number of a heap page with at
// least minFreeBytes available, and true. Returns (0, false) when no
// such page is registered in the FSM (e.g. VACUUM has not yet run).
//
// The returned block may be stale (another writer could have consumed
// the free space since the FSM entry was recorded). Callers must handle
// a failed insert gracefully (invalidate the FSM entry and retry).
func (f *FSM) GetPageWithFreeSpace(rel RelFileNode, minFreeBytes uint16) (BlockNumber, bool) {
	if f == nil || minFreeBytes == 0 {
		return 0, false
	}
	key := fsmKeyFor(rel)
	f.mu.RLock()
	defer f.mu.RUnlock()
	for blk, free := range f.pages[key] {
		if free >= minFreeBytes {
			return BlockNumber(blk), true
		}
	}
	return 0, false
}


// GetCandidates returns up to n block numbers whose registered free-space
// estimate is ≥ minFreeBytes, ordered by free-space descending (most-free
// first). Used by the insert path to choose among multiple candidate
// pages — see docs/design/perf-optimize/07-wal-fsm-insert.md §3 and
// docs/design/0107-0007b-fsm-get-candidates.md. (M0107-0007 slice C.)
//
// Like GetPageWithFreeSpace, the returned blocks may be stale (another
// writer could have consumed the free space since the FSM entry was
// recorded). Callers must handle a failed insert gracefully.
//
// Returns nil when n <= 0, minFreeBytes == 0, no page qualifies, or
// f is nil. Among ties (equal free-space estimates), the lowest block
// number is returned first — deterministic for reproducible plans.
func (f *FSM) GetCandidates(rel RelFileNode, minFreeBytes uint16, n int) []BlockNumber {
	if f == nil || n <= 0 || minFreeBytes == 0 {
		return nil
	}
	key := fsmKeyFor(rel)
	f.mu.RLock()
	defer f.mu.RUnlock()
	pages := f.pages[key]
	if len(pages) == 0 {
		return nil
	}
	type entry struct {
		free uint16
		blk  BlockNumber
	}
	// Insertion sort over a small fixed-size buffer (n is typically 4).
	// Each scan slot: (free uint16, blk BlockNumber). When a new page
	// beats the smallest kept candidate, slide it in and drop the tail.
	kept := make([]entry, 0, n)
	for blk, free := range pages {
		if free < minFreeBytes {
			continue
		}
		e := entry{free: free, blk: BlockNumber(blk)}
		if len(kept) < n {
			kept = append(kept, e)
			for i := len(kept) - 1; i > 0 && kept[i].free > kept[i-1].free; i-- {
				kept[i], kept[i-1] = kept[i-1], kept[i]
			}
			continue
		}
		if e.free <= kept[n-1].free {
			continue
		}
		kept[n-1] = e
		for i := n - 1; i > 0 && kept[i].free > kept[i-1].free; i-- {
			kept[i], kept[i-1] = kept[i-1], kept[i]
		}
	}
	if len(kept) == 0 {
		return nil
	}
	out := make([]BlockNumber, len(kept))
	for i, e := range kept {
		out[i] = e.blk
	}
	return out
}

// RecordFreeSpace stores the approximate free bytes for one heap page.
// A value of 0 marks the page as full; subsequent GetPageWithFreeSpace
// calls won't return it until a positive value is recorded again.
func (f *FSM) RecordFreeSpace(rel RelFileNode, blk BlockNumber, freeBytes uint16) {
	if f == nil {
		return
	}
	key := fsmKeyFor(rel)
	f.mu.Lock()
	defer f.mu.Unlock()
	pages := f.pages[key]
	for int(blk) >= len(pages) {
		pages = append(pages, 0)
	}
	pages[blk] = freeBytes
	f.pages[key] = pages
}

// RecordFreeSpaceForPage reads the page header's FreeSpace() and records
// it in the FSM for rel/blk. Convenience wrapper for VACUUM and insert
// paths that already have the page pinned.
func (f *FSM) RecordFreeSpaceForPage(rel RelFileNode, blk BlockNumber, p Page) {
	if f == nil {
		return
	}
	free := MustHeader(p).FreeSpace()
	if free < 0 {
		free = 0
	}
	f.RecordFreeSpace(rel, blk, uint16(free))
}

// DropRelation removes all FSM entries for rel. Called on DROP TABLE /
// TRUNCATE to prevent stale entries from directing inserts to non-existent
// pages.
func (f *FSM) DropRelation(rel RelFileNode) {
	if f == nil {
		return
	}
	key := fsmKeyFor(rel)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pages, key)
}

// FSMStatePath returns the canonical on-disk location for FSM
// state under a goopg data directory. Mirrors `VMStatePath`'s
// shape — one global blob rather than per-relation fork files.
// (M0080-0004.)
func FSMStatePath(dataDir string) string {
	return filepath.Join(dataDir, "global", "pg_fsm_state.bin")
}

const (
	fsmFileMagic   uint32 = 0x66534D31 // "fSM1" — FSM-State v1
	fsmFileVersion uint32 = 1
)

// Save writes the FSM's in-memory state to `path` atomically
// (temp file + rename). Called from the graceful-shutdown
// hook so free-space estimates survive a clean restart and
// the first INSERT after restart doesn't always extend the
// relation. Format:
//
//	magic(4) | version(4) | numRels(4) |
//	[ DBOid(4) | RelOid(4) | nBlocks(4) | freeBytes[nBlocks](2 each) ]*
//
// (M0080-0004.)
func (f *FSM) Save(path string) error {
	if f == nil {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("fsm: mkdir for save: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fsm: tempfile: %w", err)
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
	hdr := make([]byte, 12)
	binary.LittleEndian.PutUint32(hdr[0:4], fsmFileMagic)
	binary.LittleEndian.PutUint32(hdr[4:8], fsmFileVersion)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(f.pages)))
	if _, err := tmp.Write(hdr); err != nil {
		return fmt.Errorf("fsm: write header: %w", err)
	}
	keys := make([]fsmKey, 0, len(f.pages))
	for k := range f.pages {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].DBOid != keys[j].DBOid {
			return keys[i].DBOid < keys[j].DBOid
		}
		return keys[i].RelOid < keys[j].RelOid
	})
	relHdr := make([]byte, 12)
	for _, k := range keys {
		blocks := f.pages[k]
		binary.LittleEndian.PutUint32(relHdr[0:4], k.DBOid)
		binary.LittleEndian.PutUint32(relHdr[4:8], k.RelOid)
		binary.LittleEndian.PutUint32(relHdr[8:12], uint32(len(blocks)))
		if _, err := tmp.Write(relHdr); err != nil {
			return fmt.Errorf("fsm: write rel header: %w", err)
		}
		body := make([]byte, 2*len(blocks))
		for i, free := range blocks {
			binary.LittleEndian.PutUint16(body[2*i:2*i+2], free)
		}
		if _, err := tmp.Write(body); err != nil {
			return fmt.Errorf("fsm: write rel body: %w", err)
		}
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("fsm: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fsm: close: %w", err)
	}
	closed = true
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("fsm: rename: %w", err)
	}
	return nil
}

// Load reads the FSM state from `path` and replaces the in-memory
// map. A missing file is treated as a fresh-from-init / pre-FSM-
// persistence cluster (no error). Format mismatch returns an
// error so the operator sees a hard startup failure rather
// than running with stale free-space estimates. (M0080-0004.)
func (f *FSM) Load(path string) error {
	if f == nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("fsm: read %q: %w", path, err)
	}
	if len(data) < 12 {
		return io.ErrUnexpectedEOF
	}
	if magic := binary.LittleEndian.Uint32(data[0:4]); magic != fsmFileMagic {
		return fmt.Errorf("fsm: %q: bad magic %#x (want %#x)", path, magic, fsmFileMagic)
	}
	if ver := binary.LittleEndian.Uint32(data[4:8]); ver != fsmFileVersion {
		return fmt.Errorf("fsm: %q: unsupported version %d (want %d)", path, ver, fsmFileVersion)
	}
	numRels := int(binary.LittleEndian.Uint32(data[8:12]))
	off := 12
	loaded := make(map[fsmKey][]uint16, numRels)
	for i := 0; i < numRels; i++ {
		if len(data) < off+12 {
			return fmt.Errorf("fsm: %q: truncated at rel %d header", path, i)
		}
		dbOid := binary.LittleEndian.Uint32(data[off : off+4])
		relOid := binary.LittleEndian.Uint32(data[off+4 : off+8])
		nBlocks := int(binary.LittleEndian.Uint32(data[off+8 : off+12]))
		off += 12
		if len(data) < off+2*nBlocks {
			return fmt.Errorf("fsm: %q: truncated at rel %d body (need %d bytes)", path, i, 2*nBlocks)
		}
		blocks := make([]uint16, nBlocks)
		for j := 0; j < nBlocks; j++ {
			blocks[j] = binary.LittleEndian.Uint16(data[off+2*j : off+2*j+2])
		}
		off += 2 * nBlocks
		loaded[fsmKey{DBOid: dbOid, RelOid: relOid}] = blocks
	}
	f.mu.Lock()
	f.pages = loaded
	f.mu.Unlock()
	return nil
}
