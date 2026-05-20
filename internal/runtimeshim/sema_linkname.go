//go:build go1.24 && !go1.27 && !noLinkname

package runtimeshim

import _ "unsafe" // required for //go:linkname

// runtime_Semacquire / runtime_Semrelease are the well-known runtime
// semaphore primitives that sync.Mutex, sync.WaitGroup, sync.Cond and
// other synchronisation tools build upon. The linkname targets are the
// `sync`-package-internal aliases (sync.runtime_Semacquire,
// sync.runtime_Semrelease) rather than the runtime-internal
// `runtime.semacquire` / `runtime.semrelease` names, because the
// `sync.runtime_*` symbols are the de facto stable external API the
// standard library itself depends on and have therefore tracked the
// runtime's internal renames across Go versions.
//
// The semaphore is a single uint32 cell owned by the caller. Acquire
// blocks until *s > 0 and atomically decrements; Release atomically
// increments *s and wakes one waiter. There is no internal mutex on
// the runtime side — wait-list bookkeeping uses the runtime's
// goroutine-park machinery directly, which is materially cheaper than
// the sync.Cond pattern (sync.Cond owns an explicit mutex).
//
//go:linkname runtime_Semacquire sync.runtime_Semacquire
func runtime_Semacquire(s *uint32)

//go:linkname runtime_Semrelease sync.runtime_Semrelease
func runtime_Semrelease(s *uint32, handoff bool, skipframes int)

// SemaAcquire blocks until the semaphore at *s is > 0, then atomically
// decrements it and returns. *s must be a stable address for the
// lifetime of any concurrent acquirer or releaser; per the runtime's
// contract the cell is identified by its address, not by its value.
//
// The function may park the calling goroutine; it is therefore NOT
// safe to call inside a PinP/UnpinP window. The two primitives are
// complementary, not nestable.
func SemaAcquire(s *uint32) { runtime_Semacquire(s) }

// SemaRelease atomically increments *s and wakes one acquirer parked
// on the same address, if any. The handoff=false / skipframes=0
// arguments match sync.Mutex.Unlock's call site: non-handoff release
// is the right default for buffer-pool-style "I/O finished; anybody
// waiting on this slot can now proceed" notifications, where any
// pending acquirer is equally well-suited to take ownership.
func SemaRelease(s *uint32) { runtime_Semrelease(s, false, 0) }
