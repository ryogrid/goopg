# M0145-0008ae: a pulled semijoin to a provably unique rel becomes an inner join

Status: **LANDED 2026-09-25** (`f19b6f7cc`). Task: `.ralph/fix_plan.md`
M0145-0008ae (Kind: impl, Parent: M0145-0008ad). Evidence:
`analysis/m0145/m0145-0008ae/`.

## What PG does

`query_planner` calls `reduce_unique_semijoins`
(`postgres/src/backend/optimizer/plan/analyzejoins.c:844`,
`postgres/src/backend/optimizer/plan/planmain.c:234`) before the join search.
For every JOIN_SEMI SpecialJoinInfo it:
1. requires a single-baserel `min_righthand` that `rel_supports_distinctness`;
2. collects the clauses linking that rel to `min_lefthand`;
3. asks `innerrel_is_unique(..., JOIN_SEMI, ...)`, which goes through
   `rel_is_distinct_for`.

When the RHS is unique, the SpecialJoinInfo is **deleted**. The pair is then
an ordinary inner join for legality, estimation (`eqjoinsel_inner`) and path
generation. The proof has two arms:
- **RTE_SUBQUERY:** `query_is_distinct_for` on the joined output columns.
- **RTE_RELATION:** a unique index covered by mergejoinable equality clauses
  (`relation_has_unique_index_for`).

M0145-0008ad measured the consequence on TPC-H Q18 with the instrumented PG.
All 63 of PG's candidates for `orders ⋈ ANY_subquery` are JOIN_INNER. goopg
kept the SpecialJoinInfo, its SEMI and unique-inner paths tied exactly, and
the incumbent SEMI path won.

## What changed

- `pulledSemiRhsIsUnique` (`internal/optimizer/reduce_unique_semijoins.go`)
  is the port for a pulled sublink body. It requires:
  - JOIN_SEMI;
  - exactly one leaf and no nested child body, since a child widens the
    syntactic RHS (PG's single-baserel test).

  The proof uses only the `=` clauses between an RHS column and a
  same-typed left-hand column (`equalityRhsSide`):
  - **derived leaf:** `jtPulledBody.distinct` (M0145-0008ab's
    `query_is_distinct_for`);
  - **base-table leaf:** some non-partial UNIQUE index
    (`uniqueKeyColumnSets`) all of whose columns are equated.
- `classifyPulledQuals` skips `pulledSemiJoinInfo` for such a body. Its
  spanning quals are already in the search's qual pool, so they become
  ordinary join clauses. The census reason is `semijoin-reduced-to-inner`.

Not used for the proof, as ledgered:
- restriction clauses equating an RHS column to a constant, which
  `relation_has_unique_index_for` also accepts;
- cross-type equality operators;
- partial unique indexes with a predicate implied by the quals.

In every case a missed proof only keeps the semijoin.

## Tests

- `TestReduceUniqueSemijoinsBaseTable`:
  - IN and EXISTS over a unique key are reduced.
  - These keep their semi join, because reducing them would duplicate outer
    rows:
    - no index;
    - a non-unique index;
    - a composite unique key that is not covered;
    - a unique index on another column.
- `TestPullUpAnyDerivedBody`: distinct bodies (GROUP BY, DISTINCT, UNION) are
  reduced; a LIMIT body keeps its semi join.
- Probe against a stock PG 18.3: 14 value checks (derived and primary-key
  IN/EXISTS shapes) are identical. goopg's plans for the two reductions have
  PG's Hash Join shape.

## Measured

- **TPC-H fire set:** Q18 and Q20 change. Match 3 → 3; `join-method` 11 → 10.
  - **Q18:** `orders ⋈ ANY_subquery` is now a Hash Join, as PG plans it.
  - **Q20:** `ps_partkey IN (SELECT p_partkey FROM part …)` is reduced
    (`part` is keyed on `p_partkey`), and `part ⋈ partsupp` becomes PG's
    nested loop over `partsupp_part_fkidx`. goopg evaluates the
    `ps_availqty > (SubPlan)` filter above the Gather, and EXPLAIN prints no
    `Filter:` line there; PG keeps it on the `partsupp` index scan (ledgered).
    Values are identical serially and in parallel: 101 rows in the parallel
    EXPLAIN ANALYZE, `q20-candidate-parallel-analyze.plan.txt`. Serial time
    went 0.16 s → 0.34 s, which is reported, not judged.
- **TPC-DS fire set:** no changed query at SF0.25 or SF1.
- **Values:** acceptance arm 24 MATCH.
- **EA-RATCHET:** 52 → 52.

## Gates (staged code `f19b6f7cc`)

- units: PASS.
- `tpch-spotcheck`: PASS.
- acceptance arm: 24 MATCH.
- `tpcds-sf025`: 96 PASS, `MISMATCH=0 TIMEOUT=0`, 99/99 shapes the same.
- fire-set (SF0.25 + SF1): PASS, fires none.
- TPC-H fire set: fires Q18 and Q20, no timeouts.
- `make ea-ratchet`: PASS.
- lineage guard: OK.
- pgbench smoke: PASS.

Movement: none. TPC-H match 3 → 3, and `join-method` 11 → 10 is inside the
±3 noise band.
