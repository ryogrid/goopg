Status: in-progress — M0143-0003a (PRIMARY KEY `IsConstraint` restoration) and
M0143-0003b (CHECK constraint write+reload) landed 2026-09-17; M0143-0003c/d/e
remain as follow-ups, not yet started.
Date: 2026-09-17
Supersedes: none

# M0143-0003 — `pg_constraint` returns 0 rows of any contype after a restart

## Problem

`.ralph/fix_plan.md`'s M0143-0003 (filed by M0143-0002's own text, "a second,
independent reload gap that R126 explicitly did not touch") reports `SELECT *
FROM pg_constraint` returning 0 rows after a restart, "including the `p`/`u`
rows synthesised from indexes that demonstrably survive". This doc records
this loop's recon: `pg_constraint` is a **fully virtual** table (`Virtual:
true`, `catalog.go:10360`) whose row set is synthesised live, per connection,
by `InMemory.PGConstraintRowsForDBOid` (`catalog.go:7104`) from four
independent in-memory sources — one per `contype`. Each source has its own,
separate reload gap; there is no single root cause.

| contype | source field | reload status |
|---|---|---|
| `f` (FOREIGN KEY) | `catalog.Table.ForeignKeys` | **fixed** — R126's `loadForeignKeysFromHeapForDB` (`internal/initdb/catalog_heap_reload.go:495`) |
| `c` (CHECK, table-level) | `catalog.Table.CheckConstraints`/`NamedChecks` | **fixed** — M0143-0003b (`writeCheckConstraintRow` / `loadCheckConstraintsFromHeapForDB`) |
| `n` (NOT NULL, PG18 named) | `catalog.Table.NotNullConstraints` | **never written to any heap, never reloaded** — M0143-0003d (enforcement itself survives via `pg_attribute.attnotnull`/`Column.NotNull`, which *is* reloaded; only the named-constraint metadata is lost) |
| `p`/`u` (PRIMARY KEY / UNIQUE, index-backed) | `catalog.Index.IsConstraint` (`catalog.go:2060`) | **fixed for `p` only** — M0143-0003a (this loop); `u` needs new durable state — M0143-0003c |
| `x` (EXCLUDE) | `catalog.Index.IsExclusion` | **never restored** — M0143-0003e |

Confirmed by exhaustive grep, not inference: `grep -in
"checkconstraints\|namedchecks" internal/initdb/*.go` and `grep -in
"notnullconstraints" internal/initdb/*.go` both return zero hits outside this
doc's own citation — neither field is touched by any reload path. This makes
M0143-0003b more severe than a display-only gap: `internal/executor/copy.go`,
`operators_fk.go`, and `operators_storage.go` all gate CHECK enforcement on
`len(tbl.CheckConstraints) > 0`, so **every CHECK constraint on every table
silently stops being enforced after any server restart**, with no error at
restart or at the first violating write.

## M0143-0003a — PRIMARY KEY `IsConstraint` restoration (landed this loop)

`RegisterIndexDuringRecoveryForDB` (`catalog.go:6595`) is the sole reload path
for indexes (WAL-based `RecordKindCreateIndex` replay was retired by B5 Slice
A — `internal/initdb/open.go:3654`). It restores `Primary`/`Unique` from the
decoded `pg_index` heap row (`IndIsPrimary`/`IndIsUnique`,
`internal/initdb/open.go:3809-3810`) but never set `IsConstraint`, so
`PGConstraintRowsForDBOid`'s `if (!idx.IsConstraint && !idx.IsExclusion) ...
continue` filter (`catalog.go:7200`) skipped every reloaded index
unconditionally — a PRIMARY KEY's backing index came back with `Primary=true`
but `IsConstraint=false`, vanishing from `pg_constraint` though the index
itself (and its uniqueness enforcement) kept working.

Fix: `IsConstraint: primary` at construction time (`catalog.go:6595`
neighborhood). This is lossless for PRIMARY KEY specifically — real PG's
`indisprimary` **always** implies a `pg_constraint` row; there is no "bare
primary index" concept, unlike UNIQUE (`CREATE UNIQUE INDEX` vs `ALTER TABLE
ADD CONSTRAINT ... UNIQUE` both set `indisunique` with no durable distinguishing
signal — see M0143-0003c). Verified: reverted the one-line change, confirmed
`TestDatabaseDDLReloadAcrossRestart`'s new PK assertions fail with exactly the
predicted symptom (`pg_constraint PK rows = [], want exactly [parent_pkey]`),
restored, re-ran green.

## M0143-0003b — CHECK constraint durable persistence (write + reload)

**Landed 2026-09-17.** Scope followed R126's FK precedent
(`writeForeignKeyConstraintRow`/`loadForeignKeysFromHeapForDB`,
`internal/executor/sys_pg_constraint.go:318` /
`internal/initdb/catalog_heap_reload.go:495`), with one deliberate deviation
from the plan originally written here (step 3 below):

1. `buildPGConstraintRowForTableCheck(tbl *catalog.Table, nc
   catalog.NamedCheckConstraint) Row` in `sys_pg_constraint.go`, mirroring
   `buildPGConstraintRowForDomainCheck` but `conrelid=tbl.OID`, `contypid=0`,
   and carrying `nc.IsLocal`/`InhCount`/`NoInherit` into
   `conislocal`/`coninhcount`/`connoinherit` — field-for-field matched against
   `PGConstraintRowsForDBOid`'s table-CHECK block (`catalog.go:7126-7159`),
   including leaving `conkey` NULL (real PG never populates it for CHECK).
2. `writeCheckConstraintRow(ctx, tbl, nc)` → `pgConstraintTableRel(ctx)`, same
   per-DB routing the FK write already uses.
3. **Deviation from the original plan:** rather than a single new wrapper
   hand-wired into all 9 `AddCheck*`/`AddCheckInherited` call sites
   (`operators_ddl.go:4319,4338,4345,4372,4405,5556,5582,9368,13106`), the
   write was wired into the EXISTING `syncTableToCatalogHeap` funnel (a
   write-all-of-`tbl.NamedChecks` loop, directly mirroring the FK loop already
   there) plus `deleteCatalogRowsForOID` (new `stampCheckConstraintRows`,
   mirroring `stampForeignKeyConstraintRows`). This is the SAME funnel every
   other per-table catalog row (pg_class/pg_attribute/pg_attrdef/pg_inherits/
   FK) already uses, so a CHECK row can never drift from what `syncTableTo
   CatalogHeap` writes for anything else on the same table. Getting full
   coverage of the 9 sites this way required finding and closing TWO ordering
   traps their own containing functions already document for NOT NULL
   (M0134-0005y) but that nothing had extended to CHECK: `execCreateTable`
   (5 of the 9 sites) and `execCreatePartitionChild` (2 of the 9) each call
   `syncTableToCatalogHeap` exactly ONCE, early, BEFORE any of their own
   CHECK-registration blocks run — `execCreateTable`'s resync condition was
   widened from `notNullHeapDirty` to `notNullHeapDirty ||
   len(tbl.NamedChecks) > 0`; `execCreatePartitionChild` had no second
   `syncTableToCatalogHeap` call at all and got one added, gated the same way.
   The remaining 2 sites (`:9368` ALTER ADD CHECK, `:13106` inside
   `cascadeCheckToChildrenAt`'s ADD-cascade) now call the pre-existing
   `(o *ddlOp) syncConstraintCatalogRow` helper (`operators_ddl.go:13003` —
   the DROP-cascade path was already calling it, just with nothing yet to
   persist).
4. DROP CONSTRAINT: the cascade-to-child path
   (`cascadeCheckDropToChildren`) already called `syncConstraintCatalogRow
   (child)`, which now actually persists once (3) landed. The TOP-LEVEL
   table's own `tbl.CheckConstraints`/`NamedChecks` splice
   (`execAlterTableDropConstraint`, `:13422`-ish) had NO resync call at all —
   added `o.syncConstraintCatalogRow(tbl)` there. (`deleteConstraintRowByOID`,
   the domain-CHECK xmax-stamp-by-OID helper the original plan named, was
   NOT reused for table CHECK — `syncConstraintCatalogRow`'s stamp-then-
   rewrite-current pattern was already in place and is what the ADD sites
   needed anyway, so DROP reuses the same call for consistency rather than
   introducing a second removal mechanism.)
5. `loadCheckConstraintsFromHeapForDB`/`loadCheckConstraintsFromHeap` in
   `catalog_heap_reload.go`, mirroring `loadForeignKeysFromHeapForDB`'s shape
   (scan `pg_constraint` heap `contype='c' AND conrelid<>0` — the
   `conrelid=0` domain-CHECK rows stay `reloadUserDomainsFromHeap`'s), wired
   immediately after `loadForeignKeysFromHeap` in `open.go`.
6. Regression test: `TestDatabaseDDLReloadAcrossRestart`
   (`internal/postmaster/database_ddl_reload_test.go`) extended with a
   `gauge (id int4 PRIMARY KEY, level int4 CONSTRAINT gauge_level_check CHECK
   (level >= 0))` table in `r1`, asserting BOTH the post-restart
   `pg_constraint` row AND actual enforcement (a satisfying `INSERT`
   succeeds, a violating one still fails). Live-verified: temporarily
   no-op'd the `loadCheckConstraintsFromHeap` call in `open.go` and confirmed
   both new assertions fail with the exact predicted symptom, restored,
   re-ran green.

**Residual, not covered by this task** (found while auditing every
`.NamedChecks` mutator for ADD-site completeness — recorded as a
deferral-ledger row, 2026-09-17, not a new fix_plan task): `ALTER TABLE ...
VALIDATE CONSTRAINT` (`operators_ddl.go:9249`, flips `NotValid` false) and
`ALTER TABLE ... RENAME CONSTRAINT` (`:10302`/`:10309`) mutate
`NamedChecks` in place but still never resync — metadata-only (enforcement is
unaffected either way), so a restart reverts a validated CHECK to NOT VALID
or a renamed CHECK to its old name. Same one-line `syncConstraintCatalogRow`
fix as the sites above; ledger row has the exact resume point.

## M0143-0003c — UNIQUE (non-PRIMARY-KEY) constraint-backed index `IsConstraint`

**Not started. Architecturally harder than 0003b**, not merely unimplemented:
there is genuinely no durable signal to distinguish `ALTER TABLE t ADD
CONSTRAINT u UNIQUE (col)` from a bare `CREATE UNIQUE INDEX u ON t(col)` after
a restart today. Real PG distinguishes them via `pg_constraint.conindid`
pointing back at the index — a durable heap row goopg does not write for
`contype IN ('u','x')` (`pg_constraint` is entirely virtual for these, per the
table above). Two candidate fixes, not yet evaluated against each other:

- (a) Extend M0143-0003b's heap-write mechanism to ALSO persist a `contype='u'`
  row for constraint-backed UNIQUE indexes (mirroring the PK case, but PK gets
  away with zero new state because `indisprimary` already carries the
  signal — UNIQUE has no such luck), OR
- (b) add a new persisted bit directly on the `pg_index` heap row (schema
  already reserves space per real PG's column layout — see 0003e below, same
  shape of problem for `indisexclusion`).

Either requires a schema/heap-format decision this doc does not make. File as
its own implementation loop once 0003b's write-path plumbing exists to reuse
(option (a) is the smaller diff if 0003b lands first).

## M0143-0003d — NOT NULL constraint named-metadata durable persistence

**Not started, lower severity than 0003b**: `Column.NotNull` (the actual
enforcement flag) already reloads correctly from `pg_attribute.attnotnull`
(`internal/initdb/open.go`'s `loadUserTablesFromHeapForDB`, `NotNull:
ar.AttNotNull`) — confirmed by grep, this is NOT a repeat of the CHECK gap.
Only `catalog.Table.NotNullConstraints` (the named-constraint list PG18 uses
for `pg_constraint` `contype='n'` rows and `pg_get_constraintdef` naming) is
unreloaded. Same write+reload shape as 0003b, smaller blast radius (metadata
only, not enforcement) — sequence after 0003b so it can reuse the same
`addTableCheckAndSync`-style wrapper pattern once proven.

## M0143-0003e — EXCLUDE constraint `indisexclusion` durability

**Not started.** `indisexclusion` is a declared `pg_index` heap column
(`internal/initdb/initdb.go:4766`) but is never written by the real
index-creation path (confirmed by grep: zero non-comment hits for
`indisexclusion`/`IndIsExclusion` outside catalog schema declarations and the
unrelated `pg18_user_catalog_rows.go:1460` hardcoded-false synthetic row) and
never decoded on reload, so `Index.IsExclusion` is lost on every restart —
`x`-contype `pg_constraint` rows and `deferred_exclusion.go`'s
deferred-exclusion-check machinery both silently stop working for any EXCLUDE
constraint surviving a restart. Likely the same shape as 0003c option (b)
(persist a bit on the `pg_index` heap row); worth doing together with 0003c
once that decision is made, since both need a new `pg_index`-row bit.

## Sequencing

0003a (done) → 0003b (CHECK, highest severity, no dependency) → 0003d (NOT
NULL, reuses 0003b's wrapper pattern) → 0003c/0003e together (both need the
same new-durable-bit decision for `pg_index`).
