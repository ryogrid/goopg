# M0145-0008 prerequisite: executor-capability inventory

Status: inventory produced 2026-09-23 — no member is a "generatable but
unexecutable" shape; one NEW readiness finding (Q20, filed as M0145-0027 —
FIXED the same day, knob Q20 3.80 s → 0.18 s).
Task: `.ralph/fix_plan.md` M0145-0008 (Kind: impl), owner direction
2026-09-22 (progress-report §5.6). Measurement only — no product code.

## What the owner asked for

Before `GOOPG_JOINTREE_PIPELINE` flips to default, the cutover must produce an
explicit ledger of every shape the jointree arm can ELECT that the executor
cannot RUN, so no "generatable but unexecutable" plan survives the flip. Four
members were named in advance; the inventory also has to show nothing else
belongs on the list.

Two independent channels were used, because either one alone can miss:

1. **Code classification** of the four named members — is the shape (a) never
   generated, (b) generated and executable, or (c) generated but not runnable?
2. **Corpus execution** on the knob arm — every query whose plan differs from
   the default arm is actually executed and its values compared. A shape the
   named list missed would show up here as an ERROR.

## Channel 1 — the four named members

| # | member | class | evidence | arm difference |
|---|---|---|---|---|
| 1 | Parallel Hash over a PARTIAL inner (PG `parallel_hash = true`, shared build) | **(a) never generated** | `addPartialHashJoinPath` never reads `inner.PartialPathlist` — the refusal is stated in its header (`internal/optimizer/joinpathsparallel.go:59-61`); the inner is always a complete path (`cheapestParallelSafeTotalInner`). PG's producer: `./postgres/src/backend/optimizer/path/joinpath.c:1306` (`try_partial_hashjoin_path`'s `parallel_hash` argument, fed from `innerrel->partial_pathlist` in `hash_inner_and_outer`). | none |
| 2 | Row-emitting Partial aggregate (Partial → Gather Merge → Finalize) | **(a) never generated** | the only split producer `addPartialAggSplitArm` always builds `PathGather`, never Gather Merge (`internal/optimizer/partialaggupper.go:549-556`, goopg's Partial node "emits no rows at all"); a GatherMerge input keeps the refusal (`partialaggupper.go:93-96`). Executor fails closed on a mis-wired pair (XX000, `internal/executor/operators_join_agg.go:2358-2389`). PG: `./postgres/src/backend/optimizer/plan/planner.c:7351` `create_partial_grouping_paths`. | none |
| 3 | Partial nested-loop index join under Gather | **(b) generated and executable where admitted** | `addPartialNestLoopPaths` files INNER and SEMI only (`internal/optimizer/joinpathsnli.go:463-468`, ledger `m0137-0019b-partial-nl-left-anti-still-refused`); the Gather gate admits a parameterised-probe inner for INNER only (`internal/optimizer/gatherpaths.go:640-642`); the executor's `NestedLoopIndexJoinIsPartialCapable` admits INNER/LEFT/SEMI/ANTI and is called by the executor itself (no twin to drift). Identity pinned by `TestParallelSemiNestedLoopIdentity`, `TestParallelLeftAntiNestedLoopIdentity`, `TestParallelNLIJointypeIdentity`. | none |
| 4 | `appendrelMember`-root Q17-class body (no correlated index probe) | **not a capability member** — a plan-quality/timing gap | the body is born `Filter(SeqScan)`, which the executor runs correctly; the cost is time, not correctness. `appendrelMember` is set only on the knob arm (`internal/optimizer/planner.go:5797`, `:5855`). Unverified inference (recorded, not relied on): member scopes re-enter `planSelectImpl` with `jointree=true`, so the Q17 restoring rule at `planner.go:2008` may already cover them. | knob arm only |

Members 1 and 2 are therefore **parity floors, not unexecutable plans**: the
search simply never offers the shape, so the cutover cannot elect it. They stay
with their filed homes (M0140-0007; M0141-S3–S6) exactly as the task text
asked, and the flip changes nothing about them.

One drift worth recording on member 3, not a capability gap: the three gates
disagree. The producer files SEMI but the Gather gate refuses it; the executor
would accept LEFT/ANTI but the producer never files them. The narrowest gate
(Gather, INNER-only) decides, so a filed-but-refused SEMI head can occupy
`PartialPathlist[0]` — a plan-quality risk, M0145-0010's territory.

## Channel 2 — corpus execution on the knob arm

**TPC-DS** — the M0145-0018 fire-set run (`scripts/tpcds-fireset-gate.sh`,
`tmp/fireset-m0145-0018/`, taken on the staged code of `f619cbcf4`; no
`internal/` or `cmd/` change has landed since, so it describes HEAD). The fire
set is exactly the queries whose plan differs between the arms — the only
population the flip can newly expose to the executor:

| corpus | fires (plan differs) | baseline arm | knob arm |
|---|---|---|---|
| SF0.25 | 25 | 25 PASS | 25 PASS |
| SF1 | 25 | 25 PASS | 25 PASS |

**TPC-H** — a fresh full acceptance arm under the knob, this loop:
`GOOPG_JOINTREE_PIPELINE=1 ACCEPT_BASELINE=tmp/m0145-0018-acceptance-on.txt
scripts/tpch-acceptance-arm.sh knob0008 …` (SF1, serial, digest on,
`PGSHAPED=1`). **24/24 labels value-identical** to the default-arm baseline.
(This is inventory evidence only; per G8 a knob-arm PASS discharges no value
gate.)

So no query in either corpus elects a shape the executor cannot run. The
named-member classification and the corpus agree: the inventory's (c) set is
**empty**.

## New finding: Q20 is the Q17 class, and the Q17 fix does not reach it

Timing from the same TPC-H pair (default = the M0145-0018 baseline file, knob =
this loop's run; full 22-query runs, so per-query times are comparable):

| | default | knob | ratio |
|---|---|---|---|
| total | 50.41 s | 53.65 s | 1.06x |
| **Q20** | 0.13 s | **3.80 s** | **29.2x** |
| Q4 | 0.45 s | 1.07 s | 2.4x |
| every other label | | | 0.6x–1.1x |

The 1.06x total matches the figure M0145-0008's record already carries — and
hides Q20. The timing A/B doc tracked Q20 at 27.5x originally, then as "within
0.4 s of before" after the semijoin fix, and its residual analysis attributed
the remaining gap to Q17 alone. Q20 was never attributed.

Plan capture (EXPLAIN-only, `scripts/jointree-parity-capture.sh tpch`, both
arms at `PGSHAPED=1`, parallel mode; both arms score `match=2/22`):

- **PG 18.3** keeps `Filter: (ps_availqty > (SubPlan 1))` on the partsupp
  index scan, `SubPlan 1` = `Aggregate` over an `Index Scan using
  lineitem_part_supp_fkidx` with `Index Cond: (l_partkey = ps_partkey AND
  l_suppkey = ps_suppkey)`.
- **default arm** keeps the SubPlan too (index probe on `l_partkey`, `l_suppkey`
  as filter) — same class as PG.
- **knob arm** DECORRELATES it: `Hash Join … Filter: (ps_availqty > (0.5 *
  sum))` over `HashAggregate (Group Key: l_partkey, l_suppkey)` over a full
  `Seq Scan on lineitem`. PG does not convert `EXPR_SUBLINK`
  (`./postgres/src/backend/optimizer/prep/prepjointree.c:652`
  `pull_up_sublinks_qual_recurse` converts only ANY/EXISTS sublinks, at
  `:679`/`:733`), so the knob arm is
  the unfaithful one — a fidelity defect as well as 29x.

**CORRECTED 2026-09-23 (M0145-0027, fixed):** the nesting guess below was
wrong — the real cause is the multi-conjunct WHERE; see
`m0145-0008-cutover-readiness-timing-ab.md` §"Q20 FIXED". Original text:
the outcome has the same shape as Q17's (`canUnnestSubquery`'s probe-cheap
guard reading a body that never got its index path — a hypothesis for Q20,
not yet measured), but the Q17 fix
(`planner.go:2008`, `isSimpleSingle && (jointree || oneRelSearchEnabled()) &&
planIsBareSeqScanTree`) does not reach it. The difference is where the scalar
sublink sits: in Q20 it is inside the body of an `IN` sublink that the knob
arm pulls up, so the scalar body is planned — or unnested — on a route the
fix's gate does not cover. Attribution needs instrumentation (an impl task),
so it is filed as **M0145-0027** rather than guessed here.

## Consequence for the cutover

- The executor-capability prerequisite is **met**: the (c) set is empty on
  code and on both corpora; members 1–2 are floors with filed homes, member 3
  is executable, member 4 is not a capability member.
- The flip is **not yet clean on fidelity/timing**: Q20 would move from a
  PG-shaped SubPlan to a decorrelated whole-table aggregate, 29x slower, with
  every value gate green. M0145-0027 should land before the flip.
- TPC-H floor note: both arms read `match=2/22` in this capture against the
  recorded parallel floor of 3 — inside the ±3 noise band, and identical
  between arms, so not attributable to the knob; recorded for M0145-0025's
  pinned-seed re-baseline.
