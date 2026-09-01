# Module: `internal/port`

The **platform/runtime portability layer** — a small collection of packages that
reach into Go runtime internals (via `//go:linkname`) to recover the
RDBMS-grade primitives goopg's hot paths need, each paired with a pure-Go
fallback for unsupported toolchains. It is the Go analogue of PG's
`src/port/` and `src/include/port/`.

## Sub-packages

### `internal/port/runtimeshim` — runtime-internal primitives

The bounded set of `//go:linkname` accesses into Go runtime internals, each
paired with a tag-inverse fallback using public APIs.

| File | LOC | Role |
|---|---|---|
| `doc.go` | 28 | Package discipline statement (design `08-runtime-internals.md`). |
| `nanotime_linkname.go` | 24 | `Nanotime()` → `runtime.nanotime` (build-tagged `go1.24 && !go1.27 && !noLinkname`). |
| `nanotime_fallback.go` | 11 | `Nanotime()` → `time.Now().UnixNano()` (inverse tag). |
| `sema_linkname.go` | 46 | `SemaAcquire`/`SemaRelease` → `sync.runtime_Semacquire`/`sync.runtime_Semrelease`. |
| `sema_fallback.go` | 58 | Fallback via a single global mutex + per-cell `sync.Cond` map. |
| `pinp_linkname.go` | 40 | `PinP`/`UnpinP` → `runtime.procPin`/`runtime.procUnpin`; `PinP` returns the P index. |
| `pinp_fallback.go` | 36 | Fallback via a global mutex, always returns P index 0. |
| `nanotime_test.go` | 66 | Monotonicity/non-decreasing, fallback parity. |
| `sema_test.go` | 177 | Acquire/release ordering, wake-one semantics, zero-value cell, stress. |
| `pinp_test.go` | 124 | Pin/unpin balance, P-index stability. |
| `pinp_recursive_test.go` | 30 | Nested PinP regression (linkname-tag-gated; the fallback's non-reentrant contract differs). |

**`Nanotime()`** — a direct call to `runtime.nanotime` (monotonic clock)
avoiding the `time.Now()` → `time.Since` indirection. It is a single-MOV read
of the per-OS monotonic counter (~5 ns on Linux/amd64) vs ~50 ns for
`time.Now()`, which adds VDSO + wall-clock conversion overhead. The result is
monotonically non-decreasing within a process lifetime, expressed in
nanoseconds, but is NOT a wall-clock value and NOT comparable across processes
or after suspend/resume. Used by the activity registry's event timing and the
WAL writer's commit-delay measurement.

**`SemaAcquire`/`SemaRelease`** — the runtime semaphore primitives
(`sync.runtime_Semacquire` / `sync.runtime_Semrelease`) that back
`sync.Mutex`/`sync.Cond`. goopg uses the raw semaphore (not `sync.Cond`) for
buffer-pool slot wait — materially cheaper because there is no internal mutex on
the runtime side. The linkname targets are the `sync`-package-internal aliases
(`sync.runtime_Semacquire`, not `runtime.semacquire`) because those are the de
facto stable external API the stdlib itself depends on and have tracked the
runtime's internal renames across Go versions. The semaphore is a single
`uint32` cell owned by the caller, identified by address (never by value).
`SemaRelease` uses `handoff=false, skipframes=0` (matching `sync.Mutex.Unlock`),
the right default for buffer-pool "I/O finished; anybody waiting can proceed"
notifications.

**`PinP`/`UnpinP`** — processor-pin the current goroutine to a logical
processor (`runtime_procPin` / `runtime_procUnpin`) so a lock-free
read/modify/write across two words (e.g., the `Slot` atomic-state update +
arena pointer) is not preempted mid-operation. While pinned, the runtime
increments `m.locks`, disabling preemption and goroutine migration; per-P
sharded data can be mutated without synchronization for the pinned window.
`PinP` returns the P's integer index in `[0, GOMAXPROCS)`. Callers MUST call
`UnpinP` before any potentially-blocking operation (no channel send/recv, no
mutex that may wait, no syscall, no GC-able allocation that might trigger an
STW — violating this risks deadlock with the runtime's preemption logic). The
pin/unpin pair must be balanced on every code path; production usage embeds
`UnpinP` immediately without a defer (defers add ~20 ns vs inline unpin, which
negates the win for sub-100-ns hot paths).

**Discipline** (from `docs/design/perf-optimize/08-runtime-internals.md`):
- One package holds every linkname site.
- One build-tag pattern per site: `go1.X && !go1.Y && !noLinkname` (currently
  `go1.24 && !go1.27`). Bumping `Y` is the explicit "we tested it" gesture.
- `noLinkname` is an operator escape hatch that forces the fallback path on a
  supported toolchain; `go test -tags noLinkname ./...` smokes the fallback.
- Every site compiles cleanly under `-race`; no `//go:nosplit` on linkname sites.

### `internal/port/gls` — goroutine-local storage

Cheap per-goroutine backend-id storage used to pick a WAL insert-lock stripe.

| File | LOC | Role |
|---|---|---|
| `gls.go` | 45 | `SetBackendID` via `runtime/pprof.SetGoroutineLabels`; package doc with the motivation. |
| `gls_linkname.go` | 96 | `BackendID` via a linkname mirror of `runtime/pprof.runtime_getProfLabel`, plus the layout mirror and runtime self-check probe. |
| `gls_fallback.go` | 14 | `BackendID` always `(0, false)` on unsupported runtimes. |
| `gls_test.go` | 66 | Round-trip, stripe selection, fallback phrasing via `usableForTest`. |

**Mechanism** — `SetBackendID(int32)` stamps the calling goroutine with the
pprof label key `"goopg_backend_id"` (labels are inherited at `go` time); the
read side (`BackendID`) needs a `//go:linkname` to
`runtime/pprof.runtime_getProfLabel` because the standard library exposes no
"read current goroutine's labels" function. The label map is read through a
`labelMapMirror` matching `runtime/pprof.labelMap` layout on the supported Go
window (`struct{ list []labelMirror }`, mirroring
`label.Set → []Label → {Key, Value string}`). A runtime self-check
(`glsUsable = probeLayout()`, run once at package init on a throwaway
goroutine, panic-recovered) validates the linkname read AND the layout mirror
before any hot-path read trusts it; on a layout mismatch `BackendID` returns
`(0, false)` and callers fall back to stripe 0, always valid.

**Motivation** — the WAL append hot path previously called
`activity.LookupCurrentGoroutine()` → `runtime.Stack` on every append, which
was 57% of server CPU under pgbench simple-update. `BackendID` is a pointer
load plus a one-entry label scan, allocation-free. goopg reserves goroutine
pprof labels for this purpose (no other code sets goroutine labels).

## Public API

```go
// runtimeshim — the linkname'd primitives (with pure-Go fallbacks)
func Nanotime() int64                       // monotonic clock (runtime.nanotime)
func SemaAcquire(s *uint32)                 // block until *s > 0, then decrement
func SemaRelease(s *uint32)                 // increment *s, wake one acquirer
func PinP() int                             // pin goroutine to current P, return P index
func UnpinP()                               // unpin from P

// gls — goroutine-local backend id
func SetBackendID(id int32)                 // stamp pprof label (cold path)
func BackendID() (int32, bool)              // read back (WAL hot path)
```

## Internal structure

```mermaid
flowchart TD
    subgraph runtimeshim
        subgraph linkname build (go1.24 && !go1.27 && !noLinkname)
            NL[Nanotime → runtime.nanotime]
            SA[SemaAcquire → sync.runtime_Semacquire]
            SR[SemaRelease → sync.runtime_Semrelease]
            PP[PinP → runtime.procPin]
            PU[UnpinP → runtime.procUnpin]
        end
        subgraph fallback build (inverse tag / noLinkname)
            NFL[Nanotime → time.Now().UnixNano]
            SFL[Sema via global mutex + sync.Cond map]
            PFL[PinP/UnpinP via global mutex, P index 0]
        end
    end

    subgraph gls
        SET[SetBackendID → pprof.SetGoroutineLabels]
        PROBE[glsUsable = probeLayout<br/>recovered round-trip on throwaway goroutine]
        READ[BackendID → linkname runtime_getProfLabel<br/>+ labelMapMirror scan]
        SET --> PROBE
        PROBE -->|usable| READ
        PROBE -->|mismatch| FB2[(0,false) → stripe 0]
    end
```

- **`runtimeshim`** — each primitive has three files: the `//go:linkname`
  site (`*_linkname.go`, build-tagged `go1.X && !go1.Y && !noLinkname`), the
  pure-Go fallback (`*_fallback.go`, inverse tag), and the exported wrapper.
  The semaphore is a single `uint32` cell owned by the caller; `SemaAcquire`
  parks via the runtime's goroutine-park machinery (no internal mutex, unlike
  `sync.Cond`). The fallback semaphore collapses every distinct `*uint32` cell
  onto a single global mutex + `sync.Cond` pool (the map grows monotonically;
  cells are not GC'd because the linkname path has no cleanup either).
  `PinP`/`UnpinP` bracket two-word lock-free updates; the fallback collapses
  per-P sharding to a single virtual P (index 0) and its mutex is
  non-reentrant (the linkname path supports nesting via `m.locks`).
- **`gls`** — `SetBackendID` calls `runtime/pprof.SetGoroutineLabels` (labels
  are inherited by spawned goroutines); `BackendID` reads the current label via
  a linkname mirror of `runtime/pprof.runtime_getProfLabel` (the stdlib exposes
  no "read my labels" function). A runtime self-check detects layout drift and
  returns `(0, false)`, so callers fall back to stripe 0.

### How the WAL stripe uses `gls`

`SetBackendID` runs once per connection at backend startup (cold path, one
small allocation). The WAL append path calls `BackendID()` on every append; the
returned id selects one of the WAL insert-lock stripes so concurrent backends
rarely contend on the same stripe. If `BackendID` reports `(0, false)` — an
unset label, an unsupported runtime, or a layout-mismatch degradation — the
caller falls back to stripe 0, which is always valid (just less striping).

## Dependencies

- **Used by** — `internal/storage` (buffer pool slot waits, pin/unpin),
  `internal/access/transam/xlog` (WAL append stripe, commit-delay timing,
  lock-free tail update), `internal/utils/activity` (event timing).
- **Uses** — Go runtime internals only (via linkname); no imports into other
  `internal/` packages (`gls` imports `context`, `runtime/pprof`, `strconv`,
  `unsafe`; `runtimeshim` imports only `sync`/`time`/`unsafe` in fallbacks).

## Notable patterns / gotchas

- **`SemaAcquire` may park** — it is NOT safe to call inside a `PinP`/`UnpinP`
  window; the two primitives are complementary, not nestable.
- **`PinP` may not block** — between `PinP` and `UnpinP` the goroutine MUST
  NOT do anything that can block (channel, mutex, syscall, allocation that
  might trigger STW) or the runtime's preemption logic can deadlock. Deferring
  `UnpinP` costs ~20 ns, so hot paths inline it.
- **Runtime version window** — a linkname target's symbol shape may change in
  a future Go minor; the build tags confine each site to the tested window
  (`go1.24..go1.26`), and the fallback file covers everything outside it.
  Bumping the window requires re-verifying each linkname against the new
  toolchain (`make race-gate` + the `-tags noLinkname` smoke).
- **`Nanotime` ≠ wall clock** — `Nanotime` returns monotonic time; do not use
  it where PG semantics require `clock_timestamp()`/wall time. It is also NOT
  comparable across processes or after suspend/resume.
- **pprof labels are reserved** — the WAL backend-id stripe depends on the
  goroutine's pprof labels being exclusively goopg's; setting other labels on
  the same goroutine would corrupt `BackendID`.
- **Fallback contract differences** — the fallback semaphore serializes every
  operation under one global mutex (correct, slower); the fallback `PinP` is
  non-reentrant (a nested call deadlocks) whereas the linkname path supports
  nesting via `m.locks` — `pinp_recursive_test.go` covers the linkname shape.
- **`gls` self-check** — `probeLayout` runs once at init on a throwaway
  goroutine and panic-recover guards a wild pointer deref from a layout
  mismatch, so a bad runtime degrades to stripe 0 instead of crashing the
  process.
- **Do not promote the window casually** — moving the upper bound past
  `go1.26` requires re-verifying every linkname target (including the pprof
  `labelMap` layout mirrored by `gls_linkname.go`) on the new toolchain before
  bumping the tags.