package storage

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/runtimeshim"
	"github.com/goopg/goopg/internal/stats"
)

// Slot state bit layout (single 64-bit atomic word):
//
//	bits  0..21  pinCount    (22 bits → 4 M concurrent pins max)
//	bits 22..29  usageCount  (8 bits, saturating clock-sweep counter)
//	bit      30  dirty       (page has been mutated since last flush)
//	bit      31  valid       (slot holds a valid page for its tag)
//	bit      32  ioInflight  (a disk read is in progress for this slot)
//	bits 33..47  gen         (15 bits, bumped on eviction; ABA guard)
//	bits 48..63  reserved
const (
	slotPinMask    = uint64((1 << 22) - 1) // bits 0..21
	slotUsageShift = 22
	slotUsageMask  = uint64(((1 << 8) - 1) << slotUsageShift)
	slotDirtyBit   = uint64(1 << 30)
	slotValidBit   = uint64(1 << 31)
	slotIOBit      = uint64(1 << 32)
	slotGenShift   = 33
	slotGenMask    = uint64(((1 << 15) - 1) << slotGenShift)
	maxUsageCount  = 5
	maxPinCount    = (1 << 22) - 1
)

// slotState helpers — extract fields from a packed state word.
func statePin(st uint64) uint64   { return st & slotPinMask }
func stateUsage(st uint64) uint64 { return (st & slotUsageMask) >> slotUsageShift }
func stateGen(st uint64) uint32   { return uint32((st & slotGenMask) >> slotGenShift) }
func stateValid(st uint64) bool   { return st&slotValidBit != 0 }
func stateDirty(st uint64) bool   { return st&slotDirtyBit != 0 }
func stateIO(st uint64) bool      { return st&slotIOBit != 0 }

// Slot is one buffer-pool entry holding one Page. Callers receive
// *Slot from Pin and return it via Unpin. Direct field access is
// permitted under contentMu (held implicitly by the pin guarantee
// for single-writer flows).
type Slot struct {
	page Page // alias into the arena

	// idx is this slot's fixed index into Pool.slots, set once in
	// NewPool. Lets MarkDirty* (which only receive a *Slot) address
	// Pool.slotEvents without pointer arithmetic. See DebugTraceSlotEvents.
	idx int32

	// Tag identifies the (rel, fork, block) the slot currently holds.
	// Written only while ioInflight is set in state; readable once valid.
	tag BufferTag

	// state packs pinCount, usageCount, dirty, valid, ioInflight, gen
	// into a single 64-bit atomic word for lock-free Pin/Unpin.
	state atomic.Uint64

	// nativeImageLSN is the end-LSN of the last NATIVE full-page image
	// (RecordKindPageImage via logFPI, or the multi-page-image records the
	// MarkDirtyWithLSN* callers log) emitted for the page occupying this
	// slot; 0 on load/reuse. The FPI decision keys on THIS token — not on
	// pd_lsn — because pd_lsn is stamped by BOTH record families: a
	// canonical record's stamp would otherwise satisfy the test and
	// suppress the native image the native-family replay depends on (the
	// cross-family poisoning class rejected as F1 in the C1 design review;
	// rediscovered as a live S2 regression by the S3a crash-sim tests).
	// Slot-keyed: eviction/reload re-arms conservatively (extra image).
	nativeImageLSN atomic.Uint64

	// contentMu guards the Page bytes for read/write. The pool does
	// not hold any global lock while this is taken.
	contentMu sync.RWMutex
}

// Page returns the Page-typed view of the slot's storage. The caller
// must hold a pin on the slot.
func (s *Slot) Page() Page { return s.page }

// Tag returns the (rel, fork, block) the slot currently holds.
func (s *Slot) Tag() BufferTag { return s.tag }

// Lock acquires the page's exclusive content lock.
func (s *Slot) Lock()    { s.contentMu.Lock() }
func (s *Slot) Unlock()  { s.contentMu.Unlock() }
func (s *Slot) RLock()   { s.contentMu.RLock() }
func (s *Slot) RUnlock() { s.contentMu.RUnlock() }

// valid is a read helper for tests/internal callers.
func (s *Slot) isValid() bool { return stateValid(s.state.Load()) }

// dirty is a read helper for tests/internal callers.
func (s *Slot) isDirty() bool { return stateDirty(s.state.Load()) }

// getPinCount returns the current pin count.
func (s *Slot) getPinCount() int32 { return int32(statePin(s.state.Load())) }

// Pool is the buffer manager. It is goroutine-safe.
type Pool struct {
	mgr   *Manager
	arena *arena
	slots []Slot
	wal   WALFlusher
	// logFPI emits a full-page-image WAL record and returns the
	// record's end LSN. nil disables FPI emission.
	logFPI func(rel RelFileNode, blk BlockNumber, page Page) (LSN, error)
	// logBtreeSplit emits an atomic B-tree split WAL record.
	logBtreeSplit LogBtreeSplitFunc
	// logHeapInsert emits a logical heap-insert WAL record.
	logHeapInsert LogHeapInsertFunc
	// logBtreeInsert emits a logical B-tree non-split insert WAL record.
	logBtreeInsert LogBtreeInsertFunc
	// logHeapDelete emits a logical heap-delete WAL record.
	logHeapDelete LogHeapDeleteFunc
	// logHeapLock emits a logical heap-lock WAL record.
	logHeapLock LogHeapLockFunc
	// logHeapVacuum emits a logical heap-vacuum WAL record.
	logHeapVacuum LogHeapVacuumFunc
	// logBtreeVacuum emits a logical btree-vacuum WAL record.
	logBtreeVacuum LogBtreeVacuumFunc
	// M0079-0003 logical records.
	logBtreeUnlinkPage       LogBtreeUnlinkPageFunc
	logBtreeNewRoot          LogBtreeNewRootFunc
	logBtreeMarkPageHalfDead LogBtreeMarkPageHalfDeadFunc
	// M0080-0001 logical record for VACUUM FREEZE.
	logHeapFreeze LogHeapFreezeFunc
	// logHeapHotUpdate emits an atomic HOT-update WAL record.
	logHeapHotUpdate LogHeapHotUpdateFunc
	// logHeapPruneOpt emits an opportunistic page-pruning WAL record.
	logHeapPruneOpt LogHeapPruneOptFunc
	// logSmgrCreate emits a relation-file creation WAL record.
	logSmgrCreate func(rel RelFileNode) error
	// logChangeRecord emits a pre-encoded change record.
	logChangeRecord func(payload []byte) (LSN, error)
	// fullPageWrites gates FPI emission.
	fullPageWrites atomic.Bool

	// redoRecPtr is the published checkpoint redo pointer gating FPI
	// emission (see PublishRedoRecPtr). Zero until seeded/published.
	redoRecPtr atomic.Uint64

	// fpiPublishMu interlocks redo publication with in-flight FPI
	// decisions (the goopg analog of PG's fpw_lsn recheck under the WAL
	// insert locks, xlog.c XLogInsertRecord). Writers hold RLock across
	// needsImage -> record append inside the MarkDirty* variants; the
	// checkpointer's PublishRedoBarrier takes Lock, so by the time the new
	// redo is sampled+stored every straddling writer has completed its
	// append — its record LSN is below the sampled frontier and therefore
	// below the new redo, closing the decide->append window (adversarial
	// review F1 of perf-optimize3-dash S2).
	fpiPublishMu sync.RWMutex
	// logger surfaces non-fatal FPI failures.
	logger *slog.Logger

	// prefetchEnabled gates Pool.Prefetch.
	prefetchEnabled atomic.Bool

	// asyncFlushBatchSize controls FlushAllPaced batching.
	asyncFlushBatchSize atomic.Int32

	// bm is the lock-free buffer-tag → slot-index hash table.
	// Fast path (cache hit): lock-free Lookup + CAS on slot state.
	// Slow path (cache miss / IO-inflight wait): must hold pinMu.
	bm *bufmap

	// pinMu serialises the slow path: cache-miss eviction + insert.
	// The fast path (pin of a valid cached slot) never acquires this.
	// pinMu also serialises waits on IO-in-progress so only the right
	// goroutines get woken and re-check the correct slot.
	pinMu sync.Mutex

	// clockHand is the atomic clock-sweep hand for victim selection.
	clockHand atomic.Int64

	// sharedHitCount / sharedReadCount tally shared-buffer cache hits vs.
	// disk reads across the pool's lifetime, mirroring PgBufferUsage's
	// shared_blks_hit / shared_blks_read (postgres/src/backend/utils/
	// misc/pgstat_wal.c neighbour, actually tracked in executor/instrument.c
	// BufferUsageAccumDiff). EXPLAIN (ANALYZE, BUFFERS) reads these via
	// BufferCounters() and diffs a before/after snapshot per plan node
	// (see internal/executor/instrument.go). Only Pin()'s two decision
	// points increment these; PinNew (new-block allocation) and the rare
	// tryPinSlot race-recovery calls inside pinLoad/PinNew are not counted
	// here (deferred — see .ralph/deferral_ledger.md M0122-0003 BUFFERS row).
	sharedHitCount  atomic.Int64
	sharedReadCount atomic.Int64

	// sharedDirtiedCount / sharedWrittenCount extend the pair above to
	// mirror PgBufferUsage's shared_blks_dirtied / shared_blks_written.
	// dirtiedCount increments exactly once per clean->dirty transition,
	// at every MarkDirty* call site (mirrors bufmgr.c's MarkBufferDirty /
	// MarkBufferDirtyHint: "if the buffer was not dirty already, do
	// vacuum accounting"). writtenCount increments only when evictVictim
	// flushes a dirty victim to make room for this pool's own Pin/PinNew
	// (the FlushBuffer() call site bufmgr.c increments from) — NOT the
	// bgwriter's WriteDirtyPages or the checkpointer's FlushAll/
	// FlushAllPaced, since upstream's pgBufferUsage is per-backend and
	// those run as separate processes with their own counter instance;
	// counting them here into this pool-wide counter would attribute
	// background/checkpoint IO to whichever query happens to be running.
	sharedDirtiedCount atomic.Int64
	sharedWrittenCount atomic.Int64

	// sharedReadTimeNanos accumulates real wall-clock time spent in the
	// pinLoad disk read (the exact span OnPinWait/OnPinDone bracket),
	// backing pg_stat_io's read_time / EXPLAIN's I/O Timings columns
	// (M0122-0003 track_io_timing follow-up: "actual per-wait-event
	// timing collection"). Only accumulated when the reading backend has
	// track_io_timing on — see initdb.Open's OnPinDone wiring, which only
	// calls AddReadTimeNanos when ActivityRegistry.LookupTrackedGoroutine
	// reports ok (itself gated on the backend's flag), matching upstream's
	// "these will be zero if track_io_timing is not enabled" semantics
	// without this package needing to know about the activity registry.
	sharedReadTimeNanos atomic.Int64

	// sharedWriteTimeNanos is sharedReadTimeNanos's write-side sibling: real
	// wall-clock time spent in evictVictim's dirty-victim flushSlot call
	// (the exact span the OnFlushWait/OnFlushDone bracket covers), backing
	// pg_stat_io's write_time column. Only accumulated when the evicting
	// backend has track_io_timing on (see initdb.Open's OnFlushDone wiring,
	// which mirrors OnPinDone's AddReadTimeNanos call exactly).
	sharedWriteTimeNanos atomic.Int64

	// sharedEvictionCount / sharedExtendCount back pg_stat_io's evictions /
	// extends columns (M0122-0003 follow-up: "the remaining five op
	// counters"). evictionCount increments once per real victim eviction
	// (evictVictim, only when a valid tag is actually displaced — an empty
	// slot is not an eviction), regardless of whether the victim was dirty,
	// mirroring pgBufferUsage's shared_blks_evicted accounting in bufmgr.c's
	// StrategyGetBuffer/BufferAlloc. extendCount increments once per
	// successful PinNew relation extension (the pool's sole smgr Extend
	// call site), mirroring shared_blks_extend.
	sharedEvictionCount atomic.Int64
	sharedExtendCount   atomic.Int64

	// sharedExtendTimeNanos is sharedWriteTimeNanos's relation-extension
	// sibling: real wall-clock time spent in PinNew's mgr.Extend call (the
	// exact span the OnExtendWait/OnExtendDone bracket covers), backing
	// pg_stat_io's extend_time column. Only accumulated when the extending
	// backend has track_io_timing on (see initdb.Open's OnExtendDone
	// wiring, which mirrors OnFlushDone's AddWriteTimeNanos call exactly).
	sharedExtendTimeNanos atomic.Int64

	// checkpointFlushAfterBlocks / bgwriterFlushAfterBlocks /
	// backendFlushAfterBlocks mirror upstream's checkpoint_flush_after /
	// bgwriter_flush_after / backend_flush_after GUCs (in BLCKSZ-page
	// units; 0 disables writeback for that context). Defaulted in NewPool
	// to upstream's DEFAULT_CHECKPOINT_FLUSH_AFTER/DEFAULT_BGWRITER_FLUSH_AFTER/
	// DEFAULT_BACKEND_FLUSH_AFTER (32/64/0) so a Pool built without server
	// wiring (e.g. tests) still exhibits real Linux behaviour; overridden
	// from the live GUC values at startup (initdb.Open).
	// backendFlushAfterBlocks specifically is the process-wide fallback for
	// accountBackendWrite — a per-session `SET backend_flush_after` value
	// (upstream's GUC is PGC_USERSET) takes precedence via
	// BackendFlushAfterOverride when the calling backend has one.
	checkpointFlushAfterBlocks atomic.Int32
	bgwriterFlushAfterBlocks   atomic.Int32
	backendFlushAfterBlocks    atomic.Int32

	// pendingCheckpointFlushBlocks / pendingBgwriterFlushBlocks /
	// pendingBackendFlushBlocks count pages written by each context since
	// its last writeback hint, mirroring upstream's WritebackContext
	// pending-block accounting (bufmgr.c's IssuePendingWritebacks),
	// simplified to a single running counter rather than upstream's
	// per-relation-segment coalesced range list (see writeback.go).
	pendingCheckpointFlushBlocks atomic.Int64
	pendingBgwriterFlushBlocks   atomic.Int64
	pendingBackendFlushBlocks    atomic.Int64

	// sharedCheckpointWritebackCount / sharedBgwriterWritebackCount /
	// sharedBackendWritebackCount back pg_stat_io's writeback column for
	// the (checkpointer|background writer|client backend, relation,
	// normal) rows respectively — a real sync_file_range(2) hint issued
	// once the corresponding *FlushAfterBlocks threshold is crossed (see
	// writeback.go's accountCheckpointerWrite/accountBgwriterWrite/
	// accountBackendWrite). *WritebackTimeNanos are their real wall-clock
	// time siblings (writeback_time), gated on track_io_timing exactly
	// like sharedWriteTimeNanos above (backend: OnBackendWritebackDone
	// gated per-goroutine via ActivityRegistry.LookupTrackedGoroutine;
	// bgwriter/checkpointer: gated on the boot-time track_io_timing value,
	// since those are singleton background goroutines with no per-session
	// SET semantics to look up — see initdb.Open's wiring).
	sharedCheckpointWritebackCount     atomic.Int64
	sharedCheckpointWritebackTimeNanos atomic.Int64
	sharedBgwriterWritebackCount       atomic.Int64
	sharedBgwriterWritebackTimeNanos   atomic.Int64
	sharedBackendWritebackCount        atomic.Int64
	sharedBackendWritebackTimeNanos    atomic.Int64

	// sharedCheckpointWrittenCount / sharedBgwriterWrittenCount back
	// pg_stat_io's writes column for the (checkpointer|background writer,
	// relation, normal) rows — the background-writer/checkpointer sibling
	// of sharedWrittenCount above, which upstream's per-backend
	// pgBufferUsage deliberately excludes (M0122-0003 writeback follow-up,
	// "background writer / checkpointer rows' own writes/write_bytes/
	// write_time cells are still an honest 0" simplification, now closed).
	// Incremented once per real dirty-slot write each context performs:
	// checkpointer in flushBatch (FlushAllPaced, also driven by Pool.Close's
	// final shutdown flush — upstream attributes a shutdown checkpoint's
	// writes to the checkpointer too), bgwriter in WriteDirtyPages.
	// *WriteTimeNanos are their real wall-clock time siblings (write_time),
	// gated on track_io_timing exactly like the writeback counters above.
	sharedCheckpointWrittenCount   atomic.Int64
	sharedCheckpointWriteTimeNanos atomic.Int64
	sharedBgwriterWrittenCount     atomic.Int64
	sharedBgwriterWriteTimeNanos   atomic.Int64

	// OnCheckpointerWritebackWait/Done, OnBgwriterWritebackWait/Done, and
	// OnBackendWritebackWait/Done bracket each context's real
	// SyncFileRangeHint call (mirrors OnFlushWait/OnFlushDone), one
	// distinct pair per context for the same per-backend-type attribution
	// reason OnExtendWait/OnExtendDone is distinct from
	// Manager.OnExtendWait/OnExtendDone.
	OnCheckpointerWritebackWait func()
	OnCheckpointerWritebackDone func()
	OnBgwriterWritebackWait     func()
	OnBgwriterWritebackDone     func()
	OnBackendWritebackWait      func()
	OnBackendWritebackDone      func()

	// OnCheckpointerWriteWait/Done and OnBgwriterWriteWait/Done bracket
	// each context's real dirty-page write (mirrors OnFlushWait/OnFlushDone,
	// the backend-eviction analogue), backing write_time for the
	// (checkpointer|background writer, relation, normal) rows.
	OnCheckpointerWriteWait func()
	OnCheckpointerWriteDone func()
	OnBgwriterWriteWait     func()
	OnBgwriterWriteDone     func()

	// BackendFlushAfterOverride, when set, resolves the calling backend's
	// own per-session backend_flush_after value (upstream's GUC is
	// PGC_USERSET — independently settable per session via `SET
	// backend_flush_after`), returning ok=false when the caller isn't a
	// tracked backend (e.g. the bgwriter/checkpointer goroutines, or a
	// Pool used without server wiring in tests) so accountBackendWrite
	// falls back to backendFlushAfterBlocks (initdb.Open's wiring: see
	// deferral ledger's "backend_flush_after applied process-wide, not
	// per-session" simplification, now closed).
	BackendFlushAfterOverride func() (int32, bool)

	// bgwriterHand is the bgwriter's independent scan cursor.
	// Protected by bgwriterMu.
	bgwriterMu   sync.Mutex
	bgwriterHand int

	// compactMu serialises the rare tombstone-compaction rebuild.
	compactMu sync.Mutex

	// tombstones counts tombstone entries in bm; used to trigger compaction.
	tombstones atomic.Int64

	// slotSema is a parallel []uint32 array (len == len(slots)) used as
	// per-slot runtime semaphores for IO-in-progress waits.
	// A goroutine waiting for slot i's IO calls runtimeshim.SemaAcquire(&slotSema[i]).
	// The loader releases slotWaiters[i] times when IO completes.
	slotSema []uint32

	// slotWaiters tracks the number of goroutines currently waiting on
	// each slot's IO via SemaAcquire. Written under pinMu so the loader
	// can read an exact count before releasing the sema.
	slotWaiters []atomic.Int32

	// OnPinWait is called when Pool.Pin issues a disk read.
	OnPinWait func()

	// OnPinDone is called after the disk read finishes.
	OnPinDone func()

	// OnFlushWait is called when evictVictim is about to flush a dirty
	// victim's page to disk (write_time's OnPinWait analogue).
	OnFlushWait func()

	// OnFlushDone is called after that flush finishes.
	OnFlushDone func()

	// OnFlushSnapshot is an M-NIGHTLY investigation aid
	// (AI-20260708-064334-001, 8th loop; re-anchored post-write in the 9th
	// loop), nil by default (zero cost when unset). When set, flushSlot
	// calls it immediately AFTER the WriteBlock call succeeds, with the
	// exact tag and bytes just durably written to disk — letting a caller
	// (e.g. btree.BTree.RecordFlushSnapshot) capture "what a dirty-page
	// flush actually wrote" so it can be compared against the last
	// known-good in-memory snapshot recorded for the same block by an
	// independent, higher-layer log. Post-write placement (not pre-write,
	// as the 8th loop originally had it) matters for Seq-ordered
	// cross-referencing against OnBlockReload, which fires post-read: both
	// hooks now log "operation durably completed" time, so Seq comparisons
	// between the two logs reflect real IO ordering instead of "intent to
	// write" vs "read completed". Fires for every flushSlot call
	// regardless of caller (evictVictim's dirty branch, WriteDirtyPages,
	// checkpoint flushBatch), not just eviction-triggered flushes. Remove
	// once the M-NIGHTLY root cause is fixed.
	OnFlushSnapshot func(tag BufferTag, page Page)

	// OnBlockReload is an M-NIGHTLY investigation aid
	// (AI-20260708-064334-001, 9th loop), nil by default (zero cost when
	// unset). When set, pinLoad's cache-miss branch calls it immediately
	// after Manager.ReadBlock completes successfully, while s.contentMu is
	// still held and before the slot is published for Pin — letting a
	// caller (e.g. btree.BTree.RecordReloadSnapshot) capture "what bytes a
	// disk reload actually served" so it can be compared against the last
	// flush-snapshot recorded for the same block by OnFlushSnapshot. Remove
	// once the M-NIGHTLY root cause is fixed.
	OnBlockReload func(tag BufferTag, page Page)

	// OnBufmapInsert/OnBufmapDelete are M-NIGHTLY investigation aids
	// (AI-20260708-064334-001, 14th loop), nil by default (zero cost when
	// unset). Every one of this Pool's bm.Insert/bm.Delete calls is routed
	// through bmInsert/bmDelete below, which invoke these hooks while still
	// holding bufmap's own internal mu — so, unlike OnFlushSnapshot/
	// OnBlockReload (whose Seq is stamped by a later, separately-locked
	// call into btree's insertLogMu and can therefore drift from true
	// completion order under scheduling jitter, per the 11th loop's
	// finding), a caller's hook here observes bufmap mutations in their
	// TRUE serialization order relative to every other Insert/Delete on
	// the same bufmap. Exists to let btree.BTree record a per-tag
	// ownership timeline and directly prove or refute whether two
	// different slots ever simultaneously believe they own the same
	// BufferTag (the one invariant never yet checked with live
	// instrumentation across 13 prior loops on this task — see
	// .ralph/fix_plan.md's M-NIGHTLY / pgbench/nightly-reopen-20260708
	// entry). Remove once the M-NIGHTLY root cause is fixed.
	OnBufmapInsert func(tag BufferTag, slotIdx int32, gen uint32, ok bool)
	OnBufmapDelete func(tag BufferTag, slotIdx int32)

	// OnExtendWait is called when PinNew is about to extend rel via the
	// pool's sole smgr Extend call (extend_time's OnFlushWait analogue).
	// Deliberately a distinct pair from storage.Manager's own
	// OnExtendWait/OnExtendDone (smgr.go) rather than a reuse: the
	// Manager-level hooks fire for every Extend/ExtendBatch call
	// regardless of caller, while this pool-level pair — like
	// OnFlushWait/OnFlushDone versus Manager.OnWriteWait/OnWriteDone —
	// exists to attribute IO time to pg_stat_io's per-backend-type
	// extend_time column, which upstream's pgBufferUsage tracks
	// per-backend, not per-smgr-call.
	OnExtendWait func()

	// OnExtendDone is called after that extend finishes.
	OnExtendDone func()

	// OnBufferIOWait is called when a goroutine waits for an in-flight read.
	OnBufferIOWait func()

	// OnFlushAll is called at the start of FlushAll/FlushAllPaced.
	OnFlushAll func()

	// Dirty-victim instrumentation (M0048-0003).
	// Per-P sharded counters (no cache-line bouncing under concurrent
	// foreground eviction); cold-path Sum reads only feed the bgwriter
	// DoD ratio and ResetVictimStats.
	dirtyVictimCount stats.Counter
	totalVictimCount stats.Counter

	// DebugValidateCleanEvictions is an M-NIGHTLY investigation aid
	// (AI-20260708-064334-001), off by default (zero cost when false).
	// When true, every "clean" (non-dirty) eviction re-reads its block
	// from disk and compares it byte-for-byte against the in-memory
	// page, logging an Error on any mismatch — a mismatch is definitive
	// proof that the dirty bit was wrong at eviction time (the eviction
	// silently discarded unflushed content). See
	// TestVerifyBtreeEngineSilentOnRealConcurrentContended in
	// internal/amcheck, which sets this to reproduce the bug in ~1s.
	// Remove this field once the eviction-race root cause is fixed.
	DebugValidateCleanEvictions bool

	// DebugTraceSlotEvents is an M-NIGHTLY investigation aid
	// (AI-20260708-064334-001), off by default (zero cost when false: the
	// only always-paid cost is the per-slot ring buffer allocation in
	// NewPool). When true, every dirty-mark call and every claim/evict/
	// publish/release state transition is appended to a bounded per-slot
	// ring (slotEvents), so a debugValidateCleanEviction mismatch can dump
	// the exact sequence of events that led to a slot's dirty bit reading
	// false when unflushed content says it should have read true. Remove
	// alongside DebugValidateCleanEvictions once the root cause is fixed.
	DebugTraceSlotEvents bool
	slotEvents           []slotEventRing
	slotEventSeq         atomic.Uint64
}

// slotEventKind identifies which state-transition site produced a
// slotEvent. See DebugTraceSlotEvents.
type slotEventKind uint8

const (
	evMarkDirty slotEventKind = iota
	evMarkDirtyWithLSNLocked
	evMarkDirtyChangeRecord
	evClaimVictim
	evEvictVictimClean
	evEvictVictimDirty
	evPinLoadPublish
	evPinNewPublish
	evReleaseVictimSlot
)

func (k slotEventKind) String() string {
	switch k {
	case evMarkDirty:
		return "MarkDirty"
	case evMarkDirtyWithLSNLocked:
		return "MarkDirtyWithLSNLocked"
	case evMarkDirtyChangeRecord:
		return "MarkDirtyChangeRecord"
	case evClaimVictim:
		return "claimVictim"
	case evEvictVictimClean:
		return "evictVictim(clean)"
	case evEvictVictimDirty:
		return "evictVictim(dirty)"
	case evPinLoadPublish:
		return "pinLoad-publish"
	case evPinNewPublish:
		return "PinNew-publish"
	case evReleaseVictimSlot:
		return "releaseVictimSlot"
	default:
		return "unknown"
	}
}

// slotEvent is one recorded state transition. See DebugTraceSlotEvents.
type slotEvent struct {
	seq      uint64
	kind     slotEventKind
	tag      BufferTag
	oldState uint64
	newState uint64
}

// slotEventRingSize bounds memory and dump size; large enough to cover
// several full occupancy cycles of a slot under heavy churn.
const slotEventRingSize = 64

// slotEventRing is a lock-free (single monotonic-counter) ring buffer of
// the most recent slotEventRingSize events for one slot. Writers use
// pos.Add(1)-1 as their slot index; a torn read racing a wraparound write
// is acceptable for a best-effort debug dump.
type slotEventRing struct {
	events [slotEventRingSize]slotEvent
	pos    atomic.Uint64
}

// traceSlotEvent records one state transition for slotIdx when
// DebugTraceSlotEvents is enabled; a no-op otherwise.
func (p *Pool) traceSlotEvent(slotIdx int32, kind slotEventKind, tag BufferTag, oldSt, newSt uint64) {
	if !p.DebugTraceSlotEvents {
		return
	}
	seq := p.slotEventSeq.Add(1)
	ring := &p.slotEvents[slotIdx]
	i := ring.pos.Add(1) - 1
	ring.events[i%slotEventRingSize] = slotEvent{seq: seq, kind: kind, tag: tag, oldState: oldSt, newState: newSt}
}

// dumpSlotEvents logs the recorded event history for slotIdx, oldest
// first. Used by debugValidateCleanEviction to explain a mismatch.
func (p *Pool) dumpSlotEvents(slotIdx int32) {
	ring := &p.slotEvents[slotIdx]
	pos := ring.pos.Load()
	start := uint64(0)
	if pos > slotEventRingSize {
		start = pos - slotEventRingSize
	}
	for i := start; i < pos; i++ {
		e := ring.events[i%slotEventRingSize]
		p.logger.Error("slot-event (M-NIGHTLY AI-20260708-064334-001)",
			"seq", e.seq, "slotIdx", slotIdx, "kind", e.kind.String(), "tag", e.tag,
			"oldState", fmt.Sprintf("%#016x", e.oldState), "newState", fmt.Sprintf("%#016x", e.newState))
	}
}

// PoolConfig controls Pool sizing.
type PoolConfig struct {
	Slots int

	// WAL, when non-nil, is asked to flush up to the page's pd_lsn
	// before any dirty page is written to data files.
	WAL WALFlusher

	// LogPageImage, when non-nil, is invoked by MarkDirty on the
	// first mutation of each page after every checkpoint to emit
	// a full-page-image WAL record.
	LogPageImage func(rel RelFileNode, blk BlockNumber, page Page) (LSN, error)

	// FullPageWrites mirrors upstream's full_page_writes GUC.
	FullPageWrites bool

	LogBtreeSplit            LogBtreeSplitFunc
	LogHeapInsert            LogHeapInsertFunc
	LogBtreeInsert           LogBtreeInsertFunc
	LogHeapDelete            LogHeapDeleteFunc
	LogHeapLock              LogHeapLockFunc
	LogHeapVacuum            LogHeapVacuumFunc
	LogBtreeVacuum           LogBtreeVacuumFunc
	LogBtreeUnlinkPage       LogBtreeUnlinkPageFunc
	LogBtreeNewRoot          LogBtreeNewRootFunc
	LogBtreeMarkPageHalfDead LogBtreeMarkPageHalfDeadFunc
	LogHeapFreeze            LogHeapFreezeFunc
	LogHeapHotUpdate         LogHeapHotUpdateFunc
	LogHeapPruneOpt          LogHeapPruneOptFunc
	LogSmgrCreate            func(rel RelFileNode) error
	LogChangeRecord          func(payload []byte) (LSN, error)

	// Logger receives FPI emission failures. nil means slog.Default().
	Logger *slog.Logger
}

// WALFlusher is the minimal WAL contract the buffer pool needs to
// enforce write-ahead ordering.
type WALFlusher interface {
	FlushUpTo(lsn uint64) error
}

// LogBtreeSplitFunc emits one atomic WAL record covering a B-tree page split.
// On a non-rightmost split sibBlk is the old right sibling whose btpo_prev is
// relinked to rightBlk and sibPage its post-relink image, so the relink is
// crash-atomic with the split; on a rightmost split sibBlk is
// InvalidBlockNumber and sibPage is nil.
type LogBtreeSplitFunc func(rel RelFileNode, leftBlk, rightBlk BlockNumber, leftPage, rightPage Page, sibBlk BlockNumber, sibPage Page) (LSN, error)

// LogHeapInsertFunc emits one logical heap-insert redo record.
type LogHeapInsertFunc func(rel RelFileNode, blk BlockNumber, lineSlot uint16, tuple []byte) (LSN, error)

// LogBtreeInsertFunc emits one logical B-tree non-split insert redo record.
type LogBtreeInsertFunc func(rel RelFileNode, blk BlockNumber, item []byte) (LSN, error)

// LogHeapDeleteFunc emits one logical heap-delete (xmax stamp) redo record.
type LogHeapDeleteFunc func(rel RelFileNode, blk BlockNumber, lineSlot uint16, xmax TransactionID, oldTuple []byte) (LSN, error)

// LogHeapLockFunc emits one row-lock WAL record.
type LogHeapLockFunc func(rel RelFileNode, blk BlockNumber, lineSlot uint16, xmax TransactionID, lockStrength uint16) (LSN, error)

// LogHeapVacuumFunc emits one logical heap-vacuum (page prune) redo record.
type LogHeapVacuumFunc func(rel RelFileNode, blk BlockNumber, deadSlots []uint16) (LSN, error)

// LogBtreeVacuumFunc emits one logical btree-vacuum redo record.
type LogBtreeVacuumFunc func(rel RelFileNode, blk BlockNumber, keptItems [][]byte, opaqueFlags uint16) (LSN, error)

// BtreeUnlinkPageRequest collects the 4-page mutation control info.
type BtreeUnlinkPageRequest struct {
	LeafBlk          BlockNumber
	LeafFlagsAfter   uint16
	HasLeftSib       bool
	LeftSibBlk       BlockNumber
	LeftSibNewNext   BlockNumber
	HasRightSib      bool
	RightSibBlk      BlockNumber
	RightSibNewPrev  BlockNumber
	HasParent        bool
	ParentBlk        BlockNumber
	ParentRemoveSlot uint16
}

// LogBtreeUnlinkPageFunc emits the M0079-0003 atomic page-deletion redo record.
type LogBtreeUnlinkPageFunc func(rel RelFileNode, req BtreeUnlinkPageRequest) (LSN, error)

// LogBtreeNewRootFunc emits the M0079-0003 root-replacement record.
type LogBtreeNewRootFunc func(rel RelFileNode, rootBlk BlockNumber, level uint32, items [][]byte) (LSN, error)

// LogBtreeMarkPageHalfDeadFunc emits the M0079-0003 leaf-only half-dead transition record.
type LogBtreeMarkPageHalfDeadFunc func(rel RelFileNode, leafBlk BlockNumber, flagsAfter uint16) (LSN, error)

// LogHeapFreezeFunc emits the M0080-0001 heap-freeze redo record.
type LogHeapFreezeFunc func(rel RelFileNode, blk BlockNumber, frozenSlots []uint16) (LSN, error)

// LogHeapHotUpdateFunc emits one atomic HOT-update redo record.
type LogHeapHotUpdateFunc func(rel RelFileNode, blk BlockNumber, oldSlot uint16, xmax TransactionID, tupleBytes []byte) (LSN, error)

// LogHeapPruneOptFunc emits one opportunistic page-pruning redo record.
type LogHeapPruneOptFunc func(rel RelFileNode, blk BlockNumber, redirects [][2]uint16, unused []uint16) (LSN, error)

// NewPool allocates a Pool of cfg.Slots fixed buffers backed by a
// Go-heap arena.
func NewPool(mgr *Manager, cfg PoolConfig) (*Pool, error) {
	if cfg.Slots <= 0 {
		return nil, fmt.Errorf("pool: Slots must be > 0, got %d", cfg.Slots)
	}
	a, err := newArena(cfg.Slots)
	if err != nil {
		return nil, err
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	p := &Pool{
		mgr:                      mgr,
		arena:                    a,
		slots:                    make([]Slot, cfg.Slots),
		wal:                      cfg.WAL,
		logFPI:                   cfg.LogPageImage,
		logBtreeSplit:            cfg.LogBtreeSplit,
		logHeapInsert:            cfg.LogHeapInsert,
		logBtreeInsert:           cfg.LogBtreeInsert,
		logHeapDelete:            cfg.LogHeapDelete,
		logHeapLock:              cfg.LogHeapLock,
		logHeapVacuum:            cfg.LogHeapVacuum,
		logBtreeVacuum:           cfg.LogBtreeVacuum,
		logBtreeUnlinkPage:       cfg.LogBtreeUnlinkPage,
		logBtreeNewRoot:          cfg.LogBtreeNewRoot,
		logBtreeMarkPageHalfDead: cfg.LogBtreeMarkPageHalfDead,
		logHeapFreeze:            cfg.LogHeapFreeze,
		logHeapHotUpdate:         cfg.LogHeapHotUpdate,
		logHeapPruneOpt:          cfg.LogHeapPruneOpt,
		logSmgrCreate:            cfg.LogSmgrCreate,
		logChangeRecord:          cfg.LogChangeRecord,
		logger:                   logger,
		bm:                       newBufmap(cfg.Slots),
		slotSema:                 make([]uint32, cfg.Slots),
		slotWaiters:              make([]atomic.Int32, cfg.Slots),
		slotEvents:               make([]slotEventRing, cfg.Slots),
	}
	p.fullPageWrites.Store(cfg.FullPageWrites)
	// Upstream defaults (pg_config_manual.h): DEFAULT_CHECKPOINT_FLUSH_AFTER=32,
	// DEFAULT_BGWRITER_FLUSH_AFTER=64, DEFAULT_BACKEND_FLUSH_AFTER=0 (never
	// enabled by default). initdb.Open overrides these from the live GUCs;
	// this keeps a bare NewPool (e.g. in tests) behaviourally real too.
	p.checkpointFlushAfterBlocks.Store(32)
	p.bgwriterFlushAfterBlocks.Store(64)
	// Initialise per-slot page pointers.
	for i := range p.slots {
		p.slots[i].page = a.slot(i)
		p.slots[i].idx = int32(i)
	}
	return p, nil
}

// LogBtreeSplit returns the configured atomic split-record hook.
func (p *Pool) LogBtreeSplit() LogBtreeSplitFunc { return p.logBtreeSplit }

// LogPageImage returns the configured full-page-image hook.
func (p *Pool) LogPageImage() func(rel RelFileNode, blk BlockNumber, page Page) (LSN, error) {
	return p.logFPI
}

// LogHeapInsert returns the configured heap-insert change-record hook.
func (p *Pool) LogHeapInsert() LogHeapInsertFunc { return p.logHeapInsert }

// LogChangeRecord appends a pre-encoded WAL change record.
func (p *Pool) LogChangeRecord(payload []byte) (LSN, error) {
	if p == nil || p.logChangeRecord == nil {
		return 0, nil
	}
	return p.logChangeRecord(payload)
}

// LogBtreeInsert returns the configured btree non-split insert change-record hook.
func (p *Pool) LogBtreeInsert() LogBtreeInsertFunc { return p.logBtreeInsert }

// LogHeapDelete returns the configured heap-delete change-record hook.
func (p *Pool) LogHeapDelete() LogHeapDeleteFunc { return p.logHeapDelete }

// LogHeapLock returns the configured row-lock change-record emitter.
func (p *Pool) LogHeapLock() LogHeapLockFunc { return p.logHeapLock }

// LogHeapVacuum returns the configured heap-vacuum change-record hook.
func (p *Pool) LogHeapVacuum() LogHeapVacuumFunc { return p.logHeapVacuum }

// LogBtreeVacuum returns the configured btree-vacuum kept-items change-record emitter.
func (p *Pool) LogBtreeVacuum() LogBtreeVacuumFunc { return p.logBtreeVacuum }

// LogBtreeUnlinkPage returns the configured atomic page-deletion change-record emitter.
func (p *Pool) LogBtreeUnlinkPage() LogBtreeUnlinkPageFunc { return p.logBtreeUnlinkPage }

// LogBtreeNewRoot returns the configured root-replacement change-record emitter.
func (p *Pool) LogBtreeNewRoot() LogBtreeNewRootFunc { return p.logBtreeNewRoot }

// LogBtreeMarkPageHalfDead returns the configured leaf-only half-dead transition emitter.
func (p *Pool) LogBtreeMarkPageHalfDead() LogBtreeMarkPageHalfDeadFunc {
	return p.logBtreeMarkPageHalfDead
}

// LogHeapFreeze returns the configured heap-freeze change-record emitter.
func (p *Pool) LogHeapFreeze() LogHeapFreezeFunc { return p.logHeapFreeze }

// LogHeapHotUpdate returns the configured HOT-update change-record hook.
func (p *Pool) LogHeapHotUpdate() LogHeapHotUpdateFunc { return p.logHeapHotUpdate }

// LogHeapPruneOpt returns the configured opportunistic-pruning change-record hook.
func (p *Pool) LogHeapPruneOpt() LogHeapPruneOptFunc { return p.logHeapPruneOpt }

// SetFullPageWrites toggles full-page-image emission at runtime.
func (p *Pool) SetFullPageWrites(on bool) { p.fullPageWrites.Store(on) }

// FullPageWrites reports the current setting.
func (p *Pool) FullPageWrites() bool { return p.fullPageWrites.Load() }

// PublishRedoRecPtr publishes the checkpoint redo pointer that gates
// full-page-image emission (perf-optimize3-dash/03, option (b) — PG's
// CreateCheckPoint semantics): a page needs a fresh image iff its pd_lsn is
// <= the published redo, i.e. its last WAL-logged change predates the redo
// point crash recovery (or a standby restartpoint) will replay from.
// Publication IS the epoch boundary — there is no separately-timed per-slot
// reset to race, which is what closed the image-less replay window the old
// fpiSinceCheckpoint sweep left open (adversarial review F-1).
//
// Called by the checkpointer at checkpoint START (before the buffer flush),
// and by initdb.Open at startup with pg_control's checkPointCopy.redo so the
// first post-restart epoch is anchored correctly. Checkpoints are serialized,
// so a plain store suffices; the seed and the first checkpoint publication
// are monotone by construction.
func (p *Pool) PublishRedoRecPtr(redo uint64) {
	p.redoRecPtr.Store(redo)
}

// PublishRedoBarrier atomically (with respect to in-flight FPI decisions)
// samples the redo pointer via the caller-supplied closure and publishes it.
// The exclusive lock waits for every writer currently inside a
// needsImage->append critical section (RLock held by the MarkDirty*
// variants), so a record decided against the OLD redo always has
// LSN < the frontier the closure samples — i.e. < the NEW redo — and is
// covered by the previous epoch's image on any replay that starts at the new
// redo's checkpoint. sample must not acquire pool or slot locks (the
// checkpointer passes a WAL-frontier read). Returns the published redo.
func (p *Pool) PublishRedoBarrier(sample func() uint64) uint64 {
	p.fpiPublishMu.Lock()
	redo := sample()
	p.redoRecPtr.Store(redo)
	p.fpiPublishMu.Unlock()
	return redo
}

// RedoRecPtr returns the currently published redo pointer (0 on a fresh
// cluster before any checkpoint — every page images on first touch, the safe
// direction).
func (p *Pool) RedoRecPtr() uint64 { return p.redoRecPtr.Load() }

// needsImage is the per-record FPI decision: the slot's last NATIVE image
// predates the published redo, so the page's first post-redo native-family
// modification must carry a fresh image (torn-page anchor for
// replay-from-redo). Callers hold fpiPublishMu.RLock (see
// PublishRedoBarrier); the read is a plain atomic — no page-byte access.
//
// Deliberately NOT pd_lsn: pd_lsn is stamped by both record families, and a
// canonical record's stamp must not suppress the native image (cross-family
// poisoning — see Slot.nativeImageLSN).
//
// LSN-base note: nativeImageLSN carries 1-based writer LSNs while the
// published redo is the 0-based redoLSN0 of the PG checkpoint record
// (= frontier pos + page-header leading). The mismatch errs only toward
// EXTRA images (every pre-publication LSN <= pos <= pos+leading); a needed
// image is never skipped. Do not renormalize one side alone.
func (p *Pool) needsImage(s *Slot) bool {
	return s.nativeImageLSN.Load() <= p.redoRecPtr.Load()
}

// Close flushes every dirty slot through smgr and releases the arena.
func (p *Pool) Close() error {
	flushErr := p.FlushAll()
	closeErr := p.arena.close()
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

// NBlocks reports the number of blocks currently in rel.
func (p *Pool) NBlocks(rel RelFileNode) (BlockNumber, error) {
	return p.mgr.NBlocks(rel)
}

// Exists reports whether rel's backing fork file is present on disk without
// creating it (mirrors smgrexists). See Manager.Exists.
func (p *Pool) Exists(rel RelFileNode) bool {
	return p.mgr.Exists(rel)
}

// RelPath returns rel's fork path relative to the data directory (e.g.
// "base/5/16407"), for building the upstream-verbatim missing-file message.
func (p *Pool) RelPath(rel RelFileNode) string {
	return p.mgr.RelPath(rel)
}

// Manager exposes the underlying storage manager.
func (p *Pool) Manager() *Manager { return p.mgr }

// BufferCounters returns the pool-wide cumulative shared-buffer hit/read
// tallies. EXPLAIN (ANALYZE, BUFFERS) diffs a before/after snapshot of this
// pair per plan node (internal/executor/instrument.go) to render PG's
// "Buffers: shared hit=N read=N" line — mirrors BufferUsage.shared_blks_hit /
// shared_blks_read (postgres/src/include/executor/instrument.h).
func (p *Pool) BufferCounters() (hit, read, dirtied, written int64) {
	return p.sharedHitCount.Load(), p.sharedReadCount.Load(), p.sharedDirtiedCount.Load(), p.sharedWrittenCount.Load()
}

// AddReadTimeNanos accumulates n nanoseconds of real disk-read wait time
// into the pool-wide read-time tally. Called only from the OnPinDone hook
// when the pinning backend has track_io_timing enabled (see
// sharedReadTimeNanos's doc comment).
func (p *Pool) AddReadTimeNanos(n int64) {
	if n > 0 {
		p.sharedReadTimeNanos.Add(n)
	}
}

// ReadTimeNanos returns the pool-wide cumulative real time spent in disk
// reads by backends with track_io_timing on, backing pg_stat_io's
// read_time column (milliseconds) and EXPLAIN's I/O Timings.
func (p *Pool) ReadTimeNanos() int64 {
	return p.sharedReadTimeNanos.Load()
}

// AddWriteTimeNanos accumulates n nanoseconds of real dirty-victim flush
// wait time into the pool-wide write-time tally. Called only from the
// OnFlushDone hook when the evicting backend has track_io_timing enabled
// (see sharedWriteTimeNanos's doc comment).
func (p *Pool) AddWriteTimeNanos(n int64) {
	if n > 0 {
		p.sharedWriteTimeNanos.Add(n)
	}
}

// WriteTimeNanos returns the pool-wide cumulative real time spent flushing
// dirty victims by backends with track_io_timing on, backing pg_stat_io's
// write_time column (milliseconds).
func (p *Pool) WriteTimeNanos() int64 {
	return p.sharedWriteTimeNanos.Load()
}

// EvictionCount returns the pool-wide cumulative count of real victim
// evictions (backs pg_stat_io's evictions column).
func (p *Pool) EvictionCount() int64 {
	return p.sharedEvictionCount.Load()
}

// ExtendCount returns the pool-wide cumulative count of relation extensions
// (backs pg_stat_io's extends / extend_bytes columns).
func (p *Pool) ExtendCount() int64 {
	return p.sharedExtendCount.Load()
}

// AddExtendTimeNanos accumulates n nanoseconds of real relation-extension
// wait time into the pool-wide extend-time tally. Called only from the
// OnExtendDone hook when the extending backend has track_io_timing enabled
// (see sharedExtendTimeNanos's doc comment).
func (p *Pool) AddExtendTimeNanos(n int64) {
	if n > 0 {
		p.sharedExtendTimeNanos.Add(n)
	}
}

// ExtendTimeNanos returns the pool-wide cumulative real time spent
// extending relations by backends with track_io_timing on, backing
// pg_stat_io's extend_time column (milliseconds).
func (p *Pool) ExtendTimeNanos() int64 {
	return p.sharedExtendTimeNanos.Load()
}

// SetCheckpointFlushAfter sets the checkpointer's writeback threshold, in
// BLCKSZ pages (mirrors checkpoint_flush_after; <= 0 disables writeback for
// checkpointer-issued writes).
func (p *Pool) SetCheckpointFlushAfter(n int) {
	p.checkpointFlushAfterBlocks.Store(int32(n))
}

// SetBgwriterFlushAfter sets the bgwriter's writeback threshold, in BLCKSZ
// pages (mirrors bgwriter_flush_after; <= 0 disables writeback for
// bgwriter-issued writes).
func (p *Pool) SetBgwriterFlushAfter(n int) {
	p.bgwriterFlushAfterBlocks.Store(int32(n))
}

// SetBackendFlushAfter sets the writeback threshold applied to a backend's
// own dirty-victim-eviction writes, in BLCKSZ pages (mirrors
// backend_flush_after; <= 0, the upstream default, disables it).
func (p *Pool) SetBackendFlushAfter(n int) {
	p.backendFlushAfterBlocks.Store(int32(n))
}

// CheckpointWritebackCount / CheckpointWritebackTimeNanos back pg_stat_io's
// (checkpointer, relation, normal) writeback / writeback_time cells.
func (p *Pool) CheckpointWritebackCount() int64 { return p.sharedCheckpointWritebackCount.Load() }
func (p *Pool) CheckpointWritebackTimeNanos() int64 {
	return p.sharedCheckpointWritebackTimeNanos.Load()
}

// BgwriterWritebackCount / BgwriterWritebackTimeNanos back pg_stat_io's
// (background writer, relation, normal) writeback / writeback_time cells.
func (p *Pool) BgwriterWritebackCount() int64 { return p.sharedBgwriterWritebackCount.Load() }
func (p *Pool) BgwriterWritebackTimeNanos() int64 {
	return p.sharedBgwriterWritebackTimeNanos.Load()
}

// BackendWritebackCount / BackendWritebackTimeNanos back pg_stat_io's
// (client backend, relation, normal) writeback / writeback_time cells.
func (p *Pool) BackendWritebackCount() int64 { return p.sharedBackendWritebackCount.Load() }
func (p *Pool) BackendWritebackTimeNanos() int64 {
	return p.sharedBackendWritebackTimeNanos.Load()
}

// AddCheckpointWritebackTimeNanos / AddBgwriterWritebackTimeNanos /
// AddBackendWritebackTimeNanos accumulate real wall-clock time spent in a
// context's SyncFileRangeHint call. Called only from the matching
// On*WritebackDone hook (see writeback.go / initdb.Open's wiring).
func (p *Pool) AddCheckpointWritebackTimeNanos(n int64) {
	if n > 0 {
		p.sharedCheckpointWritebackTimeNanos.Add(n)
	}
}

func (p *Pool) AddBgwriterWritebackTimeNanos(n int64) {
	if n > 0 {
		p.sharedBgwriterWritebackTimeNanos.Add(n)
	}
}

func (p *Pool) AddBackendWritebackTimeNanos(n int64) {
	if n > 0 {
		p.sharedBackendWritebackTimeNanos.Add(n)
	}
}

// CheckpointWrittenCount / CheckpointWriteTimeNanos back pg_stat_io's
// (checkpointer, relation, normal) writes / write_time cells.
func (p *Pool) CheckpointWrittenCount() int64 { return p.sharedCheckpointWrittenCount.Load() }
func (p *Pool) CheckpointWriteTimeNanos() int64 {
	return p.sharedCheckpointWriteTimeNanos.Load()
}

// BgwriterWrittenCount / BgwriterWriteTimeNanos back pg_stat_io's
// (background writer, relation, normal) writes / write_time cells.
func (p *Pool) BgwriterWrittenCount() int64 { return p.sharedBgwriterWrittenCount.Load() }
func (p *Pool) BgwriterWriteTimeNanos() int64 {
	return p.sharedBgwriterWriteTimeNanos.Load()
}

// AddCheckpointWriteTimeNanos / AddBgwriterWriteTimeNanos accumulate real
// wall-clock time spent in a context's dirty-page write. Called only from
// the matching On*WriteDone hook (see initdb.Open's wiring).
func (p *Pool) AddCheckpointWriteTimeNanos(n int64) {
	if n > 0 {
		p.sharedCheckpointWriteTimeNanos.Add(n)
	}
}

func (p *Pool) AddBgwriterWriteTimeNanos(n int64) {
	if n > 0 {
		p.sharedBgwriterWriteTimeNanos.Add(n)
	}
}

// SyncAllDataFiles fdatasyncs every open data file.
func (p *Pool) SyncAllDataFiles() error {
	if p.mgr == nil {
		return nil
	}
	return p.mgr.SyncAll()
}

// SetPrefetchEnabled toggles Pool.Prefetch's behaviour.
func (p *Pool) SetPrefetchEnabled(on bool) { p.prefetchEnabled.Store(on) }

// SetAsyncFlushBatchSize controls how many dirty slots FlushAllPaced batches.
func (p *Pool) SetAsyncFlushBatchSize(n int) {
	if n > MaxFlushBatchSize {
		n = MaxFlushBatchSize
	}
	if n < 0 {
		n = 0
	}
	p.asyncFlushBatchSize.Store(int32(n))
}

// MaxFlushBatchSize caps the per-batch dirty-slot count.
const MaxFlushBatchSize = 256

// flushBatchSize returns the effective batch size for FlushAllPaced.
func (p *Pool) flushBatchSize() int {
	n := int(p.asyncFlushBatchSize.Load())
	if n < 1 {
		return 1
	}
	return n
}

// Prefetch is a hint that the caller is about to Pin tag.
func (p *Pool) Prefetch(tag BufferTag) {
	if !p.prefetchEnabled.Load() {
		return
	}
	// Fast check: already cached?
	if slotIdx, _ := p.bm.Lookup(tag); slotIdx >= 0 {
		return
	}
	buf := make([]byte, BlockSize)
	_, _ = p.mgr.PrefetchBlock(tag.Rel, tag.Block, buf)
}

// InvalidateRel evicts every slot currently bound to rel.
func (p *Pool) InvalidateRel(rel RelFileNode) {
	for i := range p.slots {
		s := &p.slots[i]
		st := s.state.Load()
		if !stateValid(st) {
			continue
		}
		if s.tag.Rel != rel {
			continue
		}
		if statePin(st) > 0 {
			continue
		}
		// Try to atomically clear valid+dirty bits.
		// We only clear if the slot is still valid+unpinned.
		newSt := st &^ (slotValidBit | slotDirtyBit)
		if s.state.CompareAndSwap(st, newSt) {
			p.bmDelete(s.tag, int32(i))
			p.tombstones.Add(1)
		}
	}
}

// InvalidateBlock evicts the slot bound to tag if it exists and is unpinned.
func (p *Pool) InvalidateBlock(tag BufferTag) {
	slotIdx, _ := p.bm.Lookup(tag)
	if slotIdx < 0 {
		return
	}
	s := &p.slots[slotIdx]
	st := s.state.Load()
	if !stateValid(st) || statePin(st) > 0 {
		return
	}
	newSt := st &^ (slotValidBit | slotDirtyBit)
	if s.state.CompareAndSwap(st, newSt) {
		p.bmDelete(tag, slotIdx)
		p.tombstones.Add(1)
	}
}

// ErrNoBuffer is returned when every slot is pinned and the clock
// sweep can't find a victim.
var ErrNoBuffer = errors.New("no available buffer (all pinned)")

// bmInsert and bmDelete are the sole call sites that touch p.bm.Insert/
// p.bm.Delete (M-NIGHTLY AI-20260708-064334-001, 14th loop) — routing
// every bufmap mutation through these two funcs lets OnBufmapInsert/
// OnBufmapDelete observe every (tag, slotIdx) ownership change in bufmap's
// own true lock order, with no extra synchronization of their own (the
// hook runs synchronously inside bufmap's Insert/Delete call, still
// holding bufmap's internal mu). Zero cost when both hooks are nil.
func (p *Pool) bmInsert(tag BufferTag, slotIdx int32, gen uint32) bool {
	ok := p.bm.Insert(tag, slotIdx, gen)
	if p.OnBufmapInsert != nil {
		p.OnBufmapInsert(tag, slotIdx, gen, ok)
	}
	return ok
}

func (p *Pool) bmDelete(tag BufferTag, slotIdx int32) {
	p.bm.Delete(tag, slotIdx)
	if p.OnBufmapDelete != nil {
		p.OnBufmapDelete(tag, slotIdx)
	}
}

// tryPinSlot attempts to increment the pin count on slotIdx atomically,
// verifying the slot is valid, not in IO, and has the expected generation.
// Returns the slot on success, or nil on failure (caller should retry/fallback).
func (p *Pool) tryPinSlot(slotIdx int32, gen uint32) *Slot {
	s := &p.slots[slotIdx]
	for {
		old := s.state.Load()
		if !stateValid(old) || stateIO(old) {
			return nil
		}
		if stateGen(old) != gen {
			return nil // ABA
		}
		pinCount := statePin(old)
		if pinCount >= maxPinCount {
			return nil
		}
		usage := stateUsage(old)
		if usage < maxUsageCount {
			usage++
		}
		newSt := (old &^ (slotPinMask | slotUsageMask)) | (pinCount + 1) | (usage << slotUsageShift)
		if s.state.CompareAndSwap(old, newSt) {
			return s
		}
	}
}

// releaseVictimSlot returns a victim slot back to the free pool when we
// decide not to use it (e.g., because we found the tag already published
// by another goroutine). MUST be called with pinMu held.
// Wakes any goroutines waiting on this slot's IO via per-slot semaphore.
func (p *Pool) releaseVictimSlot(victimIdx int) {
	n := p.slotWaiters[victimIdx].Load()
	s := &p.slots[victimIdx]
	prevSt := s.state.Load()
	s.state.Store(0)
	p.traceSlotEvent(int32(victimIdx), evReleaseVictimSlot, s.tag, prevSt, 0)
	for i := int32(0); i < n; i++ {
		runtimeshim.SemaRelease(&p.slotSema[victimIdx])
	}
}

// claimVictim finds a victim slot using clock-sweep and atomically sets
// ioInflight, clearing valid+dirty. Returns (idx, wasDirty, oldTag).
// MUST be called with pinMu held.
func (p *Pool) claimVictim() (victimIdx int, wasDirty bool, oldTag BufferTag, err error) {
	n := len(p.slots)
	if n == 0 {
		return 0, false, BufferTag{}, ErrNoBuffer
	}
	maxProbes := n * (maxUsageCount + 2)
	for probe := 0; probe < maxProbes; probe++ {
		i := int(p.clockHand.Add(1)-1) % n
		s := &p.slots[i]
		old := s.state.Load()

		if statePin(old) != 0 {
			continue // pinned
		}
		if stateIO(old) {
			continue // another goroutine is evicting this slot
		}

		usage := stateUsage(old)
		if usage > 0 {
			// Decrement usage (second-chance). Best-effort.
			newSt := (old &^ slotUsageMask) | ((usage - 1) << slotUsageShift)
			s.state.CompareAndSwap(old, newSt)
			continue
		}

		wasValid := stateValid(old)
		dirty := stateDirty(old)

		// Track dirty-victim stats.
		if wasValid {
			p.totalVictimCount.Add(1)
			if dirty {
				p.dirtyVictimCount.Add(1)
			}
		}

		// Claim: set ioInflight, bump gen to invalidate in-flight fast-path CAS.
		curGen := stateGen(old)
		newGen := (curGen + 1) & 0x7FFF
		newSt := slotIOBit | (uint64(newGen) << slotGenShift)
		if s.state.CompareAndSwap(old, newSt) {
			tag := s.tag
			if !wasValid {
				tag = BufferTag{}
			}
			p.traceSlotEvent(int32(i), evClaimVictim, tag, old, newSt)
			return i, wasValid && dirty, tag, nil
		}
		// Another goroutine modified this slot concurrently; try next.
	}
	return 0, false, BufferTag{}, ErrNoBuffer
}

// evictVictim flushes (if dirty) and removes the old tag from bufmap.
// MUST be called with pinMu held.
func (p *Pool) evictVictim(victimIdx int, wasDirty bool, oldTag BufferTag) error {
	if oldTag == (BufferTag{}) {
		return nil // slot was free
	}
	p.sharedEvictionCount.Add(1)
	if !wasDirty {
		// M-NIGHTLY investigation aid (AI-20260708-064334-001): the
		// "clean" fast path below assumes on-disk content already
		// matches in-memory content. When DebugValidateCleanEvictions is
		// set, verify that assumption directly; a mismatch is definitive
		// proof the dirty bit was wrong at eviction time. Off by default
		// (zero cost). See TestVerifyBtreeEngineSilentOnRealConcurrentContended.
		if p.DebugValidateCleanEvictions {
			p.debugValidateCleanEviction(victimIdx, oldTag)
		}
		// Nothing to flush: the on-disk content already matches, so it's
		// safe to retire the tag immediately.
		st := p.slots[victimIdx].state.Load()
		p.traceSlotEvent(int32(victimIdx), evEvictVictimClean, oldTag, st, st)
		p.bmDelete(oldTag, int32(victimIdx))
		p.tombstones.Add(1)
		p.maybeCompact()
		return nil
	}
	s := &p.slots[victimIdx]
	p.traceSlotEvent(int32(victimIdx), evEvictVictimDirty, oldTag, s.state.Load(), s.state.Load())
	// Release pinMu while doing IO so other goroutines can proceed.
	p.pinMu.Unlock()
	s.contentMu.Lock()
	if p.OnFlushWait != nil {
		p.OnFlushWait()
	}
	recordIOTrace(oldTag, "preFlush", s.page)
	flushErr := p.flushSlot(oldTag, s.page)
	if p.OnFlushDone != nil {
		p.OnFlushDone()
	}
	s.contentMu.Unlock()
	p.pinMu.Lock()
	// Delete from bufmap only AFTER the flush has durably landed: the
	// slot's IO-inflight bit (set by claimVictim) keeps a concurrent
	// Pin(oldTag) waiting on this slot's semaphore for the whole flush,
	// so deleting earlier let such a waiter fall through to a bufmap
	// miss and race its own fresh disk read against this write — often
	// winning and caching the pre-flush (stale/virgin) page forever
	// under a different slot (M-NIGHTLY loop 13 root cause). Any
	// waiters that queued up during the flush are released below, by
	// which point the delete is already visible to them.
	p.bmDelete(oldTag, int32(victimIdx))
	p.tombstones.Add(1)
	p.maybeCompact()
	if n := p.slotWaiters[victimIdx].Load(); n > 0 {
		for i := int32(0); i < n; i++ {
			runtimeshim.SemaRelease(&p.slotSema[victimIdx])
		}
	}
	if flushErr != nil {
		return flushErr
	}
	p.sharedWrittenCount.Add(1)
	p.accountBackendWrite(oldTag.Rel)
	return nil
}

// debugValidateCleanEviction is an M-NIGHTLY investigation aid
// (AI-20260708-064334-001) gated by DebugValidateCleanEvictions. It
// re-reads oldTag's block from disk and compares it byte-for-byte
// against the slot's in-memory content; a mismatch proves the "clean"
// (non-dirty) eviction path was about to silently discard unflushed
// content. Only called when the flag is set, so it costs nothing by
// default. Remove once the eviction-race root cause is fixed.
func (p *Pool) debugValidateCleanEviction(victimIdx int, oldTag BufferTag) {
	diskCopy := make(Page, BlockSize)
	if err := p.mgr.ReadBlock(oldTag.Rel, oldTag.Block, diskCopy); err != nil {
		return
	}
	mem := p.slots[victimIdx].page
	if bytes.Equal(diskCopy, mem) {
		return
	}
	firstDiff, lastDiff, ndiff := -1, -1, 0
	for i := 0; i < len(diskCopy) && i < len(mem); i++ {
		if diskCopy[i] != mem[i] {
			if firstDiff < 0 {
				firstDiff = i
			}
			lastDiff = i
			ndiff++
		}
	}
	p.logger.Error("BUG: clean-eviction content mismatch (M-NIGHTLY AI-20260708-064334-001)",
		"tag", oldTag, "slotIdx", victimIdx, "firstDiffByte", firstDiff,
		"lastDiffByte", lastDiff, "ndiffBytes", ndiff,
		"diskLSN", MustHeader(diskCopy).LSN(), "memLSN", MustHeader(mem).LSN())
	if p.DebugTraceSlotEvents {
		p.dumpSlotEvents(int32(victimIdx))
		p.dumpCrossSlotEventsForTag(oldTag)
	}
}

// dumpCrossSlotEventsForTag is a one-shot M-NIGHTLY investigation aid
// (AI-20260708-064334-001): scans every slot's event ring for any event
// touching tag, to check whether two different physical slots ever both
// recorded activity for the same tag concurrently (which bufmap's Insert
// mutex is supposed to make impossible). Only called from the already
// DebugTraceSlotEvents-gated debugValidateCleanEviction path.
func (p *Pool) dumpCrossSlotEventsForTag(tag BufferTag) {
	for slotIdx := range p.slotEvents {
		ring := &p.slotEvents[slotIdx]
		pos := ring.pos.Load()
		start := uint64(0)
		if pos > slotEventRingSize {
			start = pos - slotEventRingSize
		}
		for i := start; i < pos; i++ {
			e := ring.events[i%slotEventRingSize]
			if e.tag != tag {
				continue
			}
			p.logger.Error("cross-slot-event (M-NIGHTLY AI-20260708-064334-001)",
				"seq", e.seq, "slotIdx", slotIdx, "kind", e.kind.String(), "tag", e.tag,
				"oldState", fmt.Sprintf("%#016x", e.oldState), "newState", fmt.Sprintf("%#016x", e.newState))
		}
	}
}

// DumpEventsForTag is the exported, string-returning sibling of
// dumpCrossSlotEventsForTag (M-NIGHTLY AI-20260708-064334-001, 5th loop): scans
// every slot's event ring for any event touching tag and returns them as
// formatted lines, oldest first across all slots, for a caller outside this
// package (e.g. a btree-layer investigation test) to log/cross-reference
// against its own write-path trace. Requires DebugTraceSlotEvents to have
// been true for the events to exist; returns nil otherwise.
func (p *Pool) DumpEventsForTag(tag BufferTag) []string {
	var out []string
	for slotIdx := range p.slotEvents {
		ring := &p.slotEvents[slotIdx]
		pos := ring.pos.Load()
		start := uint64(0)
		if pos > slotEventRingSize {
			start = pos - slotEventRingSize
		}
		for i := start; i < pos; i++ {
			e := ring.events[i%slotEventRingSize]
			if e.tag != tag {
				continue
			}
			out = append(out, fmt.Sprintf("seq=%d slotIdx=%d kind=%s tag=%v oldState=%#016x newState=%#016x",
				e.seq, slotIdx, e.kind.String(), e.tag, e.oldState, e.newState))
		}
	}
	return out
}

// PinNew pins the next-after-end block of rel for writing without
// reading from disk. Returns the block number the slot represents.
func (p *Pool) PinNew(rel RelFileNode) (*Slot, BlockNumber, error) {
	p.pinMu.Lock()

	victimIdx, wasDirty, oldTag, err := p.claimVictim()
	if err != nil {
		p.pinMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	s := &p.slots[victimIdx]

	if err := p.evictVictim(victimIdx, wasDirty, oldTag); err != nil {
		p.releaseVictimSlot(victimIdx)
		p.pinMu.Unlock()
		return nil, InvalidBlockNumber, fmt.Errorf("flush victim: %w", err)
	}

	// Initialize page and extend the relation. Release pinMu during IO.
	p.pinMu.Unlock()
	s.contentMu.Lock()
	if err := InitPage(s.page); err != nil {
		s.contentMu.Unlock()
		p.pinMu.Lock()
		p.releaseVictimSlot(victimIdx)
		p.pinMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	if p.OnExtendWait != nil {
		p.OnExtendWait()
	}
	blk, err := p.mgr.Extend(rel, s.page)
	if p.OnExtendDone != nil {
		p.OnExtendDone()
	}
	s.contentMu.Unlock()
	if err != nil {
		p.pinMu.Lock()
		p.releaseVictimSlot(victimIdx)
		p.pinMu.Unlock()
		return nil, InvalidBlockNumber, err
	}
	p.sharedExtendCount.Add(1)
	p.pinMu.Lock()

	// Emit SmgrCreate WAL record on first block creation.
	if blk == 0 && p.logSmgrCreate != nil {
		if emitErr := p.logSmgrCreate(rel); emitErr != nil {
			p.logger.Error("SmgrCreate WAL emission failed", "rel", rel, "err", emitErr)
		}
	}

	tag := BufferTag{Rel: rel, Block: blk}

	// Publish this slot as valid+dirty+pinned.
	gen := stateGen(s.state.Load())
	prevSt := s.state.Load()
	s.tag = tag
	s.nativeImageLSN.Store(0) // fresh page in this slot: re-arm first-touch image
	newSt := slotValidBit | slotDirtyBit | uint64(1) | (uint64(1) << slotUsageShift) | (uint64(gen) << slotGenShift)
	s.state.Store(newSt)
	p.traceSlotEvent(int32(victimIdx), evPinNewPublish, tag, prevSt, newSt)

	// Insert into bufmap. Under pinMu, no other goroutine can insert the same tag.
	if !p.bmInsert(tag, int32(victimIdx), gen) {
		// Another goroutine published this block while we were in Extend.
		// Use their slot and release ours.
		if existingIdx, existingGen := p.bm.Lookup(tag); existingIdx >= 0 {
			if existing := p.tryPinSlot(existingIdx, existingGen); existing != nil {
				p.traceSlotEvent(int32(victimIdx), evReleaseVictimSlot, tag, s.state.Load(), 0)
				s.tag = BufferTag{}
				s.state.Store(0)
				p.pinMu.Unlock()
				return existing, blk, nil
			}
		}
		// Fall through: keep our publication.
	}

	p.pinMu.Unlock()
	return s, blk, nil
}

// ExtendRelationBatch appends n empty, initialized blocks to rel as a
// single batched smgr write, and returns the block number of the first
// new block; subsequent blocks occupy firstBlk+1 .. firstBlk+n-1.
//
// Unlike PinNew, no buffer slot is pinned and no bufmap entry is
// published — the new pages live on disk only. Subsequent heap-insert
// calls find them via FSM (the caller registers FSM entries for the
// added blocks per
// `docs/design/perf-optimize/07-wal-fsm-insert.md` §3).
//
// If the relation transitions from empty to non-empty (firstBlk == 0)
// the SmgrCreate WAL record is emitted exactly once, matching the
// invariant pinned by PinNew. Foundation 2 of 3 for M0107-0007 slice C;
// the executor consumer (`selectInsertPage`) lands after the third
// foundation (`Pool.SlotPinCount`, blocked on M0107-0006).
func (p *Pool) ExtendRelationBatch(rel RelFileNode, n int) (BlockNumber, error) {
	if n <= 0 {
		return InvalidBlockNumber, fmt.Errorf("ExtendRelationBatch: n=%d must be > 0", n)
	}
	// One zero-initialized page is reused for every block; the heap-insert
	// caller fills the headers and tuples on the next Pin+MarkDirty cycle.
	page := make([]byte, BlockSize)
	if err := InitPage(page); err != nil {
		return InvalidBlockNumber, err
	}
	first, err := p.mgr.ExtendBatch(rel, page, n)
	if err != nil {
		return InvalidBlockNumber, err
	}
	if first == 0 && p.logSmgrCreate != nil {
		if emitErr := p.logSmgrCreate(rel); emitErr != nil {
			p.logger.Error("SmgrCreate WAL emission failed",
				"rel", rel, "err", emitErr)
		}
	}
	return first, nil
}

// Pin returns the slot holding tag, reading from disk if necessary.
// The slot's pinCount is incremented; the caller MUST call Unpin.
//
// BM_IO_IN_PROGRESS: when multiple goroutines miss the cache for the same
// tag simultaneously, only one issues a disk read (the winner). The others
// wait via per-slot semaphore until the IO completes, then find the slot valid.
//
// Fast path (cache hit): lock-free Lookup + CAS, no mutex.
// Slow path (cache miss / IO wait): acquires pinMu.
func (p *Pool) Pin(tag BufferTag) (*Slot, error) {
	for {
		// Fast path: tag is cached and valid (no IO in flight).
		slotIdx, gen := p.bm.Lookup(tag)
		if slotIdx >= 0 {
			s := &p.slots[slotIdx]
			old := s.state.Load()

			if stateValid(old) && !stateIO(old) && stateGen(old) == gen {
				// Slot is valid and ready: try CAS to increment pin.
				pinCount := statePin(old)
				if pinCount >= maxPinCount {
					return nil, ErrNoBuffer
				}
				usage := stateUsage(old)
				if usage < maxUsageCount {
					usage++
				}
				newSt := (old &^ (slotPinMask | slotUsageMask)) | (pinCount + 1) | (usage << slotUsageShift)
				if s.state.CompareAndSwap(old, newSt) {
					p.sharedHitCount.Add(1)
					return s, nil
				}
				// CAS failed (concurrent pin/unpin); retry fast path.
				continue
			}
		}

		// Slow path: cache miss, IO in flight, or transient state.
		if s, err := p.pinSlow(tag); s != nil || err != nil {
			return s, err
		}
		// pinSlow returned nil,nil: spurious wakeup or ABA; retry from top.
	}
}

// pinSlow is the slow path for Pin: acquires pinMu, re-checks the bufmap,
// waits for in-flight IO to complete, or performs a full cache-miss eviction+read.
//
// Returns (slot, nil) on success, (nil, err) on hard error,
// (nil, nil) to signal the caller to retry the fast path.
func (p *Pool) pinSlow(tag BufferTag) (*Slot, error) {
	p.pinMu.Lock()
	defer p.pinMu.Unlock()

	for {
		slotIdx, gen := p.bm.Lookup(tag)
		if slotIdx >= 0 {
			s := &p.slots[slotIdx]
			old := s.state.Load()

			if stateIO(old) {
				// IO in flight: wait for it to complete via per-slot semaphore.
				// Increment waiter count under pinMu so the loader sees us.
				p.slotWaiters[slotIdx].Add(1)
				p.pinMu.Unlock()
				if p.OnBufferIOWait != nil {
					p.OnBufferIOWait()
				}
				runtimeshim.SemaAcquire(&p.slotSema[slotIdx])
				p.slotWaiters[slotIdx].Add(-1)
				p.pinMu.Lock()
				continue // re-check after wakeup
			}

			if stateValid(old) && stateGen(old) == gen {
				// Valid: try to pin.
				if s2 := p.tryPinSlot(slotIdx, gen); s2 != nil {
					p.sharedHitCount.Add(1)
					return s2, nil
				}
				// tryPinSlot failed (race): retry
				continue
			}

			// Slot in unexpected state (just evicted?): retry Lookup.
			continue
		}

		// Cache miss: load the page from disk.
		return p.pinLoad(tag)
	}
}

// pinLoad loads tag from disk into a victim slot. Called under pinMu.
// Returns (slot, nil) on success, (nil, err) on error.
func (p *Pool) pinLoad(tag BufferTag) (*Slot, error) {
	// Find a victim.
	victimIdx, wasDirty, oldTag, err := p.claimVictim()
	if err != nil {
		return nil, err
	}
	s := &p.slots[victimIdx]

	// Evict the old contents.
	if err := p.evictVictim(victimIdx, wasDirty, oldTag); err != nil {
		p.releaseVictimSlot(victimIdx)
		return nil, fmt.Errorf("flush victim: %w", err)
	}

	// Re-check after eviction in case another goroutine already loaded this tag.
	if existingIdx, existingGen := p.bm.Lookup(tag); existingIdx >= 0 {
		if existing := p.tryPinSlot(existingIdx, existingGen); existing != nil {
			p.releaseVictimSlot(victimIdx)
			return existing, nil
		}
	}

	// Publish tag in bufmap with ioInflight set. Any concurrent Lookup will
	// see the slot and wait in the "stateIO" branch above.
	gen := stateGen(s.state.Load())
	s.tag = tag
	s.nativeImageLSN.Store(0) // slot reused for a different page: re-arm image
	if !p.bmInsert(tag, int32(victimIdx), gen) {
		// Another goroutine (in a concurrent PinNew?) published this tag.
		p.traceSlotEvent(int32(victimIdx), evReleaseVictimSlot, tag, s.state.Load(), 0)
		s.tag = BufferTag{}
		p.releaseVictimSlot(victimIdx)
		return nil, nil // caller retries
	}

	// Release pinMu during the disk read so other goroutines can proceed.
	p.pinMu.Unlock()
	s.contentMu.Lock()
	if p.OnPinWait != nil {
		p.OnPinWait()
	}
	ioErr := p.mgr.ReadBlock(tag.Rel, tag.Block, s.page)
	if ioErr == nil && p.OnBlockReload != nil {
		p.OnBlockReload(tag, s.page)
	}
	if p.OnPinDone != nil {
		p.OnPinDone()
	}
	s.contentMu.Unlock()
	p.pinMu.Lock()

	if ioErr != nil {
		p.bmDelete(tag, int32(victimIdx))
		p.tombstones.Add(1)
		s.tag = BufferTag{}
		p.releaseVictimSlot(victimIdx) // also wakes per-slot sema waiters
		return nil, ioErr
	}

	// Transition to valid+pinned. Read waiter count under pinMu before
	// clearing ioInflight so no new waiters can arrive between read and wake.
	n := p.slotWaiters[victimIdx].Load()
	prevSt := s.state.Load()
	newSt := slotValidBit | uint64(1) | (uint64(1) << slotUsageShift) | (uint64(gen) << slotGenShift)
	s.state.Store(newSt)
	p.traceSlotEvent(int32(victimIdx), evPinLoadPublish, tag, prevSt, newSt)
	for i := int32(0); i < n; i++ {
		runtimeshim.SemaRelease(&p.slotSema[victimIdx])
	}
	p.sharedReadCount.Add(1)
	return s, nil
}

// Capacity returns the total number of buffer slots in the pool.
func (p *Pool) Capacity() int { return len(p.slots) }

// TryPin returns the pool slot for tag if it is already cached.
// Returns (nil, false) when the tag is not in the pool.
func (p *Pool) TryPin(tag BufferTag) (*Slot, bool) {
	slotIdx, gen := p.bm.Lookup(tag)
	if slotIdx < 0 {
		return nil, false
	}
	s := &p.slots[slotIdx]
	for {
		old := s.state.Load()
		if !stateValid(old) || stateIO(old) {
			return nil, false
		}
		if stateGen(old) != gen {
			return nil, false
		}
		pinCount := statePin(old)
		if pinCount >= maxPinCount {
			return nil, false
		}
		usage := stateUsage(old)
		if usage < maxUsageCount {
			usage++
		}
		newSt := (old &^ (slotPinMask | slotUsageMask)) | (pinCount + 1) | (usage << slotUsageShift)
		if s.state.CompareAndSwap(old, newSt) {
			return s, true
		}
	}
}

// SlotPinCount returns the current pin count for tag's slot, or 0 if
// tag is not currently mapped. Lock-free; safe for concurrent use.
//
// Consumers outside the Pin/Unpin core (e.g. M0107-0007 FSM hot-page
// avoidance in selectInsertPage) use this helper to avoid inlining
// slotState bit-mask arithmetic.
func (p *Pool) SlotPinCount(tag BufferTag) int32 {
	slotIdx, gen := p.bm.Lookup(tag)
	if slotIdx < 0 {
		return 0
	}
	s := &p.slots[slotIdx]
	st := s.state.Load()
	if !stateValid(st) || stateGen(st) != gen {
		return 0
	}
	return int32(statePin(st))
}

// Unpin decrements the slot's pin count via CAS.
func (p *Pool) Unpin(s *Slot) {
	for {
		old := s.state.Load()
		pinCount := statePin(old)
		if pinCount == 0 {
			panic(fmt.Sprintf("unpin underflow on tag %v", s.tag))
		}
		newSt := (old &^ slotPinMask) | (pinCount - 1)
		if s.state.CompareAndSwap(old, newSt) {
			return
		}
	}
}

// MarkDirty flags the slot as having been mutated.
func (p *Pool) MarkDirty(s *Slot) {
	p.maybeEmitFPI(s)
	// Atomically set dirty bit.
	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			return // already dirty
		}
		newSt := old | slotDirtyBit
		if s.state.CompareAndSwap(old, newSt) {
			p.sharedDirtiedCount.Add(1)
			p.traceSlotEvent(s.idx, evMarkDirty, s.tag, old, newSt)
			return
		}
	}
}

// MarkDirtyHintBit marks s dirty for a hint-bit-only write. Unlike MarkDirty,
// it does NOT call maybeEmitFPI: hint bits are re-derived from pg_xact on
// crash recovery and are never WAL-logged. The page is flushed to disk at
// checkpoint time, persisting the cached bits for future restarts.
func (p *Pool) MarkDirtyHintBit(s *Slot) {
	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			return
		}
		if s.state.CompareAndSwap(old, old|slotDirtyBit) {
			p.sharedDirtiedCount.Add(1)
			return
		}
	}
}

// MarkDirtyWithLSN records an explicit page LSN and marks the slot dirty.
// Callers of the WithLSN variants log their own image-bearing multi-page
// records (e.g. btree split left+right+sibling images), so the LSN also
// advances the native-image watermark.
func (p *Pool) MarkDirtyWithLSN(s *Slot, lsn LSN) {
	s.contentMu.Lock()
	MustHeader(s.page).SetLSN(lsn)
	s.contentMu.Unlock()
	s.nativeImageLSN.Store(uint64(lsn))
	p.markDirtyWithLSNCommon(s)
}

// MarkDirtyWithLSNLocked is the variant for callers that already hold s.contentMu.
func (p *Pool) MarkDirtyWithLSNLocked(s *Slot, lsn LSN) {
	MustHeader(s.page).SetLSN(lsn)
	s.nativeImageLSN.Store(uint64(lsn))
	p.markDirtyWithLSNCommon(s)
}

func (p *Pool) markDirtyWithLSNCommon(s *Slot) {
	// Set dirty bit. The caller already stamped pd_lsn (> published redo),
	// which is what suppresses redundant images this epoch under the
	// pd_lsn<=redo test — no per-slot flag to maintain.
	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			break
		}
		newSt := old | slotDirtyBit
		if s.state.CompareAndSwap(old, newSt) {
			p.sharedDirtiedCount.Add(1)
			p.traceSlotEvent(s.idx, evMarkDirtyWithLSNLocked, s.tag, old, newSt)
			break
		}
	}
}

// MarkDirtyForceFPI emits a fresh full-page image, overriding any stale FPI.
func (p *Pool) MarkDirtyForceFPI(s *Slot) {
	if p.logFPI == nil || !p.fullPageWrites.Load() {
		for {
			old := s.state.Load()
			if old&slotDirtyBit != 0 {
				return
			}
			if s.state.CompareAndSwap(old, old|slotDirtyBit) {
				p.sharedDirtiedCount.Add(1)
				return
			}
		}
	}
	tag := s.tag

	pageCopy := make(Page, BlockSize)
	copy(pageCopy, s.page)
	lsn, err := p.logFPI(tag.Rel, tag.Block, pageCopy)
	if err != nil {
		for {
			old := s.state.Load()
			if old&slotDirtyBit != 0 {
				return
			}
			if s.state.CompareAndSwap(old, old|slotDirtyBit) {
				p.sharedDirtiedCount.Add(1)
				return
			}
		}
	}
	MustHeader(s.page).SetLSN(lsn)
	s.nativeImageLSN.Store(uint64(lsn))
	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			break
		}
		if s.state.CompareAndSwap(old, old|slotDirtyBit) {
			p.sharedDirtiedCount.Add(1)
			break
		}
	}
}

// MarkDirtyChangeRecord is the change-record-aware variant of MarkDirty.
func (p *Pool) MarkDirtyChangeRecord(s *Slot, emitter func() (LSN, error)) error {
	// RLock spans the FPI decision AND the record append so a concurrent
	// redo publication cannot land between them (PublishRedoBarrier).
	p.fpiPublishMu.RLock()
	err := func() error {
		needFPI := p.needsImage(s)
		tag := s.tag

		if needFPI {
			if p.logFPI != nil && p.fullPageWrites.Load() {
				pageCopy := make(Page, BlockSize)
				copy(pageCopy, s.page)
				lsn, err := p.logFPI(tag.Rel, tag.Block, pageCopy)
				if err != nil {
					return err
				}
				MustHeader(s.page).SetLSN(lsn)
				s.nativeImageLSN.Store(uint64(lsn))
			} else if emitter != nil {
				lsn, err := emitter()
				if err != nil {
					return err
				}
				MustHeader(s.page).SetLSN(lsn)
				// Legacy mode (no logFPI): the change record itself is the
				// replay anchor; treat it as the image watermark.
				s.nativeImageLSN.Store(uint64(lsn))
			}
		} else {
			if emitter == nil {
				return fmt.Errorf("MarkDirtyChangeRecord: emitter required after first dirty")
			}
			lsn, err := emitter()
			if err != nil {
				return err
			}
			MustHeader(s.page).SetLSN(lsn)
		}
		return nil
	}()
	p.fpiPublishMu.RUnlock()
	if err != nil {
		return err
	}

	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			break
		}
		newSt := old | slotDirtyBit
		if s.state.CompareAndSwap(old, newSt) {
			p.sharedDirtiedCount.Add(1)
			p.traceSlotEvent(s.idx, evMarkDirtyChangeRecord, s.tag, old, newSt)
			break
		}
	}
	return nil
}

// MarkDirtyLogicalChange is the logical-decoding-aware variant of MarkDirtyChangeRecord.
func (p *Pool) MarkDirtyLogicalChange(s *Slot, emitter func() (LSN, error)) error {
	if emitter == nil {
		return fmt.Errorf("MarkDirtyLogicalChange: emitter required")
	}
	// RLock spans decision + both appends (see MarkDirtyChangeRecord).
	p.fpiPublishMu.RLock()
	err := func() error {
		needFPI := p.needsImage(s)
		tag := s.tag

		lsn, err := emitter()
		if err != nil {
			return err
		}
		MustHeader(s.page).SetLSN(lsn)

		if needFPI && p.logFPI != nil && p.fullPageWrites.Load() {
			pageCopy := make(Page, BlockSize)
			copy(pageCopy, s.page)
			fpiLSN, fpiErr := p.logFPI(tag.Rel, tag.Block, pageCopy)
			if fpiErr != nil {
				return fpiErr
			}
			MustHeader(s.page).SetLSN(fpiLSN)
			s.nativeImageLSN.Store(uint64(fpiLSN))
		}
		return nil
	}()
	p.fpiPublishMu.RUnlock()
	if err != nil {
		return err
	}

	for {
		old := s.state.Load()
		if old&slotDirtyBit != 0 {
			break
		}
		if s.state.CompareAndSwap(old, old|slotDirtyBit) {
			p.sharedDirtiedCount.Add(1)
			break
		}
	}
	return nil
}

// maybeEmitFPI runs the FPI side-effect of MarkDirty for the first
// mutation of a page since the published redo pointer.
func (p *Pool) maybeEmitFPI(s *Slot) {
	if p.logFPI == nil || !p.fullPageWrites.Load() {
		return
	}
	// RLock spans decision + append (see MarkDirtyChangeRecord).
	p.fpiPublishMu.RLock()
	defer p.fpiPublishMu.RUnlock()
	if !p.needsImage(s) {
		return
	}

	tag := s.tag

	pageCopy := make(Page, BlockSize)
	copy(pageCopy, s.page)
	lsn, err := p.logFPI(tag.Rel, tag.Block, pageCopy)
	if err != nil {
		p.logger.Warn("full-page-image WAL emit failed",
			"rel", tag.Rel, "block", tag.Block, "err", err)
		return
	}
	MustHeader(s.page).SetLSN(lsn)
	// Advancing the native-image watermark above the published redo is what
	// suppresses further images until the next redo publication.
	s.nativeImageLSN.Store(uint64(lsn))
}

// FlushAll writes every dirty slot and clears the dirty bit.
func (p *Pool) FlushAll() error {
	if p.OnFlushAll != nil {
		p.OnFlushAll()
	}
	return p.FlushAllPaced(nil)
}

// FlushRel writes every dirty slot belonging to rel to its CURRENT on-disk
// location and clears their dirty bits, leaving the slots cached (unlike
// InvalidateRel, which discards dirty content instead of writing it — correct
// for DROP TABLE where the data is being deleted anyway, but wrong here).
// Used by ALTER TABLE/INDEX ... SET TABLESPACE's physical relocation
// (M0122-0007): the relation's file must reflect every buffered write before
// it is copied to the new tablespace path, otherwise the copy would silently
// drop whatever this session (or another) wrote but hasn't been evicted yet.
// Callers still need InvalidateRel afterward to drop the now-clean cached
// slots so the next access re-resolves rel's (possibly changed) tag.
func (p *Pool) FlushRel(rel RelFileNode) error {
	type pending struct {
		idx int
		tag BufferTag
	}
	var todo []pending
	for i := range p.slots {
		st := p.slots[i].state.Load()
		if stateValid(st) && stateDirty(st) && p.slots[i].tag.Rel == rel {
			todo = append(todo, pending{i, p.slots[i].tag})
		}
	}
	if len(todo) == 0 {
		return nil
	}
	batchSize := p.flushBatchSize()
	for start := 0; start < len(todo); start += batchSize {
		end := start + batchSize
		if end > len(todo) {
			end = len(todo)
		}
		batchSlots := make([]*Slot, 0, end-start)
		batchTags := make([]BufferTag, 0, end-start)
		for _, t := range todo[start:end] {
			batchSlots = append(batchSlots, &p.slots[t.idx])
			batchTags = append(batchTags, t.tag)
		}
		if err := p.flushBatch(batchSlots, batchTags); err != nil {
			return err
		}
	}
	return nil
}

// FlushAllPaced writes every dirty slot and invokes pacer after each batch.
func (p *Pool) FlushAllPaced(pacer func(progress float64) error) error {
	if p.OnFlushAll != nil {
		p.OnFlushAll()
	}
	type pending struct {
		idx int
		tag BufferTag
	}
	var todo []pending
	for i := range p.slots {
		st := p.slots[i].state.Load()
		if stateValid(st) && stateDirty(st) {
			todo = append(todo, pending{i, p.slots[i].tag})
		}
	}

	total := len(todo)
	if total == 0 {
		return nil
	}
	batchSize := p.flushBatchSize()
	for start := 0; start < total; start += batchSize {
		end := start + batchSize
		if end > total {
			end = total
		}
		batchSlots := make([]*Slot, 0, end-start)
		batchTags := make([]BufferTag, 0, end-start)
		for _, t := range todo[start:end] {
			batchSlots = append(batchSlots, &p.slots[t.idx])
			batchTags = append(batchTags, t.tag)
		}
		if err := p.flushBatch(batchSlots, batchTags); err != nil {
			return err
		}
		if pacer != nil {
			progress := float64(end) / float64(total)
			if err := pacer(progress); err != nil {
				return err
			}
		}
	}
	return nil
}

// flushBatch flushes a batch of dirty slots.
func (p *Pool) flushBatch(slots []*Slot, tags []BufferTag) error {
	for _, s := range slots {
		s.contentMu.RLock()
	}
	defer func() {
		for _, s := range slots {
			s.contentMu.RUnlock()
		}
	}()

	// FlushAllPaced's dirty-page scan reads each slot's tag via an unlocked
	// state.Load(), well before this batch reaches contentMu.RLock() here.
	// In that gap the slot can be evicted and repurposed for a different
	// relation/block entirely (claimVictim doesn't consult contentMu).
	// contentMu.RLock (held for the rest of this function) blocks any
	// further repurposing once acquired, so re-checking s.tag now is
	// race-free and authoritative for the remainder of the call. A
	// mismatch means tags[i] is stale: skip this slot rather than writing
	// its NEW content to the OLD tag's (rel, block) on disk — that silent
	// cross-relation write is the M-NIGHTLY btree keyLen-mismatch
	// corruption (heap page bytes landing in a btree index file), see
	// .ralph/deferral_ledger.md. Whoever now owns the slot's real tag is
	// responsible for flushing it through its own dirty-bit lifecycle.
	stale := make([]bool, len(slots))
	for i, s := range slots {
		stale[i] = s.tag != tags[i]
	}

	var maxLSN LSN
	for i, s := range slots {
		if stale[i] {
			continue
		}
		if lsn := MustHeader(s.page).LSN(); lsn > maxLSN {
			maxLSN = lsn
		}
	}

	if p.wal != nil && maxLSN != 0 {
		if err := p.wal.FlushUpTo(uint64(maxLSN)); err != nil {
			return fmt.Errorf("flush wal up to %d: %w", maxLSN, err)
		}
	}

	if p.OnCheckpointerWriteWait != nil {
		p.OnCheckpointerWriteWait()
	}
	handles := make([]AIOHandle, len(slots))
	for i, s := range slots {
		if stale[i] {
			continue
		}
		h, err := p.mgr.WriteBlockAIO(tags[i].Rel, tags[i].Block, s.page)
		if err != nil {
			for j := 0; j < i; j++ {
				if handles[j] != nil {
					_, _ = handles[j].Wait()
				}
			}
			if p.OnCheckpointerWriteDone != nil {
				p.OnCheckpointerWriteDone()
			}
			return err
		}
		handles[i] = h
	}

	var firstErr error
	for _, h := range handles {
		if h == nil {
			continue
		}
		if _, err := h.Wait(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if p.OnCheckpointerWriteDone != nil {
		p.OnCheckpointerWriteDone()
	}
	if firstErr != nil {
		return firstErr
	}

	// Clear dirty bits where tag still matches.
	for i, s := range slots {
		if stale[i] {
			continue
		}
		if s.tag == tags[i] {
			for {
				old := s.state.Load()
				if old&slotDirtyBit == 0 {
					break
				}
				if s.state.CompareAndSwap(old, old&^slotDirtyBit) {
					break
				}
			}
			p.sharedCheckpointWrittenCount.Add(1)
			p.accountCheckpointerWrite(tags[i].Rel)
		}
	}
	return nil
}

func (p *Pool) flushSlot(tag BufferTag, page Page) error {
	if p.wal != nil {
		lsn := MustHeader(page).LSN()
		if lsn != 0 {
			if err := p.wal.FlushUpTo(uint64(lsn)); err != nil {
				return fmt.Errorf("flush wal up to %d: %w", lsn, err)
			}
		}
	}
	if err := p.mgr.WriteBlock(tag.Rel, tag.Block, page); err != nil {
		return err
	}
	// OnFlushSnapshot fires AFTER WriteBlock succeeds (M-NIGHTLY
	// AI-20260708-064334-001, 9th loop) so its Seq reflects "durably
	// written" time, matching OnBlockReload's post-ReadBlock placement --
	// firing before the write (as this did through the 8th loop) let a
	// concurrent reload's real ReadAt land before this write's real
	// WriteAt while still being assigned a LOWER Seq, making Seq-ordered
	// flush-vs-reload comparisons unreliable.
	if p.OnFlushSnapshot != nil {
		p.OnFlushSnapshot(tag, page)
	}
	return nil
}

// maybeCompact triggers a tombstone compaction if the tombstone ratio exceeds 25%.
func (p *Pool) maybeCompact() {
	n := int64(len(p.slots))
	ts := p.tombstones.Load()
	if ts*4 < n {
		return
	}
	if !p.compactMu.TryLock() {
		return // another goroutine is already compacting
	}
	defer p.compactMu.Unlock()
	p.bm.compact()
	p.tombstones.Store(0)
}

// DirtyVictimRate returns the fraction of foreground evictions that
// encountered a dirty page.
func (p *Pool) DirtyVictimRate() float64 {
	total := p.totalVictimCount.Sum()
	if total == 0 {
		return 0
	}
	return float64(p.dirtyVictimCount.Sum()) / float64(total)
}

// ResetVictimStats resets the dirty-victim counters to zero.
func (p *Pool) ResetVictimStats() {
	p.dirtyVictimCount.Reset()
	p.totalVictimCount.Reset()
}

// WriteDirtyPages proactively flushes up to maxPages dirty slots.
// Called only by the bgwriter goroutine.
func (p *Pool) WriteDirtyPages(maxPages int) int {
	if maxPages <= 0 {
		return 0
	}

	type victim struct {
		idx int
		tag BufferTag
	}
	n := len(p.slots)

	p.bgwriterMu.Lock()
	start := p.bgwriterHand
	p.bgwriterHand = (start + n) % n
	p.bgwriterMu.Unlock()

	victims := make([]victim, 0, maxPages)
	for i := 0; i < n && len(victims) < maxPages; i++ {
		idx := (start + i) % n
		s := &p.slots[idx]
		st := s.state.Load()
		if stateValid(st) && stateDirty(st) && statePin(st) == 0 {
			victims = append(victims, victim{idx: idx, tag: s.tag})
		}
	}

	written := 0
	for _, v := range victims {
		s := &p.slots[v.idx]
		s.contentMu.RLock()

		// Re-check under RLock that the slot is still dirty with same tag.
		st := s.state.Load()
		stillValid := stateValid(st) && s.tag == v.tag && stateDirty(st) && statePin(st) == 0

		if stillValid {
			if p.OnBgwriterWriteWait != nil {
				p.OnBgwriterWriteWait()
			}
			err := p.flushSlot(v.tag, s.page)
			if p.OnBgwriterWriteDone != nil {
				p.OnBgwriterWriteDone()
			}
			if err == nil {
				if s.tag == v.tag {
					for {
						old := s.state.Load()
						if old&slotDirtyBit == 0 {
							break
						}
						if s.state.CompareAndSwap(old, old&^slotDirtyBit) {
							break
						}
					}
					written++
					p.sharedBgwriterWrittenCount.Add(1)
					p.accountBgwriterWrite(v.tag.Rel)
				}
			}
		}
		s.contentMu.RUnlock()
	}
	return written
}
