# M0146-0047c — a bare name is a column at any level before it is a whole row

Status: done (2026-10-03). Parent: M0146-0047 (banner item 2a descendant, S2).

## The defect

With `wd(b int)` holding 7 and `wb(k, x, y)` holding `(1,10,a)`:

| query | goopg before | PG 18.3 |
|---|---|---|
| `SELECT (SELECT b FROM wb b LIMIT 1) FROM wd` | `(1,10,a)` | `7` |
| `… LEFT JOIN LATERAL (SELECT b AS r FROM wa a JOIN wb b …) x ON true` | `(1,10,a)` | `7` |
| `SELECT wd.b FROM wd WHERE EXISTS (SELECT 1 FROM wb b WHERE b = 7)` | 42804 `operator = has incompatible operand types` | the row |

## The PG behaviour

For a one-part name, `transformColumnRef` (parse_expr.c) first calls
`colNameToVar`, which looks for a column of that name at every visible
query level, innermost first. Only when no level has one does it try
`refnameNamespaceItem`: the name as a relation, again innermost first,
giving a whole-row Var. A column of an outer query therefore beats a local
relation of the same name.

## What landed

Both resolvers had the relation fallback inside their per-level step, so a
local relation was taken before the outer levels were searched for a
column. Each now walks the levels twice:

- **Planner.** `resolveColumnRef` (internal/optimizer/planner.go) walks
  `resolveColumnRefAt` over every level for a column, then
  `resolveWholeRowAt` over every level for a relation. The whole-row arm
  moved out of `resolveColumnRefAt` unchanged, including the MERGE
  `old`/`new` and pulled-derived forms.
- **Analyzer.** `resolveColumnRefType` (internal/parser/analyzer) does the
  same with `wholeRowTypeAt`. This is the twin that raised the 42804, by
  typing `b` as a whole-row text value.

Level counting, LATERAL siblings and ambiguity handling are unchanged.

## Verification

- Live probe against PG 18.3: 12 shapes are identical. They cover:
  - outer column vs local relation, in a scalar subquery, EXISTS and
    LATERAL;
  - a relation name only, which resolves to the innermost relation;
  - a column at the same level;
  - the M0146-0047a join cases.
- `TestBareNamePrefersOuterColumnOverLocalWholeRow` pins 7 cases and fails
  at HEAD.
- Regress A/B against HEAD: rowtypes, subselect, with, merge, returning,
  create_view, updatable_views, insert_conflict, aggregates, select,
  triggers and rules are byte-identical. Two suites differ for reasons
  that are not this change:
  - `join` shows the known nondeterministic row-order flip of an unordered
    result;
  - `plpgsql` flaps between 4366 and 4369 diff lines on both the old and
    the new build.
- Gates: units, spotcheck, SF0.25 sweep 96/96, fire set (no plan change),
  TPC-H arm, ea-ratchet.

## Residuals

- M0146-0047a's whole-row column expansion decides per scope from the
  names written there; it does not consult outer levels. A sublink's bare
  `b` that now resolves to an outer column still makes the sublink's own
  relation `b` keep all its columns. That over-keeps, which is safe.
