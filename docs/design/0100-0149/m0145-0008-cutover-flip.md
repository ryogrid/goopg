# M0145-0008: the cutover flip — the jointree pipeline becomes the default

Status: flip landed 2026-09-24; the legacy-deletion slices remain (task stays
open). Task: `.ralph/fix_plan.md` M0145-0008 (Kind: impl, Parent: M0145-0007).
Earlier docs: `m0145-0008-cutover-readiness-timing-ab.md`,
`m0145-0008-executor-capability-inventory.md`, `m0145-0008-flip-test-triage.md`.

## How it landed (attribution)

The flip's code (18 files: `internal/optimizer`, `internal/executor` tests,
three harness scripts) was staged and gated as one unit. Before the loop
could commit it, a concurrent session committed **without a pathspec**, and
`ddb4eabd4` "analysis(latency-trend): …" swept the whole staged index into
itself, the nightly `ci/logs` files included. That commit is the flip. The
gates below ran on exactly that staged tree (stamps in `tmp/gate-stamps/`),
so the code that landed is the code that was verified. It is already pushed;
history is not rewritten. The NULL-key prerequisite landed on its own, as
`eb0e7d388`.

## What changed

- `jointreePipelineFromEnv` (`internal/optimizer/jointreepipeline.go`):
  `v == "1"` → `v != "0"`. The jointree pipeline is the default;
  `GOOPG_JOINTREE_PIPELINE=0` selects legacy until the legacy-deletion slices
  remove it and the knob.
- Owner-sanctioned test hooks (2026-09-24), same shape as `SetGatherPathsMode`,
  each resolved through its production env resolver:
  - `optimizer.SetJointreePipeline(label)`: the knob across the package
    boundary, not a second selection mechanism.
  - `optimizer.SetIndexProbeCostMultiplier(label)`: option (c) of the
    multiplier question.
- Harness sites from the M0145-0030 §Script audit:
  - `tpcds-sf025-regression.sh`: default arm `1`; its extra EXPLAIN pass is
    now the legacy control (`=0`);
  - `tpch-estimate-audit-arm.sh`: `JOINTREE` default `1`;
  - `planner-flags.env` regenerated (`unset(on)`).
  - `tpcds-fireset-gate.sh` is **deliberately unchanged**. It compares legacy
    (0) with jointree (1) on one binary; after the flip the candidate is the
    shipped arm and the baseline is legacy, which still means something until
    legacy is deleted. The HEAD-vs-staged redesign belongs to the deletion
    slice.

## Test dispositions under the flip

The local flip left 13 tests red at HEAD. Each was classified against PG 18.3
(scratch cluster, port 5534):

| test | finding | disposition |
|---|---|---|
| `TestIndexFullScansKeepNullKeyedRows/capable=false` | **wrong results**: the index-only producer's bare branch (no local qual) skipped the NULL-key guard, so a full index-only scan dropped NULL-keyed rows in a cluster without the capability. Reachable on the **legacy** default too, through any join the search plans (2000 rows for PG's 2002) | fixed separately first, `eb0e7d388` (pathindexonly.go: `indexUnboundKeysNotNull(tbl, idx, 0)`), with a default-arm witness subtest |
| `TestJointreePipelineKnobPolarity`, `TestFlagProvenanceEnvIsGenerated` | group K: they state the default | updated / regenerated |
| `TestSAOPWithConjunctMoves` | PG 18.3: `Index Scan using idx_item_sk_flag (0.15..8.17 rows=1)`; seq 28.00. goopg priced the seq path at 1.01 because the fixture reports no block count, a state PG never plans against. With a 0-block sizer (PG's view: `estimate_rel_size`'s 10-page floor) goopg's seq path is 28 = PG, but rows were 3 against PG's 1 | two PG-faithful estimator fixes, below; the fixture gains the sizer |
| `TestIOS_HeapFallback`, `TestIndexOnlyDeformColdAndVisible` | multiplier-dependent (PG prices index vs bitmap within 0.01) | plan with PG's multiplier 1 via the hook; the deform test also installs the live block-count reader `initdb.Open` installs |
| `TestIndexScan{Varchar,Char,Timestamp}EndToEnd`, `TestIndexDeformRescanPersistsBound` | PG 18.3 plans these tiny unmeasured tables as `Bitmap Heap Scan` (4.18..12.64) and, with bitmap off, `Seq Scan` (20.12, goopg 20.125); goopg now agrees | they exist to drive the executor's IndexScan, so they plan under `enable_seqscan = off, enable_bitmapscan = off`, where PG elects the Index Scan (`planOneIndexScan`) |
| `TestParallelNLIJointypeIdentity/semi`, `/anti`, `TestSubqueryUnnestKillSwitch`, `TestHashedInMixedKindFallsBack` | legacy-only machinery (M0145-0030 hand-off) | pinned with `SetJointreePipeline("0")`; `left` stays on the default arm |
| `TestExplainTransitiveGroupKeyRendersInnerCall` (new red after the estimator fix) | the inner aggregate is now estimated at PG's 200 groups (was 1), so the outer aggregate is hashed. PG elects GroupAggregate over one Sort DESC and keeps the Subquery Scan; goopg does not adopt ORDER BY's direction for grouping | the pin keeps its purpose (the inner call renders, never an opaque ref) and accepts either strategy; the grouping gap is ledgered with its own task |

## The two estimator fixes (PG citations)

1. **`scalararraysel` calls the element operator's estimator**
   (`./postgres/src/backend/utils/adt/selfuncs.c:1821`), so eqsel's
   `isunique` branch (`selfuncs.c:338`) applies to each IN-list element as it
   does to a scalar `col = const`. `inListElementSelectivity` now tries
   `uniqueEqSelectivity` first. That is the sibling twin of `eqOpSelectivity`,
   which already had it (Hard-won Rule 2). The reliability-tracking twin no
   longer declines a unique column without statistics.
2. **`vardata->rel->tuples` is the estimate, not `pg_class.reltuples`.**
   Without ANALYZE, PG's divisor is still the `estimate_rel_size` row count
   `get_relation_info` stamps on the rel (`./postgres/src/backend/optimizer/util/plancat.c`).
   `baseColumnOfTable` read only `Stats.RowCount`, so an unanalysed relation's
   base column had `rawRows = 0` and every consumer fell to a default.
   It now falls back to the scan's `EstRelRows`, the same estimate. `IndexScan`
   carries no `EstRelRows`, so only `*SeqScan` leaves get it (ledgered).

Neither is a tuning constant. Both move goopg's number onto PG's: rows=1 for
the SAOP fixture, 200 groups for the r66t inner aggregate.

## Gates (default pipeline = jointree, staged tree)

- units (full precommit scope): PASS.
- `tpch-spotcheck`: PASS.
- `tpcds-sf025 sweep`: `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`.
  PLAN-SHAPE `same=74 changed=25`, the fire set. FLOW-CONVERGENCE default:
  `pinned-spine=0 jointree-pullup=23 seam-classes=2 seam-declines=6`. The
  legacy control reads `pinned-spine=207 jointree-pullup=0 seam-classes=4
  seam-declines=31`.
- `tpch-acceptance-arm` vs `arm-on-20260922-loop77`: values identical, PASS.
- `tpcds-fireset`: 25 fires, `introduced=none unchanged=none missing=none`,
  PASS.
- `make ea-ratchet`: baseline 53 → current 50, **1 NEW**:
  `Q95:cte:ws_wh+customer_address+date_dim+web_sales+web_site`
  (`Hash Semi Join`, est 1, actual 22, `pg_est` null). The flip introduced the
  key and PG has no estimate for that scope, so neither G4 widening permits a
  repin: ledger row, with the owning recon held in the M0145-0001 lineage
  escalation (S4 forbids filing it). (A first run scored 0 nodes because the
  scratch PG held port 5534; that run is void.)

## D2 report

1. **TPC-H** (`PGSHAPED=1`, parallel, seed-pinned lane):
   - `PLAN-PARITY: queries=22 match=2`, Q6 and Q11, the same as the blessed
     legacy baseline (M0145-0025, match=2/22, Q6/Q11).
   - `CATEGORIES-EXCL-MATCH: join-order=16 join-method=11 scan-type=11
     parameterisation=6 aggregation-strategy=4 sort-strategy=8 parallelism=16
     qual-placement=5 rendering=4`. Against the baseline (15/10/10/6/7/11/15/4/2)
     every category is within ±3.
   - **TPC-DS SF0.25:** `match=2` (Q9, Q41; floor held).
     `CATEGORIES-EXCL-MATCH: join-order=90 join-method=68 scan-type=61
     parameterisation=55 aggregation-strategy=40 sort-strategy=60
     parallelism=81 qual-placement=27 rendering=26`. Same-binary legacy
     control: 91/68/61/54/40/60/82/27/26, every category within ±1.
2. `shape-delta`: SF0.25 `same=74 changed=25`.
3. Stats epoch: both SF0.25 arms from one binary on fresh private clones of
   the same source; TPC-H seed 20260905 as in the baseline.
4. Seam declines: default arm `seam-classes=2 seam-declines=6`, legacy
   `4/31` (flow-convergence instrument in the sweep).
5. Planning route: PG-shaped search, jointree pipeline.
6. Movement: none (no category beyond ±3, match unchanged; the ea-ratchet
   count fell 53 → 50 but introduced one NEW key, so it is not claimed).
   Parent: M0145-0007.
7. Wall time. Acceptance arm, same day, serial, on the pre-flip binary:
   - The whole arm is 1.14x (58.3 s → 66.5 s).
   - Isolated A/B on one binary, both pipelines: **Q18 12.3 s → 20.2 s** and
     **Q22 0.48 s → 1.52 s**. Q21 is faster (2.83 s → 2.15 s). Q4's
     full-run gap was cache warmth: cold, the arms are 1.67 s and 1.89 s.
   - Neither estimator fix causes it: without them Q18 is 24.5 s and Q22
     1.41 s. Nor does the NULL-key guard: without it, 23.1 s and 1.42 s.
   - Both queries are SHAPE-DIFF against PG on both arms. Ledgered; the recon is held in the
     M0145-0001 lineage escalation (S4). Time is reported, not judged (AGENT.md Goal).

## Remaining in this task

The legacy-deletion slices: delete `planSelectLegacyPipeline` and the machinery
only it runs (the group L tests go with it), the seam guards retired by 0001's
list, and the knob. The fire-set gate's HEAD-vs-staged redesign landed FIRST
(slice 1, `m0145-0008-del1-fireset-head-vs-staged.md`), because deleting the
knob under the old legacy-vs-jointree design would have made the gate derive
zero fires on every run.
Slice 2 (`m0145-0008-del2-legacy-pipeline-deleted.md`) deleted the legacy
pipeline and retired the knob; no plan moved. The dead code it left, the
retired seam guards and the knob's script remnants remain.
