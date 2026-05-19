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
	// Backfill any in-memory committed/aborted entries so an opened clog
	// (which loaded data from the flat file but had no SLRU mirror) is
	// projected to the SLRU on first enable. This covers the recovery path
	// in initdb.Open where the flat clog is read before the mirror is
	// wired.
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
