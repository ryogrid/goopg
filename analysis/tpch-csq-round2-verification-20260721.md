# Correlated-Subquery Planning Round 2 (S4 / S5a / D6.3 / S7) — Verification Report

**Date:** 2026-07-21
**Branch:** `planner-kaizen2` (base `a8233ac4`, on top of round 1's `planner-kaizen`)
**Design:** [`docs/design/correlated-subquery-planning/`](../docs/design/correlated-subquery-planning/README.md)
**Stage tracking:** [`IMPLEMENTATION-TODO.md`](../docs/design/correlated-subquery-planning/IMPLEMENTATION-TODO.md) (Round 2 part)
**Round-1 report:** [`tpch-csq-s0s3-verification-20260721.md`](tpch-csq-s0s3-verification-20260721.md)

Round 2 executed the remaining bundle roadmap on user direction: **S4**
(decorrelation coverage extensions), **S5a** (sublink pull-up before
join-order search; **S5b deferred by user decision** with a recorded reopen
criterion), **full D6.3** (cost-model touchpoints, including the
previously-deferred INNER Filter-inner NLI unwrap), **S7** (Memoize), plus
the **measurement-harness throttle-trap guard**. Seven implementation
commits, each with the round-1 gate discipline.

## 1. Commit ledger

| commit | stage | content |
|---|---|---|
| `3a620f2b` | R2-0 | throttle-trap guard in the cgroup wrapper (refuses `memory.high < GOMEMLIMIT`); NLI residual `Predicate` finally visible in EXPLAIN |
| `55ad7fdb` | R2-1 (S4a) | residual lifting generalized to IN/scalar; NL semi/anti executor modes; aggregate-above-join scalar rewrite with ordinality multiplicity tag; tautology strip; matrix → M19 |
| `712827cc` | R2-2 (S4b) | nested-sublink tolerance: deep Level-tracking walkers, escape check, sublink deep-copy (F7 aliasing trap killed); **pre-existing Stage-8 lowering blind spot fixed** (operand-hidden refs dangled, live XX000); matrix → M22 |
| `b35714b7` | R2-3 (D6.3a) | `estimateSubplanCostPerCall`; stats-aware NLI semi/anti cost gate — **with a live-fire correction**: the "no stats → reject" rule was falsified on the fresh bench server (ANALYZE stats are in-memory/restart-lost ⇒ no-stats is the common case ⇒ the rule disabled semi/anti NLI in practice, reproducing the 71× Q4 class); inverted to optimistic-accept without stats |
| `a607cf2b` | R2-4 (S5a) | sublink pull-up before `tryBushyDP` for EXISTS/IN-family WHEREs; semi/anti pinned above the DP-searched subtree; F8 re-resolution on layout change; `GOOPG_UNNEST_PREDP=off` rollback; S5b deferral ledgered |
| `8d453246` | R2-6 (D6.3b) | INNER Filter-inner NLI unwrap re-enabled behind a cost gate with an ×8 LIKE/regex/function residual surcharge (the Q9-killer class); no-stats → decline (asymmetry with the semi/anti default argued side-by-side in code); Q9 verified 90.5 s |
| `894485ba` | R2-7 (S7) | Memoize: plan node + `memoizeOp` behind a new `nliInner` protocol; kvcache budget; complete-entries-only; `enable_memoize` GUC un-no-op'd via the OnChange bridge; PG-style EXPLAIN/ANALYZE counters |

## 2. Measurements (SF1, guarded capped server, 600 s budget)

Before = round-1 close-out sweep (`sf1-final-6e8da3c0.txt`). After = this
round's close-out sweep (`sf1-r2-final-894485ba.txt`, archived in the bundle
evidence directory).

| Q | round 1 | round 2 | note |
|---|---:|---:|---|
| Q1 | 30.50 s | 29.51 s | |
| Q2 | 2.59 s | 2.52 s | scalar decorrelation (unchanged path) |
| Q3 | 23.25 s | 22.67 s | |
| Q4 | 3.45 s | 3.42 s | NLI semi (gate-protected) |
| Q5 | 425.72 s | 416.46 s | first-ever completion was round 1 |
| Q6 | 17.38 s | 16.54 s | |
| Q7 | 151.46 s | 150.77 s | |
| Q8 | 3.46 s | 3.29 s | |
| Q9 | 103.64 s | 100.08 s | D6.3b tripwire (INNER unwrap declines) |
| Q10 | 26.81 s | 24.60 s | |
| Q11 | 2.78 s | 2.59 s | |
| Q12 | 30.05 s | 27.59 s | canonical tripwire (2 rows) |
| Q13 | 96.73 s | 95.00 s | canonical tripwire (33 rows) |
| Q14 | 65.99 s | 47.29 s | |
| Q15 (view/body/main) | 0.04 / 31.77 / 64.54 s | 0.02 / 17.90 / 33.59 s | |
| Q16 | 11.26 s | 6.27 s | |
| Q17 | 90.51 s | 47.77 s | probe-cheap SubPlan (unchanged path) |
| Q18 | 67.29 s | 36.79 s | |
| Q19 | 100.96 s | 52.00 s | |
| Q20 | 4.00 s | 2.02 s | |
| Q21 | 50.29 s | 27.75 s | |
| Q22 | 1.52 s | 0.75 s | NLI anti |

**All 22 queries complete, zero errors, and every row count is byte-identical
to the round-1 sweep** (the no-regression bar this round was gated on).
Stream total ≈ **1 167 s** vs round 1's ≈ 1 406 s — a 17 % improvement that
should be read honestly: the plans are identical (every stage's plan-gate was
MATCH), so the delta is warm-cache and run-to-run variance on the shared
queries, not an optimization claim. The load-bearing numbers are the
tripwires: Q4 3.42 s (NLI semi preserved through two new cost gates), Q9
100.08 s (the INNER-unwrap re-enable declined exactly as designed), Q12/Q13
canonical row counts, Q21 27.75 s / 370 rows, Q22 0.75 s.

**Why round 2 is a plan-stability round on TPC-H.** Every stage predicted —
and plan-gate confirmed per stage — **zero TPC-H plan changes**: the new
shapes S4 unlocks (zero-equijoin sublinks, scalar residuals, nested
sublinks) do not occur in TPC-H; S5a's reorder converges to identical trees
on a stats-less server; D6.3's gates preserve every round-1 NLI decision;
Memoize requires stats that a fresh server does not have (PG likewise
memoizes zero of the 22 queries at SF1). The round's value on this benchmark
is **defense** (cost gates that prevent the two measured 71×-class traps
from ever re-firing) and **coverage** (semantics beyond TPC-H's shapes);
the timing table above is a no-regression check, not a win claim.

## 3. Semantics coverage growth (the real win surface)

The dual-path semantics matrix grew M1–M13 → **M1–M22** this round; all
green on both plan paths at every stage:

- M14: zero-equijoin EXISTS → NL semi (new executor capability)
- M15: escaping nested-sublink ref stays SubPlan (the wrong-scope aliasing
  guard — pinned so the silent-grandparent-resolution bug class cannot land)
- M16/M17/M19: scalar residual lifting via aggregate-above-join, duplicate
  outer-row multiplicity preserved by an ordinality tag, whitelist
  containment (21000 parity)
- M18: correlated NOT IN + residual keeps three-valued NULL semantics
- M20/M21/M22: nested sublinks riding into semi/anti build sides; the
  operand-hidden-ref blind spot (found and fixed live in R2-2)

## 4. Bugs found and fixed during the round

| # | bug | found by |
|---|---|---|
| 1 | Stage-8 lowering never descended into a lowerable sublink's operand — an `OuterColumnRef` hidden in a nested IN's operand dangled at runtime (XX000, reproduced live; pre-existing) | new matrix row M21 (R2-2) |
| 2 | D6.3a's first-cut "no stats → reject" rule silently disabled ALL semi/anti NLI on fresh servers (stats are in-memory/restart-lost), flipping Q4 back to the 276 s hash-semi shape | plan-gate tripwire (R2-3), root-caused with an env-gated debug print before commit |
| 3 | live F7 aliasing trap: cloned sublink nodes shared their `.Plan` with the original — any later in-place pass mutated both | R2-2's unnest-then-mutate regression test |

Items 2 is the round's headline lesson: **on goopg, "conservative without
statistics" and "safe" are not synonyms** — statistics are usually absent
(in-memory, restart-lost), so a no-stats default IS the production default.
The two cost gates now document their opposite no-stats choices side by
side (semi/anti: optimistic-accept, because rejecting disables a 71×-upside
shape; INNER unwrap: decline, because accepting risks the DNF direction and
declining merely keeps the healthy status quo).

## 5. Harness guard (the throttle trap, closed)

`scripts/goopg-test-run.sh` now refuses (`exit 2`) any invocation whose
cgroup `memory.high` sits below `GOMEMLIMIT`, with unit normalization across
the systemd (`20G`) and Go (`18GiB`) dialects. With `GOGC=off` (the TPC-H
bench tuning) that configuration parks the scope permanently in the kernel
reclaim band after one big query — measured in round 1 as CREATE VIEW at
420 s vs 0.04 s and two full sweep tails collapsing in a way that mimicked a
code regression. Every capped launch path funnels through the wrapper, so
one guard covers them all; misconfiguration is now impossible to run, not
merely inadvisable.

## 6. Left open (tracked)

- **S5b** (DP participation for pinned semi/anti joins) — deferred by user
  decision; ledger row carries the reopen criterion (a query measurably
  slower than PG that differs only by semi/anti placement). Scalar-family
  WHEREs also still take the legacy pipeline order (widening `predp`
  eligibility is the smaller first step).
- Memoize: `Memory Usage:` ANALYZE line and an EstEntries-informed budget
  floor (ledgered with resume points).
- Earlier rounds' still-open rows: derived-table-under-cross-NL zero-rows
  bug; hashed-probe family limits; DML-sublink lowering; LEFT+residual NLI
  hazard audit; composite-equijoin EXISTS decorrelation.
- `GOOPG_NLI_COSTGATE=legacy` retained (documented escape hatch; its test
  hook is exercised by the gate suite).

## 7. Reproduction

```bash
scripts/csq-bench-server.sh start        # guarded + capped (wrapper defaults)
go build -o tmp/tpch-csq-runner ./cmd/tpch-runner
tmp/tpch-csq-runner --host 127.0.0.1 --port 65433 \
  --db postgres --user postgres --password postgres --per-query-timeout=600s

# round-2 rollback switches
GOOPG_UNNEST_PREDP=off       # S5a pipeline reorder
GOOPG_NLI_COSTGATE=legacy    # D6.3a stats-aware semi/anti gate
GOOPG_MEMOIZE=off            # S7 Memoize insertion
GOOPG_INDEXKEY_HARVEST=off   # (round 1) decorrelation harvest
GOOPG_SUBPLAN_RESCAN=off     # (round 1) rescan engine
GOOPG_HASHED_SUBPLAN=off     # (round 1) hashed IN probe

scripts/csq-bench-server.sh stop
```
