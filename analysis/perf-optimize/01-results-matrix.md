# 01 — Results Matrix

Run ID: `20260518_115032` (2026-05-18 11:50 → 13:15 JST, ~85 min wall). All settings as documented in [`00-methodology.md`](00-methodology.md). Scale factor 100. Each measurement = 180 s steady state.

## Side-by-side TPS

| clients | workload | goopg TPS | PG TPS | ratio (PG / goopg) | goopg p_avg ms | PG p_avg ms |
|---:|---|---:|---:|---:|---:|---:|
| 10  | select-only   | 2 307.74    | 37 062.32 | **16.1×** | 4.33   | 0.27 |
| 10  | simple-update | 410.49      | 3 355.17  | **8.2×**  | 24.36  | 2.98 |
| 10  | standard      | 349.29      | 2 332.81  | **6.7×**  | 28.63  | 4.29 |
| 50  | select-only   | 5 033.85    | 62 455.12 | **12.4×** | 9.93   | 0.80 |
| 50  | simple-update | 347.26      | 10 166.49 | **29.3×** | 143.93 | 4.92 |
| 50  | standard      | 338.48      | 7 111.98  | **21.0×** | 147.66 | 7.03 |
| 100 | select-only   | 6 399.64    | 45 769.87 |  **7.2×** | 15.62  | 2.18 |
| 100 | simple-update | **SKIPPED** | 9 658.30  | —         | —      | 10.35 |
| 100 | standard      | **SKIPPED** | 6 470.47  | —         | —      | 15.45 |

Latency stddev (ms) and per-10s progress samples live in the raw files under `runs/20260518_115032/pgbench_<target>_c<C>_<wl>.txt`.

## Why `c=100` write workloads were skipped

Both `goopg_c100_simple-update` and `goopg_c100_standard` hung at 0 TPS for the full 180 s pgbench window (and would have hung indefinitely — `simple-update` was killed at `pgbench-elapsed=1530 s`; `standard` was killed at `+240 s` by the watchdog). Per the user directive (do not modify goopg in this exercise), the suite marked them `SKIPPED` and continued.

Deadlock-state snapshots captured before killing:

- `profiles/goopg_c100_simple-update.deadlock_{goroutine,mutex,heap,block,cpu}.{txt,pb.gz}`
- `profiles/goopg_c100_standard.deadlock_{goroutine,mutex,heap}.{txt,pb.gz}`

The signature is identical across both runs: **19 goroutines blocked on a single `bufferPartition.mu` at `internal/storage/bufpool.go:927` (`Pool.Pin`) for 23 min**; 82 more blocked on `Pool` `evictMu` `RWMutex.RLock`. The single partition mutex is the one whose hash slot covers `pgbench_history`'s tail page — every insert pins the same last block, so all 100 clients converge on one partition out of 128. See [`04-contention.md`](04-contention.md) §4.3 for the full call-stack analysis.

## How goopg scales with clients

| workload | c=10 → c=50 scaling | c=50 → c=100 scaling | reads as |
|---|---|---|---|
| select-only   | ×2.2 (2 307 → 5 034) | ×1.3 (5 034 → 6 400) | sub-linear; bufpool partition + evictMu RWMutex helps somewhat |
| simple-update | ×0.85 (410 → 347)    | × — (deadlock)       | **regression** — extra clients hurt |
| standard      | ×0.97 (349 → 338)    | × — (deadlock)       | **regression** — extra clients hurt |

PG's same scaling:

| workload | c=10 → c=50 | c=50 → c=100 |
|---|---|---|
| select-only   | ×1.68 (37 062 → 62 455) | ×0.73 (62 455 → 45 770) — PG also feels c=100 oversubscription on 16 logical cores |
| simple-update | ×3.03 (3 355 → 10 166) | ×0.95 (10 166 → 9 658) |
| standard      | ×3.05 (2 332 → 7 112)  | ×0.91 (7 112 → 6 470) |

The asymmetry — PG's write workloads scale ×3 from c=10→c=50 while goopg's regress — is the headline finding driving §04.

## Latency progression

Average latency (ms) by client count and workload, both systems:

```
                  select-only        simple-update         standard
clients     goopg /   PG    Δ×    goopg /   PG    Δ×    goopg /   PG    Δ×
   10        4.33 / 0.27 = 16.1   24.36 /  2.98 =  8.2   28.63 /  4.29 =  6.7
   50        9.93 / 0.80 = 12.4  143.93 /  4.92 = 29.3  147.66 /  7.03 = 21.0
  100       15.62 / 2.18 =  7.2     hang / 10.35           hang / 15.45
```

The goopg write-workload latency **explodes** from 24 ms (c=10) to 144 ms (c=50) — a 6× latency spike for 5× clients — which is the textbook signature of a single point of serialisation in the commit path (see §04 for `mvcc.Manager.Commit` confirmation).

## Sanity check vs `initial connection time`

`pgbench -i` and the `initial connection time` field provide a baseline for per-connection setup cost:

| target | scale-100 init (s) | c=10 conn (ms) | c=100 conn (ms) |
|---|---:|---:|---:|
| goopg | 47 (`init_goopg.txt`) | 1.5–3   | 30–90 |
| PG    | 29 (`init_pg.txt`)    | 2.0–4   | 30–60 |

goopg's `pgbench -i -s 100` is ~1.6× slower than PG; conn-time is comparable. Init cost is bulk INSERT + index build, which is heavy on the same write-path serialisation that bottlenecks pgbench `standard`.

## Sample size and confidence

Each measurement is a single 180 s run. Latency stddev is reasonable for all goopg patterns except c=50 writes (stddev = 47.8 ms for simple-update, 44.2 ms for standard — high relative to the 144 ms mean), indicating queue-induced latency bimodality consistent with mutex queueing. Re-runs (not done in this exercise) would tighten the picture but the ordinal conclusions are robust to ±15 % noise: every gap is at least 6.7×, and the rank-order of bottlenecks is unaffected by sampling jitter.

## Raw artefacts

```
runs/20260518_115032/
├── pgbench_goopg_c<C>_<wl>.txt    # 7 files
├── pgbench_pg_c<C>_<wl>.txt       # 9 files
├── SKIPPED_goopg_c100_simple-update.txt
├── SKIPPED_goopg_c100_standard.txt
├── init_goopg.txt / init_pg.txt
├── results_summary.tsv            # flat TSV of all rows
├── driver.log                     # full driver log
└── profiles/                      # 64 .pb.gz / .out / .txt
```

`results_summary.tsv` is the machine-readable form of the table above.
