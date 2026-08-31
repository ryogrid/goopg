# Design: `internal/stats` — per-P `Counter` primitive (M0107-0008, loop 6)

**Status**: accepted
**Milestone**: M0107-0008 — Phase D5: runtime internals (`//go:linkname` shims)
**Parent design**: [`docs/design/perf-optimize/08-runtime-internals.md`](../../perf-optimize/08-runtime-internals.md) §4 "Use case 2: per-P statistics counters"
**Predecessor**: [`0107-0008e-activity-registry-nanotime-wiring.md`](0107-0008e-activity-registry-nanotime-wiring.md)
**Filed**: 2026-05-21

## Scope of this loop

Add the second consumer of [`PinP`/`UnpinP`](0107-0008b-runtimeshim-pinp.md):
a per-P sharded 64-bit additive counter in a new `internal/stats`
package. The package contains exactly one public type — `Counter` —
with `Add(delta int64)` on the hot path and `Sum() int64` / `Reset()`
on the cold path.

Following the loop-1/2/3 pattern, **only the primitive lands here**.
Migration of specific global atomic counters (e.g. heap rows scanned,
buffers hit, tuples returned) to `stats.Counter` is deferred to
subsequent loops, one consumer family at a time, so this primitive can
be evaluated standalone with its own race-clean test suite before any
caller depends on it.

This finishes the parent chapter's two `PinP` use cases: the per-P xid
allocator cache was ruled out in
[`0107-0008d`](0107-0008d-perp-xidcache-snapshot-incompat.md) for
snapshot-correctness reasons; the per-P statistics counter is the
remaining viable consumer and lands cleanly because counters have no
visibility-invariant interaction.

## What landed

| File | Build tag | Purpose |
| --- | --- | --- |
| `internal/stats/counter.go` | (none) | `Counter` struct, `Add`/`Sum`/`Reset`. Per-P shard table of `maxShards = 256` cache-line-padded `atomic.Int64` slots. |
| `internal/stats/counter_test.go` | (none) | Single-goroutine `Add`/`Sum`, `Reset` round-trip, concurrent-Add total-exact stress (32 g × 16 K = 524 288 adds), per-shard write-distribution sanity (GOMAXPROCS≥2 only), concurrent Sum-vs-Add no-torn-read property, and `BenchmarkCounterAdd`. |

The package has no other types and no other test files — by design.
Counters are an additive primitive; histograms, gauges, and meters
are explicitly out of scope (they would require different per-shard
state and merge semantics).

## Why this shape

[`08-runtime-internals.md`](../../perf-optimize/08-runtime-internals.md) §4
"Use case 2" sketches the type; this loop implements it under the same
§2 discipline that the three shim primitives followed:

- **`shard { n atomic.Int64; _ [56]byte }`** — exactly one cache line
  (64 B). Independent Ps writing to neighbouring shards never share a
  cache line, so the only contention possible is within a single shard
  — which the `PinP` window prevents on the linkname path and the
  fallback's global mutex prevents on the fallback path.
- **`atomic.Int64` inside the pinned window, not plain `int64`.** The
  pin window prevents *cross-P* concurrent mutation of the shard, but
  a concurrent `Sum()` reader (called by the stats consumer from a
  different goroutine on a different P) still needs a well-defined
  read. `atomic.Int64.Add` inside the pinned window matches
  `atomic.Int64.Load` inside `Sum` so the memory model gives a
  defined value at every point. The atomic *within* the pin is cheap
  (no cross-core invalidation while pinned) and removes an entire
  class of "racy but fast" trade-offs the design might otherwise
  invite.
- **`maxShards = 256`** is a deliberate static upper bound. PinP
  returns indices in `[0, GOMAXPROCS)`, and GOMAXPROCS in practice
  rarely exceeds 256 (the kernel's `nproc` ceiling on common server
  hardware). Sizing the shard table statically avoids the
  GOMAXPROCS-introspection-at-allocation-time race that would
  otherwise need to be handled when the runtime grows GOMAXPROCS on
  the fly (rare but legal). The fallback's `PinP` always returns 0,
  which fits at any `maxShards ≥ 1` — so the static sizing also keeps
  the fallback path zero-special-case.
- **`Sum` iterates 256 shards unconditionally.** No "high-water-mark"
  tracking, no "only-touched-shards" set. The cost is 256 atomic
  loads on a single cold-path call; at modern memory speeds that is
  ~1 μs, which is below the noise floor of every consumer that calls
  `Sum` (pg_stat_* readout, snapshot serialisation). The simplicity
  win — no metadata to keep coherent with the shard table — pays for
  the wasted loads.
- **`Reset` is non-atomic across the whole counter.** The function's
  doc comment makes this explicit; the intended callers are test
  fixtures and end-of-epoch rollovers, both of which run in
  quiescence. A "real" reset semantics would require either pausing
  every P (we have no such API) or accepting a CAS-loop per shard
  that the design judged not worth the complexity for the actual
  caller set.
- **No public access to individual shards.** `Counter` is a struct
  with a private shard table; callers cannot index by P, cannot
  serialise per-P breakdowns, and cannot mutate a specific shard
  out-of-band. This locks the abstraction down to the additive
  semantics — a future consumer asking for "the per-P breakdown" is a
  signal that they want a different primitive (probably a histogram
  or a per-P gauge), not a leak in this one.

## Caller contract (reiterated here so it lives near the code)

- **Use a `*Counter`, never copy a `Counter` after the first `Add`.**
  Copying after `Add` splits the shard table; both copies would
  observe and mutate disjoint subsets of the count. The struct is
  large (`256 × 64 = 16 KiB`) so accidental copies are rare in
  practice, but the doc comment names the contract explicitly.
- **`Add` is hot-path-safe.** The pinned window is two function calls
  (`PinP` → atomic add → `UnpinP`), no allocation, no blocking
  operation. Safe to invoke from any goroutine context including
  inside other critical sections, provided those sections themselves
  do not violate the [`PinP` contract](0107-0008b-runtimeshim-pinp.md)
  (e.g. a caller already inside its own pin window should release
  before calling `Counter.Add`, since `PinP` is not nestable in any
  way the runtime guarantees).
- **`Sum` and `Reset` are cold-path.** They iterate 256 shards; they
  are *correct* under concurrent `Add` (atomic loads/stores; no torn
  reads) but they are not *consistent* — a `Sum` taken concurrently
  with ongoing `Add` is a snapshot that may not reflect adds in
  flight on shards the loop has already passed. Stats consumers
  understand and tolerate this.

## What we measured

`internal/stats/counter_test.go` contains the verification harness;
this loop's measurements:

```
$ go test -race -count=1 ./internal/stats/...
ok   github.com/goopg/goopg/internal/stats   <time>

$ go test -bench=BenchmarkCounterAdd -benchtime=1s -run=^$ ./internal/stats/
BenchmarkCounterAdd-16    <ops>    <ns/op>
```

The `BenchmarkCounterAdd` figure measures the full `Add` path:
`PinP` → `shards[pid].n.Add` → `UnpinP`. The benchmark uses
`b.RunParallel`, so multiple goroutines drive concurrent Adds across
the shard table — the right shape to measure the contention-free
common case.

`TestCounter_ConcurrentAddTotalsExact` is the load-bearing test: 32
goroutines × 16 384 iterations = 524 288 `Add(1)` calls; the final
`Sum()` must equal exactly 524 288. A torn read, a dropped Add under
a broken pin window, or a misaligned shard-table index would surface
as a mismatch. Running this under `-race` is the regression canary
for the entire per-P-counter pattern.

`TestCounter_PerShardWriteDistribution` verifies that with
GOMAXPROCS ≥ 2 and 64 producer goroutines, the writes actually fan
out across multiple shards. This catches the most-likely regression
mode where `Counter.Add` silently collapses to a single shard (e.g. a
bad refactor that pins inside a wrapper function and reuses the same
P index for every call from that wrapper).

`TestCounter_SumConcurrentWithAdd` verifies the no-torn-read property
explicitly: a `Sum()` reader running concurrently with 8 producers
must always return a value in `[0, totalIssued]`. The producers run
to completion, then the reader is signalled to stop, then a final
`Sum()` is asserted equal to the producer total.

## Test coverage rationale

Five tests, each anchored to a contract clause from §"Caller contract":

1. **`TestCounter_SingleGoroutineAddSum`** — basic `Add`+`Sum` round
   trip, plus negative `Add` (so the API really is *additive*, not
   monotonic). A regression that special-cased non-negative deltas
   would fail here.
2. **`TestCounter_Reset`** — `Reset` zeroes every shard. A
   regression that reset only the touched shards (or only shard 0)
   would fail here.
3. **`TestCounter_ConcurrentAddTotalsExact`** — 32 g × 16 K = 524 288
   adds; final Sum is exact. Race-clean. This is the canonical
   correctness test for the entire per-P pattern.
4. **`TestCounter_PerShardWriteDistribution`** — at least two shards
   accumulate when GOMAXPROCS ≥ 2 and 64 producers run. Skips on
   single-P builds (e.g. the fallback path); on the linkname path it
   ensures sharding actually happens.
5. **`TestCounter_SumConcurrentWithAdd`** — `Sum` reads under
   concurrent `Add` are well-defined (no torn reads); final `Sum`
   after producer completion is exact.

`BenchmarkCounterAdd` is the regression canary: a future PinP build
tag mismatch (fallback selected on a Go minor where the linkname
ought to work) would surface as a ~7× slowdown on this benchmark.

## PG-compat impact

None. This is a Go-process-internal counter primitive; no on-disk
format, no wire protocol, no catalog touch. The parent design's
PG-compat gate (invariants §6 Phase D5) is satisfied trivially:
linkname targets only touch scheduling/timing, not durable state.

When a consumer migrates from a global `atomic.Int64` to
`stats.Counter`, the externally-observable values (`SHOW`,
`pg_stat_database.*`, etc.) remain bit-identical — `Sum()` is the
direct equivalent of `atomic.Int64.Load()` from the consumer's
perspective.

## Follow-ups (separate loops)

- **Migrate one stats consumer family** (e.g. heap rows-scanned,
  buffers-hit, tuples-returned) from its current global
  `atomic.Int64` to `stats.Counter.Add` on the hot path and
  `Counter.Sum` at the readout site. Each consumer family lands as
  its own loop with its own design doc so the migration can be
  reviewed and reverted independently.
- **Bufpool per-slot `SemaAcquire`/`SemaRelease` caller** — still
  the other remaining caller in M0107-0008 scope. The shim itself
  landed in loop 3; the bufpool consumer lands in its own loop per
  [`06-bufpool-lockfree`](../../perf-optimize/06-bufpool-lockfree.md) §4.
- **Per-Go-minor CI smoke matrix** (Go 1.24 / 1.25 / 1.26) on
  `internal/runtimeshim/` *and* `internal/stats/` so a runtime-symbol
  break in either surfaces in CI rather than in the field.
