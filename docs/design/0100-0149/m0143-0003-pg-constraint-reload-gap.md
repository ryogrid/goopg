Status: in-progress — M0143-0003a (PRIMARY KEY `IsConstraint` restoration)
landed 2026-09-17; M0143-0003b/c/d/e filed as follow-ups, not yet started.
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
| `c` (CHECK, table-level) | `catalog.Table.CheckConstraints`/`NamedChecks` | **never written to any heap, never reloaded** — M0143-0003b |
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

**Not started. Highest-severity remaining piece** (enforcement loss, not just
display). Scope, following R126's FK precedent exactly
(`writeForeignKeyConstraintRow`/`loadForeignKeysFromHeapForDB`,
`internal/executor/sys_pg_constraint.go:318` /
`internal/initdb/catalog_heap_reload.go:495`):

1. `buildPGConstraintRowForTableCheck(tbl *catalog.Table, nc
   catalog.NamedCheckConstraint) (Row, error)` in `sys_pg_constraint.go`,
   mirroring `buildPGConstraintRowForDomainCheck` but `conrelid=tbl.OID`,
   `contypid=0`, and carrying `nc.IsLocal`/`InhCount`/`NoInherit` into
   `conislocal`/`coninhcount`/`connoinherit` (fields the existing virtual-row
   synthesis at `catalog.go:7112-7159` already threads — the heap writer must
   match it field-for-field or a restart would change what `pg_dump` emits,
   the exact failure mode R126's own doc warns against).
2. `writeCheckConstraintRow(ctx, tbl, nc)` → `pgConstraintTableRel(ctx)` (the
   same per-DB heap FK rows already use — table-level, not domain-level).
3. Wire at all 9 in-memory call sites (`operators_ddl.go:4319,4338,4345,4372,
   4405,5556,5582,9368,13106` — every `tbl.AddCheck*`/`child.AddCheckInherited`
   call found this loop) via a single new executor-level wrapper (e.g.
   `(o *ddlOp) addTableCheckAndSync`) so the in-memory mutation and the heap
   write can never drift apart — do NOT hand-pair each call site
   individually, that is exactly the sibling-path-desync shape
   `pattern_sibling_paths_must_agree` warns about.
4. DROP CONSTRAINT (`operators_ddl.go:13154-13155` cascade,
   `:13384-13385` direct): stamp `xmax` on the corresponding heap row by
   `nc.OID`, mirroring `deleteConstraintRowByOID` (`sys_pg_constraint.go:337`).
5. `loadCheckConstraintsFromHeapForDB`/`loadCheckConstraintsFromHeap` in
   `catalog_heap_reload.go`, mirroring `loadForeignKeysFromHeapForDB`'s shape
   exactly (scan `pg_constraint` heap `contype='c' AND conrelid<>0` — the
   `conrelid=0` domain-CHECK rows stay `reloadUserDomainsFromHeap`'s, per the
   existing "shares this heap" comment at `catalog_heap_reload.go:514-518`),
   repopulating `tbl.CheckConstraints`/`NamedChecks` in OID order. Wire the
   call immediately after `loadForeignKeysFromHeap` in `open.go`'s startup
   sequence.
6. Regression test: extend `TestDatabaseDDLReloadAcrossRestart`
   (`internal/postmaster/database_ddl_reload_test.go`) with a CHECK-bearing
   table in `r1`/`r2`, asserting BOTH the `pg_constraint` row AND actual
   enforcement (an `INSERT` violating the CHECK must still fail) post-restart
   — the enforcement assertion is the one that actually matters; a
   catalog-only test could pass while `tbl.CheckConstraints` stays empty if
   the reload only touched `NamedChecks`.

Deferral ledger row filed for this (`.ralph/deferral_ledger.md`).

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
