# perf-optimize2 — goopg vs PostgreSQL, pgbench simple-update re-analysis (2026-07-12)

Re-measurement and re-attribution of the update-heavy short-transaction gap
(`pgbench -N`, 50 clients, scale 100), under the exact conditions of the
2026-05 `analysis/perf-optimize/` study — same pgbench flags, same server
configs, both servers uncapped and on the same host. Includes the
user-requested analysis of the `pgbench -i` initial-load gap. goopg was
built from clean HEAD `10746d73`; run ID `20260712_114859`.

## Headline

| | goopg | PostgreSQL 18.3 | gap |
|---|---:|---:|---:|
| c=50 simple-update, 180 s | **1,268.8 TPS** (39.4 ms) | **15,556.3 TPS** (3.2 ms) | **12.3×** |
| same, goopg all-profiling-off (aux, 60 s; conservative) | 1,754.2 TPS | — | ≈ 8.9× |
| 2026-05 baseline (same conditions) | 347.3 | 10,166.5 | 29.3× |
| `pgbench -i -s 100` total | 539.4 s | 15.8 s | **34×** (COPY phase 56×) |

**One-paragraph conclusion.** goopg improved 3.7× since May (group commit,
mvcc-mutex decomposition, GC hot-path fix landed), and the old #1/#2
bottlenecks (`mvcc.Manager.mu`, per-query ReadMemStats) are gone. The gap
that remains is dominated by a single new finding: **57 % of all goopg CPU
is `runtime.Stack`**, called by `internal/wal.(*state).stripeNum` to derive
a goroutine ID on *every WAL append* (a PostgreSQL backend gets the same
information from the `MyProcNumber` global for free). The commit
*architecture* is now the same shape as PG's — goopg batches ≈8.9
commits/fdatasync and blocks on the flush like PG blocks on WALWriteLock
(77.7 % of PG's own wait samples) — but goopg spends ~22× more CPU per
transaction around it. The same per-append storm plus a row-at-a-time COPY
path (PG batches ~1000 tuples into one WAL record per page via
`heap_multi_insert` + COPY FREEZE) explains the 56× bulk-load gap. Startup
additionally re-reads the whole WAL once per DDL-recovery module (~14×).
Fix designs with expected-lift arithmetic are in `04-improvement-designs/`
(P0: WAL-stripe backend ID, COPY multi-insert).

## Documents

## Fixes implemented (2026-07-12)

fix-01 + fix-03(a,b,d) + fix-05 landed (commits `8f30f11d`, `fedb0eec`). Results
in [05-improvement-results.md](05-improvement-results.md):

| metric | before | after | change |
|---|---:|---:|---|
| CPU busy (c=50 -N) | 2.39 cores | 1.01 cores | **2.4× less** |
| `runtime.Stack` (`wal.stripeNum`) | 56.7 % CPU | 0.09 % | eliminated |
| startup (scale-100, 1.1 GB WAL) | 28.02 s | 6.73 s | **4.2×** |
| c=50 -N TPS, sync=on | 1,121 | 1,145 | +2 % (flush-bound) |
| c=50 -N TPS, sync=off | 2,494 | 9,820 | **3.9×** |

Key finding: fix-01 removes the 57 %-CPU `runtime.Stack` storm (confirmed) and
cuts total CPU 2.4×, but at c=50 with `synchronous_commit=on` the throughput
gate is WAL fdatasync latency, not CPU — so sync-on TPS is flat while sync-off
(CPU-bound) jumps 3.9×. This re-frames the remaining gap vs PG as the
commit-flush path (fix-02/03c), not CPU. fix-02 (single commit record) and
fix-04 (COPY multi-insert) remain deferred — both change WAL format (PG-
compatible) and need the full regress/pg_waldump/crash-recovery verification.

## Documents

| doc | content |
|---|---|
| [00-methodology.md](00-methodology.md) | exact conditions, parity with perf-optimize, provenance, incidents/deviations, noise policy |
| [01-results.md](01-results.md) | headline + trend + aux attribution runs + pgbench-i phase table + PG wait-event distribution |
| [02-bottleneck-analysis.md](02-bottleneck-analysis.md) | ranked bottlenecks with profile evidence; Q1–Q5 verdicts; COPY attribution; per-txn cost model |
| [03-postgres-mechanisms.md](03-postgres-mechanisms.md) | how PG 18.3 implements each mechanism, with `postgres/src` file/function citations |
| [04-improvement-designs/](04-improvement-designs/README.md) | seven implementable fix designs, priority-ordered with lift estimates, risks, verification plans |
| [05-improvement-results.md](05-improvement-results.md) | before/after measurement of the landed fixes (fix-01/03/05): CPU 2.4×, startup 4.2×, sync-off TPS 3.9×, and why sync-on TPS is flush-bound |
| `scripts/run_su50.sh` | the reproducible driver (byte-parity conditions + diagnostics) |
| `runs/20260712_114859/` | raw artifacts: pgbench outputs, pprof profiles, wait samples, env.txt, COPY diagnostic |

## Top findings (ranked)

1. **WAL stripe selection calls `runtime.Stack` per append — 57 % CPU**
   (`wal/writer.go:1870` → `activity.LookupCurrentGoroutine`). Fix-01, P0.
2. **COPY ingest is row-at-a-time** — one WAL record + one append handoff
   per row vs PG's one record per page batch; pgbench loads via COPY
   FREEZE. 56× load gap. Fix-04, P0.
3. **Commit pipeline shape is right, unit costs aren't**: two commit
   records per txn, a per-flush Info log, and no *pre-enqueue*
   already-flushed fast exit (the background pre-flush itself is active —
   `FlushUpTo(WrittenLSN())` every 200 ms — but a committer still pays the
   full queue handoff to discover its LSN is already durable). Fix-02/03,
   P1.
4. **GC still matters** (+43 % TPS at GOGC=400) but is second-order until
   fix-01 lands; the per-statement arena remains unengaged. Fix-06, P2.
5. **Startup re-reads the entire WAL ~20×** (one `wal.ReadAll(dir, 0)` per
   DDL-recovery module — 26 such modules): 28 s startup, 200 GB transient
   allocation. Fix-05, P2.
6. **Methodology**: the combined profiling instrumentation (mutex/block
   rate=1 plus the concurrent CPU/trace/heap collection) costs −28 % TPS
   (kept in the headline for parity with 2026-05; uninstrumented gap
   ≈ 8.9×, conservative). The May-era conclusions about `mvcc.Manager.mu`
   and per-query `ReadMemStats` are confirmed resolved. All ratios are
   n=1 point estimates (see 00 §Deviations for the full caveat list,
   including a cold-vs-warm-cache parity gap vs the prior suite).

## Review log

All three reviews ran against the drafts; every finding below was applied to
the documents (none rejected).

| date | reviewer lens | outcome |
|---|---|---|
| 2026-07-12 | technical accuracy vs goopg code + run artifacts | 4 substantive corrections applied: (1) the "inert background pre-flush / rejected `^uint64(0)` sentinel" claim was **wrong** — the code calls `FlushUpTo(WrittenLSN())` every 200 ms (`open.go:2126`); the sentinel survives only in a stale comment + stale design doc; docs and fix-03 were rewritten around the real gap (no *pre-enqueue* fast exit). (2) COPY-phase `runtime.Stack` share corrected ~25 % → **~60 %** (cum). (3) `internal/executor/arena.go` does not exist — the mechanism is `internal/mctx` (M0107-0001) and it *is* acquired per statement; fix-06 reframed to "route hot allocators through it". (4) DDL-recovery module count corrected ~14 → **26** (≈20 full WAL passes, ≈34 GB read). Plus minor line-number and labelMap-layout nits. All TPS/ratio/profile numbers verified correct. |
| 2026-07-12 | PG fidelity vs `postgres/src` (18.3) | No MAJOR findings; 2 MINOR applied: FREEZE gating conditions on pgbench COPY (v14+, non-partitioned); lock-acquisition attribution `GetSnapshotData` vs `GetSnapshotDataReuse`. All cited functions, constants, line numbers, and quoted comments verified. |
| 2026-07-12 | benchmark methodology / condition parity | 1 MAJOR applied: cold-vs-warm-cache parity gap vs the prior suite (prior c=50 simple-update ran after a select-only warmup; disclosed as deviation 7). 8 MINOR applied: profiling-overhead attribution (A5 = all instrumentation, not mutex/block alone), cross-duration ratio caveats, n=1 + interval-range disclosure, hedged May-PG-slower causal claim, sampler-load disclosure, hang-timeline consistency, copydiag reproduction steps, `pg_stat_checkpointer` column-name bug (fixed in driver). Verdict: no finding invalidates the 12.3× gap or the runtime.Stack root cause. |
