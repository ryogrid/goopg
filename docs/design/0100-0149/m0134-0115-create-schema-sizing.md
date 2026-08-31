# M0134-0115 — `create_schema.sql`: sizing + AUTHORIZATION/sub-element fix

**Status:** PARKED (`failed`). One contained fix landed, taking the case from
95 diff lines / 15 `^-ERROR` / 2 `^+ERROR` to 38 diff lines / 0 `^-ERROR` /
0 `^+ERROR` — every ERROR-shaped mismatch is now byte-identical to the
oracle. The remaining gap is a single REFACTOR-tier subsystem (goopg has no
execution path at all for `CREATE SCHEMA`'s embedded `CREATE <element>`
sub-command list).

## Oracle case

`postgres/src/test/regress/sql/create_schema.sql` exercises PG's
`CREATE SCHEMA [name] [AUTHORIZATION role_spec] [CREATE <element> ...]`
form end to end: five schema-qualification-mismatch failure cases (the
embedded element references a schema different from the one being created,
raising `ERROR: CREATE specifies a schema (%s) different from the one being
created (%s)`) repeated across three header shapes (`AUTHORIZATION role`,
`AUTHORIZATION CURRENT_ROLE` under `SET ROLE`, `name AUTHORIZATION
CURRENT_ROLE`), then three matching-schema success cases that must actually
create the schema *and* the nested `CREATE TABLE`, verified via `\d` and a
`DROP SCHEMA ... CASCADE` that expects a `NOTICE: drop cascades to table
...`.

Sized live via `scripts/pg-regress-runner.sh -v create_schema` against the
PG 18.3 oracle: 95-line diff before any fix (15 `^-ERROR`, 2 `^+ERROR`), 38
lines after the fix below (0 `^-ERROR`, 0 `^+ERROR`).

## Root cause

goopg has no execution path for `CREATE SCHEMA`'s sub-command list, so — by
design — the parser (`internal/parser/ddl.go`'s `case
p.acceptIdentKeyword("schema")` arm inside `parseCreate`) always succeeds
for any `CREATE SCHEMA` spelling by skipping every token up to the
statement's terminating `;`/EOF and emitting a `CompatNoopStmt{Tag: "CREATE
SCHEMA", ObjType: "schema", ObjName: {Name: schemaName}}` that
`execCompatNoop`'s `"schema"` case (`internal/executor/operators_ddl.go`)
registers in the catalog. Two things were silently dropped by this design,
both invisible until sized live against the oracle:

1. **The `AUTHORIZATION role_spec` clause was parsed *only* to detect its
   presence** (so the arm wouldn't mistake the `AUTHORIZATION` keyword for
   an explicit schema name) — the role token itself, and PG's rule that an
   omitted schema name defaults to the role's name, were both discarded.
   Every one of the file's `CREATE SCHEMA AUTHORIZATION <role> ...` forms
   (no explicit schema name) therefore registered **no schema at all**
   (`ObjName.Name == ""` short-circuits `execCompatNoop`'s `"schema"` case).
2. **The embedded `CREATE <element>` sub-command's schema qualification was
   never inspected**, so PG's `setSchemaName` check
   (`postgres/src/backend/parser/parse_utilcmd.c` ~4197-4212,
   `ERRCODE_INVALID_SCHEMA_DEFINITION` / `42P15`) — which fires for *every*
   element in the sub-command list whose explicit schema doesn't match the
   schema being created, before anything is created — had no goopg
   counterpart. The five mismatch cases per header shape silently absorbed
   as a bare `CREATE SCHEMA` success instead of erroring.

Because dead reckoning suggested a dispatch-level (pre-parse, text-based)
intercept mirroring `role_ddl.go`'s pattern, the first attempt landed
entirely in `internal/postmaster/dispatch.go`'s `registerCompatNoopSchema`
compat-noop-absorption path — but that path only runs when `parser.Parse`
**fails**, and the pre-existing `ddl.go` arm above means `parser.Parse`
*never* fails for `CREATE SCHEMA`, so that first attempt was silently dead
code (confirmed live: diff unchanged before/after). The real fix had to
land in the parser/executor pair that actually owns this statement.

## Landed this loop

**Parser** (`internal/parser/ddl.go`'s CREATE SCHEMA arm,
`internal/parser/ast.go`'s `CompatNoopStmt`): the arm now captures the
`AUTHORIZATION` role token verbatim into a new `SchemaAuthRole` field
(lowercased, so `CURRENT_ROLE`/`CURRENT_USER`/`SESSION_USER`/`USER` compare
cheaply later), and — when a `CREATE <element>` sub-command follows — calls
a new `captureCreateSchemaSubElementSchema` helper that advances far enough
into the sub-command (using the existing `parseObjectName` for its
`ObjectName.Schema` field) to extract the schema qualification of the
target `SEQUENCE`/`TABLE`/`VIEW` name, or the table after `INDEX ON`/
`TRIGGER ... ON`, into `SchemaSubElementSchema`/`SchemaHasSubElement`. Any
tokens neither this helper nor the role-token capture consumed are still
swept up by the arm's pre-existing skip-to-semicolon loop — no attempt is
made to actually parse or execute the sub-command's full grammar (column
lists, `AS SELECT`, trigger `EXECUTE FUNCTION` clause, etc.).

**Executor** (`internal/executor/operators_ddl.go`'s `execCompatNoop`
`"schema"` case): resolves the owning schema name — `ObjName.Name` if an
explicit name was given, else `SchemaAuthRole` resolved against the live
session (`Context.EffectiveUserName()` for `CURRENT_ROLE`/`CURRENT_USER`/
`USER`, `Context.SessionUserName()` for `SESSION_USER`, otherwise the role
token itself as a literal name) — then, if a sub-element was captured and
its schema differs from the owning schema, raises the exact PG error
(`42P15`, `"CREATE specifies a schema (%s) different from the one being
created (%s)"`) *before* registering anything, matching PG's "the whole
statement fails, nothing is created" semantics. When the schemas match (or
there's no sub-element), the schema registers under the resolved name
exactly as before, just now correctly for the `AUTHORIZATION`-only form
too.

**Dispatch-level fallback kept as defense-in-depth**
(`internal/postmaster/dispatch.go`'s `registerCompatNoopSchema`,
`dispatch_extended.go`'s `tryCompatNoopExtended`): threaded `actingRole`/
`sessionUser` through and ported the identical `AUTHORIZATION`-role
resolution / `setSchemaName`-style check at the text level too, so a
`CREATE SCHEMA` spelling that *did* somehow fail `parser.Parse` (none
currently exist, but the parser's grammar coverage is not a hard
guarantee) gets the same behavior rather than silently regressing to the
pre-fix no-op. `schemaQualMismatchError` / `compatNoopSchemaErrorCode` pick
`42P15` over the path's usual generic `errcodes.SystemError` for this one
failure mode.

Verified live: all 15 `^-ERROR` / 2 `^+ERROR` lines are gone —
`scripts/pg-regress-runner.sh -v create_schema`'s diff drops from 95 to 38
lines with zero remaining `ERROR`-line mismatches anywhere in the file.
`go test ./internal/parser/... ./internal/executor/... ./internal/postmaster/...`
all still pass, including the pre-existing
`TestExtendedProtocolCompatNoopSchema` and
`TestPort_CreateSchemaSurvivesRestart`/`TestPort_AlterSchemaSurvivesRestart`
restart-durability tests.

## Remaining gap (why this case is still PARKED)

**goopg has no execution path for `CREATE SCHEMA`'s embedded sub-command
list at all** — the parser deliberately discards every sub-command's body
(column lists, `AS SELECT` query, trigger function, etc.) rather than
building and running the corresponding `CreateTableStmt`/`CreateViewStmt`/
etc. This is the entire remaining 38-line diff: the file's three
schema-matches success cases expect the nested `CREATE TABLE ...` to
actually run (checked via `\d schema.tab` and a `DROP SCHEMA ... CASCADE`
`NOTICE: drop cascades to table ...`), and goopg's `\d` finds nothing and
its `DROP SCHEMA` has nothing to cascade to. Implementing this properly
means, at minimum: threading a real per-sub-command AST (not just a
schema-qualification peek) through `CompatNoopStmt`, transactionally
creating the schema first, then re-dispatching each sub-command through the
normal `CreateTableStmt`/`CreateSequenceStmt`/`CreateViewStmt`/
`IndexStmt`/`CreateTrigStmt` executor paths with the sub-command's implicit
schema filled in wherever it was omitted (mirroring
`parse_utilcmd.c`'s `setSchemaName` default-fill behavior, not just its
mismatch-check half) — genuinely REFACTOR-tier, not a contained fix. This
is also the second of the three independent gaps M0134-0009
(`select_views.sql`, PARKED 2026-08-19,
`docs/design/m0134-0009-session-user-identity.md`) named as blocking that
case: `?#` operator lexing, unary prefix `#`, and `CREATE SCHEMA ...
CREATE TABLE` sub-commands — this loop did not attempt the shared blocker.

## Resume point

Re-run `scripts/pg-regress-runner.sh -v create_schema` after the
sub-command execution subsystem above lands (or after any other engine
change that might incidentally touch it). The parser-level scaffolding
this loop landed (`SchemaAuthRole`/`SchemaSubElementSchema`/
`SchemaHasSubElement` on `CompatNoopStmt`, `captureCreateSchemaSubElementSchema`)
only captures a schema-qualification *peek*, not the sub-command's full
AST — a real implementation will likely want to replace it with genuine
sub-statement parsing (probably reusing `parseCreateTableTail`/
`parseCreateViewTail`/etc. directly from the CREATE SCHEMA arm) rather than
extending the peek incrementally.
