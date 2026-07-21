package mctx

import (
	"fmt"
	"sync"
	"testing"
)

// TestPermConcurrentAllocAndRead exercises the fix for a PRE-EXISTING defect
// found while designing parallel query (docs/design/parallel-query/03 §3.2).
//
// Perm() is the process-global permanent context. Every big-mantissa numeric
// in the engine allocates from it (newBigNumericInCtx in internal/executor),
// and it is never Reset or Released — which is exactly why arena-backed
// numerics are safe to hand between goroutines at all.
//
// But allocation is a bump pointer that appends to c.chunks, growChunk also
// memmoves that slice's tail, and Bytes reads it. So two concurrent SESSIONS
// doing arithmetic on numerics too large for an int64 mantissa already raced
// here, long before any parallel-query work. It is rare only because such
// values are rare; parallel query would make it routine.
//
// This test must be run under `-race` to be meaningful. It deliberately
// interleaves allocation with resolution, because the read path needs the lock
// too — a reader can otherwise observe a torn or relocated chunk slice.
func TestPermConcurrentAllocAndRead(t *testing.T) {
	const (
		goroutines = 8
		perG       = 200
	)
	p := Perm()

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// Keep what we wrote so we can prove it is still readable after
			// other goroutines have grown the arena underneath us.
			type written struct {
				off, length uint32
				want        string
			}
			mine := make([]written, 0, perG)
			for i := 0; i < perG; i++ {
				// Vary the payload length so chunk growth actually happens
				// rather than every write landing in the first chunk.
				s := fmt.Sprintf("g%02d-i%04d-%s", g, i, string(make([]byte, i%97)))
				off, length := p.AllocBytes([]byte(s))
				mine = append(mine, written{off, length, s})

				// Interleave a resolution of an earlier write, so reads and
				// writes from different goroutines overlap in time.
				if len(mine) > 1 {
					prev := mine[len(mine)-2]
					if got := string(p.Bytes(prev.off, prev.length)); got != prev.want {
						errCh <- fmt.Errorf("g%d: readback mismatch: got %q want %q", g, got, prev.want)
						return
					}
				}
			}
			// Final pass: everything this goroutine wrote must still resolve
			// after all the concurrent growth.
			for _, w := range mine {
				if got := string(p.Bytes(w.off, w.length)); got != w.want {
					errCh <- fmt.Errorf("g%d: post-pass mismatch: got %q want %q", g, got, w.want)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestPermIsTheOnlySharedContext pins the scope of the locking change: only
// the permanent context pays for synchronisation. Every per-session,
// per-statement and per-expression context is single-owner by contract and
// must keep the lock-free path — the whole design of mctx depends on
// allocation being a bare bump pointer there.
func TestPermIsTheOnlySharedContext(t *testing.T) {
	if !Perm().isShared() {
		t.Error("Perm() must be treated as shared")
	}
	sess := Acquire(nil, KindSession)
	defer sess.Release()
	stmt := Acquire(sess, KindStmt)
	expr := Acquire(stmt, KindExpr)
	for _, c := range []*Context{sess, stmt, expr} {
		if c.isShared() {
			t.Errorf("context kind %v must not take the shared path", c.kind)
		}
	}
}
