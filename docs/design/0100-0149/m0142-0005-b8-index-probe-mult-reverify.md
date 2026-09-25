# M0142-0005 — B8 re-measurement: is `indexProbeCostMultiplier = 2.0` still load-bearing?

Task: `.ralph/fix_plan.md` **M0142-0005** (banner item 6). Parent: none (the
scoping recon itself); this doc discharges its residual B8 question —
"`indexProbeCostMultiplier = 2.0`'s calibration (`c61781d6`, 2026-09-05)
post-dates Memoize's landing and is unverified against the current binary;
measure before touching it".

Status: **recon complete, no production change.** Root task `[!]` since
2026-09-19 — S4 lineage budget exhausted (descendants 0005b–f all closed
`Movement: none`); escalation block in `.ralph/fix_plan.md` M0142-0005
carries the owner-side corpus-rebuild follow-up. Verdict: the knob is
still load-bearing, and it now sits in a corpus-dependent tension —
removing it regresses TPC-H plan parity, keeping it suppresses TPC-DS
scan-type parity. The faithful exit is the NL-probe executor gap the knob
was created to compensate, filed as **M0142-0005b**.

## What the knob is

`indexProbeCost` (`internal/optimizer/cost_funcs.go:1104`) is the
per-outer-row rescan cost a nested-loop index probe pays
(`innerRescanTotal`). PG's constants (multiplier 1.0) price it at
`2*random_page_cost + cpu_index_tuple_cost + cpu_tuple_cost +
cpu_operator_cost`. goopg ships `indexProbeMultCalibrated = 2.0`
(`:1160`), env-overridable as `GOOPG_INDEX_PROBE_MULT`, because goopg's
NL-index probe **materialises the whole inner TID list eagerly per probe**
(the comment's "ch. 06 §5" mechanism): a PG-priced NL probe over a large
outer is ruinously slow *here* even though it is cheap in real PG.
`c61781d6` set 2.0 after measuring TPC-H SF=1: suite 138.58 s -> 100.79 s
and plan parity *improved* (match 5->6) — the plans it suppressed were
non-PG-shape NL probes.

## Measurement (2026-09-19, HEAD `94c1fb6f0` + WAL fix)

Two-arm A/B on private clones, identical data and binary, only the env
flag differs:

- TPC-DS SF0.25: `cp -a bench/tpcds/runtime_goopg/data-sf025` ->
  `tmp/m0142-0005-b8/data-sf025`, served on :5533 by
  `tmp/m0142-0005-b8/goopg` (HEAD build, sha256 `605c1d5e…`), once with
  `GOOPG_INDEX_PROBE_MULT=1` and once unset (=2); env confirmed via
  `/proc/<pid>/environ`. Plans captured by `scripts/capture-tpcds.sh`
  (EXPLAIN-only, work_mem=64MB, max_parallel_workers_per_gather=4):
  `analysis/m0142/m0142-0005-b8-tpcds-mult{1,2}.txt`.
  (Note: the capture stamp's `planner-flags` line reflects the *capture
  client's* env — it prints `unset(2)` on both arms; the server-side value
  is the authoritative one and was verified via `/proc`.)
- TPC-H SF=1: reused `tmp/goopg-spotcheck-tpch-data` (today's
  pg_basebackup clone of `:65433`), served on :5534 the same way;
  `tpch-runner -explain` over all 22:
  `analysis/m0142/m0142-0005-b8-tpch-mult{1,2}.txt`.

Diff method: shape-only compare (cost/rows/width stripped), then
`scripts/pg-plan-parity-diff.py` against `bench/tpcds/plans-pg/` for the
TPC-DS arms and `bench/tpch/plans-pg/Q*.txt` for the TPC-H arms.

## Result

**The knob still moves plans.** 20/99 TPC-DS queries change shape
(Q1/Q17/Q25/Q26/Q29/Q30/Q34/Q36/Q46/Q49/Q54/Q64/Q68/Q70/Q72/Q73/Q76/
Q77/Q81/Q86 — Q36/Q70/Q86 are the pre-existing `SKIP_QUERYGEN` ERROR
queries, unchanged verdict in both arms) and 3/22 TPC-H queries change
shape (Q9, Q10, Q14).

TPC-DS SF0.25 vs `plans-pg` (`match=2` on both arms; floor held either
way):

```
                        mult=1   mult=2
CATEGORIES-EXCL-MATCH:
  join-order              88       91
  join-method             71       72
  scan-type               51       59     <- -8 at PG constants
  parameterisation        60       59
  aggregation-strategy    45       45
  sort-strategy           69       70
  parallelism             85       86
  qual-placement          24       20     <- +4 at PG constants
  rendering               26       26
```

The scan-type improvement is real and PG-directional: at mult=1 the NL
inner probes PG chooses (e.g. Q73/Q34 `Index Scan using customer_pkey`)
survive costing, while mult=2 prices them above the bitmap-heap-scan
alternative (`Bitmap Heap Scan on customer` + `Bitmap Index Scan`),
diverging from PG's exact scan choice.

TPC-H SF=1 vs `plans-pg` — the opposite direction:

| query | mult=1 | mult=2 | PG |
|---|---|---|---|
| Q14 | Nested Loop + `Index Scan part_pk` | **Parallel Hash Join** | Hash Join |
| Q10 | NL+idx on customer arm | **Parallel Hash Join** (customer) | Hash Join |
| Q9  | NL+idx part⋈partsupp | **Parallel Hash Join** part⋈partsupp | Hash Join |

At mult=1 all three slide to NL+index probes where PG picks hash joins —
exactly the divergence class `c61781d6` calibrated the knob against.

## Verdict

`indexProbeCostMultiplier = 2.0` is **still load-bearing** — neither dead
weight nor removable:

- Keeping 2.0 costs TPC-DS SF0.25 roughly 8 scan-type blockers (and ~3
  join-order, within noise) — PG picks plain NL+index probes there and the
  multiplier prices them out of contention.
- Dropping to 1.0 (PG's constant) breaks TPC-H Q9/Q10/Q14's join-method
  parity — goopg would emit NL probes where PG hashes, and those probes
  are genuinely ruinous in goopg's executor (the reason the knob exists).

No single scalar is PG-faithful on both corpora, because the multiplier is
not modelling a costing error — it is compensating an **executor** gap
(eager TID-list materialisation per probe vs PG's per-tuple
`index_getnext_tid` + `heap_hot_search_buffer`/`heap_fetch` loop,
`postgres/src/backend/executor/nodeIndexscan.c` +
`nodeIndexonlyscan.c`, costed in `cost_index`'s per-tuple model
`costsize.c`). The faithful exit is to make the probe lazy/streamed like
PG's, then retire the knob and re-verify both corpora.

Filed: **M0142-0005b** — `Kind: impl`, the streamed NL-index-probe
executor + knob retirement, expected movement = TPC-DS `scan-type` −8 at
SF0.25 (Q26/Q30/Q34/Q46/Q68/Q72/Q73/Q76/Q81 family) with TPC-H match held.

## Artefacts

- `analysis/m0142/m0142-0005-b8-tpcds-mult1.txt`,
  `analysis/m0142/m0142-0005-b8-tpcds-mult2.txt`
- `analysis/m0142/m0142-0005-b8-tpch-mult1.txt`,
  `analysis/m0142/m0142-0005-b8-tpch-mult2.txt`
- Servers: `tmp/m0142-0005-b8/data-sf025` (:5533),
  `tmp/goopg-spotcheck-tpch-data` (:5534); binary
  `tmp/m0142-0005-b8/goopg` sha256 `605c1d5e…`, HEAD `94c1fb6f0`.
