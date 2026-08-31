# 0107-0008e — Activity registry: runtimeshim.Nanotime wiring

Status: ACCEPTED (M0107-0008 loop 5, 2026-05-21).

Parent design: [`docs/design/perf-optimize/08-runtime-internals.md`](../../perf-optimize/08-runtime-internals.md) §4.
Predecessor shim design: [`docs/design/0107-0008-runtimeshim-nanotime.md`](0107-0008-runtimeshim-nanotime.md).

## Why

`ActivityRegistry.WaitEventStart` and `WaitEventEnd` are called on
every protocol-frame boundary — measured at roughly `c × 100 k/s` of
backend traffic in the pgbench c=100 SU baseline. Each call previously
performed two `time.Now().UnixNano()` reads (one indirectly via the
`stateChange` atomic). On linux/amd64 Go 1.25, `time.Now()` clocks at
about 50 ns/op because it issues a `clock_gettime(CLOCK_REALTIME)`
vDSO call and runs the wall-clock-vs-monotonic reconciliation paths
in `runtime.nanotime` and `runtime.walltime` independently.

`runtimeshim.Nanotime()` (loop 1 of M0107-0008) is a `//go:linkname`
binding to `runtime.nanotime` and benchmarks at ~20 ns/op (Go 1.25
build) — about a 2.5× reduction at this call site. At ~30 k/s WaitEvent
calls per active backend, that is ~0.9 µs/s/backend of saved CPU at
c=100, or ~90 µs/s of saved time-spent-in-clock-code across the
pgbench fleet. Small per-call but high cumulative density.

## What

`internal/activity/registry.go` is rewritten so every hot-path timestamp
store goes through `runtimeshim.Nanotime()` instead of
`time.Now().UnixNano()`. Five call sites are updated:

- `WaitEventStart` (hot, ~c × 30 k/s)
- `WaitEventEnd` (hot, ~c × 30 k/s)
- `UpdateState` (warm, ~1/statement)
- `BeginTransaction` / `EndTransaction` (cold, ~1/xact)
- `acquire` (very cold, ~1/connection)

The fields written are `activitySlot.stateChange`, `coldActivity.XactStart`,
and `coldActivity.QueryStart`. All three previously stored
`time.Now().UnixNano()` (wall-clock unix nanos); they now store
`runtimeshim.Nanotime()` (monotonic-since-runtime-start nanos).

## Monotonic → wall-clock conversion

`runtimeshim.Nanotime()` returns a runtime-internal monotonic clock that
counts from an opaque epoch — typically system boot for the
`CLOCK_MONOTONIC`-derived reading the Go runtime uses on Linux. Stored
values are useful as differences (for elapsed-time measurement) but
emitting them straight to `pg_stat_activity` as a wall-clock string
would produce e.g. `1970-01-02T...` for a host that has been up under
30 hours.

`ActivityRegistry` therefore captures the mono↔wall epoch pair once at
construction (`NewActivityRegistry`):

```go
monoEpoch := runtimeshim.Nanotime()
wallEpoch := time.Now().UnixNano()
```

These two reads happen back-to-back on the same goroutine; the
inter-read skew is ≤ 1 µs on modern hardware — irrelevant for the
RFC3339Nano timestamps that surface in `pg_stat_activity`. A new
private helper performs the per-snapshot conversion:

```go
func (r *ActivityRegistry) monoToWall(mono int64) int64 {
    if mono == 0 {
        return 0
    }
    return r.wallEpoch + (mono - r.monoEpoch)
}
```

`Snapshot()` applies `monoToWall` to `stateChange`, `XactStart`, and
`QueryStart` before passing them to `formatNanos()` (which wraps
`time.Unix(0, ns).UTC().Format(time.RFC3339Nano)`). The `mono == 0`
guard preserves the existing "0 → empty string" semantics for cold
fields that have not been set since slot acquire.

Long-uptime drift between the captured `wallEpoch + monoElapsed`
synthesised wall time and the actual `time.Now()` is bounded by the
difference between the kernel's `CLOCK_REALTIME` slewing and its
`CLOCK_MONOTONIC` rate. NTP-induced wall-clock adjustments after
registry creation will not be reflected in synthesised timestamps,
which is the correct behaviour for elapsed-time-style fields: a
backend that started 10 seconds ago should report a `xact_start` 10
seconds before "now" regardless of an intervening `ntpd` slew. For
the wall-clock display of `state_change` etc., users accept the same
order-of-magnitude drift NTP-uncorrected hosts already exhibit; in
practice, wall-clock-vs-monotonic skew on a well-disciplined NTP host
is ≤ 1 ms over 24 h.

## Why not also convert `BackendStart`

`coldActivity.BackendStart` is declared as `int64 // unix nanos` but is
never written anywhere in the package — `coldFromBackend` does not
populate it. It is left untouched (always 0 → `formatNanos` returns
`""`). Promoting it to the same mono encoding is deferred until a
caller exists; the contract `monoToWall(0) == 0` preserves the empty
string semantics either way.

## Why not `PinP` here

The activity registry already uses per-slot atomics for sharded
mutation; a single `Store` does not need a `PinP`/`UnpinP` window.
`PinP` will land separately, in a caller that updates a per-P counter
(per parent chapter §5 "per-P stats counters").

## Verification

- `go test -race -count=1 ./internal/activity/... ./internal/runtimeshim/...` — PASS.
- New regression: `TestActivityRegistryStateChangeIsWallClock` asserts
  the mono→wall conversion in `Snapshot()` produces an RFC3339Nano
  string parseable as a recent wall-clock time (±2 s of the
  surrounding `time.Now()` calls). Also covers `XactStart` via
  `BeginTransaction`.
- Broader regression set: `internal/mvcc`, `internal/server`,
  `internal/executor`, `internal/wal`, `internal/storage`, all PASS
  under `-race -count=1`.
- `internal/initdb` reports pre-existing failures unrelated to this
  change (verified by stashing the diff and reproducing the same
  failure on `master`).

## Related

- [[0107-0008-runtimeshim-nanotime]] — the underlying shim.
- [[0107-0008b-runtimeshim-pinp]] — PinP shim (future per-P counter
  callers).
- [[0107-0008c-runtimeshim-sema]] — Sema shim (future bufpool wait
  callers).
- [[0107-0008d-perp-xidcache-snapshot-incompat]] — caller wiring that
  was found to be incompatible with snapshot semantics; this loop's
  Nanotime caller has no such interaction (purely
  human-display-oriented).
