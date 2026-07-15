# Design: Enum and Domain Types (M0097-0017)

**Status**: accepted  
**Milestone**: M0097-0017 — Extended type parity  
**Target tests**: `enum`, `domain`

## Problem

The `enum` and `domain` regress tests currently fail at parse time because
goopg has no `CREATE TYPE … AS ENUM` or `CREATE DOMAIN` parser branch.
Every subsequent statement that references these types then produces errors
or wrong answers.

## Scope

This document covers:
1. `CREATE TYPE name AS ENUM (val1, val2, …)` — user-defined enum types
2. `ALTER TYPE name ADD VALUE [IF NOT EXISTS] val [BEFORE|AFTER ref]` — enum mutations
3. `DROP TYPE name [CASCADE|RESTRICT]` — type removal
4. `CREATE DOMAIN name [AS] base_type [constraints]` — domain alias types
5. `DROP DOMAIN name [CASCADE|RESTRICT]` — domain removal
6. `pg_enum` virtual catalog view
7. Column type resolution: enum/domain columns stored as base type
8. Cast support: `'val'::enumtype`, `val::domaintype`
9. Helper functions: `enum_first`, `enum_last`, `enum_range`, `pg_input_is_valid`

Out of scope (v0):
- Composite types (`CREATE TYPE … AS (col type, …)`) — needed for rowtypes.sql
- Range types (`CREATE TYPE … AS RANGE`) — separate milestone
- Full enum constraint enforcement beyond label validation
- Domain CHECK constraint evaluation (parsed, not enforced)
- ALTER TYPE RENAME VALUE, RENAME TO
- CAST catalog entries

## Design

### Storage model

**Enums**: stored as text at rest. The enum label is the canonical value.
Comparison uses the sort order (position in the original definition list).
The catalog tracks the ordered label list; the btree codec treats enum
columns as text.

**Domains**: transparent alias for the base type. A column declared as
`domainint4` stores and behaves identically to `int4`. The domain registry
maps name → base type so the column type resolver can substitute.

### Catalog additions

```go
// EnumType holds one user-defined enum type.
type EnumType struct {
    Name   string
    OID    uint32
    Values []string  // ordered, position = sortorder
}

// Domain holds one user-defined domain type.
type Domain struct {
    Name    string
    OID     uint32
    Base    Type    // resolved base type (recurse through domain chains)
    NotNull bool
}
```

`InMemory` gets two new maps: `enumTypes map[string]*EnumType` and
`domains map[string]*Domain`.

### pg_enum virtual view

Columns: `enumtypid text, enumsortorder int, enumlabel text`  
`enumtypid` stores the enum type name (not a numeric OID) so that
`'rainbow'::regtype` (which returns the name unchanged in v0) matches.

### Column type resolution

`execCreateTable` already resolves column types. After the existing type
switch, an additional fallback looks up the type name in `enumTypes` (→
text) and `domains` (→ recursively resolved base type).

### Cast evaluation

`evalTypedStringLit` gains a fallback at the end of the switch: if the
type name is a known enum, validate the label and return it as a text
datum. If the type name is a known domain, recurse with the base type.

`evalFuncCall` case `"regtype"` returns the argument as-is (already done).

### Planner routing

`CreateTypeStmt`, `AlterTypeStmt`, `DropTypeStmt`, `CreateDomainStmt`,
`DropDomainStmt` are routed through the DDL pass-through: `planner.DDL{Stmt: s}`.

## Files changed

| File | Change |
|------|--------|
| `internal/parser/ast.go` | CreateTypeStmt, AlterTypeStmt, DropTypeStmt, CreateDomainStmt, DropDomainStmt |
| `internal/parser/ddl.go` | parseCreateType, parseAlterType, parseDropType, parseCreateDomain, parseDropDomain |
| `internal/parser/parser.go` | Dispatch for CREATE TYPE, ALTER TYPE, DROP TYPE, DROP DOMAIN |
| `internal/catalog/catalog.go` | EnumType, Domain structs; enumTypes/domains maps; Register/Lookup/Drop methods; pg_enum virtual table |
| `internal/planner/planner.go` | Route new stmt types through DDL |
| `internal/executor/operators_ddl.go` | execCreateType, execAlterType, execDropType, execCreateDomain, execDropDomain |
| `internal/executor/expr.go` | enum cast fallback in evalTypedStringLit; enum_first, enum_last, enum_range stubs |

## Follow-up (2026-07-15): `AS` base_type accepts multi-word type names

`parseCreateDomain`'s base-type parsing used bare `parseObjectName()`, which
only handles `schema.name` — it never consumed the trailing keywords of PG's
multi-word built-in type names, so `CREATE DOMAIN ... AS double precision`
failed with a parser syntax error (surfaced by the DU-002 pg_dump round-trip
probe, `TestPort_PgDumpConnectionSetup`, once the M0122-0007 4e catalog
cross-database isolation fixes for domains cleared the collision that
previously masked this gap).

`parseColumnType` (CREATE TABLE's column-type grammar) already handled
`double precision`, `character varying`, `bit varying`, and
`timestamp`/`time [with|without time zone]` (including the `time(N) with
time zone` form, where the qualifier trails the typmod parens). That switch
logic was factored out into two shared helpers so `parseCreateDomain` reuses
it instead of duplicating it:

- `parser.parseMultiWordTypeName(leading string) string` — the pre-typmod-args
  keyword switch (`double precision`, `character/bit varying`,
  `timestamp/time with/without time zone`).
- `parser.parseTimeZoneQualifierAfterArgs(name string) string` — the
  post-typmod-args `time(N)`/`timestamp(N) with/without time zone` case.

`parseCreateDomain` calls `parseMultiWordTypeName` after `parseObjectName`
(only when the base type wasn't schema-qualified, mirroring
`parseColumnType`'s own schema-qualified branch) and
`parseTimeZoneQualifierAfterArgs` after its typmod-args loop, before the
existing array-notation handling. `parseColumnType`'s own behavior is
unchanged — same switch logic, just relocated into the shared helpers.

Tests: 8 new multi-word cases appended to `TestM0097_0017_EnumDomainParsing`
plus `TestCreateDomainMultiWordBaseType` (asserts `BaseType`/`BaseTypeArgs`
for each multi-word form, including an array suffix) in
`internal/parser/m0097_0017_test.go`.

This is a parser-grammar fix, not a catalog cross-database isolation fix —
a different mechanism from the M0122-0007 4e series in
`docs/design/0122-0018-per-database-catalog-namespace.md`. See
`.ralph/deferral_ledger.md` (2026-07-15 row) for the next DU-002 probe
blocker this unblocked (`CREATE TYPE` cross-database isolation).

## Follow-up (2026-07-15, later loop): enum types gain per-database isolation

`catalog.InMemory.enumTypes` (backing `CREATE TYPE ... AS ENUM`) was keyed
by bare case-insensitive name only, so two distinct databases could not each
register a same-named enum — the exact collision class the M0122-0007 4e
series already fixed for `domains` and `userCollations`
(`docs/design/0122-0018-per-database-catalog-namespace.md`). The DU-002
round-trip probe (`TestPort_PgDumpConnectionSetup`) surfaced this as `type
"gtype" already exists` once the prior two follow-ups in this doc cleared
the domain collision and the domain-grammar gap ahead of it.

Fix mirrors the `domains`/`domainKey` pattern exactly: `EnumType` gained a
`DBOid uint32` field; a new `enumKey(dbOid, name) string` helper
(`internal/catalog/catalog.go`, next to `domainKey`) folds dbOid into the
`c.enumTypes` registry key. `RegisterEnum`/`RenameEnum`/`RenameEnumValue`/
`SetEnumOwner`/`AddEnumValue`/`AddEnumValueResult`/`RemoveEnumValue`/
`DropEnum` each gained a trailing variadic `dbOid ...uint32` (omitting it
resolves to `DefaultDBOid` via `resolveDBOid`, so every pre-existing call
site stays behavior-preserving). `LookupEnum` also gained the variadic
`dbOid` (added to the `Catalog` interface too) — when omitted it falls back
to a global by-name scan (`lookupEnumByNameLocked`, mirrors
`lookupDomainByNameLocked`) for read call sites not yet threaded through a
connection's dbOid.

All 7 write-path call sites in `internal/executor/operators_ddl.go`
(`execCreateType`'s `CREATE TYPE ... AS ENUM`, `execAlterType`'s RENAME
VALUE/RENAME TO/OWNER TO/ADD VALUE, `execDropType`'s enum branch) and the 3
ROLLBACK-undo call sites in `internal/executor/operators_tx.go`
(`undoEnumDDLFromContext`'s `RemoveEnumValue`/`RenameEnum`/`DropEnum`) thread
`ctx.CurrentDatabaseOid`.

`internal/server/dispatch.go` has a **second, independent** copy of the same
undo logic — `undoEnumDDLForRollback(connTx, cat)`, called from the
simple-query dispatch path's explicit `ROLLBACK`/failed-`COMMIT`/SSI-abort/
two-phase-abort/connection-teardown branches (5+2 call sites across
`dispatch.go`/`server.go`/`twophase.go`) — a sibling of
`undoEnumDDLFromContext` that mirrors it step-for-step but operates on
`connTx.Pending*` directly rather than `ctx.Pending*`
(`pattern_sibling_paths_must_agree`). Threading only the `executor` package's
copy left this one still calling `RemoveEnumValue`/`RenameEnum`/`DropEnum`
with no dbOid — which resolves to `DefaultDBOid` via `resolveDBOid`'s
empty-variadic fallback, silently mismatching the `RegisterEnum` call's raw
(possibly `0`, not `1`) `ctx.CurrentDatabaseOid` in embedded/test contexts
with no real per-connection database resolution. This surfaced immediately
as two real test failures
(`TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitCreateType`/
`...AddValue` in `internal/server/dispatch_batch_atomicity_test.go`) — the
enum survived its own `ROLLBACK` because the drop looked in the wrong
dbOid bucket. Fixed by adding a `dbOid uint32` parameter to
`undoEnumDDLForRollback` and threading it from each call site: the 5
`dispatch.go` sites already have a `ctx *executor.Context` in scope
(`ctx.CurrentDatabaseOid`); `twophase.go`'s `abortForPrepareSSIFailure` does
too; `server.go`'s connection-teardown path has no `ctx`, so it resolves the
dbOid directly from `connTx.DBName` via the pre-existing `resolveConnDBOid`
helper (the same resolution `wireExtensionRows` uses to stamp
`ctx.CurrentDatabaseOid` in the first place, so the two paths agree).

The ~20 remaining read-only `LookupEnum` call
sites (CAST/type-declaration/attribute-row-building paths in `expr.go`,
`operators_fk.go`, `operators_index.go`, `operators_indexonly.go`,
`operators_pg_input_error_info.go`, `operators_storage.go`,
`pg18_user_catalog_rows.go`, `planner.go`, plus a few more in
`operators_ddl.go`) are **not** dbOid-threaded — same bounded scope as the
`domains` follow-up's resume point (2), left for a future loop.

New `TestCreateEnumCrossDatabaseIsolation`
(`internal/catalog/create_enum_test.go`), mirroring
`TestCreateDomainCrossDatabaseIsolation`. Confirmed via the DU-002 probe:
the round-trip's failure point moved past `type "gtype" already exists` to
an unrelated parser gap (`DEFAULT 'na'::character varying` — a multi-word
type name as a CAST target, not as a column/domain base-type declaration —
a different grammar production from the one this doc's prior follow-up
fixed).

Still deferred (ledgered): WAL restart-persistence for enums is not
dbOid-aware (mirrors the domains/collations gap); `pg_enum`'s `VirtualRows`
iterates `c.enumTypes` without dbOid scoping, so `SELECT * FROM pg_enum`
still surfaces every database's enum labels regardless of which database
is querying (pre-existing, not newly introduced — the registry itself had
no isolation before this fix either).

## Follow-up (2026-07-15, third loop): composite types gain per-database isolation

The last unaudited sibling map in the M0122-0007 4e series:
`catalog.InMemory.compositeTypes`/`compositeTypeNames`/`compositeTypeFields`
(backing `CREATE TYPE ... AS (col type, …)`) were all keyed by bare
case-insensitive name only — the identical collision class already fixed for
`domains`, `userCollations`, and `enumTypes`
(`docs/design/0122-0018-per-database-catalog-namespace.md`).

Fix mirrors the `enumTypes`/`enumKey` pattern exactly: `CompositeType` gained
a `DBOid uint32` field; a new `compositeKey(dbOid, name) string` helper
(`internal/catalog/catalog.go`, next to `domainKey`/`enumKey`) folds dbOid
into all three composite registry keys.
`RegisterCompositeType`/`RegisterCompositeTypeWithFields`/
`RenameCompositeType`/`SetCompositeTypeOwner`/`DropCompositeType`/
`HasCompositeType` each gained a trailing variadic `dbOid ...uint32`
(omitting it resolves to `DefaultDBOid`, so every pre-existing call site
stays behavior-preserving — `RegisterCompositeType`/`RegisterCompositeTypeWithFields`
never errored on a same-name re-registration to begin with, unlike
`RegisterEnum`, so there is no analogous "duplicate silently permitted
cross-database" regression risk to guard against here).
`LookupCompositeType`/`LookupCompositeTypeFields` also gained the variadic
`dbOid` (added to the `Catalog` interface too) — when omitted they fall back
to a global by-name scan (`lookupCompositeTypeByNameLocked`, mirrors
`lookupEnumByNameLocked`/`lookupDomainByNameLocked`) for the ~15 read-only
call sites not yet threaded through a connection's dbOid (`expr.go`,
`pg18_user_catalog_rows.go`, `plpgsql_runtime.go`, `operators_fk.go`-adjacent
paths) — same bounded scope as the enum follow-up's identical deferral.

All composite write-path call sites in `internal/executor/operators_ddl.go`
thread `o.ctx.CurrentDatabaseOid`: `execCreateType`'s `CREATE TYPE ... AS
(...)` (both the with-fields and name-only-registration branches),
`execAlterType`'s `ADD`/`RENAME`/`DROP`/`ALTER ATTRIBUTE` single-subcommand
branches, its `RENAME TO`/`OWNER TO` composite-dispatch guards,
`execAlterTypeAttrCmds`'s multi-subcommand form, and `execDropType`'s
composite branch (both the `LookupCompositeType` heap-stamp guard and the
`DropCompositeType` call itself).

Applied the sibling-path lesson from the enum follow-up proactively this
time instead of discovering it via test failure: grepped
`internal/executor/operators_tx.go` and `internal/server/dispatch.go` up
front for the second, independent undo copy
(`undoEnumDDLFromContext`/`undoEnumDDLForRollback`) before running any
tests, and threaded `ctx.CurrentDatabaseOid`/`dbOid` through both
`PendingCreatedComposites` drop loops in the same edit pass as the executor
write paths. `undoEnumDDLForRollback` already accepted a `dbOid` parameter
from the enum fix, so only its call to `DropCompositeType` needed the
argument added.

This still surfaced one genuine regression, caught by the full targeted
test run rather than by the proactive grep: the pre-existing
`internal/executor/operators_tx_composite_test.go` built a bare
`&Context{Catalog: cat, PendingCreatedComposites: ...}` with no
`CurrentDatabaseOid` set (zero value `0`), while its
`RegisterCompositeTypeWithFields` calls omitted `dbOid` entirely (resolving
to `DefaultDBOid`, i.e. `1`, via `resolveDBOid`'s empty-variadic fallback) —
so once `undoEnumDDLFromContext` started passing the real (zero) `ctx.
CurrentDatabaseOid` to `DropCompositeType`, the drop looked in the `0`
bucket while the registration lived in the `1` bucket, silently failing to
undo. This is not a product bug — no real connection context resolves to a
literal `0` `CurrentDatabaseOid` — but a test-fixture inconsistency exposed
by making the dbOid plumbing real; fixed by setting `ctx.CurrentDatabaseOid:
catalog.DefaultDBOid` and passing `catalog.DefaultDBOid` to the `Register`/
`Lookup` calls in both test functions, matching how a real `Context` is
populated.

New `TestCreateCompositeTypeCrossDatabaseIsolation`
(`internal/catalog/create_composite_type_test.go`), mirroring
`TestCreateEnumCrossDatabaseIsolation`/`TestCreateDomainCrossDatabaseIsolation`.

Still deferred (ledgered, same shape as the enum/domain gaps): WAL
restart-persistence for composite types is not dbOid-aware; the
`PGClassRowsForDBOid`/`PGConstraintRowsForDBOid` virtual builders iterate
`c.compositeTypes`/`c.domains` without dbOid scoping, so the implicit
`pg_class`/`pg_attribute` rows for a composite type (and a domain's CHECK
constraints) surface regardless of which database is querying — pre-existing
for all three type kinds, not newly introduced by this fix. The ~15
remaining read-only `LookupCompositeType`/`LookupCompositeTypeFields` call
sites are not dbOid-threaded, matching the enum follow-up's identical
resume-point scope.
