# M0148\-0001j1 — ordered aggregates under grouping sets; DROP TYPE dependents

**Parent:** M0148\-0001j · **Status:** landed 2026\-11\-03; closed (residue
in M0148\-0001j2)

## Divergence

| case | PG | goopg (before) |
|---|---|---|
| `select ten, count(distinct s) from gt group by rollup(ten)` | `GroupAggregate` with `Group Key: ten` and `Group Key: ()` over `Sort` | `MixedAggregate`, `Hash Key: ten` |
| `… group by cube(ten, three)` with `array_agg(s order by s)` | sorted GroupAggregate, with the second rollup re\-sorting its input | hashed |
| `DROP TYPE st` while functions use `st` | 2BP01 listing the dependents | the type dropped, its functions left behind |
| `DROP TYPE st CASCADE` | `drop cascades to 5 other objects`, functions and aggregate dropped | only the type dropped; a re\-CREATE failed "function already exists" |
| `DROP TYPE person_type RESTRICT` with typed tables `OF person_type` | 2BP01 naming the tables | the type dropped |

## PostgreSQL

- **No hashing.** `create_grouping_paths` sets `GROUPING_CAN_USE_HASH`
  only with `numOrderedAggs == 0`. The rule holds for grouping sets as
  well, so `consider_groupingsets_paths` builds only the sorted rollups,
  with no AGG\_MIXED path.
- **pg\_depend edges** (`ProcedureCreate`, `AggregateCreate`,
  `heap_create_with_catalog`):
  - a function depends on each argument type and its result type;
  - an array type depends internally on its element type;
  - an aggregate depends on its argument and result types and on its
    transition, final, combine and serialization functions, but not on its
    state type;
  - a typed table depends on its type.
- **Traversal.** `findDependentObjects` visits each object's dependents in
  descending OID order (`object_address_comparator`), depth first. It
  appends each object after its dependents, recording the object it came
  from. `reportDependentObjects` reports the list back to front, skipping
  the original target and internal dependents. RESTRICT gives 2BP01 with
  DETAIL lines `X depends on Y`. CASCADE gives one `drop cascades to X`
  notice, or a count with a DETAIL list.

## Change

- **Hash gate** (`groupingpaths.go`).
  - The sorted\-rollup arm (M0146\-0020a/b) now admits ordered aggregates
    and offers no Mixed path for them.
  - `groupingSetsHashable` keeps the hashed grouping\-sets path for an
    ordered aggregate only when no sorted rollup exists, so the grouping
    always has a path.
  - The partial\-aggregate gathered arm uses the same gate.
- **DROP TYPE dependents** (`executor/drop_type_dependents.go`, called from
  `execDropType` for enum, composite and range types).
  - `typeDependentObjects` rebuilds the edges from the routine registry,
    the user aggregates and `Table.OfTypeOID`, and runs PG's walk.
  - Argument types are matched by OID, or by name when the OID is 0
    (`t[]` and `_t` spellings included).
  - `dropTypeDependents` raises 2BP01 or reports, then drops in deletion
    order: aggregates through `DropUserAggregate` plus the catalog rows,
    functions through `DropRoutine` plus WAL, typed tables through
    `dropTableByRef`.

## Verification

- `TestGroupingSetsOrderedAggRunsSorted`: the ROLLUP plan verbatim, the
  CUBE plan not hashed, and the values.
- `TestDropTypeDependents`: PG's RESTRICT DETAIL and CASCADE DETAIL
  verbatim, and re\-create after CASCADE.
- Probes against a scratch PG 18.3 were identical:
  - ROLLUP, CUBE, GROUPING SETS with `mode()`, and disjoint sets;
  - composite type with an array argument, a return type and an
    aggregate; enum; range.
- Regress runner, HEAD → change, 14 suites:
  - groupingsets: 1653 → 1614;
  - typed\_table: 135 → 133 after typed tables were added;
  - plpgsql moved on reruns of an unchanged build (a known flapping suite);
  - the other 11 suites are byte\-identical.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass. The TPC gates show no plan change.

## Remaining (M0148\-0001j2)

- **Other dependents.** Columns of the type in ordinary tables (`drop
  cascades to column x of table t`), composite types with attributes of
  the type, and domains over it are not reported or dropped. The enum and
  domain paths keep their own column checks.
- **OID order.** goopg's routine OIDs come from a counter separate from
  relation OIDs, so a mix of tables and functions reports in a different
  order than PG (typed\_table).
- **pg\_proc.** `pg_proc.proargtypes` holds 0 for a composite\-array
  argument (`st[]`).
- **Unrelated gaps seen while probing.** A SQL function using `(s).total`
  on a composite parameter errors "column notation applied to type
  unknown". `CREATE TABLE … OF t (name WITH OPTIONS …)` with a repeated
  column is accepted where PG raises 42701.
- **Grouping\-sets shapes.** Grouping sets that form no rollup the sorted
  arm can run still hash with an ordered aggregate (PG would sort or
  error).
