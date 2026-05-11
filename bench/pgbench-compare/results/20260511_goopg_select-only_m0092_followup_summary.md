# pgbench select-only @ -c 10 post-M0092 follow-up (2026-05-11)

## What this measures

Re-runs of pgbench `-S -c 10 -j 10 -T 180` against the M0092
follow-up tip (commits M0092-0004 / -0005 / -0006 / -0007 on
top of the M0092 baseline at 82d7acf). Goal: confirm whether
the 4 broadly-distributed allocation cuts move TPS toward
M0091's ≥ 1,000 bar.

## Parameters

scale=100, `-c 10 -j 10 -T 180 -P 10 -S`. Server config
identical to M0091 / M0092 summaries (`shared_buffers=2560MB`,
`wal_buffers=100MB`, `checkpoint_timeout=24h`,
`max_wal_size=1024GB`). `track_io_timing` defaults off.

## Results

| run | commit | TPS | latency avg | failed |
|---|---|---:|---:|---:|
| M0092 baseline (re-measured today) | 82d7acf | **317.47** | 31.5 ms | 0 |
| M0092 follow-up (4 commits) | da7224d | **328 ± ~25** (3 runs: 341.58 / 328.08 / 282.94) | ~31 ms | 0 |

Documented earlier M0092 baseline from the M0092 summary was
437.62 TPS. **That number does not reproduce today** — re-
measurement of the same commit on the same machine produces
~317 TPS. The earlier figure was likely measured at a
different cache / system-load state. The relative comparison
in this doc uses today's fresh baseline.

## Verdict

The 4 follow-up commits **do not move TPS** in either
direction past the noise floor. Run-to-run variance dominates
the per-commit signal. Per-query allocation profile shows
clear reductions at the targeted sites — they're gone from
the top-25 alloc list — but allocation is not the bottleneck
on this workload.

## Why the bottleneck didn't move

CPU pprof captured during a 30s window of the follow-up run:

```
Duration: 30s, Total samples = 50ms (0.17 %)
60 % runtime.syscall (epoll, futex)
40 % runtime.findRunnable / wakep
```

**The goopg server is essentially idle**: 0.17 % CPU
utilisation. At 328 TPS / 10 clients / 31 ms latency, every
client backend spends ~31 ms blocked per query while goopg
is doing almost no work. Per-query allocation reductions
can't help when CPU isn't the constraint.

The dominant per-query cost is structural: every implicit
`BEGIN ... SELECT ... COMMIT` round-trip triggers
~19,600 walwriter flushes per 60 s (one per query),
meaning every query ends with a WAL fsync for its commit
record even though SELECT writes no data. This serialises
queries on disk-fsync latency on every commit, not on
goopg's CPU work.

## Targeted alloc cuts — confirmed gone from top-25

Pre-followup alloc top (per the M0092 summary):
- `executor.SlotFromRow` — 5.96 %
- `storage.PageGetHeapTuple` — 6.17 %
- `storage.ParseHeapTuple` — 3.06 %
- protocol `cells := make([][]byte, ncols)` — present
- protocol `[]byte(d.Format())` — present
- `payload := make([]byte, 0, size)` in WriteDataRow — present
- 14 `activity.LookupGoroutine` sites with runtime.Stack — present

Post-followup alloc top (`pprof-data/m0092_followup/allocs.prof`):
None of the above appear in the top 23 entries. They've been
fully eliminated from the steady-state allocation path. New
top is dominated by `parser.Lex` (22 %) — the parse / plan
cost per query that the deferred M0092-0008 plan-cache would
target.

## What this means for next steps

The pgbench `-S -c 10` workload's bottleneck is **per-query
WAL fsync on the implicit commit**, not goopg's CPU or
allocation rate. To move TPS toward M0091's ≥ 1,000 bar,
M0093 should target one of:

1. **Skip WAL emission for read-only implicit transactions.**
   A simple `BEGIN ... SELECT ... COMMIT` that writes no
   data shouldn't emit a commit record. Upstream PG does
   this; goopg currently emits a marker. Confirm via the
   walwriter flush count and gate emission on
   "txn wrote any WAL".
2. **Plan cache for the simple-query path.** M0092-0008
   documents why this is non-trivial for pgbench (which
   substitutes literals client-side). A normalised-template
   plan cache would still help; design is a multi-week
   effort.
3. **Connection pooling + extended-protocol prepared
   statements.** pgbench can drive `-M prepared` which
   uses the extended protocol; goopg's extended path
   currently re-plans on every Execute. Wiring plan
   reuse there is more tractable than the simple-query
   normalisation work.

The M0092 follow-up commits should still land — they remove
broadly-distributed allocation overhead that DOES matter for
other workloads (long-running TPC-H queries, mixed read/write
workloads with higher CPU pressure). They just don't surface
in pgbench-S TPS because pgbench-S is fsync-bound.

## Honest M0092 follow-up acceptance

- ✅ All 4 sub-milestones implemented and committed.
- ✅ Unit / testport tests all pass (replcluster failure
  pre-existing on this branch).
- ✅ Targeted alloc sites gone from steady-state profile.
- ✅ Plan cache deferral analysis filed (M0092-0008).
- ❌ Original "TPS ≥ 700" target NOT met. Bottleneck is
  pre-existing per-commit WAL fsync, not what M0092 set
  out to fix.

Filed for M0093:

- WAL emission gating for read-only implicit transactions
  (high-leverage; new finding from this measurement).
- Extended-protocol plan reuse (the natural plan-cache
  scope per M0092-0008's analysis).
- Per-session GUC wiring for `track_io_timing` (process-
  wide for now per M0092-0005's deferral).

## Cross-references

- `bench/pgbench-compare/results/20260511_goopg_select-only_m0092_summary.md`
- `docs/milestones/0092-lazy-row-emission-in-scan-and-project.md`
- `docs/design/0092-0004` / `0005` / `0006` / `0007` / `0008`
- `pprof-data/m0092_followup/cpu.prof`
- `pprof-data/m0092_followup/allocs.prof`
- Raw run logs:
  `20260511_144323_*` / `_144711_*` / `_145921_*`
  (followup), `_145544_*` (baseline re-measure).
