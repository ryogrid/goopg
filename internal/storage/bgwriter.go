package storage

import (
	"log/slog"
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
type Bgwriter struct {
	pool     *Pool
	delay    time.Duration
	maxPages int
	stop     chan struct{}
	done     chan struct{}
	logger   *slog.Logger
}

// NewBgwriter creates a Bgwriter bound to pool. delay is the inter-tick
// interval (0 disables the goroutine). maxPages caps writes per tick.
func NewBgwriter(pool *Pool, delay time.Duration, maxPages int) *Bgwriter {
	return &Bgwriter{
		pool:     pool,
		delay:    delay,
		maxPages: maxPages,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		logger:   slog.Default(),
	}
}

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
	if b.delay <= 0 {
		return
	}
	ticker := time.NewTicker(b.delay)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if n := b.pool.WriteDirtyPages(b.maxPages); n > 0 {
				b.logger.Info("bgwriter flush", "pages", n)
			}
		case <-b.stop:
			return
		}
	}
}
