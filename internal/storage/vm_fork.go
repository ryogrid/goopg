package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VM fork page layout (mirrors PG visibilitymap.c).
//
// Each VM page is BlockSize bytes with a standard PageHeaderData. The
// visibility bits live in the page body starting at SizeOfPageHeaderData.
// PG uses 2 bits per heap page (ALL_VISIBLE + ALL_FROZEN); goopg tracks both
// since the vacuum probe-audit (ALL_FROZEN set by VACUUM freeze passes,
// cleared by DML alongside ALL_VISIBLE).
//
// Bits per byte: 4 pages per byte (2 bits each). Within a byte, the
// least-significant bits correspond to the earliest heap pages.

const (
	// VMBitsPerHeapPage is the number of VM bits per heap page.
	VMBitsPerHeapPage = 2
	// VMPagesPerByte is the number of heap pages tracked per byte in the VM.
	VMPagesPerByte = 8 / VMBitsPerHeapPage // 4
	// VMAllVisible is the mask for the ALL_VISIBLE bit (bit 0).
	VMAllVisible = 0x01
	// VMAllFrozen is the mask for the ALL_FROZEN bit (bit 1).
	VMAllFrozen = 0x02

	// vmDataOffset is where visibility data starts within a VM page.
	vmDataOffset = SizeOfPageHeaderData
	// vmDataSize is the number of bytes available for visibility data per page.
	vmDataSize = BlockSize - vmDataOffset
	// vmMaxHeapPagesPerPage is the max number of heap pages one VM page covers.
	vmMaxHeapPagesPerPage = vmDataSize * VMPagesPerByte
)

// buildVMPage builds a single VM page from per-heap-page bit masks
// (VMAllVisible | VMAllFrozen). Returns a BlockSize-byte slice with a valid
// page header.
func buildVMPage(masks []uint8) []byte {
	p := make([]byte, BlockSize)
	_ = InitPage(p)

	for i, mask := range masks {
		if mask == 0 {
			continue
		}
		byteIdx := i / VMPagesPerByte
		bitShift := (i % VMPagesPerByte) * VMBitsPerHeapPage
		if vmDataOffset+byteIdx < BlockSize {
			p[vmDataOffset+byteIdx] |= mask << bitShift
		}
	}

	return p
}

// parseVMPage reads visibility bits from a VM page. Returns per-heap-page
// bit masks (VMAllVisible | VMAllFrozen) indexed by heap page number.
func parseVMPage(p []byte) []uint8 {
	if len(p) != BlockSize {
		return nil
	}
	masks := make([]uint8, vmMaxHeapPagesPerPage)
	for i := 0; i < vmMaxHeapPagesPerPage; i++ {
		byteIdx := i / VMPagesPerByte
		bitShift := (i % VMPagesPerByte) * VMBitsPerHeapPage
		if vmDataOffset+byteIdx < BlockSize {
			b := p[vmDataOffset+byteIdx]
			masks[i] = (b >> bitShift) & (VMAllVisible | VMAllFrozen)
		}
	}
	return masks
}

// WriteVMFork writes a PG-compatible VM fork file for one relation.
// masks is indexed by heap block number holding VMAllVisible/VMAllFrozen
// bits.
//
// The file is written atomically (temp + rename) to path.
func WriteVMFork(path string, masks []uint8) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("vm fork: mkdir: %w", err)
	}

	// Build VM pages.
	numPages := (len(masks) + vmMaxHeapPagesPerPage - 1) / vmMaxHeapPagesPerPage
	if numPages == 0 && len(masks) > 0 {
		numPages = 1
	}

	pages := make([][]byte, 0, numPages)
	for i := 0; i < len(masks); i += vmMaxHeapPagesPerPage {
		end := i + vmMaxHeapPagesPerPage
		if end > len(masks) {
			end = len(masks)
		}
		pages = append(pages, buildVMPage(masks[i:end]))
	}

	if len(pages) == 0 {
		// Write at least one page for an empty VM (PG expects fork to be
		// non-empty if it exists — but we shouldn't write empty files).
		return nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("vm fork: tempfile: %w", err)
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
			return fmt.Errorf("vm fork: write page: %w", err)
		}
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("vm fork: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("vm fork: close: %w", err)
	}
	closed = true
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("vm fork: rename: %w", err)
	}
	return nil
}

// ReadVMFork reads a PG-compatible VM fork file and returns per-block bit
// masks indexed by heap block number. Returns nil if the file does not exist.
func ReadVMFork(path string) ([]uint8, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("vm fork: read %q: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	if len(data)%BlockSize != 0 {
		return nil, fmt.Errorf("vm fork: %q: size %d not a multiple of page size %d", path, len(data), BlockSize)
	}

	numPages := len(data) / BlockSize
	var masks []uint8
	for i := 0; i < numPages; i++ {
		pageOff := i * BlockSize
		page := parseVMPage(data[pageOff : pageOff+BlockSize])
		masks = append(masks, page...)
	}
	return masks, nil
}

// DeleteVMFork removes the _vm fork file for a relation.
func DeleteVMFork(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("vm fork: remove %q: %w", path, err)
	}
	return nil
}

// VMSaveForks writes per-relation VM fork files for all tracked relations
// under dataDir. Also removes stale fork files for relations no longer tracked.
func (v *VisibilityMap) VMSaveForks(dataDir string, prevKeys map[vmKey]bool) error {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()

	for key, pages := range v.pages {
		rfn := RelFileNode{DBOid: key.DBOid, RelOid: key.RelOid, Fork: VisibilityMapFork}
		path := RelForkPath(dataDir, rfn)
		if err := WriteVMFork(path, pages); err != nil {
			return fmt.Errorf("vm: save fork for %d/%d: %w", key.DBOid, key.RelOid, err)
		}
	}

	// Remove stale fork files for relations no longer tracked.
	if prevKeys != nil {
		for key := range prevKeys {
			if _, ok := v.pages[key]; ok {
				continue
			}
			rfn := RelFileNode{DBOid: key.DBOid, RelOid: key.RelOid, Fork: VisibilityMapFork}
			path := RelForkPath(dataDir, rfn)
			if err := DeleteVMFork(path); err != nil {
				return fmt.Errorf("vm: delete stale fork for %d/%d: %w", key.DBOid, key.RelOid, err)
			}
		}
	}

	return nil
}

// VMRelations returns the set of relation keys currently tracked by the VM.
func (v *VisibilityMap) VMRelations() map[vmKey]bool {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	keys := make(map[vmKey]bool, len(v.pages))
	for k := range v.pages {
		keys[k] = true
	}
	return keys
}

// VMLoadForks scans dataDir for _vm fork files and loads them into the VM.
func (v *VisibilityMap) VMLoadForks(dataDir string) error {
	if v == nil {
		return nil
	}

	loaded := make(map[vmKey][]uint8)

	// Helper to scan a directory for _vm files.
	scanDir := func(dir string, dbOid uint32) error {
		files, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), "_vm") {
				continue
			}
			relOidStr := file.Name()[:len(file.Name())-3] // strip "_vm"
			relOid, err := parseUint32(relOidStr)
			if err != nil {
				continue
			}
			path := filepath.Join(dir, file.Name())
			masks, err := ReadVMFork(path)
			if err != nil {
				return fmt.Errorf("vm: load %q: %w", path, err)
			}
			if masks != nil {
				key := vmKey{DBOid: dbOid, RelOid: relOid}
				loaded[key] = masks
			}
		}
		return nil
	}

	// Scan base/ directories.
	baseDir := filepath.Join(dataDir, "base")
	if baseEntries, err := os.ReadDir(baseDir); err == nil {
		for _, entry := range baseEntries {
			if !entry.IsDir() {
				continue
			}
			dbOid, err := parseUint32(entry.Name())
			if err != nil {
				continue
			}
			dbDir := filepath.Join(baseDir, entry.Name())
			if err := scanDir(dbDir, dbOid); err != nil {
				return err
			}
		}
	}

	// Scan global/ for shared relations.
	globalDir := filepath.Join(dataDir, "global")
	if err := scanDir(globalDir, 0); err != nil {
		return err
	}

	v.mu.Lock()
	v.pages = loaded
	v.mu.Unlock()
	return nil
}
