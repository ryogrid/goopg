# 0126-0002 — `EstimateRows` gains its `*MultiHashJoin` arm, and the plan baseline moves

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-31 |
| task | M0126-0002 |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` **09** Stage −1a, **08** R18, README "five things" #5 — read them first; this doc does not restate them |
| depends on | `0126-0001` (R0 must already be pinned) |

## 1. Scope

`EstimateRows` (`internal/planner/cardinality.go:38+`) switches over 15 node
kinds and has **no `*MultiHashJoin` case**, so every packed MHJ estimates
**0 rows** — and because `mhjPackingEnabled` defaults to true, every ancestor of
every packed chain chooses its build side and algorithm from that zero **today,
in the default configuration**. This task adds the arm. It is both a live-defect
fix and a measurement precondition: an A/B that flips packing while this arm is
missing moves two variables at once (bundle 09 Stage −1a), so **no Stage 0
measurement (-0005) may be taken before this is landed and re-baselined**.

## 2. Files and symbols touched

| file | symbol | change |
|---|---|---|
| `internal/planner/cardinality.go:38+` | `EstimateRows` | add `case *MultiHashJoin:` — estimate as the equivalent join chain (probe rows × per-dim selectivities), consistent with the `*Join` arm's method |
| `plan_snapshots/` | — | re-capture after landing (`LABEL=m0126-mhj-card`) |

Note the interaction to check while writing the arm: `buildLeft = lRows > 0 &&
rRows > 0 && lRows < rRows` (`bushy.go:1375`) requires **both** sides > 0 — a
zero on one side silently defaults to build-on-right regardless of true sizes.

## 3. Commit split

One commit for the arm + hand-review record; one for the snapshot re-baseline.

## 4. Gates

UNITS, SMOKE, SPOT, **PLAN — diffs EXPECTED**, DS05.

Every PLAN hunk is a build-side or algorithm decision previously taken on a
zero-row estimate. **Hand-review every one and enumerate them in the commit
message, each classified improvement / regression / neutral.** Some may be
regressions — record them; do not silently accept the diff wholesale. DS05 must
stay at zero row/checksum deltas (a plan change that alters results is a
correctness bug, not a plan change).

## 5. Stop / decision conditions

Unconditional and **blocking for -0005**. Stop: if any PLAN hunk cannot be
explained as a consequence of the 0→real estimate, stop and diagnose before
landing — an unexplained diff means the arm is wrong, not that the baseline was.

## 6. Rollback

Revert the arm commit; restore the previous snapshot. Preserve the diff record
under `evidence/` either way (10 §5).

## 7. What this doc deliberately does not decide

The exact estimation formula (mirror the `*Join` arm's existing method — this is
consistency work, not cardinality research; cardinality quality belongs to
-0009/-0010).
