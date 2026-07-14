# 05 — Improvement results (fix-01 / fix-03 / fix-05)

Measured 2026-07-12, same host as the `20260712_114859` baseline, both binaries
built this session and run back-to-back under identical conditions (uncapped,
`GOMEMLIMIT=18GiB`, `-c 50 -j 50 -T 180 -P 10 -N`, scale 100). "before" =
clean HEAD before the fixes (`07aa31e6…`); "after" = HEAD `fedb0eec` with
fix-01 + fix-03(a,b,d) + fix-05 (`c6257e12…`).

Commits: `8f30f11d` (fix-01 + fix-03 + main.go GOMEMLIMIT log), `fedb0eec`
(fix-05).

## Headline numbers

| metric | before | after | change |
|---|---:|---:|---|
| **CPU busy** (120 s profile window) | 286.99 s ≈ **2.39 cores** | 121.24 s ≈ **1.01 cores** | **2.4× less CPU** |
| `runtime.Stack` via `wal.stripeNum` | **56.7 %** of CPU | **0.09 %** | eliminated |
| **startup** (scale-100 dir, 1.1 GB WAL) | **28.02 s** | **6.73 s** | **4.2× faster** |
| c=50 `-N` TPS, `synchronous_commit=on` (default) | 1,121.3 | 1,145.0 | +2.1 % (flat) |
| c=50 `-N` TPS, `synchronous_commit=off` | 2,494.3 | **9,820.4** | **3.9× faster** |
| avg latency (sync=on) | 44.59 ms | 43.67 ms | ~flat |

## What each fix delivered

### fix-01 — the `runtime.Stack` removal (the big CPU/efficiency win)

The profile goal is met unambiguously: `wal.stripeNum` went from **56.7 % of
all CPU** (deriving a goroutine id via `runtime.Stack` on every WAL append) to
**0.09 %**, and `runtime.Stack` no longer appears in the profile at all. Total
CPU consumed for the same workload fell from **2.39 cores to 1.01 cores** — the
storm was burning ~166 CPU-seconds per 120 s that are now gone. The after-fix
CPU profile is dominated by genuine work: `Syscall6` 21 % (WAL `pwrite`/
`fdatasync`), `memmove` 11 % (buffer copies), then GC/runtime and
`captureSnapshot`/`VacuumHeapPageBySlots` at low single digits.

### The throughput result, honestly (and why sync=on TPS is flat)

At c=50 with the default `synchronous_commit=on`, TPS barely moved (+2 %). This
is not a failure of fix-01 — it is the correct, informative result: **at c=50,
goopg's simple-update throughput is bound by WAL commit-flush latency
(fdatasync serialization), not by CPU.** goopg was using only ~2.4 of 16 cores,
so the `runtime.Stack` CPU was "free" wall-clock-wise; removing it slashed CPU
2.4× but the commits still wait on the same group-commit `fdatasync` cadence, so
throughput is unchanged. `Syscall6` becoming the new #1 cost confirms the gate
is now I/O, not CPU.

The `synchronous_commit=off` A/B is the control that proves the point: with the
fdatasync gate removed, the CPU win converts straight into throughput —
**2,494 → 9,820 TPS, a 3.9× gain**. So fix-01's value is (1) a large,
unconditional **CPU-efficiency / scaling-headroom** win at the default setting,
and (2) a **3.9× throughput** win whenever the workload is not fsync-latency-
bound (async commit, batched commits, or a faster durable device than the
WSL2 ext4 volume this was measured on).

This also **re-frames the bottleneck ranking** in `02-bottleneck-analysis.md`:
with the CPU storm gone, the remaining c=50-sync gap vs PostgreSQL (15,556 TPS)
is now almost entirely the commit-flush path — the target of fix-02 (one commit
record per txn, fewer WAL bytes/fsync), fix-03(c) (commit-wakeup batching), and
the fsync/`wal_sync_method` path — not CPU or GC.

### fix-03 — commit-pipeline safe items

(a) the per-flush `walwriter flush` Info log (which fired ~143×/s on the commit
critical path) is demoted to Debug; (b) `Writer.FlushUpTo` now takes a
pre-enqueue fast exit when the LSN is already durable (published via
`flushedLSNAtomic` after the fdatasync loop), skipping the group-flush queue —
mirroring PG's `record <= LogwrtResult.Flush`; (d) the stale `^uint64(0)`
walwriter-sentinel description was corrected in code + design doc. These reduce
per-commit overhead and latency-tail but do not change the c=50-sync throughput
gate (still fdatasync-bound). Item (c) commit-wakeup batching is deferred
("measure first"; ledger).

### fix-05 — startup recovery memoization

Startup on the scale-100 / 1.1 GB-WAL data dir dropped **28.02 s → 6.73 s
(4.2×)** by decoding the WAL once and sharing it across the ~30 catalog-recovery
passes instead of ~20 whole-WAL re-reads (the 200 GB startup-allocation
hotspot). Pure memoization — no change to recovery logic, record order, or WAL
format; the `internal/initdb` recovery-survival suite passes unchanged.

## Correctness gates (all green)

- `go build ./...` clean; `gofmt` clean.
- `go test -race ./internal/wal ./internal/gls ./internal/mvcc` PASS
  (incl. `TestConcurrentAppendAcrossSegmentBoundariesNoOverflow`, the test that
  caught the rejected procPin variant — see below).
- `go test ./internal/server ./internal/initdb` PASS; the initdb
  Recovery/SurvivesRestart/Replays suite PASS (129 s) with the fix-05 cache
  active.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2 / Q13=33) — row counts unchanged.
- pre-commit pgbench smoke PASS on both commits.

## Implementation note: mechanism chosen for fix-01

The design doc's Option A (pprof-label goroutine-local id) was used, **not** the
simpler `runtime.procPin` P-index alternative. The plan's correctness gate
required verifying `procNum` is used only for stripe selection; a first
implementation using `procPin` (P-index striping) **failed** the WAL test
`TestConcurrentAppendAcrossSegmentBoundariesNoOverflow` — the state-loop slow
path runs on the unregistered writer goroutine and its `insertPosTracker`
cross-segment pad bookkeeping assumes stripe 0, which procPin's non-zero P-index
violated. The gls approach delivers the **exact** procNum (0 for unregistered
goroutines), preserving today's behavior precisely, and is what landed. The
pprof-label read is isolated in `internal/gls` behind a version-gated build tag,
a runtime layout probe (degrades to stripe 0 on mismatch), and a canary test.

## Raw artifacts

`tmp/perf-optimize2/beforeafter-out/` (this session, not committed — large):
`pgbench_before.txt` / `pgbench_after.txt`, `before.cpu.pb.gz` /
`after.cpu.pb.gz`. Driver: `scripts/bench_goopg_su50.sh`. Startup timing:
scratch `time_startup.sh`; sync-off A/B: scratch `syncoff_ab.sh`.
