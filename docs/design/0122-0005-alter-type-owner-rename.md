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

## Follow-up: range-type `OWNER TO` / `RENAME TO` (2026-07-06)

`catalog.RangeType` gained the same `Owner uint32` / `OwnerOrDefault()` pair
as enum/composite (defaulting to the bootstrap superuser, OID 10). New
catalog methods `SetRangeTypeOwner`/`RenameRangeType` mirror
`SetCompositeTypeOwner`/`RenameCompositeType` exactly (`RenameRangeType`
leaves `MultirangeName` untouched — the auto-generated multirange type is a
distinct pg_type row with its own name, unaffected by renaming the range type
itself, matching real PostgreSQL). `execCreateType`'s `IsRange` branch now
stamps `rt.Owner = o.currentDDLOwnerOID()` at creation (the same
"creator becomes owner" convention as enum/composite). `execAlterType`'s
RENAME TO and OWNER TO branches each gained a `cat.LookupRangeType(s.Name)`
check (after the composite check, before the enum fallback) — previously
`ALTER TYPE <range> RENAME TO`/`OWNER TO` silently mis-dispatched into
`RenameEnum`/`SetEnumOwner`, raising a spurious `42704`/`42710` "type does not
exist" for a range type that does exist, the identical dispatch-by-kind bug
class this design doc's original composite fix closed. The four
`bootstrapSuperuserOID` typowner literals in
`internal/executor/pg18_user_catalog_rows.go`
(`buildUserPGTypeRowForRange`/`ForMultirange`/`ForRangeArray`/
`ForMultirangeArray`) now read `rt.OwnerOrDefault()` instead — the multirange
and both auto-generated array types share the base range type's owner (there
is no independent multirange/array `Owner` field, mirroring the enum/
composite array-type precedent).

Tests: `internal/executor/alter_type_owner_test.go`'s
`TestAlterTypeOwnerToRange`/`TestAlterTypeRenameToRange`. Gates: `go build
./...` clean; `go test ./internal/catalog/... ./internal/executor/...
./internal/parser/...` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).

**Deferred (unchanged from before, plus one new item):** restart persistence
of `Owner` (range types already have a WAL record for `CREATE TYPE ... AS
RANGE`, unlike enum/composite, but `Owner` was added purely in-memory this
loop and is not yet threaded into `wal.EncodeCreateRangeType`/
`DecodeCreateRangeType` — a range type's owner reverts to the bootstrap
superuser default across a restart even though the type definition itself
survives). **Domain typowner remains untouched**: unlike range types, there is
no working `ALTER TYPE`-style dispatch to piggyback on — `ALTER DOMAIN` is a
wholly distinct, unparsed statement today (`internal/parser/ddl.go`'s
`parseAlter` discards it via the generic "collation / domain / extension /
language / operator / system" compatibility-stub loop, consuming tokens to
`;`/EOF with no AST at all), so adding `Domain.Owner` now would be dead code
with no way to ever set it. This is a materially larger, separate task
(parsing `ALTER DOMAIN`'s full grammar: `RENAME TO`, `SET`/`DROP DEFAULT`,
`SET`/`DROP NOT NULL`, `ADD`/`DROP CONSTRAINT`, `RENAME CONSTRAINT`,
`OWNER TO`, `SET SCHEMA`), not a bounded mechanical follow-up like this one.

## Deferred

- **Restart persistence of `Owner`.** Enum/composite `CREATE TYPE` has no WAL
  record (`grep EncodeCreateEnum/EncodeCreateComposite` finds nothing) and no
  heap-reload path in `internal/initdb` either — type definitions themselves
  are not yet restart-durable independent of this change, so `Owner` inherits
  that same gap rather than introducing a new one. See ledger row for the
  resume point once enum/composite restart durability is tackled.
- **Domain typowner** stays hardcoded to the bootstrap superuser; `ALTER
  DOMAIN` isn't parsed at all today, so there is nothing to route an
  ownership change through yet (see the range-type follow-up section above
  for the full scope of what building `ALTER DOMAIN` would take).
- **`pg_dump` ACL default (`acldefault('T', typowner)`) interaction** with a
  non-default owner was not separately verified against a live `pg_dump`
  round-trip this loop (the external-binary TAP suite, `TestPort_
  PgDumpConnectionSetup`, was not re-run — see the WAL/TAP practice card:
  ported tests are slow and out of the default gate). The default value is
  unchanged for every existing test (`OwnerOrDefault()` returns the same `10`
  `bootstrapSuperuserOID` constant did when `Owner` is unset), so regression
  risk on existing dump fixtures is low, but a non-default owner's ACL
  rendering has not been end-to-end verified.

## Follow-up: range-type `Owner` restart persistence (2026-07-06, later loop)

Closes deferred item (1) from the range-type follow-up section above: a
range type's `Owner` now survives a restart, matching the type definition
itself (which already had a WAL record). `wal.EncodeCreateRangeType`/
`DecodeCreateRangeType` gained a 10th `ownerOID uint32` parameter (written
as a fixed field right after `collationOID`, before the variable-length
name strings); `execCreateType`'s range branch now passes `rt.Owner` at
CREATE time and `internal/initdb/range_type_ddl_recovery.go`'s CREATE case
threads the decoded `ownerOID` into the reconstructed `catalog.RangeType`.

A post-CREATE `ALTER TYPE <range> RENAME TO`/`OWNER TO` needed its own new
WAL records, since — unlike composite/enum's ALTER forms, which still have
no restart persistence at all today — range types are the one type kind
whose ALTER-TYPE-addressable mutations are worth making durable now that the
CREATE record exists. Two new record kinds mirror
`RecordKindAlterCollationRename`/`RecordKindAlterCollationOwner` exactly,
minus the schema field (range types are keyed by name only, like access
methods, not schema-scoped):

- `RecordKindAlterRangeTypeRename` (117) /
  `wal.Encode`/`DecodeAlterRangeTypeRename` —
  `kind(1) | nameLen(2) | name | newNameLen(2) | newName`.
- `RecordKindAlterRangeTypeOwner` (118) /
  `wal.Encode`/`DecodeAlterRangeTypeOwner` —
  `kind(1) | ownerOID(4) | nameLen(2) | name`.

`execAlterType`'s range RENAME TO / OWNER TO branches (`internal/executor/
operators_ddl.go`) now call `o.ctx.WAL.Append` with these after the catalog
mutation succeeds, exactly like `execAlterCollation`. New catalog replay
hooks `RenameRangeTypeDuringRecovery`/`SetRangeTypeOwnerDuringRecovery`
(idempotent wrappers mirroring `RenameCollationDuringRecovery`/
`SetCollationOwnerDuringRecovery`) are called from two new cases in
`replayRangeTypeDDLRecords`. Both new record kinds are also added to the
physical-replay skip-list (`internal/wal/recovery.go`'s
`shouldSkipPhysicalReplay`-style switch) alongside `RecordKindCreateRangeType`/
`RecordKindDropRangeType`, since they carry only in-memory registry state
with no page-level effect, identical reasoning to the collation records.

Tests: `internal/wal/range_type_ddl_test.go` gained
`TestEncodeDecodeAlterRangeTypeRenameRoundTrip`/
`TestEncodeDecodeAlterRangeTypeOwnerRoundTrip` (plus the pre-existing CREATE
round-trip test now also asserts `ownerOID`); `internal/initdb/
range_type_ddl_recovery_test.go` gained
`TestRangeTypeDDLRecoveryReplaysRenameAndOwner` (CREATE + RENAME + OWNER TO
across a restart, mirroring `TestRangeTypeDDLRecoveryReplaysCreate`). Gates:
`go build ./...` clean; `go test ./internal/catalog/... ./internal/executor/...
./internal/wal/... ./internal/initdb/...` PASS; `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33).

**Still deferred:** domain typowner (unchanged — see above, needs `ALTER
DOMAIN` grammar first); grant-option delegation-chain resolution is
unrelated to this row. The `pg_dump` ACL-default live-verification gap noted
above is also unchanged (out of scope for a WAL-persistence follow-up).

## Follow-up: `ALTER DOMAIN RENAME TO` / `OWNER TO` (2026-07-06, later loop)

Closes the "domain typowner remains completely untouched" deferred item
above — the first two of `AlterDomainStmt`'s named sub-forms (`RENAME TO`,
`OWNER TO`); the rest (`SET`/`DROP DEFAULT`, `SET`/`DROP NOT NULL`, `ADD`/
`DROP CONSTRAINT`, `RENAME CONSTRAINT`, `SET SCHEMA`) remain out of scope,
per the "materially larger, separate task" note above and this bucket's
"ONE task per loop" rule.

Unlike range types, `ALTER DOMAIN` had **no dispatch to piggyback on at
all** — it fell entirely into `parseAlter`'s generic "collation / domain /
extension / language / operator / system" compat-stub loop
(`internal/parser/ddl.go`), which consumes tokens to `;`/EOF and returns a
bare `&AlterTableStmt{}` no-op with no AST capturing the target name, let
alone the sub-form. So this follow-up needed a new AST node, not just a new
field on an existing one (`AlterTypeStmt` doesn't apply — real PostgreSQL's
`RenameType`/`AlterTypeOwner` explicitly reject `ALTER TYPE` on a domain via
`objecttype == OBJECT_DOMAIN && typTup->typtype != TYPTYPE_DOMAIN"` — domains
are a structurally distinct statement, `AlterDomainStmt`/`RenameStmt` with
`renameType=OBJECT_DOMAIN` in `gram.y`, not `AlterTypeStmt` on a shared
production, verified against `postgres/src/backend/commands/typecmds.c`'s
`RenameType`/`AlterTypeOwner`).

1. **Parser**: new `parser.AlterDomainStmt{Name, Action ("rename"|"owner"),
   NewName, NewOwner}` (`internal/parser/ast.go`), mirroring
   `AlterSchemaStmt`'s shape exactly. A dedicated `if
   p.acceptIdentKeyword("domain")` branch in `parseAlter`
   (`internal/parser/ddl.go`), inserted immediately before the generic
   compat-stub loop (which had `"domain"` removed from its object-ident
   list, the same "carve out a dedicated case before the catch-all" pattern
   `ALTER SCHEMA` used), parses `RENAME TO`/`OWNER TO` (including the
   `current_user`/`session_user`/`current_role` owner sentinels) and falls
   back to the same token-consuming no-op for every other sub-form.
2. **Catalog**: `catalog.Domain` gains `Owner uint32` +
   `OwnerOrDefault()` (identical shape to `EnumType`/`RangeType`'s). New
   `RenameDomain`/`SetDomainOwner` methods mirror
   `RenameRangeType`/`SetRangeTypeOwner` exactly — domains are keyed by
   lowercased name only in `c.domains` (no separate OID-indexed map to
   re-key), so rename is a straight delete-old-key/re-insert-under-new-key.
3. **Executor**: `execCreateDomain` stamps `d.Owner =
   o.currentDDLOwnerOID()` at CREATE time (the same "creator becomes owner"
   convention as range types). New `execAlterDomain`
   (`internal/executor/operators_ddl.go`) switches on `s.Action`: `"rename"`
   calls `RenameDomain`, raising `42710` on failure (not-found or
   name-collision — matches the existing enum/range-type RENAME TO
   simplification of one shared code for both cases, not real PG's
   `42704`/`42710` split, for consistency with those sibling paths);
   `"owner"` resolves the role (or `current_user`) via `RoleOID`, raising
   `42704` for an unknown role, then calls `SetDomainOwner`, raising `42704`
   if the domain itself doesn't exist (matches real PG's `AlterTypeOwner`
   exactly for the not-found case). Wired into the DDL dispatch switch and
   `planner.go`'s DDL-statement-kind list; `internal/server/dispatch.go`'s
   command-tag switch gains `*parser.AlterDomainStmt` → `"ALTER DOMAIN"`
   (the `cmdtag_table.go` entry already existed, unused until now).
4. **`pg_type` rendering**: the two `bootstrapSuperuserOID` typowner
   literals in `buildUserPGTypeRowForDomain`/`ForDomainArray`
   (`internal/executor/pg18_user_catalog_rows.go`) now read
   `d.OwnerOrDefault()`, mirroring the range-type array-row precedent (the
   auto-generated array type shares the domain's owner; there is no
   independent array `Owner` field).

Tests: `internal/executor/alter_domain_owner_rename_test.go`'s
`TestAlterDomainOwnerTo` (default-owner assertion, OWNER TO updates it,
unknown-domain and unknown-role both raise `42704`) and
`TestAlterDomainRenameTo` (rename preserves `Base`/`Checks`, unknown-domain
raises `42710`). Gates: `go build ./...` clean; `go test
./internal/catalog/... ./internal/executor/... ./internal/parser/...
./internal/planner/... ./internal/server/...` PASS (no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).

**Still deferred:** every other `AlterDomainStmt` sub-form (`SET`/`DROP
DEFAULT`, `SET`/`DROP NOT NULL`, `ADD`/`DROP CONSTRAINT`, `RENAME
CONSTRAINT`, `SET SCHEMA`) still parses as a no-op — each would need its own
catalog mutation (`SET`/`DROP NOT NULL` in particular needs a
`checkDomainNotNull`-style scan of every column of the domain's type across
every table, real PG's `AlterDomainNotNull` in `typecmds.c`, not a bounded
mechanical change like RENAME TO/OWNER TO). Also unchanged: **domains have
no restart persistence at all** — `CREATE DOMAIN` itself has no WAL record
and no heap-reload path in `internal/initdb` (confirmed by grep: no
`EncodeCreateDomain`/domain recovery driver exists), unlike range types
which already had a CREATE record before this bucket started. Threading
`Owner`/rename into a hypothetical `EncodeAlterDomainRename`/`...Owner` pair
would be dead code until domain *definitions* themselves survive a restart
first — that is a materially larger, separate task (the domain analogue of
the enum/composite restart-durability gap noted in the original Deferred
section above), not a follow-up to this one.

## Follow-up: `ALTER DOMAIN ... RENAME CONSTRAINT` (2026-07-06, later loop)

Closes one more of the "rest" sub-forms named above:
`ALTER DOMAIN name RENAME CONSTRAINT old TO new`. Unlike the domain's other
sub-forms, real PostgreSQL does **not** model this under `AlterDomainStmt` in
`gram.y` at all — `RENAME CONSTRAINT` on a domain shares the generic
`RenameStmt` production with `renameType = OBJECT_DOMCONSTRAINT`
(`postgres/src/backend/parser/gram.y` line ~9436), resolved at execution time
by `RenameConstraint`'s `stmt->renameType == OBJECT_DOMCONSTRAINT` branch
(`postgres/src/backend/commands/tablecmds.c`), which calls
`get_domain_constraint_oid`/`RenameConstraintById`
(`postgres/src/backend/catalog/pg_constraint.c`). goopg groups every `ALTER
DOMAIN` sub-form under the one `AlterDomainStmt` AST node instead (simpler
given the existing `Action`-switch shape), so this is modelled as a third
`Action` value rather than a new statement type.

1. **Parser**: `parser.AlterDomainStmt` gains `ConstraintName`/
   `NewConstraintName` (for `Action == "renameconstraint"`). The "domain"
   branch's existing `if p.acceptIdentKeyword("rename")` arm now checks for a
   following `CONSTRAINT` keyword (`p.acceptKeyword(KwConstraint)` — a real
   lexer keyword, not an identifier, unlike `"rename"`/`"owner"`/`"domain"`
   themselves) before falling into the plain `RENAME TO` parse, and if found
   parses `old_name TO new_name`.
2. **Catalog**: new `InMemory.RenameDomainConstraint(domainName, oldName,
   newName string) error`, mirroring PG's exact two error messages so the
   executor can distinguish them by content: `"constraint %q for domain %s
   does not exist"` (covers both an unknown domain and an unknown constraint
   name on an existing one — `get_domain_constraint_oid`'s `missing_ok=false`
   path) and `"constraint %q for domain %s already exists"` (collision with
   another `CHECK` already on the same domain — `RenameConstraintById`'s
   `ConstraintNameIsUsed(CONSTRAINT_DOMAIN, ...)` guard). Domains have no
   separate constraint-name index (`Domain.Checks []DomainCheck` is a plain
   slice), so both lookups are linear scans, consistent with
   `domainCheckNameTaken`'s existing style.
3. **Executor**: `execAlterDomain`'s switch gains a `"renameconstraint"` case
   that maps the error to `42704` (`ERRCODE_UNDEFINED_OBJECT`) by default,
   `42710` (`ERRCODE_DUPLICATE_OBJECT`) when the message contains "already
   exists" — this exactly matches real PG's two distinct error codes for this
   statement (unlike the `RENAME TO`/`OWNER TO` follow-up above, which
   collapsed both cases to `42710` for consistency with the range-type/enum
   precedent; `RENAME CONSTRAINT` had no such precedent to match, so it
   follows real PG directly instead).

Tests: `internal/executor/alter_domain_owner_rename_test.go`'s
`TestAlterDomainRenameConstraint` — renames one of two named `CHECK`s
declared at `CREATE DOMAIN` time (multi-CHECK, DU-002 slice 385; `ALTER
DOMAIN ADD CONSTRAINT` itself is still unimplemented, so the second CHECK
needed for the collision case has to come from `CREATE DOMAIN`), asserts the
renamed check's expression and its sibling are otherwise untouched, and
covers all three failure modes (unknown constraint → `42704`, unknown domain
→ `42704`, name collision → `42710`). Gates: `go build ./...` clean; `go test
./internal/catalog/... ./internal/executor/... ./internal/parser/...
./internal/planner/... ./internal/server/...` PASS (no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).

**Still deferred:** `SET`/`DROP DEFAULT`, `SET`/`DROP NOT NULL`, `ADD`/`DROP
CONSTRAINT`, `SET SCHEMA` remain no-ops, and domain restart persistence is
still entirely unbuilt (both unchanged from the note above).

## Follow-up: `ALTER DOMAIN ... ADD`/`DROP CONSTRAINT` (2026-07-06, later loop)

Closes the `ADD`/`DROP CONSTRAINT` item named as still-deferred above. Real
PG's `gram.y` `AlterDomainStmt` production models both directly (unlike
`RENAME CONSTRAINT`'s generic `RenameStmt` detour): `ADD [CONSTRAINT name]
CHECK (expr) [NOT VALID]` (`DomainConstraint`/`DomainConstraintElem`, only
`CHECK` is legal there — a domain-level `NOT NULL` constraint is set via
`SET NOT NULL`, not `ADD`) and `DROP CONSTRAINT [IF EXISTS] name [RESTRICT |
CASCADE]`, executed by `AlterDomainAddConstraint`/`AlterDomainDropConstraint`
(`postgres/src/backend/commands/typecmds.c`).

1. **Parser**: `parser.AlterDomainStmt` gains `Action == "addconstraint"` /
   `"dropconstraint"`, plus `CheckExpr`/`CheckInValues` (mirroring
   `DomainCheckClause.Expr`/`InValues`) and `IfExists`. The domain branch in
   `parseAlter` gained two new arms:
   - `ADD [CONSTRAINT name] CHECK (expr) [NOT VALID]` reuses
     `tryParseCheckInValues`/`parseDomainCheckExpr` — the exact same helpers
     `CREATE DOMAIN`'s `CHECK` clause already uses — so a constraint added via
     `ALTER DOMAIN` stores byte-identical `Expr`/`InValues` shape to one
     declared at `CREATE DOMAIN` time. A trailing `NOT VALID` is accepted and
     discarded (see "Deferred" below).
   - `DROP CONSTRAINT [IF EXISTS] name [RESTRICT | CASCADE]` follows the
     standard `IF EXISTS`/drop-behavior parsing shape used elsewhere in this
     file (e.g. `DROP SCHEMA`); `RESTRICT`/`CASCADE` is accepted and discarded
     since goopg tracks no dependents on a domain `CHECK` to cascade over.

   **Pitfall caught by the tests**: unlike `"rename"`/`"owner"`/`"domain"`
   themselves (parsed via `acceptIdentKeyword`, since they're not reserved
   keywords in goopg's lexer), `ADD` and `DROP` **are** real keyword tokens
   (`KwAdd`/`KwDrop`) — the first pass used `acceptIdentKeyword("add"/"drop")`,
   which never matched, so the unconsumed `ADD`/`DROP` fell through into the
   generic compat-stub no-op loop and every ADD/DROP CONSTRAINT statement
   silently no-op'd (surfaced immediately by `TestAlterDomainAddConstraint`/
   `TestAlterDomainDropConstraint` as a bogus `42P01: relation "" does not
   exist`, since the swallowed statement parsed as an empty `AlterTableStmt`).
   Fixed by switching both arms to `p.acceptKeyword(KwAdd)`/
   `p.acceptKeyword(KwDrop)`, matching the existing `ALTER TABLE ADD ...`
   call sites' own convention (`ddl.go:9396` et al.).
2. **Catalog**: `AddDomainCheck`'s body was extracted into a new
   `addDomainCheckLocked(d *Domain, name, expr string, inValues []string)`
   (caller holds `c.mu`), since `AddDomainCheck` locks internally and can't be
   called from within another already-locked method without deadlocking
   (`sync.Mutex` isn't reentrant). Two new methods share it:
   - `AddDomainConstraint(domainName, name, expr string, inValues []string)
     error` looks the domain up by name (unlike `AddDomainCheck`, which takes
     an already-resolved `*Domain`), and — when an explicit name is given —
     checks it against `domainCheckNameTaken` first, returning `"constraint %q
     for domain %q already exists"` on collision (matches real PG's
     `domainAddCheckConstraint`'s `constraint "%s" for domain "%s" already
     exists"` message shape exactly, both names quoted — a different
     convention from `RenameDomainConstraint`'s message, which mirrors
     `RenameConstraintById`'s `format_type_be`-based unquoted domain name;
     the two call sites in real PG genuinely use different message shapes).
   - `DropDomainConstraint(domainName, constrName string, ifExists bool)
     error` mirrors `DropDomain`'s own `ifExists` convention: an unknown
     domain always errors regardless of `ifExists` (matches real PG —
     `missing_ok` only ever gates the constraint-name lookup, not the
     `typenameTypeId` domain lookup that happens first), an unknown
     constraint on a known domain errors unless `ifExists` (silent no-op
     instead), matching `"constraint %q of domain %q does not exist"`
     (`AlterDomainDropConstraint`'s message shape — "of domain", not "for
     domain", matching real PG's own wording split between the two call
     sites).
3. **Executor**: `execAlterDomain` gains `"addconstraint"`/`"dropconstraint"`
   cases. `"addconstraint"` looks the domain up first (for its `Base.Name`,
   needed to synthesize the `IN (...)`-shortcut expr text via the existing
   `domainInValuesCheckExpr` helper — the same one `execCreateDomain` already
   calls), then calls `AddDomainConstraint`, mapping `"already exists"` → 
   `42710` and everything else → `42704` (mirrors `"renameconstraint"`'s own
   error-code dispatch). `"dropconstraint"` calls `DropDomainConstraint`
   directly, mapping any error to `42704`.

Tests: `internal/executor/alter_domain_owner_rename_test.go`'s
`TestAlterDomainAddConstraint` (named CHECK, unnamed CHECK gets the
`<domain>_check` auto-name, `IN (...)` shortcut synthesizes the
`VALUE = ANY (ARRAY[...])` text, `NOT VALID` is accepted, name collision →
`42710`, unknown domain → `42704`) and `TestAlterDomainDropConstraint`
(plain drop, `RESTRICT` trailer accepted, unknown constraint → `42704`,
`IF EXISTS` no-ops instead, unknown domain → `42704` even with `IF EXISTS`).
Gates: `go build ./...` clean; `go test ./internal/catalog/...
./internal/executor/... ./internal/parser/... ./internal/planner/...
./internal/server/...` PASS (no regressions); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33).

**Deferred:** existing-column-data validation (real PG's
`AlterDomainAddConstraint`'s `!skip_validation` scan of every table column
typed with this domain, via `validateDomainCheckConstraint`) is not
performed — a newly `ADD`'d CHECK is accepted even when existing rows already
violate it. `NOT VALID` is parsed-and-discarded with no `convalidated`-style
flag recorded on `DomainCheck`, so there is nothing yet for a future
`VALIDATE CONSTRAINT` (itself still wholly unparsed, not even a no-op arm)
to distinguish. `SET`/`DROP DEFAULT`, `SET`/`DROP NOT NULL`, `SET SCHEMA`
remain no-ops, and domain restart persistence is still entirely unbuilt
(all unchanged from the notes above). See the matching 2026-07-06 deferral
ledger row for the full resume-point detail.

## Follow-up: `ALTER DOMAIN ... SET DEFAULT` / `DROP DEFAULT`

Picked up the `SET`/`DROP DEFAULT` item from the prior row's "remaining
`ALTER DOMAIN` sub-forms" list — the only one of the three left (`SET`/`DROP
NOT NULL`, `SET SCHEMA`) that is purely mechanical: it reuses the exact
`Default parser.Expr` field and `DefaultBin()` render path `CREATE DOMAIN
... DEFAULT expr` already populates, with no cross-table scan and no new
catalog field.

1. **Parser**: `AlterDomainStmt` gained a `DefaultExpr Expr` field. The
   domain branch in `parseAlter` (`internal/parser/ddl.go`) gained a `SET`
   arm (`p.acceptKeyword(KwSet)`) that, on a following `DEFAULT`, calls the
   same `p.parseExpr()` CREATE DOMAIN's own DEFAULT clause uses, and a `DROP`
   arm restructured to a single `p.acceptKeyword(KwDrop)` guarding nested
   `CONSTRAINT`/`DEFAULT` checks (previously `DROP CONSTRAINT` was matched by
   `p.acceptKeyword(KwDrop) && p.acceptKeyword(KwConstraint)` in one
   expression — harmless on its own, but a naive `DROP DEFAULT` arm added the
   same way would have been unreachable: Go still evaluates and consumes the
   left `KwDrop` operand even when the right operand fails to match, so by
   the time a second `p.acceptKeyword(KwDrop) && p.acceptKeyword(KwDefault)`
   ran, `DROP` would already have been eaten by the first failed attempt).
   `SET NOT NULL`/`DROP NOT NULL`/`SET SCHEMA` still fall through to the
   pre-existing generic no-op token-discard loop — consuming `SET`/`DROP`
   before falling through is harmless since that loop only discards tokens up
   to the statement terminator regardless of what's already gone.
2. **Catalog**: new `InMemory.SetDomainDefault(name string, expr parser.Expr)
   bool` sets (or, given a nil `expr`, clears) `Domain.Default` directly —
   no validation needed since any `Expr` value round-trips through
   `DefaultBin()` unconditionally, unlike `AddDomainConstraint`'s name-collision
   check.
3. **Executor**: `execAlterDomain` gained `"setdefault"`/`"dropdefault"`
   cases, both just looking the domain up via `SetDomainDefault` and mapping
   "not found" to `42704`, mirroring `"owner"`'s own not-found handling.

Tests: `internal/executor/alter_domain_owner_rename_test.go`'s
`TestAlterDomainSetDropDefault` (SET DEFAULT populates `Default`/renders via
`DefaultBin()`, a later SET DEFAULT replaces the prior expression outright,
DROP DEFAULT clears it back to nil, both forms raise `42704` on an unknown
domain). Gates: `go build ./...` clean; `go test ./internal/catalog/...
./internal/executor/... ./internal/parser/... ./internal/planner/...
./internal/server/...` PASS (no regressions); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33).

**Newly discovered (not this loop's scope, recorded for a future loop):**
while verifying `SET NOT NULL`/`DROP NOT NULL`/`SET SCHEMA` still parse as
no-ops after the `DROP` restructuring above, found they actually do NOT
silently no-op as every prior row in this bucket assumed — the shared
"unmodelled form" fallback (`return &AlterTableStmt{pos: t.Pos}, nil` with no
`Table` set) dispatches to `execAlterTable`, which immediately raises
`42P01: relation "" does not exist` on the empty target. Confirmed
pre-existing at HEAD before this loop's change (same failure via `git
stash`), not a regression introduced here, and not unique to domains — the
same `&AlterTableStmt{pos: t.Pos}` fallback pattern appears at 12 sites
across `internal/parser/ddl.go` for other unimplemented sub-forms. **Remaining:**
`SET`/`DROP NOT NULL` still needs the `checkDomainNotNull`-style cross-table
scan (feature-sized, unchanged); `SET SCHEMA` still needs a `Schema` field
decision on `catalog.Domain` (unchanged); and separately, whichever of the 12
fallback call sites are actually reachable by a real client statement (as
opposed to already covered by a more specific branch) should either return a
true no-op (e.g. a dedicated `NoopStmt`) or be audited case-by-case — needs a
survey of all 12 sites first, not a one-line fix. Domain restart persistence
remains entirely unbuilt.

## Follow-up: the 12-site `&AlterTableStmt{pos}` fallback bug (2026-07-06, later loop)

Surveyed and fixed all 12 sites named above (`internal/parser/ddl.go`). Every
one shares the same shape: a statement family's `parseAlter*` branch
recognizes its modelled sub-forms (RENAME TO, OWNER TO, SET (...), etc.) via
dedicated `if` arms, then falls through to a generic "consume the rest of the
statement, succeed as a no-op" tail for anything else it doesn't model. That
tail used to build the no-op via a bare, nameless
`&AlterTableStmt{pos: t.Pos}` (11 sites use the inline
`for ... p.advance() ... return &AlterTableStmt{...}` shape;
`parseAlterOpFamilyTail`'s `noop` closure at ddl.go:1643 is the 12th, routed
through the shared `parseSkipToSemicolonHelper`). Dispatching that empty
statement to `execAlterTable` (`internal/executor/operators_ddl.go:6634`)
reaches its final fallback (`operators_ddl.go:6878`), which unconditionally
raises `42P01: relation "" does not exist` — a real, reachable regression:
`ALTER DOMAIN d SET NOT NULL`, `ALTER AGGREGATE agg(int) SET SCHEMA s`,
`ALTER PUBLICATION p SET (publish = 'insert')`, etc. all errored instead of
silently succeeding, even though every one of these forms was *intended* to
be a no-op (per each site's own comment).

**Fix:** route all 12 sites through `parser.CompatNoopStmt{Tag: "<PG command
tag>"}` instead — the same no-op vehicle already used for GRANT/REVOKE/COMMENT
ON/etc. (`execCompatNoop`, `operators_ddl.go:14988`) — which short-circuits at
its very first check (`if s.ObjType == "" { return nil }`, `operators_ddl.go`
~15111) before any relation lookup, and whose `Tag` drives the
`CommandComplete` reply tag directly (`internal/server/dispatch.go:2498`)
instead of the misleading `"ALTER TABLE"` tag the old stub produced. Tags
assigned per site: `ALTER AGGREGATE`, `ALTER INDEX`, `ALTER MATERIALIZED
VIEW`, `ALTER VIEW` (3 sites — the two ALTER-COLUMN-on-a-view arms plus the
top-level ALTER VIEW tail), `ALTER OPERATOR`, `ALTER OPERATOR CLASS`, `ALTER
OPERATOR FAMILY` (the `parseAlterOpFamilyTail` closure), `ALTER
PUBLICATION`/`ALTER SUBSCRIPTION` (picked dynamically from the loop's
`pubSubKind` variable), `ALTER SCHEMA`, `ALTER DOMAIN`, and the generic
`collation`/`extension`/`language`/`operator`/`system` compat-stub loop
(`"ALTER " + strings.ToUpper(objIdent)` — `operator`/`collation` are
practically unreachable there since both have their own dedicated branches
earlier, but fixed for consistency/defense-in-depth).

Two pre-existing tests asserted the *old*, buggy behavior as the expected
shape and had to be corrected alongside the fix (not just the production
code): `TestParseAlterOperatorOwnerToIsNoop`
(`internal/parser/op_compat_test.go`) and
`TestParseAlterPublicationOtherFormsStillNoop`
(`internal/parser/alter_pubsub_owner_test.go`) both asserted
`stmts[0].(*AlterTableStmt)` for these exact no-op forms — updated to assert
`*CompatNoopStmt` instead, with a comment explaining why the old assertion
was itself pinning the bug.

New test: `internal/executor/alter_domain_owner_rename_test.go`'s
`TestAlterDomainUnmodelledFormsAreNoop` exercises the concrete
executor-level regression end to end (`SET NOT NULL`/`DROP NOT
NULL`/`SET SCHEMA` on a real domain now succeed instead of raising
`42P01`). Gates: `go build ./...` clean; `go test ./internal/parser/...
./internal/catalog/... ./internal/executor/... ./internal/planner/...
./internal/server/...` PASS (no regressions); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33).

**Deferred (unchanged from the prior row):** `SET`/`DROP NOT NULL` on a
domain still needs the `checkDomainNotNull`-style cross-table scan;
`SET SCHEMA` still needs a `Schema` field decision on `catalog.Domain`;
domain restart persistence remains entirely unbuilt. This follow-up only
fixed the *no-op mechanism* (wrong error where none should occur), not any
of the underlying feature-sized gaps.

## Follow-up: domain restart persistence (`CREATE`/`DROP DOMAIN` WAL records)

**Gap:** unlike every other user-defined type kind (enum, composite, range),
`catalog.Domain` had no WAL record and no `internal/initdb/*_ddl_recovery.go`
replay driver at all — `CREATE DOMAIN` never wrote anything to the WAL, so a
server restart silently forgot every domain: its base type, `NOT NULL`,
`DEFAULT`, every `CHECK` constraint, and its owner. This was the last named
resume point in the M0122-0005 domain bucket (every prior follow-up in this
section — RENAME TO/OWNER TO, RENAME CONSTRAINT, ADD/DROP CONSTRAINT, SET/DROP
DEFAULT — explicitly deferred it).

**Fix:** two new WAL record kinds, mirroring the `RangeType` precedent
(`RecordKindCreateRangeType`/`RecordKindDropRangeType`):

- `RecordKindCreateDomain` (119): carries the domain's OID/ArrayOID, base type
  name + typmod args, resolved enum `BaseOID`/`BaseIsEnum`, `NotNull`, owner,
  the `DEFAULT` expression as SQL text (`catalog.FormatExprForAttrdef` — the
  same base-form-no-domain-decoration text `RecordKindColumnDefaults` already
  uses for column defaults, re-parsed via `parser.ParseExpr` on replay; the
  domain-specific `::type` cast suffix `Domain.DefaultBin()` adds is computed
  dynamically from `Base.Name` at render time, so it must *not* be baked into
  the persisted text or it would double up after a `parser.ParseExpr` round
  trip), and every `CHECK` constraint (name, expr text, OID, `InValues` for
  the `IN (...)` shortcut form).
- `RecordKindDropDomain` (120): name only, mirroring `RecordKindDropRangeType`.

New `wal.EncodeCreateDomain`/`DecodeCreateDomain` (buffer-based, mirroring
`EncodeColumnDefaults`'s style rather than range type's fixed-offset layout,
since the nested variable-length `Checks`/`InValues` lists don't fit a flat
offset scheme) and `wal.EncodeDropDomain`/`DecodeDropDomain`. New
`catalog.InMemory.RegisterDomainDuringRecovery`/`DropDomainDuringRecovery`
(mirror `RegisterRangeTypeDuringRecovery`/`DropRangeTypeDuringRecovery`) —
the register hook also advances `nextOID` past the domain's OID, ArrayOID,
and every CHECK constraint OID so recovery doesn't reallocate them. New
`internal/initdb/domain_ddl_recovery.go`'s `replayDomainDDLRecords`, wired
into `Open` (`internal/initdb/open.go`) right after
`replayRangeTypeDDLRecords` — domains, like range types, are keyed by a plain
name string, so ordering relative to schema replay doesn't matter. Both new
record kinds were added to the physical-replay no-op skip-list
(`internal/wal/recovery.go`), same as every other catalog-registry-only kind.

`execCreateDomain` (`internal/executor/operators_ddl.go`) now appends a
`RecordKindCreateDomain` record right after `syncDomainTypeToCatalogHeap`;
`execDropDomain` appends a `RecordKindDropDomain` record right after a
successful `cat.DropDomain` call.

Verified against a real running server (not just the unit/recovery tests
below): `CREATE DOMAIN us_zip AS varchar(10) NOT NULL DEFAULT '00000' CHECK
(length(VALUE::text) = 5)`, then a full `stop`/`start` restart — `\dD`,
`pg_get_expr(typdefaultbin, 0)`, and `pg_get_constraintdef` all rendered
identically before and after the restart.

**Newly discovered, out of scope for this follow-up:** domain `NOT NULL` and
`CHECK` constraints are not enforced at all on table columns of a domain
type — `INSERT INTO t (z) VALUES (NULL)` into a `NOT NULL` domain column, and
`INSERT INTO t (p) VALUES (-5)` into a `CHECK (VALUE > 0)` domain column, both
silently succeed (reproduced against a *freshly created* domain in the same
live session, with no restart involved at all — this is not a persistence
gap, it is runtime DML-path validation that was never wired for domain-typed
columns in the first place). This is unrelated to the restart-persistence gap
this follow-up closes (which is only about the domain *definition* surviving
a restart) and is a materially larger, separate task — real PG enforces this
via `domain_check`/`ExecEvalConstraintNotNull` coercion nodes wrapped around
every domain-typed column's INSERT/UPDATE value at plan time
(`postgres/src/backend/executor/execExprInterp.c`), which goopg's expression
evaluator has no equivalent for today.

Tests: `internal/wal/domain_ddl_test.go`'s
`TestEncodeDecodeCreateDomainRoundTrip`/`TestEncodeDecodeDropDomainRoundTrip`/
`TestDecodeDomainRejectsWrongKindAndTruncatedPayload`;
`internal/initdb/domain_ddl_recovery_test.go`'s
`TestDomainDDLRecoveryReplaysCreate`/`TestDomainDDLRecoveryReplaysDropAfterCreate`/
`TestReplayDomainDDLRecordsHandlesMissingWalDir`/
`TestReplayDomainDDLRecordsHandlesNilCatalog`. Gates: `go build ./...` clean;
`go test ./internal/catalog/... ./internal/executor/... ./internal/parser/...
./internal/planner/... ./internal/wal/... ./internal/initdb/...
./internal/server/...` PASS (no regressions); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); manual restart verification against a real running
server (above).

**Deferred:** `SET`/`DROP NOT NULL` cross-table scan and `SET SCHEMA`'s
`Domain.Schema` field decision remain open (unchanged, feature-sized). The
newly discovered domain-constraint-enforcement gap above is recorded in the
deferral ledger as a fresh, separate item. None of the later `ALTER DOMAIN`
sub-forms (RENAME TO/OWNER TO/RENAME CONSTRAINT/ADD-DROP CONSTRAINT/SET-DROP
DEFAULT) got their own WAL records in this follow-up — only the state as of
`CREATE DOMAIN` (plus whatever ended up folded into the domain by the time it
was created) survives a restart; a post-CREATE `ALTER DOMAIN` mutation is
still lost on restart, mirroring the gap the range-type rename/owner-restart
follow-up closed for that type but which this loop did not extend to domains
(separate, smaller follow-up: mirror `RecordKindAlterRangeTypeRename`/`Owner`
(117/118) for domains at kinds 121+).
