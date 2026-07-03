# 0119-0004co — DDL CommandComplete tag fidelity

## Status

Accepted / landed (loop #77).

## Problem

`internal/server/dispatch.go`'s `ddlTag`/`commandTagFor` build the
wire-protocol `CommandComplete` tag string every DDL statement returns to the
client (`PQcmdStatus`/psql's `CREATE TABLE` echo, `libpq`'s dispatch on tag
prefix, etc.). A cluster of DDL forms — mostly ones added incrementally for
pg_dump round-tripping under M0119-0004/DU-002 — never got a correct,
object-specific tag:

- `ddlTag`'s type switch had no case for `*parser.CreateOpClassStmt`
  (`CREATE OPERATOR CLASS ...`) or `*parser.DropCompatStmt` (the shared stub
  for `DROP OPERATOR CLASS/FAMILY/SEQUENCE/SCHEMA/ROLE/...`), so both fell
  through to the final `return "OK"`.
- `internal/parser/ddl.go`'s `parseSkipToSemicolon` — the shared fallback
  parser for several `CREATE X` forms — unconditionally returns
  `&CompatNoopStmt{Tag: "CREATE"}`. Callers (`parseCreateOpFamilyTail`,
  `parseCreateConversionTail`, the inline `CREATE OPERATOR` and
  `CREATE TEXT SEARCH ...` arms) set `ObjType` afterward but never
  overrode `Tag`, so `CREATE OPERATOR FAMILY`, `CREATE CONVERSION`,
  `CREATE OPERATOR`, and all four `CREATE TEXT SEARCH ...` variants all
  reported a bare `CREATE` tag.
- Three more `CompatNoopStmt` literals hardcoded `Tag: "CREATE"` directly:
  `CREATE SERVER`, `CREATE USER MAPPING`, `CREATE FOREIGN DATA WRAPPER`.
- `ALTER FOREIGN DATA WRAPPER ... OPTIONS (...)` hardcoded `Tag: "ALTER"`
  (bare) instead of `"ALTER FOREIGN DATA WRAPPER"` — pinned by a pre-existing
  test (`TestParseAlterForeignDataWrapperOptions`) that asserted the wrong
  value, i.e. the bug was test-locked, not merely untested.

This was first surfaced (for the `CREATE OPERATOR FAMILY` instance only) by
the loop #41 ledger row (2026-07-01) and explicitly deferred as "cosmetic
only ... not investigated further this loop." No subsequent loop (through
loop #76) revisited it or the sibling instances.

This does not affect `pg_dump` output text (pg_dump doesn't read
`CommandComplete` tags), which is why the DU-002 slice battery
(`TestPort_PgDumpConnectionSetup`) never caught it — it is a separate
wire-protocol-fidelity axis (`PQcmdStatus`, extended-protocol
`CommandComplete`, any client that branches on the tag string).

## Oracle

`postgres/src/include/tcop/cmdtaglist.h` is PostgreSQL's authoritative
tag table; `postgres/src/backend/tcop/utility.c`'s `CreateCommandTag`
derives the tag purely from the parsed statement's node type (not the
surface keyword used), which is why `DROP ROLE`/`DROP USER`/`DROP GROUP`
(all parsed into `T_DropRoleStmt`) share one tag, `CMDTAG_DROP_ROLE`
(`utility.c:2975-2976`).

## Fix

**Parser side** (`internal/parser/ddl.go`): set the real PG tag at each
`CompatNoopStmt`-producing call site instead of leaving the generic
`"CREATE"`/`"ALTER"` placeholder:

- `CREATE OPERATOR` → `"CREATE OPERATOR"`
- `CREATE TEXT SEARCH {DICTIONARY,CONFIGURATION,PARSER,TEMPLATE}` →
  `"CREATE " + strings.ToUpper(tsType)` (tsType is already the lowercase
  two/three-word object phrase, e.g. `"text search dictionary"`)
- `CREATE SERVER` → `"CREATE SERVER"`
- `CREATE USER MAPPING` → `"CREATE USER MAPPING"`
- `CREATE FOREIGN DATA WRAPPER` → `"CREATE FOREIGN DATA WRAPPER"`
- `ALTER FOREIGN DATA WRAPPER ... OPTIONS (...)` → `"ALTER FOREIGN DATA
  WRAPPER"`
- `parseCreateOpFamilyTail` → `"CREATE OPERATOR FAMILY"`
- `parseCreateConversionTail` (covers both `CREATE CONVERSION` and
  `CREATE DEFAULT CONVERSION`, which share PG's `CMDTAG_CREATE_CONVERSION`
  regardless of the `DEFAULT` keyword) → `"CREATE CONVERSION"`

**Server side** (`internal/server/dispatch.go`):

- `ddlTag` gained a `*parser.CreateOpClassStmt` case →
  `"CREATE OPERATOR CLASS"`.
- New `dropCompatTags map[string]string` keyed by `DropCompatStmt.ObjType`
  (the full set of object-type strings the DROP-fallback parser loop
  produces: `database`, `foreign table`, `foreign-data wrapper`,
  `user mapping`, `aggregate`, `operator class`, `operator family`,
  `operator`, the four `text search *` variants, `cast`, `transform`,
  `sequence`, `schema`, `collation`, `materialized view`, `extension`,
  `server`, `language`, `access method`, `event trigger`, `group`, `role`,
  `user`, `conversion`), each mapped to its real PG tag. `group`/`role`/
  `user` all map to `"DROP ROLE"` per the oracle behavior above. `ddlTag`
  consults this map when the statement is a `*parser.DropCompatStmt`.

Both the simple-query (`dispatch.go`) and extended-protocol
(`dispatch_extended.go`) paths call the same `commandTagFor`/`ddlTag`, so
this is a single chokepoint fix, not a sibling-path change — no risk of the
two protocols diverging on this axis.

**Sibling-path fix required in the executor** (found during pre-commit
verification, not by the original loop #77 pass): `internal/executor/
operators_ddl.go`'s `execCompatNoop` gates its `ALTER FOREIGN DATA WRAPPER
... OPTIONS (...)` merge branch on `s.Tag == "ALTER"` — the exact bare
placeholder this loop replaced with `"ALTER FOREIGN DATA WRAPPER"` at the
parser call site. Changing the parser's tag without updating this string
match silently broke the branch: `ALTER FOREIGN DATA WRAPPER` became a
complete no-op (options never merged, `nosuch`-target errors never raised)
because the guard no longer matched anything. Updated the guard to
`s.Tag == "ALTER FOREIGN DATA WRAPPER"`. This class of bug (parser Tag
string and executor Tag string-match drifting apart) is exactly the
"sibling paths must change together" trap the project's practice cards flag
— caught here only because `TestAlterForeignDataWrapperOptionsRoundtrip`/
`TestAlterForeignDataWrapperOptionsErrors` were explicitly re-run before
commit rather than trusting the original loop's PASS claim.

## Tests

- `internal/server/ddl_command_tag_test.go` (new):
  `TestDDLCommandTagMatchesPostgres` — 20 table-driven cases parsing real SQL
  end-to-end through `parser.Parse` and asserting `ddlTag` on the result,
  covering every fixed site (`CREATE OPERATOR CLASS/FAMILY/OPERATOR`,
  `CREATE [DEFAULT] CONVERSION`, `CREATE TEXT SEARCH
  DICTIONARY/CONFIGURATION`, `CREATE SERVER/USER MAPPING/FOREIGN DATA
  WRAPPER`, `ALTER FOREIGN DATA WRAPPER`, and a representative spread of
  `DropCompatStmt` object types including the `DROP ROLE/USER/GROUP`
  three-way tag collapse).
- `internal/parser/ddl_test.go`'s pre-existing
  `TestParseAlterForeignDataWrapperOptions` asserted the bug (`Tag ==
  "ALTER"`) as expected behavior — corrected to assert `"ALTER FOREIGN DATA
  WRAPPER"`.

`internal/executor`'s `TestAlterForeignDataWrapperOptionsRoundtrip`/
`TestAlterForeignDataWrapperOptionsErrors` (pre-existing, loop #58) caught
the sibling-path regression above once actually re-run pre-commit — both
failed (`ADD` silently not applied; `nosuch`-target no longer 42704'd)
until `execCompatNoop`'s guard was updated to match the new tag string.

## Gates

`go build ./...`/`go vet ./...` clean; `internal/parser` suite PASS (no
other test pinned a wrong tag); `internal/server` suite PASS; new
`TestDDLCommandTagMatchesPostgres` PASS (20/20); `TestPort_PgDumpConnectionSetup`
PASS (unaffected — pg_dump doesn't read CommandComplete tags); full
`internal/executor`/`internal/catalog`/`internal/wal`/`internal/initdb`
suites PASS, including (after the sibling-path fix above)
`TestAlterForeignDataWrapperOptionsRoundtrip`/
`TestAlterForeignDataWrapperOptionsErrors`; `scripts/tpch-spotcheck.sh`
PASS; pgbench smoke = pre-commit hook.

## Deferred

None — this closes the full class of bare/incorrect DDL command tags
findable by static review of `ddlTag` and every `CompatNoopStmt`/
`DropCompatStmt` literal in `internal/parser/ddl.go`. If a future DDL form
is added via the same `CompatNoopStmt`/`DropCompatStmt` stub pattern, the
new call site must set its own correct `Tag` (or add a `dropCompatTags`
entry) at the same time — there is no automatic derivation, so this is a new
per-site obligation, not a closed generic mechanism.
