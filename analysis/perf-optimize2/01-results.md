# 01 — Results (run 20260712_114859, 2026-07-12)

## 1. Headline: c=50 simple-update, 180 s, conditions identical to perf-optimize

| | goopg (HEAD 10746d73) | PostgreSQL 18.3 | gap |
|---|---:|---:|---:|
| TPS | **1,268.8** | **15,556.3** | **12.3×** |
| avg latency | 39.41 ms | 3.21 ms | 12.3× |
| WAL flush calls (fdatasync barrier) | 25,784 / 180 s = **143.2/s** | n/a (see wait events) | |
| implied group-commit batch | ≈ 8.9 txns/flush | | |

Trend against earlier measurements of the same pattern:

| date | goopg TPS | PG TPS | gap | notes |
|---|---:|---:|---:|---|
| 2026-05-18 (perf-optimize) | 347.3 | 10,166.5 | 29.3× | pre-group-commit-tuning era |
| 2026-05-21 (m0107_su_v3, 90 s, undocumented) | ~1,428 | — | — | fix-experiment artifacts only |
| **2026-07-12 (this run)** | **1,268.8** | **15,556.3** | **12.3×** | profiling rate=1 included |
| 2026-07-12, all profiling disabled (aux A5, 60 s) | 1,754.2 | — | **≈ 8.9×** | the *uninstrumented* gap (caveat: 60 s unprofiled goopg aux ÷ 180 s PG headline, on a more-bloated table — both handicaps fall on goopg, so 8.9× is conservative) |

goopg improved 3.7× since May (WAL group commit + commit_delay batching, mvcc
mutex decomposition, GC hot-path fix all landed in between). PG also measured
~1.5× faster than in May — we attribute this to environment state (the May
run coexisted with heavier background activity; not independently verified),
which is exactly why both sides were re-measured under one noise policy
instead of comparing across eras.

Variance caveat: **n = 1 run per side** (matching the prior study's
protocol); all ratios in this document are point estimates. PG's per-10 s
progress TPS was tight (15.6–16.0 k after an 11.5 k first interval); goopg's
oscillated **837–1,734 TPS** across intervals (first interval 949 —
partly the cold buffer pool, see methodology deviation 7; raw data in
`pgbench_goopg_c50_simple-update.txt`).

## 2. Attribution (aux) runs — 60 s each, directional

| run | TPS | Δ vs headline | reading |
|---|---:|---:|---|
| A1 goopg `-M prepared` | 1,165.6 | −8 % | **parse/plan is NOT the write-path bottleneck.** (Caveat: goopg's extended protocol auto-commits per statement, so `-M prepared` also changes commit grouping — its flush count rose to 16,091/60 s.) |
| A2 goopg `GOGC=400` | 1,815.8 | **+43 %** | GC pacing costs a large TPS fraction even though GC frames are <7 % of the CPU profile — the profile is dominated by an even larger overhead (see 02); reduced GC also reduces runtime-lock traffic. |
| A3 goopg `synchronous_commit=off` | 2,867.0 | +126 % | WAL flush waiting costs ~2.3× at c=50 … |
| A3 PG `synchronous_commit=off` | 46,116.1 | +196 % | … but PG gains *more*; the async-commit gap is **16.1×**. goopg is not fsync-bound *relative to PG*. |
| A4 goopg `-c 1` | 174.9 | | per-txn service gap at c=1 is **2.9×** (PG 511.1). goopg c=1 is fsync-per-commit-bound: 10,542 flushes ≈ 1/txn. |
| A4 PG `-c 1` | 511.1 | | scaling 1→50 clients: goopg ×7.3 vs PG ×30.4 — goopg loses an extra **~4.2×** to scalability collapse. (Caveat: the ×7.3/×30.4 ratios divide 180 s profiled headlines by 60 s aux runs — directional.) |
| A5 goopg all profiling off | 1,754.2 | **+38 %** | combined profiling instrumentation — mutex/block rate=1 **plus** the concurrent CPU-profile/trace/heap collection that ran only during the headline — costs ~28 % of headline TPS (kept in the headline for parity with the prior run). 60 s-vs-180 s caveat as above. |

## 3. pgbench -i (initial data load): a 34× gap — NEW finding, user-requested

`pgbench -i -s 100` phase breakdown (from `init_goopg.txt` / `init_pg.txt`):

| phase | goopg | PG 18.3 | ratio |
|---|---:|---:|---:|
| client-side generate (COPY, 10 M rows) | **529.4 s** (≈ 18.9 k rows/s) | **9.4 s** (≈ 1.06 M rows/s) | **56×** |
| vacuum | 2.4 s | 0.53 s | 4.5× |
| primary keys (btree build) | 7.5 s | 2.9 s | 2.6× |
| **total** | **539.4 s** | **15.8 s** | **34×** |

The goopg COPY progress log additionally shows periodic 4–8 s stalls roughly
every 400 k rows. A dedicated diagnostic (`copydiag/`, `pgbench -i -s 20`
with a mid-COPY CPU profile) attributes the gap — see
`02-bottleneck-analysis.md` §7 and the fix design
`04-improvement-designs/fix-04-copy-multi-insert.md`.

Note this regressed relative to May 2026 (47 s vs 29 s then): the COPY path
inherited the same per-append overhead that now dominates the OLTP write path
(§2 of the bottleneck analysis), so the two findings share a root cause.

## 4. PG-side wait-event distribution (symmetric "where does time go")

Sampled every 250 ms across the 180 s PG headline run (50 client backends;
~34.5 k samples):

| wait event | share |
|---|---:|
| LWLock:WALWrite | **77.8 %** |
| CPU (no wait event) | 10.1 % |
| Client:ClientRead | 8.8 % |
| IO:WalSync | 1.9 % |
| IO:DataFileRead | 1.0 % |
| LWLock:WALInsert | 0.2 % |
| others (Lock:extend, IPC:ProcarrayGroupUpdate, …) | <0.2 % |

Reading: **PostgreSQL at 15.5 k TPS is itself WAL-flush-serialized** — 77.8 %
of backend time is spent queued on WALWriteLock while a leader flushes.
That is the intended group-commit design operating at its ceiling. goopg's
group commit reproduces the same *shape* (block profile: 73 % of block time
under `Context.CommitTransaction` waiting on the flush-done channel); the
12× difference is not the *architecture* of the commit protocol but the
**CPU cost per transaction around it** (see 02).

`pg_stat_wal` delta over the run: 11.39 M records, 711 MB WAL
(≈ 254 B/txn, ≈ 4.1 records/txn at 2.80 M txns).

## 5. Correctness note

pgbench validates row effects per transaction; all runs completed with
`0 failed` transactions. No result-set checking beyond pgbench's own is
implied. TPC-H Q12/Q13 spot-check gates are not applicable (no goopg code
was changed by this analysis).
