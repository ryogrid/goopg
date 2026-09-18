# M0141-S2b-13 — impl: restore PG `COSTS_EQUAL` semantics in the serial pathlist tournament

`Kind: impl` · `Parent: M0141-S2b-12`

Status: in progress — implementation landed, gates running.

## Task

S2b-12's recon (its design doc is the full justification) proved the
M0129-S1 exact-cost fallback in `comparePathCostsFuzzily` cannot be
*narrowed* into the desired behaviour — the grouped-rel signature Q4/Q12
needs released (keyless-cheaper vs keyful-dearer in-band) is identical to
the join-rel signature Q74 needed protected — and measured a full
`COSTS_EQUAL` restore as net-positive. This task lands it, plus the
`partialaggupper.go` no-split-arm sibling reorder.

## What changed

### `internal/optimizer/path.go`

- `comparePathCostsFuzzily`: the exact-cost fallback inside the
  double-fuzz branch deleted; both costs fuzzily equal now returns
  `costsEqual`, matching `compare_path_costs_fuzzily`
  (`postgres/src/backend/optimizer/util/pathnode.c:227-237`).
- `comparePaths` rewritten from the four-dimension fold to a direct port
  of `add_path`'s pairwise dominance table (pathnode.c:480-618). The
  S2b-12 experiment (naive `costsEqual` under the old dim model) exposed
  live divergences the dim abstraction cannot express; the port restores
  all of them:
  - `COSTS_BETTER1/2` dominance now requires PG's guards: pathkeys not
    opposed, outer rels `EQUAL`/subset on the winning side,
    `rows <=`/`>=`, `parallel_safe >=`/`<=` (pathnode.c:586-608). The dim
    model had no rows check at all.
  - `COSTS_EQUAL` + `PATHKEYS_BETTER1/2`: pathkeys dominate only under
    PG's outer/rows/psafe conditions (pathnode.c:524-540).
  - `COSTS_EQUAL` + `PATHKEYS_EQUAL` + equal outers: PG's chain —
    parallel-safety, then rows, then `compare_path_costs_fuzzily` at the
    tight fuzz `1.0000000001`, then keep-old (pathnode.c:544-575). The
    tight-fuzz re-comparison replaces the dim model's keep-first, which
    `TestPartialSortVerdictIsCostDriven` proved divergent (it elected the
    0.003%-dearer worker arm in all 16 grid cells where PG elects the
    cheaper leader arm).
  - Parameterized paths pretend `NIL` pathkeys (pathnode.c:475:
    `new_path_pathkeys = new_path->param_info ? NIL : ...`), expressed
    via `RequiredOuter == 0`. The dim model compared their raw keys,
    which artificially kept parameterized index scans alive against
    cheaper same-parameterization rivals.
  - `PATHKEYS_DIFFERENT` keeps both regardless of cost (pathnode.c:512),
    matching the old `dimIncomparable` early-out.
- Dead helpers `boolDim`/`costDim` removed (the dim fold was their only
  caller).

### `internal/optimizer/partialaggupper.go`

The gathered no-split arm now files the whole SORTED family (sorted agg
+ worker-sort GatherMerge arm) BEFORE the HASHED arm — PG's
can_sort-before-can_hash order (`add_paths_to_grouping_rel`,
`src/backend/optimizer/plan/planner.c`), the same change S2b-11 made in
`groupingpaths.go`. Under `COSTS_EQUAL` insertion order decides fuzzy
ties, so the order is load-bearing.

### Tests re-asserted to PG semantics

- `path_test.go::TestComparePathCostsFuzzily_WithinFuzzIsEqual` —
  within-fuzz pair now expects `costsEqual`.
- `groupingpaths_test.go::TestAddGroupingPathsSingleCandidatePerShape` —
  the fixture's pair is fuzzily tied; PG's pathkeys dominance rejects
  the keyless hashed candidate → lone sorted survivor.
- `pathindexordered_test.go::TestAddBaseRelIndexPathsRunsBothHalves` —
  the parameterised half survives as `PathBitmapHeapScan`: the
  fuzzily-dearer parameterised `PathIndexScan` is dominated under the
  `NIL`-keys pretense. Count now includes bitmap kinds.
- `upperordered_test.go::TestElectOrderedGroupingElectsNoSortAtQ4Numbers` —
  Q4's literal numbers no longer produce a 2-candidate election (the
  tied hashed is rejected upstream). The fixture now builds a genuine
  startup/total trade-off (`COSTS_DIFFERENT`, `ConsiderStartup`) — the
  way two candidates legitimately coexist under upstream `add_path`.
- `parallel_hash_path_consumer_test.go` (both C19f consumers) — the
  fixture's Gather-over-partial-hash-join was only 0.26% cheaper than
  the keyed mergejoin incumbent, INSIDE `STD_FUZZ_FACTOR`. Under
  `COSTS_EQUAL` + `PATHKEYS_BETTER2` the keyed merge correctly evicts
  it (pathnode.c:533-539), as it also evicts the keyless serial hash
  join — PG resolves the identical contest the same way; the fixture
  only ever "won" through the deviation-era exact-cost tiebreak.
  `c19fSettings` recalibrated (`MaxParallelWorkersPerGather` 2→4,
  `CPUTupleCost`→1.0) so the partial path wins by a REAL margin — more
  workers alone cannot do it because `cost_seqscan` divides only
  `cpu_run_cost`, not the page-bound disk cost (costsize.c:336-354).
- `explain_alias_source_keys_test.go::TestExplainTransitiveGroupKeyRendersInnerCall` —
  the sorted-first grouping order now elects a `GroupAggregate` top
  node, which renders the transitive group key parenthesised:
  `Group Key: (count(x))` (was bare `count(x)` under the elected
  HashAggregate). The transitive resolution the test pins is intact —
  only the node type (and therefore its renderer) changed.

## Discovered divergence (recorded, not fixed here)

While tracing the C19f eviction: `makeGatherPath`/`makeGatherMergePath`
always stamp `Rows = computeGatherRows(sub)` (`sub.Rows × parallel
divisor`). PG's `cost_gather`/`cost_gather_merge` stamp `rel->rows`
unless the caller overrides — `generate_gather_paths` passes
`override_rows=false` for scan AND join rels (allpaths.c:3091-3112,
:557, :3518), so a scan/join Gather carries the relation's own total
(e.g. 186), not the divide-then-multiply round-trip estimate (e.g.
187). The off-by-one only reaches `add_path`'s `rows` tie-break inside
a fuzzy tie and shifts EXPLAIN's `rows=` estimate by the same rounding;
the C19f eviction itself was pathkey-driven and unaffected. Ledgered
as a separate small port rather than folded into this commit.

## Measured expectation (from S2b-12's naive-restore experiment)

The full port adds rows/psafe/NIL-pretense corrections on top of that
measurement, so movement may differ in detail; expected direction:

- TPC-H `CATEGORIES-EXCL-MATCH` aggregation-strategy 9→4; Q4/Q12
  (+Q5/Q8/Q21/Q22) flip HashAggregate→GroupAggregate.
- TPC-DS SF0.25 aggregation-strategy 71→44, sort-strategy 77→71.
- Known adverse drift (S2b-12): TPC-H join-order +2/join-method
  +1/scan-type +1; TPC-DS parallelism +2, qual-placement +2; TPC-H Q9
  away-from-PG (pre-existing cost-model disagreement the tie-break
  exposes). No reverts (R3) — adverse drift is filed, not reverted.
- Q74 stays healthy (hash joins through the CTE chain) — the
  `initialRelRows` estimate fallback, not this comparator, holds down
  the NL pathology.

## Gates (2026-09-18 run, patched tree vs HEAD e28a4fc9f)

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: PASS.
- `scripts/tpch-spotcheck.sh`: PASS + stamp (Q12=2, Q13=34).
- `scripts/tpch-acceptance-arm.sh`: 24/24 value MATCH vs a HEAD
  (e28a4fc9f) arm + stamp.
- `scripts/tpcds-sf025-regression.sh sweep`: PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3 (oracle-directed) + stamp;
  Q74 PASS; 64/99 plans changed; wall 154s→149s.
- TPC-H floor capture (`estimate-audit -plan-only`, private clone) +
  `pg-plan-parity-diff.py` vs `bench/tpch/plans-pg/`:
  `match=8` (was 7, floor ≥7 holds); same-epoch HEAD capture also
  `match=8` with identical per-query verdict sets — no prior match
  lost. `CATEGORIES-EXCL-MATCH` HEAD→patched:
  aggregation-strategy 8→3, parameterisation 5→4, qual-placement 4→3;
  join-order 12→14, join-method 9→10, scan-type 8→9 (adverse, within
  the ±3 band S2b-12 predicted).
- TPC-DS SF0.25 parity vs `bench/tpcds/plans-pg/`:
  `match=2` (Q9/Q41 — floor holds, same queries as HEAD).
  `CATEGORIES-EXCL-MATCH` HEAD→patched:
  aggregation-strategy 71→44, sort-strategy 77→69, join-method 69→66,
  scan-type 59→56, join-order 91→90, parallelism 84→84;
  qual-placement 20→23, parameterisation 45→46, rendering 23→24
  (adverse, ≤+3).
- `make ea-ratchet`: HEAD reproduces the baseline's 70 findings
  exactly (PASS); patched = 76 findings, +13 NEW / −7 FIXED vs HEAD.
  Every NEW finding is relset-key churn from plan-shape change, not an
  estimate regression — the estimator is untouched: Q85's
  `reason`-early join order matches PG's own plan (Hash Join on
  `wr_reason_sk` right after web_returns); Q54 is the intended
  HashAggregate→GroupAggregate node rename at identical est/actual
  (22/0); Q61/Q68/Q89/Q7/Q13 re-key the known ~40x
  `date_dim+store_sales`-family under-estimates already tracked
  (Q34/Q73 class). Baseline re-pinned to 76 via
  `make ea-ratchet-repin` (M0142-0012 precedent: repin + file the NEW
  findings for triage); re-run PASS 76=76. The 13 findings are filed
  for triage as **M0141-S2b-14**.
- Q74 timing sanity, SF0.25 (:65437, largest loaded scale — the SF=1
  goopg cluster's catalog is empty): 1.8s, plan 2NL/5HJ/2Gather vs
  PG's own 5NL/2HJ/2Gather — no NL resurrection.

## Ledgered divergence

The `makeGatherPath` row-stamp divergence above is filed separately.
