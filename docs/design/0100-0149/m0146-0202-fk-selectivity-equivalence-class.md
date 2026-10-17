# M0146-0202 — foreign-key join selectivity matches columns through equivalence classes

Status: done 2026-10-16 — TPC-H Q9 matches PG.

## Problem

TPC-H Q9 joins `lineitem` to `{partsupp, part, supplier, nation}`. PG
estimates the `lineitem` probe's Nested Loop at 116871 rows from 15640 outer
rows, about 7.5 per row: 6M lineitem over 800k partsupp. goopg estimated 39
rows from 13034. That moved every node above, ending in a GroupAggregate over
Gather Merge where PG has a Sort over a Finalize HashAggregate.

PG's estimate is the declared FK `lineitem_partsupp_fk` (`l_partkey`,
`l_suppkey` → `partsupp`), applied by `get_foreign_key_join_selectivity`
(costsize.c:5651) at 1/|partsupp|.

- `match_eclasses_to_foreign_key_col` matches each FK column through its
  equivalence class.
- At the join, any restrictlist clause whose `parent_ec` is that class is
  consumed (costsize.c:5718-5733): "even if equivclass.c chose to generate a
  clause equating some other pair of Vars, it could have generated one
  equating the FK's Vars".

Q9 writes all three `suppkey` equalities, so the class is {ps_suppkey,
s_suppkey, l_suppkey}, and the join applies `s_suppkey = l_suppkey`.

goopg's `superkeyJoinSelectivity` saw only the applied clauses. Their pairs
were lineitem–supplier and lineitem–part, so `fkParentRel` found no
partsupp pair. The FK was skipped and eqjoinsel multiplied 1/200000 by
1/10000.

The coverage test was also too loose. `coveringClauses` checked only the
referencing columns, so a clause equating `l_suppkey` to supplier counted
toward an FK onto partsupp. PG's matching
(`match_foreign_keys_to_quals`) needs each referencing Var equated to its
own referenced Var.

## Change

- `ecFKPairs` (`joinrelsize.go`) adds stand-in pairs. For each applied
  clause in a class, every other clause of the class in the statement's list
  that splits the same way across the join is added. Each stand-in is
  `virtual` and owned by the applied clause, so consuming it consumes that
  clause.
- `fkCoveringClauses` replaces the generic cover test for the FK arm. Each
  referencing column must be paired with its own referenced column of the
  parent relation.
  - One applied clause standing for two key columns returns nothing, which is
    PG's `nmatched_ec` chicken-out.
- The unique-index bound keeps to applied clauses: `coveringClauses` skips
  stand-ins. `provableKeys` and `fkParentRel` index `removed` through
  `pairOwner`.
- Test: `TestForeignKeyMatchesThroughEquivalenceClass`, on Q9's shape.
  - With the class member, the FK fires at 1/800000 and consumes both
    applied clauses.
  - Without it, it does not fire.

## Measured

Captures in `analysis/m0146/m0146-0202/`, compared with the M0146-0201
captures:

- **TPC-H:** match 17 → 18 (Q9 MATCH). No other plan changed.
- **TPC-DS SF0.25 and SF1:** plan text byte-identical (84/99, 71/99).

The acceptance arm executed Q9 with values identical (175 rows; 24 MATCH).

## Residual

PG also discounts `ec_has_const` classes (`nconst_ec`): an FK column matched
to a class that holds a constant generates no join clause, and PG adjusts
the selectivity for it (costsize.c:5763-5767, 5848-5866). goopg's stand-ins come
only from join clauses, so such an FK column is uncovered, and goopg skips
the FK instead.
