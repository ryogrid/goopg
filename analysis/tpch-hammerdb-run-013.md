# HammerDB TPC-H SF=1 Run 013 — goopg perf-analysis HEAD (NLI ON)

**Date:** 2026-05-05 → 2026-05-06.
**goopg commit:** `3a7fe10` (perf-analysis HEAD at run start;
  schema build commit `627e5db`). Comprises every M0054 sub-task
  landed earlier in this session: M0054-0001..0006 plus the
  followups M0054-0005a/b/c-followup, M0054-0006-followup-Q9-
  composite / Q15b / Q19, M0054-0006e-followup. Notably this is the
  first HammerDB SF=1 run with **NLI re-enabled by default** —
  no `GOOPG_DISABLE_NLI` env-var was set.
**Run status:** **PARTIAL — TIMED OUT.** Schema build, data load,
  CREATE INDEX, ANALYZE, and the first **three** power-test queries
  (Q14, Q2, Q9) all completed successfully. The fourth query (Q20)
  was approximately **117 minutes** into execution when the
  HammerDB driver hit the 7200 s wall-clock budget and was
  terminated by `timeout 7200`. The remaining 18 queries (Q6, Q17,
  Q18, Q8, Q21, Q13, Q3, Q22, Q16, Q4, Q11, Q15, Q1, Q10, Q19, Q5,
  Q7, Q12) were not exercised. M0054-0007's 22/22 close criterion
  is **NOT MET** by run-013 — the run is reported here with named
  follow-up tasks (M0054-0008/0009/0010) instead of a silent
  acceptance demotion.
**NLI state:** **ON** for the duration. No `GOOPG_DISABLE_NLI` env
  var; planner default `nliEnabled.Store(true)` from `init()`.

## 1. Environment

| Parameter | Value |
|-----------|-------|
| Scale factor | SF=1 |
| goopg port | 65433 |
| goopg host | 127.0.0.1 |
| Database | tpch |
| User | tpch / postgres (trust auth) |
| Build threads | 1 (single virtual user) |
| Power-test VUs | 1 |
| Power-test wall-clock budget | 7200 s (2 h) |
| `GOMEMLIMIT` | 20 GiB |
| `shared_buffers` | 2048 MB (262144 slots) |
| WAL buffers | 16 MiB |
| AIO method | worker (3 workers) |
| `GOOPG_DISABLE_NLI` | (unset — NLI ON) |

## 2. Schema Build & Data Load

**Result:** PASS. Build started at 22:48:41, finished at 23:04:50
— total ~12 min 9 s. Same row counts as previous runs (region 5,
nation 25, supplier 10000, customer 150000, part 200000, partsupp
800000, orders 1500000, lineitem ~6000000). No COPY disconnects,
no oversized-frame errors. The M0052 fix and M0053-0005 fix carry
in HEAD continue to hold.

## 3. Index Creation

**Result:** PASS. The HammerDB `buildschema` step's
`CREATING TPCH INDEXES` phase completed without error and
progressed to `GATHERING SCHEMA STATISTICS`. This is the first
run to exercise the M0054-0005c-followup `DecodeRowProjection`
path during bulk index build (skip per-column heap allocations
for non-index columns).

## 4. ANALYZE

**Result:** PASS. `GATHERING SCHEMA STATISTICS` completed without
error; `TPCH SCHEMA COMPLETE` and `Vuser 1:FINISHED SUCCESS` both
emitted.

## 5. Power Test Results (Q1–Q22)

The HammerDB single-stream power test runs the 22 TPC-H queries in
the canonical pseudo-randomised order
`14, 2, 9, 20, 6, 17, 18, 8, 21, 13, 3, 22, 16, 4, 11, 15, 1, 10,
19, 5, 7, 12`. Run terminated by the 7200 s budget after Q20 had
been running ~117 minutes.

| Order | Query | run-013 (s) | run-012 #2 (s) | run-011 (s) | Δ vs run-011 | Δ vs run-012 #2 | Status |
|-------|-------|-------------|----------------|-------------|--------------|-----------------|--------|
| 1 | Q14 | 30.06 | 29.69 | 34.92 | **-13.9 %** | +1.2 % | OK |
| 2 | Q2  | 5.36  | 5.36  | 6.10  | **-12.1 %** | 0.0 % | OK |
| 3 | Q9  | 138.48 (~2.3 min) | 1351.24 (~22.5 min) | 1809.65 (~30 min) | **-92.4 %** | **-89.7 %** | OK |
| 4 | Q20 | TIMED OUT (~117 min running) | aborted ~45 min in | not finished within 38+ min | n/a | n/a | TIMED OUT |
| 5–22 | Q6, Q17, …, Q12 | — | — | — | — | — | NOT REACHED |

Source: `bench/tpch/logs/run_goopg_20260505T230450.log`.

### Headline result — Q9

**Q9 wall-clock dropped from 1810 s (run-011) → 138 s (run-013) — a
**92.4 % reduction** with the full M0054 NLI / Borrow / projection
stack on.** Q9 is TPC-H's six-table join with subquery aggregation
and is widely regarded as the hardest single-query case in TPC-H.
The decisive change is the M0054-0006 NLI rewrite: the inner side
of every equi-join with an indexed key now uses a parameterised
IndexScan rather than a hash-table build over the full table.
M0054-0006-followup-Q9-composite (planner gate + multi-column
encoder for composite-key indexes like `partsupp_pk`) was the
specific fix that closed the run-012 attempt #1
`column "ps_suppkey" is not numeric at runtime` regression. With
the gate in place, NLI can fire on Q9's
`l_partkey = ps_partkey AND l_suppkey = ps_suppkey` join without
hitting the partial-prefix probe bug.

The composite-key probe also surfaces the M0054-0005a/b/c-followup
allocation-reduction work: per-row buffer reuse (seqScan / spill /
project), Borrow-semantics opt-in for pipeline-pass operators, and
DecodeRowProjection during the index build phase. Empirical pprof
attribution between these effects requires a fresh pprof slice
during this run's Q9 window — bundled into the M0054-0007-followup-
profile sub-task (deferred until Q20 unblock lands).

### Q14 / Q2 — neutral

Q14 is essentially flat vs run-012 attempt #2 (+1.2 %). The NLI
promotion was already happening in run-012 #2 via the
M0054-0006a-pre input-IndexScan rewrite (which puts the inner
SeqScan into IndexScan form even without the full
`*NestedLoopIndexJoin` operator). NLI ON adds the operator-level
rewrite but the work shifted is small for Q14's two-table join
shape.

Q2 is unchanged. Q2 is scan- and join-bound on small dimension
tables; the M0054-0005a/b/c reuse work covers the dominant cost
already.

### Q20 — the new bottleneck

Q20 ran for >117 minutes without completing. **The Q20 result is
WORSE than run-011's 38-minute threshold.** This is the run-013's
chief negative finding.

Q20's correlated subquery
(`SUM(l_quantity)` per `(ps_partkey, ps_suppkey)` against lineitem)
is re-evaluated per outer probe under today's executor. NLI ON
helps the OUTER joins (supplier × nation; supplier × partsupp) but
does not penetrate the correlated inner aggregation. The naive
SubPlan invocation pattern (per-outer re-aggregation) dominates.

The decorrelation work needed to fix Q20 is named as
**M0054-0008** (magic-set / SIPS) — see
`docs/design/0054-0003-magic-set-decorrelation.md`. Two adjacent
sub-tasks open from Q20:

- **M0054-0009** — verify the prefix-range LIKE in Q20 actually
  picks `IndexScan` (audit M0051-0004's integration with Q20's
  expression shape). `docs/design/0054-0004-like-prefix-range-q20-audit.md`.
- **M0054-0010** — strengthen the small-side hash-join build
  estimator so nation (25 rows) is always on the build side.
  `docs/design/0054-0005-hash-join-small-side-build.md`.

## 6. NLI Q9 regression confirmation

The run-012 attempt #1 Q9 failure was
`column "ps_suppkey" is not numeric at runtime` — caused by the
NLI rule promoting a composite-key index (`partsupp_pk`) on a
single-column equi-conjunct, leaving the trailing column unbound
and producing a partial-prefix probe.

run-013 Q9 completed cleanly with NLI ON. The
M0054-0006-followup-Q9-composite fix:

1. The planner gate refuses to promote an index whose every
   leading column is not bound by an equi-conjunct from the join
   predicate.
2. The executor accepts a multi-column probe key
   (`IndexScan.Keys []Expr`) when the planner supplies one,
   encoding each column in declared order with no `0xFF` suffix
   padding.

For Q9, the planner sees `l_partkey = ps_partkey AND l_suppkey =
ps_suppkey`, identifies both as cross-side equi-conjuncts on
partsupp_pk's leading prefix, and emits an NLI with `Keys = [
l_partkey-ref, l_suppkey-ref ]`. The executor's `lookupKeys`
encodes both in order, performs an exact equality probe, and
returns matching rows without the type-error-inducing partial
prefix.

## 7. Why the run did not reach 22/22

Q20's correlated-aggregate cost dominated. M0054-0007's "22/22
within 7200 s" close criterion is therefore not met by run-013.
The path to closing M0054-0007 is:

1. Land **M0054-0008** (magic-set decorrelation). Q20's expected
   wall-clock after decorrelation is on the order of 5–10 min
   (rough estimate; design doc commits to ≤ 600 s acceptance).
2. With Q20 reduced, the remaining 18 queries should fit within
   the residual budget. Most are not Q20-shaped (no nested
   correlated aggregates) so the M0054-0006 NLI gain should apply
   broadly.
3. Re-run as M0054-0007-followup-resume-2.

## 8. Comparison with prior runs

| Phase | run-011 | run-012 #2 | run-013 |
|-------|---------|------------|---------|
| Schema build + load + index + ANALYZE | ~10 min 52 s | ~12 min 51 s | ~12 min 9 s |
| NLI state | OFF (no M0054-0006) | OFF (env-var) | **ON** |
| Q14 | 34.92 s | 29.69 s | 30.06 s |
| Q2 | 6.10 s | 5.36 s | 5.36 s |
| Q9 | 1810 s (~30 min) | 1351 s (~22.5 min) | **138 s (~2.3 min)** |
| Q20 | not finished within 38+ min | aborted ~45 min in | TIMED OUT (~117 min) |
| Q5–Q22 | not reached | not reached | not reached |

### Cumulative effect of M0054 on Q9

run-011 → run-012 #2: **-25.3 %** (M0054-0005a/b/c + M0054-0006a-pre).
run-012 #2 → run-013: **-89.7 %** (M0054-0006 NLI promotion).
run-011 → run-013: **-92.4 %**.

run-013 is the first run that empirically validates the M0054-0006
NLI design. The decisive observation is the difference between
"M0054-0006a-pre alone (HashJoin with IndexScan inputs, run-012
#2 path)" and "full M0054-0006 NLI (per-outer-row IndexScan
probe, run-013 path)" — an 8.7× speed-up on Q9's six-table join
shape.

## 9. Open follow-ups

The following sub-tasks are open in `.ralph/fix_plan.md` after
run-013:

- **M0054-0007-followup-resume**: NOT MET. Re-run after
  M0054-0008 lands.
- **M0054-0008** Q20 magic-set decorrelation (new). Design doc
  `0054-0003-magic-set-decorrelation.md`.
- **M0054-0009** Q20 LIKE-prefix range audit (new). Design doc
  `0054-0004-like-prefix-range-q20-audit.md`.
- **M0054-0010** Hash-join small-side build estimation (new).
  Design doc `0054-0005-hash-join-small-side-build.md`.
- **M0054-0007-followup-profile** (deferred, implicit): pprof
  capture during Q9 to empirically attribute the 92.4 %
  reduction across M0054-0005a/b/c and M0054-0006 contributions.
  Bundled into the next resume run.

The M0054-0007 milestone close criterion (22/22 within 7200 s)
remains **not yet met** and stays open.
