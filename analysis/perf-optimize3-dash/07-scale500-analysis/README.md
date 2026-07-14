# perf-optimize3-dash — 07: Scale-500 (buffer-exceeding) analysis

date: 2026-07-14 · goopg commit: `2159d329` (= 06 tip `dad9f39e` + the
eviction-flush retry fix below; branch `wal-system-pgnize`) · PostgreSQL 18.3
(`postgres/local_install`) · comparison points: scale-100 post-landing
(`analysis/perf-optimize3-dash/06-post-landing-analysis/`, run
`postdash_6e3b7a37`) and the scale-100 baseline
(`analysis/perf-optimize3/01-results.md`, run `20260713_004324`).

## What changed vs 06: only the scale factor

Same procedure as `analysis/perf-optimize3/00-methodology.md` (c=50/j=50,
T=120 s per workload, fresh restart per workload, both engines uncapped,
identical conf deltas incl. `shared_buffers=2560MB`, sequential engines),
with **pgbench scale factor 500** instead of 100 so the data no longer fits
in the buffer pool. Run artifacts:
`analysis/perf-optimize3/runs/scale500b_2159d329/` (headline + `aux2/` +
`profiles/`).

**Regime disclosure (read this before any number):**

1. **Buffer-pool-miss / page-cache-hit regime, not disk-bound.** The host has
   31 GB RAM; at scale 500 both data directories still fit in the OS page
   cache, so a buffer-pool miss costs a memcpy-priced `pread`, not a device
   read. This measures eviction/reload churn, pin contention and double
   buffering — not storage IO.
2. **The same scale factor exerts different pressure on the two engines.**
   goopg's `pgbench_accounts` heap is 2.2 GB (44 B/row incl. page overhead;
   whole data dir 4.7 GB) while PG's is 6.7 GB (data dir 15 GB). Against the
   same 2560 MB pool, goopg's `-S` working set (heap+pkey ≈ 3.0 GB) is ~85 %
   pool-resident while PG's (≈ 7.8 GB) is ~33 % resident. The compactness is
   a real engine property, but it means "scale 500" is a *milder* miss regime
   for goopg than for PG — gap movements below must be read with that in
   mind.
3. **The 120 s window is too short for goopg's write-path steady state.**
   goopg `-N` ramps 3,711 → 5,898 → 7,738 TPS across the three 30 s progress
   ticks (still rising at cutoff) while PG is flat-to-declining
   (13,477 → 11,650). Averages therefore understate goopg's steady state;
   01 reports both the 120 s average and the last-30 s window.

## One-page summary

| Workload | goopg @500 | PG @500 | gap @500 | gap @100 (06) |
|---|---:|---:|---:|---:|
| **read** `-S` | 85,324 TPS / 0.585 ms | 160,867 TPS / 0.310 ms | **1.89×** | 2.03× |
| **write** `-N` | 6,630 TPS / 7.54 ms | 12,694 TPS / 3.94 ms | **1.91×** (final-30 s: **1.27×**, see 01) | 1.47× |

- **Read: the gap narrows under pressure (2.03× → 1.89×).** goopg lost 5.1 %
  of its scale-100 TPS; PG lost 11.8 %. Part of this is the asymmetric
  pressure of point 2 above (PG misses far more often); the profile in 02
  shows goopg's `-S` picked up a real reload wait (14 % of block delay) while
  staying CPU-bound with an essentially unchanged CPU shape.
- **Write: the 120 s average gap widens (1.47× → 1.91×), but the window
  gaps converge toward the scale-100 value as goopg warms** (60–90 s: 1.70×;
  final 30 s: 1.27×, goopg side derived from run totals — see 01). The
  average is dragged by goopg's slow warm-up (point 3). The real regime shifts are: (a) both engines pay the
  full-page-image tax — WAL/txn rose from 1,964 → 6,266 B on PG and from
  5,031 → 8,331 B on goopg (AUX probe), so `END` grew on both (PG 2.83 → 3.31
  ms, goopg 3.26 → 5.19 ms); (b) goopg's UPDATE now also pays eviction work
  inside `Pin` (02).
- **A latent correctness bug was found and fixed by this measurement.** The
  first scale-500 run aborted 30/50 goopg `-N` clients with two error
  signatures, 15 each: (a) the eviction flush path treated the WAL writer's
  transient position-accounting lag (the C3-S3 hint-flush barrier reads the
  `WrittenLSN` frontier, which can momentarily lead `FlushUpTo`'s
  accounting — M0099 Path A) as a fatal error, and (b)
  `btree: empty internal page` on the accounts pkey. (a) is fixed in
  `2159d329` (`Pool.flushWALWithRetry` + shared
  `storage.ErrWALAccountingLag` sentinel); scale 100 never evicts, so all
  prior runs were blind to it. (b) did not recur in the fixed rerun, which
  is *consistent with* it being downstream fallout of statements dying
  mid-btree-split on error (a) — but that causal link is a hypothesis, not
  proven; if the signature ever reappears it must be treated as its own
  bug. The aborted run is preserved at `runs/scale500_dad9f39e/` as
  evidence.

## Document map

- `01-results-scale500.md` — headline, per-statement, AUX actuals, growth;
  side-by-side with scale-100 post-landing and baseline.
- `02-buffer-pressure-analysis.md` — where the waits moved under pressure,
  eviction-path costs, ranked fixes, and which 06 conclusions the new regime
  changes (very few).
