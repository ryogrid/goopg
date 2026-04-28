package wal

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// DirtyPageFlusher is the buffer-pool contract used by the checkpointer.
type DirtyPageFlusher interface {
	FlushAll() error
}

// epochResetter is implemented by *storage.Pool so the
// checkpointer can clear the per-page "FPI emitted this epoch"
// flag after a successful checkpoint. Optional — checkpointers
// constructed in tests with a flusher that doesn't satisfy this
// interface keep working.
type epochResetter interface {
	ResetCheckpointEpoch()
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
	Logger              *slog.Logger
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

	lastCheckpointLSN atomic.Uint64
}

// NewCheckpointer constructs a checkpointer worker.
func NewCheckpointer(flusher DirtyPageFlusher, wal checkpointWAL, cfg CheckpointerConfig) *Checkpointer {
	cfg.withDefaults()
	return &Checkpointer{
		flusher: flusher,
		wal:     wal,
		cfg:     cfg,
	}
}

// LastCheckpointLSN returns the most recent successful checkpoint marker LSN.
func (c *Checkpointer) LastCheckpointLSN() uint64 {
	return c.lastCheckpointLSN.Load()
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
			if err := c.checkpointOnce(); err != nil {
				c.cfg.Logger.Warn("checkpoint failed", "err", err)
			}
		case <-volumeC:
			if !c.volumeTriggerFires(vr) {
				continue
			}
			if err := c.checkpointOnce(); err != nil {
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

// CheckpointNow performs a synchronous checkpoint and returns when
// the marker is durable on disk. The SQL `CHECKPOINT` verb and the
// `goopg ctl` checkpoint subcommand both call this; the periodic
// Run loop also routes through it. Concurrent calls are serialized
// through the underlying WAL writer.
func (c *Checkpointer) CheckpointNow() error {
	return c.checkpointOnce()
}

func (c *Checkpointer) checkpointOnce() error {
	if err := c.flusher.FlushAll(); err != nil {
		return fmt.Errorf("flush dirty pages: %w", err)
	}
	_, endLSN, err := c.wal.Append(EncodeCheckpoint())
	if err != nil {
		return fmt.Errorf("append checkpoint marker: %w", err)
	}
	if err := c.wal.FlushUpTo(endLSN); err != nil {
		return fmt.Errorf("flush checkpoint marker up to lsn %d: %w", endLSN, err)
	}
	c.lastCheckpointLSN.Store(endLSN)
	// Open a new full-page-image epoch: the next mutation of each
	// page must emit a fresh FPI so crash recovery from this
	// checkpoint can replay it on a torn page.
	if er, ok := c.flusher.(epochResetter); ok {
		er.ResetCheckpointEpoch()
	}
	return nil
}
