package walsummarizer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/backup/walsummary"
)

// Source yields decoded WAL records in order, blocking while none is
// available. *xlog.RecordIterator satisfies it; with SetReadLimit(
// Writer.FlushedLSN) it never yields WAL a crash could still rewrite.
type Source interface {
	Next(ctx context.Context) (xlog.Record, error)
}

// State is what the summarizer publishes (WalSummarizerData in
// walsummarizer.c, read by pg_get_wal_summarizer_state):
// SummarizedLSN is the end of the last summary written, PendingLSN the end
// of the last record read, LSNIsExact whether SummarizedLSN is a record
// boundary the next summary can start from exactly.
type State struct {
	Initialized   bool
	SummarizedTLI uint32
	SummarizedLSN uint64
	LSNIsExact    bool
	PendingLSN    uint64
	Running       bool
}

// IOCounters counts the WAL pages the summarizer reads, for pg_stat_io's
// (walsummarizer, wal, normal) reads / read_bytes cells. PG counts each
// XLOG_BLCKSZ page WALRead fetches; the summarizer counts each page its
// record stream enters. Cumulative across summarizer restarts.
type IOCounters struct {
	Reads     atomic.Int64
	ReadBytes atomic.Int64
}

const walPageSize = 8192 // XLOG_BLCKSZ

// countPages adds the pages [from, to] to c, skipping those at or before
// *last, and advances *last.
func (c *IOCounters) countPages(startLSN, endLSN uint64, last *int64) {
	if c == nil || endLSN == 0 {
		return
	}
	first, final := int64(startLSN/walPageSize), int64((endLSN-1)/walPageSize)
	if first <= *last {
		first = *last + 1
	}
	if final < first {
		return
	}
	n := final - first + 1
	c.Reads.Add(n)
	c.ReadBytes.Add(n * walPageSize)
	*last = final
}

// Summarizer is the live WAL summarizer: it consumes a Source and writes a
// summary file at every redo point, as WalSummarizerMain / SummarizeWAL do.
// It is safe to read its State from other goroutines while Run executes.
type Summarizer struct {
	DataDir string
	TLI     uint32

	mu    sync.Mutex
	state State
	// changed is closed and replaced on every published summary, so
	// waiters (WaitForWalSummarization) can block on it.
	changed chan struct{}

	// AfterSummary, when set, runs after each summarized range, where
	// WalSummarizerMain calls MaybeRemoveOldWalSummaries.
	AfterSummary func()

	// IO, when set, counts the WAL pages read.
	IO *IOCounters
}

// NewSummarizer returns a summarizer for timeline tli writing under dataDir.
func NewSummarizer(dataDir string, tli uint32) *Summarizer {
	return &Summarizer{DataDir: dataDir, TLI: tli, changed: make(chan struct{})}
}

// State returns a snapshot of the published state.
func (s *Summarizer) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Changed returns a channel closed at the next published summary.
func (s *Summarizer) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// ResumePoint is the exact-start half of GetOldestUnsummarizedLSN: the
// highest end LSN among the summaries already on disk for tli, from which
// summarization continues with no gap. ok is false when there are none; the
// caller must then start at a known record start (a redo point).
func ResumePoint(dataDir string, tli uint32) (lsn uint64, ok bool, err error) {
	files, err := walsummary.List(dataDir, tli, 0, 0)
	if err != nil {
		return 0, false, err
	}
	for _, f := range files {
		if f.End > lsn {
			lsn, ok = f.End, true
		}
	}
	return lsn, ok, nil
}

// Run summarizes src from start (an exact record start) until ctx is
// cancelled, writing one summary per range between redo points and
// publishing State after each. Records before start are skipped, so src may
// be positioned earlier. It returns nil on cancellation.
func (s *Summarizer) Run(ctx context.Context, src Source, start uint64) error {
	s.mu.Lock()
	s.state = State{Initialized: true, SummarizedTLI: s.TLI, SummarizedLSN: start, LSNIsExact: true, PendingLSN: start, Running: true}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.state.Running = false
		s.mu.Unlock()
	}()
	cur := newRangeSummarizer(s.TLI, start)
	lastPage := int64(-1)
	for {
		r, err := src.Next(ctx)
		if err != nil {
			if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ctx.Err())) {
				return nil
			}
			return fmt.Errorf("walsummarizer: reading WAL after %X/%X: %w", uint32(cur.end>>32), uint32(cur.end), err)
		}
		if readRecPtr(r) < start {
			continue
		}
		s.IO.countPages(readRecPtr(r), endRecPtr(r), &lastPage)
		done, err := cur.add(r)
		if err != nil {
			return err
		}
		if done {
			if _, _, err := cur.finish(s.DataDir); err != nil {
				return err
			}
			s.publish(cur.end)
			if s.AfterSummary != nil {
				s.AfterSummary()
			}
			cur = newRangeSummarizer(s.TLI, cur.end)
			if _, err := cur.add(r); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.state.PendingLSN = cur.end
		s.mu.Unlock()
	}
}

// publish records a completed summary ending at end and wakes waiters.
// A fast-forwarded range writes no file but still advances SummarizedLSN,
// as SummarizeWAL's return value does.
func (s *Summarizer) publish(end uint64) {
	s.mu.Lock()
	s.state.SummarizedLSN = end
	s.state.LSNIsExact = true
	if end > s.state.PendingLSN {
		s.state.PendingLSN = end
	}
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}
