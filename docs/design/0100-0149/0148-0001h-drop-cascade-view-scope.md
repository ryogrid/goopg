# M0148-0001h — DROP TABLE … CASCADE drops only the views over the dropped table

**Parent:** M0148-0001 `aggregates.sql` regress coverage closure · **Status:**
slices 1\-3 landed 2026\-11\-03; closed (residue in M0148\-0001h1)
**Target:** the aggregates hunk at `drop table t1 cascade` (expected\-line
~1302)

## Why

The task text had the hunk reversed. PG prints only `NOTICE:  drop cascades
to table t1c`; goopg additionally reported, and **dropped**, eight views that
`create_view.sql` (run before aggregates as the runner's prerequisite) had
created over a *different* `t1` in schema `testviewschm2`:

```
NOTICE:  drop cascades to 8 other objects
DETAIL:  drop cascades to view nontemp1 … view temporal4
```

That is a data\-loss defect: dropping one table removed unrelated objects. PG
records a view's dependencies by OID when the view is created
(`DefineView` → `StoreViewQuery` → `recordDependencyOnExpr`, pg\_depend) and
walks them by OID on DROP (`findDependentObjects`, dependency.c). goopg finds
a table's dependent views at DROP time by matching the relation **name** in
each stored view query (`viewsDependingOnTable`).

## Causes

1. The CASCADE path passed the raw parsed name. An unqualified `drop table
   t1 cascade` has an empty schema, which the schema filter read as "any
   schema". (The RESTRICT path already scanned by the resolved table identity,
   M0134\-0069.)
2. The catalog spells `public` as `""`, so even the resolved identity of a
   public table disabled the filter; and an explicitly qualified reference
   (`vs2.t1`) was compared by name only.
3. CREATE VIEW under `search_path = s, public` places the view in `public`,
   not `s` (PG: `RangeVarGetCreationNamespace` — the first search\-path schema).
   CREATE TABLE already pre\-resolves this (`currentWritableSchema`).
4. A temp table carries the public\-equivalent empty schema, so dropping a temp
   `t1` matches public views over an unqualified `t1`.

Causes 3 and 4 are what the aggregates hunk still shows.

## Slice 1 — CASCADE scope (landed `73b858090`)

- The CASCADE path passes the resolved identity (`tbl.Schema`, `tbl.Name`) to
  `viewsDependingOnTable` and `matViewsDependingOnRelation`, as RESTRICT does.
- `viewsDependingOnTable` uses `selectRefsRelation`: an explicitly qualified
  reference matches only when its schema is the dropped table's
  (`normViewDepSchema` maps `""` to `public`); an unqualified reference keeps
  the previous rule (any view when the table is in public, else views in the
  table's schema). This only removes false matches; it never drops a match the
  old rule made for an unqualified reference.
- Pin: `TestDropTableCascadeLeavesOtherSchemasViews` (fails before the fix:
  `drop table t1 cascade` on public.t1 dropped `vs2.v_other` over `vs2.t1`).
- Gates on one code\_tree (8d2a3340fd7a): units, tpch\-spotcheck, TPC\-DS
  SF0.25 sweep (99), TPC\-H acceptance arm (24 MATCH), fire\-set (no query
  fired on any corpus).

## Slice 2 — view placement and schema drops (2026\-11\-03)

Slice 1 regressed the must\-pass regress case `create_function_sql`. Its
`CREATE VIEW functestv3` under `search_path = temp_func_test, public` landed
in public, so the schema\-scoped match no longer linked it to
`temp_func_test.functest3`. The case passed at b11a5eca0; slice 1's gates
did not include `TestPort_RegressSuite`.

- `execCreateView` places an unqualified view in the creation namespace
  (`currentWritableSchema`, DefineView → RangeVarGetCreationNamespace), as
  CREATE TABLE does. The exception is a view whose FROM items resolve to a
  temporary relation: PG makes that view temporary (view.c
  `isQueryUsingTempRelation`), and goopg keys temporary relations bare, so
  it stays unqualified.
- The DROP TABLE CASCADE notice names a dependent view or matview
  unqualified when its schema is on the search path (`dropCascadeObjectName`,
  getRelationDescription's RelationIsVisible rule): `view functestv3`, not
  `view temp_func_test.functestv3`.
- `DROP SCHEMA … CASCADE` unregisters the namespace after dropping its
  members (dependency.c deletes the dependents first). The member views used
  to be dropped after the schema was gone, so the IF EXISTS guard reported
  `schema "s" does not exist, skipping` and left each view behind.
  - The member\-view loop shares one dropped set.
  - A listed table name that no longer resolves is skipped. A table renamed
    after ALTER … SET SCHEMA is reported under a stale name; that is
    ledgered.
- `DROP VIEW` resolves an unqualified name through search\_path
  (`resolveRelationBySearchPath`, RangeVarGetRelid order: a temporary
  relation, then each path schema).
- Pins: `TestCreateViewPlacedBySearchPath`, which covers placement and the
  temp exception; `TestDropSchemaCascadeDropsItsViews`. Both are
  mutation\-checked.
- Regress\-runner diff lines, HEAD → change:

  | suite | before | after |
  |---|---|---|
  | create\_view | 2501 | 2467 |
  | create\_function\_sql | 272 | 242 |
  | alter\_table | 3718 | 3679 |
  | aggregates | 251 | 247 |

  rules, matview, with, select\_views and namespace are unchanged.
  `TestPort_RegressSuite` and `TestPort_PgDump*` pass.

## Slice 3 — temporary views are session\-scoped (2026\-11\-03)

- `execCreateView` marks a view temporary when it is declared TEMP, or when
  it is unqualified and its FROM items resolve to a temporary relation. PG
  makes the latter implicitly temporary and emits
  `NOTICE: view "v" will be a temporary view` (view.c DefineView).
- A temporary view gets `Temp`/`TempOwner`, so backend exit
  (`cleanupSessionTempObjects` → `DropSessionTempObjects`,
  RemoveTempRelations) drops it with the session's temp tables.
- create\_view.sql's `temporal1..4` therefore no longer outlive their
  session, and aggregates.sql's `drop table t1 cascade` hunk is gone:
  aggregates goes from 11 to 10 hunks and 247 to 231 diff lines, and
  create\_view from 2467 to 2427. The other eight measured suites are
  unchanged.
- Pin: `TestImplicitTempViewIsSessionScoped`, which covers the NOTICE, the
  explicit TEMP view, the Temp stamp and session\-exit cleanup. It is
  mutation\-checked.

## Closure

The aggregates hunk this task names is matched. The remaining fidelity
items move to M0148\-0001h1:

- A temporary view keeps goopg's bare key; PG places it in the session's
  pg\_temp namespace.
- `DROP SCHEMA … CASCADE` still does not print PG's dependency\-ordered
  cascade list.
- Other unqualified view lookups (ALTER VIEW, COMMENT ON VIEW, GRANT) do not
  go through `resolveRelationBySearchPath` yet.
- Temp tables in a temp namespace identity for dependency matching.
- The faithful model: record the view's referenced relation OIDs at CREATE
  VIEW (persisted) and match by OID in `viewsDependingOnTable` /
  `matViewsDependingOnRelation`; `functionsDependingOnTable` keeps an
  unscoped name match too.
