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

- `nanotime_linkname.go` / `nanotime_fallback.go` — `Nanotime()`: a direct
  call to `runtime.nanotime` (monotonic clock) avoiding the `time.Now()` →
  `time.Since` indirection. Used by the activity registry's event timing and
  the WAL writer's commit-delay measurement.
- `sema_linkname.go` / `sema_fallback.go` — `SemaAcquire(*uint32)` /
  `SemaRelease(*uint32)`: the runtime semaphore primitives
  (`sync.runtime_Semacquire` / `sync.runtime_Semrelease`) that back
  `sync.Mutex`/`sync.Cond`. goopg uses the raw semaphore (not `sync.Cond`) for
  buffer-pool slot wait — materially cheaper because there is no internal
  mutex on the runtime side.
- `pinp_linkname.go` / `pinp_fallback.go` — `PinP()`/`UnpinP()`: processor-pin
  the current goroutine to a logical processor (`runtime_procPin` /
  `runtime_procUnpin`) so a lock-free read/modify/write across two words
  (e.g., the `Slot` atomic-state update + arena pointer) is not preempted
  mid-operation. Used by the buffer-pool and WAL hot paths.

**Discipline** (from `docs/design/perf-optimize/08-runtime-internals.md`):
- One package holds every linkname site.
- One build-tag pattern per site: `go1.X && !go1.Y && !noLinkname` (currently
  `go1.24 && !go1.27`). Bumping `Y` is the explicit "we tested it" gesture.
- `noLinkname` is an operator escape hatch that forces the fallback path on a
  supported toolchain; `go test -tags noLinkname ./...` smokes the fallback.
- Every site compiles cleanly under `-race`; no `//go:nosplit` on linkname sites.

### `internal/port/gls` — goroutine-local storage

Cheap per-goroutine backend-id storage used to pick a WAL insert-lock stripe.

- `gls.go` — `SetBackendID(int32)` / `BackendID() (int32, bool)`: the carrier
  is **pprof goroutine labels** (`runtime/pprof`). `SetBackendID` stamps the
  calling goroutine (labels are inherited at `go` time); the read side
  (`BackendID`) needs a `//go:linkname` to `runtime/pprof.runtime_getProfLabel`
  because the standard library exposes no "read current goroutine's labels"
  function.
- `gls_linkname.go` — the pprof-label mirror (with a runtime self-check; on a
  layout mismatch `BackendID` returns `(0, false)` and callers fall back to
  stripe 0, always valid).
- `gls_fallback.go` — the pure-Go fallback path.

**Motivation** — the WAL append hot path previously called
`activity.LookupCurrentGoroutine()` → `runtime.Stack` on every append, which
was 57% of server CPU under pgbench simple-update. `BackendID` is a pointer
load plus a one-entry label scan, allocation-free. goopg reserves goroutine
pprof labels for this purpose (no other code sets goroutine labels).

## Dependencies

- **Used by** — `internal/storage` (buffer pool slot waits, pin/unpin),
  `internal/access/transam/xlog` (WAL append stripe, commit-delay timing,
  lock-free tail update), `internal/utils/activity` (event timing).
- **Uses** — Go runtime internals only (via linkname); no imports into other
  `internal/` packages.

## Notable patterns / gotchas

- **`SemaAcquire` may park** — it is NOT safe to call inside a `PinP`/`UnpinP`
  window; the two primitives are complementary, not nestable.
- **Runtime version window** — a linkname target's symbol shape may change in
  a future Go minor; the build tags confine each site to the tested window
  (`go1.24..go1.26`), and the fallback file covers everything outside it.
  Bumping the window requires re-verifying each linkname against the new
  toolchain (`make race-gate` + the `-tags noLinkname` smoke).
- **`Nanotime` ≠ wall clock** — `Nanotime` returns monotonic time; do not use
  it where PG semantics require `clock_timestamp()`/wall time.
- **pprof labels are reserved** — the WAL backend-id stripe depends on the
  goroutine's pprof labels being exclusively goopg's; setting other labels on
  the same goroutine would corrupt `BackendID`.