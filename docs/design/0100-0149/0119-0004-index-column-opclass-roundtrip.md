# 0119-0004 — per-column operator class round-trip in pg_dump (DU-002 slice 312)

**Milestone:** M0119-0004 (pg_dump 002–010 catalog-view parity battery; source M0110-0001)
**Status:** accepted / implemented
**Oracle:** PostgreSQL 18.3 (`./postgres/local_install`), `pg_dump --no-sync`

## Problem

A B-tree index column may declare a non-default *operator class*, e.g.
`CREATE INDEX ON t (a text_pattern_ops)`. The opclass selects the comparison
semantics the index supports: `text_pattern_ops` orders by raw byte value (so the
index can drive `LIKE 'prefix%'` / prefix-range scans), whereas the default
`text_ops` orders by the collation. Two indexes that differ only in opclass are
functionally distinct, so the opclass is a real, restorable property.

PG carries it through `pg_index.indclass` and re-emits it in
`pg_get_indexdef_worker` (ruleutils.c) via `get_opclass_name`, which prints the
opclass **after** the column/expression (and after any `COLLATE` clause) and
**before** the `ASC`/`DESC` ordering — and *suppresses* the column type's default
opclass so a plain index dumps unchanged:

```
... USING btree (a text_pattern_ops, b)
                   ^^^^^^^^^^^^^^^^   non-default opclass emitted
                                  ^   default opclass on b suppressed
```

pg_dump emits each index via `pg_get_indexdef(oid)` verbatim, so the opclass
rides along automatically — *if* the server reproduces it.

goopg did **not** surface the opclass at all. `parseIndexColumnList`
(`internal/parser/ddl.go`) already *consumed* the bare opclass ident (it had to,
to reach the trailing `ASC`/`DESC`/`NULLS` modifiers) but **discarded** the name —
only the rare opclass-*with-options* form was retained, for an execution-time
rejection. `catalog.Index` had no per-column opclass field, and `BuildIndexDef`
never emitted one. So `CREATE INDEX … (a text_pattern_ops)` dumped as a plain
`(a)`, silently widening the index back to the default opclass on restore — a
semantic change (the restored index can no longer back the prefix-range scans the
original supported).

## Fix

Thread the explicit opclass name end-to-end, parallel to the existing
ASC/DESC ordering (`ColDescending`/`ColNullsFirst`). Four sites:

1. **AST** (`internal/parser/ast.go`): new `IndexColOrder.OpClass string` (empty =
   default opclass). `IndexColOrder` is already the per-column carrier the parser
   builds, so the opclass lands beside the ordering it precedes.
2. **Parser** (`internal/parser/ddl.go`, `parseIndexColumnList`): capture the
   bare-ident opclass name (previously discarded) into a local and assign it to
   `order.OpClass`. The opclass-with-options path is unchanged.
3. **Catalog** (`internal/catalog/catalog.go`): new `Index.ColOpClasses []string`
   (parallel to `Columns`), mirroring `pg_index.indclass`.
4. **Executor** (`internal/executor/operators_ddl.go`): a new `indexHasOpClass`
   guard ORs into the existing "store non-default index metadata" condition; when
   any column has an explicit opclass, copy `ColOrders[i].OpClass` into
   `idx.ColOpClasses`.
5. **Deparse** (`internal/catalog/catalog.go`, `BuildIndexDef`): emit
   ` <opclass>` after the column/expression and before the `ASC`/`DESC` clause,
   matching the upstream byte order, for any non-empty `ColOpClasses[i]`.

### Default-opclass suppression

PG suppresses the *default* opclass even when the user wrote it explicitly
(it compares `indclass` against the type's default). goopg records only an
explicitly-written opclass string and emits it unconditionally — so a non-empty
`ColOpClasses` entry is, in practice, non-default and correctly emitted, while a
plain column stores `""` and emits nothing. The one residual divergence (a user
who *explicitly* writes the default opclass, e.g. `(a text_ops)`, would see it
re-emitted where PG drops it) is the same latent edge the partition-key opclass
path already accepts (`PartitionKeyOpClasses`, slice 300) and is out of scope
here; faithfully suppressing it needs a default-opclass-per-type table.

## Blast radius

A plain index (no explicit opclass — the overwhelming majority) stores an empty
`ColOpClasses` and is byte-unchanged in both the catalog and the def string. The
parser already consumed the opclass token, so no previously valid statement
changes parse behaviour; the only change is that the name is now *retained* rather
than dropped. No WAL/dump-format change. goopg builds every index with its default
B-tree comparison regardless of the declared opclass, so this is dump-fidelity
only — the restored index carries the correct opclass for the day opclass-aware
scans land.

## Verification

* New **DU-002 slice 312** in `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`):
  `CREATE INDEX opcidx_pat ON public.opcidx (a text_pattern_ops, b)`; the dump now
  emits `CREATE INDEX opcidx_pat ON public.opcidx USING btree (a text_pattern_ops, b);`,
  asserted as a substring of real pg_dump 18.3's stdout (the second column `b`
  confirms the default opclass stays suppressed). PASS (5.1 s).
* New unit `TestBuildIndexDefColOpClass` (`internal/catalog/index_def_opclass_test.go`):
  pins opclass emission, its position relative to `DESC`/`NULLS`, the mixed
  default/non-default composite case, and the empty-slice no-op.
* New unit `TestParseCreateIndexColOpClass` (`internal/parser/ddl_test.go`):
  asserts the opclass lands on `ColOrders[i].OpClass`, coexisting with `COLLATE`
  and `DESC`.
* `go test ./internal/parser/ ./internal/catalog/ ./internal/executor/` PASS;
  `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
* `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

* pg_dump 002–010 catalog-view parity battery (further slices) — candidates:
  per-column `COLLATE` on an index column/expression (a sibling of this slice;
  `BuildIndexDef` emits no `COLLATE` yet), comment round-trip on more object
  kinds, GENERATED STORED expression edge cases.
* extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement).
