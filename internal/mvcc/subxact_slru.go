package mvcc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// pg_subtrans SLRU layout constants. Unlike CLOG (2 bits per XID), pg_subtrans
// stores a full 4-byte parent TransactionId per XID, so the packing is far
// looser: 8192/4 = 2048 XIDs per page, 32 pages per segment file (named %04X of
// segno). See postgres/src/backend/access/transam/subtrans.c:
//
//	#define SUBTRANS_XACTS_PER_PAGE (BLCKSZ / sizeof(TransactionId))  // = 2048
//	SLRU_PAGES_PER_SEGMENT = 32 (postgres/src/include/access/slru.h)
//
// and the page/segment math in SubTransSetParent / SubTransGetParent.
const (
	subtransBytesPerXact    = 4                                              // sizeof(TransactionId)
	subtransXactsPerPage    = storage.BlockSize / subtransBytesPerXact       // 2048 with BLCKSZ=8192
	subtransPagesPerSegment = 32                                             // SLRU_PAGES_PER_SEGMENT
	subtransXactsPerSegment = subtransXactsPerPage * subtransPagesPerSegment // 65536
)

// SubtransSLRU is a PG-byte-compatible mirror of pg_subtrans: a flat SLRU that
// maps every subtransaction XID to its immediate parent TransactionId, stored
// as a little-endian uint32 at the XID's 4-byte slot. Segment files are named
// like CLOG segments (%04X of segno) and live under dir.
//
// This implements gap G5 (persistent pg_subtrans): the parent links survive a
// restart, mirroring upstream subtrans.c. Only parent links are stored here —
// abort status stays in CLOG / in-memory (PG's pg_subtrans likewise only holds
// parent TransactionIds, never abort flags).
//
// Thread-safe: a single mutex guards the on-disk writes, consistent with the
// locking discipline in clog.go.
type SubtransSLRU struct {
	mu  sync.Mutex
	dir string
}

// OpenSubtransSLRU opens (or creates) the pg_subtrans SLRU mirror under dir.
// The directory is created if missing. Segment files are created lazily on the
// first SetParent call, mirroring clog.go's lazy segment creation.
func OpenSubtransSLRU(dir string) (*SubtransSLRU, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("subtrans slru: mkdir %q: %w", dir, err)
	}
	return &SubtransSLRU{dir: dir}, nil
}

// Dir returns the pg_subtrans directory this mirror writes to. Intended for
// tests / introspection.
func (s *SubtransSLRU) Dir() string {
	return s.dir
}

// segmentByteOffset returns the segment number and the byte offset within that
// segment file at which xid's 4-byte parent slot lives. Mirrors subtrans.c's
// TransactionIdToPage / TransactionIdToEntry arithmetic, scaled to 4-byte
// entries and BLCKSZ pages.
func subtransLocate(xid storage.TransactionID) (segNo uint64, byteOffset int64) {
	segNo = uint64(xid) / subtransXactsPerSegment
	xidInSeg := uint64(xid) % subtransXactsPerSegment
	pageInSeg := xidInSeg / subtransXactsPerPage
	xidInPage := xidInSeg % subtransXactsPerPage
	byteOffset = int64(pageInSeg)*int64(storage.BlockSize) + int64(xidInPage)*int64(subtransBytesPerXact)
	return segNo, byteOffset
}

// SetParent records that xid's parent (immediate enclosing transaction) is
// parent, writing the 4-byte little-endian parent TransactionId into the
// matching pg_subtrans segment file. The segment file is extended in BLCKSZ
// page units (so SimpleLruReadPage_ReadOnly always sees a complete page) and
// fsynced. Mirrors subtrans.c:SubTransSetParent and the segment/extend/fsync
// structure of clog.go's mirrorToSLRUUnlocked.
//
// XIDs below FirstNormalTransactionID are skipped: PG never records a parent
// for the bootstrap/frozen/invalid XIDs (see subtrans.c, which is only ever
// called for real subtransaction XIDs), and leaving those slots zero keeps
// basebackup byte-equality with PG's own pg_subtrans.
func (s *SubtransSLRU) SetParent(xid, parent storage.TransactionID) error {
	if xid < FirstNormalTransactionID {
		return nil
	}
	segNo, byteOffset := subtransLocate(xid)
	// The page index within the segment that must exist to hold xid's slot.
	xidInSeg := uint64(xid) % subtransXactsPerSegment
	pageInSeg := xidInSeg / subtransXactsPerPage
	minSize := (int64(pageInSeg) + 1) * int64(storage.BlockSize)

	var le [subtransBytesPerXact]byte
	le[0] = byte(parent)
	le[1] = byte(parent >> 8)
	le[2] = byte(parent >> 16)
	le[3] = byte(parent >> 24)

	s.mu.Lock()
	defer s.mu.Unlock()

	segPath := filepath.Join(s.dir, fmt.Sprintf("%04X", segNo))
	f, err := os.OpenFile(segPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("subtrans slru: open %q: %w", segPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("subtrans slru: stat %q: %w", segPath, err)
	}
	if fi.Size() < minSize {
		if err := f.Truncate(minSize); err != nil {
			return fmt.Errorf("subtrans slru: extend %q: %w", segPath, err)
		}
	}
	if _, err := f.WriteAt(le[:], byteOffset); err != nil {
		return fmt.Errorf("subtrans slru: write %q@%d: %w", segPath, byteOffset, err)
	}
	return f.Sync()
}

// GetParent returns the recorded parent TransactionId for xid, or
// storage.InvalidTransactionID (0) when xid has no entry (never registered,
// out of range, or below FirstNormalTransactionID). Reads the 4-byte
// little-endian slot from the matching segment file. Mirrors
// subtrans.c:SubTransGetParent.
func (s *SubtransSLRU) GetParent(xid storage.TransactionID) (storage.TransactionID, error) {
	if xid < FirstNormalTransactionID {
		return storage.InvalidTransactionID, nil
	}
	segNo, byteOffset := subtransLocate(xid)

	s.mu.Lock()
	defer s.mu.Unlock()

	segPath := filepath.Join(s.dir, fmt.Sprintf("%04X", segNo))
	f, err := os.Open(segPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return storage.InvalidTransactionID, nil
		}
		return storage.InvalidTransactionID, fmt.Errorf("subtrans slru: open %q: %w", segPath, err)
	}
	defer f.Close()

	var le [subtransBytesPerXact]byte
	if _, err := f.ReadAt(le[:], byteOffset); err != nil {
		if errors.Is(err, io.EOF) {
			// Slot not yet written — treated as unset.
			return storage.InvalidTransactionID, nil
		}
		return storage.InvalidTransactionID, fmt.Errorf("subtrans slru: read %q@%d: %w", segPath, byteOffset, err)
	}
	parent := storage.TransactionID(le[0]) |
		storage.TransactionID(le[1])<<8 |
		storage.TransactionID(le[2])<<16 |
		storage.TransactionID(le[3])<<24
	return parent, nil
}

// ScanParents reads every segment file under the SLRU directory and returns the
// recorded subxid → parent links (gap G5 read path / restore-on-restart). It is
// the inverse of the page/segment arithmetic in subtransLocate + SetParent:
//
//	xid = segNo*subtransXactsPerSegment
//	    + pageInSeg*subtransXactsPerPage
//	    + xidInPage
//
// Only slots whose parent is a normal XID (>= FirstNormalTransactionID) are
// returned; zero slots — never-written entries, and the bootstrap-zeroed 0000
// segment created by initdb — are skipped, so a freshly-initialised cluster
// scans to an empty map. A segment that was extended only far enough to hold its
// highest written slot yields a short final page; whatever bytes exist are
// decoded and any trailing partial slot is ignored. Mirrors the structure of
// clog.go's SLRU backfill on the recovery path.
func (s *SubtransSLRU) ScanParents() (map[storage.TransactionID]storage.TransactionID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[storage.TransactionID]storage.TransactionID)
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("subtrans slru: readdir %q: %w", s.dir, err)
	}

	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		// Segment files are named exactly "%04X" (4 uppercase hex digits).
		var segNo uint64
		if n, perr := fmt.Sscanf(name, "%04X", &segNo); perr != nil || n != 1 || len(name) != 4 {
			continue
		}
		segPath := filepath.Join(s.dir, name)
		data, rerr := os.ReadFile(segPath)
		if rerr != nil {
			return nil, fmt.Errorf("subtrans slru: read %q: %w", segPath, rerr)
		}
		segBase := segNo * subtransXactsPerSegment
		// Walk every complete 4-byte slot; a slot maps to its XID via the page
		// layout (subtransPagesPerSegment pages of subtransXactsPerPage slots).
		nSlots := len(data) / subtransBytesPerXact
		for slot := 0; slot < nSlots; slot++ {
			off := slot * subtransBytesPerXact
			parent := storage.TransactionID(data[off]) |
				storage.TransactionID(data[off+1])<<8 |
				storage.TransactionID(data[off+2])<<16 |
				storage.TransactionID(data[off+3])<<24
			if parent < FirstNormalTransactionID {
				continue // zero/unset slot, or a non-normal parent we never write
			}
			pageInSeg := uint64(off) / uint64(storage.BlockSize)
			xidInPage := (uint64(off) % uint64(storage.BlockSize)) / subtransBytesPerXact
			xid := storage.TransactionID(segBase + pageInSeg*subtransXactsPerPage + xidInPage)
			if xid < FirstNormalTransactionID {
				continue
			}
			out[xid] = parent
		}
	}
	return out, nil
}
