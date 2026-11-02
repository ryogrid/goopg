# M0148-0001h — DROP TABLE … CASCADE drops only the views over the dropped table

**Parent:** M0148-0001 `aggregates.sql` regress coverage closure · **Status:**
slice 1 landed 2026\-11\-03 in `73b858090`; the task stays open
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

## Remaining (task stays open)

- Slice 2: CREATE VIEW creates an unqualified non\-temp view in the first
  writable search\-path schema (mirror CREATE TABLE's `currentWritableSchema`
  pre\-resolution in `execCreateView`), so create\_view.sql's views live in
  `testviewschm2`.
- Temp tables in a temp namespace identity for dependency matching.
- The faithful model: record the view's referenced relation OIDs at CREATE
  VIEW (persisted) and match by OID in `viewsDependingOnTable` /
  `matViewsDependingOnRelation`; `functionsDependingOnTable` keeps an
  unscoped name match too.
