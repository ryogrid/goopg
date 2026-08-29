# M0134-0171 — `REFERENCES <table>` with an omitted column list must resolve to the PRIMARY KEY

Status: **landed** (2026-08-29)
Case: `postgres/src/test/regress/sql/foreign_key.sql` (M0134-0171)
Related: [`m0134-0161-indimmediate-deferrable-key.md`](m0134-0161-indimmediate-deferrable-key.md)
(the other FK/constraint catalog-fidelity slice), ledger rows `0171a`–`0171d`.

## Summary

A foreign key written `REFERENCES <table>` — with no referenced-column list —
takes the referenced table's **PRIMARY KEY** as its referenced columns. goopg
resolved that default by returning the referenced table's **first column**,
which is right only when the PK happens to be column 1.

This was a silent data-integrity bug, not a cosmetic one. It produced three
different wrong answers, and the shape of goopg's existing test coverage is
exactly why it survived: every FK test used a single-column PK declared first.

## The bug

`internal/executor/operators_fk.go`, before this change:

```go
// pkColumns returns the primary-key column names for tbl by scanning its
// indexes for the one with Primary = true. Falls back to the first column.
func pkColumns(tbl *catalog.Table) []string {
	if len(tbl.Columns) > 0 {
		return []string{tbl.Columns[0].Name}
	}
	return nil
}
```

The doc comment describes an index scan the body never performed — the
function had no access to the catalog at all, only to the `*catalog.Table`.
The "falls back to" clause papered over the fact that the fallback was the
*entire* implementation.

Consequences, all three verified live against the PG 18.3 oracle:

| referenced table | goopg resolved to | effect |
|---|---|---|
| `pk1(a, b, c)`, `PRIMARY KEY (a, b)` | `{a}` | arity mismatch: N key values compared against 1 column ⇒ **every valid row rejected** with a bogus 23503 |
| `pk3(label text, id int PRIMARY KEY)` | `{label}` | FK **silently enforced against the wrong column** — `INSERT INTO fk VALUES ('aaa')` accepted against `label`, where PG rejects the constraint at DDL time as untypeable |
| `pk2(a int PRIMARY KEY, b text)` | `{a}` | accidentally correct — the only case the test suite covered |

The second row is the dangerous one: the constraint is *created* and *enforced*,
just against a different column than the one the user named. PG refuses to
create it at all:

```
ERROR:  foreign key constraint "cf" cannot be implemented
DETAIL:  Key columns "x" of the referencing table and "id" of the referenced
         table are of incompatible types: text and integer.
```

The `id` in that DETAIL is proof upstream resolved the PK, not column 1.

## Upstream

PostgreSQL resolves the omitted list **once, at constraint-definition time**,
in `ATAddForeignKeyConstraint` (`postgres/src/backend/commands/tablecmds.c:10190`):

```c
if (fkconstraint->pk_attrs == NIL)
{
    numpks = transformFkeyGetPrimaryKey(pkrel, &indexOid,
                                        &fkconstraint->pk_attrs,
                                        pkattnum, pktypoid, pkcolloid,
                                        opclasses, &pk_has_without_overlaps);
}
```

`transformFkeyGetPrimaryKey` (`tablecmds.c:13382`) walks
`RelationGetIndexList`, takes the index with `indisprimary && indisvalid`, and
builds the attribute list "from the indkey definition". Note it writes the
resolved list back into `fkconstraint->pk_attrs` — so from that point on the
default is *concrete*, and `pg_constraint.confkey` is always populated.

goopg instead models the default lazily: `catalog.ForeignKey.RefColumns` is
left empty (`// empty = use parent PK`) and each consumer re-resolves it. That
is a legitimate design choice, but it only works if the shared resolver is
correct — and it puts the same predicate in five call sites plus the catalog
row builder, which is the *sibling paths must agree* hazard this project keeps
re-encountering.

## The fix

`pkColumns` now performs the index scan its comment always claimed, via the
`IndexesOnTable` accessor already on the catalog interface:

```go
func pkColumns(ctx *Context, tbl *catalog.Table) []string {
	if ctx == nil || ctx.Catalog == nil || tbl == nil {
		return nil
	}
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		if idx.Primary {
			return append([]string(nil), idx.Columns...)
		}
	}
	return nil
}
```

`Index.Columns` is an ordered slice, so multi-column PKs come back in
index-key order (`PRIMARY KEY (b, a)` ⇒ `{b, a}`), which is what upstream's
indkey ordering gives. A table with no primary key now returns `nil` instead
of its first column.

All five consumers in `operators_fk.go` (the referencing-side INSERT/UPDATE
check, the referenced-side DELETE/UPDATE cascade paths at `:177`/`:343`, the
still-referenced DETAIL builder at `:802`, and the detached-partition scan at
`:1138`) thread `ctx` through; no other behaviour changed.

### Why the fix is at the runtime resolver, not at DDL time

The upstream-faithful placement would be DDL time — resolve once, store a
concrete `confkey`. goopg cannot do that at the FK construction sites as they
stand: `execCreateTable` registers foreign keys at
`internal/executor/operators_ddl.go:3758`/`:3795`, but creates the table's
**primary-key index at `:4024`** — afterwards. A self-referencing FK
(`CREATE TABLE t (id int PRIMARY KEY, parent int REFERENCES t)`) would
therefore resolve against a table that has no PK index yet and spuriously
fail. Keeping resolution lazy sidesteps the ordering entirely, and the
`SelfReferencingFK` subtest pins that behaviour.

Moving to the upstream model means adding a resolution pass *after* index
creation; that is ledgered as **0171a** rather than smuggled into this change,
because it also changes `pg_constraint.confkey` from `{}` to concrete values
and so needs its own catalog A/B.

## Results

`foreign_key.sql`: **3490 → 3343 diff lines** (−147), `+ERROR` **279 → 253**
(−26), `-ERROR` 155 → 154.

14-case regress A/B against a HEAD worktree (`foreign_key`, `constraints`,
`alter_table`, `inherit`, `insert`, `update`, `delete`, `truncate`,
`create_table`, `indexing`, `identity`, `insert_conflict`, `privileges`,
`triggers`): **13 byte-identical, zero regressions**; only the target case
moved.

One bucket rose, `update or delete on table … violates foreign key constraint`
1 → 2, and it is the expected shape of progress. The new occurrence is
upstream's block at `foreign_key.sql` headed *"Test a primary key with
attributes located in later attnum positions compared to the fk attributes"*
— `pktable2(a,b,c,d,e)` with `PRIMARY KEY (d, e)`, i.e. precisely this bug
class. Before the fix the whole block ran against column `a` and produced
garbage; now `insert into fktable2 values (4, 5)` succeeds and
`delete from pktable2` correctly raises the referenced-side error. The residual
divergence there is only the auto-generated constraint *name*
(`fktable2_d_fkey` vs PG's `fktable2_d_e_fkey`) — a separate root cause,
ledgered as **0171c**.

## Guard

`internal/executor/operators_fk_omitted_refcolumns_test.go`,
`TestFKOmittedRefColumnsResolveToPrimaryKey`: multi-column PK (accept valid /
reject partial-match), PK-not-first-column (accept the real PK value / reject a
non-key value), self-referencing FK, and a direct `pkColumns` unit check
including PK-key ordering and the no-PK `nil`.

Revert-checked: restoring the `tbl.Columns[0]` body fails `MultiColumnPK`,
`PrimaryKeyNotFirstColumn` and `PkColumnsHelper`. `SelfReferencingFK` passes
either way — it is the accidentally-correct PK-is-column-1 case, and it is in
the test precisely to pin the DDL-ordering constraint described above.

## Deferred (ledger)

- **0171a** — resolve the omitted list at DDL time so `pg_constraint.confkey`
  is concrete (`{}` today) and matches upstream; needs a resolution pass after
  PK-index creation in `execCreateTable`.
- **0171b** — `there is no primary key for referenced table "%s"` (42704,
  `tablecmds.c:13437`) is not raised; goopg accepts `REFERENCES t` against a
  PK-less table.
- **0171c** — multi-column FK auto-naming: goopg uses `<table>_<firstcol>_fkey`,
  PG joins every key column (`<table>_<col1>_<col2>_fkey`).
- **0171d** — `ON DELETE RESTRICT` reports the generic "violates foreign key
  constraint" message; PG distinguishes "violates RESTRICT setting of foreign
  key constraint".

The case remains `failed` and is PARKED: its dominant remaining buckets are
113 cascaded `relation does not exist` plus the partitioned-FK matrix, which
are REFACTOR-tier.
