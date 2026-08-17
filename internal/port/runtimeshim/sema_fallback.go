//go:build !go1.24 || go1.27 || noLinkname

package runtimeshim

import "sync"

// fallbackSemaMu serialises every fallback semaphore operation across
// the process. The fallback collapses every distinct *uint32 cell onto
// a single global mutex + sync.Cond pool, trading throughput for
// correctness. The fallback's job is to keep goopg functional on Go
// minors outside the tested linkname window; pgbench-grade performance
// is not a goal here.
var (
	fallbackSemaMu    sync.Mutex
	fallbackSemaConds = map[*uint32]*sync.Cond{}
)

// fallbackCondFor returns the sync.Cond registered for cell s under
// fallbackSemaMu, creating one on first use. The map grows monotonically
// for the process lifetime; cells are not garbage-collected from the
// table because the linkname path has no equivalent cleanup either
// (the runtime's wait list is keyed by address with no destruction
// hook), and we keep the fallback's externally-observable contract
// identical.
func fallbackCondFor(s *uint32) *sync.Cond {
	if c, ok := fallbackSemaConds[s]; ok {
		return c
	}
	c := sync.NewCond(&fallbackSemaMu)
	fallbackSemaConds[s] = c
	return c
}

// SemaAcquire blocks until *s is > 0 under the shared fallback mutex,
// then decrements *s. The Wait loop tolerates spurious wake-ups (none
// occur under the current sync.Cond implementation but the loop keeps
// the contract robust against future changes).
func SemaAcquire(s *uint32) {
	fallbackSemaMu.Lock()
	c := fallbackCondFor(s)
	for *s == 0 {
		c.Wait()
	}
	*s--
	fallbackSemaMu.Unlock()
}

// SemaRelease increments *s and signals one waiter (if any) parked on
// the same cell. Like the linkname variant, the wake-up is
// non-handoff: any waiter is equally eligible to take the freed unit.
func SemaRelease(s *uint32) {
	fallbackSemaMu.Lock()
	*s++
	if c, ok := fallbackSemaConds[s]; ok {
		c.Signal()
	}
	fallbackSemaMu.Unlock()
}
