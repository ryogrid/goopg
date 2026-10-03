# M0146-0047a — a whole-row reference reads every column of its relation

Status: done (2026-10-03). Parent: M0146-0047 (banner item 2a descendant, S2).

## The defect

`SELECT a.id, b FROM wa a JOIN wb b ON b.k = a.k` returned `(1,,)`; PG
18.3 returns `(1,10,a)`. Every whole-row reference read across a join lost
the columns the query never named:

- `SELECT a, b.x …` gave `(,1)`;
- `GROUP BY b` and a scalar subquery reading `b` lost them too;
- so did a pulled-up derived table selecting `b`.

`SELECT b FROM wb b` alone was right.

## Cause

The join search narrows each join input to the columns the rest of the
query reads. That set is collected from the statement AST by column name
(`neededColumnNames` / `outputColumnNames`, pathindexonlyneed.go). A
whole-row reference is the bare relation name `b`. The collector records
it like any unqualified name, the name matches none of `wb`'s columns, and
the narrowed input kept only the join key. The RowExpr built above the join
then read padded NULLs.

## The PG behaviour

A whole-row reference is a Var with `varattno = 0`. `pull_varattnos` /
`build_base_rel_tlists` turn it into `attr_needed` for every attribute of
the relation, so nothing is projected away below it.

`transformColumnRef` resolves a bare name as a column first: `colNameToVar`
searches every visible query level. Only when no column matches does it
take the name as a relation (`refnameNamespaceItem`).

## What landed

`expandWholeRowColumnNames` (pathindexonlyneed.go) runs right after the two
sets are built in `planSelectWithSettings`, next to
`addPulledBodyColumnNames`. For each relation it decides to expand, every
column is added to both sets, in two forms:

- as a qualified read (`neededQualKey`, which `neededColumnNamedFor`
  consults);
- as the plain name (which `neededKeepSet` matches).

The sets carry no scope, so the decision is taken per scope, from the names
written in that scope:

- **The statement.** Its own references count (the collectors do not
  descend into FROM subqueries; sublinks are included), together with the
  columns of its own FROM relations.
- **Each pulled-up derived body.** Only the names collected from the body
  itself count, together with the columns of the body's own FROM
  relations. A non-LATERAL derived table does not see its parent's FROM
  list.

A relation is expanded when its qualifier (alias, else table name) is
written bare in its scope and no relation of that scope has a column of
that name. The column test follows transformColumnRef's precedence.

A derived table's output names come from `pulledDerived` or its column
aliases. A source whose names are unknown contributes none, which can only
expand more: over-keeping is the safe direction of the collectors.

Two drafts were rejected on the way:

- A scope-blind version, which expanded any bare name matching any FROM
  relation, widened TPC-H Q9's narrowing; `TestSlice3LiveQ9ShapeDerivation`
  caught it. Q9's outer `nation` is the derived table's column, while its
  body reads a relation named `nation` that the body never names bare.
- A version that let the body see the parent's FROM columns passed Q9 for
  the wrong reason. It would under-keep a body's whole-row `b` next to a
  parent column `b`, which is now a test case.

The same expansion covers the index-only producer (`neededColumnsOfRel`).
A whole-row reader of a relation can no longer be offered an index-only
scan that lacks its other columns.

## Verification

- Live probe against PG 18.3: 15 whole-row shapes are identical. They
  cover:
  - inner and outer joins, aliased and unaliased references, either side;
  - GROUP BY, ORDER BY, scalar subquery, EXISTS;
  - derived tables, pulled up and not;
  - a column named like a joined table (`wc.wb`, where the column wins);
  - a parent column `b` beside a body's whole-row `b`.
- `TestWholeRowReadAcrossJoinKeepsEveryColumn` pins 9 cases.
- Regress A/B against HEAD: rowtypes, subselect, aggregates, create_view,
  groupingsets, window, inherit, select_distinct and union are
  byte-identical. In `join`, one row of an unordered result flips position
  between runs, in both directions across two A/B pairs; that is
  nondeterministic and not this change.
- Gates: units, spotcheck, SF0.25 sweep 96/96, fire set (no plan change at
  SF0.25 or SF1), TPC-H arm, ea-ratchet.

## Residuals

- The null-extended row of a LEFT JOIN to a relation with no NOT NULL
  column still prints `(,,)` where PG prints NULL. This is the witness
  limitation already ledgered under M0146-0047.
- Composite field selection `(expr).field` — `(b).x`, `(ROW(1,2)).f1`,
  `(c).p` — is a syntax error in goopg. Filed as M0146-0047b.
- **M0146-0047c (S2, filed, not worked).** The resolver prefers a local
  whole-row reference over an outer-level column. `SELECT (SELECT b FROM
  wb b LIMIT 1) FROM wd`, where `wd` has a column `b`, gives `(1,10,a)`;
  PG gives `7`, because colNameToVar searches every level for a column
  before taking a relation name. The LATERAL form is wrong the same way.
  This is pre-existing and does not involve narrowing: the expansion only
  ever keeps more columns.
- The expansion does not consult outer query levels. Where goopg currently
  reads a whole row, it keeps every column, which stays consistent with
  the resolver until M0146-0047c lands.
