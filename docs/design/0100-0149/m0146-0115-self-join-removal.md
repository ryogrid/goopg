# M0146-0115 — an inner self-join on a unique key keeps one scan

Status: done 2026-10-09 (dff8d5999). Parent: M0146-0005.

## Problem

PG 18 removes a self-join that a unique key proves redundant
(`remove_useless_self_joins`, analyzejoins.c). goopg kept the join. The
regress join file has dozens of these:

| query | goopg | PG 18.3 |
|---|---|---|
| `select p.* from sj p, sj q where q.a = p.a and q.b = q.a - 1` | `Hash Join` of `sj p` and `sj q` | `Seq Scan on sj q  Filter: ((a IS NOT NULL) AND (b = (a - 1)))` |

## PG behaviour

Take two references to one plain table: x, the earlier, and y, the later.
Suppose they are joined by equalities `x.c = y.c` that cover every column
of a unique index. Then each y row pairs with exactly one x row, which is
the same row. PG does the following:

- it removes x and keeps y;
- it rewrites every Var of x as one of y;
- it turns a restriction whose two sides are now the same expression into
  `expr IS NOT NULL`;
- it repeats until no pair is left, so a double self-join removes two
  scans.

x's own restrictions come before y's in the merged list, which gives the
Filter order.

## Change

`internal/optimizer/remove_self_joins.go`. The planner calls
`removeUselessSelfJoins` right after `removeUselessLeftJoins`, on the
statement and before its FROM clause is planned.

- **Shapes handled.**
  - x is a comma-list item with no joins of its own, or the base of a FROM
    item whose first join is `INNER JOIN y ON …`. In the second case the
    ON clause moves to WHERE: an inner join at the bottom of the item
    filters the same rows.
  - y is a comma-list item or an inner-joined item, never a nullable side.
- **Relations.** Both references are plain tables: no subquery, function,
  LATERAL, TABLESAMPLE or column aliases. They name the same catalog table,
  which is not a view, matview or CTE, not partitioned, not an inheritance
  parent and not under row-level security. The statement has no set
  operation and no row-locking clause.
- **The unique proof.** `selfJoinUniqueKeyCovered` reads the top-level
  WHERE conjuncts plus the moved ON conjuncts. They must hold same-column
  equalities `x.c = y.c` that cover an immediate, non-partial unique
  index.
- **Attribution.** The rewrite declines on any of these:
  - a schema-qualified column;
  - an unqualified reference to a column of the table or to either
    qualifier (ambiguous, or a whole-row reference);
  - a NATURAL or USING join;
  - a nested FROM that reuses x's qualifier.

  A bare `*` expands to one `qualifier.*` per FROM item first, so the
  output columns keep their count and order.
- **The rewrite.**
  - `renameParserQualifier` deep-copies the statement and renames x to y in
    every ColumnRef and StarExpr. The copy fails closed past depth 200 and
    on maps, so the caller's statement is never touched.
  - The WHERE list keeps x's own conjuncts first, then the derived
    `IS NOT NULL` for each reflexive equality (`parserExprsSame`), then the
    rest. Duplicates are dropped.

## Verification

- **Probes** (PG 18.3 vs goopg). These are identical to PG:
  - the trivial self-join;
  - the double self-join, where two scans go;
  - `x.c = y.c AND x.b IS NULL`, in PG's Filter order;
  - `select *` results.

  A join on a different unique column keeps the join, as PG does.
- **Tests.**
  - `TestSelfJoinOnUniqueKeyIsRemoved` checks the EXPLAIN shape and the
    rows.
  - `TestRemoveUselessSelfJoinsLeavesInputUntouched` checks that the input
    statement keeps both references and its unrenamed join clause.
- **Regress A/B.** join 14868 → 14730 diff lines and equivclass 280 → 264.
  Error counts are unchanged.
- **TPC.** No TPC-H or TPC-DS query has a unique-key self-join, so the
  TPC-H plans are byte-identical and the fire set reports no changed query
  at either scale.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Not covered (ledgered)

- A semi join made by pulling up EXISTS / IN over the same table.
- Self-joins proved through equivalence classes or constants rather than
  a direct `x.c = y.c`.
- Joins nested deeper than the first join of a FROM item.
- A statement with an unqualified column reference, e.g. a subquery that
  reads `a` bare. It declines.
- PG's `Result  One-Time Filter: (t2.a = t2.a)` gating when the removal
  happens inside a subplan.
