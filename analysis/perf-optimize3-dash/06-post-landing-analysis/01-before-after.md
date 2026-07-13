# 06-01: Before / after — same conditions, mechanism by mechanism

runs: baseline `20260713_004324` (goopg `e453e3f2`) vs `postdash_6e3b7a37`
(goopg `6e3b7a37`); PG 18.3 measured in BOTH runs on the same host —
its numbers moved <10 % between runs, confirming environment stability.
Conditions per `analysis/perf-optimize3/00-methodology.md` (scale 100,
c=50/j=50, T=120 s, fresh restart per workload, both engines uncapped,
sequential engines). 0 of 50 clients aborted in any workload — itself a
regression check for the connection proc-slot fix (bcfd0ed9), since the 5 Hz
wait-event sampler alone previously wrapped the slot counter.

## Headline

| Workload | goopg before | goopg after | Δ | PG (this run) | gap before → after |
|---|---:|---:|---:|---:|---:|
| `-N` write | 2,141 TPS / 23.35 ms | **9,898 TPS / 5.05 ms** | **+362 % TPS, −78 % lat** | 14,517 TPS / 3.44 ms | 7.38× → **1.47×** |
| `-S` read | 91,783 TPS / 0.544 ms | 89,955 TPS / 0.555 ms | −2 % (noise) | 182,384 TPS / 0.273 ms | 1.96× → 2.03× |

The read path was intentionally untouched (all three bundles are
write-machinery work); its −2 % is within run-to-run variance (PG likewise
moved +1.2 % on `-S` and −8 % on `-N` between the two runs).

## Per-statement latency (`pgbench -r`, the decisive decomposition)

`-N` transaction = `BEGIN; UPDATE accounts; SELECT; INSERT history; END`:

| statement | goopg before | goopg after | PG after | ratio before → after |
|---|---:|---:|---:|---:|
| `BEGIN` | 0.251 | 0.229 | 0.085 | 3.0× → 2.7× |
| `UPDATE pgbench_accounts` | 0.797 | **1.022** | 0.217 | 5.1× → **4.7×** |
| `SELECT abalance` | 0.227 | 0.265 | 0.185 | 1.25× → 1.4× |
| `INSERT pgbench_history` | 0.899 | **0.279** | 0.136 | 6.8× → **2.1×** |
| **`END` (commit + WAL flush)** | **21.200** | **3.263** | 2.828 | 8.1× → **1.15×** |
| total | 23.35 | 5.05 | 3.44 | 7.4× → 1.47× |

Reading of the table:

1. **`END` collapsed 6.5×** (21.20 → 3.26 ms) and is now within 15.4 % of PG.
   The baseline attributed END to three serialized costs: the 8 KB-image WAL
   volume (dash), the pg_xact fsync every ~11 commits (C2), and the narrow
   commit group. The first two are gone (below); the third remains and is the
   subject of 02.
2. **`INSERT` fell 3.2×** (0.899 → 0.279 ms): the canonical-record assembly
   that the baseline profile blamed for ~75 % of `memmove`
   (`buildCanonicalSingleFPIBody` etc.) no longer runs — native-only default.
3. **`UPDATE` is the one statement that got slower in absolute terms**
   (0.797 → 1.022 ms) and is now the worst non-commit ratio (4.7×). At 4.6×
   the throughput, per-page contention rises (the new `-N` block profile puts
   17.7 % of block delay under `updateViaIndex`, most of it bottoming out in
   `Pool.Pin` waits on the per-file `readBlock` mutex — see 02), and the C3 kill-list oracle adds work at the
   visibility site. This is the second-ranked item in 02.

## Mechanism actuals (AUX probe, `aux2/`, baseline-parity procedure: 60 s c=50 `-N` on the post-headline data, goopg under `strace -c`)

| metric | baseline goopg | goopg now | PG now | design target |
|---|---:|---:|---:|---|
| **WAL bytes/txn** | 33,004 | **5,031** (537.5 MB / 106,853 txns) | 1,964 (LSN delta / 839,903 txns) | ~6.3 KB at 60 s (05 §6) — beaten |
| **commit-path plain `fsync`** | 6,734 / 60 s (avg 6.29 ms) | **54** / 60 s | n/a (PG issues none) | ~0 — met |
| WAL `fdatasync` count | 12,269 (avg 3.81 ms) | 18,766 (avg 3.03 ms) | 36,164 (`pg_stat_io` object='wal') | unchanged (the floor) |
| **group-commit width (txns/fsync)** | ≈6.1 | ≈**5.7** | ≈**23.2** | not targeted — the remaining lever |
| TPS in probe (strace-perturbed) | 1,243 | 1,783 | 13,996 | — |

Notes: goopg WAL bytes = `pg_stat_wal_io` drain-bytes delta (physical);
PG = `pg_current_wal_lsn()` delta (logical) — same meters as the baseline, so
the *ratio movement* (11.6× → 2.6×) is like-for-like even though the absolute
comparison stays soft. The probe runs on post-headline (bloated) data in both
eras; only TPS-independent ratios are read from it, per the baseline's own
caveat.

## Index growth during `-N` (headline run, `sizes.before/after`)

| metric | baseline goopg | goopg now | PG |
|---|---:|---:|---:|
| txns in run | 256,927 | 1,187,777 | 1,742,073 |
| `pgbench_accounts_pkey` growth | +166,830,080 B (one file doubling) | +166,830,080 B (one file doubling) | **0 B** |
| per-txn rate in the window | 649 B/txn | 140 B/txn | 0 |

**Caveat (review finding): the 120 s window is confounded for this metric.**
Both eras grew the pkey by the byte-identical amount — exactly one file
doubling — so the per-txn ratio (4.64×) is just the txn-count ratio by
construction; a 120 s window cannot distinguish "C3 slowed growth" from
"both runs hit one doubling". The **real C3 evidence is the 600 s soak**
(`runs/s5c3_soak2_bcfd0ed9`): still only one doubling after 3,829,254 txns
= **43.6 B/txn** (14.9× below the baseline rate), and — the property C3
exists for — **TPS-over-time flat**: 6.4k @150 s → 6.3k @450 s, where the
baseline degraded with runtime. Growth is not yet ~0 because kill-list
collection rides `indexScanOp` only; pgbench `-N`'s UPDATE probes (9
remaining `RangeScan` callers) do not collect kills, so dead entries
accumulate until the no-space purge reclaims them (deferral-ledger row).

## Cross-checks

- Consistency / regime note (review finding): the AUX width figures come
  from DIFFERENT regimes — goopg's 5.7 was measured at 1,783 TPS under
  strace (fdatasync 3.03 ms, perturbed) while PG's 23.2 ran at 13,996 TPS;
  width scales with arrival rate. The headline goopg run (9,898 TPS, END
  3.26 ms) must have operated at an effective width of ~20+ — at width 5.7
  on even a 2 ms floor it could not exceed ~3 k TPS. PG's 36,164 serialized
  fdatasyncs in 60 s also bound the true device floor at ≤1.66 ms, below
  the strace-perturbed 3.03 ms. Era-over-era goopg width (6.1 → 5.7, same
  probe conditions) is like-for-like; the goopg-vs-PG width comparison is
  directional only.
- goopg wait-event columns are still empty (23,088/23,088 samples) — the
  observability gap noted in the baseline 04 remains; block profiles remain
  the substitute (02).
