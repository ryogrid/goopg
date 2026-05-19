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

// TxnStatus is the commit-log status for a single transaction ID.
type TxnStatus byte

const (
	// TxnStatusUnknown means no COMMIT or ROLLBACK record was written for this
	// XID. After a crash, any XID with this status is treated as aborted.
	TxnStatusUnknown TxnStatus = 0
	// TxnStatusCommitted records a successful COMMIT.
	TxnStatusCommitted TxnStatus = 1
	// TxnStatusAborted records a ROLLBACK or crash-implied abort.
	TxnStatusAborted TxnStatus = 2
)

// CLog is a flat-file persistent commit log that maps transaction IDs to their
// terminal status. One byte per XID; stored at <DataDir>/global/pg_xact.
//
// Thread-safe: all public methods hold mu for the duration of the operation.
type CLog struct {
	mu      sync.RWMutex
	path    string
	data    []byte // data[xid] = TxnStatus; grows on demand
	slruDir string // optional: PG-canonical pg_xact/ SLRU directory; empty = mirror disabled
}

// OpenCLog opens (or creates) the commit log at path. If the file exists its
// contents are loaded; if not, an empty in-memory CLog is returned. The clog
// file is created on the first SetCommitted / SetAborted call.
func OpenCLog(path string) (*CLog, error) {
	c := &CLog{path: path}
	raw, err := os.ReadFile(path)
	if err == nil {
		c.data = raw
		return c, nil
	}
	if os.IsNotExist(err) {
		return c, nil // fresh clog — file created on first write
	}
	return nil, fmt.Errorf("clog: read %q: %w", path, err)
}

// IsEmpty reports whether the clog has no entries yet (file was absent or the
// on-disk data is zero-length). Used by Open to detect the upgrade case.
func (c *CLog) IsEmpty() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.data) == 0
}

// GetStatus returns the recorded status for xid. Returns TxnStatusUnknown if
// xid has no entry (transaction never finished or XID is out of range).
func (c *CLog) GetStatus(xid storage.TransactionID) TxnStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	idx := int(xid)
	if idx >= len(c.data) {
		return TxnStatusUnknown
	}
	return TxnStatus(c.data[idx])
}

// SetCommitted marks xid as committed and persists the change to disk.
func (c *CLog) SetCommitted(xid storage.TransactionID) error {
	return c.setStatus(xid, TxnStatusCommitted)
}

// SetAborted marks xid as aborted and persists the change to disk.
func (c *CLog) SetAborted(xid storage.TransactionID) error {
	return c.setStatus(xid, TxnStatusAborted)
}

// InitializeAsCommitted marks every XID in the range [1, highXID) as
// TxnStatusCommitted, leaving entries that are already non-zero unchanged.
// Called by Open when the clog file was absent (upgrade from a pre-clog
// cluster): all XIDs assigned before the clog existed are assumed committed.
func (c *CLog) InitializeAsCommitted(highXID storage.TransactionID) error {
	if highXID == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	top := int(highXID)
	// Grow if needed.
	for len(c.data) < top {
		c.data = append(c.data, byte(TxnStatusUnknown))
	}
	// Mark [1, top) as committed, preserving any entries already set.
	for i := 1; i < top; i++ {
		if c.data[i] == byte(TxnStatusUnknown) {
			c.data[i] = byte(TxnStatusCommitted)
		}
	}
	return os.WriteFile(c.path, c.data, 0600)
}

// MarkUnknownAsAborted marks every XID in the range [1, highXID) whose current
// status is TxnStatusUnknown as TxnStatusAborted, leaving Committed/Aborted
// entries unchanged. Called by Open() after WAL replay finishes to implement
// crash-recovery's "any xid not explicitly Committed is treated as Aborted"
// semantics (M0106-0011): a transaction that wrote heap rows but crashed
// before its commit/abort marker reached disk leaves its xid as Unknown in
// the local clog, and downstream visibility filters need an explicit Aborted
// stamp to exclude its rows. Mirrors PostgreSQL's recovery-time treatment of
// TRANSACTION_STATUS_IN_PROGRESS CLOG slots.
//
// CAUTION for basebackup-attached clusters: upstream xids that pre-date the
// attach are not present in our local clog and would be incorrectly marked
// Aborted by this sweep. Such clusters MUST call InitializeAsCommitted with
// the upstream cluster's nextXid BEFORE this sweep runs so the upstream
// range is already Committed.
func (c *CLog) MarkUnknownAsAborted(highXID storage.TransactionID) error {
	if highXID == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	top := int(highXID)
	// Grow data to include the full sweep range. Newly-appended slots
	// default to TxnStatusUnknown and will be stamped Aborted below
	// (these correspond to xids that were allocated but whose status
	// was never written, e.g. crashed in progress).
	for len(c.data) < top {
		c.data = append(c.data, byte(TxnStatusUnknown))
	}
	dirty := false
	for i := 1; i < top; i++ {
		if c.data[i] == byte(TxnStatusUnknown) {
			c.data[i] = byte(TxnStatusAborted)
			dirty = true
			if err := c.mirrorToSLRULocked(storage.TransactionID(i), TxnStatusAborted); err != nil {
				return err
			}
		}
	}
	if !dirty {
		return nil
	}
	return os.WriteFile(c.path, c.data, 0600)
}

// setStatus updates data[xid] = status and rewrites the file. When a PG SLRU
// directory has been wired (see EnablePGSLRUMirror), the matching 2-bit lane
// of <slruDir>/<segno>:<page>:<byte> is also updated so a PG standby reading
// the basebackup-shipped pg_xact/ via SimpleLruReadPage_ReadOnly observes the
// correct status. M0106-0010 batched-44.
func (c *CLog) setStatus(xid storage.TransactionID, status TxnStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := int(xid)
	// Grow buffer to accommodate xid.
	for len(c.data) <= idx {
		c.data = append(c.data, byte(TxnStatusUnknown))
	}
	if TxnStatus(c.data[idx]) == status {
		return nil // idempotent — no write needed
	}
	c.data[idx] = byte(status)
	if err := os.WriteFile(c.path, c.data, 0600); err != nil {
		return err
	}
	return c.mirrorToSLRULocked(xid, status)
}

// PG SLRU CLOG layout constants. PG18 packs 2 bits per XID into bytes ordered
// as 4 lanes (lane = xid % 4, shift = lane * 2). 8192 bytes per page * 4 XIDs
// per byte = 32768 XIDs per page; 32 pages per segment file (named %04X of
// segno). See postgres/src/backend/access/transam/clog.c and slru.h.
const (
	clogBitsPerXact     = 2
	clogXactsPerByte    = 4
	clogXactsPerPage    = storage.BlockSize * clogXactsPerByte // 32768 with BLCKSZ=8192
	slruPagesPerSegment = 32
	clogXactsPerSegment = clogXactsPerPage * slruPagesPerSegment // 1048576

	// PG XidStatus constants, must match TRANSACTION_STATUS_* in
	// postgres/src/include/access/clog.h.
	pgClogStatusInProgress = 0x00
	pgClogStatusCommitted  = 0x01
	pgClogStatusAborted    = 0x02
)

// EnablePGSLRUMirror wires this CLog to also write each
// SetCommitted/SetAborted into a PG-canonical pg_xact/ SLRU segment file under
// dir. Creates the directory and the initial segment file (zeroed BLCKSZ
// page, mirroring PG's BootStrapCLOG → SimpleLruZeroPage(0)) if they don't
// exist, so a fresh PG standby attaching via basebackup can read the SLRU
// without trying to extend a missing first page. Idempotent. M0106-0010
// batched-44.
func (c *CLog) EnablePGSLRUMirror(dir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("clog slru: mkdir %q: %w", dir, err)
	}
	seg0 := filepath.Join(dir, "0000")
	if _, err := os.Stat(seg0); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(seg0, make([]byte, storage.BlockSize), 0600); err != nil {
			return fmt.Errorf("clog slru: write zero page %q: %w", seg0, err)
		}
	} else if err != nil {
		return fmt.Errorf("clog slru: stat %q: %w", seg0, err)
	}
	c.slruDir = dir

	// M0106-0013: load committed/aborted entries from the on-disk SLRU
	// files into c.data BEFORE the backfill below. The SLRU is fsynced on
	// every commit (mirrorToSLRULocked calls f.Sync()), making it the most
	// durable source of truth after a crash. The flat-file (c.data loaded
	// by OpenCLog) is written without fsync and may be stale or truncated
	// (os.WriteFile uses O_TRUNC; a crash between truncate and write leaves
	// a zero-length file). By loading SLRU first, crash-recovery sees the
	// correct committed/aborted status even when the flat-file is missing.
	if err := c.loadFromSLRULocked(dir); err != nil {
		return err
	}

	// Backfill any in-memory committed/aborted entries so an opened clog
	// (which loaded data from the flat file but had no SLRU mirror) is
	// projected to the SLRU on first enable. mirrorToSLRULocked uses OR so
	// it never clears bits already set by loadFromSLRULocked above.
	for i := 1; i < len(c.data); i++ {
		st := TxnStatus(c.data[i])
		if st == TxnStatusCommitted || st == TxnStatusAborted {
			if err := c.mirrorToSLRULocked(storage.TransactionID(i), st); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadFromSLRULocked reads committed/aborted entries from existing SLRU
// segment files under dir and merges them into c.data. Called by
// EnablePGSLRUMirror before the backfill pass so the on-disk SLRU state
// (fsynced at every commit) wins over a potentially stale flat-file. mu
// must be held. Best-effort: segment files that cannot be read are skipped
// rather than returning an error — a missing or corrupt segment means the
// affected XIDs remain as Unknown and are handled by MarkUnknownAsAborted.
func (c *CLog) loadFromSLRULocked(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("clog slru: readdir %q: %w", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != 4 {
			continue
		}
		// Parse 4-hex-digit segment number (e.g. "0000", "0001").
		var segNo uint64
		valid := true
		for _, ch := range name {
			segNo <<= 4
			switch {
			case ch >= '0' && ch <= '9':
				segNo |= uint64(ch - '0')
			case ch >= 'A' && ch <= 'F':
				segNo |= uint64(ch - 'A' + 10)
			case ch >= 'a' && ch <= 'f':
				segNo |= uint64(ch - 'a' + 10)
			default:
				valid = false
			}
		}
		if !valid {
			continue
		}
		segData, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue // best-effort
		}
		// Decode each byte: 2 bits per XID, 4 XIDs per byte.
		// XID at (segNo, byteIdx, lane):
		//   pageInSeg  = byteIdx / BlockSize
		//   xidInPage  = (byteIdx % BlockSize) * clogXactsPerByte + lane
		//   XID        = segNo*clogXactsPerSegment + pageInSeg*clogXactsPerPage + xidInPage
		baseXID := segNo * uint64(clogXactsPerSegment)
		for i, b := range segData {
			if b == 0 {
				continue
			}
			pageInSeg := uint64(i) / uint64(storage.BlockSize)
			xidInPageBase := (uint64(i)%uint64(storage.BlockSize))*uint64(clogXactsPerByte)
			for lane := uint64(0); lane < uint64(clogXactsPerByte); lane++ {
				rawBits := (b >> (lane * uint64(clogBitsPerXact))) & 0x3
				var status TxnStatus
				switch rawBits {
				case pgClogStatusCommitted:
					status = TxnStatusCommitted
				case pgClogStatusAborted:
					status = TxnStatusAborted
				case pgClogStatusInProgress:
					continue
				default:
					// 0x03 = both bits set. This is PG's SUB_COMMITTED state
					// (not used by goopg), but can appear as a corruption
					// artifact if MarkUnknownAsAborted previously ORed the
					// aborted bit onto a committed XID. Treat as committed:
					// the committed bit (0x01) was definitely set at some
					// point.
					status = TxnStatusCommitted
				}
				xid := baseXID + pageInSeg*uint64(clogXactsPerPage) + xidInPageBase + lane
				if xid == 0 || xid > uint64(^storage.TransactionID(0)) {
					continue
				}
				idx := int(xid)
				for len(c.data) <= idx {
					c.data = append(c.data, byte(TxnStatusUnknown))
				}
				// SLRU is the authoritative source (fsynced); overwrite
				// whatever the flat-file had for this slot.
				c.data[idx] = byte(status)
			}
		}
	}
	return nil
}

// HighestKnownXID returns the highest XID that has a committed or aborted
// status in the clog. Returns 0 if no terminal status is recorded. Used at
// startup to advance txnMgr.NextXID past all previously committed XIDs so
// new snapshots have a high enough Xmax to see pre-crash rows. (M0106-0013)
func (c *CLog) HighestKnownXID() storage.TransactionID {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for i := len(c.data) - 1; i >= 1; i-- {
		if TxnStatus(c.data[i]) != TxnStatusUnknown {
			return storage.TransactionID(i)
		}
	}
	return 0
}

// SLRUDir returns the PG-canonical pg_xact/ directory, or "" if the mirror is
// disabled. Intended for tests.
func (c *CLog) SLRUDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.slruDir
}

// mirrorToSLRULocked writes the 2-bit lane for xid into the matching
// pg_xact/<segno> segment file. Caller must hold c.mu. No-op if the mirror is
// disabled or status is not a terminal committed/aborted code. Extends the
// segment file in BLCKSZ-page units so SimpleLruReadPage_ReadOnly sees a
// complete page.
func (c *CLog) mirrorToSLRULocked(xid storage.TransactionID, status TxnStatus) error {
	if c.slruDir == "" {
		return nil
	}
	// PG's TransactionLogFetch short-circuits BootstrapTransactionId (1) and
	// FrozenTransactionId (2) — and the unused InvalidTransactionId (0) — as
	// COMMITTED without consulting the SLRU (see
	// postgres/src/backend/access/transam/transam.c). PG's own initdb leaves
	// the corresponding lanes in pg_xact/0000 as zero; we mirror that
	// invariant so basebackup byte-equality holds.
	if xid < FirstNormalTransactionID {
		return nil
	}
	var bits byte
	switch status {
	case TxnStatusCommitted:
		bits = pgClogStatusCommitted
	case TxnStatusAborted:
		bits = pgClogStatusAborted
	default:
		return nil
	}
	segNo := uint64(xid) / clogXactsPerSegment
	xidInSeg := uint64(xid) % clogXactsPerSegment
	pageInSeg := xidInSeg / clogXactsPerPage
	xidInPage := xidInSeg % clogXactsPerPage
	byteOffset := int64(pageInSeg)*int64(storage.BlockSize) + int64(xidInPage/clogXactsPerByte)
	bShift := uint((xidInPage % clogXactsPerByte) * clogBitsPerXact)

	name := fmt.Sprintf("%04X", segNo)
	segPath := filepath.Join(c.slruDir, name)
	f, err := os.OpenFile(segPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("clog slru: open %q: %w", segPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("clog slru: stat %q: %w", segPath, err)
	}
	minSize := (int64(pageInSeg) + 1) * int64(storage.BlockSize)
	if fi.Size() < minSize {
		if err := f.Truncate(minSize); err != nil {
			return fmt.Errorf("clog slru: extend %q: %w", segPath, err)
		}
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], byteOffset); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("clog slru: read %q@%d: %w", segPath, byteOffset, err)
	}
	// Strict OR mirrors PG's TransactionIdSetStatusBit: lanes only advance
	// from in-progress to terminal. We never need to clear bits.
	b[0] |= bits << bShift
	if _, err := f.WriteAt(b[:], byteOffset); err != nil {
		return fmt.Errorf("clog slru: write %q@%d: %w", segPath, byteOffset, err)
	}
	return f.Sync()
}
