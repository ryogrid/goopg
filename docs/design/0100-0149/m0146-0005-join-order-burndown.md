# M0146-0005: join-order / candidate-pool divergence burn-down

Status: **IN PROGRESS**. Slice 1 landed 2026-09-25 (`23edfda2e`); slice 2 landed
2026-09-25 (`2962ae22d`). Task:
`.ralph/fix_plan.md` M0146-0005 (Kind: impl, Parent: none). Evidence:
`analysis/m0146/m0146-0005/`.

The family owns 50 first-divergence records in M0146-0001's census:
- TPC-H: 5;
- TPC-DS SF0.25: 24;
- TPC-DS SF1: 21.

Each slice takes one mechanism, measured with the per-candidate traces:
goopg's `DPPATH` (`GOOPG_PGSHAPED_DP_TRACE=1`) against PG's `PLANCAND`
(the M0144-0005 instrumented build).

## Slice 1: Memoize `calls` is the outer path's rows

### Finding

TPC-H Q3, Q9 and Q10 diverge the same way: PG has a Parallel Hash Join where
goopg has a nested loop. On Q3, `orders ⋈ customer` offers both candidates.

| candidate | goopg total | PG total |
|---|---|---|
| partial Parallel Hash Join | 38260.15 | 37618.5 |
| partial nested loop, Memoize over `customer_pk` | **37991.19** | ≥ 185294 |

goopg's nested loop was about 5x cheaper than PG's, and it won. The distinct
count was not the cause: goopg's ANALYZE gives 93573 for `o_custkey` and
PG's gives 95137. A debug print of `costMemoizeRescan`'s inputs, not
committed and recorded in `goopg-q3-memoize-inputs-before.txt`, showed
`calls=735593`. That is the whole `orders` rel's row count, which put the hit
ratio at 0.87.

PG passes `outer_path->rows`
(`postgres/src/backend/optimizer/path/joinpath.c:812-819`, stored as
`mpath->calls` at `pathnode.c:1693` and read by `cost_memoize_rescan`,
`costsize.c:2549`). For a partial outer that is the per-worker count, about
180K, so each worker's cache is priced by the probes that worker actually
makes. PG's hit ratio here is about 0.47.

### Change

`getMemoizePath` (`internal/optimizer/joinpathsmemoize.go`) passes
`outerPath.Rows` as `calls`. The `< 2 rows` gate keeps reading the rel
(`outer_path->parent->rows`, joinpath.c:696), as PG's does.
`TestMemoizeCallsAreTheOuterPathRows` pins that a per-worker outer prices its
cache on its own rows.

### Measured

- **TPC-H fire set:** Q3, Q9, Q10 and Q18 change. **Q3 and Q10 now match PG
  exactly**: PLAN-PARITY match 3 → 5.
  - `join-order` 17 → 15, `join-method` 10 → 8, `parameterisation` 8 → 4.
  - Q9 still diverges at a nested loop under the Partial HashAggregate; that
    is the next slice's candidate.
- **TPC-DS fire set:** 52 queries at SF0.25 and 37 at SF1 change plans;
  Memoize nested loops are everywhere in TPC-DS.
  - Parity is unchanged: match 4 at SF0.25 and 6 at SF1.
  - `parameterisation` drops by 1 at each scale.
  - No timeouts introduced.
- **Values:** identical. Acceptance arm 24 MATCH; SF0.25 sweep
  `MISMATCH=0 TIMEOUT=0`.
- **Serial arm times:** Q3 2.96 s → 1.56 s, Q10 6.80 s → 1.58 s.
- **EA-RATCHET:** 52 → 52.

Movement: yes. TPC-H PLAN-PARITY match 3 → 5 (Q3, Q10).

### Not ported (ledgered)

PG sizes the key's distinct count with
`estimate_num_groups(param_exprs, calls)`, which also scales for the outer
rel's restriction selectivity. goopg's `memoizeKeyNDistinct` reads the
column's `get_variable_numdistinct` and only clamps it to `calls`.

## Slice 2: the partial nested loop re-pays the inner's rescan startup

### Finding

After slice 1, TPC-H Q9 still ended in a partial nested loop into
`orders_pk`, where PG has a Parallel Hash Join against `orders`.

| candidate for the last join | goopg | PG |
|---|---|---|
| partial nested loop into `orders_pk` | **49356.8** | at least 83062 (not even built) |
| partial Parallel Hash Join | 78386.8 | 81246.5 |

- goopg's nested loop was the 43026.6 outer plus only 6330 for 96086 probes,
  i.e. 0.066 per probe. Its parameterized `orders_pk` path costs 0.375..0.431.
- PG's parameterized probe costs 0.4275..0.4663.
- PG's cheapest nested-loop candidate for this joinrel is killed by
  `add_partial_path_precheck` at 83062 or more.

For the PG side, the private instrumented PG needed Q9's tables plus the
reference's composite FK `lineitem_partsupp_fk`. Without that FK PG's own
estimate collapses to 47 rows. With it, the private copy reproduces the
reference plan (`pg-reference-fks.txt`).

The partial nested-loop arm (`addPartialNestLoopPaths`) called
`nestloopCost(..., 0, matRescan)`, with the rescan **startup** hard-coded to
0. `initial_cost_nestloop` charges
`(outer_path_rows - 1) * inner_rescan_start_cost`
(`postgres/src/backend/optimizer/path/costsize.c:3299-3302`), which is the
index descent that a parameterized inner re-pays on every rescan, whether or
not the outer is parallel. The serial NLI arm got this term in R69; the
partial arm repeated the arithmetic inline and missed it. That is a
sibling-path defect (hard-won rule 2).

### Change

- Both arms now call one helper, `nliNestLoopCost`
  (`internal/optimizer/joinpathsnli.go`): `initial_cost_nestloop` plus
  `final_cost_nestloop` for a parameterized or memoized inner, with the
  rescan startup included. The two arms can no longer price the same pair
  differently.
- `TestNLINestLoopCostChargesRescanStartup` pins that a probe's startup is
  re-paid per rescan: the same 0.43 per probe whether the startup is 0.375 or
  0. Before the fix it came to 0.055.

### Measured

- **TPC-H fire set:** match 5 → 5; Q3 and Q10 still match.
  - Q9's first divergence moves from `join-method` at depth 4 to
    `qual-placement` at depth 5. Its top is now PG's Parallel Hash Join on
    `orders`.
  - `join-method` 8 → 7.
  - Q9 on the serial arm: 3.54 s → 1.85 s.
- **TPC-DS fire set:** no timeouts introduced at either scale.

  | scale | match | new matches | `parameterisation` | `join-method` | `join-order` |
  |---|---|---|---|---|---|
  | SF0.25 | 4 → **7** | Q12, Q15, Q20 | 54 → 38 | 66 → 56 | 91 → 84 |
  | SF1 | 6 → **8** | Q7, Q91 | 48 → 41 | 65 → 58 | 85 → 83 |

- **Values:** identical. Acceptance arm 24 MATCH; SF0.25 sweep `MISMATCH=0`,
  with 71 shapes changed.
- **EA-RATCHET:** FAIL, with 51 findings (was 52), of which one is NEW:
  - The new finding is Q7's Gather over
    `customer_demographics+date_dim+item+store_sales` (estimate 47, actual
    1944, `pg_est` null).
  - It is the key class the change FIXED under Q27, the sibling template of
    Q7, now surfacing under Q7 because its plan changed.
  - Per the gate table it gets a ledger row and an owning task
    (M0146-0009a).

Movement: yes. TPC-DS PLAN-PARITY match 4 → 7 at SF0.25 (Q12, Q15, Q20) and
6 → 8 at SF1 (Q7, Q91).

## Slice 3 (in progress): Q17 — a correlated SubPlan filter is never priced

Evidence: `analysis/m0146/m0146-0005/slice3/`. The measurements come from the
private instrumented PG 18.3, with two temporary `debug_plan_candidates`-gated
traces in `final_cost_hashjoin` and `approx_tuple_count`.

### Finding

TPC-H Q17 (`l_quantity < (SELECT 0.2 * avg(l_quantity) … WHERE l_partkey =
p_partkey)`) is a join-method divergence. PG hash-joins a full `lineitem`
seq scan (212797); goopg picks a nested loop with a parameterised bitmap
probe (31572). Forcing each join method in PG with and without the filter:

| join filter | hash join | nested loop |
|---|---|---|
| none | 212338 | 31877 |
| the SubPlan | 213576 | 778121 |

- **Per call:** PG prices the correlated SubPlan at `cost_subplan`'s per-call
  cost. For an EXPR_SUBLINK that is the run cost plus the startup, because a
  correlated subplan re-pays its startup: 123.76.
- **Nested loop:** the filter runs on every probed inner row (30 per probe,
  about 5,940 in total).
- **Hash join:** `part` is inner-unique, so `final_cost_hashjoin` charges the
  join filter on `outer_matched_rows` only: 10.
  - `compute_semi_anti_join_factors` requests JOIN_SEMI but passes the
    join's own `sjinfo`, whose `jointype` is JOIN_INNER, and `eqjoinsel`
    switches on that. So `outer_match_frac` is the inner-join selectivity
    of the restrict list: joinrel.rows / (outer.rows × inner.rows),
    1/200000 × 1/3 here.
  - `match_count` = nselec × inner.rows / jselec.

goopg has neither piece:
- `qualEvalCost` charges a flat `cpu_operator_cost` per join conjunct at all
  eight join-qual sites, with no SubPlan term, so its nested loop never pays
  for the 5,940 evaluations.
- `hashJoinFinalCostInputFor` sets `outerMatchFrac = joinrel.Rows /
  outer.Rows`, which lacks the `/ inner.Rows`, and `hashJoinCost` fixes
  `match_count` at 1.

### Planned change

1. A `cost_qual_eval`-style per-conjunct cost: the existing
   `cpu_operator_cost` plus `cost_subplan` for each SubPlan in the conjunct.
   - Correlated: per-call = run + startup.
   - Uncorrelated with a materialising root: startup once.
   - EXISTS: run / rows.
   - ANY / ALL: half the run plus half the rows × `cpu_operator_cost`.
   It reads the SubPlan's planned `PlanCost`, and is used at the join-qual
   sites (NL, hash, merge, parallel arms).
2. The hash join charges its non-hash join quals on `hashjointuples`: the
   inner-unique `outer_matched`, else approx_tuple_count. `outerMatchFrac`
   and `matchCount` become PG's.

This overlaps M0146-0013 (`cost_qual_eval` ordering), which will reuse the
per-conjunct cost.

### Second finding: the qual never reaches the join search (2026-09-25)

Change 1 was implemented as `subplan-qual-cost.wip.patch` in the evidence
directory: a `cost_subplan` port, `joinQualPerTuple`, and all seven join-qual
sites plus the semi/anti nested-loop arm. It moved nothing: the TPC-H
first-divergence census is identical (`census-tpch-with-wip-patch.txt`). The
reason is upstream of costing:
- `relidsOfExpr` (`joinrestrict.go`) does not look inside a sublink's plan.
  `l_quantity < (SubPlan)` therefore reads as a `lineitem`-only clause, and
  `buildRestrictInfos` drops it as `relLevel < 2`.
- Being a correlated sublink, it is also not leaf-eligible
  (`conjunctIsLocalEligible`).
- So it is re-applied as a Filter above the finished join tree, and no join
  path is charged for it.

In PG, the SubPlan's `args` (the `part.p_partkey` Param source) put `part` in
the clause's relids. It is a {lineitem, part} join clause, placed and costed
per join path; that is what lets the hash join's `outer_matched` charge (10)
beat the nested loop's charge (about 5,940).

**Prerequisite, not yet built:**
- The relids of a correlated-sublink clause must include the relations its
  correlation reads.
- `createPlan` must then place such a clause at a join, remapping the outer
  references inside the sublink plan (`remapOuterRefsInSubplan`,
  `bushy.go`/`joinlayout.go`) to that join's row layout.

That is restriction placement, M0146-0012's territory, and it is filed under
M0146-0012. Q17 waits for it. The saved patch is the costing half, to land
together with it. The slice moves to Q19.

## Slice 4: derived OR restrictions (`extract_restriction_or_clauses`)

Landed 2026-09-25 (`80e2d5d21`). Evidence: `analysis/m0146/m0146-0005/slice4/`.

### Finding

TPC-H Q19's first divergence was `join-method`: PG nested-loops from `part`
into `lineitem_part_supp_fkidx`, goopg parallel-hash-joined. PG's `part` scan
carries a filter the query never wrote: the OR of the three arms'
`p_brand`/`p_container`/`p_size` sub-clauses. PG's `orclauses.c` derives it
from the join OR clause, so `part` is sized at about 200 rows. goopg had no
such step; the gap was ledgered at M0127-P5.6-g-iv ("extract_restriction_or_clauses
absent"). goopg sized `part` at 83333 rows.

### Change

- **`orclauses.go`** is a port of `extract_restriction_or_clauses`,
  `extract_or_clause` and `is_safe_restriction_clause_for`. It runs at the
  seam right after `partitionConjunctsForJoinPlanning`:
  - for each join OR clause and each real binding it mentions, it ORs each
    arm's binding-only, leaf-eligible, non-volatile sub-clauses (recursing
    through nested ORs, flattening);
  - it declines if any arm yields nothing;
  - it adds the result to the leaf's local filters when its selectivity is
    ≤ 0.9.
  The pool the partition sees already excludes nullable-side relations, which
  stands in for PG's `join_clause_is_movable_to`.
- **Compensation** (`consider_new_or_clause`): the join OR clause's
  selectivity is divided by the derived clauses' selectivity product. The
  map rides `joinlistProblem` into `searchCtx` and is applied in
  `joinClauseSelectivityExt` before memoising, as PG hacks `norm_selec`.
- **`applyLocalFilterSelectivity`** now rounds (PG's `clamp_row_est` is
  `rint`) instead of truncating. Otherwise Q7's derived
  `n_name = 'FRANCE' OR n_name = 'GERMANY'` sized `nation` at 1 row where
  PG says 2 (25 × 0.0784 = 1.96), quartering the join estimate.

### Measured

- **TPC-H Q19:** the join is now PG's. Derived filters on `part` (202 rows;
  PG 200) and on `lineitem`, and a nested loop probing
  `lineitem_part_supp_fkidx`. Its first divergence moves to the top
  Partial/Finalize aggregation, M0146-0003's category.
- **TPC-H Q7:** the whole join tree now equals PG's, with PG's estimates
  (5000 / 50000 / 60379 / 2513 rows against 2522). The first divergence is
  now the top aggregation: goopg estimates 200 groups for
  `(n1.n_name, n2.n_name, extract(year))` where PG clamps to the input rows
  and sorts. That is M0146-0009's category.
- **TPC-H match:** stays 5/22; `join-method` first divergences go from 2 to 1
  (Q17 remains, blocked on M0146-0012a).
- **TPC-DS parity:**
  - SF0.25: match 7 → 7; join-order 84 → 83, join-method 56 → 55,
    parameterisation 38 → 36, qual-placement 26 → 25.
  - SF1: match 8 → 8; join-order 83 → 84, join-method 58 → 60,
    parallelism 78 → 79, qual-placement 23 → 21.
- **First-divergence depth:** one query per scale went shallower (Q13 at
  SF0.25, Q48 at SF1). Both are OR-heavy and now carry PG's derived filters;
  the remaining difference is which join evaluates the OR join clause.
- **Values:** identical everywhere (acceptance arm; SF0.25 96 PASS); no
  timeouts at either scale.

### Not ported (ledgered)

- PG extracts from every base rel's `joininfo`, including outer-join clauses
  on the nullable side where `join_clause_is_movable_to` allows it. goopg
  derives only from the flat inner pool the seam partitions.
- `scaleByFloat` still truncates at its other call sites; only the
  base-relation size now follows `clamp_row_est`.

## Slice 5 (not landed): the default hash bucket for a stats-less key

Evidence: `analysis/m0146/m0146-0005/slice5/`.

TPC-DS Q79's top join (`join-method` at depth 2, both scales) hash-joins an
aggregated subquery against `customer`. PG prices that orientation at 37599
because the hashed key (`ss_customer_sk`, a GROUP BY output) has no
statistics: `estimate_hash_bucket_stats` punts to a 0.1 bucket, about 14100
of bucket walk. goopg's `estimateHashBucketSize` skips a key without
statistics (`continue`, then "no information"), so its hash join costs 22509
and beats PG's nested loop (24561).

Dropping the skip is PG-faithful, and is saved as
`default-hash-bucket.wip.patch`. But it makes TPC-DS Q47 time out: the CTE
self-join then elects a nested loop over `CTE Scan` with an inner merge join
estimated at 1 row and costed as cached, where PG merge-joins over
`Materialize`. The patch waits on that election (M0146-0005a). Q79 would
additionally need PG's fuzzy startup tie-break against the nested loop.

### M0146-0005b: CTE scans carry the CTE body's pathkeys (landed)

Landed 2026-09-25 (`5301b111b`); `internal/optimizer/ctescanpathkeys.go`.

PG's `set_cte_pathlist` gives a CTE scan path the CTE plan's pathkeys,
translated into the outer query by `convert_subquery_pathkeys`.
`addCTEScanPathkeys` does the same for goopg's prebuilt CTE-scan leaf paths,
once the clause list and query pathkeys are published:
- the body's output ordering comes from `inputNodePathkeys`;
- each ordered output column maps to this query's expression for it, using
  the same useful-column map the ordered index paths consult;
- translation stops at the first key with no counterpart, as
  `convert_subquery_pathkeys` does.

The executor's CTE row cache returns rows in body order, so the claim holds
for every reference.

**Measured:**
- TPC-DS Q47's three CTE scans now merge-join without a Sort (goopg 0..263,
  PG 0..369).
- The fire set fires Q47 only at both scales: join-method 55 → 54 (SF0.25)
  and 60 → 58 (SF1); scan-type +2 at each scale, where PG puts `Materialize`
  over its inner CTE scans.
- Values are identical, with no timeouts.

With 0005b in place, the slice-5 bucket patch no longer times out Q47
(SF0.25 96 PASS). It stays parked on one remaining blocker, M0146-0005c:
twelve executor tests build unanalyzed tables and rely on hash joins, which
PG's default bucket now prices out
(`slice5/executor-tests-failing-with-patch.txt`).

**Not ported (ledgered):**
- PG converts subquery-scan pathkeys the same way (`set_subquery_pathlist`).
- PG's merge join adds `Materialize` over an inner that cannot mark/restore
  cheaply.

### M0146-0005c: the default hash bucket lands (landed)

Landed 2026-09-25 (`eae560af7`).

`estimateHashBucketSize` no longer skips a hash key without statistics. As
in PG's `estimate_hash_bucket_stats`, a default-ndistinct key gets
`Max(0.1, mcv_freq)`. A nil search context (hand-built unit-test pairs)
still reports "no information".

Two prerequisites had to land first:
- M0146-0005b, the CTE-scan pathkeys (the Q47 timeout);
- test fixtures with key statistics. Thirteen executor tests and two
  optimizer tests built tables whose join keys had no statistics and relied
  on hash joins the default bucket now prices out. The executor fixtures'
  ANALYZE does not count heap-written or same-transaction rows (a separate
  recon). So each fixture now carries what ANALYZE would record: a new
  executor helper `setFixtureStats`, TPC-H key ndistinct for the
  TPC-H-shaped catalogs, and unique synthetic keys elsewhere. Every test
  keeps the plan and geometry it asserts.

**Measured:**
- Match counts unchanged (TPC-DS 7 / 8).
- The fire set shows no introduced timeouts.
- First divergence is deeper on Q2 (both scales), Q30 and Q81 (SF1); none
  got shallower.
- Categories: join-order −1/−2, sort-strategy −2/−3; rendering +3/+4, and
  SF1 join-method +2.

Q79 itself still hash-joins. PG keeps its nested loop on the fuzzy
startup tie-break (M0146-0005d).

## Remaining records

Per M0146-0001's `m0146-0001-ranked.txt`, still to be worked:
- TPC-H: Q17 and Q19 (`join-method`); Q9's first divergence is now a
  `qual-placement` one.
- TPC-DS: 24 SF0.25 and 21 SF1 records, split between `join-order`,
  `join-method` and presorted-input `sort-strategy`.

Re-run the first-divergence census on each slice's capture before choosing
the next mechanism.
