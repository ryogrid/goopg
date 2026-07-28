# Milestone 0125 — TPC-DS timeout class & planner expression-walker extinction

**Status:** planned
**Filed:** 2026-07-28
**Reference plan:** `.ralph/fix_plan.md` (M0125 section)
**Parent audit:** `docs/design/tpcds-round2-fixes/README.md` §13 — implements §13.5 actions **2, 3, 4**
**Prerequisites:** M0124-0002 (the plan baseline every A/B diffs against) and M0124-0005
(value checksums, since two tasks here cannot be accepted on row counts). See
"The M0125-0004 scheduling conflict" below for the one case where M-NIGHTLY forces an
exception.
**Branch:** `tpcds-fix2`

## Goal

Close the three engine-side actions §13.5 names, under a constraint round 2 never had to
face: **goopg's planner sits on a measured trade-off curve, and every item here moves along
it.**

## The constraint this milestone is organised around

- `analysis/tpch-evolution-round4-parallel-query-20260722.md` **§2/§5** — enabling
  statistics fixed TPC-H **Q5 22.8×** (415.2 → 18.2 s) and regressed **Q22 128×, Q4 79×,
  Q8 53×, Q2 26×, Q12 4.4×**, taking the serial stream **1162 → 1307 s** (12 % *slower*).
- `analysis/tpch-evolution-round5-int64-hashjoin-20260724.md` **§6** — the cost-driven
  join-order planner is **4 wins / 6 regressions / 12 neutral**: it repairs Q2 18.8× and
  Q8 4.1×, and creates star-query regressions by dropping MultiHashJoin — Q5 and Q21 hang,
  Q9 times out, Q10 11.4×, Q18 4.3×, Q7 1.9×. It ships **off by default** for that reason.

> **A row-count gate cannot see this class.** Every query that completed in round-5 §6
> returned **identical rows** while running 1.9× to non-completing slower.
> `scripts/tpch-spotcheck.sh` is a Q12/Q13 row-count gate. Plan-shape commits here need a
> **timed** 22-query TPC-H power run plus a **label-pinned** plan diff.

Round-5's *absolute* seconds are not a valid baseline either: the round-5 fix bundle cut the
stream 1086 → 325 s while changing no plans and no rows, so only signs and ratios transfer.
M0124-0002's arm B is the baseline.

## A coupling that was investigated and found NOT to exist

An earlier draft of this milestone claimed `localizeExprToLeaf` was reached ungated by
`estimateBaseRelInfo` (`internal/planner/cardinality.go:145`) and was dormant only because
`baseRows` is 0 on an S-cold server — making M0125-0003 the thing that "wakes" an
unconverted walker, and M0125-0002 a hard prerequisite for it.

**That is false, and the record is kept here so it is not re-derived.** The `local`
argument comes from `locals.byBinding[i]`, and `locals` is populated **only** inside
`if shouldAttachBeforeMHJ(ctx.bindings)` (`internal/planner/bushy.go:158-169`).
`estimateBaseRelInfo` returns early on `local == nil` (`cardinality.go:142`) *before*
`baseRows` is consulted. So with the gate closed the walker is unreachable at that site
regardless of relation sizes; and when the gate **is** open, `attachRelationLocalFilters`
(`bushy.go:219`) already calls the same walker on the same predicates today.

Consequence: **M0125-0003's stages do not depend on M0125-0002.** The two tasks are
independent, and the ordering below is by cost and measurability alone.

## Required Design Docs

| Task | Content | §13.5 | Design doc |
|---|---|---|---|
| **M0125-0001** | `internal/planner/exprwalk.go` child-slot primitive + drivers + `go/ast` exhaustiveness gate + the §2.6 pins. No call site converted. | 4a | `0125-0001-exprwalk-driver-and-exhaustiveness-gate.md` |
| **M0125-0002** | Convert the seven remaining §2.4 walkers, one per commit, lowest blast radius first. | 4b | `0125-0002-walker-conversion-and-mhj-composition-risk.md` |
| **M0125-0003** | `tableRows` fallback modelled on `table_block_relation_estimate_size`, behind `GOOPG_RELSIZE_FALLBACK` defaulting **off**, staged by consumer. | 2 | `0125-0003-relsize-fallback-and-tpch-stats-tradeoff.md` |
| **M0125-0004** | Q75: push single-side quals onto inner-join **inputs**, scoped so it cannot re-open the `shouldAttachBeforeMHJ` Q8/Q21 regression. | 3 | `0125-0004-q75-join-residual-evaluation-order.md` |
| **M0125-0005** | The `GOOPG_RELSIZE_FALLBACK` default flip — its own commit, its own decision, its own evidence. | 2 (rider) | `0125-0005-relsize-fallback-default-flip.md` |

## Order

1. **M0125-0004 (Q75)** — first. Smallest, a failure this programme caused, and a **live CI
   break**: query 75 is in the nightly TPC-DS qualifying set with `Q75,100,pinned` at
   `ci/batch/tpcds-row-anchors.csv:46` and **no** `expected-failures.csv` entry.
2. **M0125-0001** — inert (no call site), hard prerequisite for 0002.
3. **M0125-0003** — all stages; flag-off throughout, so the whole task is inert. Front-loads
   §13.5's highest-value item and the finding that would most change this milestone's
   cost/benefit: if real sizes do not move the timeout class, the eight-commit walker
   programme is no longer worth its gate budget.
4. **M0125-0002** — the expensive one. Re-run 0003's C1/C2 arms afterwards if plan shape moved.
5. **M0125-0005** — last, and only on evidence.

### The M0125-0004 scheduling conflict, resolved explicitly

M-NIGHTLY preempts by its own charter and will raise Q75 as a nightly anchor failure. But
M0125-0004's own Gate requires `make plan-diff LABEL=tpcds-round2-head` — a label
`plan_snapshots/` does not contain until **M0124-0002** creates it — and its acceptance is by
value, which needs **M0124-0005**. Following both rules literally deadlocks.

Resolution, in priority order:

- If M0124-0002 and M0124-0005 have landed, M0125-0004 runs with its full gate.
- If M-NIGHTLY forces Q75 **before** them, land it against the **`r5-default`** snapshot label
  with the SF0.5 gate at row-count only, and record in the commit message that the full gate is
  **outstanding**. Re-run both the label-pinned plan-diff and the checksum acceptance once
  M0124-0002/-0005 land; the task is not `[x]` until they pass.
- Q75 must **not** land inside M0124-0001's 8–10 h sweep window: `0124-0001` D1 requires the
  sweep commit to be an ancestor of every M0125 commit, and a code change mid-sweep voids the
  sweep. Coordinate on the sweep, not on the nightly.

**Gate budget, stated because it is large.** M0125-0002 is 7–8 commits ×
{units + label-pinned plan-diff + timed 22-query TPC-H + pre-commit pgbench} ≈ 12–20 h,
across two clusters (65433, 65437). The SF0.5 sweep (~1 h) runs on the first and last commit
and on any commit whose plan-diff shows a hunk — not on all eight.

## Definition of Done

1. `internal/planner/exprwalk.go` exists; the exhaustiveness test fails when a 33rd `Expr`
   type is added **anywhere in package `planner`** without a slot entry (proven with a
   throwaway type). The gate parses the *package*, not `plan.go` alone: the `exprNode()`
   marker is unexported, which closes the set to other **packages** but not to other **files**
   in this one.
2. The seven §2.4 walkers route through the driver and carry a `default:` arm, **and
   `remapByPosMap` is re-based as the eighth commit** — it is the walker §0 names as the
   defect and it still lacks a `default:`. "The class is extinct" is not claimed regardless,
   since `walkColumnRefsImpl` and the `shiftColumnRefs` closure stay out of scope with a
   ledger row.
3. Each walker-conversion commit has an `analysis/` per-query TPC-H table and a plan-diff
   verdict, with every hunk enumerated and justified in the commit message.
4. `GOOPG_RELSIZE_FALLBACK` exists, defaults off, and a unit test asserts it does **not**
   fire when `Stats.RowCount > 0`.
5. `analysis/tpch-relsize-fallback-<date>.md` records the four-arm matrix per stage, with the
   watch list of round-4 §5's five regressed queries pre-registered before the run.
6. Q75 returns PG's row count **and** its `all_sales` CTE aggregates equal PG's per year and
   column, with `zerogroups = 1` preserved.
7. Design docs indexed; milestone index row updated; status `accepted`.

## Out of scope

- **Phase 6.2**, the greedy join-order fallback for `n > 12` (`bushy.go:93`). Not in §13.5;
  review finding B3 showed it does not fix Q64 alone — `query64.sql`'s FROM references the
  `cs_ui` CTE, so `tryBushyDP` also declines at the non-scan-leaf gate. Ledger row; reopen
  after M0125-0005.
- **Opening `shouldAttachBeforeMHJ`'s `SmallDimension` gate** (§7.3 RC-5). Ledger row from
  M0124-0003; reopen criterion is "after M0125-0002 **and** M0125-0005". Changing the
  walkers and the gate that masks them at once is the one experiment guaranteed to be
  uninterpretable.
- **`pg_class.reltuples` rendering 0.** §7.1 lists it as a consequence, but it reads
  `t.Stats.RowCount` directly (`internal/catalog/catalog.go:6946`), so a planner-side
  fallback cannot fix it and must not promise to.
- Persisting `reltuples`/`relpages` (`pq-P10` option (a)) — still the alternative to 6.1.
- **Q47's downstream windowed self-join defect, Q49's one-row gap, Q51, and §13.1 phase
  0.2's unfinished panic → `XX000` half.** None are in §13.5's seven; all get a ledger row
  from M0124-0003 so they are not orphaned by this milestone's completion.

## PostgreSQL References

- `postgres/src/backend/access/table/tableam.c` — `table_block_relation_estimate_size`
- `postgres/src/backend/access/heap/heapam_handler.c` — `heapam_estimate_rel_size`
- `postgres/src/backend/optimizer/util/plancat.c` — `estimate_rel_size` dispatch
- `postgres/src/backend/optimizer/plan/initsplan.c` — `distribute_restrictinfo_to_rels`
- `postgres/src/backend/optimizer/plan/createplan.c` — `order_qual_clauses`
- `postgres/src/backend/utils/adt/numeric.c` — `sqrt_var` (checksum float normalisation)
