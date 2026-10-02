# M0146-0005dl — remove_useless_joins for a FROM-clause LEFT JOIN

Status: done (2026-10-03). Parent: M0146-0005 (banner item 3).

## The PG behaviour

`remove_useless_joins` (analyzejoins.c), run by `query_planner` before the
join search, deletes a LEFT JOIN whose nullable side contributes nothing.
`join_is_removable` holds when all of these are true:

- the inner side is a single base relation;
- none of its attributes is needed above the join (`attr_needed` within
  the join's own relids, no PlaceHolderVar);
- it is provably distinct for the join's mergejoinable equalities
  (`rel_is_distinct_for` → `relation_has_unique_index_for`).

Each preserved row then joins at most one inner row and yields exactly one
output row either way, and nothing reads the inner columns. PG repeats the
scan after each removal, because one removal can make another join
removable. TPC-DS Q72 left-joins `catalog_returns` on its primary key and
never reads it; PG plans no join for it.

## What landed

`removeUselessLeftJoins` (remove_useless_joins.go) rewrites the statement
in `planSelectWithSettings`, just before `planFromClause`.
`pushWhereQualsIntoGroupedItems` already rewrites the statement at that
point, which is the precedent. Nothing downstream ever sees the removed
relation: derived pull-ups, bindings, jointree scope, joinlist,
SpecialJoinInfos. It drops `FromExprs[i].Joins[k]` together with its flat
`From` entry and repeats until no join is removable.

A join is removable when:

- it is `LEFT`, not NATURAL or USING, has an ON clause, and its right side
  is a plain table (no subquery, function, LATERAL, TABLESAMPLE or column
  aliases), with no LATERAL or function item anywhere in the FROM list;
- the ON clause holds no sublink;
- the ON clause's equalities equate a set of the table's columns to
  same-typed columns of other FROM tables, and that set covers a
  non-partial unique index (`uniqueKeyColumnSets` / `columnsCover`, the
  `reduce_unique_semijoins` proof). Other ON conjuncts are ignored, as in
  PG: they cannot add rows;
- no column of the table is read anywhere else. The check runs
  `neededColumnNames` over the statement with this join's ON clause
  blanked. A qualified or unqualified reference to any of its columns, or
  a whole-row reference, keeps the join. Statement shapes the collector
  does not model decline: SELECT \*, WITH, window clauses.

Name-level attribution over-counts and never under-counts: an unqualified
name shared with another table keeps the join.

## Verification

- Scratch probes against PG 18.3 return identical plans and values.
  - Removed: PK covered; extra ON restriction; unqualified columns; two
    removals in a chain; a later join's ON reading the first table, then
    both removed.
  - Kept: partial key; no unique key; read in the target list; read in
    WHERE (`IS NULL`); read by a correlated EXISTS; read by a later ON
    whose table is itself read; whole-row reference; INNER join.
- `TestRemoveUselessLeftJoins` pins all 13 cases. It also checks that the
  removed join returns the rows of the same join kept by an opaque ON.
- Regress A/B against HEAD:
  - `join` shrinks 18573→18546 lines, as several of PG's join-removal
    tests now plan the same reduced tree;
  - eight other suites are byte-identical, and no result row changed;
  - `rowsecurity` differs only by the Go pointer addresses its policy
    rendering already prints.
- TPC-DS: Q72 fires at both scales and its values pass. Its
  `catalog_returns` join is gone, as in PG; the rest of goopg's Q72 plan
  already diverged (join order, Gather placement). PG-aligned plan lines
  rose 2332→2334 (SF0.25) and 2178→2179 (SF1). match is flat. The
  classifier newly tags SF1 Q72 `qual-placement` (7→8) on the reshuffled,
  still-divergent tree.
- Gates: units, spotcheck, sweep 96/96, TPC-H arm, ea-ratchet, fire set.

## Found on the way

**M0146-0047 (S2, filed, not worked):** `count(b)` over a null-extended
row counts it. goopg returns 2000 where PG returns 1715, on a join this
change does not remove. The whole-row value of a null-extended row is not
NULL when fed to an aggregate, while `b IS NULL` is already right.

## Residuals (ledgered)

- PG also removes:
  - a LEFT JOIN to a subquery that is distinct for the join columns
    (`query_is_distinct_for`);
  - equalities to constants or cross-type operators of the index opfamily;
  - USING / NATURAL joins.
- PG 18's self-join elimination (`remove_useless_self_joins`) for INNER
  joins is not ported.
- The attribution is name-based. A precise `attr_needed` over resolved
  ColumnRefs would remove joins this pass keeps, for example when a B
  column name is shared with another table and read unqualified there.
