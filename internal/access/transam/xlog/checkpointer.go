package xlog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/utils/activity/stats"
)

// DirtyPageFlusher is the buffer-pool contract used by the checkpointer.
type DirtyPageFlusher interface {
	FlushAll() error
}

// pacedFlusher is implemented by *storage.Pool so the
// checkpointer can spread dirty-page writeback over a target
// wall-clock window (mirrors upstream's
// checkpoint_completion_target). Optional — flushers that don't
// satisfy it fall back to FlushAll, matching v0 behaviour.
type pacedFlusher interface {
	FlushAllPaced(pacer func(progress float64) error) error
}

// redoPublisher is implemented by *storage.Pool so the checkpointer can
// publish the redo pointer at checkpoint START (before the buffer flush) —
// PG CreateCheckPoint order. Publication is the FPI epoch boundary: the
// pool's per-record pd_lsn<=redo test keys on it, replacing the old
// post-checkpoint ResetCheckpointEpoch sweep whose reset-after-redo-sample
// ordering left an image-less replay window (perf-optimize3-dash/03).
// Optional — checkpointers constructed in tests with a flusher that doesn't
// satisfy this interface keep working.
type redoPublisher interface {
	PublishRedoBarrier(sample func() uint64) uint64
}

// dataFileSyncer is implemented by anything that can fdatasync every
// open data file (e.g. *storage.Pool wrapping *storage.Manager.SyncAll).
// Optional — tests that use stub flushers without storage backing
// keep working; only production checkpoints need durability across
// power loss. M0089-0001.
type dataFileSyncer interface {
	SyncAllDataFiles() error
}

type checkpointWAL interface {
	Append(payload []byte) (uint64, uint64, error)
	FlushUpTo(lsn uint64) error
}

// volumeReporter is implemented by a *Writer so the checkpointer
// can drive the max_wal_size trigger without coupling to the
// concrete type. checkpointWAL stays narrow for tests that don't
// exercise the volume trigger.
type volumeReporter interface {
	WrittenLSN() uint64
}

// CheckpointerConfig controls checkpoint cadence and logging.
// GUCParameters holds the 8 GUC fields that PostgreSQL broadcasts via
// XLOG_PARAMETER_CHANGE so standbys can enforce compatibility via
// CheckRequiredParameterValues (src/backend/access/transam/xlog.c).
// Mirrors `xl_parameter_change` in src/include/access/xlog_internal.h.
type GUCParameters struct {
	MaxConnections     int32
	MaxWorkerProcesses int32
	MaxWalSenders      int32
	MaxPreparedXacts   int32
	MaxLocksPerXact    int32
	// WalLevel: 0=minimal, 1=replica, 2=logical (matches PG's WalLevel enum).
	WalLevel             int32
	WalLogHints          bool
	TrackCommitTimestamp bool
}

// DefaultGUCParameters returns the standard GUC values for a goopg primary
// that matches a PG18 initdb configuration.
func DefaultGUCParameters() GUCParameters {
	return GUCParameters{
		MaxConnections:       100,
		MaxWorkerProcesses:   8,
		MaxWalSenders:        10,
		MaxPreparedXacts:     0,
		MaxLocksPerXact:      64,
		WalLevel:             1, // replica — minimum for streaming replication
		WalLogHints:          false,
		TrackCommitTimestamp: false,
	}
}

type CheckpointerConfig struct {
	// Interval is the period between automatic checkpoints
	// (mirrors upstream's checkpoint_timeout).
	Interval time.Duration
	// MaxWALBytes, when > 0, is upstream's max_wal_size expressed in
	// bytes. It is NOT the trigger distance: like PG, the distance is
	// derived from it by CalculateCheckpointSegments — see
	// checkpointSegments/volumeTriggerFires. Requires the underlying
	// writer to satisfy volumeReporter; otherwise the trigger is a
	// no-op.
	MaxWALBytes uint64
	// VolumeCheckInterval is how often the loop polls
	// WrittenLSN to evaluate the volume trigger between timer
	// ticks. Defaults to 1s when MaxWALBytes is set.
	VolumeCheckInterval time.Duration
	// CompletionTarget is the fraction of Interval over which
	// timer-driven checkpoints spread their dirty-page writeback
	// (mirrors upstream's checkpoint_completion_target). 0
	// disables spreading; values are clamped to [0, 1]. Volume-
	// triggered and SQL-triggered (CheckpointNow) checkpoints
	// always run at IMMEDIATE speed and ignore this knob.
	CompletionTarget float64
	Logger           *slog.Logger
	// SegmentSize is the WAL segment size (default 16 MiB). Only used
	// by EncodeCheckpointCompat to compute the correct first-page
	// header size when encoding a PG-compatible checkpoint record.
	SegmentSize int64
	// DataDir, when non-empty, causes each successful checkpoint to
	// update <DataDir>/global/pg_control via control.UpdateControlFile.
	// When empty the pg_control update is skipped (tests that don't
	// write a real data directory leave this blank).
	DataDir string

	// GUCParams holds the 8 GUC echo fields that are written into pg_control
	// at each checkpoint so a PG standby always reads consistent values.
	// Populated by initdb.Open from DefaultGUCParameters() at server start.
	// M0106-0010 batched-33.
	GUCParams GUCParameters

	// NextXIDFn, when non-nil, is invoked at each checkpoint to read the
	// live "next-to-assign" XID from the MVCC manager so the
	// checkpointCopy.nextXid field in pg_control reflects current XID
	// consumption. Without this hook the field stays at the bootstrap
	// value (FirstNormalTransactionId = 3) and a PG standby attached via
	// basebackup boots with snapshot xmax=3, hiding every tuple created
	// after initdb. M0106-0010 batched-45.
	NextXIDFn func() uint64

	// NextOIDFn, when non-nil, is invoked at each checkpoint to read the
	// live "next-to-assign" OID from the catalog so the
	// checkpointCopy.nextOid field in pg_control and the checkpoint WAL
	// record reflect current OID consumption. Without this hook the
	// field stays at the hardcoded bootstrap value (FirstNormalObjectId =
	// 16384) and a crashed cluster that had allocated OIDs above 16384
	// cannot recover the OID counter from pg_control alone, requiring
	// pg_catalog.json. M0106-0013.
	NextOIDFn func() uint32

	// TimelineIDFn, when non-nil, is invoked at each checkpoint to read
	// the LIVE timeline the WAL writer is emitting on (M0131-S18.3).
	// Both the checkpoint record's ThisTimeLineID/PrevTimeLineID and
	// pg_control's checkPointCopy used to be hardcoded to 1, so the first
	// checkpoint after a promotion (M0130-S8.5 finalizePromotion) stomped
	// pg_control back to TLI 1 while the segments on disk were named for
	// TLI 2 — a real PG then PANICs "could not locate a valid checkpoint
	// record". Wired to Writer.TimelineID by initdb.Open; nil keeps the
	// historical TLI 1. PrevTimeLineID mirrors ThisTimeLineID, matching
	// upstream CreateCheckPoint for every non-end-of-recovery checkpoint
	// (xlog.c:7030-7034).
	TimelineIDFn func() uint32

	// FullPageWritesFn, when non-nil, reports the live full_page_writes
	// setting as the buffer pool sees it — upstream samples
	// Insert->fullPageWrites under the WAL insert lock (xlog.c:7041), not
	// the postgresql.conf text, because a runtime ALTER SYSTEM takes
	// effect only at the next XLOG_FPW_CHANGE. The GUC reaches the pool
	// (cmd/goopg/main.go, Pool.SetFullPageWrites) but never reached
	// pg_control or the checkpoint record before M0131-S18.3. nil keeps
	// the historical unconditional `on`.
	FullPageWritesFn func() bool

	// NextMultiXactFn, when non-nil, reports the multixact allocator's
	// next-to-assign MultiXactId, its next member offset, and the oldest
	// MultiXactId still needed. goopg's store never truncates, so oldest
	// stays FirstMultiXactId; the seam exists so pg_control stops
	// claiming nextMulti = 1 on a cluster that has created multixacts
	// (M0131-S18.4). nil keeps FirstMultiXactId/0/FirstMultiXactId.
	NextMultiXactFn func() (next, nextOffset, oldest uint32)

	// PostCheckpointFn, when non-nil, is called at the end of each
	// successful checkpoint (timed, volume-triggered, or on-demand).
	// initdb.Open wires this to regenerate pg_internal.init files so PG
	// standbys can always attach: crash-recovery WAL replay of
	// RecordKindXactCommitInval unlinks the init files, and the commit-
	// time hook may not run before the next pg_basebackup attempt.
	// Errors are logged as warnings and do not fail the checkpoint.
	// M0106-0011 follow-up (b).
	PostCheckpointFn func() error

	// TruncateCLOGFn, when non-nil, is called at the end of each successful
	// checkpoint AFTER the checkpoint marker is durable, to truncate CLOG
	// (pg_xact) up to the conservative oldest-safe XID (G1). Wiring truncation
	// here makes it durable-ordered: the checkpoint that records the advanced
	// redo point is on disk before any clog segment is removed, so a crash
	// between the two leaves recoverable state. The callee is responsible for
	// computing a conservative horizon (never above OldestXmin or any live
	// snapshot's xmin) and for emitting the CLOG_TRUNCATE WAL record. Errors
	// are logged as warnings and do not fail the checkpoint. G1.
	TruncateCLOGFn func() error

	// TruncateSubtransFn, when non-nil, is called at the end of each
	// successful checkpoint AFTER the checkpoint marker is durable, alongside
	// TruncateCLOGFn, to truncate pg_subtrans (both the in-memory subxact map
	// and its on-disk SLRU mirror) up to the same conservative horizon
	// (M0122-0009). Without this, a long-lived cluster's subxact parent-link
	// tracking grows without bound. The callee is responsible for computing a
	// conservative horizon (never above any live snapshot's xmin); no WAL
	// record is emitted (matches upstream — pg_subtrans truncation is not
	// WAL-logged). Errors are logged as warnings and do not fail the
	// checkpoint, same treatment as TruncateCLOGFn.
	TruncateSubtransFn func() error

	// FlushCLOGFn, when non-nil, is called during the dirty-page flush phase
	// of every checkpoint (timed, volume-triggered, or on-demand) — in the
	// same phase as, and before, the primary buffer-pool flush's redo LSN is
	// sampled — to drain the CLOG buffer pool's dirty pages. This bounds how
	// long an async commit's deferred CLOG write-back
	// (mvcc.CLog.setStatusWithLSN, M0117-0007 Part B continuation) can stay
	// unflushed: without it, a CLOG page dirtied by synchronous_commit=off
	// commits would rely solely on a later synchronous commit's group flush
	// or LRU eviction, unbounded on an all-async workload. Wired to
	// mvcc.CLog.FlushAll. Unlike PostCheckpointFn/TruncateCLOGFn, an error
	// here fails the checkpoint (same treatment as the primary flush): a
	// checkpoint whose redo LSN advances past commits whose CLOG state
	// failed to flush would leave crash recovery unable to reconstruct that
	// state (recovery only replays WAL from the checkpoint's redo LSN
	// onward).
	FlushCLOGFn func() error

	// PGCompatCheckpoints, when true, writes an 88-byte PG18 CheckPoint
	// struct with the EXPLICIT checkpoint opcode (EncodeCheckpointPG:
	// XLOG_CHECKPOINT_SHUTDOWN for shutdown checkpoints, ONLINE otherwise —
	// A9-checkpoint-opcode), preceding every ONLINE checkpoint with an
	// XLOG_RUNNING_XACTS record (upstream LogStandbySnapshot, xlog.c:7243)
	// so a hot-standby PG reaches STANDBY_SNAPSHOT_READY. When false
	// (default), the legacy 1-byte RecordKindCheckpoint marker is used.
	// Set to true by initdb.Open; tests that do not need PG compatibility
	// leave this false so legacy detection code works.
	PGCompatCheckpoints bool

	// RunningXactsFn, when set alongside PGCompatCheckpoints, supplies the
	// running-transaction snapshot stamped into the XLOG_RUNNING_XACTS
	// record before an ONLINE checkpoint and into CheckPoint.oldestActiveXid:
	// the running top-level xids, the oldest running xid (nextXid when none),
	// and latestCompletedXid (nextXid-1 when idle). Wired to the mvcc
	// manager's snapshot by initdb.Open. When nil, the checkpointer
	// synthesises the idle-system values from NextXIDFn — correct for the
	// bootstrap/immediate checkpoints that run with no user xact in flight.
	RunningXactsFn func() (xids []uint32, oldestRunning, latestCompleted uint32)

	// OnLoopStart/OnLoopEnd, when set, bracket Run's entire timer/volume
	// loop (called once each, not per-checkpoint) so a caller can register
	// this goroutine's identity with the activity registry the same way
	// wal.Config's OnLoopStart/OnLoopEnd let initdb.Open track the WAL
	// writer goroutine (walProcNum). SQL-triggered checkpoints
	// (CheckpointNow, called directly from a client backend's own
	// goroutine) do not go through Run and so are unaffected — they keep
	// attributing to the calling backend's own already-registered identity.
	// Without this the timer/volume-driven checkpointer's dirty-page
	// flushes (flushDirty → accountCheckpointerWrite) have no registered
	// goroutine to attribute a wait event to (M0122-0003 writeback
	// simplification 3 follow-up).
	OnLoopStart func()
	OnLoopEnd   func()
}

func (c *CheckpointerConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 10 * time.Second
	}
	if c.MaxWALBytes > 0 && c.VolumeCheckInterval <= 0 {
		c.VolumeCheckInterval = time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Checkpointer periodically flushes dirty pages and writes a
// checkpoint marker to WAL.
type Checkpointer struct {
	flusher DirtyPageFlusher
	wal     checkpointWAL
	cfg     CheckpointerConfig

	// retainer, when non-nil, runs after each successful
	// checkpoint marker is durable. The slot-aware production
	// implementation lives in retention.go; tests can wire a
	// fake. nil disables WAL pruning entirely (the v0 default —
	// caller opts in by passing SetRetainer).
	retainer Retainer

	lastCheckpointLSN      atomic.Uint64
	lastCheckpointRedoLSN  atomic.Uint64
	lastCheckpointStartLSN atomic.Uint64

	// volumeAnchor is the fallback RedoRecPtr for the max_wal_size
	// trigger before this process has completed its first checkpoint:
	// the writer position observed when Run started. Upstream always has
	// a RedoRecPtr — recovery seeds it from the checkpoint it started
	// from — so the elapsed-segment count is measured from "where this
	// server began writing" rather than from LSN 0. Anchoring at 0 would
	// make every restart of a cluster whose WAL has already passed
	// max_wal_size fire a checkpoint on the first poll (M0131-S30.4).
	volumeAnchor atomic.Uint64

	// distMu guards priorRedoLSN/checkPointDistanceEstimate. Checkpoints
	// are infrequent (seconds-to-minutes apart) so a plain mutex — rather
	// than lock-free float CAS — is fine here.
	distMu                     sync.Mutex
	priorRedoLSN               uint64
	checkPointDistanceEstimate float64

	// Aggregate counters surfaced through pg_stat_checkpointer.
	// Mirror the upstream PG 18 view's counter shape:
	// num_timed     — timer-driven cycles
	// num_requested — SQL CHECKPOINT, CLI ctl, max_wal_size volume
	// write_time_ms — cumulative wall time inside flushDirty
	// statsResetAt  — timestamp of the last counter reset
	//                 (currently only set at construction)
	numTimed     stats.Counter
	numRequested stats.Counter
	writeTimeMs  stats.Counter
	statsResetAt atomic.Int64 // unix nanos
}

// Stats is the snapshot pg_stat_checkpointer renders into a row.
type Stats struct {
	NumTimed          uint64
	NumRequested      uint64
	WriteTimeMs       uint64
	LastCheckpointLSN uint64
	StatsResetAt      time.Time
}

// Stats returns a coherent counter snapshot. Atomic loads only —
// the result is a point-in-time view, fine for the
// pg_stat_checkpointer virtual table.
func (c *Checkpointer) Stats() Stats {
	return Stats{
		NumTimed:          uint64(c.numTimed.Sum()),
		NumRequested:      uint64(c.numRequested.Sum()),
		WriteTimeMs:       uint64(c.writeTimeMs.Sum()),
		LastCheckpointLSN: c.lastCheckpointLSN.Load(),
		StatsResetAt:      time.Unix(0, c.statsResetAt.Load()),
	}
}

// NewCheckpointer constructs a checkpointer worker.
func NewCheckpointer(flusher DirtyPageFlusher, wal checkpointWAL, cfg CheckpointerConfig) *Checkpointer {
	cfg.withDefaults()
	c := &Checkpointer{
		flusher: flusher,
		wal:     wal,
		cfg:     cfg,
	}
	c.statsResetAt.Store(time.Now().UnixNano())
	return c
}

// LastCheckpointLSN returns the most recent successful checkpoint marker LSN.
func (c *Checkpointer) LastCheckpointLSN() uint64 {
	return c.lastCheckpointLSN.Load()
}

// LastCheckpointRedoLSN returns the REDO LSN for the most recent successful
// checkpoint — the position published at checkpoint START, from which crash
// recovery must replay. Exported for BASE_BACKUP's pg_control update path
// (M0102-0007).
func (c *Checkpointer) LastCheckpointRedoLSN() uint64 {
	return c.lastCheckpointRedoLSN.Load()
}

// LastCheckpointRecordLSN returns the START LSN (1-based) of the most recent
// checkpoint RECORD itself — the value backup_label's CHECKPOINT LOCATION and
// pg_control's CheckPoint field must carry. A9-checkpoint-opcode made this
// distinct from the redo point: an ONLINE checkpoint's record is preceded by
// an XLOG_RUNNING_XACTS record (and, in general, by any records appended
// after redo was captured), so a standby that looks for the checkpoint AT
// redo finds the wrong record ("invalid resource manager ID in checkpoint
// record"). Returns 0 if no checkpoint has completed yet.
func (c *Checkpointer) LastCheckpointRecordLSN() uint64 {
	return c.lastCheckpointStartLSN.Load()
}

// updateCheckPointDistanceEstimate mirrors upstream's
// UpdateCheckPointDistanceEstimate (xlog.c): a moving average of the WAL
// bytes generated between successive checkpoints' redo LSNs, consulted by
// SlotAwareRetainer to size WAL-segment recycling (XLOGfileslop, M0122-0009
// follow-up). The average declines slowly when a cycle used less WAL than
// estimated, but jumps immediately when a cycle used more — biased toward
// the peak load rather than a plain average, so a bursty workload isn't
// underestimated right after a quiet period. The very first checkpoint
// (priorRedoLSN still 0) has nothing to compare against and leaves the
// estimate at its zero value.
func (c *Checkpointer) updateCheckPointDistanceEstimate(redoLSN uint64) {
	c.distMu.Lock()
	defer c.distMu.Unlock()
	prior := c.priorRedoLSN
	c.priorRedoLSN = redoLSN
	if prior == 0 || redoLSN <= prior {
		return
	}
	nbytes := float64(redoLSN - prior)
	if c.checkPointDistanceEstimate < nbytes {
		c.checkPointDistanceEstimate = nbytes
	} else {
		c.checkPointDistanceEstimate = 0.90*c.checkPointDistanceEstimate + 0.10*nbytes
	}
}

// CheckPointDistanceEstimate returns the current moving-average estimate
// (bytes) of inter-checkpoint WAL volume. Exported so SlotAwareRetainer can
// size WAL-segment recycling; 0 before the second checkpoint of a run
// completes (no distance to measure yet).
func (c *Checkpointer) CheckPointDistanceEstimate() float64 {
	c.distMu.Lock()
	defer c.distMu.Unlock()
	return c.checkPointDistanceEstimate
}

// CheckpointRedoLSN is the executor.Checkpointer interface method.
func (c *Checkpointer) CheckpointRedoLSN() uint64 {
	return c.lastCheckpointRedoLSN.Load()
}

// SetInterval updates the periodic checkpoint cadence. Call before
// Run starts; once Run is in flight the ticker is already armed
// against the original interval and a change here only affects a
// subsequent Run invocation. The control-socket reload path will
// hook into this once it observes the GUC registry.
func (c *Checkpointer) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	c.cfg.Interval = d
}

// SetMaxWALBytes updates the volume-driven trigger threshold,
// mirroring upstream's max_wal_size. Call before Run starts.
// Zero disables the trigger.
func (c *Checkpointer) SetMaxWALBytes(b uint64) {
	c.cfg.MaxWALBytes = b
	if b > 0 && c.cfg.VolumeCheckInterval <= 0 {
		c.cfg.VolumeCheckInterval = time.Second
	}
}

// SetRetainer installs the post-marker WAL pruning hook. nil
// disables pruning (the v0 default). Production wiring passes a
// *SlotAwareRetainer that consults the replication-slot registry.
// Call before Run starts.
func (c *Checkpointer) SetRetainer(r Retainer) {
	c.retainer = r
}

// SetNextMultiXactFn installs the multixact-counter hook (M0131-S18.4).
// A setter rather than a CheckpointerConfig literal because the
// process-shared multixact store is created in cmd/goopg after
// initdb.Open has already built the checkpointer. Call before Run starts.
func (c *Checkpointer) SetNextMultiXactFn(fn func() (next, nextOffset, oldest uint32)) {
	c.cfg.NextMultiXactFn = fn
}

// SetCompletionTarget updates the spread fraction (0 disables
// spreading, 1 spreads across the full Interval). Out-of-range
// values are clamped.
func (c *Checkpointer) SetCompletionTarget(t float64) {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	c.cfg.CompletionTarget = t
}

// Run starts the periodic checkpoint loop and returns when ctx is canceled.
func (c *Checkpointer) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	// volumeTicker is non-nil only when MaxWALBytes is set AND
	// the writer can report its written LSN. Polling once a
	// second between timeout ticks is fine — checkpoints take
	// far longer than that.
	var volumeC <-chan time.Time
	vr, _ := c.wal.(volumeReporter)
	if c.cfg.MaxWALBytes > 0 && vr != nil {
		// Seed the pre-first-checkpoint RedoRecPtr stand-in — see
		// volumeAnchor's doc comment.
		c.volumeAnchor.Store(vr.WrittenLSN())
		vt := time.NewTicker(c.cfg.VolumeCheckInterval)
		defer vt.Stop()
		volumeC = vt.C
	}

	// The hooks fire only once the loop is fully ARMED — after the
	// volume anchor is seeded, not before it (AI-20260813-005117-008).
	// The anchor is read from the writer inside this goroutine while the
	// caller that spawned Run keeps appending, so "Run has started" is
	// only an observable, useful fact once the anchor is stored: a waiter
	// that unblocks earlier can still race its own appends ahead of the
	// seed and be absorbed into the anchor, which silently widens the
	// first max_wal_size window. Production semantics are unchanged —
	// both hooks still bracket the entire timer/volume loop.
	if c.cfg.OnLoopStart != nil {
		c.cfg.OnLoopStart()
	}
	if c.cfg.OnLoopEnd != nil {
		defer c.cfg.OnLoopEnd()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.runCheckpoint(ctx, true, false); err != nil {
				c.cfg.Logger.Warn("checkpoint failed", "err", err)
			}
		case <-volumeC:
			if !c.volumeTriggerFires(vr) {
				continue
			}
			// Volume-triggered checkpoints run at IMMEDIATE
			// speed: max_wal_size is a backpressure signal,
			// not a cadence knob.
			if err := c.runCheckpoint(ctx, false, false); err != nil {
				c.cfg.Logger.Warn("volume-triggered checkpoint failed", "err", err)
			}
		}
	}
}

// segmentSize reports the WAL segment size this checkpointer reasons in,
// falling back to the 16 MiB default when the config leaves it unset.
func (c *Checkpointer) segmentSize() uint64 {
	if c.cfg.SegmentSize > 0 {
		return uint64(c.cfg.SegmentSize)
	}
	return uint64(DefaultSegmentSize)
}

// checkpointSegments mirrors upstream's CalculateCheckpointSegments
// (xlog.c:2170-2198): max_wal_size is the ceiling on WAL kept for one
// checkpoint cycle, and a spread checkpoint itself consumes
// checkpoint_completion_target more segments while it runs, so the trigger
// distance is max_wal_size / (1 + checkpoint_completion_target) — NOT
// max_wal_size itself. Rounded down, floored at 1, exactly like upstream.
//
// M0131-S30.4: goopg used the raw byte value, so at the shared PG defaults
// (max_wal_size = 1 GiB, checkpoint_completion_target = 0.9, 16 MiB
// segments) it checkpointed every 1024 MiB where PG checkpoints every
// 32 segments = 512 MiB — half as often as the oracle under identical
// settings.
func (c *Checkpointer) checkpointSegments() uint64 {
	segs := uint64(float64(c.cfg.MaxWALBytes/c.segmentSize()) / (1.0 + c.completionTargetForSegments()))
	if segs < 1 {
		segs = 1
	}
	return segs
}

// completionTargetForSegments clamps CompletionTarget into [0,1] for the
// segment math. SetCompletionTarget already clamps, but the field is also
// settable directly through a CheckpointerConfig literal.
func (c *Checkpointer) completionTargetForSegments() float64 {
	t := c.cfg.CompletionTarget
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

// volumeTriggerFires reports whether enough WAL segments have elapsed since
// the last checkpoint's REDO point to warrant a new checkpoint, mirroring
// upstream's XLogCheckpointNeeded (xlog.c:2279-2289):
//
//	new_segno >= old_segno + (CheckPointSegments - 1)
//
// with old_segno taken from RedoRecPtr. Two deviations were fixed in
// M0131-S30.4: the distance is now CheckPointSegments (see
// checkpointSegments), and the anchor is the checkpoint's redo point rather
// than the checkpoint RECORD's end LSN. The record trails redo by the whole
// flush phase plus the running-xacts record, so anchoring on it silently
// shortened every window after the first by that gap.
func (c *Checkpointer) volumeTriggerFires(vr volumeReporter) bool {
	if c.cfg.MaxWALBytes == 0 {
		return false
	}
	written := vr.WrittenLSN()
	segSize := c.segmentSize()
	// lastCheckpointRedoLSN is stored 1-based (internal convention);
	// convert back to the 0-based byte position segment numbers use.
	anchor := c.volumeAnchor.Load()
	if redo := c.lastCheckpointRedoLSN.Load(); redo > 0 {
		anchor = redo - 1
	}
	newSegNo := written / segSize
	oldSegNo := anchor / segSize
	if newSegNo < oldSegNo {
		return false
	}
	return newSegNo-oldSegNo >= c.elapsedSegmentsNeeded()
}

// elapsedSegmentsNeeded is XLogCheckpointNeeded's `CheckPointSegments - 1`
// with a floor of one elapsed segment. The floor exists because of WHERE
// the two engines evaluate the test: upstream calls XLogCheckpointNeeded
// from XLogWrite only when it has just opened a NEW segment
// (xlog.c:2415-2440), so at CheckPointSegments == 1 — any max_wal_size
// below two segments' worth — it still checkpoints once per segment.
// goopg instead polls on VolumeCheckInterval, where a bare
// `elapsed >= 0` is satisfied on every tick: measured on a 48 MB
// max_wal_size cluster, that produced 42 checkpoints in 40 s of pgbench,
// most of them against WAL that had not advanced a single segment
// (M0131-S30.4).
func (c *Checkpointer) elapsedSegmentsNeeded() uint64 {
	if segs := c.checkpointSegments(); segs > 1 {
		return segs - 1
	}
	return 1
}

// CheckpointNow performs a synchronous, IMMEDIATE-speed
// checkpoint and returns when the marker is durable on disk.
// The SQL `CHECKPOINT` verb and the `goopg ctl` checkpoint
// subcommand both call this. Spread/throttling is bypassed;
// the caller asked for fast.
func (c *Checkpointer) CheckpointNow() error {
	return c.runCheckpoint(context.Background(), false, false)
}

// CheckpointShutdown runs a synchronous shutdown checkpoint: identical
// to CheckpointNow except it stamps pg_control's State field with
// DB_SHUTDOWNED instead of DB_IN_PRODUCTION. This mirrors upstream's
// CreateCheckPoint(CHECKPOINT_IS_SHUTDOWN) path (xlog.c), which sets
// ControlFile->state = DB_SHUTDOWNED once a shutdown checkpoint
// completes. It MUST be the last checkpoint of a clean shutdown (goopg
// wires it into Runtime.Close, the final durable checkpoint before WAL
// is closed) so external tools — pg_resetwal, pg_rewind, pg_controldata
// — observe a cleanly-shut-down cluster. goopg's own startup replays
// WAL regardless of State, so this is primarily an on-disk
// compatibility surface (M0110-0004 / RW-002).
func (c *Checkpointer) CheckpointShutdown() error {
	return c.runCheckpoint(context.Background(), false, true)
}

// runCheckpoint executes one checkpoint cycle. When `spread` is
// true and CompletionTarget > 0, dirty-page writeback is paced so
// it finishes near `start + Interval * CompletionTarget`. The WAL
// marker is appended after writeback completes and is always
// flushed synchronously — pacing only governs the dirty-page
// drain.
//
// `spread` also classifies the checkpoint for pg_stat_checkpointer:
// timer-driven cycles set `spread=true` and bump num_timed; SQL
// CHECKPOINT / CLI ctl / volume-triggered cycles set `spread=false`
// and bump num_requested. write_time_ms accumulates the wall time
// spent in flushDirty, matching upstream's checkpointer view.
// runningXacts returns the running-transaction snapshot for the
// XLOG_RUNNING_XACTS record and CheckPoint.oldestActiveXid. Falls back to
// the idle-system synthesis (no xids, oldestRunning = nextXid,
// latestCompleted = nextXid-1 — what PG's GetRunningTransactionData reports
// with an empty procarray) when no RunningXactsFn is wired.
func (c *Checkpointer) runningXacts(nextXid uint64) ([]uint32, uint32, uint32) {
	if c.cfg.RunningXactsFn != nil {
		return c.cfg.RunningXactsFn()
	}
	if nextXid < 3 {
		nextXid = 3 // FirstNormalTransactionId floor, mirrors EncodeCheckpointPG
	}
	return nil, uint32(nextXid), uint32(nextXid - 1)
}

// timelineID reports the timeline this checkpoint belongs to (M0131-S18.3).
// Falls back to 1 — the bootstrap timeline — when no hook is wired, which
// is what every caller got unconditionally before the hook existed.
func (c *Checkpointer) timelineID() uint32 {
	if c.cfg.TimelineIDFn == nil {
		return 1
	}
	if tli := c.cfg.TimelineIDFn(); tli != 0 {
		return tli
	}
	return 1
}

// fullPageWrites reports the live full_page_writes setting (M0131-S18.3).
// Defaults to true, matching both the GUC's BootVal and the historical
// hardcoded value.
func (c *Checkpointer) fullPageWrites() bool {
	if c.cfg.FullPageWritesFn == nil {
		return true
	}
	return c.cfg.FullPageWritesFn()
}

// nextMultiXact reports the multixact allocator state for the checkpoint
// (M0131-S18.4). Without a hook it reports the bootstrap triple, which is
// what the encoder hardcoded before.
func (c *Checkpointer) nextMultiXact() (next, nextOffset, oldest uint32) {
	if c.cfg.NextMultiXactFn == nil {
		return firstMultiXactId, 0, firstMultiXactId
	}
	return c.cfg.NextMultiXactFn()
}

func (c *Checkpointer) runCheckpoint(ctx context.Context, spread, shutdown bool) error {
	start := time.Now()
	checkpointType := "requested"
	if spread {
		checkpointType = "scheduled"
	}
	// M0057-0001: log checkpoint start so benchmark runs can confirm
	// whether the checkpointer fires mid-measurement.
	c.cfg.Logger.Info("checkpoint start", "type", checkpointType)
	pacer := c.buildPacer(ctx, spread, start)

	// perf-optimize3-dash/03: compute AND PUBLISH the redo pointer BEFORE
	// the buffer flush (PG CreateCheckPoint order). The published pointer is
	// the FPI-epoch boundary for the pool's pd_lsn<=redo image test; the
	// flush below then trivially covers everything <= redo, and every page's
	// first post-redo modification carries a fresh image — closing the
	// image-less replay window the old post-record epoch reset left open.
	computeRedo := func() uint64 {
		pos := int64(0)
		if vr, ok := c.wal.(volumeReporter); ok {
			pos = int64(vr.WrittenLSN())
		}
		segSize := c.cfg.SegmentSize
		if segSize <= 0 {
			segSize = DefaultSegmentSize
		}
		leading := 0
		if pos%XLOGBlockSize == 0 {
			leading = SizeOfXLogShortPHD
			if segSize > 0 && pos%segSize == 0 {
				leading = SizeOfXLogLongPHD
			}
		}
		return uint64(pos + int64(leading))
	}
	// A9-checkpoint-opcode: an ONLINE PG-compat checkpoint determines its redo
	// point by INSERTING an XLOG_CHECKPOINT_REDO record — the record's start
	// LSN becomes the redo pointer, exactly like upstream CreateCheckPoint
	// (xlog.c:7087-7099). PG17+ recovery validates that the record found at
	// CheckPoint.redo is this record whenever redo < the checkpoint record's
	// own LSN. The append happens INSIDE the redo barrier's critical section:
	// FPI-decision sections hold fpiPublishMu.RLock across decide->append, so
	// with the write side held here no torn-page-relevant record can land
	// between the sampled redo and the published one — appending the marker
	// under the same lock makes "redo == marker start" atomic. Lock order is
	// preserved (fpiPublishMu -> WAL stripe, the FPI sections' own order) and
	// the marker itself makes no FPI decision, so no self-deadlock.
	// Shutdown (and legacy) checkpoints keep the sampled-frontier redo: no
	// WAL may be written between a shutdown checkpoint's redo and its record,
	// so the record itself marks the redo point (upstream comment, xlog.c).
	var redoAppendErr error
	sampleRedo := computeRedo
	if c.cfg.PGCompatCheckpoints && !shutdown {
		sampleRedo = func() uint64 {
			rec, err := EncodeCheckpointRedoPG()
			if err != nil {
				redoAppendErr = err
				return computeRedo()
			}
			startLSN, _, err := c.wal.Append(rec)
			if err != nil {
				redoAppendErr = err
				return computeRedo()
			}
			return startLSN - 1
		}
	}
	var redoLSN0 uint64
	if rp, ok := c.flusher.(redoPublisher); ok {
		// The barrier waits out every in-flight FPI-decision->append critical
		// section BEFORE sampling the frontier, so no record decided against
		// the old redo can land at an LSN >= the new redo (PG's
		// fpw_lsn-under-insert-locks analog; adversarial review F1).
		redoLSN0 = rp.PublishRedoBarrier(sampleRedo)
	} else {
		redoLSN0 = sampleRedo()
	}
	if redoAppendErr != nil {
		return fmt.Errorf("append checkpoint-redo record: %w", redoAppendErr)
	}

	flushStart := time.Now()
	if err := c.flushDirty(pacer); err != nil {
		return fmt.Errorf("flush dirty pages: %w", err)
	}
	// M0117-0007 Part B continuation: drain the CLOG buffer pool's dirty
	// pages in the same phase as the primary flush above, before the redo
	// LSN below is sampled — see FlushCLOGFn's doc comment for why an error
	// here fails the checkpoint rather than being logged and swallowed.
	if c.cfg.FlushCLOGFn != nil {
		if err := c.cfg.FlushCLOGFn(); err != nil {
			return fmt.Errorf("flush clog dirty pages: %w", err)
		}
	}
	// M0089-0001: after pwrite'ing every dirty page, fdatasync the
	// data files so the bytes are durable before we advance the
	// checkpoint LSN. Without this, a host crash between
	// `FlushAllPaced` and the next OS-level flush could rewind the
	// data files even though WAL replay would believe those records
	// are already applied.
	if ds, ok := c.flusher.(dataFileSyncer); ok {
		if err := ds.SyncAllDataFiles(); err != nil {
			return fmt.Errorf("sync data files: %w", err)
		}
	}
	c.writeTimeMs.Add(time.Since(flushStart).Milliseconds())
	// M0102-0007: redoLSN0 (computed and published above, BEFORE the flush)
	// is what the PG-compatible checkpoint record carries — PG's xlogreader
	// validates it.
	// M0122-0009 follow-up: feed this cycle's redo distance into the
	// moving-average estimate SlotAwareRetainer consults for
	// XLOGfileslop-style WAL-segment recycling. Must run before the
	// retainer call below (via c.retainer.Retain) so the estimate it reads
	// already reflects this cycle.
	c.updateCheckPointDistanceEstimate(redoLSN0)
	// M0106-0010 batched-47: sample nextXid from the mvcc manager
	// BEFORE appending the checkpoint record so the on-wire CheckPoint
	// payload carries the live value. The standby reads nextXid out of
	// this record to initialise ShmemVariableCache->nextXid and
	// latestCompletedXid; if the value lags goopg's real allocator, the
	// standby's recovery snapshots treat post-initdb tuples as "future"
	// and 42P01s every user table on the first SELECT.
	var nextXid uint64
	if c.cfg.NextXIDFn != nil {
		nextXid = c.cfg.NextXIDFn()
	}
	// M0106-0013: sample nextOid from the catalog BEFORE appending the
	// checkpoint record so the on-wire CheckPoint payload carries the
	// live OID counter. PG's InitWalRecovery seeds
	// ShmemVariableCache->nextOid from this field on recovery, eliminating
	// the need for pg_catalog.json to survive a crash.
	var nextOid uint32
	if c.cfg.NextOIDFn != nil {
		nextOid = c.cfg.NextOIDFn()
	}
	// M0131-S18.3/.4: sample the live timeline, full_page_writes and
	// multixact counters ONCE, before the record is encoded, so the
	// checkpoint record and the pg_control checkPointCopy written below
	// cannot disagree — PG cross-checks the two at startup.
	cpFields := CheckPointFields{
		ThisTLI:        c.timelineID(),
		FullPageWrites: c.fullPageWrites(),
		WalLevel:       uint32(max(c.cfg.GUCParams.WalLevel, 0)),
		NextXid:        nextXid,
		NextOid:        nextOid,
	}
	cpFields.NextMulti, cpFields.NextMultiOffset, cpFields.OldestMulti = c.nextMultiXact()
	// Resolve PrevTLI/OldestXidDB/OldestMultiDB and every floor now, so the
	// pg_control writer below reads the same values the record encodes
	// rather than re-deriving them.
	cpFields = cpFields.withDefaults()
	var checkpointPayload []byte
	if c.cfg.PGCompatCheckpoints {
		// A9-checkpoint-opcode: explicit ONLINE/SHUTDOWN opcode via the
		// assembled-record path (the classify-by-len==88 hack is retired).
		// An online checkpoint is preceded by XLOG_RUNNING_XACTS — upstream
		// CreateCheckPoint calls LogStandbySnapshot() after CheckPointGuts
		// and before inserting the checkpoint record (xlog.c:7243) — so a
		// hot-standby PG recovering from this checkpoint's redo reaches
		// STANDBY_SNAPSHOT_READY. Shutdown checkpoints skip it (recovery
		// synthesises the running-xacts state on the wasShutdown path) and
		// stamp oldestActiveXid = InvalidTransactionId, exactly like PG.
		oldestActive := uint32(0)
		if !shutdown {
			xids, oldestRunning, latestCompleted := c.runningXacts(nextXid)
			oldestActive = oldestRunning
			rx, rerr := EncodeRunningXactsPG(uint32(max(nextXid, 3)), oldestRunning, latestCompleted, xids)
			if rerr != nil {
				return fmt.Errorf("encode running-xacts record: %w", rerr)
			}
			if _, _, err := c.wal.Append(rx); err != nil {
				return fmt.Errorf("append running-xacts record: %w", err)
			}
		}
		var cerr error
		cpFields.RedoLSN0 = redoLSN0
		cpFields.OldestActiveXid = oldestActive
		checkpointPayload, cerr = EncodeCheckpointPGFields(shutdown, cpFields)
		if cerr != nil {
			return fmt.Errorf("encode checkpoint record: %w", cerr)
		}
	} else {
		checkpointPayload = EncodeCheckpoint()
	}
	startLSN, endLSN, err := c.wal.Append(checkpointPayload)
	if err != nil {
		return fmt.Errorf("append checkpoint marker: %w", err)
	}
	if err := c.wal.FlushUpTo(endLSN); err != nil {
		return fmt.Errorf("flush checkpoint marker up to lsn %d: %w", endLSN, err)
	}
	// M0102-0007 / C2-S3 (review MUST-FIX): store the checkpoint's REDO
	// pointer — the position published at checkpoint START — not the
	// checkpoint record's own start. Records in the (redo, record] window
	// cover state the dirty-page/CLOG flush phase may not have captured
	// (a commit acked mid-flush leaves only its WAL record), so BASE_BACKUP
	// streams and recovery must begin at redo, exactly like PG's
	// checkPoint.redo. Stored 1-based (internal convention; redoLSN0 is
	// 0-based).
	c.lastCheckpointRedoLSN.Store(redoLSN0 + 1)
	c.lastCheckpointLSN.Store(endLSN)
	c.lastCheckpointStartLSN.Store(startLSN)
	// Update pg_control on disk so pg_controldata and standbys see the
	// current checkpoint location. Mirrors CreateCheckPoint (post-flush)
	// in upstream's UpdateControlFile call (xlog.c:7306).
	if c.cfg.DataDir != "" {
		checkLSN0 := startLSN - 1 // convert 1-based internal to 0-based PG LSN
		now := time.Now().Unix()
		guc := c.cfg.GUCParams
		if err := control.UpdateControlFile(c.cfg.DataDir, func(cd *control.ControlFileData) {
			// A shutdown checkpoint leaves the cluster cleanly shut down
			// (DB_SHUTDOWNED); every other checkpoint runs while the
			// cluster is live (DB_IN_PRODUCTION). Mirrors upstream's
			// CreateCheckPoint state assignment in xlog.c — only the
			// CHECKPOINT_IS_SHUTDOWN path sets DB_SHUTDOWNED. External
			// tools (pg_resetwal/pg_rewind/pg_controldata) gate on this
			// byte to decide whether recovery is required.
			if shutdown {
				cd.State = control.DBStateShutdowned
			} else {
				cd.State = control.DBStateInProduction
			}
			cd.Time = now
			cd.CheckPoint = checkLSN0
			cd.CheckPointCopyRedo = redoLSN0
			cd.CheckPointCopyTime = now
			// M0131-S18.3/.4: the same sample the checkpoint record above
			// carries. These three were hardcoded 1/1/true, which stomped
			// pg_control back to TLI 1 after a promotion.
			cd.CheckPointCopyThisTLI = cpFields.ThisTLI
			cd.CheckPointCopyPrevTLI = cpFields.PrevTLI
			cd.CheckPointCopyFullPageWrites = cpFields.FullPageWrites
			cd.CheckPointCopyNextMulti = cpFields.NextMulti
			cd.CheckPointCopyNextMultiOffset = cpFields.NextMultiOffset
			cd.CheckPointCopyOldestMulti = cpFields.OldestMulti
			cd.CheckPointCopyOldestMultiDB = cpFields.OldestMultiDB
			// M0106-0010 batched-45: refresh checkPointCopy.nextXid so a
			// PG standby attached after this checkpoint sees the right
			// snapshot xmax instead of the bootstrap FirstNormalXID=3.
			// Only update when the hook is wired and the value advances —
			// nextXid in pg_control must be monotonic.
			if nextXid > cd.CheckPointCopyNextXid {
				cd.CheckPointCopyNextXid = nextXid
			}
			// M0106-0013: refresh checkPointCopy.nextOid so a crashed cluster
			// can recover the OID counter from pg_control rather than from
			// pg_catalog.json. Only update when the hook is wired and the
			// value advances — nextOid must be monotonic.
			if nextOid > cd.CheckPointCopyNextOid {
				cd.CheckPointCopyNextOid = nextOid
			}
			cd.MinRecoveryPoint = 0
			cd.MinRecoveryPointTLI = 0
			// Refresh GUC echo fields at every checkpoint so a standby that
			// attaches after the checkpoint record always sees consistent values.
			// Mirrors CreateCheckPoint's ControlFile copy in xlog.c. M0106-0010.
			cd.WalLevel = uint32(guc.WalLevel)
			cd.WalLogHints = guc.WalLogHints
			cd.MaxConnections = uint32(guc.MaxConnections)
			cd.MaxWorkerProcesses = uint32(guc.MaxWorkerProcesses)
			cd.MaxWalSenders = uint32(guc.MaxWalSenders)
			cd.MaxPreparedXacts = uint32(guc.MaxPreparedXacts)
			cd.MaxLocksPerXact = uint32(guc.MaxLocksPerXact)
			cd.TrackCommitTimestamp = guc.TrackCommitTimestamp
		}); err != nil {
			c.cfg.Logger.Warn("pg_control update failed after checkpoint", "err", err)
		}
	}
	if spread {
		c.numTimed.Add(1)
	} else {
		c.numRequested.Add(1)
	}
	// NOTE (perf-optimize3-dash/03): the FPI epoch boundary is the redo
	// publication at checkpoint START above — there is no post-record epoch
	// reset any more (the old ResetCheckpointEpoch sweep here raced writers
	// between the redo sample and the sweep, leaving image-less records).
	// Slot-aware WAL retention: prune segments that are no longer
	// needed by either crash recovery (everything below this
	// checkpoint LSN is now redo-redundant) or any live
	// replication slot. A retainer error is logged but does not
	// fail the checkpoint — the marker is already durable, and
	// retention is best-effort.
	if c.retainer != nil {
		// C2-S3 (review MUST-FIX): retain from the REDO pointer, not the
		// checkpoint record — recycling the (redo, record] window would
		// destroy commit records whose CLOG lanes exist only in memory,
		// making the redo-anchored replay (wal.replayStart) unable to
		// reconstruct an acked commit after a crash.
		if err := c.retainer.Retain(redoLSN0 + 1); err != nil {
			c.cfg.Logger.Warn("wal retention failed", "err", err)
		}
	}
	// M0106-0011 follow-up (b): call the post-checkpoint hook (when wired)
	// to regenerate pg_internal.init after crash-recovery WAL replay may
	// have unlinked it via RecordKindXactCommitInval. Non-fatal.
	if c.cfg.PostCheckpointFn != nil {
		if err := c.cfg.PostCheckpointFn(); err != nil {
			c.cfg.Logger.Warn("post-checkpoint init file refresh failed", "err", err)
		}
	}
	// G1: durable-ordered CLOG truncation. The checkpoint marker is now on
	// disk, so removing clog segments below the frozen horizon is crash-safe.
	if c.cfg.TruncateCLOGFn != nil {
		if err := c.cfg.TruncateCLOGFn(); err != nil {
			c.cfg.Logger.Warn("post-checkpoint clog truncation failed", "err", err)
		}
	}
	// M0122-0009: durable-ordered pg_subtrans truncation, same ordering
	// rationale as TruncateCLOGFn above.
	if c.cfg.TruncateSubtransFn != nil {
		if err := c.cfg.TruncateSubtransFn(); err != nil {
			c.cfg.Logger.Warn("post-checkpoint subtrans truncation failed", "err", err)
		}
	}
	// M0057-0001: log checkpoint complete so benchmark runs can see
	// when the checkpointer finishes and whether it was mid-benchmark.
	c.cfg.Logger.Info("checkpoint complete",
		"type", checkpointType,
		"lsn", endLSN,
		"elapsed_ms", time.Since(start).Milliseconds())
	return nil
}

// flushDirty drains the buffer pool's dirty set, using the paced
// API when both the flusher and pacer are available.
func (c *Checkpointer) flushDirty(pacer func(progress float64) error) error {
	if pf, ok := c.flusher.(pacedFlusher); ok && pacer != nil {
		return pf.FlushAllPaced(pacer)
	}
	return c.flusher.FlushAll()
}

// buildPacer returns a per-buffer delay function that aims to
// finish writeback at start + Interval*CompletionTarget. Returns
// nil when spreading is disabled or the inputs are degenerate;
// flushDirty then takes the IMMEDIATE-speed FlushAll path.
func (c *Checkpointer) buildPacer(ctx context.Context, spread bool, start time.Time) func(float64) error {
	if !spread || c.cfg.CompletionTarget <= 0 || c.cfg.Interval <= 0 {
		return nil
	}
	target := time.Duration(float64(c.cfg.Interval) * c.cfg.CompletionTarget)
	if target <= 0 {
		return nil
	}
	return func(progress float64) error {
		if progress >= 1.0 {
			return nil
		}
		deadline := start.Add(time.Duration(float64(target) * progress))
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
			return nil
		}
	}
}
