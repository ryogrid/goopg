package xlog

import (
	"sync"
	"testing"
)

// TestPublishTailConcurrentPublishersMonotone is the CAS-max regression test
// (docs/design/wal-backend-flush/ 04 §4.3, M2). Unlike the single-publisher
// scenario in wal_buffer_publish_tail_test.go, this drives MANY concurrent
// publishers — the hot multi-caller path the backend-driven WAL write path
// creates (every waiter's waitInsertionsToFinish spin, the flush holder's
// widen, the walwriter's frontier, and fast-path RLock appenders). The
// watermark must end at the max published value; a Load-then-Store publishTail
// would lose a racing higher publish (A loads 90, B stores 105, A stores 100)
// and could finish below the max.
func TestPublishTailConcurrentPublishersMonotone(t *testing.T) {
	t.Parallel()
	const G = 16
	const N = 512
	b := newWALBuffer(int64(G*N) + 64)
	b.reset(0)

	var wg sync.WaitGroup
	wg.Add(G)
	for g := 0; g < G; g++ {
		start := int64(g*N + 1)
		go func() {
			defer wg.Done()
			// disjoint ascending runs, interleaved out of order across goroutines
			for i := int64(0); i < N; i++ {
				if got := b.publishTail(start + i); got < start+i {
					t.Errorf("publishTail(%d) returned %d < requested", start+i, got)
					return
				}
			}
		}()
	}
	wg.Wait()

	want := int64(G * N) // max value published across all goroutines
	if got := b.tail.Load(); got != want {
		t.Errorf("final tail = %d, want %d (lost update under concurrent publishers)", got, want)
	}
}
