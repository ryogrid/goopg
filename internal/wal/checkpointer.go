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

type checkpointWAL interface {
	Append(payload []byte) (uint64, uint64, error)
	FlushUpTo(lsn uint64) error
}

// CheckpointerConfig controls checkpoint cadence and logging.
type CheckpointerConfig struct {
	Interval time.Duration
	Logger   *slog.Logger
}

func (c *CheckpointerConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 10 * time.Second
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

// Run starts the periodic checkpoint loop and returns when ctx is canceled.
func (c *Checkpointer) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.checkpointOnce(); err != nil {
				c.cfg.Logger.Warn("checkpoint failed", "err", err)
			}
		}
	}
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
	return nil
}
