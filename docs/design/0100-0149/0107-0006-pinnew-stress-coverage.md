# 0107-0006 — `PinNew` Stress Coverage (loop 3)

Milestone: **M0107-0006 — Phase D3: lock-free buffer pool**
Parent design: [`docs/design/perf-optimize/06-bufpool-lockfree.md`](../../perf-optimize/06-bufpool-lockfree.md)
Companions:
- [`docs/design/0107-0006-bufpool-bufmap-correctness.md`](0107-0006-bufpool-bufmap-correctness.md) (loop 1)
- [`docs/design/0107-0006-bufmap-keys-atomic.md`](0107-0006-bufmap-keys-atomic.md) (loop 2)

Status: partial progress — pgbench TPS gates still pending.

## Background

Loop 1 closed three correctness bugs in the partially-landed Phase D3
lock-free buffer pool. Loop 2 rewrote the `bufmap` around per-bucket
atomic key fields and added `TestPoolHighConcurrencyPinUnpinStress` —
a 64-default / 1 000-gate-level Pin/Unpin/MarkDirty stress that hammers
the cache-hit fast path and the eviction slow path.

What that stress test does **not** exercise is `Pool.PinNew`, the heap-
extension code path that drives `Manager.Extend` from inside the
buffer pool. This is the path pgbench tpcb (`-c 100 -N`, the milestone's
headline livelock workload) hits on every transaction when it appends
a row to `pgbench_history`. The loop-2 test creates a fixed 256-block
working set up front and never extends, so the
`claimVictim → evictVictim → InitPage → Extend → bm.Insert → state.Store`
sequence — which races concurrent lock-free `Lookup` for the just-
extended block — is uncovered under sustained concurrent pressure.

Without dedicated coverage for `PinNew`, a race in the extension path
would surface only at pgbench validation time (a long, expensive
discovery loop). Loop 3 closes that coverage gap.

## What the new test does

`TestPoolPinNewVsPinStress`
(`internal/storage/bufpool_stress_test.go`) spins up:

- 4 writer goroutines that loop `PinNew(rel) → MarkDirty → Unpin` and
  publish the just-extended block number to an atomic `highestBlock`
  counter so readers know the valid block range.
- N reader goroutines (default 32; gate-level via
  `GOOPG_BUFPOOL_STRESS_GOROUTINES`) that loop
  `Pin(rel, rand_blk) → maybe MarkDirty → Unpin` against
  `[0, highestBlock)`.

Pool sized at 32 slots vs an unbounded growing working set ensures
every PinNew is forced through `claimVictim` + `evictVictim` (often
flushing a dirty victim back to disk), and every reader Pin is forced
to compete with concurrent extensions for those same victim slots.

The test passes clean under `-race` at both the default scale and at
500 readers × 4 writers × 10 s
(`GOOPG_BUFPOOL_STRESS_GOROUTINES=500 GOOPG_BUFPOOL_STRESS_SECONDS=10`).
That run logs `pinNewOK=347 pinNewErr=2182 pinOK=22318 pinErr=273458` —
the `pinErr` count reflects expected `ErrNoBuffer` returns when
`claimVictim` finds every slot pinned / IO-inflight under heavy
oversubscription, not a livelock or torn-read.

## Why this is a useful regression gate

The `PinNew` path has three interactions the loop-2 test cannot
provoke:

1. **`bm.Insert` of a fresh tag concurrent with `Lookup` for the same
   tag.** Pin's pinLoad does this too, but only after a disk read.
   PinNew does it after a synchronous `Manager.Extend` (no
   read-from-disk delay) so the publish→observe window is far tighter,
   stressing the seqlock snapshot protocol that loop 2 introduced.
2. **`claimVictim` returning a freshly-extended slot.** When PinNew
   immediately Unpins, its slot is the freshest second-chance victim
   on the clock and may be reclaimed within microseconds, exercising
   the gen-bump → state.Store(0) eviction → bm.Delete sequence under
   the maximum concurrent pressure on the smallest valid set.
3. **`s.contentMu.Lock()` during `Extend`.** PinNew releases `pinMu`
   while holding `contentMu`. The loop-1 fix moved `fpiSinceCheckpoint`
   off `contentMu`, but the FPI flag is still atomic across this
   contentMu-held region. Any future refactor of contentMu (e.g.,
   M0107-0007) needs a regression signal that the existing pattern
   stays race-free.

## Scope of this change

| File | Change |
|---|---|
| `internal/storage/bufpool_stress_test.go` | new test `TestPoolPinNewVsPinStress`; reuses the existing `envInt` helper and stress-tunable env vars |

No production-code changes. No on-disk or wire-format bytes affected.

## Verification

- `go test -race -count=1 ./internal/storage/` — PASS (5.4 s)
- `go test -race -count=1 ./internal/mvcc/ ./internal/wal/
  ./internal/access/btree/` — PASS
- `GOOPG_BUFPOOL_STRESS_GOROUTINES=500 GOOPG_BUFPOOL_STRESS_SECONDS=10
  go test -race -run TestPoolPinNewVsPinStress ./internal/storage/` — PASS
- `GOOPG_BUFPOOL_STRESS_GOROUTINES=2000 GOOPG_BUFPOOL_STRESS_SECONDS=20
  go test -race -run TestPoolHighConcurrencyPinUnpinStress
  ./internal/storage/` — PASS (regression of loop-2 gate)
- `make ralph-state-guard` — PASS

## What this **does not** cover (still M0107-0006 open)

- pgbench `c=100 SU` TPS ≥ 500 (was SKIPPED/DEADLOCK in the M0098/M0099 era)
- `runtime.futex` cum% at c=100 SO < 8 % (vs 23 %)
- `bufferPartition.mu` confirmed absent from mutex top-20 via a real
  mutex-profile run on pgbench
- `TestE2E_FailoverGoopgToPG/async` PASS

These remain open for subsequent M0107-0006 loops or the milestone-close
performance suite (`analysis/perf-optimize/scripts/run_perf_suite.sh`).

## Cross-references

- [[perf-optimize/06-bufpool-lockfree]] — full Phase D3 design
- [[0107-0006-bufpool-bufmap-correctness]] — loop-1 correctness fixes
- [[0107-0006-bufmap-keys-atomic]] — loop-2 atomic-keys rewrite
