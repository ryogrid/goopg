// Continuous (streaming) WAL replay for standby servers.
//
// The crash-recovery path (ReplayRecords / ReplayFromDirWithMgr) is
// batch-oriented: it reads all records under pg_wal, finds the last
// checkpoint, and applies the tail in a single pass. That model
// works for the "open the data dir, replay, then start serving" boot
// flow but is wrong for streaming replication, where new records
// arrive indefinitely and must apply as soon as the writer has
// flushed them.
//
// `StreamReplayer` wraps the per-record `ApplyRecord` kernel and
// drives it from a `RecordIterator`. The standby's main loop wires
// it up alongside the walreceiver: walreceiver Append's into the
// local WAL writer, the iterator wakes on the writer's flush
// subscription, and `StreamReplayer.Run` applies each record
// one-at-a-time. Apply is idempotent via `pd_lsn` so restart-resume
// never needs a separate apply cursor — we always start the
// iterator at the writer's WrittenLSN at boot time and the
// previously-applied tail is silently skipped.
//
// See docs/design/0005-0002-standby-recovery-and-replay.md.
package wal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// StreamReplayer is the standby-side continuous-replay driver.
// Construct via `NewStreamReplayer`, then call `Run(ctx, iter)` from
// a goroutine. `ApplyLSN()` is safe to call concurrently and returns
// the LSN of the last record successfully applied (or the
// constructor-supplied baseline when no records have been applied
// yet).
type StreamReplayer struct {
	mgr *storage.Manager

	mu       sync.Mutex
	applyLSN uint64
	records  uint64
	applied  uint64
}

// NewStreamReplayer constructs a replayer rooted at `mgr`.
// `baselineLSN` seeds the initial `ApplyLSN()` reading; pass the
// local WAL writer's `WrittenLSN()` at boot so observers see a
// monotone progression even before the first record arrives.
func NewStreamReplayer(mgr *storage.Manager, baselineLSN uint64) *StreamReplayer {
	if mgr == nil {
		panic("wal: NewStreamReplayer: nil storage manager")
	}
	return &StreamReplayer{mgr: mgr, applyLSN: baselineLSN}
}

// ApplyLSN returns the LSN of the most recently applied record (or
// the baseline supplied to NewStreamReplayer when no records have
// been applied yet).
func (sr *StreamReplayer) ApplyLSN() uint64 {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.applyLSN
}

// Stats returns a snapshot of the replayer's progress counters: the
// total number of records observed (including markers) and the
// number that triggered a real page mutation.
func (sr *StreamReplayer) Stats() (records, applied uint64) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.records, sr.applied
}

// Run pulls records from `iter` and applies each one until the
// context is cancelled or the iterator returns io.EOF (the writer
// closed). Returns:
//   - nil when ctx is cancelled cleanly
//   - nil when the iterator hits io.EOF (writer closed or iterator
//     closed) — graceful end-of-stream is not an error
//   - the wrapped apply error on any per-record failure (the caller
//     decides whether to crash or retry; in v0 cmd/goopg start logs
//     and exits the standby because a divergence between primary
//     WAL and standby data files is unrecoverable without a fresh
//     base backup)
func (sr *StreamReplayer) Run(ctx context.Context, iter *RecordIterator) error {
	if iter == nil {
		return errors.New("wal: StreamReplayer.Run: nil iterator")
	}
	for {
		rec, err := iter.Next(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			if errors.Is(err, io.EOF) || errors.Is(err, ErrClosed) {
				return nil
			}
			return fmt.Errorf("wal: stream replay read: %w", err)
		}
		applied, err := ApplyRecord(sr.mgr, rec)
		if err != nil {
			return fmt.Errorf("wal: stream replay record lsn[%d,%d]: %w", rec.StartLSN, rec.EndLSN, err)
		}
		sr.mu.Lock()
		sr.records++
		if applied {
			sr.applied++
		}
		if rec.EndLSN > sr.applyLSN {
			sr.applyLSN = rec.EndLSN
		}
		sr.mu.Unlock()
	}
}
