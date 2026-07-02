# 0119-0004bh — `ALTER COLLATION … RENAME TO / OWNER TO / REFRESH VERSION`

Status: accepted
Milestone: M0119-0004 (pg_dump getter-battery parity; CSV row DU-002)

## Problem

CREATE/DROP COLLATION (slice 389) and COMMENT ON COLLATION (slice 390) already
round-trip a user collation through pg_dump, and loop #50 added WAL-record
restart persistence for CREATE/DROP. The deferral ledger (loop #50 row, dated
2026-07-01) records the remaining gap: **`ALTER COLLATION` is entirely
unhandled** — no parser branch exists for the `collation` keyword inside
`parseAlter()` at all, so `ALTER COLLATION name RENAME TO / OWNER TO / REFRESH
VERSION` falls through to a parse error today.

## Oracle (PG 18.3)

`AlterCollation()` in `postgres/src/backend/commands/collationcmds.c:423-503`
handles only the `REFRESH VERSION` form (RENAME/OWNER TO/SET SCHEMA route
through the generic `AlterObjectRename_internal` /
`AlterObjectOwner_internal` / `AlterObjectNamespace` paths used by every
renameable/ownable object, not collation-specific code):

- `REFRESH VERSION` is rejected on the `default` collation (OID 100) with a
  hint to use `ALTER DATABASE … REFRESH COLLATION VERSION` instead, and
  requires ownership (`ACLCHECK_NOT_OWNER`).
- It computes `newversion` via `get_collation_actual_version()`, which reads
  the *current* ICU/glibc collation version for the provider (returns NULL if
  the platform can't report one, e.g. non-glibc libc — this is not an error).
- If old ≠ new: `NOTICE: version has not changed` is **not** emitted; instead
  `NOTICE: changing version from %s to %s` + an `UPDATE pg_collation SET
  collversion = ...` catalog write.
- If old == new (including both NULL): `NOTICE: version has not changed for
  collation "%s"`, no catalog write.

goopg has no real ICU/glibc binding behind a user collation — the registry
only models the dump round-trip (`CollationAttrsByName`,
`docs/design/0119-0004-index-column-collation-roundtrip.md`-adjacent slices).
There is nothing to "actually" version, so the faithful minimal behavior is
the platform-can't-report-a-version branch: always emit `NOTICE: version has
not changed for collation "%s"` and perform no catalog write. This matches a
real, common PG deployment (any non-glibc/non-ICU host) rather than
inventing a fake version number.

## Change (bounded to the in-memory registry; no restart persistence)

Mirrors the already-landed `ALTER STATISTICS`/`ALTER AGGREGATE … RENAME`
shapes (`internal/parser/ddl.go` — `AlterStatisticsStmt` tail-consuming
pattern, `AlterAggregateRenameStmt` RENAME TO pattern) and the existing `ALTER
TABLE … OWNER TO` executor mutation (`operators_ddl.go:5909-5938`, a direct
in-memory field write with no WAL emission — the same precedent this change
follows for scope).

1. **Parser** (`ast.go`): new `AlterCollationStmt{pos, Name ObjectName,
   IfExists bool, Action string, NewName string, NewOwner string}`. `Action`
   is one of `"rename"`, `"owner"`, `"refresh"`; any other trailing form (e.g.
   `SET SCHEMA`) is consumed as a no-op with `Action == ""` (mirrors
   `AlterStatisticsStmt`'s unmodelled-form handling) so the statement never
   fails to parse.

2. **Parser** (`ddl.go` `parseAlter`): new `p.acceptIdentKeyword("collation")`
   branch (collation is currently entirely absent from `parseAlter`'s
   if-chain) parsing `[IF EXISTS] name` then dispatching on
   `RENAME TO <ident>` / `OWNER TO <role|CURRENT_USER|SESSION_USER|
   CURRENT_ROLE>` / `REFRESH VERSION` (`acceptIdentKeyword("refresh")` +
   `acceptIdentKeyword("version")`, mirroring how `VERSION` is already parsed
   as a plain unreserved identifier elsewhere in this file) else consumes to
   the statement terminator.

3. **Catalog** (`catalog.go`): two new `InMemory` mutators next to
   `CreateCollation`/`DropCollation` (~line 8622): `RenameCollation(oldName,
   newSchema-resolved-schema, newName string) error` (collision-checks the
   new name within the same namespace, same shape as `RenameEnum`) and
   `SetCollationOwner(name, schema string, ownerOID uint32) bool`. Both
   operate only on `userCollations` — a target naming a built-in collation
   (not in the registry) reports "not found", matching PG's refusal to
   rename/own a pinned catalog row (different error text, acceptable: no
   spec exercises this).

4. **Executor** (`operators_ddl.go`): dispatch `case
   *parser.AlterCollationStmt: return nil, o.execAlterCollation(s)`.
   `execAlterCollation` resolves `IF EXISTS`/not-found the same way
   `execDropCompat`'s `"collation"` block does (`42704 collation "%s" does not
   exist`); on `"rename"` calls `RenameCollation`; on `"owner"` resolves the
   role name via the existing `catalog.RoleOID` (falling back to the
   bootstrap superuser OID 10 for the `CURRENT_USER` sentinel, same mapping
   `ALTER TABLE … OWNER TO` uses) and calls `SetCollationOwner`; on
   `"refresh"` calls `o.ctx.AddNotice(fmt.Sprintf("version has not changed
   for collation %q", s.Name.Name))` and does nothing else (no error, no
   catalog write — matches the non-glibc/non-ICU oracle branch above).

5. **Planner** (`planner.go`) / **command tag** (`dispatch.go` `ddlTag`):
   add `*parser.AlterCollationStmt` to the DDL passthrough list and
   `"ALTER COLLATION"` tag, mirroring every sibling ALTER statement.

### Restart persistence (landed in a follow-up loop, not deferred anymore)

RENAME TO / OWNER TO are now WAL-logged: `RecordKindAlterCollationRename`
(44) / `RecordKindAlterCollationOwner` (45) in `internal/wal/recovery.go`,
emitted from `execAlterCollation`'s two mutation call sites in
`operators_ddl.go`, and replayed by two new cases in
`replayCollationDDLRecords` (`internal/initdb/collation_ddl_recovery.go`)
via `RenameCollationDuringRecovery`/`SetCollationOwnerDuringRecovery`
(`catalog.go`, discard-result recovery counterparts mirroring
`DropCollationDuringRecovery`). This still does **not** extend to `ALTER
TABLE RENAME`/`OWNER TO`, which remain in-memory-only — that gap is
unrelated and out of scope here.

### Explicitly out of scope (ledger follow-up, not silently dropped)

- **pg_dump round-trip of the ALTER itself is not separately tested** beyond
  unit coverage of the registry mutation: pg_dump always dumps a collation's
  **final** state as a single `CREATE COLLATION` (+ `ALTER … OWNER TO` only
  when the owner differs from the restoring user), and the existing
  `CollationAttrsByName`/`getCollations` projection already reads whatever
  `Owner`/`Name` a collation currently carries — no separate dump-side change
  is needed for `RENAME`/`OWNER TO` to be reflected in a subsequent dump.
- **`REFRESH VERSION`'s catalog write path** (`collversion` column) does not
  exist in `UserCollation` and is not added — goopg's registry has nothing to
  meaningfully version, so the no-catalog-write NOTICE-only branch above is
  the terminal, not partial, behavior for this milestone.

## Gates

- New parser tests: `TestParseAlterCollationRename`,
  `TestParseAlterCollationOwner`, `TestParseAlterCollationRefreshVersion`.
- New executor test: `TestAlterCollationRenameOwnerRefresh` (rename updates
  `CollationAttrsByName`; owner updates `Owner`; refresh is a no-op notice,
  no error; `IF EXISTS` on an unknown name is a no-op, without it raises
  `42704`).
- Restart-persistence follow-up: `TestEncodeDecodeAlterCollationRenameRoundTrip`
  / `…OwnerRoundTrip` + reject-wrong-kind/truncated-payload guards
  (`internal/wal/collation_ddl_test.go`); `TestCollationDDLRecoveryReplaysRenameAfterCreate`
  / `…OwnerAfterCreate` (`internal/initdb/collation_ddl_recovery_test.go`,
  full close→reopen→replay round trip, OID preserved across rename).
- `go build ./...` clean; `go vet` parser/catalog/executor/wal/initdb clean;
  full `internal/parser` + `internal/catalog` + `internal/executor` +
  `internal/wal` + `internal/initdb` suites PASS; `go test -race
  ./internal/wal/...` clean (WAL/recovery practice card).
- Deferral ledger: flip the loop #50 "ALTER COLLATION … still unhandled" row
  to `resolved` for the RENAME/OWNER/REFRESH scope landed here; the
  restart-persistence follow-up recorded as its own row is now resolved by
  this change.
