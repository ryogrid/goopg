# M0146-0005dk — a semijoin's RHS is unique-ified (JOIN_UNIQUE_INNER)

Status: done (2026-10-03). Parent: M0146-0005 (banner item 3).

## The PG behaviour

`populate_joinrel_with_paths` (joinrels.c) handles a JOIN_SEMI in two
independent arms:

- **Forward containment.** The ordinary JOIN_SEMI paths, plus PG 18's
  commuted JOIN_RIGHT_SEMI.
- **Unique-ify.** If one input rel *is* the semijoin's syntactic RHS and
  `create_unique_path` can unique-ify it, PG also adds JOIN_UNIQUE_INNER
  `(rel1, rel2)` and JOIN_UNIQUE_OUTER `(rel2, rel1)`. Each de-duplicates
  the RHS on its correlation columns and then runs a plain inner join.

`create_unique_path` (pathnode.c) chooses how to de-duplicate:

- **NOOP** when a unique index or a distinct subquery already proves
  uniqueness.
- **SORT or HASH** otherwise:
  - estimate the groups;
  - price sort+unique as `cost_sort` plus one `cpu_operator_cost` per
    column per input row;
  - price hashed aggregation as `cost_agg(AGG_HASHED)` with no
    aggregates, refused when `(width + 64) × groups` exceeds the hash
    memory;
  - keep the cheaper (fewer disabled nodes first, then total cost).

The path's `parallel_safe` is `rel->consider_parallel &&
subpath->parallel_safe`.

`add_paths_to_joinrel` then prices the JOIN_UNIQUE_INNER join with
`extra.inner_unique = bms_is_subset(min_lefthand, outerrel->relids)`. Its
semi factors are the semijoin's own: `compute_semi_anti_join_factors`
under JOIN_SEMI, over the un-unique-ified inner rel's rows.
`final_cost_hashjoin` gives a UniquePath inner a bucket fraction of
`1 / virtualbuckets`.

## What goopg was missing

The forward JOIN_UNIQUE_INNER call already existed, but on the jointree
pipeline (the only pipeline since M0145-0008) every pulled sublink body
reaches `createPulledUniquePath`. That function ported only the NOOP arm,
so a plain relation such as `date_dim_8` in TPC-DS Q83, or the `s` of
`EXISTS (SELECT 1 FROM t s WHERE s.f2 = upper.f1)`, could never be
unique-ified. goopg kept a Hash Semi Join where PG builds Hash Join over
HashAggregate.

## What landed

- **`createPulledBaseUniquePath` (createuniquepath.go).** The paid arm for
  a pulled RHS that is one base relation:
  - maps the problem-space uniq exprs onto the leaf's columns
    (`index − baseOffset`), merging a column equated twice;
  - estimates the groups over the leaf;
  - prices SORT and HASH as above and keeps the cheaper.

  The path carries its uniq exprs in problem space (`Path.UniqueExprs`),
  because a Sort key and the lowering both re-base through the child's
  layout. A multi-relation RHS still declines.
- **Hashed `DistinctOn`.** PG's UNIQUE_PATH_HASH is an Agg grouped on the
  uniq exprs with the rest of the row passed through. goopg expresses it
  as `DistinctOn{Hashed: true}`: the executor keeps the first row of each
  key in a hash set, and EXPLAIN prints `HashAggregate` / `Group Key:`
  with the key chased to its source column. Only the uniq exprs are read
  above a semijoin RHS, so which duplicate survives is immaterial. The
  SORT method reuses the existing adjacency `DistinctOn` over a Sort.
- **`createUniquePlan`.** Resolves `UniqueExprs` through the built
  child's layout into key positions, and publishes the child's layout.
  Before, it returned a nil layout, which was harmless for the prebuilt
  subquery leaf but would make a join above panic over a base relation.
- **Join costing (joinpaths.go, cost_funcs.go).** For JOIN_UNIQUE_INNER:
  - `uniqInnerUnique = min_lefthand ⊆ outer` (PG's rule; merge's
    `innerUnique` now uses it too);
  - the semi factors feed the nested loop and the hash join's early-exit
    branch;
  - `hashJoinFinalCostInput.uniquePathInner` prices the bucket at
    `1 / virtualbuckets`.
- **`ParallelSafe` on every PathUnique.** Without it a join over the
  unique path is parallel-unsafe, and `add_path`'s COSTS_EQUAL tie-break
  (parallel safety before rows and the tight-fuzz cost) handed the plan
  back to the semi join, even where the unique path was 0.015 cheaper,
  as in Q83.
- **NOOP for a relation.** `reduce_unique_semijoins`
  (`pulledSemiRhsIsUnique`) already deletes the SpecialJoinInfo of a
  single-relation RHS whose unique index the equated columns cover. The
  index proofs it does not port (a key completed by a restriction
  constant, a cross-type equality) would reach the paid arm here
  (ledgered).

## Verification

- **Scratch probes against PG 18.3** match plan and cost to the cent:
  - `subselect_tbl` one-key EXISTS: Hash Join 81.27, HashAggregate
    33.12..35.12.
  - Two-key EXISTS: 86.62.
  - A three-level nested IN (Q83's shape): 2927.12 / 1107.80 / 745.81.
- **Tests:**
  - `TestCreatePulledUniquePathNoop`: NOOP / SORT / HASH / desync.
  - `TestUniqueifiedSemiInnerRows`: IN and EXISTS over a duplicate- and
    NULL-heavy RHS against a DISTINCT reference.
  - `TestExplainSemiAntiJoinLabels`: a non-equality correlation stays
    `Semi Join`; an all-equality IN shows PG's HashAggregate.
  - The chained semi-link test now uses non-unique-ifiable links, so it
    still sees two semi joins.
- **TPC-DS fire set** (Q83 at SF0.25, Q33 at SF1; all values PASS):
  - SF0.25 Q83's catalog_returns subtree matches PG line for line;
    `scan-type` 29→28.
  - SF1 `join-order` 61→60, and PG-aligned plan lines 2148→2178.
  - match / text-identity are flat.
- **Regress A/B against HEAD.** Eight suites are byte-identical, and no
  result row changed. `subselect` and `join` move as follows:
  - `subselect`'s `tenk1 A … hundred in (… B.odd = A.odd)` now has PG's
    Hash Join / HashAggregate. Only the key order differs: PG prints
    `b.odd, b.hundred`.
  - The `IN (VALUES …)` cases took a different non-PG shape. PG 18
    rewrites them to `= ANY('{…}')` (`convert_VALUES_to_ANY`), which
    goopg lacks either way.
  - `join`'s two `tenk1 a WHERE unique1 IN (SELECT unique2 …)` plans flip
    from PG's Hash Semi Join to a HashAggregate-driven Nested Loop. Cause:
    regress's `VACUUM ANALYZE` collects no column statistics on goopg
    (filed **M0146-0009j**); with `ANALYZE` the same query plans PG's
    Hash Semi Join.

## Residuals (ledgered)

- A multi-relation RHS is not unique-ified (PG unique-ifies any rel equal
  to `syn_righthand`).
- The relation-NOOP proofs beyond `reduce_unique_semijoins`' are not
  ported.
- The uniq-expr order follows goopg's spanning-clause order, not PG's
  (`Group Key: b.hundred, b.odd` vs `b.odd, b.hundred`).
- Q23's unique-ified CTE inner is a CTE scan leaf, not a base relation.
- `VACUUM (ANALYZE)` statistics (M0146-0009j).
