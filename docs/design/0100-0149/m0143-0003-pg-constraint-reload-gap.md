Status: in-progress — M0143-0003a (PRIMARY KEY `IsConstraint` restoration),
M0143-0003b (CHECK constraint write+reload), M0143-0003d (NOT NULL
named-metadata write+reload), and M0143-0003c (UNIQUE non-PK
constraint-backed index `IsConstraint` write+reload) landed 2026-09-17/18;
M0143-0003e (EXCLUDE `indisexclusion`) remains as the last follow-up, not yet
started.
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
| `n` (NOT NULL, PG18 named) | `catalog.Table.NotNullConstraints` | **fixed** — M0143-0003d (`writeNotNullConstraintRow` / `loadNotNullConstraintsFromHeapForDB`; enforcement itself always survived via `pg_attribute.attnotnull`/`Column.NotNull` — only the named-constraint metadata was lost) |
| `p`/`u` (PRIMARY KEY / UNIQUE, index-backed) | `catalog.Index.IsConstraint` (`catalog.go:2060`) | **fixed** — `p` via M0143-0003a (indisprimary already durable, no new state); `u` via M0143-0003c (`writeUniqueConstraintRow` / `loadUniqueConstraintsFromHeapForDB`, new `contype='u'` heap row keyed by `conindid`) |
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

## M0143-0003c — UNIQUE (non-PRIMARY-KEY) constraint-backed index `IsConstraint` (landed 2026-09-18)

**Done.** Resolved the schema decision this section previously left open in
favor of option (a) — extending M0143-0003b's heap-write mechanism, not
inventing a new `pg_index` bit (option (b), which 0003e still needs for
`indisexclusion` since EXCLUDE has no equivalent "reuse an existing signal"
option). Rationale: real PG already solves this exact problem via
`pg_constraint.conindid` pointing back at the index, so writing a
`contype='u'` heap row is the PG-faithful mechanism, not an invented one, and
it reuses the identical write/stamp/reload funnel M0143-0003b and -0003d
already built rather than adding a second, parallel persistence path.

- Write: `buildPGConstraintRowForUnique`/`writeUniqueConstraintRow`/
  `stampUniqueConstraintRows` (`internal/executor/sys_pg_constraint.go`),
  field-matched against the synthesised view's own `contype='u'/'p'/'x'`
  projection (`catalog.go`'s `PGConstraintRowsForDBOid`): `conenforced`/
  `convalidated`/`conislocal` hardcoded true, `coninhcount`/`connoinherit`
  hardcoded 0/false (the view tracks no inheritance-locality state for
  index-backed constraints, unlike CHECK/NOT NULL, so the new row doesn't
  invent any either), `condeferrable`/`condeferred` taken from
  `idx.Deferrable`/`idx.InitiallyDeferred` (a latent, unrelated durability gap
  closed for free by reusing this same row — neither field was reloaded from
  anywhere before this), `conkey` carries every key column's 1-based ordinal
  (like NOT NULL, unlike CHECK), and **`conindid=idx.OID`** — the field this
  task exists to persist; PRIMARY KEY is excluded from the write loop (no new
  row needed, 0003a already covers it via `indisprimary`). Wired into the same
  `syncTableToCatalogHeap` write loop and `deleteCatalogRowsForOID` stamp
  funnel CHECK/NOT NULL use.
- Resync coverage: unlike CHECK/NOT NULL, the constraint object here IS the
  index (no `Table`-owned list), so `execCreateTable`'s/
  `execCreatePartitionChild`'s resync-dirty gates couldn't reuse a
  `len(...) > 0` check — added `tableHasUniqueConstraintIndex` (scans
  `IndexesOnTable` for `Unique && !Primary && IsConstraint`) as the equivalent
  trigger at both gate sites. The three ALTER-path call sites that set
  `IsConstraint = true` outside those two functions
  (`execAlterTableAddUnique`'s build-new-index branch,
  `adoptExistingIndexAsConstraint`'s USING-INDEX branch guarded to the
  non-`primary` case) each gained a `syncConstraintCatalogRow` call,
  mirroring 0003b's identical precedent for its own non-`execCreateTable`
  CHECK call sites.
- Reload: `loadUniqueConstraintsFromHeap`/`loadUniqueConstraintsFromHeapForDB`
  (`internal/initdb/catalog_heap_reload.go`), wired in `open.go` right after
  `loadNotNullConstraintsFromHeap`. Resolves `conindid` via a new
  `catalog.InMemory.LookupIndexByOIDAllDBs` (added alongside the existing
  dbOid-scoped `LookupIndexByOID`, mirroring `LookupTableByOIDAllDBs`'s
  cross-database fallback the CHECK/NOT NULL/FK loaders already rely on for
  `conrelid`) and sets `idx.IsConstraint = true` plus
  `idx.Deferrable`/`idx.InitiallyDeferred` from the decoded row.
- Test: `TestDatabaseDDLReloadAcrossRestart` extended — `gauge` now also
  carries `tag text CONSTRAINT gauge_tag_unique UNIQUE`. Post-restart
  assertions check the `pg_constraint` row AND a behavior that only works when
  `IsConstraint` is genuinely true — `ALTER TABLE gauge RENAME CONSTRAINT
  gauge_tag_unique TO gauge_tag_uniq2` (RENAME CONSTRAINT's `!Primary &&
  Unique && IsConstraint` guard) — plus a genuine UNIQUE-violation INSERT.
  Verified live: temporarily no-op'd the `loadUniqueConstraintsFromHeap` call
  in `open.go`, re-ran — failed with the predicted symptom (empty
  `pg_constraint` UNIQUE row AND the RENAME failing 42704 "constraint ...
  does not exist"), restored, re-ran green.
- Found and deliberately NOT fixed here (pre-existing, unrelated to restart
  durability — recorded as a deferral-ledger row): `execCreatePartitionChild`'s
  `poc.UniqueColumns` inline-column path and `LIKE ... INCLUDING INDEXES`
  both clone/create a unique index without ever setting `IsConstraint = true`,
  even live, pre-restart — a narrower, adjacent bug this task's research
  surfaced but did not introduce.

## M0143-0003d — NOT NULL constraint named-metadata durable persistence (landed 2026-09-18)

**Done.** `Column.NotNull` (the actual enforcement flag) already reloaded
correctly from `pg_attribute.attnotnull` (`internal/initdb/open.go`'s
`loadUserTablesFromHeapForDB`, `NotNull: ar.AttNotNull`) — confirmed by grep,
this was never a repeat of the CHECK gap. Only `catalog.Table.
NotNullConstraints` (the named-constraint list PG18 uses for `pg_constraint`
`contype='n'` rows and `pg_get_constraintdef` naming) was unreloaded.

Landed as the exact write+reload shape 0003b predicted, reusing its funnel
directly rather than a new wrapper:

- Write: `buildPGConstraintRowForNotNull`/`writeNotNullConstraintRow`/
  `stampNotNullConstraintRows` (`internal/executor/sys_pg_constraint.go`),
  field-matched against the synthesised view's own NOT NULL projection
  (`catalog.go`'s `PGConstraintRowsForDBOid`, NOT NULL block): `conenforced`
  is hardcoded `true` (PG has no NOT ENFORCED spelling for a NOT NULL
  constraint — `NamedNotNullConstraint` carries no `NotEnforced` field to
  begin with), `convalidated = !NotValid`, and `conkey` carries the single
  column ordinal (unlike CHECK, a NOT NULL constraint's `conkey` is NOT NULL
  in real PG — `ruleutils.c`'s `print_notnull` reads it to find the column).
  Wired into the same `syncTableToCatalogHeap` write loop and
  `deleteCatalogRowsForOID` stamp funnel CHECK uses (write loop added right
  after the CHECK loop; stamp call added right after
  `stampCheckConstraintRows`).
- Resync coverage: `execCreateTable` needed **no change** — its
  `notNullHeapDirty` flag already fires on every `tbl.AddNotNull` call (it
  predates this task, added for the `pg_attribute.attnotnull` sync per
  M0134-0005y), and that flag already gates a full `syncTableToCatalogHeap`
  re-run, which now also emits the NOT NULL rows for free.
  `execCreatePartitionChild` was NOT already covered: its 0003b-added resync
  block gated on `len(tbl.NamedChecks) > 0` only, but the same function's
  named-NOT-NULL block (parent-inherited + explicit `poc.NotNullColumns`)
  mutates `tbl.NotNullConstraints` via `AddNotNull` after the same early
  `syncTableToCatalogHeap` call CHECK's fix already named — widened the
  condition to `len(tbl.NamedChecks) > 0 || len(tbl.NotNullConstraints) > 0`.
- Reload: `loadNotNullConstraintsFromHeap`/`loadNotNullConstraintsFromHeapForDB`
  (`internal/initdb/catalog_heap_reload.go`), mirroring
  `loadCheckConstraintsFromHeapForDB`'s shape and reusing the FK loader's
  `fkAttnumsFromArrayText`/`fkColumnNames` helpers to decode `conkey` back to
  a column name; wired in `open.go` right after `loadCheckConstraintsFromHeap`.
- Test: `TestDatabaseDDLReloadAcrossRestart` extended — `gauge` now also
  carries `code text CONSTRAINT gauge_code_not_null NOT NULL`. Finding made
  live, not assumed: a `PRIMARY KEY` column (`gauge.id`, `parent.id`) turns
  out to ALSO carry its own auto-named `<table>_<col>_not_null` constraint —
  so an unfiltered `contype='n'` scan of the database picks up every table's
  PK column, and the assertion had to filter on
  `conrelid = 'gauge'::regclass` and expect both `gauge_code_not_null` AND
  `gauge_id_not_null`. Verified live: temporarily no-op'd the
  `loadNotNullConstraintsFromHeap` call in `open.go`, re-ran with `-count=1`
  — failed with the predicted symptom (`pg_constraint NOT NULL rows = []`),
  restored, re-ran green.

## M0143-0003e — EXCLUDE constraint `indisexclusion` durability

**Not started.** `indisexclusion` is a declared `pg_index` heap column
(`internal/initdb/initdb.go:4766`) but is never written by the real
index-creation path (confirmed by grep: zero non-comment hits for
`indisexclusion`/`IndIsExclusion` outside catalog schema declarations and the
unrelated `pg18_user_catalog_rows.go:1460` hardcoded-false synthetic row) and
never decoded on reload, so `Index.IsExclusion` is lost on every restart —
`x`-contype `pg_constraint` rows and `deferred_exclusion.go`'s
deferred-exclusion-check machinery both silently stop working for any EXCLUDE
constraint surviving a restart. 0003c's landed shape (a `pg_constraint` heap
row keyed by `conindid`, reusing the same funnel) generalizes directly:
EXCLUDE already sets `idx.IsConstraint = true` alongside `IsExclusion` for its
btree-equality special case (`execAlterTableAddExclude`,
`operators_ddl.go:12846`/`:4238`), so the natural fix is a `contype='x'`
sibling of `buildPGConstraintRowForUnique` that also stamps
`idx.IsExclusion = true` on reload — not a new `pg_index` bit after all, now
that 0003c has proven the `pg_constraint`-row approach out.

## Sequencing

0003a (done) → 0003b (CHECK, highest severity, no dependency) → 0003d (NOT
NULL, reuses 0003b's wrapper pattern) → 0003c (done, UNIQUE via
`pg_constraint.conindid`) → 0003e (EXCLUDE, reuses 0003c's exact shape with
contype='x').
