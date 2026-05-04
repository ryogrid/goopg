package initdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/activity"
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

	// Activity is the backend-activity registry backing
	// pg_catalog.pg_stat_activity (M0022 Stage A). nil when
	// not configured.
	Activity *activity.Registry

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

	// WALInitZero, when true, forwards to wal.Config.Preallocate
	// so new WAL segments are zero-filled to SegmentSize at
	// creation time. Mirrors upstream's `wal_init_zero` GUC.
	// cmd/goopg start sets this from the GUC; tests typically
	// leave it false.
	WALInitZero bool

	// WALSenderMemoryBuffer sizes the in-memory WAL byte ring
	// the walsender RecordIterator consults before falling
	// back to disk reads. 0 disables the ring. Mirrors the
	// `wal_sender_memory_buffer` GUC. See
	// docs/design/0010-0002-walsender-in-memory-wal-handoff.md.
	WALSenderMemoryBuffer int64

	// WALBuffers sizes the in-memory WAL buffer that holds
	// generated WAL records before they hit segment files. 0
	// disables the buffer (legacy behaviour). Mirrors the
	// `wal_buffers` GUC. See
	// docs/design/0013-0001-wal-buffers-architecture.md.
	WALBuffers int64

	// AIO* control the AIO engine the storage manager (and
	// future heap-scan / checkpointer / WAL-writer callers)
	// will use. Maps to the upstream-aligned `io_method`,
	// `io_workers`, and `io_max_concurrency` GUCs. When
	// AIOMethod is empty, no engine is attached and the
	// synchronous code paths run unchanged.
	AIOMethod         string
	AIOWorkers        int
	AIOMaxConcurrency int

	// WALSegmentSize overrides the WAL segment file size. 0 uses the
	// default (wal.DefaultSegmentSize = 16 MiB). Tests pass a smaller
	// value (e.g. 1 MiB) to exercise WAL retention with less data.
	// Maps directly to wal.Config.SegmentSize.
	WALSegmentSize int64
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
		DataDir: abs,
	})

	// Activity registry (M0022): must be created early so WAL writer,
	// AIO engine, and Manager hooks can register their goroutines via
	// activity.RegisterCurrentGoroutine. The pg_stat_activity virtual
	// view is registered later, after the catalog is fully set up.
	act := activity.NewRegistry()

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

	walCfg := wal.Config{
		WALDir:             filepath.Join(abs, "pg_wal"),
		SegmentSize:        opts.WALSegmentSize, // 0 → wal.DefaultSegmentSize
		Preallocate:        opts.WALInitZero,
		SenderMemoryBuffer: opts.WALSenderMemoryBuffer,
		WALBuffers:         opts.WALBuffers,
		OnLoopStart: func() {
			pid := "wal-writer-0"
			act.Register(&activity.Backend{
				PID:         pid,
				BackendType: "walwriter",
				State:       "active",
			})
			activity.RegisterCurrentGoroutine(act, pid)
		},
		OnLoopEnd: func() {
			activity.ClearCurrentGoroutine()
		},
		OnWALWrite: func() {
			if reg, pid := activity.LookupGoroutine(); reg != nil {
				reg.WaitEventStart(pid, activity.WaitTypeIO, activity.WaitWALWrite)
			}
		},
	}
	if aioEngine != nil {
		walCfg.AIO = walAIOEngineAdapter{eng: aioEngine}
	}
	walWriter, err := wal.NewWriter(walCfg)
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

	// Row-lock (lock-only xmax + lock-strength) change record.
	// M0021 tuple-level locking step 2 producer hook.
	logHeapLock := func(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, lockStrength uint16) (storage.LSN, error) {
		payload := wal.EncodeHeapLock(rel, blk, lineSlot, xmax, lockStrength)
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}

	// Relation-file creation WAL record (M0030-0002). Emitted by
	// Pool.PinNew when it creates block 0 of a new relfile so crash
	// recovery can recreate the file before replaying data pages.
	logSmgrCreate := func(rel storage.RelFileNode) error {
		payload := wal.EncodeSmgrCreate(rel)
		_, _, err := walWriter.Append(payload)
		return err
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
		LogHeapLock:    logHeapLock,
		LogSmgrCreate:  logSmgrCreate,
		FullPageWrites: true,
	})
	if err != nil {
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: bufpool: %w", err)
	}
	// Wire BufferPin wait event hook.
	pool.OnPinWait = func() {
		if reg, pid := activity.LookupGoroutine(); reg != nil {
			reg.WaitEventStart(pid, activity.WaitTypeBufferPin, activity.WaitBufferPin)
		}
	}
	// With an AIO engine attached, opt the Pool into prefetching
	// so heap-scan / future bitmap-scan / ANALYZE callers issuing
	// `Pool.Prefetch(tag)` hints actually warm the page cache via
	// the engine's worker pool rather than no-oping.
	if aioEngine != nil {
		pool.SetPrefetchEnabled(true)
		// Batch dirty-page flushes so the checkpointer's
		// FlushAllPaced pipelines writes through the engine.
		// Default batch = 8 (small enough that a checkpoint
		// pacing tick still fires reasonably often, large
		// enough to keep `io_workers=3` busy under load). A
		// future GUC can expose this; for now it's wired off
		// the engine attachment.
		pool.SetAsyncFlushBatchSize(8)
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
	// One-shot migration: if this is a legacy JSON-only cluster whose
	// pg_class has no user-table rows, write all in-memory user tables to
	// the pg_class/pg_attribute heap relfiles so future startups use the
	// heap path (M0030-0004). Runs after loadCatalogSnapshot so the
	// catalog is already populated from JSON; runs before
	// loadSystemCatalogsIfPresent so system-catalog registration follows.
	if err := maybeMigrateCatalogToHeap(mgr, cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: catalog migration: %w", err)
	}
	// Register system catalog heap tables (pg_type, pg_attribute) if their
	// relfiles exist.  Must run after loadCatalogSnapshot / Restore so the
	// registration survives the catalog reset that Restore() performs.
	// Safe to skip on old clusters without the M0030-0001 relfiles.
	if err := loadSystemCatalogsIfPresent(abs, cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: system catalog load: %w", err)
	}
	// Supplement the catalog with user tables found in the pg_class/pg_attribute
	// heap files (M0030-0003). If a table was created after the last JSON
	// snapshot (e.g. crash before SaveCatalog), this path recovers it from the
	// WAL-replayed heap pages. Idempotent: tables already loaded from JSON are
	// skipped. Safe on old clusters — skips if pg_class relfile is absent.
	if err := loadUserTablesFromHeap(mgr, cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: user table heap load: %w", err)
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
	if err := registerStatReplicationView(cat, walSenders, walWriter); err != nil {
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
	// pg_stat_aio: one row per AIO engine (v0 has at most one).
	// Backed by aio.Engine.Stats(). Emits zero rows when no
	// engine is attached so a SELECT against the view doesn't
	// surface as a missing-table error. See
	// docs/design/0009-0004-aio-observability.md.
	if err := registerStatAIOView(cat, aioEngine); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}
	// pg_aios: one row per currently-outstanding AIO operation.
	// Backed by aio.Engine.InFlight() (per-handle tracking).
	// Zero rows when no engine is attached or no Ops are
	// in flight.
	if err := registerPgAiosView(cat, aioEngine); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}
	// pg_stat_aio_targets: one row per Op.Target seen
	// (relfile path or WAL segment path) with that target's
	// accumulated I/O counters and latency. Backed by
	// aio.Engine.PerTarget(). Zero rows when no engine is
	// attached or no targets have accumulated.
	if err := registerPgStatAIOTargetsView(cat, aioEngine); err != nil {
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
	// pg_stat_wal_io: M0010-0003 observability surface. One row
	// when a WAL writer is attached; surfaces direct-I/O write
	// counters and walsender in-memory ring metrics. See
	// docs/design/0010-0003-wal-direct-io-observability-and-operations.md.
	if err := registerStatWALIOView(cat, walWriter); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}
	// pg_proc: M0015 Stage A step 2 — user-defined routine
	// introspection. Empty until CREATE FUNCTION execution lands
	// in a later slice; registering here makes the view present
	// from the first session so `\df` doesn't surface a
	// missing-table error in the meantime.
	if err := registerPgProcView(cat); err != nil {
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

	// pg_stat_activity: M0022 Stage A. Backend-activity registry
	// tracking connection lifecycle, current query, and state.
	// One row per active backend. Backed by *activity.Registry.
	if err := registerPgStatActivityView(cat, act); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}

	// Wire AIO wait-event hooks so pg_stat_activity can report
	// "AIO" wait events when backends block on I/O completion.
	// The hooks use goroutine-ID lookup to find the correct backend
	// (see activity.RegisterCurrentGoroutine).
	if aioEngine != nil {
		aioEngine.OnWaitStart = func() {
			if reg, pid := activity.LookupGoroutine(); reg != nil {
				reg.WaitEventStart(pid, activity.WaitTypeIO, activity.WaitAIO)
			}
		}
		aioEngine.OnWaitEnd = func() {
			if reg, pid := activity.LookupGoroutine(); reg != nil {
				reg.WaitEventEnd(pid)
			}
		}
	}

	// Wire data-file I/O wait-event hooks so pg_stat_activity records
	// DataFileRead / DataFileWrite / DataFileExtend / DataFileSync
	// when backends block on storage operations.
	mgr.OnReadWait = func() {
		if reg, pid := activity.LookupGoroutine(); reg != nil {
			reg.WaitEventStart(pid, activity.WaitTypeIO, activity.WaitDataFileRead)
		}
	}
	mgr.OnWriteWait = func() {
		if reg, pid := activity.LookupGoroutine(); reg != nil {
			reg.WaitEventStart(pid, activity.WaitTypeIO, activity.WaitDataFileWrite)
		}
	}
	mgr.OnExtendWait = func() {
		if reg, pid := activity.LookupGoroutine(); reg != nil {
			reg.WaitEventStart(pid, activity.WaitTypeIO, activity.WaitDataFileExtend)
		}
	}
	mgr.OnSyncWait = func() {
		if reg, pid := activity.LookupGoroutine(); reg != nil {
			reg.WaitEventStart(pid, activity.WaitTypeIO, activity.WaitDataFileSync)
		}
	}

	// Wire WAL I/O wait-event hooks.
	if walWriter != nil {
		walWriter.OnWALSync = func() {
			if reg, pid := activity.LookupGoroutine(); reg != nil {
				reg.WaitEventStart(pid, activity.WaitTypeIO, activity.WaitWALSync)
			}
		}
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
		Activity:       act,
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
// adapting Wait through aioHandleAdapter. Direction is
// forwarded so writes (Manager.WriteBlockAIO) flow through
// the same submission path as reads (Manager.PrefetchBlock).
func (a aioEngineAdapter) Submit(op storage.AIOSubmitOp) storage.AIOHandle {
	dir := aio.DirRead
	if op.Direction == storage.AIODirWrite {
		dir = aio.DirWrite
	}
	return aioHandleAdapter{
		h: a.eng.Submit(aio.Op{
			File:      aioFileAdapter{f: op.File},
			Buffer:    op.Buffer,
			Offset:    op.Offset,
			Direction: dir,
			Target:    op.Target,
		}),
	}
}

// aioFileAdapter exposes a storage.AIOFile to the aio engine,
// which expects a full aio.File. Both ReadAt (for PrefetchBlock)
// and WriteAt (for WriteBlockAIO) flow through.
type aioFileAdapter struct {
	f storage.AIOFile
}

func (a aioFileAdapter) ReadAt(p []byte, off int64) (int, error) {
	return a.f.ReadAt(p, off)
}

func (a aioFileAdapter) WriteAt(p []byte, off int64) (int, error) {
	return a.f.WriteAt(p, off)
}

// Fd forwards the underlying file's descriptor when the wrapped
// storage.AIOFile exposes one. The io_uring method type-asserts
// for this interface; without it the io_uring path falls back
// to inline pread/pwrite (correct, but no kernel acceleration).
// Returns ^uintptr(0) when the underlying file doesn't surface
// a real fd (e.g. an in-memory test file).
func (a aioFileAdapter) Fd() uintptr {
	if fr, ok := a.f.(interface{ Fd() uintptr }); ok {
		return fr.Fd()
	}
	return ^uintptr(0)
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

// walAIOEngineAdapter bridges *aio.Engine to wal.AIOEngine.
// Same shape as aioEngineAdapter (storage-side) — kept as a
// separate type so the `internal/wal` package can stay free of
// the `internal/aio` import. Both adapters delegate to the same
// underlying *aio.Engine so reads, heap writes, and WAL writes
// flow through one pool.
type walAIOEngineAdapter struct {
	eng *aio.Engine
}

func (a walAIOEngineAdapter) Submit(op wal.AIOSubmitOp) wal.AIOHandle {
	dir := aio.DirRead
	if op.Direction == wal.AIODirWrite {
		dir = aio.DirWrite
	}
	return walAIOHandleAdapter{
		h: a.eng.Submit(aio.Op{
			File:      walAIOFileAdapter{f: op.File},
			Buffer:    op.Buffer,
			Offset:    op.Offset,
			Direction: dir,
			Target:    op.Target,
		}),
	}
}

type walAIOFileAdapter struct {
	f wal.AIOFile
}

func (a walAIOFileAdapter) ReadAt(p []byte, off int64) (int, error) {
	return a.f.ReadAt(p, off)
}
func (a walAIOFileAdapter) WriteAt(p []byte, off int64) (int, error) {
	return a.f.WriteAt(p, off)
}

// Fd forwards the underlying WAL segment's descriptor — same
// shape as aioFileAdapter.Fd(). io_uring's submit path uses
// this so WAL writes flow through io_uring on Linux instead
// of falling back to inline pwrite.
func (a walAIOFileAdapter) Fd() uintptr {
	if fr, ok := a.f.(interface{ Fd() uintptr }); ok {
		return fr.Fd()
	}
	return ^uintptr(0)
}

type walAIOHandleAdapter struct {
	h *aio.Handle
}

func (a walAIOHandleAdapter) Wait() (int, error) {
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

// loadSystemCatalogsIfPresent registers pg_type and pg_attribute as
// real heap-backed catalog tables when their M0030-0001 relfiles are
// present under <dataDir>/base/<DefaultDBOid>/.
//
// On fresh clusters (after goopg init with Phase 1+2 changes), the
// relfiles exist and contain seeded rows.  On old clusters that were
// initialized before M0030-0001, the files are absent and this
// function is a no-op — backward compatible.
//
// The registration uses catalog.RegisterRealTable (non-virtual,
// OID-pre-set), so a SeqScan on these tables reads directly from the
// heap relfile.  The rows are visible to all sessions because they
// were written with xmin=BootstrapTransactionID (1).
func loadSystemCatalogsIfPresent(dataDir string, cat *catalog.InMemory) error {
	base := filepath.Join(dataDir, "base", fmt.Sprint(catalog.DefaultDBOid))

	// pg_type (OID 1247) — built-in type catalog.
	pgTypeFile := filepath.Join(base, fmt.Sprint(catalog.TypeRelationId))
	if _, err := os.Stat(pgTypeFile); err == nil {
		t := &catalog.Table{
			Schema:  "pg_catalog",
			Name:    "pg_type",
			Columns: catalog.PGTypeColumns(),
			OID:     catalog.TypeRelationId,
		}
		if err := cat.RegisterRealTable(t); err != nil {
			return fmt.Errorf("register pg_type: %w", err)
		}
	}

	// pg_attribute (OID 1249) — column definition catalog.
	pgAttrFile := filepath.Join(base, fmt.Sprint(catalog.AttributeRelationId))
	if _, err := os.Stat(pgAttrFile); err == nil {
		t := &catalog.Table{
			Schema:  "pg_catalog",
			Name:    "pg_attribute",
			Columns: catalog.PGAttributeColumns(),
			OID:     catalog.AttributeRelationId,
		}
		if err := cat.RegisterRealTable(t); err != nil {
			return fmt.Errorf("register pg_attribute: %w", err)
		}
	}

	return nil
}

// maybeMigrateCatalogToHeap implements the M0030-0004 one-shot migration
// gate. It detects legacy JSON-only clusters (pg_class relfile present but
// no user-table rows) and, when the in-memory catalog (loaded from JSON) has
// user tables, writes them to pg_class/pg_attribute so future startups use
// the heap path.
//
// Safe conditions:
//   - No pg_class relfile: old cluster without M0030-0001 files → no-op.
//   - pg_class has user rows (OID ≥ FirstUserOID): already migrated → no-op.
//   - In-memory catalog has no user tables: nothing to migrate → no-op.
//
// Idempotent: if interrupted mid-way, the next startup sees some user rows
// in pg_class and skips migration; the remaining tables are loaded from JSON
// via TryRegisterUserTable's idempotency.
func maybeMigrateCatalogToHeap(mgr *storage.Manager, cat *catalog.InMemory) error {
	classRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	nClassBlocks, err := mgr.NBlocks(classRel)
	if err != nil || nClassBlocks == 0 {
		return nil // no pg_class relfile — pre-M0030-0001 cluster
	}

	// Scan pg_class for any user-table row (fast early-exit).
	page := make(storage.Page, storage.BlockSize)
	for blk := storage.BlockNumber(0); blk < nClassBlocks; blk++ {
		if err := mgr.ReadBlock(classRel, blk, page); err != nil {
			return fmt.Errorf("maybeMigrate: scan pg_class blk %d: %w", blk, err)
		}
		count, _ := storage.PageLinePointerCount(page)
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if ht.Header.Xmin == storage.InvalidTransactionID {
				continue
			}
			if ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			row, err := catalog.DecodePGClassRow(ht.Data)
			if err != nil {
				continue
			}
			if row.OID >= catalog.FirstUserOID {
				return nil // already has user rows — migration done
			}
		}
	}

	// pg_class has no user rows. Get user tables from the in-memory catalog.
	userTables := cat.AllTables()
	if len(userTables) == 0 {
		return nil // nothing to migrate
	}

	// Build pg_class and pg_attribute tuples for all user tables.
	xid := storage.TransactionID(bootstrapXID) // migration rows use bootstrap XID
	var classTuples []storage.HeapTuple
	var attrTuples []storage.HeapTuple

	for _, tbl := range userTables {
		ns := catalog.PublicNamespaceOID
		if tbl.Schema == "pg_catalog" {
			ns = catalog.PGCatalogNamespaceOID
		}
		classData := catalog.EncodePGClassRow(catalog.PGClassRow{
			OID:            tbl.OID,
			RelName:        tbl.Name,
			RelNamespace:   uint32(ns),
			RelKind:        "r",
			RelNAtts:       int32(len(tbl.Columns)),
			RelFileNode:    tbl.OID,
			RelPersistence: "p",
		})
		classTuples = append(classTuples, storage.NewHeapTuple(xid, storage.InvalidTransactionID, classData))

		for _, col := range tbl.Columns {
			attrData := catalog.EncodePGAttributeRow(catalog.PGAttributeRow{
				AttRelID:  tbl.OID,
				AttName:   col.Name,
				AttTypID:  catalog.TypeNameToOID(col.Type.Name),
				AttNum:    int32(col.Ordinal + 1),
				AttNotNull: col.NotNull,
			})
			attrTuples = append(attrTuples, storage.NewHeapTuple(xid, storage.InvalidTransactionID, attrData))
		}
	}

	// Write pg_class rows.
	if err := appendCatalogRows(mgr, classRel, classTuples); err != nil {
		return fmt.Errorf("maybeMigrate: write pg_class: %w", err)
	}

	// Write pg_attribute rows.
	attrRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	if nAttrBlocks, _ := mgr.NBlocks(attrRel); nAttrBlocks > 0 {
		if err := appendCatalogRows(mgr, attrRel, attrTuples); err != nil {
			return fmt.Errorf("maybeMigrate: write pg_attribute: %w", err)
		}
	}
	return nil
}

// appendCatalogRows appends HeapTuples to the last page of a relfile,
// extending to new pages when the current page is full. Used by the
// catalog migration (M0030-0004) to backfill user-table rows into
// pg_class and pg_attribute without disturbing the existing bootstrap rows.
func appendCatalogRows(mgr *storage.Manager, rel storage.RelFileNode, tuples []storage.HeapTuple) error {
	if len(tuples) == 0 {
		return nil
	}
	nBlocks, err := mgr.NBlocks(rel)
	if err != nil {
		return fmt.Errorf("NBlocks: %w", err)
	}
	if nBlocks == 0 {
		return fmt.Errorf("appendCatalogRows: relfile OID %d has 0 blocks", rel.RelOid)
	}

	page := make(storage.Page, storage.BlockSize)
	curBlk := nBlocks - 1
	if err := mgr.ReadBlock(rel, curBlk, page); err != nil {
		return fmt.Errorf("ReadBlock %d: %w", curBlk, err)
	}

	for _, tup := range tuples {
		if _, addErr := storage.PageAddHeapTuple(page, tup); addErr == nil {
			continue
		}
		// Page full — flush current, extend new page.
		if err := mgr.WriteBlock(rel, curBlk, page); err != nil {
			return fmt.Errorf("WriteBlock %d: %w", curBlk, err)
		}
		for i := range page {
			page[i] = 0
		}
		if err := storage.InitPage(page); err != nil {
			return err
		}
		curBlk, err = mgr.Extend(rel, page)
		if err != nil {
			return fmt.Errorf("Extend: %w", err)
		}
		if _, addErr := storage.PageAddHeapTuple(page, tup); addErr != nil {
			return fmt.Errorf("PageAddHeapTuple on fresh page: %w", addErr)
		}
	}
	return mgr.WriteBlock(rel, curBlk, page)
}

// loadUserTablesFromHeap supplements the in-memory catalog with user tables
// found in the pg_class and pg_attribute heap relfiles (M0030-0003). It scans
// all live (xmin≠0, xmax=0) pg_class rows with relkind='r' and OID ≥ FirstUserOID,
// then collects their column definitions from pg_attribute rows, and calls
// TryRegisterUserTable for each.
//
// This is the primary crash-recovery path for user tables: if the server crashes
// after DDL-sync writes to the heap but before SaveCatalog() writes the JSON,
// this scan recovers the new tables from the WAL-replayed heap pages on the
// next startup.
//
// The scan is safe on old clusters (pre-M0030-0001) that have no pg_class relfile
// and is idempotent with the JSON snapshot path (tables already loaded from JSON
// are skipped via TryRegisterUserTable's exists-check).
func loadUserTablesFromHeap(mgr *storage.Manager, cat *catalog.InMemory) error {
	classRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	nClassBlocks, err := mgr.NBlocks(classRel)
	if err != nil || nClassBlocks == 0 {
		return nil // pg_class absent or empty — old cluster or fresh initdb
	}

	page := make(storage.Page, storage.BlockSize)

	// Pass 1: collect user table rows from pg_class.
	var userTableRows []catalog.PGClassRow
	for blk := storage.BlockNumber(0); blk < nClassBlocks; blk++ {
		if err := mgr.ReadBlock(classRel, blk, page); err != nil {
			return fmt.Errorf("loadUserTablesFromHeap: read pg_class blk %d: %w", blk, err)
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if ht.Header.Xmin == storage.InvalidTransactionID {
				continue // not a real tuple
			}
			if ht.Header.Xmax != storage.InvalidTransactionID {
				continue // deleted
			}
			row, err := catalog.DecodePGClassRow(ht.Data)
			if err != nil {
				continue
			}
			if row.RelKind == "r" && row.OID >= catalog.FirstUserOID {
				userTableRows = append(userTableRows, row)
			}
		}
	}
	if len(userTableRows) == 0 {
		return nil // no user tables in heap
	}

	// Pass 2: collect pg_attribute rows for user tables.
	attrRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	nAttrBlocks, err := mgr.NBlocks(attrRel)
	if err != nil || nAttrBlocks == 0 {
		return nil // no attributes — can't reconstruct columns
	}

	attrByRelOID := map[uint32][]catalog.PGAttributeRow{}
	for blk := storage.BlockNumber(0); blk < nAttrBlocks; blk++ {
		if err := mgr.ReadBlock(attrRel, blk, page); err != nil {
			return fmt.Errorf("loadUserTablesFromHeap: read pg_attribute blk %d: %w", blk, err)
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if ht.Header.Xmin == storage.InvalidTransactionID {
				continue
			}
			if ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			row, err := catalog.DecodePGAttributeRow(ht.Data)
			if err != nil {
				continue
			}
			if !row.AttIsDropped && row.AttRelID >= catalog.FirstUserOID {
				attrByRelOID[row.AttRelID] = append(attrByRelOID[row.AttRelID], row)
			}
		}
	}

	// Pass 3: register each user table with its heap-recovered column definitions.
	for _, tr := range userTableRows {
		attrRows := attrByRelOID[tr.OID]
		sort.Slice(attrRows, func(i, j int) bool {
			return attrRows[i].AttNum < attrRows[j].AttNum
		})

		cols := make([]catalog.Column, len(attrRows))
		for i, ar := range attrRows {
			cols[i] = catalog.Column{
				Name:    ar.AttName,
				Type:    catalog.Type{Name: catalog.OIDToTypeName(ar.AttTypID)},
				NotNull: ar.AttNotNull,
				Ordinal: i,
			}
		}

		schema := ""
		if tr.RelNamespace == catalog.PGCatalogNamespaceOID {
			schema = "pg_catalog"
		}

		tbl := &catalog.Table{
			Schema:  schema,
			Name:    tr.RelName,
			Columns: cols,
			OID:     tr.OID,
		}
		if err := cat.TryRegisterUserTable(tbl); err != nil {
			return fmt.Errorf("loadUserTablesFromHeap: register %q: %w", tr.RelName, err)
		}
	}
	return nil
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
