// Package mvcc implements multi-version concurrency control primitives for goopg.
package mvcc

import (
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

// XidGen is a lock-free, monotonically-increasing XID allocator.
type XidGen struct {
	next atomic.Uint64
}

// Allocate atomically claims the current next-XID and advances the counter.
// Semantics: next stores the NEXT xid to assign; Allocate returns next and
// increments to next+1. This mirrors the old Manager.nextXID pattern.
func (g *XidGen) Allocate() storage.TransactionID {
	return storage.TransactionID(g.next.Add(1) - 1)
}

// Peek returns the current next-XID without advancing it.
func (g *XidGen) Peek() storage.TransactionID {
	return storage.TransactionID(g.next.Load())
}

// SetNext advances the generator to x using a CAS loop; no-op if x <= current.
func (g *XidGen) SetNext(x storage.TransactionID) {
	want := uint64(x)
	for {
		cur := g.next.Load()
		if want <= cur {
			return
		}
		if g.next.CompareAndSwap(cur, want) {
			return
		}
	}
}
