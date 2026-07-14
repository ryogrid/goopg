# 07-01: Scale-500 results — headline, per-statement, mechanisms

run: `scale500b_2159d329` (goopg `2159d329`, binary sha256 `99b3bc15…`).
Conditions identical to `00-methodology.md` except `-s 500` (c=50/j=50,
T=120 s, fresh restart per workload, both engines uncapped, sequential).
0 of 50 clients aborted, 0 failed transactions in all four workloads —
this validates the `2159d329` eviction-flush fix, since the first attempt
(`runs/scale500_dad9f39e/`, same conditions at `dad9f39e`) aborted 30/50
`-N` clients (15× the eviction-flush error the fix targets, 15× a
`btree: empty internal page` symptom that did not recur — see README for
the causality caveat on the latter).

Reference points: scale-100 post-landing = `postdash_6e3b7a37` (06),
scale-100 baseline = `20260713_004324` (goopg `e453e3f2`).

## Headline

| Workload | goopg @500 | PG @500 | gap | goopg @100 (06) | PG @100 (06) | gap @100 |
|---|---:|---:|---:|---:|---:|---:|
| `-S` read | 85,324 TPS / 0.585 ms | 160,867 TPS / 0.310 ms | **1.89×** | 89,955 / 0.555 | 182,384 / 0.273 | 2.03× |
| `-N` write | 6,630 TPS / 7.541 ms | 12,694 TPS / 3.938 ms | **1.91×** | 9,898 / 5.05 | 14,517 / 3.44 | 1.47× |

Scale-100 → 500 movement per engine: goopg `-S` −5.1 %, PG `-S` −11.8 %;
goopg `-N` −33.0 %, PG `-N` −12.6 %. (Baseline for context: goopg was
2,141 TPS `-N` / 91,783 TPS `-S` at `e453e3f2`, gaps 7.38× / 1.96× — the
landed bundles' gains persist at scale 500: `-N` is still 3.1× the baseline
TPS under a harsher regime.)

**Warm-up caveat (write path).** goopg `-N` 30 s progress ticks: 3,711 →
5,898 → 7,738 TPS, still rising at cutoff (goopg printed no 120 s tick; its
final-window rate is derivable from the totals: 795,575 − 30 × (3,711.3 +
5,897.6 + 7,738.0) = 275,168 txns in the last ~30 s ≈ **9,172 TPS**). PG:
13,477 → 12,536 → 13,116 → 11,650 (flat/declining). The 120 s average
(6,630) therefore understates goopg's steady state. Window-consistent
gaps: 60–90 s = 13,116 / 7,738 = **1.70×**; final 30 s = 11,650 / 9,172 =
**1.27×** — the gap converges through the scale-100 value (1.47×) as goopg
warms, and had not yet flattened at cutoff. The honest headline is the
pair: *average gap 1.91× (includes goopg's long cold start); the
steady-state gap is in the ~1.3–1.7× band, bracketing the scale-100
1.47×*. PG reaches its steady state within the first 30 s tick; goopg
takes >120 s to fill and re-sort a 2.5 GB pool through `pinSlow` while
absorbing first-touch FPIs (02 §eviction).

Read path shows no such ramp on either engine (goopg `-S` tick 3: 87,749;
PG steady within ticks).

## Per-statement latency (`pgbench -r`)

| statement | goopg @500 | PG @500 | ratio | goopg @100 | PG @100 | ratio @100 |
|---|---:|---:|---:|---:|---:|---:|
| `BEGIN` | 0.207 | 0.091 | 2.3× | 0.229 | 0.085 | 2.7× |
| `UPDATE pgbench_accounts` | **1.509** | 0.205 | **7.4×** | 1.022 | 0.217 | 4.7× |
| `SELECT abalance` | 0.259 | 0.204 | 1.3× | 0.265 | 0.185 | 1.4× |
| `INSERT pgbench_history` | 0.382 | 0.140 | 2.7× | 0.279 | 0.136 | 2.1× |
| **`END` (commit)** | **5.190** | 3.307 | 1.57× | 3.263 | 2.828 | 1.15× |
| total | 7.541 | 3.938 | 1.91× | 5.05 | 3.44 | 1.47× |

Excess-over-PG decomposition (7.541 − 3.938 = 3.603 ms):

| component | excess | share (@100 share) |
|---|---:|---:|
| `END` | 1.883 ms | **52 %** (27 %) |
| `UPDATE` | 1.304 ms | **36 %** (50 %) |
| `INSERT` | 0.242 ms | 7 % |
| `BEGIN` + `SELECT` | 0.171 ms | 5 % |

The commit is back on top of the excess ranking — but note PG's own `END`
grew 2.828 → 3.307 ms (+17 %): both engines pay the same new tax, goopg
pays ~4× more of it (END +1.93 ms vs PG +0.48 ms). The tax is quantified
next.

## Mechanism actuals (AUX probe, `aux2/`, 60 s c=50 `-N` on post-headline data, goopg under `strace -c`)

| metric | goopg @500 | PG @500 | goopg @100 (06) | PG @100 (06) |
|---|---:|---:|---:|---:|
| **WAL bytes/txn** | **8,331** (661,566,432 B / 79,413 txns, drain delta) | **6,266** (LSN delta 4,873,941,920 / 777,841 txns) | 5,031 | 1,964 |
| goopg/PG WAL ratio | **1.33×** | — | 2.56× | — |
| commit-path plain `fsync` | **64** / 60 s | n/a | 54 / 60 s | n/a |
| WAL `fdatasync` | 22,012 (avg 2.76 ms) | 33,820 (`pg_stat_wal_io` fsyncs delta) | 18,766 (avg 3.03 ms) | 36,164 |
| group width (txns/fsync) | ≈3.6 (probe TPS 1,325, strace-perturbed) | ≈23.0 (probe TPS 12,962) | ≈5.7 (probe 1,783) | ≈23.2 |

**The full-page-image regime is the dominant scale-500 write-path change.**
With `checkpoint_timeout=24h` *and* `max_wal_size=1024GB` (both set by the
methodology conf — either alone would not prevent WAL-triggered
checkpoints at the ~7 GB/120 s rate seen here) and a fresh start, every
page's first touch after startup emits an ~8 KB FPI, once for the whole
run. goopg has PG-parity FPI semantics here: its checkpointer publishes
the redo pointer as the FPI-epoch boundary (`internal/wal/checkpointer.go`),
so with this config the watermark is effectively per-restart on both
engines. At scale 100 (accounts heap: PG ≈ 164 k pages, goopg ≈ 54 k) the
hot set is fully re-imaged within seconds and steady-state records are
small; at scale 500 (PG ≈ 820 k pages, goopg ≈ 272 k) first touches keep
arriving for the whole run. Measured on PG's headline window
(`pg_N.walstat.before/after`): +828,252 FPIs over 1,523,393 txns =
**0.54 FPI/txn**, WAL 7,080,669,405 B / 1,523,393 = **4,648 B/txn**
(vs 1,964 at scale 100). goopg's AUX WAL/txn likewise rose 5,031 → 8,331 B.
More bytes per commit group → longer `fdatasync` cycles → `END` up on both
engines. The goopg/PG WAL-volume ratio *improved* (2.56× → 1.33×): FPIs
are the same 8 KB on both sides, so the fixed per-record overhead gap is
diluted. Treat 1.33× as directional, though — FPI density depends on
transactions-per-window, and the two aux windows differ 10× in txn count
(goopg 79,413 strace-perturbed vs PG 777,841; PG's own headline-vs-aux
spread, 4,648 vs 6,266 B/txn, shows the window effect).

As in 06, the goopg width figure is strace-perturbed and directional only;
the headline run (6,630 TPS at END 5.19 ms) implies an effective width far
above 3.6. Same-procedure era-over-era comparison is the valid use.

## Growth during `-N` (headline window, `sizes.before/after`)

| metric | goopg | PG |
|---|---:|---:|
| txns in window | 795,575 | 1,523,393 |
| `pgbench_accounts` heap | +11,436,032 B | +94,109,696 B |
| `pgbench_accounts_pkey` | **+832,487,424 B** (one ~2× doubling event) | **0 B** |
| `pgbench_history` (from 0) | 41,549,824 B (52 B/txn) | 80,125,952 B (53 B/txn) |

The pkey doubling is the known C3 residual (UPDATE probes ride `RangeScan`
without kill collection — deferral-ledger row; 06-01 discusses why a 120 s
window cannot rate this metric). New at scale 500: the doubling is *costly*,
not just wasteful — under pool pressure a 1.6 GB pkey no longer co-resides
with the 2.2 GB heap, so dead index entries directly raise the miss rate
(02 ranks the fix accordingly). PG's heap grows (+94 MB, non-HOT update
spill) where goopg's barely does (+11 MB) — goopg reuses dead-tuple space
via opportunistic pruning within the smaller heap.

## Data-size parity check

goopg data dir 4.7 GB (accounts heap 2,214,060,032 B pre-run = 44 B/row);
PG 15 GB (accounts 6,714,761,216 B = 134 B/row). Row counts verified by the
load step (50,000,000 accounts both engines, `*.load.txt`). Same scale,
~3× different byte footprint — see README regime point 2 before comparing
gap movements across engines.
