package wal

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/control"
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

// epochResetter is implemented by *storage.Pool so the
// checkpointer can clear the per-page "FPI emitted this epoch"
// flag after a successful checkpoint. Optional — checkpointers
// constructed in tests with a flusher that doesn't satisfy this
// interface keep working.
type epochResetter interface {
	ResetCheckpointEpoch()
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
type CheckpointerConfig struct {
	// Interval is the period between automatic checkpoints
	// (mirrors upstream's checkpoint_timeout).
	Interval time.Duration
	// MaxWALBytes, when > 0, fires a checkpoint as soon as the
	// WAL bytes written since the last checkpoint exceed this
	// threshold (mirrors upstream's max_wal_size). Requires the
	// underlying writer to satisfy volumeReporter; otherwise the
	// trigger is a no-op.
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

	lastCheckpointLSN atomic.Uint64
	lastCheckpointRedoLSN atomic.Uint64

	// Aggregate counters surfaced through pg_stat_checkpointer.
	// Mirror the upstream PG 18 view's counter shape:
	// num_timed     — timer-driven cycles
	// num_requested — SQL CHECKPOINT, CLI ctl, max_wal_size volume
	// write_time_ms — cumulative wall time inside flushDirty
	// statsResetAt  — timestamp of the last counter reset
	//                 (currently only set at construction)
	numTimed     atomic.Uint64
	numRequested atomic.Uint64
	writeTimeMs  atomic.Uint64
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
		NumTimed:          c.numTimed.Load(),
		NumRequested:      c.numRequested.Load(),
		WriteTimeMs:       c.writeTimeMs.Load(),
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

// LastCheckpointRedoLSN returns the REDO LSN (start byte of the checkpoint
// record) for the most recent successful checkpoint. This is the position
// from which crash recovery must replay. Exported for BASE_BACKUP's
// pg_control update path (M0102-0007).
func (c *Checkpointer) LastCheckpointRedoLSN() uint64 {
	return c.lastCheckpointRedoLSN.Load()
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
		vt := time.NewTicker(c.cfg.VolumeCheckInterval)
		defer vt.Stop()
		volumeC = vt.C
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.runCheckpoint(ctx, true); err != nil {
				c.cfg.Logger.Warn("checkpoint failed", "err", err)
			}
		case <-volumeC:
			if !c.volumeTriggerFires(vr) {
				continue
			}
			// Volume-triggered checkpoints run at IMMEDIATE
			// speed: max_wal_size is a backpressure signal,
			// not a cadence knob.
			if err := c.runCheckpoint(ctx, false); err != nil {
				c.cfg.Logger.Warn("volume-triggered checkpoint failed", "err", err)
			}
		}
	}
}

// volumeTriggerFires reports whether the WAL has accumulated more
// than MaxWALBytes since the last successful checkpoint. The
// "since the last checkpoint" anchor uses the current writer
// position when no checkpoint has run yet, which matches
// upstream's CheckpointSegments / CheckPointsReqd accounting:
// the very first window starts from server start.
func (c *Checkpointer) volumeTriggerFires(vr volumeReporter) bool {
	written := vr.WrittenLSN()
	last := c.lastCheckpointLSN.Load()
	if last == 0 {
		// No checkpoint yet — anchor on server start (LSN 0)
		// so MaxWALBytes still gates the very first window.
		return written >= c.cfg.MaxWALBytes
	}
	return written > last && (written-last) >= c.cfg.MaxWALBytes
}

// CheckpointNow performs a synchronous, IMMEDIATE-speed
// checkpoint and returns when the marker is durable on disk.
// The SQL `CHECKPOINT` verb and the `goopg ctl` checkpoint
// subcommand both call this. Spread/throttling is bypassed;
// the caller asked for fast.
func (c *Checkpointer) CheckpointNow() error {
	return c.runCheckpoint(context.Background(), false)
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
func (c *Checkpointer) runCheckpoint(ctx context.Context, spread bool) error {
	start := time.Now()
	checkpointType := "requested"
	if spread {
		checkpointType = "scheduled"
	}
	// M0057-0001: log checkpoint start so benchmark runs can confirm
	// whether the checkpointer fires mid-measurement.
	c.cfg.Logger.Info("checkpoint start", "type", checkpointType)
	pacer := c.buildPacer(ctx, spread, start)

	flushStart := time.Now()
	if err := c.flushDirty(pacer); err != nil {
		return fmt.Errorf("flush dirty pages: %w", err)
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
	c.writeTimeMs.Add(uint64(time.Since(flushStart).Milliseconds()))
	// M0102-0007: pre-compute the 0-based redo LSN so the
	// PG-compatible checkpoint record carries the correct
	// checkPoint.redo — PG's xlogreader validates it.
	pos := int64(uint64(0))
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
	redoLSN0 := uint64(pos + int64(leading))
	startLSN, endLSN, err := c.wal.Append(EncodeCheckpointCompat(redoLSN0, 1))
	if err != nil {
		return fmt.Errorf("append checkpoint marker: %w", err)
	}
	if err := c.wal.FlushUpTo(endLSN); err != nil {
		return fmt.Errorf("flush checkpoint marker up to lsn %d: %w", endLSN, err)
	}
	// M0102-0007: store REDO LSN (start of checkpoint record) for
	// BASE_BACKUP so pg_control carries a valid redo point.
	c.lastCheckpointRedoLSN.Store(startLSN)
	c.lastCheckpointLSN.Store(endLSN)
	// Update pg_control on disk so pg_controldata and standbys see the
	// current checkpoint location. Mirrors CreateCheckPoint (post-flush)
	// in upstream's UpdateControlFile call (xlog.c:7306).
	if c.cfg.DataDir != "" {
		checkLSN0 := startLSN - 1 // convert 1-based internal to 0-based PG LSN
		now := time.Now().Unix()
		guc := c.cfg.GUCParams
		var nextXid uint64
		if c.cfg.NextXIDFn != nil {
			nextXid = c.cfg.NextXIDFn()
		}
		if err := control.UpdateControlFile(c.cfg.DataDir, func(cd *control.ControlFileData) {
			cd.State = control.DBStateInProduction
			cd.Time = now
			cd.CheckPoint = checkLSN0
			cd.CheckPointCopyRedo = redoLSN0
			cd.CheckPointCopyTime = now
			cd.CheckPointCopyThisTLI = 1
			cd.CheckPointCopyPrevTLI = 1
			cd.CheckPointCopyFullPageWrites = true
			// M0106-0010 batched-45: refresh checkPointCopy.nextXid so a
			// PG standby attached after this checkpoint sees the right
			// snapshot xmax instead of the bootstrap FirstNormalXID=3.
			// Only update when the hook is wired and the value advances —
			// nextXid in pg_control must be monotonic.
			if nextXid > cd.CheckPointCopyNextXid {
				cd.CheckPointCopyNextXid = nextXid
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
	// Open a new full-page-image epoch: the next mutation of each
	// page must emit a fresh FPI so crash recovery from this
	// checkpoint can replay it on a torn page.
	if er, ok := c.flusher.(epochResetter); ok {
		er.ResetCheckpointEpoch()
	}
	// Slot-aware WAL retention: prune segments that are no longer
	// needed by either crash recovery (everything below this
	// checkpoint LSN is now redo-redundant) or any live
	// replication slot. A retainer error is logged but does not
	// fail the checkpoint — the marker is already durable, and
	// retention is best-effort.
	if c.retainer != nil {
		if err := c.retainer.Retain(endLSN); err != nil {
			c.cfg.Logger.Warn("wal retention failed", "err", err)
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
