package initdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// CatalogSnapshotFile is the relative path inside the data
// directory where the catalog snapshot lives. The file is JSON for
// v0 — see internal/catalog/persist.go and
// docs/design/0017-data-directory.md.
const CatalogSnapshotFile = "global/pg_catalog.json"

// Runtime is the bundle of long-lived handles a running goopg
// server needs to drive table-touching statements: a storage
// Manager + Pool, an MVCC manager, and an in-memory catalog. Each
// component is independently usable from tests; Open is the
// production entry point that constructs the four together against
// a real data directory.
type Runtime struct {
	StorageMgr   *storage.Manager
	Pool         *storage.Pool
	TxnMgr       *mvcc.Manager
	Catalog      catalog.Catalog
	WAL          *wal.Writer
	Checkpointer *wal.Checkpointer
	DataDir      string
}

// OpenOptions controls Open.
type OpenOptions struct {
	// DataDir must point at a directory previously initialized by
	// goopg init (PG_VERSION present). Open does not run init for
	// you — that's a separate operator action.
	DataDir string

	// PoolSlots is the number of buffer pool slots to allocate.
	// Defaults to 1024 when zero, matching upstream's boot-time
	// shared_buffers floor.
	PoolSlots int

	// AlignedIO forwards through to storage.ManagerConfig.AlignedIO.
	// Production deployments want O_DIRECT|O_DSYNC; tests typically
	// leave this false.
	AlignedIO bool
}

// Open prepares a Runtime against an existing data directory.
//
// v0 starts every server with an empty in-memory catalog because
// the on-disk catalog (`global/pg_class`, `pg_attribute`, etc.) is
// not yet implemented. Schema declared via SQL during a session
// vanishes when the process exits; persistence lands alongside the
// catalog work in milestone 7+. The data files themselves on the
// other hand do persist via the storage manager.
func Open(opts OpenOptions) (*Runtime, error) {
	if opts.DataDir == "" {
		return nil, errors.New("goopg: -D <data-directory> is required")
	}
	abs, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("goopg: resolve %q: %w", opts.DataDir, err)
	}
	if err := verifyInitialized(abs); err != nil {
		return nil, err
	}

	slots := opts.PoolSlots
	if slots <= 0 {
		slots = 1024
	}

	mgr := storage.NewManager(storage.ManagerConfig{
		DataDir:   abs,
		AlignedIO: opts.AlignedIO,
	})

	// Crash recovery: replay any WAL records past the last
	// checkpoint into the data files BEFORE the buffer pool comes
	// online, so the pool always observes a consistent on-disk
	// state. Replay is idempotent — a clean shutdown leaves no
	// records past the last checkpoint, so this is a no-op for
	// normal restarts. See M0002 "Crash-recovery test" in
	// .ralph/fix_plan.md.
	if _, err := wal.ReplayFromDirWithMgr(mgr, filepath.Join(abs, "pg_wal"), 0); err != nil {
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: wal replay: %w", err)
	}

	walWriter, err := wal.NewWriter(wal.Config{
		WALDir: filepath.Join(abs, "pg_wal"),
	})
	if err != nil {
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: wal: %w", err)
	}

	// Bridge the buffer pool's FPI hook to the WAL writer.
	// storage cannot import wal (wal imports storage), so we
	// adapt via a closure here.
	logFPI := func(rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) (storage.LSN, error) {
		payload, err := wal.EncodePageImage(rel, blk, page)
		if err != nil {
			return 0, err
		}
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}

	// Atomic B-tree split record (Landing 3a of M0002 — see
	// docs/design/0002-0002-btree-concurrency.md). Same import-cycle
	// dodge as logFPI.
	logBtreeSplit := func(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page) (storage.LSN, error) {
		payload, err := wal.EncodeBtreeSplit(rel, leftBlk, rightBlk, leftPage, rightPage)
		if err != nil {
			return 0, err
		}
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}

	// Logical heap-insert change record (M0002 redo-records —
	// see docs/design/0002-0003-redo-records.md).
	logHeapInsert := func(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, tuple []byte) (storage.LSN, error) {
		payload := wal.EncodeHeapInsert(rel, blk, lineSlot, tuple)
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}

	// Logical btree non-split insert change record.
	logBtreeInsert := func(rel storage.RelFileNode, blk storage.BlockNumber, item []byte) (storage.LSN, error) {
		payload := wal.EncodeBtreeInsert(rel, blk, item)
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}

	// Logical heap-delete (xmax stamp) change record.
	logHeapDelete := func(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID) (storage.LSN, error) {
		payload := wal.EncodeHeapDelete(rel, blk, lineSlot, xmax)
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}

	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:          slots,
		WAL:            walWriter,
		LogPageImage:   logFPI,
		LogBtreeSplit:  logBtreeSplit,
		LogHeapInsert:  logHeapInsert,
		LogBtreeInsert: logBtreeInsert,
		LogHeapDelete:  logHeapDelete,
		FullPageWrites: true,
	})
	if err != nil {
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: bufpool: %w", err)
	}

	cat := catalog.NewInMemory()
	txnMgr := mvcc.NewManager()
	if err := loadCatalogSnapshot(abs, cat, txnMgr); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}

	cp := wal.NewCheckpointer(pool, walWriter, wal.CheckpointerConfig{})

	return &Runtime{
		StorageMgr:   mgr,
		Pool:         pool,
		TxnMgr:       txnMgr,
		Catalog:      cat,
		WAL:          walWriter,
		Checkpointer: cp,
		DataDir:      abs,
	}, nil
}

// loadCatalogSnapshot reads <dir>/global/pg_catalog.json (if
// present) into cat. A missing file is fine — that's the
// fresh-from-init case. Anything else (read error, JSON parse
// error, Restore error) propagates: the operator is better off
// seeing a startup failure than running with a half-loaded
// schema.
func loadCatalogSnapshot(dir string, cat *catalog.InMemory, txnMgr *mvcc.Manager) error {
	path := filepath.Join(dir, CatalogSnapshotFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("goopg: read catalog snapshot %q: %w", path, err)
	}
	var snap catalog.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("goopg: parse catalog snapshot %q: %w", path, err)
	}
	if err := cat.Restore(snap); err != nil {
		return fmt.Errorf("goopg: restore catalog snapshot %q: %w", path, err)
	}
	if snap.NextXID != 0 {
		// Advance the in-memory transaction counter past the saved
		// horizon so heap tuples from previous sessions appear
		// committed (xmin < snap.Xmin) to the new session's
		// snapshots.
		txnMgr.SetNextXID(storage.TransactionID(snap.NextXID))
	}
	return nil
}

// SaveCatalog writes the in-memory catalog to disk so a subsequent
// Open recovers the same schema. Callers (typically the goopg
// start shutdown path) should call this before Close. Returns nil
// when r is nil or the catalog isn't an *InMemory (i.e. someone
// supplied a custom catalog implementation in tests).
//
// The write is atomic: data lands in <path>.tmp first, then
// rename(2) makes it visible. A crash between the temp file and
// the rename leaves the previous snapshot intact.
func (r *Runtime) SaveCatalog() error {
	if r == nil || r.Catalog == nil {
		return nil
	}
	cat, ok := r.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	snap := cat.Snapshot()
	if r.TxnMgr != nil {
		snap.NextXID = uint32(r.TxnMgr.NextXID())
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("goopg: marshal catalog snapshot: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(r.DataDir, CatalogSnapshotFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("goopg: mkdir for catalog snapshot: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("goopg: write catalog snapshot tempfile: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("goopg: rename catalog snapshot: %w", err)
	}
	return nil
}

// Close releases the runtime's storage handles. Safe to call
// multiple times — subsequent calls are no-ops.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	var firstErr error
	if r.Pool != nil {
		if err := r.Pool.Close(); err != nil {
			firstErr = err
		}
		r.Pool = nil
	}
	if r.WAL != nil {
		if err := r.WAL.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		r.WAL = nil
	}
	if r.StorageMgr != nil {
		if err := r.StorageMgr.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		r.StorageMgr = nil
	}
	return firstErr
}

// verifyInitialized fails fast when the target directory is missing
// or wasn't laid out by goopg init. The check is intentionally
// shallow — we look for PG_VERSION and an exact match of the
// catalog version we shipped — so that an operator hasn't pointed
// the server at someone else's data directory by accident.
func verifyInitialized(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("goopg: data directory %q does not exist (run 'goopg init -D %s' first)", dir, dir)
		}
		return fmt.Errorf("goopg: stat %q: %w", dir, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("goopg: %q is not a directory", dir)
	}
	pv, err := os.ReadFile(filepath.Join(dir, "PG_VERSION"))
	if err != nil {
		return fmt.Errorf("goopg: %q is not initialized (run 'goopg init -D %s' first)", dir, dir)
	}
	got := strings.TrimSpace(string(pv))
	if got != CatalogVersion {
		return fmt.Errorf("goopg: data directory catalog version %q does not match this binary (%q)", got, CatalogVersion)
	}
	return nil
}
