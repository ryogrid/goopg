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

// vmKey identifies a heap relation (without fork) for Visibility Map lookups.
type vmKey struct{ DBOid, RelOid uint32 }

func vmKeyFor(rel RelFileNode) vmKey { return vmKey{rel.DBOid, rel.RelOid} }

// VisibilityMap is the in-memory visibility map for heap relations (M0046-0004).
//
// Each heap page has an ALL_VISIBLE bit. When set, every tuple on the page is
// known to be visible to ALL active and future snapshots, so an index scan can
// return data directly from the index key without fetching the heap page.
//
// VACUUM sets ALL_VISIBLE after verifying that all remaining tuples on a page
// have committed xmin < OldestXmin and no xmax. Any insert, delete, or update
// on a page clears its bit.
//
// Thread-safe. All methods are nil-safe.
type VisibilityMap struct {
	mu    sync.RWMutex
	pages map[vmKey][]bool // indexed by BlockNumber; true = ALL_VISIBLE
}

// NewVisibilityMap allocates an empty VisibilityMap.
func NewVisibilityMap() *VisibilityMap {
	return &VisibilityMap{pages: make(map[vmKey][]bool)}
}

// AllVisible reports whether all tuples on rel/blk are visible to every
// snapshot. Returns false for any nil receiver or unknown block.
func (v *VisibilityMap) AllVisible(rel RelFileNode, blk BlockNumber) bool {
	if v == nil {
		return false
	}
	key := vmKeyFor(rel)
	v.mu.RLock()
	defer v.mu.RUnlock()
	blocks := v.pages[key]
	if int(blk) >= len(blocks) {
		return false
	}
	return blocks[blk]
}

// SetAllVisible marks rel/blk as ALL_VISIBLE.
func (v *VisibilityMap) SetAllVisible(rel RelFileNode, blk BlockNumber) {
	if v == nil {
		return
	}
	key := vmKeyFor(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	blocks := v.pages[key]
	for int(blk) >= len(blocks) {
		blocks = append(blocks, false)
	}
	blocks[blk] = true
	v.pages[key] = blocks
}

// ClearBlock clears the ALL_VISIBLE bit for rel/blk. Called when any tuple
// on the page is inserted, deleted, or updated.
func (v *VisibilityMap) ClearBlock(rel RelFileNode, blk BlockNumber) {
	if v == nil {
		return
	}
	key := vmKeyFor(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	blocks := v.pages[key]
	if int(blk) < len(blocks) {
		blocks[blk] = false
	}
}

// DropRelation removes all VM entries for rel. Called on DROP TABLE / TRUNCATE
// to prevent stale visibility bits from being returned for future relations
// with the same OID.
func (v *VisibilityMap) DropRelation(rel RelFileNode) {
	if v == nil {
		return
	}
	key := vmKeyFor(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.pages, key)
}

// VMStatePath returns the canonical on-disk location for VM
// state under a goopg data directory. Mirrors PostgreSQL's
// pg_visibility fork-file pattern but uses a single global
// blob for simplicity (one file rather than per-relation
// fork files). (M0080-0003.)
func VMStatePath(dataDir string) string {
	return filepath.Join(dataDir, "global", "pg_vm_state.bin")
}

const (
	vmFileMagic   uint32 = 0x764D5331 // "vMS1" — VM-State v1
	vmFileVersion uint32 = 1
)

// Save writes the VM's in-memory state to `path` atomically
// (temp file + rename). Called from the graceful-shutdown
// hook in cmd/goopg so VM bits survive a clean restart.
// Format:
//
//	magic(4) | version(4) | numRels(4) |
//	[ DBOid(4) | RelOid(4) | nBlocks(4) | bits[nBlocks bytes (1 byte per block, 0/1)] ]*
//
// (M0080-0003.)
func (v *VisibilityMap) Save(path string) error {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("vm: mkdir for save: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("vm: tempfile: %w", err)
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
	binary.LittleEndian.PutUint32(hdr[0:4], vmFileMagic)
	binary.LittleEndian.PutUint32(hdr[4:8], vmFileVersion)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(v.pages)))
	if _, err := tmp.Write(hdr); err != nil {
		return fmt.Errorf("vm: write header: %w", err)
	}
	// Iterate in deterministic order so two saves with identical
	// state produce byte-identical files.
	keys := make([]vmKey, 0, len(v.pages))
	for k := range v.pages {
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
		blocks := v.pages[k]
		binary.LittleEndian.PutUint32(relHdr[0:4], k.DBOid)
		binary.LittleEndian.PutUint32(relHdr[4:8], k.RelOid)
		binary.LittleEndian.PutUint32(relHdr[8:12], uint32(len(blocks)))
		if _, err := tmp.Write(relHdr); err != nil {
			return fmt.Errorf("vm: write rel header: %w", err)
		}
		body := make([]byte, len(blocks))
		for i, b := range blocks {
			if b {
				body[i] = 1
			}
		}
		if _, err := tmp.Write(body); err != nil {
			return fmt.Errorf("vm: write rel body: %w", err)
		}
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("vm: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("vm: close: %w", err)
	}
	closed = true
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("vm: rename: %w", err)
	}
	return nil
}

// Load reads the VM state from `path` and replaces the in-memory
// map. A missing file is treated as a fresh-from-init / pre-VM-
// persistence cluster (no error). Format mismatch returns an
// error so the operator sees a hard startup failure rather
// than running with stale optimisation metadata. (M0080-0003.)
func (v *VisibilityMap) Load(path string) error {
	if v == nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("vm: read %q: %w", path, err)
	}
	if len(data) < 12 {
		return io.ErrUnexpectedEOF
	}
	if magic := binary.LittleEndian.Uint32(data[0:4]); magic != vmFileMagic {
		return fmt.Errorf("vm: %q: bad magic %#x (want %#x)", path, magic, vmFileMagic)
	}
	if ver := binary.LittleEndian.Uint32(data[4:8]); ver != vmFileVersion {
		return fmt.Errorf("vm: %q: unsupported version %d (want %d)", path, ver, vmFileVersion)
	}
	numRels := int(binary.LittleEndian.Uint32(data[8:12]))
	off := 12
	loaded := make(map[vmKey][]bool, numRels)
	for i := 0; i < numRels; i++ {
		if len(data) < off+12 {
			return fmt.Errorf("vm: %q: truncated at rel %d header", path, i)
		}
		dbOid := binary.LittleEndian.Uint32(data[off : off+4])
		relOid := binary.LittleEndian.Uint32(data[off+4 : off+8])
		nBlocks := int(binary.LittleEndian.Uint32(data[off+8 : off+12]))
		off += 12
		if len(data) < off+nBlocks {
			return fmt.Errorf("vm: %q: truncated at rel %d body (need %d bytes)", path, i, nBlocks)
		}
		blocks := make([]bool, nBlocks)
		for j := 0; j < nBlocks; j++ {
			blocks[j] = data[off+j] != 0
		}
		off += nBlocks
		loaded[vmKey{DBOid: dbOid, RelOid: relOid}] = blocks
	}
	v.mu.Lock()
	v.pages = loaded
	v.mu.Unlock()
	return nil
}

// PageAllVisible checks whether every LP_NORMAL tuple on the page p has a
// committed xmin before horizon and no valid xmax (i.e. not deleted). Used by
// VACUUM to decide whether to set the ALL_VISIBLE bit after a page prune.
//
// Returns false on any parse error to keep the caller's logic conservative.
func PageAllVisible(p Page, horizon TransactionID) bool {
	if horizon == InvalidTransactionID {
		return false
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return false
	}
	for idx := 0; idx < count; idx++ {
		item, err := readItemID(p, idx)
		if err != nil {
			return false
		}
		if item.Flags != ItemIDNormal {
			continue // unused / redirect — skip
		}
		off := int(item.Offset)
		ln := int(item.Length)
		if off < 0 || ln < 0 || off+ln > len(p) {
			return false
		}
		t, err := ParseHeapTuple(p[off : off+ln])
		if err != nil {
			return false
		}
		// Tuple must have a committed xmin before the global horizon.
		if t.Header.Xmin == InvalidTransactionID || t.Header.Xmin >= horizon {
			return false
		}
		// Tuple must not be deleted. An updater-bearing multixact xmax
		// (IS_MULTI && !LOCK_ONLY) lands here as "deleted" and correctly fails
		// all-visible — the conservative direction needs no multixact resolution
		// (resolving could only ever mark MORE pages all-visible, and an
		// all-locker multi already carries LOCK_ONLY, so it is handled below).
		if t.Header.Xmax != InvalidTransactionID && !IsHeapTupleLockOnly(t.Header.Infomask) {
			return false
		}
	}
	return true
}
