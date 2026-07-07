package initdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/aio"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// pgEpoch2000 is PG's TimestampTz origin: 2000-01-01 00:00:00 UTC.
// pgTimestampNowUsec returns the current wall-clock time as a
// TimestampTz (microseconds since pgEpoch2000). Used by the
// PG-canonical XLOG_XACT_COMMIT/ABORT emit path (M0106-0010 batched-46).
var pgEpoch2000 = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

func pgTimestampNowUsec() int64 { return time.Since(pgEpoch2000).Microseconds() }

// Runtime is the bundle of long-lived handles a running goopg
// server needs to drive table-touching statements: a storage
// Manager + Pool, an MVCC manager, and an in-memory catalog. Each
// component is independently usable from tests; Open is the
// production entry point that constructs the four together against
// a real data directory.
type Runtime struct {
	StorageMgr *storage.Manager
	Pool       *storage.Pool
	TxnMgr     *mvcc.Manager
	Catalog    catalog.Catalog
	// FSM is the in-memory free-space map (M0046-0003). VACUUM updates
	// it; INSERT consults it before extending the relation.
	FSM *storage.FSM
	// VM is the in-memory visibility map (M0046-0004). VACUUM sets the
	// ALL_VISIBLE bit; index-only scans check it to skip heap fetches.
	VM  *storage.VisibilityMap
	WAL *wal.Writer
	// LogCanonical is the callback for emitting PG-canonical WAL records
	// (XLOG_HEAP_INSERT, XLOG_BTREE_INSERT_LEAF) from DDL paths so a PG18
	// standby can replay catalog mutations. Non-nil only when PageHeaders=true.
	// M0106-0010 batched-32.
	LogCanonical catalog.LogCanonicalFunc
	Checkpointer *wal.Checkpointer
	Slots        *wal.Slots
	// SyncRep is the synchronous-replication wait primitive
	// (M0102-0005). The commit-path xactMarkerLogger uses it to
	// block COMMIT until configured standbys ack the commit LSN; the
	// walsender feedback handler calls UpdateStandbyProgress. nil
	// here would disable sync replication entirely, but Open
	// constructs one unconditionally — it is a no-op when
	// `synchronous_standby_names` is empty (upstream's async default).
	SyncRep        *wal.SyncRep
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

	// walwriterStop, when non-nil, is closed by Close() to signal the
	// background WAL writer loop to exit. nil when the loop wasn't
	// started (WalWriterDelay == 0 in tests).
	walwriterStop chan struct{}

	// bgwriter is the background page-writer goroutine (M0048-0003).
	// nil when BgwriterDelay == 0 or BgwriterMaxPages == 0.
	bgwriter *storage.Bgwriter

	// immediateShutdown, when true, makes Close() skip the final
	// shutdown checkpoint so pg_control's State stays DB_IN_PRODUCTION
	// (an unclean cluster). Set via SetImmediateShutdown from the
	// control-plane STOPIMMEDIATE handler (`goopg stop -mode
	// immediate`), mirroring upstream's immediate (SIGQUIT) shutdown.
	// (M0110-0004 / RW-002 b.)
	immediateShutdown bool
}

// SetImmediateShutdown marks the runtime so the next Close() skips its
// final shutdown checkpoint, leaving pg_control's State at
// DB_IN_PRODUCTION. Used by the control-plane STOPIMMEDIATE command to
// reproduce upstream's immediate (SIGQUIT) shutdown semantics: the
// cluster is left looking unclean and is recovered via WAL replay on the
// next start. (M0110-0004 / RW-002 b.)
func (r *Runtime) SetImmediateShutdown() {
	if r != nil {
		r.immediateShutdown = true
	}
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

	// WalWriterDelay controls the period of the background WAL writer
	// loop (M0042-0003). The loop calls FlushUpTo(maxUint64) every
	// WalWriterDelay to ensure buffered WAL bytes reach disk even when
	// no commits or checkpoints are in flight. 0 disables the loop
	// (used by tests that don't need background flushing). Default in
	// production: 200ms (mirrors upstream's wal_writer_delay GUC).
	WalWriterDelay time.Duration

	// BgwriterDelay controls the period of the background page-writer
	// goroutine (M0048-0003). The bgwriter proactively flushes dirty
	// buffer-pool pages to reduce synchronous I/O on eviction.
	// 0 disables the bgwriter. Default 200ms mirrors upstream's
	// bgwriter_delay GUC.
	BgwriterDelay time.Duration

	// BgwriterMaxPages caps dirty pages written per bgwriter tick.
	// 0 disables the bgwriter. Default 100 mirrors upstream's
	// bgwriter_lru_maxpages GUC.
	BgwriterMaxPages int

	// CheckpointFlushAfter / BgwriterFlushAfter / BackendFlushAfter set
	// each context's pg_stat_io writeback threshold, in BLCKSZ pages (0
	// disables writeback for that context — see storage/writeback.go).
	// Mirror upstream's checkpoint_flush_after / bgwriter_flush_after /
	// backend_flush_after GUCs; like BgwriterMaxPages above, a caller
	// that leaves these at the Go zero value gets writeback disabled
	// (cmd/goopg always passes the live GUC value, whose own registered
	// default is checkpoint=32/bgwriter=64/backend=0).
	CheckpointFlushAfter int
	BgwriterFlushAfter   int
	BackendFlushAfter    int

	// TrackIOTiming gates the per-I/O activity.LookupGoroutine
	// wait-event hooks (BufferPin / DataFileRead / Write / Extend
	// / Sync / AIO). Default false (matches upstream PG's
	// `track_io_timing = off` default). When false, the hooks are
	// installed as nil so the storage / pool / AIO layers skip the
	// `runtime.Stack`-based LookupGoroutine call entirely — a
	// material saving on hot read paths (M0092-0005).
	//
	// pg_stat_activity wait events will not surface I/O blocking
	// reasons while off; diagnostic sessions can flip the GUC via
	// postgresql.conf to recover them. Runtime SET is not yet
	// supported (M0093 candidate).
	TrackIOTiming bool

	// TransactionBuffers is the resident-page budget for the CLOG SLRU
	// buffer pool (the live in-memory commit-log store since M0117-0006
	// Part B). It maps to the `transaction_buffers` GUC; 0 means
	// "auto-tune" (EffectiveCLOGBuffers floors it at one SLRU bank = 16
	// pages), which is the boot default and is correctness-safe. A
	// non-zero override is clamped to [16, 1GiB/BLCKSZ]. cmd/goopg start
	// derives this from the GUC so postgresql.conf overrides flow through;
	// tests leave it 0. Wired into CLog.SetCLOGBuffers before
	// EnablePGSLRUMirror creates the pool (a no-op afterwards).
	TransactionBuffers int
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

	// Read pg_control's data_checksum_version up front so the storage
	// Manager knows whether to checksum every block it writes and verify
	// every block it reads. Version 1 ⇒ enabled (initdb --data-checksums);
	// 0 (the goopg default) ⇒ a checksum-less cluster, the byte-identical
	// fast path. A missing/unreadable control file leaves checksums off.
	checksumsEnabled := false
	if pgCtrl, pce := control.ReadControlFile(abs); pce == nil && pgCtrl != nil {
		checksumsEnabled = pgCtrl.DataChecksumVersion != 0
	}

	mgr := storage.NewManager(storage.ManagerConfig{
		DataDir:          abs,
		ChecksumsEnabled: checksumsEnabled,
	})

	// Activity registry (M0022 / M0107-0005): per-backend slot array with
	// atomic WaitEventStart/WaitEventEnd.  Background workers (WAL writer,
	// etc.) are assigned slots in the background-worker range above the
	// regular backend range.
	act := activity.NewActivityRegistry(mvcc.DefaultProcArraySize)
	// Pre-register the WAL writer background slot so the OnWALWrite closure
	// can call WaitEventStart(walProcNum, ...) without a goroutine map lookup.
	walProcNum := act.RegisterBackground(activity.WalWriterIdx, &activity.Backend{
		PID:         "wal-writer-0",
		BackendType: "walwriter",
		State:       "active",
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

	// Load (or generate) the cluster system identifier for WAL page headers.
	// M0101-0001: enables PG-compatible WAL format so pg_waldump can parse segments.
	systemID, err := LoadOrCreateSystemID(abs)
	if err != nil {
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: system_identifier: %w", err)
	}

	// M0102-0003: load (or default-create) the persistent timeline ID.
	// The TLI is stamped into every WAL page header (xlp_tli) so a
	// heterogeneous standby reattaching after a goopg promote can
	// resolve which timeline its replayed bytes belong to.
	tli, err := LoadOrCreateTimelineID(abs)
	if err != nil {
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: timeline_id: %w", err)
	}

	// M0106-0010 batched-34: post-recovery primary TLI bump.
	// If WAL segments carry a higher TLI than the persisted timeline_id
	// (e.g. crash after receiving streaming WAL on a new TLI but before
	// timeline_id was updated), write the missing history file and advance
	// timeline_id. Skip in standby mode — finalizePromotion handles the bump.
	if abs != "" {
		isStby, _ := IsStandby(abs)
		if !isStby {
			walDir := filepath.Join(abs, "pg_wal")
			if newTLI, wrote, tliErr := wal.WriteHistoryAfterRecovery(walDir, tli, 0); tliErr != nil {
				_ = mgr.Close()
				return nil, fmt.Errorf("goopg: post-recovery TLI check: %w", tliErr)
			} else if wrote {
				if err := WriteTimelineID(abs, newTLI); err != nil {
					_ = mgr.Close()
					return nil, fmt.Errorf("goopg: update timeline_id after TLI recovery: %w", err)
				}
				tli = newTLI
			}
		}
	}

	walCfg := wal.Config{
		WALDir:             filepath.Join(abs, "pg_wal"),
		SegmentSize:        opts.WALSegmentSize, // 0 → wal.DefaultSegmentSize
		Preallocate:        opts.WALInitZero,
		SenderMemoryBuffer: opts.WALSenderMemoryBuffer,
		WALBuffers:         opts.WALBuffers,
		// M0101-0001: emit PG-compatible XLOG page headers so pg_waldump
		// can parse the WAL segments. SystemID is embedded in every page
		// header for cross-segment consistency checking.
		PageHeaders: true,
		SystemID:    systemID,
		TimelineID:  tli,
		OnLoopStart: func() {
			// Register this goroutine so pool/AIO hooks can find procNum
			// via LookupCurrentGoroutine (M0107-0005).
			activity.SetCurrentGoroutine(act, walProcNum)
		},
		OnLoopEnd: func() {
			activity.ClearCurrentGoroutine()
		},
		// M0107-0005: closure-captures walProcNum (int32) for the atomic
		// hot path; no goroutine map lookup and no mutex.
		OnWALWrite: func() {
			if act != nil {
				act.WaitEventStart(walProcNum, activity.WaitTypeIO, activity.WaitWALWrite)
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
	logBtreeSplit := func(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page, sibBlk storage.BlockNumber, sibPage storage.Page) (storage.LSN, error) {
		payload, err := wal.EncodeBtreeSplit(rel, leftBlk, rightBlk, leftPage, rightPage, sibBlk, sibPage)
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
	// oldTuple carries the pre-delete heap-tuple bytes for logical
	// replication; nil when the caller doesn't need logical decoding.
	logHeapDelete := func(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, oldTuple []byte) (storage.LSN, error) {
		payload := wal.EncodeHeapDelete(rel, blk, lineSlot, xmax, oldTuple)
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

	// Logical btree-vacuum (kept-items + opaque flags) change
	// record (M0079-0002). Replaces the FPI path in
	// `btree.VacuumIndexPages` so per-page vacuum cost is
	// proportional to surviving items rather than 8 KiB of
	// page bytes.
	logBtreeVacuum := func(rel storage.RelFileNode, blk storage.BlockNumber, keptItems [][]byte, opaqueFlags uint16) (storage.LSN, error) {
		payload := wal.EncodeBtreeVacuum(rel, blk, keptItems, opaqueFlags)
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}

	// M0079-0003 logical records covering the remaining FPI
	// fallback paths in btree page deletion + root replacement.
	logBtreeUnlinkPage := func(rel storage.RelFileNode, req storage.BtreeUnlinkPageRequest) (storage.LSN, error) {
		payload := wal.EncodeBtreeUnlinkPage(wal.BtreeUnlinkPagePayload{
			Rel:              rel,
			LeafBlk:          req.LeafBlk,
			LeafFlagsAfter:   req.LeafFlagsAfter,
			HasLeftSib:       req.HasLeftSib,
			LeftSibBlk:       req.LeftSibBlk,
			LeftSibNewNext:   req.LeftSibNewNext,
			HasRightSib:      req.HasRightSib,
			RightSibBlk:      req.RightSibBlk,
			RightSibNewPrev:  req.RightSibNewPrev,
			HasParent:        req.HasParent,
			ParentBlk:        req.ParentBlk,
			ParentRemoveSlot: req.ParentRemoveSlot,
		})
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}
	logBtreeNewRoot := func(rel storage.RelFileNode, rootBlk storage.BlockNumber, level uint32, items [][]byte) (storage.LSN, error) {
		payload := wal.EncodeBtreeNewRoot(wal.BtreeNewRootPayload{
			Rel:     rel,
			RootBlk: rootBlk,
			Level:   level,
			Items:   items,
		})
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}
	logBtreeMarkPageHalfDead := func(rel storage.RelFileNode, leafBlk storage.BlockNumber, flagsAfter uint16) (storage.LSN, error) {
		payload := wal.EncodeBtreeMarkPageHalfDead(wal.BtreeMarkHalfDeadPayload{
			Rel:        rel,
			LeafBlk:    leafBlk,
			FlagsAfter: flagsAfter,
		})
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}

	// M0080-0001: heap-freeze logical record.
	logHeapFreeze := func(rel storage.RelFileNode, blk storage.BlockNumber, frozenSlots []uint16) (storage.LSN, error) {
		payload := wal.EncodeHeapFreeze(rel, blk, frozenSlots)
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

	// Opportunistic page-pruning change record (M0046-0002). Carries
	// the freed slot list so replay can deterministically reclaim the
	// same dead slots without re-running the isDead predicate.
	logHeapPruneOpt := func(rel storage.RelFileNode, blk storage.BlockNumber, redirects [][2]uint16, unused []uint16) (storage.LSN, error) {
		payload := wal.EncodeHeapPruneOpt(rel, blk, redirects, unused)
		_, end, err := walWriter.Append(payload)
		if err != nil {
			return 0, err
		}
		return storage.LSN(end), nil
	}

	// Atomic HOT-update change record (M0046-0001). Encodes the
	// old-slot xmax stamp + new tuple bytes on the same page in one
	// record so replay can reconstruct the HOT chain atomically.
	logHeapHotUpdate := func(rel storage.RelFileNode, blk storage.BlockNumber, oldSlot uint16, xmax storage.TransactionID, tupleBytes []byte) (storage.LSN, error) {
		payload := wal.EncodeHeapHotUpdate(rel, blk, oldSlot, xmax, tupleBytes)
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

	// Generic catalog-DDL WAL append (M0079-0001). The executor's
	// CREATE / DROP INDEX paths use this to emit pre-encoded
	// `RecordKindCreateIndex` / `RecordKindDropIndex` records
	// without taking a direct dependency on the wal package. The
	// returned LSN matches the record's end position; callers
	// don't currently use it but the signature mirrors the other
	// Log* hooks for consistency.
	logChangeRecord := func(payload []byte) (storage.LSN, error) {
		_, end, err := walWriter.Append(payload)
		return storage.LSN(end), err
	}

	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:                    slots,
		WAL:                      walWriter,
		LogPageImage:             logFPI,
		LogBtreeSplit:            logBtreeSplit,
		LogHeapInsert:            logHeapInsert,
		LogBtreeInsert:           logBtreeInsert,
		LogHeapDelete:            logHeapDelete,
		LogHeapVacuum:            logHeapVacuum,
		LogBtreeVacuum:           logBtreeVacuum,
		LogBtreeUnlinkPage:       logBtreeUnlinkPage,
		LogBtreeNewRoot:          logBtreeNewRoot,
		LogBtreeMarkPageHalfDead: logBtreeMarkPageHalfDead,
		LogHeapFreeze:            logHeapFreeze,
		LogHeapLock:              logHeapLock,
		LogHeapHotUpdate:         logHeapHotUpdate,
		LogHeapPruneOpt:          logHeapPruneOpt,
		LogSmgrCreate:            logSmgrCreate,
		LogChangeRecord:          logChangeRecord,
		FullPageWrites:           true,
	})
	if err != nil {
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: bufpool: %w", err)
	}
	mgr.OnBlockWritten = func(rel storage.RelFileNode, blk storage.BlockNumber) {
		pool.InvalidateBlock(storage.BufferTag{Rel: rel, Block: blk})
	}
	// M0092-0005: BufferPin wait-event hook. Wired unconditionally (not
	// gated on the boot-time TrackIOTiming value) so a runtime `SET
	// track_io_timing` takes effect without a server restart; the hook
	// body's LookupTrackedGoroutine call is itself gated on act's
	// fast-path flag, keeping the default-off cost to a single atomic
	// load rather than the goroutine-map lookup (M0122-0003 follow-up).
	// M0107-0005: use LookupCurrentGoroutine (procNum) instead of
	// LookupGoroutine (Registry+pid) so WaitEventStart is atomic.
	pool.OnPinWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeBufferPin, activity.WaitBufferPin)
		}
	}
	pool.OnPinDone = func() {
		// M0122-0003: WaitEventEnd's returned duration is real wall-clock
		// time only when track_io_timing gated this pair on (the
		// LookupTrackedGoroutine ok-branch), so accumulating it here
		// unconditionally within this branch matches upstream's "zero
		// unless track_io_timing is enabled" pg_stat_io.read_time semantics.
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			d := reg.WaitEventEnd(procNum)
			pool.AddReadTimeNanos(int64(d))
		}
	}
	// write_time's OnPinWait/OnPinDone analogue: brackets evictVictim's
	// dirty-victim flushSlot call (M0122-0003 pg_stat_io follow-up).
	pool.OnFlushWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileWrite)
		}
	}
	pool.OnFlushDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			d := reg.WaitEventEnd(procNum)
			pool.AddWriteTimeNanos(int64(d))
		}
	}
	// extend_time's OnFlushWait/OnFlushDone analogue: brackets PinNew's
	// mgr.Extend call (M0122-0003 pg_stat_io follow-up).
	pool.OnExtendWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileExtend)
		}
	}
	pool.OnExtendDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			d := reg.WaitEventEnd(procNum)
			pool.AddExtendTimeNanos(int64(d))
		}
	}
	// writeback's OnFlushWait/OnFlushDone analogues, one per context
	// (M0122-0003 pg_stat_io follow-up: writeback/writeback_time). All
	// three contexts now share the identical act.LookupTrackedGoroutine()
	// → WaitEventStart/WaitEventEnd pattern: bgwriter and checkpointer are
	// registered background slots (BgwriterIdx/CheckpointerIdx below;
	// checkpointerProcNum above) with their TrackIOTiming bit seeded once
	// from the boot-time GUC value at registration (background workers
	// have no per-session SET semantics to react to, unlike a client
	// backend), so the gate reduces to the same boot-time value the
	// previous plain time.Now()/time.Since pair used — but now backed by
	// the real activity-registry wait-event clock, which also makes their
	// writeback wait visible in pg_stat_activity (writeback simplification
	// 3, resolved: see deferral ledger).
	pool.OnBackendWritebackWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileWrite)
		}
	}
	pool.OnBackendWritebackDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			d := reg.WaitEventEnd(procNum)
			pool.AddBackendWritebackTimeNanos(int64(d))
		}
	}
	pool.OnBgwriterWritebackWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileWrite)
		}
	}
	pool.OnBgwriterWritebackDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			d := reg.WaitEventEnd(procNum)
			pool.AddBgwriterWritebackTimeNanos(int64(d))
		}
	}
	pool.OnCheckpointerWritebackWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileWrite)
		}
	}
	pool.OnCheckpointerWritebackDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			d := reg.WaitEventEnd(procNum)
			pool.AddCheckpointWritebackTimeNanos(int64(d))
		}
	}
	// write_time's OnCheckpointerWritebackWait/Done analogue: brackets
	// flushBatch's real dirty-page AIO write (M0122-0003 writeback
	// simplification (4), closed: checkpointer/bgwriter writes/write_bytes/
	// write_time cells were an honest 0; now real counters, same
	// LookupTrackedGoroutine pattern as every other On* pair above).
	pool.OnCheckpointerWriteWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileWrite)
		}
	}
	pool.OnCheckpointerWriteDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			d := reg.WaitEventEnd(procNum)
			pool.AddCheckpointWriteTimeNanos(int64(d))
		}
	}
	pool.OnBgwriterWriteWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileWrite)
		}
	}
	pool.OnBgwriterWriteDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			d := reg.WaitEventEnd(procNum)
			pool.AddBgwriterWriteTimeNanos(int64(d))
		}
	}
	pool.SetCheckpointFlushAfter(opts.CheckpointFlushAfter)
	pool.SetBgwriterFlushAfter(opts.BgwriterFlushAfter)
	pool.SetBackendFlushAfter(opts.BackendFlushAfter)
	// backend_flush_after is PGC_USERSET upstream: a connected backend's
	// own `SET backend_flush_after` takes precedence over the process-wide
	// default above (M0122-0003 writeback follow-up).
	pool.BackendFlushAfterOverride = act.BackendFlushAfterOverride
	if opts.TrackIOTiming {
		act.EnableTrackIOTimingFastPath()
	}
	// Wire FlushAll goroutine assertion (M0042-0004): Pool.FlushAll and
	// Pool.FlushAllPaced must only be called from the checkpointer goroutine
	// or from Pool.Close (unregistered goroutine). Client-backend goroutines
	// must never drive full-buffer flushes — they only flush WAL at commit.
	// The check uses the activity registry; if the calling goroutine is
	// registered and its BackendType is neither "checkpointer" nor
	// "autovacuum" nor "" (unregistered / Pool.Close), we panic with a
	// clear message so the invariant violation surfaces immediately in dev.
	pool.OnFlushAll = func() {
		reg, procNum, ok := activity.LookupCurrentGoroutine()
		if !ok {
			return // unregistered goroutine — Pool.Close or tests, OK
		}
		bt := reg.GetBackendType(procNum)
		switch bt {
		case "checkpointer", "autovacuum", "walwriter", "":
			// expected callers
		case "client_backend":
			// A client_backend executing SQL `CHECKPOINT` calls
			// checkpointOp.Open() → Checkpointer.CheckpointNow() →
			// flusher.FlushAll() — this is the legitimate Postgres-style
			// SQL CHECKPOINT pathway. Allow it; the Checkpointer is
			// responsible for running in a way that's safe from a client
			// goroutine (or we'd restructure it to delegate to the real
			// checkpointer goroutine). (M0042-0004 fix, 2026-05-06.)
		default:
			panic("BUG(M0042-0004): Pool.FlushAll called from " + bt +
				" goroutine — only checkpointer should flush all pages;" +
				" client backends must not drive full-buffer I/O directly")
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
	// Open the commit log (pg_xact). Created by bootstrapCLog during initdb;
	// upgraded on old clusters by InitializeAsCommitted.
	clogPath := filepath.Join(abs, "global", "pg_xact")
	clog, err := mvcc.OpenCLog(clogPath)
	if err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: open clog: %w", err)
	}
	// M0117-0006 Part B follow-up: size the live CLOG SLRU buffer pool from
	// the transaction_buffers GUC before EnablePGSLRUMirror creates it. 0 (the
	// boot default) keeps the auto-tuned 16-page floor; a non-zero override is
	// honoured (clamped in EffectiveCLOGBuffers). Must precede the mirror call —
	// SetCLOGBuffers is a no-op once the pool exists.
	clog.SetCLOGBuffers(opts.TransactionBuffers)
	// M0106-0010 batched-44: wire the PG-canonical pg_xact/ SLRU mirror so
	// every commit/abort updates the SLRU segment that the basebackup-shipped
	// standby reads via SimpleLruReadPage_ReadOnly. Since M0117-0006 Part C the
	// pool created here IS the CLOG store (no flat-file backfill round-trip);
	// it lazily faults pages in directly from the on-disk SLRU segments.
	if err := clog.EnablePGSLRUMirror(filepath.Join(abs, "pg_xact")); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: enable pg_xact slru mirror: %w", err)
	}
	// M0117-0007 Part B: wire the CLOG SLRU buffer pool's async-commit write
	// barrier (M0117-0007 Part A, previously never connected to a live WAL
	// writer) to walWriter.FlushUpTo. From here on, any dirty CLOG page
	// write-back (group-commit flush or LRU eviction) flushes the WAL up to
	// that page's highest associated commit-record LSN first — the invariant
	// synchronous_commit=off relies on instead of an inline per-commit fsync.
	clog.SetFlushWALHook(walWriter.FlushUpTo)
	// M0117-0003: wire the persistent pg_subtrans SLRU so subtransaction
	// parentage survives a restart (gap G5 read path). EnablePersistence opens
	// the bootstrapped pg_subtrans/ directory (created by initdb) for write-through
	// of every RegisterSubXid; RestoreFromSLRU reloads the previously-persisted
	// parent links back into memory before any query can run; SetSubxactMap makes
	// the Manager route all subxact registration/resolution through it. Unlike PG
	// (which zeroes pg_subtrans on startup) goopg restores it for durable subxact
	// resolution by an attached standby / 2PC / post-backend-exit readers.
	subxactMap := mvcc.NewSubxactMap()
	if err := subxactMap.EnablePersistence(filepath.Join(abs, "pg_subtrans")); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: enable pg_subtrans slru: %w", err)
	}
	if _, err := subxactMap.RestoreFromSLRU(); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: restore pg_subtrans: %w", err)
	}
	txnMgr.SetSubxactMap(subxactMap)
	txnMgr.SetXactMarkerLogger(func(xid storage.TransactionID, kind mvcc.XactMarker, waitLocalFlush bool) error {
		var payload []byte
		switch kind {
		case mvcc.XactCommit:
			// If the transaction wrote to a nailed catalog relation (pg_class,
			// pg_attribute, pg_proc, or pg_type), emit a commit-with-inval
			// record and unlink both pg_internal.init files so the next backend
			// reloads fresh relcache descriptors. Mirrors PG's commit-path
			// AtEOXact_Inval → RelationCacheInitFilePreInvalidate sequence.
			// M0106-0010 batched-31.
			if txnMgr.TakeRelcacheInvalPending() {
				payload = wal.EncodeXactCommitInval(xid)
				_ = catalog.WithRelCacheInitLock(func() error {
					if err := catalog.RelcacheInitFileUnlink(abs, catalog.DefaultDBOid); err != nil {
						return err
					}
					// M0114: co-invalidate the goopg catalog cache whenever
					// pg_internal.init is unlinked so both files track the same
					// invalidation epoch. The cache is rebuilt on the next startup.
					// Use cat.DBOID() rather than DefaultDBOid — the resolved OID
					// may differ when the postgres DB carries a non-default OID.
					UnlinkCatalogCache(abs, cat.DBOID())
					// Regenerate pg_internal.init immediately after unlinking
					// so the primary always has fresh copies for pg_basebackup.
					// The nailed-rel lists are static (system catalogs only).
					// M0106-0010 batched-35.
					return bootstrapRelcacheInitFiles(abs)
				})
			} else {
				payload = wal.EncodeXactCommit(xid)
			}
		case mvcc.XactAbort:
			payload = wal.EncodeXactAbort(xid)
		default:
			return fmt.Errorf("goopg: unknown xact marker %v", kind)
		}
		_, endLSN, err := walWriter.Append(payload)
		if err != nil {
			return err
		}
		// M0106-0010 batched-46: also emit a PG-canonical XLOG_XACT_COMMIT /
		// XLOG_XACT_ABORT record so a PG18 standby's `xact_redo_commit` (or
		// `xact_redo_abort`) advances `latestObservedXid`, stamps pg_xact via
		// `TransactionIdAsyncCommitTree`, and updates `KnownAssignedXids`.
		// Without this, only basebackup-snapshot XIDs are visible on the standby
		// and any commit after basebackup is invisible until the next basebackup
		// cycle. Gated on PageHeaders mode — legacy (test) clusters skip the
		// canonical record because their walWriter is not in PG-wire format.
		// The flush waits for the canonical end LSN below so both records land
		// before the client acknowledges commit (synchronous_commit = on).
		if walWriter.PageHeadersEnabled() {
			xactTime := pgTimestampNowUsec()
			switch kind {
			case mvcc.XactCommit:
				canonPayload := catalog.BuildCanonicalXactCommitPayload(uint32(xid), xactTime)
				_, canonEnd, err := walWriter.Append(canonPayload)
				if err != nil {
					return err
				}
				if canonEnd > endLSN {
					endLSN = canonEnd
				}
			case mvcc.XactAbort:
				canonPayload := catalog.BuildCanonicalXactAbortPayload(uint32(xid), xactTime)
				if _, _, err := walWriter.Append(canonPayload); err != nil {
					return err
				}
			}
		}
		// Synchronous commit (M0042-0003): flush the commit WAL record to
		// disk before returning to the client so the transaction is durable
		// across a server crash. Mirrors upstream's synchronous_commit = on
		// default. Aborts are not flushed (they're discarded on replay).
		// M0117-0007 Part B: skipped when waitLocalFlush is false
		// (synchronous_commit=off) — durability is instead guaranteed by the
		// CLOG async-commit write barrier below (SetCommittedWithLSN
		// associates endLSN with this XID's CLOG page, and
		// flushWALBeforeWriteLocked flushes the WAL up to it the moment that
		// page is written back to disk, whenever that happens).
		if kind == mvcc.XactCommit && waitLocalFlush {
			if werr := walWriter.FlushUpTo(endLSN); werr != nil {
				// ErrLSNNotWritten can surface when the WAL buffer
				// position accounting has a transient race (the WAL
				// Append comment calls this out for Path A, M0099).
				// The commit record IS in the WAL buffer and will be
				// persisted by the next checkpoint or explicit flush.
				// Treat as non-fatal to avoid aborting transactions
				// (same as the background flusher on line 1005 of
				// writer.go which also ignores this sentinel).
				if !errors.Is(werr, wal.ErrLSNNotWritten) {
					return werr
				}
			}
		}
		// Persist commit/abort status in clog (M0030-0007). Non-fatal: the
		// WAL XactCommit record is the primary durability mechanism (or, for
		// an async commit, the CLOG write barrier — see above).
		switch kind {
		case mvcc.XactCommit:
			if waitLocalFlush {
				_ = clog.SetCommitted(xid)
			} else {
				_ = clog.SetCommittedWithLSN(xid, endLSN)
			}
		case mvcc.XactAbort:
			_ = clog.SetAborted(xid)
		}
		return nil
	})
	// M0106-0013: advance the catalog OID counter to match pg_control's
	// checkPointCopy.nextOid from the most recent checkpoint. This ensures
	// the OID counter survives a crash (pg_control is durably updated at
	// every checkpoint).
	if pgCtrl, pce := control.ReadControlFile(abs); pce == nil && pgCtrl != nil {
		cat.AdvanceNextOIDPast(pgCtrl.CheckPointCopyNextOid)
		// M0106-0013: also advance txnMgr.NextXID from the checkpoint's
		// nextXid so snapshots taken after restart have Xmax >= the
		// last-checkpointed NextXID. The low 32 bits of the FullTransactionId
		// field hold the raw XID (epoch is in the high 32 bits; epoch ≠ 0
		// is not yet used in goopg v0).
		if pgCtrl.CheckPointCopyNextXid != 0 {
			checkpointNextXid := storage.TransactionID(uint32(pgCtrl.CheckPointCopyNextXid))
			txnMgr.SetNextXID(checkpointNextXid)
		}
	}
	cat.SetDBOID(detectCatalogDBOID(abs))
	// Upgrade path: if the clog is empty (old cluster started before M0030-0007
	// landed), initialize all prior XIDs as committed so loadUserTablesFromHeap
	// doesn't reject their rows.
	if clog.IsEmpty() {
		if uerr := clog.InitializeAsCommitted(txnMgr.NextXID()); uerr != nil {
			_ = pool.Close()
			_ = walWriter.Close()
			_ = mgr.Close()
			return nil, fmt.Errorf("goopg: clog upgrade: %w", uerr)
		}
	} else {
		// (M0106-0011) Crash-recovery implicit abort: WAL replay
		// restored the on-disk pg_class / pg_attribute heap pages,
		// but the JSON catalog snapshot at last clean shutdown does
		// not necessarily cover xids that were allocated after it,
		// so txnMgr.NextXID() alone is not a reliable upper bound.
		// Scan the catalog heap relfiles for the highest xmin/xmax
		// actually present on disk and mirror PG's CLOG semantics:
		// any of those xids whose clog slot is still TxnStatusUnknown
		// must have crashed in progress (no commit/abort marker ever
		// reached the clog), so stamp them Aborted before
		// loadUserTablesFromHeap runs. This is the implicit-abort
		// counterpart to the explicit-rollback filter added in
		// M0106-0011 loop 30.
		//
		// Basebackup-attached clusters must call InitializeAsCommitted
		// with the upstream nextXid before this point so upstream xids
		// stay Committed (see CLog.MarkUnknownAsAborted comment).
		highXID, herr := highestCatalogXID(mgr, cat)
		if herr != nil {
			_ = pool.Close()
			_ = walWriter.Close()
			_ = mgr.Close()
			return nil, fmt.Errorf("goopg: scan catalog xids: %w", herr)
		}
		if highXID >= txnMgr.NextXID() {
			txnMgr.SetNextXID(highXID + 1)
		}
		if aerr := clog.MarkUnknownAsAborted(txnMgr.NextXID()); aerr != nil {
			_ = pool.Close()
			_ = walWriter.Close()
			_ = mgr.Close()
			return nil, fmt.Errorf("goopg: clog implicit-abort sweep: %w", aerr)
		}
	}
	// M0106-0013: advance NextXID past the highest committed/aborted XID
	// recorded in the clog (loaded from SLRU above via EnablePGSLRUMirror
	// or stamped by replayCLogFromWAL below). This ensures that any
	// snapshot taken by the first post-restart session has Xmax large
	// enough to see all pre-crash committed rows — even when the catalog
	// heap (highestCatalogXID) only covers DDL transactions and not
	// user-table INSERT XIDs.
	if highClogXID := clog.HighestKnownXID(); highClogXID > 0 {
		txnMgr.SetNextXID(highClogXID + 1)
	}
	// Register system catalog heap tables (pg_type, pg_attribute) if their
	// relfiles exist. Safe to skip on old clusters without the M0030-0001 relfiles.
	if err := loadSystemCatalogsIfPresent(abs, cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: system catalog load: %w", err)
	}
	// M0110-0003: restore user-created schemas (CREATE/DROP SCHEMA) from the
	// WAL BEFORE loading user tables. goopg has no per-schema on-disk namespace,
	// so the catalog's schema registry is reconstructed here from the WAL
	// records (RecordKindCreateSchema/DropSchema). It must run before
	// loadUserTablesFromHeap / loadUserIndexesFromHeap so those passes can
	// reverse-map a recovered pg_class.relnamespace OID back to the schema name
	// (cat.SchemaNameForOID) — otherwise a user-schema table would be reloaded
	// under the wrong schema. Same WAL-replay mechanism as the database DDL
	// replay below.
	if err := replaySchemaDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: schema DDL replay: %w", err)
	}
	// DU-002 (M0119-0004) restart persistence: restore CREATE/DROP TRANSFORM
	// objects (pg_transform) from the WAL the same way. Order relative to
	// schema replay does not matter — transforms are keyed by (type name,
	// language), not by schema OID.
	if err := replayTransformDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: transform DDL replay: %w", err)
	}
	// DU-002 restart-persistence follow-up: restore CREATE/DROP CAST objects
	// (pg_cast) from the WAL the same way. Order relative to transform/schema
	// replay does not matter — casts are keyed by (source type, target
	// type), not by schema OID.
	if err := replayCastDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: cast DDL replay: %w", err)
	}
	// DU-002 restart-persistence follow-up: restore CREATE/DROP CONVERSION
	// objects (pg_conversion) from the WAL the same way. Unlike
	// transforms/casts, a conversion is schema-scoped (keyed by namespace
	// OID + name), so this must run after replaySchemaDDLRecords above has
	// repopulated the schema OID map.
	if err := replayConversionDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: conversion DDL replay: %w", err)
	}
	// DU-002 restart-persistence follow-up: restore CREATE/DROP TEXT SEARCH
	// DICTIONARY objects (pg_ts_dict) and CREATE/ADD MAPPING/DROP TEXT
	// SEARCH CONFIGURATION objects (pg_ts_config/pg_ts_config_map) from the
	// WAL the same way. Like conversion/collation, both are schema-scoped
	// (keyed by namespace OID + name), so this must run after
	// replaySchemaDDLRecords above has repopulated the schema OID map.
	if err := replayTSDictDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: text search dictionary DDL replay: %w", err)
	}
	if err := replayTSConfigDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: text search configuration DDL replay: %w", err)
	}
	// DU-002 restart-persistence follow-up: restore CREATE/DROP COLLATION
	// objects (pg_collation) from the WAL the same way. Like a conversion, a
	// collation is schema-scoped (keyed by namespace OID + name), so this
	// must run after replaySchemaDDLRecords above has repopulated the schema
	// OID map.
	if err := replayCollationDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: collation DDL replay: %w", err)
	}
	// DU-002 restart-persistence follow-up (slice 405 ledger resume point
	// (c)): restore CREATE/ALTER AGGREGATE objects (pg_aggregate/pg_proc)
	// from the WAL the same way. Unlike collation/conversion, a user
	// aggregate has no Schema field yet, so order relative to schema replay
	// does not matter.
	if err := replayAggregateDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: aggregate DDL replay: %w", err)
	}

	// M0114: try the fast-start catalog cache (pg_goopg_catalog_cache.json).
	// If the JSON snapshot is present and valid, populate the catalog directly
	// without scanning pg_class/pg_attribute pages. Falls through to the heap
	// scan on a miss (file absent, version mismatch, or parse error).
	cacheHit := false
	if abs != "" {
		var cerr error
		if cacheHit, cerr = readCatalogCache(abs, cat.DBOID(), cat); cerr != nil {
			slog.Warn("catalogCache: read failed, falling back to heap scan", "err", cerr)
			cacheHit = false
		}
	}

	// Load user tables from the pg_class/pg_attribute heap files (M0030-0003).
	// This is the sole catalog recovery path: DDL writes rows here via
	// syncTableToCatalogHeap, and WAL replay restores them after a crash.
	// Safe on old clusters — skips if pg_class relfile is absent.
	// The clog is passed to filter rows whose xmin was never committed (M0030-0007).
	// Skipped when M0114 catalog cache provided a valid snapshot above.
	if !cacheHit {
		if err := loadUserTablesFromHeap(mgr, cat, clog); err != nil {
			_ = pool.Close()
			_ = walWriter.Close()
			_ = mgr.Close()
			return nil, fmt.Errorf("goopg: user table heap load: %w", err)
		}
	}

	// M0106-0013: after loading user tables from heap, advance the OID
	// counter past every OID seen in the heap pages. This ensures the
	// counter never re-uses an OID already present on disk.
	// The pg_control read earlier covers the checkpoint-based advance;
	// this covers the residual gap between the last checkpoint and the crash.
	for _, tbl := range cat.AllTables() {
		if tbl.OID >= catalog.FirstUserOID {
			cat.AdvanceNextOIDPast(tbl.OID)
		}
	}

	// TOAST OID counter restart persistence (root-0022 follow-up): the
	// executor's toastOIDCounter is process-local and always starts at 0,
	// but the TOAST relations it writes chunk_id rows into survive a
	// restart on disk. Without reseeding, the first TOAST write after
	// every restart would reissue chunk_id 1, colliding with whatever
	// chunk_id 1 already resides in the same table's TOAST relation from
	// before the restart and corrupting detoast reassembly for BOTH
	// values (deferral ledger 2026-07-02, WordPress wp_options neighbor-row
	// corruption). Runs unconditionally (even on the M0114 cache-hit path)
	// since the counter always resets on process start regardless of how
	// the catalog was loaded. Skips any table whose TOAST relation has no
	// on-disk file yet (cheap Pool.Exists check inside).
	{
		mainRels := make([]storage.RelFileNode, 0, len(cat.AllTables()))
		for _, tbl := range cat.AllTables() {
			mainRels = append(mainRels, cat.RelFileNode(tbl))
		}
		if err := executor.SeedToastOIDCounter(pool, mainRels); err != nil {
			slog.Warn("TOAST OID counter reseed failed", "err", err)
		}
	}

	// M0114: write the catalog cache after a successful heap scan so the
	// next startup can skip pg_class/pg_attribute scanning entirely.
	// Non-fatal: a write failure just means a cold-start next time.
	if abs != "" && !cacheHit {
		if werr := writeCatalogCache(abs, cat.DBOID(), cat); werr != nil {
			slog.Warn("catalogCache: write failed", "err", werr)
		}
	}

	// M0054-0001: replay CREATE/DROP DATABASE WAL records into the
	// catalog's database registry. Physical WAL replay (line ~212
	// above) ignored these records because they don't touch on-disk
	// storage in v0; the recovery driver applies them here, after the
	// catalog is fully constructed, so the next connection sees an
	// accurate `pg_database`. Order matters: a drop following a
	// create cancels out, so we walk records in stream order.
	if err := replayDatabaseDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: database DDL replay: %w", err)
	}

	// M0119-0004-ACLHEAP (ALTER DATABASE ... SET follow-up): replay
	// ALTER DATABASE ... SET/RESET WAL records into pg_db_role_setting.
	// Order relative to replayDatabaseDDLRecords does not matter — each
	// record carries its own dbOid, not a name resolved through the
	// database registry.
	if err := replayDatabaseConfigRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: database config replay: %w", err)
	}

	// M0119-0004-ACLHEAP (ALTER ROLE ... SET follow-up): replay ALTER ROLE
	// ... SET/RESET WAL records into pg_db_role_setting. Each record keys
	// off the role's OID (stable across a rename/restart), not its name, so
	// ordering relative to role DDL replay does not matter.
	if err := replayRoleConfigRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: role config replay: %w", err)
	}

	// M0112: restore per-column planner statistics from pg_statistic.
	// Non-fatal: if absent or malformed, the planner uses defaults until
	// the next ANALYZE run.
	if err := loadStatisticsFromHeap(mgr, cat, clog); err != nil {
		slog.Warn("loadStatisticsFromHeap failed", "err", err)
	}

	// M0113: recover user indexes from pg_index heap (PG18-canonical path).
	// Falls back to the WAL-replay path below for clusters that predate M0113
	// (no pg_index rows written yet).
	if err := loadUserIndexesFromHeap(mgr, cat, clog); err != nil {
		slog.Warn("loadUserIndexesFromHeap failed, falling back to WAL replay", "err", err)
	}

	// M0079-0001: replay CREATE/DROP INDEX WAL records into the
	// in-memory catalog. Without this pass, indexes created
	// after the last checkpoint would disappear from
	// the catalog after a non-graceful restart even though
	// their relfiles and btree pages are restored by physical
	// replay. The pgbench `pgbench_accounts.aid` PK was the
	// surfacing case (~70x TPS regression after restart because
	// every UPDATE fell back to a 10M-row Seq Scan). Must run
	// AFTER `loadUserTablesFromHeap` so the owning table is
	// already in the catalog when we register the index.
	// M0113: kept as fallback for pre-M0113 clusters without pg_index rows.
	if err := replayIndexDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: index DDL replay: %w", err)
	}

	// Sequence / SERIAL restart persistence: re-register sequences from
	// RecordKindSequenceState WAL records and restore the owning columns'
	// serial/identity catalog markers. Must run AFTER loadUserTablesFromHeap
	// (the owning tables have to be registered so the column markers can be
	// applied) — the heap-reloaded serial columns read back as their base
	// integer type because pg_attribute stores the PG-canonical atttypid.
	// See internal/initdb/sequence_ddl_recovery.go.
	if err := replaySequenceDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: sequence DDL replay: %w", err)
	}

	// Column DEFAULT persistence (root-0020 follow-up): re-parse the DEFAULT
	// expression snapshots emitted by syncTableToCatalogHeap onto the
	// heap-reloaded columns (DefaultExpr is an in-memory AST pg_attribute
	// cannot carry). Must run AFTER loadUserTablesFromHeap.
	if err := replayColumnDefaultsRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: column-defaults replay: %w", err)
	}

	// Materialized-view query persistence (M0119-0004 follow-up): re-parse the
	// defining-query snapshots emitted by syncTableToCatalogHeap onto the
	// heap-reloaded matviews (View is an in-memory AST pg_class cannot carry).
	// Must run AFTER loadUserTablesFromHeap.
	if err := replayMatViewRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: matview replay: %w", err)
	}

	// Plain-view query persistence (M0119-0004 follow-up, sibling of the
	// matview replay above): re-parse the defining-query snapshots emitted by
	// syncTableToCatalogHeap onto the heap-reloaded views (View is an
	// in-memory AST pg_class cannot carry). Must run AFTER loadUserTablesFromHeap.
	if err := replayViewRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: view replay: %w", err)
	}

	// Role/auth restart persistence (root-0021): load the durable BASE from
	// the pg_authid heap file (global/1260 — rewritten on every role DDL by
	// SyncPgAuthidFile, mirroring PostgreSQL's pg_authid-as-store model),
	// then replay any newer role WAL records ON TOP (the crash tail).
	if err := LoadRolesFromAuthidHeap(abs, cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: pg_authid load: %w", err)
	}
	if err := replayRoleDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: role DDL replay: %w", err)
	}

	// M0119-0004-ACLHEAP (GRANT/REVOKE ROLE membership): replay GRANT/REVOKE
	// ROLE WAL records into pg_auth_members. Must run AFTER
	// LoadRolesFromAuthidHeap/replayRoleDDLRecords immediately above: those
	// calls load role OIDs preserved from before the crash (RegisterRoleWithOID)
	// and can advance the catalog's nextOID counter well past its
	// pre-replay value, so running this pass first (which mints a FRESH OID
	// per membership row via GrantRoleMembership -> AllocOID, since
	// pg_auth_members.oid is not dumped by pg_dump/pg_dumpall and so has no
	// stability requirement of its own) risks a numeric OID collision with a
	// role OID loaded afterward.
	if err := replayRoleMembershipRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: role membership replay: %w", err)
	}

	// M0106-0013: stamp the clog from WAL commit/abort records and advance
	// txnMgr.NextXID past any XIDs recorded in WAL but not yet in the
	// SLRU/flat-file (the narrow window between WAL fsync and clog writes).
	// This is the authoritative clog-from-WAL path that mirrors PG's
	// StartupXLOG xact_redo_commit behaviour. Non-fatal: physical replay
	// already succeeded; a clog-stamp failure only affects visibility of
	// some committed rows, which will surface as a user error rather than
	// a startup error.
	if abs != "" {
		if err := replayCLogFromWAL(filepath.Join(abs, "pg_wal"), clog, txnMgr); err != nil {
			slog.Default().Warn("replayCLogFromWAL failed", "err", err)
		}
		// Re-advance NextXID after replayCLogFromWAL may have stamped more
		// XIDs into the clog.
		if highClogXID := clog.HighestKnownXID(); highClogXID > 0 {
			txnMgr.SetNextXID(highClogXID + 1)
		}
	}

	// M0106-0011 follow-up (b): regenerate pg_internal.init files after
	// WAL recovery completes. Crash recovery replays
	// RecordKindXactCommitInval records which UNLINK the init files
	// (mirrors PG's standby-side redo) but does not regenerate them.
	// Without this, PG standbys that attach via pg_basebackup after a
	// crash restart would find missing init files until the first DDL
	// commit. Non-fatal: the PostCheckpointFn hook regenerates on the
	// next checkpoint as a belt-and-suspenders fallback.
	if abs != "" {
		if err := bootstrapRelcacheInitFiles(abs); err != nil {
			slog.Default().Warn("post-recovery relcache init file regeneration failed", "err", err)
		}
	}

	// G1/G9: install the CLOG truncate WAL hook NOW — after replayCLogFromWAL
	// has finished re-applying any CLOG_TRUNCATE records (which call
	// TruncateCLOG with a nil logger so recovery does not recursively append
	// to the WAL). From here on, every live TruncateCLOG (driven by the
	// checkpointer's TruncateCLOGFn) emits a durable RecordKindClogTruncate so
	// a standby learns the new valid xid and a subsequent crash recovery can
	// re-apply the idempotent truncation. Mirrors PG's WriteTruncateXlogRec
	// (postgres/src/backend/access/transam/clog.c:1029). Best-effort flush:
	// the truncation is durable-ordered after the checkpoint marker, so the
	// record need not block; the next checkpoint/flush persists it.
	if walWriter != nil {
		clog.SetTruncateLogger(func(oldestXid storage.TransactionID) error {
			payload := wal.EncodeClogTruncate(oldestXid)
			_, endLSN, err := walWriter.Append(payload)
			if err != nil {
				return err
			}
			if ferr := walWriter.FlushUpTo(endLSN); ferr != nil {
				// Non-fatal: the record is in the WAL buffer and the next
				// checkpoint flush will persist it; the physical clog removal
				// already happened durable-ordered after a checkpoint.
				slog.Default().Warn("clog-truncate WAL flush failed", "err", ferr)
			}
			return nil
		})
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
	syncRep := wal.NewSyncRep()

	defaultGUC := wal.DefaultGUCParameters()
	// Pre-register the checkpointer background slot (M0122-0003 writeback
	// simplification 3 follow-up) so Run's OnLoopStart/OnLoopEnd hooks below
	// can track its goroutine identity the same way walProcNum tracks the
	// WAL writer's. TrackIOTiming is seeded once from the boot-time GUC and
	// never updated afterward: unlike a client backend, a background
	// worker has no per-session `SET track_io_timing` to react to.
	//
	// Previously cmd/goopg/main.go registered a "cp-0" backend via
	// act.Register (the regular, numeric-PID-keyed path), which silently
	// collided with walProcNum: procNumForPID treats every non-numeric PID
	// as bgBase, the exact slot RegisterBackground(WalWriterIdx, ...)
	// above already claims, so the checkpointer's Register call was
	// clobbering the WAL writer's activitySlot. RegisterBackground(
	// CheckpointerIdx, ...) claims its own reserved slot instead.
	checkpointerProcNum := act.RegisterBackground(activity.CheckpointerIdx, &activity.Backend{
		PID:         "cp-0",
		BackendType: "checkpointer",
		State:       "active",
	})
	act.UpdateTrackIOTiming(checkpointerProcNum, opts.TrackIOTiming)
	cp := wal.NewCheckpointer(pool, walWriter, wal.CheckpointerConfig{
		DataDir:             abs,
		SegmentSize:         walCfg.SegmentSize,
		GUCParams:           defaultGUC,
		PGCompatCheckpoints: true,
		OnLoopStart: func() {
			activity.SetCurrentGoroutine(act, checkpointerProcNum)
		},
		OnLoopEnd: func() {
			activity.ClearCurrentGoroutine()
		},
		// M0106-0010 batched-45: refresh checkPointCopy.nextXid into
		// pg_control at every checkpoint from the live mvcc manager.
		// batched-47: DataDir above was previously unset on the runtime
		// construction site, so the pg_control update branch inside
		// runCheckpoint was a silent no-op. Without it the
		// checkPointCopy.nextXid in basebackup pg_control stayed at the
		// initdb-time bootstrap value (3), and a PG standby attached
		// after the basebackup hid every user tuple created by goopg
		// after initdb.
		NextXIDFn: func() uint64 { return uint64(txnMgr.NextXID()) },
		// M0106-0013: wire the catalog's OID counter so each checkpoint
		// embeds the live nextOid into pg_control and the WAL record.
		// A crashed cluster can then recover nextOid from pg_control
		// without depending on pg_catalog.json.
		NextOIDFn: cat.NextOID,
		// M0106-0011 follow-up (b): regenerate pg_internal.init after
		// each checkpoint so PG standbys can always attach. Uses
		// WithRelCacheInitLock to prevent TOCTOU races with concurrent
		// backend startup reading the init files.
		PostCheckpointFn: func() error {
			if abs == "" {
				return nil
			}
			return catalog.WithRelCacheInitLock(func() error {
				return bootstrapRelcacheInitFiles(abs)
			})
		},
		// G1: durable-ordered CLOG truncation. After each checkpoint is on
		// disk, truncate pg_xact up to a conservative horizon and emit a
		// CLOG_TRUNCATE WAL record (via the truncate logger installed on the
		// clog above). The horizon is min(datfrozenxid, OldestXmin):
		//   - datfrozenxid = min(relfrozenxid) across user tables — every XID
		//     below it is frozen in every user heap, so its status is dead;
		//   - OldestXmin guards against truncating status still consultable by
		//     any in-progress or future snapshot.
		// TruncateCLOG itself never drops the page containing the horizon, is
		// idempotent, and is nil-safe on the logger. Conservative by design:
		// when no user table has a relfrozenxid yet, datfrozenxid is 0 and we
		// skip truncation entirely (truncate less when in doubt).
		TruncateCLOGFn: func() error {
			datFrozen := cat.DatFrozenXID()
			if datFrozen < mvcc.FirstNormalTransactionID {
				return nil // nothing frozen yet — never truncate
			}
			horizon := datFrozen
			// horizon = min(datfrozenxid, OldestXmin) using wraparound-safe
			// modular comparison (PG TransactionIdPrecedes), not plain `<`,
			// which would mis-order XIDs across the 2^32 boundary and let
			// truncation overrun the snapshot horizon. The `< FirstNormal`
			// guards stay plain — they are TransactionIdIsNormal sentinel
			// checks against the bootstrap constant, not horizon comparisons.
			if ox := txnMgr.OldestXmin(); ox != 0 && storage.XIDPrecedes(ox, horizon) {
				horizon = ox
			}
			if horizon < mvcc.FirstNormalTransactionID {
				return nil
			}
			return clog.TruncateCLOG(horizon)
		},
		// M0117-0007 Part B continuation: bound how long an async commit's
		// deferred CLOG write-back can stay dirty in memory (see
		// mvcc.CLog.setStatusWithLSN / FlushAll).
		FlushCLOGFn: clog.FlushAll,
	})

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
	if err := registerStatReplicationView(cat, walSenders, walWriter, syncRep); err != nil {
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
	// pg_aggregate: CREATE AGGREGATE introspection (DU-002 slice 405).
	// Registering here (like pg_proc above) makes the view present from the
	// first session, both for the 161 built-in aggregates and any later
	// CREATE AGGREGATE.
	if err := registerPgAggregateView(cat); err != nil {
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
	// DU-002 restart-persistence follow-up (M0119-0004, loop #67 ledger
	// resume point): restore CREATE/DROP/ALTER PUBLICATION/SUBSCRIPTION
	// objects from the WAL. PubSub is not schema-scoped, so order relative
	// to schema replay above does not matter.
	if err := replayPubSubDDLRecords(filepath.Join(abs, "pg_wal"), pubsub); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: pubsub DDL replay: %w", err)
	}
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

	// DU-002 restart-persistence follow-up (M0119-0004, loop #70 ledger
	// resume point): restore CREATE/DROP/ALTER EVENT TRIGGER objects from
	// the WAL. Like PubSub, event triggers are not schema-scoped, so order
	// relative to schema replay above does not matter.
	if err := replayEventTriggerDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: event trigger DDL replay: %w", err)
	}

	// DU-002 restart-persistence follow-up (M0119-0004, loop #71 ledger
	// resume point): restore CREATE/DROP/ALTER FUNCTION/PROCEDURE objects
	// from the WAL. Like event triggers, routines are keyed by a plain
	// schema name string (no NamespaceOID to resolve), so order relative to
	// schema replay does not matter. Runs after event trigger replay so a
	// restored event trigger's evtfoid can (in principle) be cross-checked
	// against a restored routine, though neither replay path validates the
	// other today.
	if err := replayFunctionDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: function DDL replay: %w", err)
	}

	// DU-002 restart-persistence follow-up (M0119-0004, DU-002 slice 426
	// ledger resume point): restore CREATE/DROP ACCESS METHOD objects from
	// the WAL. Like event triggers, access methods are keyed by a plain name
	// string, so order relative to schema replay does not matter.
	if err := replayAccessMethodDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: access method DDL replay: %w", err)
	}

	// DU-002 restart-persistence follow-up (slice 441's own resume point):
	// restore CREATE/DROP STATISTICS (extended-statistics) objects from the
	// WAL. Runs after loadUserTablesFromHeap (above) so a restored object's
	// recorded TableOID lines up with the table it was defined on, though the
	// catalog stores the OID verbatim rather than re-resolving it.
	if err := replayStatisticsDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: statistics DDL replay: %w", err)
	}

	// DU-002 restart-persistence follow-up (M0110-0001, DU-002 slice 429
	// ledger resume point, sub-item (c)): restore CREATE/DROP TYPE ... AS
	// RANGE objects from the WAL. Like access methods, range types are keyed
	// by a plain name string, so order relative to schema replay does not
	// matter.
	if err := replayRangeTypeDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: range type DDL replay: %w", err)
	}

	// M0122-0005 restart-persistence follow-up (deferral ledger 2026-07-06
	// row: "domains have no restart persistence at all"): restore CREATE/DROP
	// DOMAIN objects from the WAL. Like range types, domains are keyed by a
	// plain name string, so order relative to schema replay does not matter.
	if err := replayDomainDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: domain DDL replay: %w", err)
	}

	// DU-002 restart-persistence follow-up (M0119-0004/M0110-0001,
	// discovered while verifying the loop #64 CREATE TYPE ... AS RANGE
	// opclass/collation follow-up — see ledger): restore CREATE/DROP
	// OPERATOR objects from the WAL. Runs after schema replay (above) since,
	// unlike range types/access methods, an operator's registry key embeds
	// its schema name.
	if err := replayOperatorDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: operator DDL replay: %w", err)
	}

	// DU-002 restart-persistence follow-up (M0119-0004/M0110-0001, closing
	// the loop #65/#66 ledger row's "still open" item (1)): restore CREATE
	// OPERATOR FAMILY / CREATE OPERATOR CLASS (+ its pg_amop/pg_amproc
	// AS-list members) / DROP OPERATOR CLASS / ALTER OPERATOR FAMILY ...
	// ADD|DROP objects from the WAL. Runs after the operator DDL replay
	// above (schema replay must have already run, and a class's AS-list
	// OPERATOR entries reference user operators by OID).
	if err := replayOperatorClassDDLRecords(filepath.Join(abs, "pg_wal"), cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: operator class/family DDL replay: %w", err)
	}

	// pg_sequences: virtual catalog view listing all registered sequences.
	// M0097-0024.
	if err := registerPgSequencesView(cat); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, err
	}

	// information_schema.sequences: standard SQL view of sequence metadata.
	// M0097-0068.
	if err := registerInformationSchemaSequencesView(cat); err != nil {
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

	// Wire AIO + data-file I/O wait-event hooks so pg_stat_activity can
	// report blocking reasons. Wired unconditionally (not gated on the
	// boot-time TrackIOTiming value) so a runtime `SET track_io_timing`
	// takes effect without a server restart; each hook body's
	// LookupTrackedGoroutine call is itself gated on act's fast-path
	// flag, so the default-off cost stays a single atomic load rather
	// than the goroutine-map lookup (M0092-0005 original rationale;
	// M0122-0003 runtime-SET follow-up). M0107-0005: use
	// LookupCurrentGoroutine (procNum) for atomic WaitEventStart instead
	// of LookupGoroutine (mutex).
	if opts.TrackIOTiming {
		act.EnableTrackIOTimingFastPath()
	}
	if aioEngine != nil {
		aioEngine.OnWaitStart = func() {
			if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
				reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitAIO)
			}
		}
		aioEngine.OnWaitEnd = func() {
			if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
				reg.WaitEventEnd(procNum)
			}
		}
	}
	mgr.OnReadWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileRead)
		}
	}
	mgr.OnReadDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventEnd(procNum)
		}
	}
	mgr.OnWriteWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileWrite)
		}
	}
	mgr.OnWriteDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventEnd(procNum)
		}
	}
	mgr.OnExtendWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileExtend)
		}
	}
	mgr.OnExtendDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventEnd(procNum)
		}
	}
	mgr.OnSyncWait = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventStart(procNum, activity.WaitTypeIO, activity.WaitDataFileSync)
		}
	}
	mgr.OnSyncDone = func() {
		if reg, procNum, ok := act.LookupTrackedGoroutine(); ok {
			reg.WaitEventEnd(procNum)
		}
	}

	// Wire WAL I/O wait-event hooks.
	// M0107-0005: capture walProcNum (int32) for atomic WaitEventStart.
	if walWriter != nil {
		walWriter.OnWALSync = func() {
			if act != nil {
				act.WaitEventStart(walProcNum, activity.WaitTypeIO, activity.WaitWALSync)
			}
		}
		walWriter.OnWALSyncDone = func() {
			if act == nil {
				return
			}
			d := act.WaitEventEnd(walProcNum)
			// fsync_time (pg_stat_io): FlushUpTo runs synchronously on the
			// calling backend's own goroutine (SetCurrentGoroutine was
			// called for it at connection setup — server.go), so
			// LookupTrackedGoroutine correctly reports *that backend's*
			// own track_io_timing setting, unlike walProcNum above (a
			// fixed background slot shared by every committing backend,
			// only suitable for the wait_event display, not per-session
			// gating). Mirrors storage.Pool's OnPinDone gating exactly.
			if _, _, ok := act.LookupTrackedGoroutine(); ok {
				walWriter.AddFsyncTimeNanos(int64(d))
			}
		}
	}

	standby, err := IsStandby(abs)
	if err != nil {
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: standby signal: %w", err)
	}

	// M0106-0010 batched-32: build the canonical WAL callback only when the
	// writer is in PG-compat PageHeaders mode so canonical records can be read
	// by PG18 standbys. In legacy mode (tests, no standby), LogCanonical stays
	// nil and DDL paths skip the canonical record emission.
	var logCanonical catalog.LogCanonicalFunc
	if walWriter != nil && walWriter.PageHeadersEnabled() {
		logCanonical = func(payload []byte) (uint64, error) {
			_, end, err := walWriter.Append(payload)
			return end, err
		}
	}

	// M0106-0010 batched-33: emit XLOG_PARAMETER_CHANGE and update pg_control
	// GUC echo fields so the first PG standby that attaches sees consistent
	// values in its pg_control copy. Mirrors PG's XLogReportParameters call
	// from postmaster startup (xlog.c:8147). Only runs in PageHeaders mode;
	// silently skipped when walWriter is nil or dataDir is empty (tests).
	if walWriter != nil && walWriter.PageHeadersEnabled() && abs != "" {
		if err := wal.ReportParameters(abs, walWriter, defaultGUC); err != nil {
			_ = pool.Close()
			_ = walWriter.Close()
			_ = mgr.Close()
			return nil, fmt.Errorf("goopg: ReportParameters: %w", err)
		}
	}

	rt := &Runtime{
		StorageMgr:     mgr,
		Pool:           pool,
		TxnMgr:         txnMgr,
		Catalog:        cat,
		WAL:            walWriter,
		LogCanonical:   logCanonical,
		Checkpointer:   cp,
		Slots:          slotsReg,
		SyncRep:        syncRep,
		WalSenders:     walSenders,
		WalReceivers:   walReceivers,
		WalSubscribers: walSubscribers,
		PubSub:         pubsub,
		AIO:            aioEngine,
		Activity:       act,
		DataDir:        abs,
		Standby:        standby,
		FSM:            storage.NewFSM(),
		VM:             storage.NewVisibilityMap(),
	}

	// M0080-0003: load persistent Visibility Map state from
	// `<DataDir>/global/pg_vm_state.bin` if present. A missing
	// file is fine — that's the fresh-from-init case OR a cluster
	// from before VM persistence existed. Failure to load is a
	// hard startup error so the operator sees the issue rather
	// than running with empty VM bits (which would still be
	// CORRECT semantically — a cleared VM bit is a conservative
	// "must check heap" — but would degrade index-only-scan
	// performance until the next VACUUM rebuilt the bits).
	if err := rt.VM.Load(storage.VMStatePath(rt.DataDir)); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: vm load: %w", err)
	}

	// M0080-0004: load persistent FSM state. Same shape /
	// nil-safety as the VM load above. A missing file is the
	// fresh-cluster case; a corrupt one is a hard startup
	// failure (running with stale FSM bits would direct INSERTs
	// to wrong pages and waste time on retries).
	if err := rt.FSM.Load(storage.FSMStatePath(rt.DataDir)); err != nil {
		_ = pool.Close()
		_ = walWriter.Close()
		_ = mgr.Close()
		return nil, fmt.Errorf("goopg: fsm load: %w", err)
	}

	// Background WAL writer loop (M0042-0003): timer-driven periodic flush
	// so buffered WAL bytes reach disk even when no commits are in flight.
	// Mirrors upstream's walwriter process cadence. Disabled (delay == 0)
	// in tests that don't need background flushing.
	if opts.WalWriterDelay > 0 && walWriter != nil {
		stop := make(chan struct{})
		rt.walwriterStop = stop
		go func() {
			ticker := time.NewTicker(opts.WalWriterDelay)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					// Drain and sync any buffered WAL. Fast no-op when
					// nothing was written since the last flush.
					_ = walWriter.FlushUpTo(walWriter.WrittenLSN())
				case <-stop:
					return
				}
			}
		}()
	}

	// Background page-writer goroutine (M0048-0003): proactively flushes
	// dirty buffer-pool pages so eviction rarely needs synchronous I/O.
	if opts.BgwriterDelay > 0 && opts.BgwriterMaxPages > 0 {
		rt.bgwriter = storage.NewBgwriter(pool, opts.BgwriterDelay, opts.BgwriterMaxPages)
		// Pre-register the bgwriter background slot (M0122-0003 writeback
		// simplification 3 follow-up) so OnLoopStart/OnLoopEnd below can
		// track its goroutine identity the same way walProcNum/
		// checkpointerProcNum track theirs. TrackIOTiming is seeded once
		// from the boot-time GUC — a background worker has no
		// per-session `SET track_io_timing` to react to.
		bgwriterProcNum := act.RegisterBackground(activity.BgwriterIdx, &activity.Backend{
			PID: "bgwriter-0",
			// "background writer" (with a space) matches upstream's
			// pg_stat_activity.backend_type / pg_stat_io backend_type
			// literal for this process (see pgstat_io.go's
			// ioBackendTypeNames).
			BackendType: "background writer",
			State:       "active",
		})
		act.UpdateTrackIOTiming(bgwriterProcNum, opts.TrackIOTiming)
		rt.bgwriter.OnLoopStart = func() {
			activity.SetCurrentGoroutine(act, bgwriterProcNum)
		}
		rt.bgwriter.OnLoopEnd = func() {
			activity.ClearCurrentGoroutine()
		}
		rt.bgwriter.Start()
	}

	return rt, nil
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
	base := filepath.Join(dataDir, "base", fmt.Sprint(cat.DBOID()))

	// heapFilePresent returns true only when the file exists AND has at least
	// one full block. The storage manager opens files with O_CREATE, so a
	// 0-byte stub can appear as a side-effect of NBlocks calls (e.g. in
	// highestCatalogXID) even when the relfile was never bootstrapped.
	heapFilePresent := func(path string) bool {
		fi, err := os.Stat(path)
		return err == nil && fi.Size() >= int64(storage.BlockSize)
	}

	// pg_type (OID 1247) — built-in type catalog.
	pgTypeFile := filepath.Join(base, fmt.Sprint(catalog.TypeRelationId))
	if heapFilePresent(pgTypeFile) {
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
	if heapFilePresent(pgAttrFile) {
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

// loadUserTablesFromHeap loads the in-memory catalog with user tables
// found in the pg_class and pg_attribute heap relfiles (M0030-0003). It scans
// all live (xmin≠0, xmax=0) pg_class rows with relkind='r' or 'm' (materialized
// view — has physical storage like a table) and OID ≥ FirstUserOID,
// then collects their column definitions from pg_attribute rows, and calls
// TryRegisterUserTable for each.
//
// This is the sole catalog recovery path: DDL writes rows to the heap via
// syncTableToCatalogHeap, and WAL replay restores them after a crash.
//
// The clog parameter (M0030-0007) filters out rows whose xmin was never
// committed — this handles tables created in transactions that crashed before
// reaching COMMIT. If clog is nil (should not happen after M0030-0007 landed)
// the scan falls back to the old xmax-only check.
//
// The scan is safe on old clusters (pre-M0030-0001) that have no pg_class relfile.
// highestCatalogXID scans the on-disk pg_class and pg_attribute heap pages
// and returns the highest xmin/xmax found across all live tuples. Used by
// Open()'s implicit-abort sweep (M0106-0011) to size the clog so xids
// allocated after the last clean catalog-snapshot save (and therefore not
// covered by the snapshot's NextXID) are still seen and Aborted-stamped if
// they never reached commit. Returns 0 when the catalog heap is empty or
// absent (fresh initdb).
func highestCatalogXID(mgr *storage.Manager, cat *catalog.InMemory) (storage.TransactionID, error) {
	var maxXID storage.TransactionID
	observe := func(xid storage.TransactionID) {
		if xid != storage.InvalidTransactionID && xid > maxXID {
			maxXID = xid
		}
	}
	scan := func(relOID uint32) error {
		rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: relOID, Fork: storage.MainFork}
		nblocks, err := mgr.NBlocks(rel)
		if err != nil || nblocks == 0 {
			return nil
		}
		page := make(storage.Page, storage.BlockSize)
		for blk := storage.BlockNumber(0); blk < nblocks; blk++ {
			if err := mgr.ReadBlock(rel, blk, page); err != nil {
				return fmt.Errorf("highestCatalogXID: read rel %d blk %d: %w", relOID, blk, err)
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
				observe(ht.Header.Xmin)
				observe(ht.Header.Xmax)
			}
		}
		return nil
	}
	if err := scan(catalog.RelationRelationId); err != nil {
		return 0, err
	}
	if err := scan(catalog.AttributeRelationId); err != nil {
		return 0, err
	}
	return maxXID, nil
}

func loadUserTablesFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	classRel := storage.RelFileNode{
		DBOid:  cat.DBOID(),
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	nClassBlocks, err := mgr.NBlocks(classRel)
	if err != nil || nClassBlocks == 0 {
		return nil // pg_class absent or empty — old cluster or fresh initdb
	}

	page := make(storage.Page, storage.BlockSize)

	// Pass 1: collect user table rows from pg_class.
	type recoveredPGClassRow struct {
		row      catalog.PGClassRow
		physical bool
	}
	var userTableRows []recoveredPGClassRow
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
			physicalRow := false
			row, err := catalog.DecodePGClassRow(ht.Data)
			if err != nil {
				row, err = catalog.DecodePGClassPhysicalRow(ht.Data)
				if err != nil {
					continue
				}
				physicalRow = true
				// DecodePGClassPhysicalRow only covers the fixed-offset
				// prefix; reloptions (attnum 33) is a varlena column past
				// it, so re-decode the full PG18-canonical row with the
				// general PG-tuple decoder to recover it. A decode failure
				// here just leaves RelOptions empty (best-effort — the
				// fixed fields above already succeeded). M0119-0004.
				natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
				cols := executor.PGClassColumnsPG18()
				decoded := make(executor.Row, len(cols))
				if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr == nil {
					const reloptionsOrdinal = 32
					if reloptionsOrdinal < len(decoded) && !decoded[reloptionsOrdinal].IsNull() {
						row.RelOptions = decoded[reloptionsOrdinal].StringValue()
					}
				}
			}
			// Skip rows from uncommitted or crashed goopg transactions
			// (M0030-0007). PG basebackup tuples come from a consistent
			// snapshot and carry xmin values from the upstream cluster
			// that are out-of-range for our local clog (GetStatus returns
			// TxnStatusUnknown) — those must not be filtered. But
			// goopg-emitted rows in the PG18-canonical layout share the
			// physical decoder path (post-M0106-0010 syncTableToCatalogHeap)
			// and their xmin IS in our clog: if that xid is aborted (e.g.
			// CREATE TABLE inside a rolled-back transaction), the row must
			// be excluded regardless of layout. Applying the filter only
			// for the explicit Aborted state keeps the basebackup
			// pass-through intact while honoring local ROLLBACKs.
			if clog != nil && clog.GetStatus(ht.Header.Xmin) == mvcc.TxnStatusAborted {
				continue
			}
			if !physicalRow && clog != nil && clog.GetStatus(ht.Header.Xmin) != mvcc.TxnStatusCommitted {
				continue
			}
			if (row.RelKind == "r" || row.RelKind == "m" || row.RelKind == "v") && row.OID >= catalog.FirstUserOID {
				userTableRows = append(userTableRows, recoveredPGClassRow{row: row, physical: physicalRow})
			}
		}
	}
	if len(userTableRows) == 0 {
		return nil // no user tables in heap
	}

	// Pass 2: collect pg_attribute rows for user tables.
	attrRel := storage.RelFileNode{
		DBOid:  cat.DBOID(),
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
			// Exclude rows from rolled-back goopg transactions. See the
			// matching comment above the pg_class scan filter for why
			// only the explicit Aborted state is checked here (basebackup
			// pass-through hinges on xids unknown to the local clog).
			if clog != nil && clog.GetStatus(ht.Header.Xmin) == mvcc.TxnStatusAborted {
				continue
			}
			row, err := catalog.DecodePGAttributeRow(ht.Data)
			if err != nil {
				row, err = catalog.DecodePGAttributePhysicalRow(ht.Data)
				if err != nil {
					continue
				}
			}
			if !row.AttIsDropped && row.AttRelID >= catalog.FirstUserOID && row.AttNum > 0 {
				attrByRelOID[row.AttRelID] = append(attrByRelOID[row.AttRelID], row)
			}
		}
	}

	// Pass 3: register each user table with its heap-recovered column definitions.
	for _, recovered := range userTableRows {
		tr := recovered.row
		attrRows := attrByRelOID[tr.OID]
		sort.Slice(attrRows, func(i, j int) bool {
			return attrRows[i].AttNum < attrRows[j].AttNum
		})

		cols := make([]catalog.Column, len(attrRows))
		for i, ar := range attrRows {
			// An array column persists its array (_typename) OID in atttypid;
			// reverse-map it to the element type and re-flag IsArray so the
			// reloaded catalog matches the CREATE-time shape. DU-002 slice 62.
			typOID := ar.AttTypID
			isArray := false
			if base, ok := catalog.BaseOIDForArray(typOID); ok {
				typOID = base
				isArray = true
			}
			cols[i] = catalog.Column{
				Name:    ar.AttName,
				Type:    catalog.Type{Name: catalog.OIDToTypeName(typOID), IsArray: isArray},
				NotNull: ar.AttNotNull,
				Ordinal: i,
			}
		}

		schema := ""
		if tr.RelNamespace == catalog.PGCatalogNamespaceOID {
			schema = "pg_catalog"
		} else if recovered.physical && tr.RelNamespace == catalog.PublicNamespaceOID {
			schema = "public"
		} else if name := cat.SchemaNameForOID(tr.RelNamespace); name != "" {
			// M0110-0003: a user table created in a CREATE SCHEMA namespace
			// carries that schema's OID in relnamespace (written by
			// syncTableToCatalogHeap via namespaceOIDForSchema). The schema
			// registry was restored above (replaySchemaDDLRecords), so reverse-map
			// the OID back to the schema name to reload the table in its schema.
			schema = name
		}

		tbl := &catalog.Table{
			Schema:         schema,
			Name:           tr.RelName,
			Columns:        cols,
			OID:            tr.OID,
			SmallDimension: tr.RelName == "region" || tr.RelName == "nation",
			// IsMatView from relkind alone; View/ViewDef/IsPopulated are
			// restored afterward by replayMatViewRecords (the AST/populated
			// flag have no heap representation — see RecordKindCreateMatView).
			IsMatView: tr.RelKind == "m",
			// A plain view (relkind='v') has no physical heap storage — mirror
			// catalog.InMemory.CreateView's Virtual=true. View/ViewDef are
			// restored afterward by replayViewRecords (the AST has no heap
			// representation — see RecordKindCreateView).
			Virtual: tr.RelKind == "v",
		}
		if tr.RelFileNode != 0 && tr.RelFileNode != tr.OID {
			tbl.RelFileNodeOID = tr.RelFileNode
		}
		// Restore storage parameters (fillfactor, autovacuum_*, and for
		// views security_barrier/security_invoker/check_option) from the
		// heap-persisted reloptions column — otherwise they silently
		// reverted to defaults across every restart. M0119-0004.
		if tr.RelOptions != "" {
			catalog.ApplyTableReloptions(tbl, tr.RelOptions)
		}
		if err := cat.TryRegisterUserTable(tbl); err != nil {
			return fmt.Errorf("loadUserTablesFromHeap: register %q: %w", tr.RelName, err)
		}
	}
	return nil
}

func detectCatalogDBOID(dataDir string) uint32 {
	const postgresDatabaseOID = 1262
	path := filepath.Join(dataDir, "global", fmt.Sprint(postgresDatabaseOID))
	data, err := os.ReadFile(path)
	if err != nil {
		return catalog.DefaultDBOid
	}
	for off := 0; off+storage.BlockSize <= len(data); off += storage.BlockSize {
		page := storage.Page(data[off : off+storage.BlockSize])
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
			dbOid, name, err := decodePGDatabasePhysicalRow(ht.Data)
			if err != nil {
				continue
			}
			if name == "postgres" {
				return dbOid
			}
		}
	}
	return catalog.DefaultDBOid
}

func decodePGDatabasePhysicalRow(data []byte) (uint32, string, error) {
	const (
		pgNameDataLen    = 64
		pgDatabaseMinLen = 4 + pgNameDataLen
	)
	if len(data) < pgDatabaseMinLen {
		return 0, "", fmt.Errorf("pg_database physical row too short: len=%d", len(data))
	}
	nameBytes := data[4 : 4+pgNameDataLen]
	end := bytes.IndexByte(nameBytes, 0)
	if end < 0 {
		end = len(nameBytes)
	}
	name := string(nameBytes[:end])
	if name == "" {
		return 0, "", fmt.Errorf("pg_database.datname: empty")
	}
	return binary.LittleEndian.Uint32(data[0:4]), name, nil
}

// SaveCatalog persists catalog metadata needed to survive a restart.
// The catalog schema is durably stored in the pg_class/pg_attribute heap
// relfiles by syncTableToCatalogHeap on every DDL; this call only updates
// pg_control's nextOid so the OID counter is not re-used after a restart.
func (r *Runtime) SaveCatalog() error {
	if r == nil || r.Catalog == nil || r.DataDir == "" {
		return nil
	}
	cat, ok := r.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	nextOid := cat.NextOID()
	if werr := control.UpdateControlFile(r.DataDir, func(cd *control.ControlFileData) {
		if nextOid > cd.CheckPointCopyNextOid {
			cd.CheckPointCopyNextOid = nextOid
		}
	}); werr != nil {
		slog.Warn("SaveCatalog: pg_control nextOid update failed", "err", werr)
	}
	return nil
}

// SaveVM writes the runtime's VisibilityMap state to
// `<DataDir>/global/pg_vm_state.bin` atomically (temp file +
// rename). Callers — typically the graceful-shutdown defer in
// `cmd/goopg/main.go` — invoke this alongside SaveCatalog so
// VM bits survive a clean restart. Returns nil when r or VM
// is nil, mirroring SaveCatalog's nil-safety. (M0080-0003.)
func (r *Runtime) SaveVM() error {
	if r == nil || r.VM == nil {
		return nil
	}
	return r.VM.Save(storage.VMStatePath(r.DataDir))
}

// SaveFSM writes the runtime's FSM state to
// `<DataDir>/global/pg_fsm_state.bin` atomically. Same shape
// as SaveVM. (M0080-0004.)
func (r *Runtime) SaveFSM() error {
	if r == nil || r.FSM == nil {
		return nil
	}
	return r.FSM.Save(storage.FSMStatePath(r.DataDir))
}

// Close releases the runtime's storage handles. Safe to call
// multiple times — subsequent calls are no-ops.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	// M0089-0002: synchronous final checkpoint while all
	// background goroutines + the buffer pool are still attached.
	// This is the load-bearing durability boundary: by the time
	// Runtime.Close is called from main.go's defer, server.Run has
	// already returned, all client connections are gone, and all
	// in-flight transactions have committed/aborted. Any dirty
	// pages remaining in the buffer pool reflect either (a) commits
	// that landed AFTER the M0089-0003 OnStop checkpoint completed
	// (the OnStop runCancel is asynchronous — workers can keep
	// inserting until they observe the cancel), or (b) heap pages
	// that the bgwriter never had time to flush.
	//
	// Without this checkpoint:
	//  - Pool.Close calls FlushAll which pwrites dirty slots but
	//    does NOT fsync the data files (M0089-0001's SyncAll is
	//    wired into Checkpointer.runCheckpoint, not into Pool.Close).
	//  - main.go persists FSM/VM state to disk AFTER all this,
	//    capturing in-memory block references for pages whose
	//    content lives only in the OS page cache. A subsequent
	//    open finds the FSM pointing at blocks the heap file is
	//    too short to contain — surfacing as `ERROR: short read
	//    at block` on the next workload.
	//
	// Errors here are logged but do not abort Close — file handles
	// must still be released so the process can exit cleanly.
	if r.Checkpointer != nil && r.immediateShutdown {
		// Immediate shutdown (`goopg stop -mode immediate`): skip the
		// final checkpoint entirely so pg_control's State stays at
		// DB_IN_PRODUCTION. External tools (pg_resetwal/pg_rewind/
		// pg_controldata) then see an unclean cluster that needs
		// recovery, and goopg's own next start replays WAL. Mirrors
		// upstream's immediate (SIGQUIT) shutdown. (M0110-0004 / RW-002 b.)
		slog.Default().Info("immediate shutdown: skipping final checkpoint",
			"note", "pg_control left at DB_IN_PRODUCTION; recovery on next start")
	} else if r.Checkpointer != nil {
		// Use the shutdown variant so pg_control's State lands on
		// DB_SHUTDOWNED — this is the final durable checkpoint of a clean
		// shutdown, after which no further WAL is written. External tools
		// (pg_resetwal/pg_rewind/pg_controldata) read this byte to decide
		// whether the cluster needs recovery (M0110-0004 / RW-002). The
		// earlier OnStop checkpoint deliberately stays DB_IN_PRODUCTION so
		// a crash in the OnStop→Close window is still flagged as unclean.
		if err := r.Checkpointer.CheckpointShutdown(); err != nil {
			slog.Default().Warn(
				"final shutdown checkpoint failed",
				"err", err,
				"note", "data files may not be fully durable on disk",
			)
		}
	}
	// Stop the background page-writer before draining the pool (M0048-0003).
	if r.bgwriter != nil {
		r.bgwriter.Stop()
		r.bgwriter = nil
	}
	// Stop the background WAL writer loop before draining the pool so the
	// loop can't issue FlushUpTo calls that race with the final WAL close.
	if r.walwriterStop != nil {
		close(r.walwriterStop)
		r.walwriterStop = nil
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

// loadUserIndexesFromHeap recovers user-defined index catalog entries from the
// pg_index heap table (OID 2610). Called after loadUserTablesFromHeap so the
// owning tables are already registered (M0113).
//
// For each index found in pg_class (relkind='i', OID >= FirstUserOID), a
// matching pg_index row is sought to obtain indrelid and indkey (the column
// attnum vector). Column names are resolved via the already-loaded pg_attribute
// data for the parent table.
//
// Non-fatal: if pg_index is absent or malformed, this function returns an error
// and the caller falls back to WAL-replay-based index recovery.
func loadUserIndexesFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	// --- Pass 1: collect index rows from pg_class (relkind='i') ---
	classRel := storage.RelFileNode{
		DBOid:  cat.DBOID(),
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	nClassBlocks, err := mgr.NBlocks(classRel)
	if err != nil || nClassBlocks == 0 {
		return nil
	}

	page := make(storage.Page, storage.BlockSize)

	type indexClassRow struct {
		oid        uint32
		name       string
		nsp        uint32
		relOptions string
	}
	var indexRows []indexClassRow

	for blk := storage.BlockNumber(0); blk < nClassBlocks; blk++ {
		if err := mgr.ReadBlock(classRel, blk, page); err != nil {
			continue
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
			if ht.Header.Xmin == storage.InvalidTransactionID || ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			if clog != nil && clog.GetStatus(ht.Header.Xmin) == mvcc.TxnStatusAborted {
				continue
			}
			row, err := catalog.DecodePGClassPhysicalRow(ht.Data)
			if err != nil {
				continue
			}
			if row.RelKind == "i" && row.OID >= catalog.FirstUserOID {
				// reloptions (attnum 33) is a varlena column past the fixed-offset
				// prefix DecodePGClassPhysicalRow decodes, same gap
				// loadUserTablesFromHeap works around for tables/views — re-decode
				// the full PG18-canonical row with the general PG-tuple decoder to
				// recover an index's fillfactor/fastupdate/etc. (M0119-0004
				// index-reloptions follow-up). Best-effort: a decode failure here
				// just leaves relOptions empty.
				relOptions := ""
				natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
				cols := executor.PGClassColumnsPG18()
				decoded := make(executor.Row, len(cols))
				if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr == nil {
					const reloptionsOrdinal = 32
					if reloptionsOrdinal < len(decoded) && !decoded[reloptionsOrdinal].IsNull() {
						relOptions = decoded[reloptionsOrdinal].StringValue()
					}
				}
				indexRows = append(indexRows, indexClassRow{oid: row.OID, name: row.RelName, nsp: row.RelNamespace, relOptions: relOptions})
			}
		}
	}
	if len(indexRows) == 0 {
		return nil
	}

	// --- Pass 2: scan pg_index for matching rows ---
	pgIndexRel := storage.RelFileNode{
		DBOid:  cat.DBOID(),
		RelOid: catalog.IndexRelationId,
		Fork:   storage.MainFork,
	}
	nIndexBlocks, err := mgr.NBlocks(pgIndexRel)
	if err != nil || nIndexBlocks == 0 {
		return nil
	}

	type recoveredIndex struct {
		indexRelid       uint32
		indRelid         uint32
		indKey           []int16
		indNKeyAtts      int16
		indOption        []int16
		isUnique         bool
		isPrimary        bool
		nullsNotDistinct bool
		hasPred          bool
		predText         string
	}
	byIndexRelid := make(map[uint32]recoveredIndex, len(indexRows))

	for blk := storage.BlockNumber(0); blk < nIndexBlocks; blk++ {
		if err := mgr.ReadBlock(pgIndexRel, blk, page); err != nil {
			continue
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
			if ht.Header.Xmin == storage.InvalidTransactionID || ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			if clog != nil && clog.GetStatus(ht.Header.Xmin) == mvcc.TxnStatusAborted {
				continue
			}
			row, err := catalog.DecodePGIndexPhysicalRow(ht.Data)
			if err != nil {
				continue
			}
			if row.IndexRelid >= catalog.FirstUserOID {
				byIndexRelid[row.IndexRelid] = recoveredIndex{
					indexRelid:       row.IndexRelid,
					indRelid:         row.IndRelid,
					indKey:           row.IndKey,
					indNKeyAtts:      row.IndNKeyAtts,
					indOption:        row.IndOption,
					isUnique:         row.IndIsUnique,
					isPrimary:        row.IndIsPrimary,
					nullsNotDistinct: row.IndNullsNotDistinct,
					hasPred:          row.IndHasPred,
					predText:         row.IndPred,
				}
			}
		}
	}

	// --- Pass 3: for each index row, resolve column names and register ---
	for _, ir := range indexRows {
		pgIdx, ok := byIndexRelid[ir.oid]
		if !ok {
			// No pg_index row yet — this cluster predates M0113; WAL fallback handles it.
			continue
		}
		tbl, ok := cat.LookupTableByOID(pgIdx.indRelid)
		if !ok {
			continue
		}
		// Map attnum → column name, carrying each key column's ASC/DESC +
		// NULLS FIRST/LAST ordering (indoption, M0122-0006) along in lockstep
		// so a filtered-out attnum doesn't desynchronize the two slices.
		// indKey holds key columns first, then INCLUDE columns
		// (indNKeyAtts splits the two — M0122-0006 follow-up 2 of 2); indoption
		// only covers the key-column prefix, matching real PG.
		nKeyAtts := int(pgIdx.indNKeyAtts)
		if nKeyAtts < 0 || nKeyAtts > len(pgIdx.indKey) {
			nKeyAtts = len(pgIdx.indKey)
		}
		colNames := make([]string, 0, nKeyAtts)
		colDescending := make([]bool, 0, nKeyAtts)
		colNullsFirst := make([]bool, 0, nKeyAtts)
		for i, attnum := range pgIdx.indKey[:nKeyAtts] {
			if attnum <= 0 || int(attnum) > len(tbl.Columns) {
				continue
			}
			colNames = append(colNames, tbl.Columns[attnum-1].Name)
			var opt int16
			if i < len(pgIdx.indOption) {
				opt = pgIdx.indOption[i]
			}
			colDescending = append(colDescending, opt&0x0001 != 0)
			colNullsFirst = append(colNullsFirst, opt&0x0002 != 0)
		}
		if len(colNames) == 0 {
			continue
		}
		var includeColNames []string
		for _, attnum := range pgIdx.indKey[nKeyAtts:] {
			if attnum <= 0 || int(attnum) > len(tbl.Columns) {
				continue
			}
			includeColNames = append(includeColNames, tbl.Columns[attnum-1].Name)
		}
		schema := ""
		if ir.nsp == catalog.PGCatalogNamespaceOID {
			schema = "pg_catalog"
		} else if ir.nsp == catalog.PublicNamespaceOID {
			schema = "public"
		} else if name := cat.SchemaNameForOID(ir.nsp); name != "" {
			// M0110-0003: reverse-map a user schema's namespace OID (sibling of
			// the loadUserTablesFromHeap path) so a user-schema index reloads in
			// its schema.
			schema = name
		}
		// ColOpClasses/ColCollations are not yet decoded from the pg_index heap
		// row here (indclass/indcollation only ever carry zero OIDs — real
		// per-column opclass/collation OID resolution is still unimplemented,
		// even on the live/non-restart path) — a known, separate residual (see
		// .ralph/deferral_ledger.md), so this heap-recovery driver always
		// passes their zero values. HasPredicate/PredicateString/
		// IncludeColumns/NullsNotDistinct ARE now decoded (M0122-0006
		// follow-up 2 of 2). Fillfactor/DeduplicateItems are restored via the
		// separate ApplyIndexReloptions call just below (reads
		// pg_class.reloptions text), not through this call.
		cat.RegisterIndexDuringRecovery(schema, ir.name, pgIdx.indRelid, colNames, pgIdx.isUnique, "btree", pgIdx.isPrimary, ir.oid, colDescending, colNullsFirst, pgIdx.hasPred, pgIdx.predText, includeColNames, nil, nil, 0, nil, pgIdx.nullsNotDistinct)
		// Restore fillfactor/deduplicate_items/fastupdate/gin_pending_list_limit/
		// pages_per_range/autosummarize from the heap-persisted pg_class row —
		// without this they silently revert to defaults across every restart
		// (M0119-0004 index-reloptions follow-up, sibling of loadUserTablesFromHeap's
		// catalog.ApplyTableReloptions call for tables/views).
		if ir.relOptions != "" {
			if newIdx, ok := cat.LookupIndexByOID(ir.oid); ok {
				catalog.ApplyIndexReloptions(newIdx, ir.relOptions)
			}
		}
	}
	return nil
}

// loadStatisticsFromHeap restores per-column planner statistics from the
// pg_statistic heap table (OID 2619). Called during startup after user tables
// are loaded (M0112). Non-fatal: a missing or corrupt pg_statistic file is
// silently ignored; the planner falls back to hard-coded defaults until the
// next ANALYZE run.
func loadStatisticsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	statRel := storage.RelFileNode{
		DBOid:  cat.DBOID(),
		RelOid: catalog.StatisticRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := mgr.NBlocks(statRel)
	if err != nil || nBlocks == 0 {
		return nil
	}

	page := make(storage.Page, storage.BlockSize)

	// Collect the most recent live stat row per (starelid, staattnum).
	// Multiple rows can exist if ANALYZE was run multiple times; the last
	// live tuple wins (highest slot number in the highest block).
	type statKey struct {
		relid  uint32
		attnum int16
	}
	statMap := make(map[statKey]catalog.PGStatisticRow)

	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		if err := mgr.ReadBlock(statRel, blk, page); err != nil {
			continue
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
			if ht.Header.Xmin == storage.InvalidTransactionID || ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			if clog != nil && clog.GetStatus(ht.Header.Xmin) == mvcc.TxnStatusAborted {
				continue
			}
			row, err := catalog.DecodePGStatisticPhysicalRow(ht.Data, ht.Bitmap)
			if err != nil {
				continue
			}
			if row.StaRelid >= catalog.FirstUserOID && row.StaAttNum > 0 {
				statMap[statKey{relid: row.StaRelid, attnum: row.StaAttNum}] = row
			}
		}
	}

	if len(statMap) == 0 {
		return nil
	}

	// Group rows by table OID and apply stats to the in-memory catalog.
	byRelid := make(map[uint32][]catalog.PGStatisticRow)
	for _, row := range statMap {
		byRelid[row.StaRelid] = append(byRelid[row.StaRelid], row)
	}

	for relid, rows := range byRelid {
		tbl, ok := cat.LookupTableByOID(relid)
		if !ok {
			continue
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].StaAttNum < rows[j].StaAttNum })

		colStats := make([]catalog.ColumnStats, len(tbl.Columns))
		for _, sr := range rows {
			idx := int(sr.StaAttNum) - 1
			if idx < 0 || idx >= len(colStats) {
				continue
			}
			var distinctVal int64
			if sr.StaDistinct > 0 {
				distinctVal = int64(sr.StaDistinct)
			}
			cs := catalog.ColumnStats{
				NDistinct: distinctVal,
				NullFrac:  float64(sr.StaNullFrac),
			}
			// MCV
			if len(sr.MCVFreqs) > 0 && len(sr.MCVValues) > 0 {
				n := len(sr.MCVFreqs)
				if len(sr.MCVValues) < n {
					n = len(sr.MCVValues)
				}
				cs.MCV = make([]catalog.MCVEntry, n)
				for i := 0; i < n; i++ {
					cs.MCV[i] = catalog.MCVEntry{Value: sr.MCVValues[i], Frequency: float64(sr.MCVFreqs[i])}
				}
			}
			// Histogram
			if len(sr.HistBounds) > 0 {
				cs.Histogram = append([]string(nil), sr.HistBounds...)
			}
			colStats[idx] = cs
		}
		stats := &catalog.TableStats{Columns: colStats}
		cat.SetTableStats(tbl, stats)
	}
	return nil
}
