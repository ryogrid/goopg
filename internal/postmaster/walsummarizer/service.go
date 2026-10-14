package walsummarizer

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/backup/walsummary"
)

// ServiceConfig wires the summarizer into a running server. The package
// stays free of executor/initdb/postmaster imports; the server passes these
// hooks in (design §4.1).
type ServiceConfig struct {
	DataDir string
	WALDir  string
	SegSize int64
	WAL     *xlog.Writer
	// RedoLSN returns the PG LSN of the latest checkpoint's redo point, or 0
	// when none is known yet. It is the start position when no summary
	// exists.
	RedoLSN func() uint64
	Logger  *slog.Logger
	// PollInterval bounds how long the reader waits before re-checking the
	// flush pointer: the WAL writer wakes readers on append, not on flush.
	// 0 means 200ms, PG's sleep quantum.
	PollInterval time.Duration
	// RetryDelay is the pause after an error before summarization resumes.
	// 0 means 10s, as WalSummarizerMain waits.
	RetryDelay time.Duration
	// KeepTime returns wal_summary_keep_time; nil or <= 0 disables summary
	// file removal.
	KeepTime func() time.Duration
	// OnStart and OnStop bracket each run of the summarizer goroutine, so
	// the server can hold a pg_stat_activity slot for it (backend_type
	// walsummarizer) while it runs.
	OnStart, OnStop func()
}

// Service runs the WAL summarizer when summarize_wal is on: the
// WalSummarizerMain process, which the postmaster starts while the
// setting is on and which exits when it is turned off.
type Service struct {
	cfg ServiceConfig

	// lastRemovalRedo is redo_pointer_at_last_summary_removal: removal runs
	// once per checkpoint cycle. Touched only by the summarizer goroutine.
	lastRemovalRedo uint64

	io IOCounters

	mu     sync.Mutex
	parent context.Context
	cancel context.CancelFunc
	done   chan struct{}
	sum    *Summarizer
}

// NewService returns a stopped service.
func NewService(cfg ServiceConfig) *Service {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 200 * time.Millisecond
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{cfg: cfg}
}

// Reconcile starts the summarizer when enabled and stops it when not. parent
// bounds its lifetime; a nil parent reuses the one from the first call (the
// reload path has no server context of its own).
func (s *Service) Reconcile(parent context.Context, enabled bool) {
	s.mu.Lock()
	if parent != nil {
		s.parent = parent
	}
	running := s.cancel != nil
	if enabled == running || s.parent == nil {
		s.mu.Unlock()
		return
	}
	if !enabled {
		s.mu.Unlock()
		s.Stop()
		return
	}
	ctx, cancel := context.WithCancel(s.parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	tli := uint32(1)
	if s.cfg.WAL != nil {
		tli = s.cfg.WAL.TimelineID()
	}
	s.sum = NewSummarizer(s.cfg.DataDir, tli)
	s.sum.AfterSummary = s.maybeRemoveOld
	s.sum.IO = &s.io
	sum, done := s.sum, s.done
	s.mu.Unlock()
	s.cfg.Logger.Info("WAL summarizer starting", "event", "walsummarizer_start", "tli", tli)
	onStart, onStop := s.cfg.OnStart, s.cfg.OnStop
	go func() {
		defer close(done)
		if onStart != nil {
			onStart()
		}
		if onStop != nil {
			defer onStop()
		}
		s.loop(ctx, sum)
	}()
}

// Stop stops the summarizer and waits for it to exit.
func (s *Service) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
		s.cfg.Logger.Info("WAL summarizer stopped", "event", "walsummarizer_stop")
	}
}

// State is the running summarizer's published state, zero when it has never
// run (pg_get_wal_summarizer_state reports 0, 0/0, 0/0, NULL then).
func (s *Service) State() State {
	s.mu.Lock()
	sum := s.sum
	s.mu.Unlock()
	if sum == nil {
		return State{}
	}
	return sum.State()
}

// WALReads reports the WAL pages the summarizer has read and their bytes,
// cumulative since the server started.
func (s *Service) WALReads() (reads, bytes int64) {
	return s.io.Reads.Load(), s.io.ReadBytes.Load()
}

// KeepLSN is the oldest LSN WAL retention must keep for the summarizer:
// what it has not summarized yet (KeepLogSeg's GetOldestUnsummarizedLSN
// clamp). 0 means no constraint (summarize_wal off).
func (s *Service) KeepLSN() uint64 {
	s.mu.Lock()
	sum, running := s.sum, s.cancel != nil
	s.mu.Unlock()
	if !running || sum == nil {
		return 0
	}
	st := sum.State()
	if !st.Initialized {
		return 0
	}
	return st.SummarizedLSN
}

// maybeRemoveOld is MaybeRemoveOldWalSummaries: once per checkpoint cycle
// (when the redo pointer has moved), remove summaries whose WAL is gone and
// whose files are older than wal_summary_keep_time.
func (s *Service) maybeRemoveOld() {
	if s.cfg.KeepTime == nil || s.cfg.RedoLSN == nil {
		return
	}
	keep := s.cfg.KeepTime()
	if keep <= 0 {
		return
	}
	redo := s.cfg.RedoLSN()
	if redo == s.lastRemovalRedo {
		return
	}
	s.lastRemovalRedo = redo
	removed, err := walsummary.RemoveOld(s.cfg.DataDir, keep, time.Now(), func(tli uint32) (uint64, bool) {
		return oldestSegmentLSN(s.cfg.WALDir, tli, s.cfg.SegSize)
	})
	if err != nil {
		s.cfg.Logger.Error("WAL summary removal failed", "event", "walsummary_remove_error", "err", err)
	}
	for _, f := range removed {
		s.cfg.Logger.Debug("removing file", "path", walsummary.Dir+"/"+f.Name())
	}
}

// oldestSegmentLSN is XLogGetOldestSegno turned into an LSN: the start of the
// oldest WAL segment file on disk for tli. ok is false when none exists.
func oldestSegmentLSN(walDir string, tli uint32, segSize int64) (uint64, bool) {
	if segSize <= 0 {
		segSize = xlog.DefaultSegmentSize
	}
	ents, err := os.ReadDir(walDir)
	if err != nil {
		return 0, false
	}
	var oldest uint64
	found := false
	for _, e := range ents {
		t, segno, ok := xlog.ParseXLogFileName(e.Name(), segSize)
		if !ok || t != tli {
			continue
		}
		if !found || segno < oldest {
			oldest, found = segno, true
		}
	}
	return oldest * uint64(segSize), found
}

// loop is WalSummarizerMain: find the start, summarize until cancelled,
// and after an error wait RetryDelay and resume from the summaries on disk.
func (s *Service) loop(ctx context.Context, sum *Summarizer) {
	for {
		err := s.runOnce(ctx, sum)
		if ctx.Err() != nil {
			return
		}
		s.cfg.Logger.Error("WAL summarizer error", "event", "walsummarizer_error", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.cfg.RetryDelay):
		}
	}
}

func (s *Service) runOnce(ctx context.Context, sum *Summarizer) error {
	start, ok, err := ResumePoint(s.cfg.DataDir, sum.TLI)
	if err != nil {
		return err
	}
	for !ok {
		// No summary yet: start at the latest redo point, a known record
		// start. (PG starts at the oldest WAL on disk and searches forward
		// with XLogFindNextRecord; see the deferral ledger.)
		if s.cfg.RedoLSN != nil {
			if start = s.cfg.RedoLSN(); start != 0 {
				ok = true
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.cfg.PollInterval):
		}
	}
	if s.cfg.WAL == nil {
		return errors.New("walsummarizer: no WAL writer")
	}
	// The iterator counts LSNs from 1: PG LSN p is its p+1.
	it, err := xlog.NewRecordIterator(s.cfg.WAL, s.cfg.WALDir, s.cfg.SegSize, start+1)
	if err != nil {
		return err
	}
	defer it.Close()
	it.SetReadLimit(s.cfg.WAL.FlushedLSN)
	return sum.Run(ctx, &pollSource{it: it, poll: s.cfg.PollInterval}, start)
}

// pollSource re-polls the iterator every poll interval so a flush that is
// not followed by an append still releases the records it made durable.
type pollSource struct {
	it   *xlog.RecordIterator
	poll time.Duration
}

func (p *pollSource) Next(ctx context.Context) (xlog.Record, error) {
	for {
		c, cancel := context.WithTimeout(ctx, p.poll)
		r, err := p.it.Next(c)
		cancel()
		if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		return r, err
	}
}
