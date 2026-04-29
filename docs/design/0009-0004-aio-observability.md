# 0009-0004 — AIO observability (M0009)

Status: accepted (first slice — aggregate counters + startup log)

## Numbering note

The M0009 milestone definition originally reserved this slot
(`0009-0004`) for "AIO observability" — the M0009 / 0009-0003
substrate slice (`aio-storage-integration`) bumped the
checkpointer/WAL doc to `0009-0005` and observability stays at
`0009-0004` as planned. Renumbering trail recorded in
`docs/design/0009-0003-aio-storage-integration.md`.

## Goal

Give an operator a structured surface for "is AIO running, and
is it making progress?" — without per-handle bookkeeping
overhead in this first cut. Per-I/O `pg_aios` (one row per
outstanding I/O), wait events, and per-relation counters land
in a follow-up slice once the engine grows a per-handle
tracking map.

## What this slice delivers

### `pg_stat_aio` view

`internal/initdb/aio_views.go` ships `registerStatAIOView`
backed by the `*aio.Engine` attached during `initdb.Open`.
Single-row when an engine is attached, zero-row when none is
(SELECT against the view still works on synchronous
deployments — no missing-table error).

Columns mirror `aio.Stats` plus the chosen method:

| Column | Type | Source |
|---|---|---|
| `method` | text | `Engine.Method()` (`sync` / `worker`) |
| `submitted` | text | `Stats.Submitted` (monotonic uint64) |
| `completed` | text | `Stats.Completed` |
| `errored` | text | `Stats.Errored` (excludes EOF; matches engine's "EOF is expected" semantics) |
| `in_flight` | text | `Stats.InFlight` (currently outstanding) |

Wired into `Open` next to the existing replication / pubsub
views.

### Startup log line

`cmd/goopg start` emits a structured slog line right after
Open returns, gated on `rt.AIO != nil`:

```
event=aio_engine_attached method=worker workers=3 max_concurrency=12
```

Mirrors the upstream "AIO method = ..." line shape from
PostgreSQL 18. Surfaces the chosen method, worker count, and
in-flight cap so an operator triaging I/O-bound workloads can
verify the live config without grepping through GUCs.

The existing `event=aio_method_fallback` warn-level line
(emitted when `io_uring` is requested but unavailable) covers
the negative case.

## Verification

- **`internal/initdb/aio_views_test.go::TestStatAIOViewEmptyWithoutEngine`**:
  view registers cleanly with a nil engine and emits zero rows.
  SELECT * against `pg_stat_aio` works on synchronous deployments.
- **`internal/initdb/aio_views_test.go::TestStatAIOViewReflectsEngineCounters`**:
  pre-Submit snapshot has zero counters and the right method
  label; submitting one read against an in-memory file and
  Waiting on it bumps `submitted` and `completed` to 1; the
  `in_flight` column is 0 after Wait.

## Per-I/O view (`pg_aios`)

Layered on top of per-handle engine tracking that landed in
the same slice:

```go
// internal/aio/aio.go
func (e *Engine) InFlight() []InFlightInfo
type InFlightInfo struct {
    ID          uint64
    Direction   Direction
    Length      int
    Offset      int64
    SubmittedAt time.Time
}
```

Each `Submit` calls `engine.registerInFlight(h, op)` which:

1. Bumps `Engine.nextID` (an `atomic.Uint64`) and stamps the
   ID + `submittedAt` on the Handle.
2. Builds an `inFlightEntry { id, submittedAt, direction,
   length, offset }` (compact; no pointers to Op or Buffer
   so large allocations aren't pinned longer than needed).
3. Inserts into `Engine.inflight` under the
   `Engine.inflightMu sync.RWMutex`.

Each completion calls `engine.finishHandle(h, r)`, which
combines the previous `completionBookkeeping(r)` work
(`completed.Add(1)` + `errored` accounting + `inFlight.Add(-1)`)
with `delete(e.inflight, h.id)` and the final `h.finish(r)` —
one call site, coherent counters + inflight map.

The mutex is only acquired on `Submit` / completion — never
on the I/O hot path, which stays on atomic counters. For a
sustained workload of hundreds of in-flight I/Os the
contention is negligible compared to the per-I/O syscall.

`Engine.InFlight()` returns a sorted copy (sort key: ID,
monotonic by submission order) so consumers see rows in
stable oldest-first order. Empty slice when no Ops are in
flight.

`registerPgAiosView` installs
`pg_catalog.pg_aios` backed by `Engine.InFlight()`:

| Column | Source |
|---|---|
| `io_id` | InFlightInfo.ID |
| `operation` | `Direction.String()` (read/write) |
| `off` | InFlightInfo.Offset |
| `length` | InFlightInfo.Length |
| `submitted_at` | RFC3339Nano UTC |
| `elapsed_us` | `time.Since(SubmittedAt).Microseconds()` |

Mirrors upstream's `pg_aios()` set-returning function shape
closely enough that `\watch pg_aios` muscle memory transfers.
Some upstream fields (`target`, `target_desc`, `raw_result`)
are not yet tracked and would need plumbing the relfile
identity through the AIO seam — deferred.

## `pg_aios.target_desc` (landed)

`aio.Op` carries a free-form `Target string` field; the
engine threads it through `inFlightEntry` and surfaces it on
`InFlightInfo`. `storage.AIOSubmitOp` and `wal.AIOSubmitOp`
both gained matching fields, and the initdb adapters
propagate them through. The storage `Manager.PrefetchBlock`
/ `Manager.WriteBlockAIO` paths stamp `relFile.path` on every
submit; `wal.state.writeAt` stamps `f.Name()` (the segment
file path). `pg_aios` grew a `target_desc` column rendering
that string verbatim. Empty strings render blank for callers
that don't set it. Closes the upstream-shape gap:
`\watch pg_aios` now lets an operator see exactly which
relfile or WAL segment a stalled I/O targets.

## What this slice doesn't deliver

- **AIO wait events.** "Waiting on AIO completion" hasn't
  been registered with the existing wait-event surface
  (the pg_stat_activity-shaped one used for lock waits in
  M0002). Now unblocked — composes with the existing
  wait-event registry once a follow-up wires it.
- **Per-relation / per-direction counter breakdown.** A
  future view could split `submitted` etc. by `Direction`
  (read/write) and by relfile, but the engine doesn't track
  those today.
- **Histograms / latency percentiles.** Out of scope; the
  observability surface here is for "is it running," not for
  performance regression triage.

## Cross-references

- AIO core + counters source: `0009-0001-aio-core.md`.
- Engine lifecycle wiring (where the view's engine handle
  comes from): `0009-0003-aio-storage-integration.md`.
- Future caller integrations whose work this view eventually
  surfaces: `0009-0005-aio-checkpointer-and-wal.md` (planned).
- Upstream:
  - `postgres/src/backend/storage/aio/aio_io.c` —
    `pg_aios()` set-returning function (the upstream
    per-I/O surface).
  - `postgres/src/backend/utils/adt/pgstatfuncs.c` —
    `pg_stat_get_io()` aggregate-counter shape this view
    most closely mirrors.
