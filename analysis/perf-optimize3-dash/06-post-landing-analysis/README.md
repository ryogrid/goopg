# perf-optimize3-dash — 06: Post-landing analysis

date: 2026-07-14 · goopg commit: `6e3b7a37` (= perf work tip `5754dd04` +
nightly-log chores; branch `wal-system-pgnize`) · PostgreSQL 18.3
(`postgres/local_install`) · baseline: `analysis/perf-optimize3/01-results.md`
(run `20260713_004324` at `e453e3f2`)

## One-page summary

The three landed bundles — **perf-optimize3-dash** (native-only WAL default),
**C2** (commit-path CLOG fsync removal), **C3** (btree LP_DEAD on-access
cleanup) — were re-measured under byte-identical conditions to the baseline
(`00-methodology.md`: scale 100, c=50/j=50, T=120 s per workload, fresh
restarts, both engines uncapped, sequential).

| Workload | baseline (`e453e3f2`) | now (`6e3b7a37`) | PG 18.3 (same host, same run) | gap before → after |
|---|---:|---:|---:|---:|
| **write** `-N` | 2,141 TPS / 23.35 ms | **9,898 TPS / 5.05 ms** | 14,517 TPS / 3.44 ms | **7.38× → 1.47×** |
| **read** `-S` | 91,783 TPS / 0.544 ms | 89,955 TPS / 0.555 ms | 182,384 TPS / 0.273 ms | 1.96× → 2.03× |

- Write path: **4.6× more TPS, 4.6× lower latency**. `END` (commit) fell from
  21.20 ms to **3.26 ms** — now within 15 % of PG's 2.83 ms.
- The three attributed mechanisms all collapsed as designed:
  WAL 33.0 KB → **5.0 KB/txn**; commit-path plain `fsync` 6,734 → **54** per
  60 s; pkey growth 649 → **43.6 B/txn** in the 600 s soak (14.9×; the 120 s
  window is confounded for this metric — see 01; PG remains 0).
- The write bottleneck **moved to where PostgreSQL's own bottleneck is**: the
  WAL flush wait (59 % of block delay under `walWriteLock.acquireOrWait`,
  mirroring PG's 87 % `LWLock:WALWrite`). The remaining 1.47× decomposes as
  UPDATE-statement excess 50 % (page-reload mutex serialization) + commit
  amortization 27 % (C5) + other statements 23 %.
- Read path: unchanged by design (the bundles touched the write machinery).
  The ~2× read gap is CPU-bound and decomposes into measured buckets in 03 —
  roughly half protocol/syscall shape, half per-query allocation+GC and
  parse/plan/operator construction. Our assessment: **~1.3–1.5× of the 2× is
  attributable to Go-runtime realities at the current architecture; the rest
  is addressable** (see 03 for the itemized list).

## Document map

- `01-before-after.md` — headline + per-statement + mechanism actuals, all
  cited to run artifacts.
- `02-bottleneck-now.md` — where the wait/CPU moved; ranked next fixes.
- `03-remaining-gap-go-vs-c.md` — the ~2× question: measured cost buckets,
  inherent-Go vs architectural attribution, and what landing each remaining
  fix would buy.

Artifacts: `analysis/perf-optimize3/runs/postdash_6e3b7a37/` (headline +
`aux2/` + `profiles/`), soak cross-reference
`analysis/perf-optimize3/runs/s5c3_soak2_bcfd0ed9/`.
