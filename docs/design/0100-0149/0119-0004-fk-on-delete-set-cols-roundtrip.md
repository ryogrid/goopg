# 0119-0004 — `ON DELETE SET NULL|DEFAULT (column_list)` round-trip in pg_dump (DU-002 slice 311)

**Status:** accepted
**Milestone:** M0119-0004 (also advances M0110-0001 pg_dump TAP `002–010` via the
self-promoting `TestPort_PgDumpConnectionSetup` guard, CSV row DU-002).

## Problem

PostgreSQL 15 added the ability to restrict an `ON DELETE SET NULL` /
`ON DELETE SET DEFAULT` referential action to a **subset** of the foreign key's
referencing columns:

```sql
ALTER TABLE child
  ADD CONSTRAINT child_fk FOREIGN KEY (a, b)
  REFERENCES parent (x, y) ON DELETE SET NULL (b);
```

When a referenced parent row is deleted, only the listed columns (`b`) are set
to NULL; the others (`a`) keep their value. The subset is stored in
`pg_constraint.confdelsetcols` (an `int2[]` of referencing-column attnums; NULL
when the whole key is affected).

`pg_get_constraintdef_worker` renders the list with
`decompile_column_index_array`, appending ` (col, …)` **after** the
`ON DELETE SET NULL` keyword (`ruleutils.c:2376`):

```c
val = SysCacheGetAttr(CONSTROID, tup, Anum_pg_constraint_confdelsetcols, &isnull);
if (!isnull) {
    appendStringInfoString(&buf, " (");
    decompile_column_index_array(val, conForm->conrelid, false, &buf);
    appendStringInfoChar(&buf, ')');
}
```

so `pg_dump` re-emits `… ON DELETE SET NULL (b);`.

**The gap:** goopg's `parseFKAction` consumed the `SET NULL` / `SET DEFAULT`
keywords but **never** the optional trailing column list. The list was silently
dropped, so a column-restricted action degraded on restore into a **whole-key**
`SET NULL` — a *semantic* change (the other FK columns would also be nulled on a
parent delete), not just a cosmetic one.

## Fix

Thread the column list end-to-end, mirroring the existing `MATCH FULL` slice
(309) plumbing:

- **Parser (`internal/parser/ddl.go`):** `parseFKAction` now returns
  `(FKAction, []string, error)`. After `SET NULL` / `SET DEFAULT` it parses an
  optional `( col_list )` via `parseColumnNameList`. The PG grammar permits the
  list after either `ON UPDATE` or `ON DELETE`; PG rejects it for `ON UPDATE` in
  parse-analysis, which the three callers mirror by recording the columns only
  on the `isDelete` branch.
- **AST (`internal/parser/ast.go`):** new `OnDeleteSetCols []string` on
  `ColumnDef`, `TableForeignKeyDef`, and `AlterTableAction`.
- **Catalog (`internal/catalog/catalog.go`):** new
  `catalog.ForeignKey.OnDeleteSetCols []string`. The `pg_constraint` virtual
  builder now projects `confdelsetcols` (row[23], `int2[]`): the referencing
  columns' attnums via the existing `colOrd` map, or NULL/empty when the action
  covers the whole key.
- **Executor (`internal/executor/operators_ddl.go`):** the three
  `catalog.ForeignKey{…}` build sites (inline column FK, table-level FK, ALTER
  TABLE ADD FK) copy the field.
- **Deparse (`internal/executor/expr.go`):** `buildForeignKeyDefString`
  (goopg's `pg_get_constraintdef`) appends ` (cols)` after the `ON DELETE`
  clause when the action is `SET NULL` / `SET DEFAULT` and `OnDeleteSetCols` is
  non-empty.

Dump-fidelity slice: goopg's FK enforcement does not yet apply the column-subset
SET-NULL/DEFAULT at parent-delete time (it stamps the whole row); that is a
separate runtime gap. The catalog and dump representations are now faithful.

## Blast radius

Nil outside column-restricted SET NULL/DEFAULT FKs. `OnDeleteSetCols` defaults
empty for every existing FK; the deparse append and the `confdelsetcols`
projection are gated on a non-empty list, so a plain FK (including the existing
slice-52 `ON DELETE SET NULL`) is byte-identical. TPC-H/pgbench carry no such
constraint.

## Gates

- New **DU-002 slice 311** in `TestPort_PgDumpConnectionSetup`
  (`sfk_child_fk … REFERENCES public.sfk_ref(id) ON DELETE SET NULL (b);`)
  asserted vs **real pg_dump 18.3** — PASS (4.65 s).
- New unit `TestForeignKeyOnDeleteSetColsRoundTrip`
  (`internal/executor/operators_fk_constraintdef_test.go`): parse →
  `OnDeleteSetCols=[b]`, `pg_constraint.confdelsetcols={2}`, deparse appends
  ` (b)`; a plain `ON DELETE SET NULL` control keeps confdelsetcols NULL and no
  suffix — PASS.
- `internal/parser` + `internal/catalog` + full `internal/executor` suites —
  PASS.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

pg_dump 002–010 catalog parity (further slices); extended-protocol commit-time
deferral; the runtime column-subset SET NULL/DEFAULT enforcement.
