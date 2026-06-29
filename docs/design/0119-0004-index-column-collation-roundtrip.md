# 0119-0004 — per-column COLLATE round-trip in pg_dump (DU-002 slice 313)

**Milestone:** M0119-0004 (pg_dump 002–010 catalog-view parity battery; source M0110-0001)
**Status:** accepted / implemented
**Oracle:** PostgreSQL 18.3 (`./postgres/local_install`), `pg_dump --no-sync`

## Problem

A B-tree index column may declare a non-default *collation*, e.g.
`CREATE INDEX ON t (a COLLATE "C")`. The collation selects the sort/comparison
ordering the index supports: `"C"` orders by raw byte value, whereas the type's
default (database) collation orders by locale rules. An index built on a
non-default collation can back only queries using that same collation, so the
collation is a real, restorable property.

PG carries it through `pg_index.indcollation` and re-emits it in
`pg_get_indexdef_worker` (ruleutils.c) via `generate_collation_name`, which prints
the collation **after** the column/expression and **before** the operator class
(and before `ASC`/`DESC`), and *suppresses* the type's default collation so a plain
index dumps unchanged:

```
... USING btree (a COLLATE "C", b)
                   ^^^^^^^^^^^^   non-default collation emitted (quoted ident)
                              ^   default collation on b suppressed
```

`pg_get_indexdef` quotes the collname as an identifier, so the pg_catalog
collation `C` prints as `"C"` (uppercase ⇒ must quote).

pg_dump emits each index via `pg_get_indexdef(oid)` verbatim, so the collation
rides along automatically — *if* the server reproduces it.

goopg did **not** surface the collation. `parseIndexColumnList`
(`internal/parser/ddl.go`) already *consumed* the `COLLATE <name>` clause (it had
to, to reach the trailing opclass/`ASC`/`DESC`/`NULLS` modifiers) but **discarded**
the name. `catalog.Index` had no per-column collation field, and `BuildIndexDef`
emitted none. So `CREATE INDEX … (a COLLATE "C")` dumped as a plain `(a)`,
silently widening the index back to the default collation on restore — a semantic
change (the restored index can no longer back the byte-ordered scans the original
supported).

## Fix

Thread the explicit collation name end-to-end, exactly parallel to the per-column
operator class landed in the sibling slice 312
(`0119-0004-index-column-opclass-roundtrip.md`). Five sites:

1. **AST** (`internal/parser/ast.go`): new `IndexColOrder.Collation string` (empty =
   default collation), beside the existing `OpClass`.
2. **Parser** (`internal/parser/ddl.go`, `parseIndexColumnList`): replace the
   `_ = p.advance()` that discarded the collation name with `parseCollationName()`
   (accepts an optional schema qualifier `pg_catalog."C"`, returns the trailing
   component — matching `pg_collation.collname`) and assign it to `order.Collation`.
   The collation is captured *before* the opclass, matching the grammar order.
3. **Catalog** (`internal/catalog/catalog.go`): new `Index.ColCollations []string`
   (parallel to `Columns`), mirroring `pg_index.indcollation`.
4. **Executor** (`internal/executor/operators_ddl.go`): a new `indexHasCollation`
   guard ORs into the existing "store non-default index metadata" condition; when
   any column has an explicit collation, copy `ColOrders[i].Collation` into
   `idx.ColCollations`.
5. **Deparse** (`internal/catalog/catalog.go`, `BuildIndexDef`): emit
   ` COLLATE <quoted>` after the column/expression and **before** the operator
   class, for any non-empty `ColCollations[i]`. A new `quoteCollationIdent` helper
   reproduces `quote_identifier`: a name is left bare only when it would re-parse
   as itself (lowercase letter/underscore start, only lowercase/digit/underscore
   thereafter), otherwise double-quoted with embedded quotes doubled — so `C`/
   `POSIX` print as `"C"`/`"POSIX"` while `ucs_basic` stays bare, matching pg_dump.

### Default-collation suppression

PG suppresses the *default* collation by comparing `indcollation[i]` against the
column type's `typcollation`. goopg records only an explicitly-written collation
string and emits it unconditionally — so a non-empty `ColCollations` entry is, in
practice, non-default and correctly emitted, while a plain column stores `""` and
emits nothing. The one residual divergence (a user who *explicitly* writes the
default collation would see it re-emitted where PG drops it) is the same latent
edge the opclass path (slice 312) and column-level COLLATE path (slice 188)
already accept; faithfully suppressing it needs a default-collation-per-type
table. Reserved-word quoting is also not reproduced (collation names are rarely
reserved words); the common collations are covered exactly.

## Blast radius

A plain index (no explicit collation — the overwhelming majority) stores an empty
`ColCollations` and is byte-unchanged in both the catalog and the def string. The
parser already consumed the `COLLATE` clause, so no previously valid statement
changes parse behaviour; the only change is that the name is now *retained* rather
than dropped. No WAL/dump-format change. goopg builds every index with its default
comparison regardless of the declared collation, so this is dump-fidelity only —
the restored index carries the correct collation for the day collation-aware scans
land.

## Verification

* New **DU-002 slice 313** in `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`):
  `CREATE INDEX collidx_c ON public.collidx (a COLLATE "C", b)`; the dump now
  emits `CREATE INDEX collidx_c ON public.collidx USING btree (a COLLATE "C", b);`,
  asserted as a substring of real pg_dump 18.3's stdout (the second column `b`
  confirms the default collation stays suppressed). PASS (4.7 s).
* New unit `TestBuildIndexDefColCollation`
  (`internal/catalog/index_def_collation_test.go`): pins COLLATE emission, its
  quoting (quoted `"C"` vs bare `ucs_basic`), its position before opclass and
  before `DESC`/`NULLS`, the mixed default/non-default composite case, and the
  empty-slice no-op.
* New unit `TestParseCreateIndexColCollation` (`internal/parser/ddl_test.go`):
  asserts the collation lands on `ColOrders[i].Collation` (schema-qualified
  `pg_catalog."C"` collapses to `C`), coexisting with an opclass and `DESC`.
* `go test ./internal/parser/ ./internal/catalog/ ./internal/executor/` PASS;
  `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
* `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

* pg_dump 002–010 catalog-view parity battery (further slices) — candidates:
  per-column COLLATE on an index *expression* (this slice covers plain key
  columns; an expression column's collation rides the same `ColCollations` slot),
  comment round-trip on more object kinds, GENERATED STORED expression edge cases.
* extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement).
