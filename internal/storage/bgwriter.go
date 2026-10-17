package storage

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// Bgwriter is the background writer goroutine (M0048-0003). It proactively
// flushes dirty buffer-pool pages to disk on a timer so that foreground
// victim-searches (evictLocked) encounter clean pages and can reuse them
// without incurring synchronous I/O.
//
// The bgwriter does NOT issue fsync — the checkpointer owns durability.
// Its sole job is to reduce the foreground "dirty-victim rate" (the
// fraction of evictions where the pool must flush synchronously).
//
// Design notes:
//   - One tick every BgwriterDelay (default 200 ms).
//   - At most BgwriterMaxPages dirty pages are written per tick.
//   - Uses Pool.WriteDirtyPages, which holds its own independent scan
//     cursor (bgwriterHand) separate from the eviction clockHand.
//   - Graceful shutdown via Stop().
//   - bgwriter_delay and bgwriter_lru_maxpages are PGC_SIGHUP in PG
//     (guc_tables.c): a reload reaches the running goroutine through
//     SetDelay / SetMaxPages, as BackgroundWriterMain re-reads them after
//     ProcessMainLoopInterrupts. bgwriter_lru_maxpages = 0 stops the LRU
//     writes but, as in PG, not the process.
type Bgwriter struct {
	pool     *Pool
	delay    atomic.Int64 // time.Duration
	maxPages atomic.Int64
	// kick wakes the loop after SetDelay so a new delay takes effect
	// at once, as the SIGHUP latch wakes PG's bgwriter from its sleep.
	kick   chan struct{}
	stop   chan struct{}
	done   chan struct{}
	logger *slog.Logger

	// OnLoopStart/OnLoopEnd, when set, bracket the goroutine's entire run
	// (called once each, not per-tick) so a caller can register this
	// goroutine's identity with the activity registry the same way
	// wal.Config's OnLoopStart/OnLoopEnd let initdb.Open track the WAL
	// writer goroutine (walProcNum). Without this the bgwriter's dirty-page
	// flushes (WriteDirtyPages → accountBgwriterWrite) have no registered
	// goroutine to attribute a wait event to (M0122-0003 writeback
	// simplification 3 follow-up).
	OnLoopStart func()
	OnLoopEnd   func()
}

// NewBgwriter creates a Bgwriter bound to pool. delay is the inter-tick
// interval (0 disables the goroutine). maxPages caps writes per tick.
func NewBgwriter(pool *Pool, delay time.Duration, maxPages int) *Bgwriter {
	b := &Bgwriter{
		pool:   pool,
		kick:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		logger: slog.Default(),
	}
	b.delay.Store(int64(delay))
	b.maxPages.Store(int64(maxPages))
	return b
}

// SetDelay live-updates bgwriter_delay (a reload). A non-positive delay is
// ignored: the GUC's range starts at 10ms.
func (b *Bgwriter) SetDelay(d time.Duration) {
	if d <= 0 || time.Duration(b.delay.Swap(int64(d))) == d {
		return
	}
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// SetMaxPages live-updates bgwriter_lru_maxpages (a reload). 0 disables
// the per-tick writes.
func (b *Bgwriter) SetMaxPages(n int) { b.maxPages.Store(int64(n)) }

// Delay and MaxPages report the settings the loop currently runs with.
func (b *Bgwriter) Delay() time.Duration { return time.Duration(b.delay.Load()) }
func (b *Bgwriter) MaxPages() int        { return int(b.maxPages.Load()) }

// SetLogger wires a custom logger. Must be called before Start().
func (b *Bgwriter) SetLogger(l *slog.Logger) {
	if l != nil {
		b.logger = l
	}
}

// Start launches the bgwriter goroutine. It is a no-op when delay ≤ 0.
func (b *Bgwriter) Start() {
	go b.run()
}

// Stop signals the goroutine to exit and waits for it to finish.
func (b *Bgwriter) Stop() {
	close(b.stop)
	<-b.done
}

func (b *Bgwriter) run() {
	defer close(b.done)
	delay := b.Delay()
	if delay <= 0 {
		return
	}
	if b.OnLoopStart != nil {
		b.OnLoopStart()
	}
	if b.OnLoopEnd != nil {
		defer b.OnLoopEnd()
	}
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if maxPages := b.MaxPages(); maxPages > 0 {
				if n := b.pool.WriteDirtyPages(maxPages); n > 0 {
					b.logger.Info("bgwriter flush", "pages", n)
				}
			}
		case <-b.kick:
			ticker.Reset(b.Delay())
		case <-b.stop:
			return
		}
	}
}
