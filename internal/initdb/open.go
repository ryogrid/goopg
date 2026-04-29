package initdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goopg/goopg/internal/aio"
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
	Slots        *wal.Slots
	WalSenders     *wal.Senders
	WalReceivers   *wal.Receivers
	WalSubscribers *wal.Subscribers
	PubSub         *catalog.PubSub
	AIO            *aio.Engine
	DataDir        string

	// Standby is true when `<DataDir>/standby.signal` was present
	// at Open time. cmd/goopg start uses this to decide whether to
	// dial the configured `primary_conninfo` and spawn a
	// `WalReceiver` instead of accepting client writes directly.
	// See docs/design/0005-0001-streaming-replication-architecture.md.
	Standby bool
}

// OpenOptions controls Open.
type OpenOptions struct {
	// DataDir must point at a directory previously initialized by
	// goopg init (PG_VERSION present). Open does not run init for
	// you — that's a separate operator action.
	DataDir string

	// PoolSlots is the number of buffer pool slots to allocate.
	// Defaults to 16384 when zero (128 MB at BlockSize=8 KB), which
	// matches upstream PostgreSQL's `shared_buffers` boot default.
	// cmd/goopg start derives this from the shared_buffers GUC so
	// postgresql.conf overrides flow through; tests pass an explicit
	// small value.
	PoolSlots int

	// AlignedIO forwards through to storage.ManagerConfig.AlignedIO.
	// Production deployments want O_DIRECT|O_DSYNC; tests typically
	// leave this false.
	AlignedIO bool

	// WALInitZero, when true, forwards to wal.Config.Preallocate
	// so new WAL segments are zero-filled to SegmentSize at
	// creation time. Mirrors upstream's `wal_init_zero` GUC.
	// cmd/goopg start sets this from the GUC; tests typically
	// leave it false.
	WALInitZero bool

	// AIO* control the AIO engine the storage manager (and
	// future heap-scan / checkpointer / WAL-writer callers)
	// will use. Maps to the upstream-aligned `io_method`,
	// `io_workers`, and `io_max_concurrency` GUCs. When
	// AIOMethod is empty, no engine is attached and the
	// synchronous code paths run unchanged.
	AIOMethod         string
	AIOWorkers        int
	AIOMaxConcurrency int
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
		slots = 16384
	}

	mgr := storage.NewManager(storage.ManagerConfig{
		DataDir:   abs,
		AlignedIO: opts.AlignedIO,
	})

	// AIO engine: optional. With opts.AIOMethod=="" no engine is
	// constructed and storage Manager.PrefetchBlock falls back to
	// synchronous reads. When set we adapt the engine through a
	// thin shim so internal/storage doesn't have to import
	// internal/aio (the aio package would otherwise need to know
	// about storage.RelFileNode types).
	var aioEngine *aio.Engine
	if opts.AIOMethod != "" {
		eng, err := aio.NewEngine(aio.EngineConfig{
			Method:         opts.AIOMethod,
			Workers:        opts.AIOWorkers,
			MaxConcurrency: opts.AIOMaxConcurrency,
		})
		if err != nil {
			_ = mgr.Close()
			return nil, fmt.Errorf("goopg: aio: %w", err)
		}
		aioEngine = eng
		mgr.SetAIO(aioEngineAdapter{eng: eng})
	}

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
		WALDir:      filepath.Join(abs, "pg_wal"),
		Preallocate: opts.WALInitZero,
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

	// Logical heap-vacuum (page prune) change record.
	logHeapVacuum := func(rel storage.RelFileNode, blk storage.BlockNumber, deadSlots []uint16) (storage.LSN, error) {
		payload := wal.EncodeHeapVacuum(rel, blk, deadSlots)
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
		LogHeapVacuum:  logHeapVacuum,
		FullPageWrites: true,
	})
	if err != nil {
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: bufpool: %w", err)
	}
	// With an AIO engine attached, opt the Pool into prefetching
	// so heap-scan / future bitmap-scan / ANALYZE callers issuing
	// `Pool.Prefetch(tag)` hints actually warm the page cache via
	// the engine's worker pool rather than no-oping.
	if aioEngine != nil {
		pool.SetPrefetchEnabled(true)
	}

	cat := catalog.NewInMemory()
	txnMgr := mvcc.NewManager()
	// Wire the M0008 logical-decoding xact-marker hook: every
	// successful Commit / Rollback against this manager appends
	// an EncodeXactCommit / EncodeXactAbort record to the WAL
	// stream so the M0008 classifier can drive its reorder
	// buffer. Errors propagate back through Commit / Rollback so
	// a WAL-append failure stops the txn from finishing. See
	// docs/design/0008-0001-logical-decoding-pipeline.md.
	txnMgr.SetXactMarkerLogger(func(xid storage.TransactionID, kind mvcc.XactMarker) error {
		var payload []byte
		switch kind {
		case mvcc.XactCommit:
			payload = wal.EncodeXactCommit(xid)
		case mvcc.XactAbort:
			payload = wal.EncodeXactAbort(xid)
		default:
			return fmt.Errorf("goopg: unknown xact marker %v", kind)
		}
		_, _, err := walWriter.Append(payload)
		return err
	})
	if err := loadCatalogSnapshot(abs, cat, txnMgr); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}

	// Replication-slot registry. The retention path on the
	// checkpointer (wired below in cmd/goopg start once the GUC
	// values are known) consults this to decide which WAL
	// segments are still pinned by a live standby. Opening the
	// registry here also makes slots available to the
	// CREATE_REPLICATION_SLOT / DROP_REPLICATION_SLOT wire
	// handlers without an extra OpenSlots call from main.
	slotsReg, err := wal.OpenSlots(abs)
	if err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: replication slots: %w", err)
	}

	// Replication monitoring registries. Senders are registered by
	// each walsender goroutine on entry; the (single) Receivers
	// entry is registered by the standby's walreceiver. The two
	// virtual views below render whichever entries are live.
	walSenders := wal.NewSenders()
	walReceivers := wal.NewReceivers()
	walSubscribers := wal.NewSubscribers()

	cp := wal.NewCheckpointer(pool, walWriter, wal.CheckpointerConfig{})

	// Surface the M0002 checkpointer counters as the
	// pg_stat_checkpointer virtual table so operators can observe
	// timer-vs-requested cadence and write_time without attaching
	// a debugger. Column shape mirrors PG 18.x's view; the
	// upstream-aligned columns we don't track in v0 (restartpoints*,
	// buffers_written, slru_written) report literal 0.
	if err := registerStatCheckpointerView(cat, cp); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}
	// pg_stat_replication: one row per active walsender. Backed by
	// the in-process Senders registry — walsender goroutines
	// register themselves on entry and unregister on exit.
	if err := registerStatReplicationView(cat, walSenders); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}
	// pg_stat_wal_receiver: zero or one row depending on whether the
	// standby's walreceiver is currently registered.
	if err := registerStatWalReceiverView(cat, walReceivers); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}
	// pg_stat_subscription: one row per active subscriber worker
	// (leader apply + per-rel tablesync). Backed by the Subscribers
	// registry — apply / tablesync goroutines register on entry and
	// unregister on exit. See
	// docs/design/0008-0005-logical-replication-observability.md.
	if err := registerStatSubscriptionView(cat, walSubscribers); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}
	// pg_replication_slots: one row per persistent replication slot
	// (physical or logical). Backed by the *wal.Slots registry.
	if err := registerReplicationSlotsView(cat, slotsReg); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}
	// Publication / subscription registry + their five virtual
	// catalog views (pg_publication, pg_publication_rel,
	// pg_publication_tables, pg_subscription, pg_subscription_rel).
	// See docs/design/0008-0003-publication-subscription-ddl.md.
	pubsub := catalog.NewPubSub()
	if err := registerPublicationViews(cat, pubsub); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}
	if err := registerSubscriptionViews(cat, pubsub); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}

	standby, err := IsStandby(abs)
	if err != nil {
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: standby signal: %w", err)
	}

	return &Runtime{
		StorageMgr:   mgr,
		Pool:         pool,
		TxnMgr:       txnMgr,
		Catalog:      cat,
		WAL:          walWriter,
		Checkpointer: cp,
		Slots:        slotsReg,
		WalSenders:     walSenders,
		WalReceivers:   walReceivers,
		WalSubscribers: walSubscribers,
		PubSub:         pubsub,
		AIO:            aioEngine,
		DataDir:        abs,
		Standby:        standby,
	}, nil
}

// aioEngineAdapter bridges *aio.Engine to storage.AIOEngine.
// Lives here because internal/storage cannot import internal/aio
// (and shouldn't — keeping storage AIO-agnostic at the type level
// lets tests substitute fake engines without dragging the whole
// package graph along).
type aioEngineAdapter struct {
	eng *aio.Engine
}

// Submit fans the storage-shaped op out to the aio engine.
// The returned aio.*Handle satisfies storage.AIOHandle by
// adapting Wait through aioHandleAdapter.
func (a aioEngineAdapter) Submit(op storage.AIOSubmitOp) storage.AIOHandle {
	return aioHandleAdapter{
		h: a.eng.Submit(aio.Op{
			File:      aioFileAdapter{f: op.File},
			Buffer:    op.Buffer,
			Offset:    op.Offset,
			Direction: aio.DirRead,
		}),
	}
}

// aioFileAdapter exposes a storage.AIOFile (ReadAt-only) to the
// aio engine, which expects a full aio.File (ReadAt + WriteAt).
// PrefetchBlock is read-only, so WriteAt is a panic in case the
// engine's contract grows to call it for non-DirRead Ops — that
// would be a bug, not graceful fallback.
type aioFileAdapter struct {
	f storage.AIOFile
}

func (a aioFileAdapter) ReadAt(p []byte, off int64) (int, error) {
	return a.f.ReadAt(p, off)
}

func (a aioFileAdapter) WriteAt(_ []byte, _ int64) (int, error) {
	panic("initdb: aioFileAdapter.WriteAt called on a read-only handle")
}

// aioHandleAdapter unwraps the aio.Result struct into the
// (n, err) pair storage.AIOHandle exposes.
type aioHandleAdapter struct {
	h *aio.Handle
}

func (a aioHandleAdapter) Wait() (int, error) {
	r := a.h.Wait()
	return r.N, r.Err
}

// registerStatCheckpointerView installs the
// `pg_catalog.pg_stat_checkpointer` virtual table backed by
// `Checkpointer.Stats`. Column ordering matches upstream PG 18.x.
func registerStatCheckpointerView(cat *catalog.InMemory, cp *wal.Checkpointer) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_stat_checkpointer",
		Columns: []catalog.Column{
			{Name: "num_timed", Type: catalog.Type{Name: "text"}},
			{Name: "num_requested", Type: catalog.Type{Name: "text"}},
			{Name: "restartpoints_timed", Type: catalog.Type{Name: "text"}},
			{Name: "restartpoints_req", Type: catalog.Type{Name: "text"}},
			{Name: "restartpoints_done", Type: catalog.Type{Name: "text"}},
			{Name: "write_time", Type: catalog.Type{Name: "text"}},
			{Name: "sync_time", Type: catalog.Type{Name: "text"}},
			{Name: "total_time", Type: catalog.Type{Name: "text"}},
			{Name: "buffers_written", Type: catalog.Type{Name: "text"}},
			{Name: "slru_written", Type: catalog.Type{Name: "text"}},
			{Name: "stats_reset", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		s := cp.Stats()
		return [][]string{{
			fmt.Sprintf("%d", s.NumTimed),
			fmt.Sprintf("%d", s.NumRequested),
			"0", // restartpoints_timed (no replication in v0)
			"0", // restartpoints_req
			"0", // restartpoints_done
			fmt.Sprintf("%d", s.WriteTimeMs),
			"0", // sync_time (not separated from write_time in v0)
			fmt.Sprintf("%d", s.WriteTimeMs),
			"0", // buffers_written (per-checkpoint counter not yet wired)
			"0", // slru_written
			s.StatsResetAt.UTC().Format("2006-01-02 15:04:05.000-07"),
		}}
	}
	return cat.RegisterVirtualTable(tbl)
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
	if r.AIO != nil {
		// Close after the storage manager so any AIO handles
		// the manager held drain cleanly. The engine's own
		// Close is idempotent and waits for worker goroutines
		// to join before returning.
		if err := r.AIO.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		r.AIO = nil
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
