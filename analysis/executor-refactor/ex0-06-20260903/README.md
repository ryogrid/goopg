# EX0-06 baseline — Q6 chain + per-query tables (E1 denominator)

```
goopg: 3b40273fa +dirty (foreign WIP; binaries built from tree per arm)
cgroup: per-arm scopes via scripts/goopg-test-run.sh (20G/24G/0)
planner-flags: scripts/planner-flags.env (29 lines, sha256:29782d8b07ba52ed…)
host-load: 16 CPU, 31 GB (per-run load in arm headers below)
```

## (a) Q6 chain, TPC-H SF=1, GOGC=100 GOMEMLIMIT=12GiB, work_mem 64MB/ecs 2GB

Serial (S-cold, parallel=off): headline **5.48 s** stabilized (5.880 /
7.295 / 6.306 warmup, then 5.480 / 5.480 / 5.490), revenue
102513054.4896 ×9 identical, 56.6B instructions/query, IPC 2.52,
alloc 1.06 GB/query (82.9% Pool.Prefetch). Full capture:
`analysis/executor-refactor/ex0-02-20260903/README.md` (this item's
conforming predecessor — regime-identical, referenced not repeated).

Parallel (S-cold, suite-default 4 workers): headline **2.25 s**
stabilized (6.08 cold, 2.24 / 2.27), values ×75 identical, 38.7B
instructions/query, decode 36.0% cum / evalFastExpr 31.7% cum, alloc
~107 MB/query (warm pool; 10× below the serial first-touch figure —
pool-fill hypothesis recorded, EX1 territory).
NOT comparable to take7 (3.792 s serial / 0.838 s parallel @GOGC=off):
different GC regime by protocol mandate (09 §6). No regression signal.

## (b) TPC-H SF=1 per-query wall (HammerDB power test, fresh server/arm)

| arm | TOTAL | geomean | Q1 | Q3 | Q5 | Q6 | Q7 | Q9 | Q13 | Q18 |
|---|---|---|---|---|---|---|---|---|---|---|
| suite-default (deg 1) | 252 s | 4.19 | 8.28 | 25.02 | 43.99 | 1.65 | 11.43 | 15.06 | 7.17 | 66.19 |
| serial (deg 0) | 284 s | 5.17 | 14.92 | 25.34 | 43.15 | 4.96 | 15.99 | 18.39 | 7.22 | 68.85 |

Full 22-query vectors: `bench/tpch/logs/run_goopg_20260903-225338.log`
(default) and `...-225842.log` (serial). TIMING ONLY — HammerDB
attests errors, not values; values pinned by plan-gate (22/22 MATCH on
the EX0-04/05 closes) + digest elsewhere.
Q4 note: 1.67/1.64 s both arms — the remembered 284 s was the stale
August corpus; HEAD measures 1.6–1.7 s three independent ways.

## (c) TPC-DS SF0.5 per-query wall (sweep, WARM-pinned, GOGC=off)

Stats regime: ANALYZEd, stats survive restart — WARM-pinned, NOT S-cold
(stamped per suite; A/B never mixes regimes).
Sweep loop patched to ms resolution (additive `(Nms)` suffix beside
`%4ss`; sweep-diff.py TIMED_RE verified parser-safe).
TOTAL = derived sum-of-query-ms; TIMEOUT entries listed separately,
never summed.

(Tables below filled at close — sweep arms in flight.)

| arm | PASS | MISMATCH | sum-ms (excl TIMEOUT) | TIMEOUTs |
|---|---|---|---|---|
| suite-default | 95 (57 ck) | 0 | 986.2 s (max Q14 103.4 s) | none (no restarts) |
| serial (PGOPTIONS max_parallel_workers_per_gather=0; verified 0 vs 4) | 95 (57 ck) | 0 | 996.5 s (max Q14 104.4 s) | none |

Serial only +1.0% over suite-default: TPC-DS SF0.5 barely parallelizes
at this scale — recorded, not a claim about larger scales.
Reports: `/tmp/opencode/ex006/ds-def/sweep-20260903-230609.txt`,
`.../ds-ser/sweep-20260903-232600.txt` (run-local; vectors transcribed
above — the committed denominator is this table).

## E1 denominator contract

Later items diff per-query deltas against (b)/(c) and witness slices
against (a)/EX0-04; suite TOTALs are the backstop with the timeout-cap
rule above. Alloc arms: (a) + EX0-04 witness corpus (regime verified
same at close: GOGC=100, 64MB/2GB).
