# 08 — Runtime Internals (`//go:linkname` patterns)

This chapter authorises and bounds the use of `//go:linkname` into Go
runtime internals. The practice docs identify three high-value
applications for an RDBMS hot path; this chapter specifies each one
plus the build-tag + fallback discipline that keeps the patterns
maintainable across Go releases.

Cross-references: [[04-mvcc-procarray]] (per-P xid allocator cache
sits in front of `XidGen`), [[05-activity-perbackend]] (`nanotime`
for `stateChange`), [[06-bufpool-lockfree]] (`runtime.semacquire` /
`semrelease` for per-slot wait coordination).

## 1. Why use linkname at all

Practice doc `go_rdbms_performance_techniques.md` §4 and §16 identify
three runtime functions that, when accessed directly via
`//go:linkname`, deliver disproportionate hot-path wins for an RDBMS:

1. **`runtime.nanotime() int64`** — single-MOV monotonic timestamp
   (~5 ns). The public `time.Now()` runs ~50 ns due to VDSO + wall-
   clock conversion. At the rates `activity.WaitEvent*` runs
   (thousands per second), the difference adds up.
2. **`runtime_procPin() int`** / **`runtime_procUnpin()`** — pin the
   current goroutine to the current P (logical processor) and return
   the P's index. Lets us index per-P sharded data with **zero
   atomic operations** while pinned. Used in `sync.Pool` internally;
   we use it for per-P xid allocator caches and per-P stats counters.
3. **`runtime.semacquire(*uint32) / semrelease(*uint32, ...)`** —
   the runtime's internal semaphore primitive. Used by
   [[06-bufpool-lockfree]]'s per-slot I/O-inflight wait. Avoids the
   overhead of `sync.Cond`'s internal mutex.

Other linkname candidates that we **do not** adopt (out of scope
because the wins are marginal in our profile):
- `runtime.fastrand()` — useful for randomised eviction in caches; we
  use clock-sweep, not random.
- `runtime.memmove` — already inlined by the compiler for known sizes.

## 2. Build-tag and fallback discipline

Every `//go:linkname` site lives in a file with a tag that gates the
known-good Go minor versions. Example:

```go
// internal/runtimeshim/nanotime_go124.go
//go:build go1.24 && !go1.27

package runtimeshim

import _ "unsafe"

//go:linkname nanotimeRuntime runtime.nanotime
func nanotimeRuntime() int64

// Nanotime returns a monotonic timestamp in nanoseconds.
func Nanotime() int64 { return nanotimeRuntime() }
```

And the fallback:

```go
// internal/runtimeshim/nanotime_fallback.go
//go:build !go1.24 || go1.27

package runtimeshim

import "time"

// Nanotime falls back to time.Now().UnixNano() when the linkname
// target is not stable on this Go version.
func Nanotime() int64 { return time.Now().UnixNano() }
```

Discipline:

- **One package** (`internal/runtimeshim`) holds every linkname site.
  Callers import only the public API (`runtimeshim.Nanotime`,
  `runtimeshim.PinP`, etc.), never the linkname'd symbols directly.
- **One build-tag pattern**: `go1.X && !go1.Y` where X is the
  minimum-tested version and Y is the first untested major. Bumping
  the Y is the explicit "we've tested this on the new Go" gesture.
- **One fallback file** per linkname site, always selected by the
  inverse tag. The fallback uses public Go APIs; it is correct but
  slower.
- **Race-detector compatibility**: every linkname site compiles
  cleanly under `-race`. Where `//go:nocheckptr` is required (none
  of the three primary uses needs it), it is documented inline with
  a one-line argument for safety.
- **No `//go:nosplit`** on linkname sites; the Go runtime expects
  caller frames to be split.
- **Per-Go-minor smoke test**: CI runs `go test ./internal/runtimeshim/...`
  on every supported Go minor version listed in `go.mod`. A change
  in the runtime symbol shape (rare; happens roughly once per year
  for `runtime.nanotime` historically) is caught here, not in production.

## 3. `Nanotime` for activity timestamps

Used by [[05-activity-perbackend]]'s `WaitEventStart` / `WaitEventEnd`
to record `stateChange`. The full chain:

```go
// internal/activity/registry.go
import "github.com/goopg/goopg/internal/runtimeshim"

func (r *ActivityRegistry) WaitEventStart(procNum int32, t WaitEventType, e WaitEvent) {
    s := &r.slots[procNum]
    s.waitEventInfo.Store(packWait(t, e))
    s.stateChange.Store(runtimeshim.Nanotime())
}
```

At c=100 SO, WaitEventStart fires ~30 K/sec. Saving 45 ns/call ~=
1.35 ms/sec, ~0.14 % CPU. Modest, but cumulative with other linkname
uses (per-P sharding has larger impact). Practice doc §4 quotes it
as "a cheap monotonic timestamp" — we adopt it for the call-rate sites
only, not for human-facing timestamps (which still use
`time.Now().UnixNano()` for display).

Conversion to wall clock for `pg_stat_activity` display: the snapshot
reader computes
```
wallNanos = systemBootWallNanos + (nanotime() - systemBootMonoNanos)
```
where `systemBootWall` and `systemBootMono` are captured once at
server start.

## 4. `PinP` / `UnpinP` for per-P sharding

```go
// internal/runtimeshim/pinp_go124.go
//go:build go1.24 && !go1.27

package runtimeshim

import _ "unsafe"

//go:linkname runtime_procPin runtime.procPin
func runtime_procPin() int

//go:linkname runtime_procUnpin runtime.procUnpin
func runtime_procUnpin()

// PinP pins the current goroutine to its current P and returns the
// P's index. Caller MUST call UnpinP before any operation that can
// block (channel send/recv, mutex acquire, syscall, etc.).
//
//go:nosplit-friendly: callers must not insert defer'd unwinds in
// the pinned window; explicit UnpinP at the end of the critical
// section is the discipline.
func PinP() int { return runtime_procPin() }

// UnpinP releases the pin.
func UnpinP() { runtime_procUnpin() }
```

### Use case 1: per-P transaction-id allocator cache

```go
// internal/mvcc/xidgen.go (extension)

// Each P caches a small batch of pre-allocated xids; xids are issued
// from the cache without atomic ops while pinned. On cache empty,
// fetch a fresh batch from the global atomic counter.

const xidCacheBatch = 32   // xids per refill

type perPCache struct {
    next, end storage.TransactionID   // TransactionID == uint32; 4 B each
    _pad      [56]byte                // pad to 64 B (4+4+56 = 64)
}

type XidGen struct {
    global atomic.Uint64
    caches [maxP]perPCache   // maxP == GOMAXPROCS bound
}

func (g *XidGen) Allocate() storage.TransactionID {
    pid := runtimeshim.PinP()
    c := &g.caches[pid]
    if c.next < c.end {
        x := c.next
        c.next++
        runtimeshim.UnpinP()
        return x
    }
    // Refill: fetch xidCacheBatch from global, install in this P's
    // cache. The global Add is atomic-only (non-blocking) so we stay
    // pinned through it. runtime_procPin disables preemption by
    // incrementing m.locks (runtime invariant), so the goroutine
    // cannot be migrated to a different P while we mutate c — that
    // is what makes the unsynchronised c.next++ safe inside the
    // pinned window.
    base := storage.TransactionID(g.global.Add(xidCacheBatch))
    c.next = base - xidCacheBatch
    c.end  = base
    x := c.next
    c.next++
    runtimeshim.UnpinP()
    return x
}
```

The per-P cache reduces the atomic contention on `g.global` by 32×.
At c=100 SU (~7 K xacts/sec × 1 xid/xact), that drops to ~220 atomic
adds/sec on the global counter — well below any contention threshold.

**Correctness of cached-but-unused xids.** A P may cache xids it never
hands out (e.g., the P sits idle with `c.next < c.end`, then the
process exits or the P's caches are abandoned at shutdown). Those xids
are leaked from the perspective of `g.global` (which has already
advanced past them) but are **never recorded in any procSlot's xid
field** and **never written to CLOG**. A subsequent visibility check
for such a leaked xid `X` consults `clog.GetStatus(X)` which returns
the default "no status recorded" value; the CLOG fast path treats this
identically to an aborted transaction — the leaked xid is invisible to
all snapshots. No correctness hazard, modest xid-space waste (bounded
by `xidCacheBatch × GOMAXPROCS = 32 × 16 = 512` xids per shutdown,
trivially small against the 32-bit xid space).

PG analogue: PG uses a single `XidGenLock` and bulk-allocates xids
inside the lock to amortise. We use the per-P cache + atomic counter,
which is cheaper.

### Use case 2: per-P statistics counters

```go
// internal/stats/counters.go (new)

type Counter struct {
    perP [maxP]paddedInt64
}

type paddedInt64 struct {
    n   int64
    _   [56]byte
}

func (c *Counter) Add(delta int64) {
    pid := runtimeshim.PinP()
    c.perP[pid].n += delta
    runtimeshim.UnpinP()
}

// Sum aggregates across all Ps. Called by stats consumers; not the
// hot path.
func (c *Counter) Sum() int64 {
    var total int64
    for i := range c.perP {
        total += atomic.LoadInt64(&c.perP[i].n)
    }
    return total
}
```

Replaces today's global atomic counters for stats like "rows scanned",
"buffers hit", "tuples returned". Reduces cache-line contention to
zero on the hot path (each P writes to its own padded field). Sum is
slightly inconsistent (a recent Add may not be visible yet) but stats
are eventual-consistency by nature.

Practice doc §16 ("per-P data") sizes this as a meaningful CPU win
for analytics workloads where counters are bumped per row.

## 5. Semaphore primitive for per-slot waits

```go
// internal/runtimeshim/sema_go124.go
//go:build go1.24 && !go1.27

package runtimeshim

import _ "unsafe"

//go:linkname runtime_Semacquire sync.runtime_Semacquire
func runtime_Semacquire(s *uint32)

//go:linkname runtime_Semrelease sync.runtime_Semrelease
func runtime_Semrelease(s *uint32, handoff bool, skipframes int)

// Acquire decrements the semaphore *s, blocking until it's > 0.
func SemaAcquire(s *uint32) { runtime_Semacquire(s) }

// Release increments the semaphore *s and wakes one waiter.
func SemaRelease(s *uint32) { runtime_Semrelease(s, false, 0) }
```

Used by [[06-bufpool-lockfree]] for per-slot I/O-inflight waits. The
slot's wait state is one `uint32` per slot (or per-stripe, depending
on cardinality); waiters call `SemaAcquire` while the I/O is in
flight; the I/O completion calls `SemaRelease`. This replaces the
current per-partition `sync.Cond` machinery, eliminating the
condition variable's internal mutex.

Note the linkname targets are `sync.runtime_Semacquire` (the
`sync`-package-internal aliases) rather than `runtime.semacquire`
directly — the former is the well-known external symbol that
`sync.Mutex`, `sync.WaitGroup`, etc. all use; it is more stable
across Go versions than the runtime-internal name.

Fallback: a small `sync.Mutex + sync.Cond` per slot. Same semantics,
slightly slower wakeups.

## 6. What we don't linkname

The practice docs mention several other patterns. We **decline** the
following because the wins are below the noise floor on our profile:

- **`runtime.fastrand`** — useful for randomised victim selection; we
  use clock-sweep.
- **`runtime.nanotime` for WAL record timestamps** — WAL records
  use LSN, not wall time; the wall-time field is debug-only.
- **`runtime.memmove`** — the compiler already inlines memmove for
  known sizes; for unknown sizes, the standard library's `copy`
  builtin compiles to the same call.
- **`runtime.GoroutineProfile` / `runtime.Stack`** — only used by
  the pprof handlers; not hot-path.

If profiling after the refactor reveals a need for additional
linkname sites, they are added per the same discipline (one shim
package, build-tag-gated, with fallback).

## 7. Race-detector and `unsafe` discipline

Every `//go:linkname` site is tested under `go test -race`. The
specific concerns:

- **`Nanotime`** — pure function; no shared state; race-clean.
- **`PinP` / `UnpinP`** — these manipulate runtime-internal goroutine
  state (`m.locks` counter). They are race-clean by construction
  (the runtime owns the synchronisation). The caller's per-P data
  must be properly aligned and the access pattern must respect the
  pin contract.
- **`SemaAcquire` / `SemaRelease`** — the runtime semaphore is
  designed for cross-goroutine signalling; race-clean.

`//go:nocheckptr` is **not** used by any of the three primary sites
because none of them perform an `unsafe.Pointer` cast that would
trigger the checkptr instrumentation.

## 8. Per-Go-minor maintenance

Three concrete events drive maintenance:

1. **New Go minor released** (e.g., Go 1.25). Update the build tag in
   each linkname file: `//go:build go1.24 && !go1.27` becomes
   `//go:build go1.24 && !go1.28` after testing on 1.25, 1.26, 1.27.
   Run the full test suite + a pgbench c=10 SO sanity run on each
   new minor before bumping the tag.
2. **Runtime symbol disappears** (e.g., Go renames `runtime.nanotime`
   to `runtime.monotime`). Detected by CI failing to link
   `internal/runtimeshim`. Resolution: introduce a new file gated on
   the new Go version (`nanotime_go1XX.go`) calling the new symbol;
   keep the old file gated on the older versions; bump the upper
   bound on the old file to exclude the new minor.
3. **Runtime symbol semantics change** (rare; would require a major
   Go version bump). Caught by the per-Go-minor smoke tests in CI;
   resolution is the same as the rename case.

The `go.mod`'s `go` directive expresses the minimum Go version goopg
builds against; the linkname files' tags expressly enumerate the
*tested* versions. The discipline keeps the brittleness scoped: one
file per primitive, one test per primitive, one CI matrix entry per
Go minor.

## 9. PG counterparts

| goopg concept                | PG counterpart                                                                          |
|------------------------------|-----------------------------------------------------------------------------------------|
| `Nanotime`                   | `INSTR_TIME_SET_CURRENT` macro in `postgres/src/include/portability/instr_time.h`; uses `clock_gettime(CLOCK_MONOTONIC)` |
| `PinP` / per-P sharding      | PG processes are inherently per-process-sharded; closest analogue is each backend's `MyProc` global in `postgres/src/backend/storage/lmgr/proc.c` |
| Per-P xid allocator cache    | `GetNewTransactionId` in `postgres/src/backend/access/transam/varsup.c::GetNewTransactionId` (single global counter under `XidGenLock`; PG does not batch-cache, but the design space is the same) |
| Per-P stats counters         | Per-backend `pgstat_*` flush model in `postgres/src/backend/utils/activity/pgstat.c` (each backend accumulates locally, flushes to shared on a slow cadence) |
| `SemaAcquire` / `SemaRelease`| PG's `PGSemaphore` abstraction in `postgres/src/include/storage/pg_sema.h`; POSIX implementation in `postgres/src/backend/port/posix_sema.c`  |

PG's process model gives it most of the per-P sharding "for free" —
each backend is its own process. We compensate with `runtime_procPin`
to approach the same effect at goroutine granularity.

## 10. Verification

After Phase D5 of [[09-migration-and-rollout]] ships:

- **Compile-time** — `internal/runtimeshim/` exists with the three
  primitives + their fallbacks. `go test ./internal/runtimeshim/...`
  passes on every Go minor in the CI matrix.
- **Micro-bench** — `Nanotime` benchmark shows ~5 ns/op vs
  `time.Now().UnixNano()` ~50 ns/op. `PinP+UnpinP` benchmark shows
  ~3 ns/op total (write to per-P data inside the pin window).
- **Race detector** — all linkname call sites green under `-race`.
- **Per-P xid contention** — at c=100 SU, the `XidGen.global.Add`
  atomic shows < 5 % of write-side mutex profile (down from being a
  bottleneck without the cache).
- **`runtime.futex` reduction** — combined with [[06-bufpool-lockfree]]
  (eliminating partition mutex hand-offs), c=100 SO `runtime.futex`
  cum% drops from 23 % to **< 8 %**.
- **Fallback build** — `go test -tags noLinkname ./...` (or
  equivalent) passes with the public-API fallbacks active; TPS is
  slightly lower but functionality is identical.

If a future Go minor breaks any linkname target, CI fails before the
broken binary ships; the fallback is one tag-flip away from being
the active code path.
