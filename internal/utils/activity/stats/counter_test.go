package stats

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCounter_SingleGoroutineAddSum(t *testing.T) {
	var c Counter
	if got := c.Sum(); got != 0 {
		t.Fatalf("zero Counter.Sum() = %d, want 0", got)
	}
	for i := 0; i < 1000; i++ {
		c.Add(1)
	}
	if got := c.Sum(); got != 1000 {
		t.Fatalf("Sum after 1000×Add(1) = %d, want 1000", got)
	}
	c.Add(-500)
	if got := c.Sum(); got != 500 {
		t.Fatalf("Sum after Add(-500) = %d, want 500", got)
	}
}

func TestCounter_Reset(t *testing.T) {
	var c Counter
	for i := 0; i < 256; i++ {
		c.Add(7)
	}
	if got := c.Sum(); got != 256*7 {
		t.Fatalf("Sum before Reset = %d, want %d", got, 256*7)
	}
	c.Reset()
	if got := c.Sum(); got != 0 {
		t.Fatalf("Sum after Reset = %d, want 0", got)
	}
	c.Add(1)
	if got := c.Sum(); got != 1 {
		t.Fatalf("Sum after Reset then Add(1) = %d, want 1", got)
	}
}

// TestCounter_ConcurrentAddTotalsExact runs many goroutines doing Add(1)
// and asserts the aggregate Sum equals goroutines × iterations exactly.
// Per-shard atomic.Int64 makes individual shard reads well-defined; the
// pinned window plus the cross-shard Sum at the end (after all goroutines
// have finished) guarantees the total is exact.
func TestCounter_ConcurrentAddTotalsExact(t *testing.T) {
	const (
		goroutines = 32
		iterations = 16 << 10
	)
	var c Counter
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				c.Add(1)
			}
		}()
	}
	wg.Wait()
	want := int64(goroutines * iterations)
	if got := c.Sum(); got != want {
		t.Fatalf("concurrent Sum = %d, want %d", got, want)
	}
}

// TestCounter_PerShardWriteDistribution sanity-checks that with
// GOMAXPROCS > 1 and many goroutines, more than one shard accumulates.
// On a single-P fallback this skips. The intent is to detect a
// regression where Add accidentally collapses to a single shard
// (which would defeat the per-P sharding's purpose).
func TestCounter_PerShardWriteDistribution(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("requires GOMAXPROCS >= 2")
	}
	const (
		goroutines = 64
		iterations = 4 << 10
	)
	var c Counter
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				c.Add(1)
			}
		}()
	}
	wg.Wait()
	nonEmpty := 0
	for i := range c.shards {
		if c.shards[i].n.Load() != 0 {
			nonEmpty++
		}
	}
	if nonEmpty < 2 {
		t.Fatalf("only %d shards received Adds; per-P sharding looks broken", nonEmpty)
	}
}

// TestCounter_SumConcurrentWithAdd verifies a Sum read during ongoing
// Adds returns a value in [0, totalIssued] — i.e., no torn read produces
// a value outside the valid range. Strict consistency is not promised
// (an in-flight Add may or may not be observed); the property under test
// is that no atomic load returns garbage.
func TestCounter_SumConcurrentWithAdd(t *testing.T) {
	const (
		producers  = 8
		iterations = 8 << 10
	)
	var c Counter
	var producersWG, readerWG sync.WaitGroup
	var stop atomic.Bool

	for p := 0; p < producers; p++ {
		producersWG.Add(1)
		go func() {
			defer producersWG.Done()
			for i := 0; i < iterations; i++ {
				c.Add(1)
			}
		}()
	}

	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		max := int64(producers * iterations)
		for !stop.Load() {
			s := c.Sum()
			if s < 0 || s > max {
				t.Errorf("Sum() = %d outside [0, %d]", s, max)
				return
			}
			runtime.Gosched()
		}
	}()

	producersWG.Wait()
	stop.Store(true)
	readerWG.Wait()

	if got := c.Sum(); got != int64(producers*iterations) {
		t.Fatalf("final Sum = %d, want %d", got, producers*iterations)
	}
}

func BenchmarkCounterAdd(b *testing.B) {
	var c Counter
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Add(1)
		}
	})
}
