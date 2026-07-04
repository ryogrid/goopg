# ALTER TYPE OWNER TO / composite RENAME TO (M0122-0005)

Status: accepted
Date: 2026-07-05
Supersedes: none

## Problem

`unimplemented_feat.json` entry `m0097-0017` ("ALTER TYPE RENAME and OWNER
operations are no-ops") was stale on the RENAME half but correct on the OWNER
half, and hid a second, more serious bug:

1. **`ALTER TYPE ... OWNER TO role`** was parsed as a bare stub — the parser's
   `parseAlterType` (`internal/parser/ddl.go`) fell into the catch-all "any
   other ALTER TYPE variant" branch, which consumes tokens to `;`/EOF without
   recording the target role anywhere. The executor's `execAlterType`
   (`internal/executor/operators_ddl.go`) then treated `s.AddValue == ""` as
   the OWNER TO case and just `return nil`'d — a genuine no-op, for both enum
   and composite types, with no way to ever observe or change `typowner`.

2. **`ALTER TYPE <composite> RENAME TO new_name`** was silently *broken*, not
   merely absent: `AlterTypeStmt.RenameTo` was already populated correctly by
   the parser (mirrors enum rename, `M0097-enum-rename`), but the executor's
   RENAME TO branch unconditionally called `cat.RenameEnum(s.Name, s.RenameTo)`
   regardless of the target type's kind. `RenameEnum` only looks in
   `c.enumTypes`, so renaming a composite type raised a spurious
   `type "x" does not exist` (SQLSTATE 42710) instead of renaming it — real
   PostgreSQL (`AlterTypeOwnerInternal`/`RenameType` in
   `postgres/src/backend/commands/typecmds.c`) supports RENAME TO uniformly
   across enum, composite, range, and base types.

## Scope

Bounded to the two ALTER-TYPE-addressable user type kinds goopg already
models with dedicated in-memory structs: **enum** (`catalog.EnumType`) and
**composite** (`catalog.CompositeType`). Domains have their own `ALTER DOMAIN`
statement (not `AlterTypeStmt`) and are out of scope here. Range/multirange
types (`catalog.RangeType`) and the auto-generated array/domain/multirange
`pg_type` rows keep their pre-existing hardcoded `bootstrapSuperuserOID`
typowner — `ALTER TYPE` never routes to them today, so they are a smaller,
separate follow-up (see Deferred below).

## Design

Mirrors the existing `ALTER COLLATION ... OWNER TO` implementation
(`execAlterCollation`, `docs/design/...alter-collation...`) and the
`Owner uint32` / `OwnerOrDefault()` convention already used by
`StatisticsObject`, `UserAggregate`, `UserOperator`, etc.
(`internal/catalog/catalog.go`): `0` means "never had OWNER TO applied,
defaults to the bootstrap superuser (OID 10) at render time."

1. **`catalog.EnumType`** / **`catalog.CompositeType`** gain an `Owner uint32`
   field and an `OwnerOrDefault()` method. `RegisterCompositeTypeWithFields`
   reuses the existing `*CompositeType` pointer on re-registration (ADD/DROP/
   ALTER ATTRIBUTE all funnel through it to re-sync the field list), so
   leaving `Owner` untouched there is sufficient to preserve it across those
   operations — only the initial `CREATE TYPE` call site
   (`internal/executor/operators_ddl.go`'s `execCreateType`) sets
   `.Owner = o.currentDDLOwnerOID()`, the same "creator becomes owner"
   convention used by `CreatePublication` et al.
2. New catalog methods, mirroring `SetCollationOwner`/`RenameEnum`:
   `SetEnumOwner`, `SetCompositeTypeOwner`, `RenameCompositeType`.
3. **Parser**: `AlterTypeStmt.NewOwner` (empty when not an OWNER TO
   statement; `"current_user"` sentinel for
   `CURRENT_USER`/`SESSION_USER`/`CURRENT_ROLE`, exactly like
   `AlterCollationStmt.NewOwner`). A new `OWNER TO` branch sits between the
   existing `RENAME` branch and the final catch-all stub in `parseAlterType`.
4. **Executor**: `execAlterType`'s RENAME TO branch now checks
   `cat.LookupCompositeType(s.Name)` first and calls `RenameCompositeType`
   when it matches, falling back to the pre-existing `RenameEnum` path
   otherwise. A new `s.NewOwner != ""` branch resolves the role (or the
   `current_user` sentinel) via `cat.RoleOID`, raising `42704` for an unknown
   role, and dispatches to `SetCompositeTypeOwner`/`SetEnumOwner` — raising
   `42704` if the named type does not exist at all (previously: no error,
   ever, regardless of whether the type existed).
5. **`pg_type` rendering**: the four `bootstrapSuperuserOID` typowner literals
   for enum/composite base + array rows
   (`internal/executor/pg18_user_catalog_rows.go`:
   `buildUserPGTypeRowForEnum(Array)`/`buildUserPGTypeRowForComposite(Array)`)
   now read `et.OwnerOrDefault()`/`ct.OwnerOrDefault()` instead. Domain/range/
   multirange rows are untouched (see Deferred).

## Tests

- `internal/parser/m0097_0017_test.go`: `TestAlterTypeOwnerToParsing` —
  `NewOwner` captures a plain role name and the three `current_user`
  sentinel spellings; `RenameTo` stays empty (must not cross-populate).
- `internal/executor/alter_type_owner_test.go`:
  - `TestAlterTypeOwnerTo` — `OwnerOrDefault()` is 10 before any OWNER TO for
    both an enum and a composite type; `ALTER TYPE ... OWNER TO` updates it
    to the resolved role OID for both kinds; an unknown type and an unknown
    role each raise 42704 (previously: silent no-op / a different, wrong
    error path).
  - `TestAlterTypeRenameToComposite` — `ALTER TYPE <composite> RENAME TO`
    renames instead of raising a spurious 42710, and the field list survives
    the rename.
- Full `go build ./...` and `go test ./internal/parser/... ./internal/catalog/... ./internal/executor/...`
  (whole packages, not just the new tests) — clean, no regressions.

## Deferred

- **Restart persistence of `Owner`.** Enum/composite `CREATE TYPE` has no WAL
  record (`grep EncodeCreateEnum/EncodeCreateComposite` finds nothing) and no
  heap-reload path in `internal/initdb` either — type definitions themselves
  are not yet restart-durable independent of this change, so `Owner` inherits
  that same gap rather than introducing a new one. See ledger row for the
  resume point once enum/composite restart durability is tackled.
- **Range/multirange/domain typowner** stay hardcoded to the bootstrap
  superuser; `ALTER TYPE`/`ALTER DOMAIN` don't route ownership changes to
  them. A future slice can extend the same `Owner`/`OwnerOrDefault` pattern to
  `catalog.RangeType`/`catalog.Domain` if `ALTER DOMAIN ... OWNER TO` or a
  range-type OWNER TO is ever exercised.
- **`pg_dump` ACL default (`acldefault('T', typowner)`) interaction** with a
  non-default owner was not separately verified against a live `pg_dump`
  round-trip this loop (the external-binary TAP suite, `TestPort_
  PgDumpConnectionSetup`, was not re-run — see the WAL/TAP practice card:
  ported tests are slow and out of the default gate). The default value is
  unchanged for every existing test (`OwnerOrDefault()` returns the same `10`
  `bootstrapSuperuserOID` constant did when `Owner` is unset), so regression
  risk on existing dump fixtures is low, but a non-default owner's ACL
  rendering has not been end-to-end verified.
