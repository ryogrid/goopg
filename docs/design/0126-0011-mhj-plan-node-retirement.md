# 0126-0011 — retire `MultiHashJoin` as a plan node (default off, code retained)

| field | value |
| --- | --- |
| status | superseded |
| superseded by | [leftdeep-joins/](../leftdeep-joins/) — MHJ retired (M0127) |
| date | 2026-07-31 |
| task | M0126-0011 — unconditional (sequencing-gated; see §5) |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` **09** Stage 4, **06** §4-§6, **10** KS3, **08** R12/R13/R17 — read them first; this doc does not restate them |
| depends on | `0126-0005` (green), `0126-0007` (green-and-enabled **or** explicitly declined), `0126-0010` if entered |

## 1. Scope

Flip `mhjPackingEnabled`'s default to `false` (`internal/planner/bushy.go:580`)
while **keeping** `rewriteMultiWayChain`, the `MultiHashJoin` node and
`internal/executor/multi_hash_join.go` in the tree, reachable via
`SetMHJPackingEnabled`, for **at least one full nightly cycle** — deleting code
and changing behaviour in one commit is unbisectable. Also ships
`scripts/pg-plan-shape-diff.sh` in **report mode only** (goopg emits no `Hash`
node and placeholder costs/widths — bundle 06 §6 — so a blocking gate would
block every commit).

**Why this precedes the flip (-0012):** `bushy.go:18-21` makes
`GOOPG_COST_DRIVEN_JOINORDER=1` set `mhjPackingEnabled=false` as a side effect;
landing this first keeps -0012 a single-variable A/B. See the milestone doc's
dedicated section.

## 2. The four-step snapshot procedure (bundle 06 §5; binding checklist)

1. `make plan-gate` **before** the flip — record it green (a SKIP is not a pass).
2. Flip.
3. `make plan-gate` and **hand-review every diff**: each must be exactly "one
   `Multi-Way Hash Join (N tables)` node expanded into N−1 `Hash Join` nodes
   over the same scans" — anything else is a regression riding in under expected
   noise, the exact mechanism by which silent row-count regressions land.
4. `make plan-snapshot-capture LABEL=post-mhj-retire`.

## 3. An open decision this task must close in writing

`generateMultiHashJoinPath` (`internal/planner/pathgen.go:100-105`): the
cost-driven path generator still *produces* MHJ paths — a separate mechanism
from the post-DP packer. Decide (and record) whether it is disabled with the
same default or retained for the KS3 revert path.

## 4. Gates

UNITS, SMOKE, SPOT, DS05, PLAN with the §2 hand review, **plus a full TPC-H SF1
sweep** compared against `docs/design/cost-model/evidence/sf1-r5-default-cb37d166.txt`
and the -0005 prediction.

## 5. Stop / decision conditions

KS3 revert triggers (bundle 10 §4): a sweep regression larger than -0005
predicted, or any plan diff not matching §2 step 3's pattern.

**This task is UNCONDITIONAL** (revised after adversarial review, finding 1):
-0012 structurally requires it (single-variable A/B), so a skip would deadlock
the terminal task. The bundle's Stage 4 preconditions are re-scoped here:
"-0005 green" and "-0007 green-or-declined-or-skipped" are *sequencing* gates
(they resolve on every path through the milestone), and "the packing queries no
longer regress with packing off" is demoted from a precondition to a
**reporting obligation** — if they still regress at this point, record the
per-query magnitude in the flip commit's message and in
`evidence/stage3-order-ab.txt`'s successor file; that record feeds -0012's bar
verdict rather than blocking this task.

## 6. Rollback

Flip `mhjPackingEnabled` back (KS3) — behaviour restores exactly because the
node and operator were not deleted; snapshots revert too (bundle 10 §1).

## 7. What this doc deliberately does not decide

The actual deletion commit (~20 files, bundle 08 R17) — explicitly out of this
milestone's scope, gated on a clean nightly cycle after this task.
