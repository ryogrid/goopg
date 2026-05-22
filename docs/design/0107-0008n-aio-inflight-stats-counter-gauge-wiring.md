# 0107-0008n — AIO Engine.inFlight gauge to stats.Counter

**Milestone**: M0107-0008 (Phase D5 — runtime internals)
**Parent design**: [docs/design/perf-optimize/08-runtime-internals.md §4 Use case 2](perf-optimize/08-runtime-internals.md)
**Predecessors**:
- [[0107-0008f]] `stats.Counter` primitive
- [[0107-0008i]] AIO Engine aggregate totals (Submitted/Completed/Errored)
- [[0107-0008j]] AIO per-direction (Read/Write {Submitted,Completed,Errored,LatencySumMicros})
- [[0107-0008k]] WAL writer drain bytes (Overflow/Flush)
- [[0107-0008l]] Checkpointer aggregates (NumTimed/NumRequested/WriteTimeMs)
- [[0107-0008m]] Bufpool dirty-victim instrumentation

## Why migrate now

`Engine.inFlight` in `internal/aio/aio.go` was the last remaining
hot-path additive site on the AIO Engine that still bounced a single
shared cache line across cores. The three `Submit` paths
(`method_iouring_linux.go:352`, `method_sync.go:56`,
`method_worker.go:50`) each issued `e.inFlight.Add(1)` against the
shared `atomic.Int64`, and `finishHandle` issued `Add(-1)` per
completion (`aio.go:524`). Under `method=worker` these are multi-P
call sites — the engine's worker pool drains completions from
several worker goroutines while client backends fan in submits — so
every Submit and every completion paid the same cross-core
cache-line hop the migrated Submitted/Completed counters
(0107-0008i, 0107-0008j) used to.

Closing this migration also restores storage-shape uniformity on
`Engine`: every additive counter on the Engine is now `stats.Counter`,
matching the chapter §4 directive that per-P sharded counters
replace shared atomic counters on the I/O hot path. The only
remaining `atomic.*` fields on the Engine are explicitly *not*
counters: `readLatencyMaxMicros` / `writeLatencyMaxMicros` need
CAS-based monotonic-forward clamping (advanceMax), and `nextID` is
a monotonic ID allocator whose `Add` return value is consumed
immediately as the new ID.

## Why this is a valid `stats.Counter` use

`stats.Counter` is documented as "a 64-bit additive counter" and its
`Reset` comment hints that production callers should treat `Sum`
as monotonically growing. `inFlight` deviates from the typical
monotonic shape — it takes signed deltas — but the math holds:

```
inFlight gauge value = (#Add(+1)) − (#Add(−1))
                     = Σ_shards (per-shard partial sum)
                     = Counter.Sum()
```

Each `Add(d)` atomically adjusts one shard's `atomic.Int64` by `d`;
`Sum()` aggregates `atomic.LoadInt64` across all shards. A consistent
snapshot is not guaranteed in the middle of a `Sum()` traversal —
ongoing `Add`s on uninspected shards may or may not be reflected —
but that is the same eventual-consistency property the migrated
monotonic counters already accept, and `Stats.InFlight` was an
observability gauge to begin with (read at `pg_aios`-summary scan
time, never as a load-bearing admission-control input). The
contract documented at `Stats()` (line 632 of `aio.go`: "Stats
returns a coherent counter snapshot") is preserved verbatim.

The shard's `atomic.Int64` (not plain `int64`) inside the pin window
is the same arrangement as the monotonic counters use: a concurrent
`Sum` reader on a different P sees a well-defined value because the
shard load is atomic. The signed-delta gauge needs no additional
discipline beyond what `stats.Counter` already provides.

## Hot-path call-site form

The four mutation sites are unchanged in syntactic form because the
untyped constants `1` and `-1` are accepted by both
`atomic.Int64.Add(int64)` and `stats.Counter.Add(int64)`:

| Call site | Before | After |
|-----------|--------|-------|
| `internal/aio/method_iouring_linux.go:352` | `m.engine.inFlight.Add(1)` | `m.engine.inFlight.Add(1)` |
| `internal/aio/method_sync.go:56`           | `m.engine.inFlight.Add(1)` | `m.engine.inFlight.Add(1)` |
| `internal/aio/method_worker.go:50`         | `m.engine.inFlight.Add(1)` | `m.engine.inFlight.Add(1)` |
| `internal/aio/aio.go:524 (finishHandle)`   | `e.inFlight.Add(-1)`      | `e.inFlight.Add(-1)`      |

Only the field type and the single cold-path read change:

```go
// Engine field — before
inFlight atomic.Int64

// Engine field — after
inFlight stats.Counter

// Stats() reader — before
InFlight: e.inFlight.Load(),

// Stats() reader — after
InFlight: e.inFlight.Sum(),
```

`Stats.InFlight` remains `int64`; the externally-observable wire
contract (the `pg_aios`-summary view's column type and value) is
preserved.

## Memory cost

One additional `stats.Counter` per server (one Engine per server).
At `maxShards = 256` × 64 B/shard = 16 KiB. The previous
`atomic.Int64` was 8 B (one cache line shared with neighbouring
fields). Net per-server overhead: ~16 KiB.

Cumulative singleton-consumer cost across the eight landed migrations
on M0107-0008 (BTree + MemRing + AIO totals + AIO per-direction +
WAL drain bytes + Checkpointer + bufpool victim + AIO in-flight)
is 336 KiB per server.

## Why not migrate `nextID`, `readLatencyMaxMicros`, etc.?

The remaining `atomic.*` fields on `Engine` cannot move to
`stats.Counter` without semantic loss:

- **`nextID atomic.Uint64`**: the `Add(1)` return value is consumed
  immediately as the new ID (`id := e.nextID.Add(1)` at
  `aio.go:465`). `stats.Counter.Add` returns nothing. Sharding
  would also break monotonicity (two Ps could hand out the same
  per-shard rank).
- **`readLatencyMaxMicros` / `writeLatencyMaxMicros atomic.Uint64`**:
  CAS-based monotonic-forward clamping (`advanceMax`); per-shard
  max is meaningless for monotonic-forward clamping.
- **per-target `*targetStats` atomic.Uint64**: formally closed as
  *do not migrate* in [[0107-0008j]] — per-target memory
  amplification ~80 MiB worst case, no contention benefit because
  targets are naturally identity-sharded.

## Verification

- `go test -race -count=1 ./internal/aio/` PASS (1.04 s)
- `go test -race -count=1 ./internal/stats/` PASS (1.02 s)
- `go test -race -count=1 ./internal/storage/ ./internal/wal/
  ./internal/runtimeshim/` PASS (5.35 s + 3.19 s + 1.22 s)
- No production-code call sites outside `aio` reach into
  `Engine.inFlight` (verified by grep); only `Stats.InFlight`
  (`int64` value field) is consumed externally, and its type/value
  contract is preserved.

## Migration shopping list — status

After this loop, the M0107-0008 in-scope `stats.Counter` consumer
shopping list is closed. The remaining `atomic.*` sites in the
codebase are either:

- **Last-write-wins state** (LSN advance, clock-sweep cursor,
  bufpool slot state bitfields, bufmap cells) — sharding does not
  fit, since the consumer needs the latest *value*, not a
  cross-shard sum.
- **Monotonic ID allocators** (`xidgen.next`, `Engine.nextID`,
  `toastOIDCounter`, `nextBackendID`, `nextPID`, sequence `current`)
  — the Add return value is the ID; `stats.Counter.Add` returns
  nothing.
- **CAS-clamped max values** (`*latencyMaxMicros`) — `stats.Counter`
  has no CAS primitive.
- **Compare-after-add state machines**
  (`queriesWithoutFreeCounter`) — the return value of Add is
  compared to a threshold each call; the sharded primitive cannot
  provide the post-Add running total cheaply.
- **Per-target stats** — formally closed as *do not migrate* per
  [[0107-0008j]] (memory amplification).
- **Bufpool hit/miss** — those counters do not exist yet; they
  arrive with the lock-free bufpool rewrite ([[0107-0006]]) and
  will be born as `stats.Counter`.

Remaining M0107-0008 work is now the bufpool per-slot Sema wait
caller (consumes [[0107-0008c]]; blocked on M0107-0006 lock-free
bufpool) and the per-Go-minor CI matrix (`08-runtime-internals.md` §2).
