# M0134-0124: `foreign_data.sql` sizing — PARKED

**Status:** PARKED (regress-sql `not-tried` → CSV `failed`, `pass_required=no`).
**Oracle:** `postgres/src/test/regress/sql/foreign_data.sql` /
`postgres/src/test/regress/expected/foreign_data.out` (PG 18.3).
**Live sizing:** `scripts/pg-regress-runner.sh --verbose foreign_data` — 0%
parity, diff ~2460 lines pre-fix (868-line source, the largest M0134 case
sized to date).

## Why this is parked

Unlike the M0134-0122/-0123 event-trigger pair, this file's 0% parity does
**not** come from one dominant blocker. `execCompatNoop`'s (
`internal/executor/operators_ddl.go`) `"foreign-data wrapper"` / `"server"` /
`"user mapping"` CREATE arms are explicitly documented as existing for
pg_dump round-trip fidelity only (DU-002 slices 375-380), not full DDL
semantics — so the file exercises a cluster of independent, differently-
shaped gaps:

1. No duplicate-OPTIONS-key validation on CREATE (only ALTER's
   `applyFDWOptionChanges` has a merge path; CREATE has none).
2. Redundant-clause detection (`HANDLER h HANDLER h2`) — the AST keeps only
   a single last-write-wins `FDWHandlerFunc`, the same shape as the
   `FilterVars`/`FilterVar` bug fixed for `CREATE EVENT TRIGGER` in
   [M0134-0122](m0134-0122-event-trigger-sizing.md).
3. `ALTER FOREIGN DATA WRAPPER foo;` with no clauses should be a syntax
   error; the grammar accepts it.
4. No unrecognized-option validation (`invalid option "%s"` + HINT) for
   FDW/SERVER/USER MAPPING options.
5. No owner tracking on any of the three CREATE arms — `\dew`/`\des` always
   show `postgres` regardless of the connected role, unlike ~10 other
   CREATE-with-owner statements in the same file that already do
   `tbl.Owner = o.ctx.NonSuperuserRole`.
6. `pg_user_mapping` (the unaliased catalog relation, distinct from the
   already-implemented `pg_user_mappings` view) doesn't exist as a queryable
   relation.
7. DROP ROLE / DROP FOREIGN DATA WRAPPER / DROP SERVER CASCADE dependency
   tracking for FDW-owned objects is entirely absent — the same
   `pg_shdepend`-shaped object-enumeration engine already flagged as
   recurring in the [M0134-0118](m0134-0118-dependency-sizing.md)
   (`dependency.sql`) ledger row, now confirmed a third time with FDW
   objects as a new object-kind needing coverage.
8. `ALTER TABLE ... ATTACH PARTITION` doesn't require/propagate a child
   table's NOT NULL constraint the way PG's partition-attach validation
   does — a partitioning-subsystem gap unrelated to FDW that happened to
   surface via this file's `fd_pt2`/`fd_pt2_1` foreign-table-partition
   fixture.

No single fix meaningfully moves parity; the highest-value slice (7) is the
same multi-file REFACTOR-tier feature already deferred twice under
M0134-0117/-0118, not newly bounded by anything found here.

## What landed instead: two authz checks on `CREATE FOREIGN DATA WRAPPER`

Sizing surfaced two independent, engine-independent bugs in the
`"foreign-data wrapper"` CREATE arm, both missing entirely (not merely
divergent):

### 1. Already-exists check

```sql
CREATE FOREIGN DATA WRAPPER foo; -- duplicate
ERROR:  foreign-data wrapper "foo" already exists
```

`catalog.InMemory.RegisterForeignDataWrapper` is deliberately idempotent
(re-registering a name is a silent no-op success) — needed for
recovery-replay callers, not something to change. The guard belongs at the
exec call site instead, mirroring PG's `CreateForeignDataWrapper`
(`postgres/src/backend/commands/foreigncmds.c` ~596-603,
`ERRCODE_DUPLICATE_OBJECT`):

```go
if _, found := im.LookupForeignDataWrapper(s.ObjName.String()); found {
    return &ExecError{Code: "42710", Pos: s.Pos(),
        Message: fmt.Sprintf("foreign-data wrapper %q already exists", s.ObjName.String())}
}
```

### 2. Superuser check — using the real `SUPERUSER` attribute, not the naive convention

```sql
SET ROLE regress_test_role;
CREATE FOREIGN DATA WRAPPER foo; -- ERROR
ERROR:  permission denied to create foreign-data wrapper "foo"
HINT:  Must be superuser to create a foreign-data wrapper.
```

PG's `CreateForeignDataWrapper` (`foreigncmds.c` ~586-591) has this check;
goopg's arm had none at all. The obvious fix — mirroring every sibling
privilege check in this file (LEAKPROOF, `CREATE EVENT TRIGGER`) —
would be:

```go
if o.ctx.NonSuperuserRole != "" { /* deny */ }
```

This is wrong here and was caught live: the file's own fixture does

```sql
CREATE ROLE regress_foreign_data_user LOGIN SUPERUSER;
SET SESSION AUTHORIZATION 'regress_foreign_data_user';
CREATE FOREIGN DATA WRAPPER dummy;   -- must succeed
```

`NonSuperuserRole` is set to the SET SESSION AUTHORIZATION target for *any*
non-`"postgres"` role name (`internal/postmaster/dispatch.go`'s
`SetSessionAuthorization` closure), regardless of whether that role actually
carries `SUPERUSER`. The naive convention every sibling check uses therefore
incorrectly denies a `CREATE ROLE ... SUPERUSER` role the moment it isn't
literally named `"postgres"` — this is a real, pre-existing gap shared by
every other site using the same convention (not introduced by this loop; see
Deferred, below).

Fixed by consulting the role's actual attribute instead:

```go
if role := o.ctx.NonSuperuserRole; role != "" {
    oid, ok := im.RoleOID(role)
    if !ok || !im.IsSuperuser(oid) {
        return &ExecError{Code: "42501", Pos: s.Pos(),
            Message: fmt.Sprintf("permission denied to create foreign-data wrapper %q", s.ObjName.String()),
            Hint:    "Must be superuser to create a foreign-data wrapper."}
    }
}
```

`catalog.InMemory.RoleOID`/`IsSuperuser` already exist (used elsewhere for
role-membership authorization, `internal/catalog/catalog.go`). This is
scoped to this one call site — the sibling checks (LEAKPROOF, event
trigger) still use the naive convention; see Deferred.

### Verification

- `go build ./...` clean.
- New unit tests `TestCreateForeignDataWrapperDuplicateErrors` /
  `TestCreateForeignDataWrapperSuperuserCheck`
  (`internal/executor/operators_ddl_foreign_data_wrapper_authz_test.go`) —
  the latter exercises both a `CREATE ROLE ... SUPERUSER`-attributed
  non-`"postgres"` role (must succeed) and a genuine non-superuser role
  (must be denied with 42501 + HINT, and must NOT register the FDW).
- `go test ./internal/executor/... ./internal/catalog/...` and
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` all pass.
- Live diff stayed roughly flat post-fix (2460 → 2468 lines): a *third*,
  still-open gap (item 1 above — no duplicate-OPTIONS-key validation) lets
  an earlier CREATE that PG rejects but goopg silently accepts leave a
  same-named FDW registered, which the new already-exists check then (also
  correctly) flags on the next statement — a downstream symptom of an
  already-cataloged gap, not a regression. Confirmed via a stash/pop A-B:
  every line that changed direction was already diverging before the fix,
  just with different wrong text (same technique as
  [M0134-0118](m0134-0118-dependency-sizing.md)).

## Resume point (deferred)

Full detail in `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0124. Priority
order for a future pass:

1. **The `pg_shdepend`-shaped dependency/CASCADE engine** (item 7) — third
   recurrence (M0134-0117/-0118/-0124). Resume at
   `catalog.InMemory.RoleDropDependencyDescriptions`
   (`internal/catalog/catalog.go`, added in M0134-0118), extending it to
   enumerate FDW/server/user-mapping ownership + ACL-grant chains, and port
   PG's actual CASCADE object-graph walk (not just DROP-blocked-without-
   CASCADE detection) for `DROP SERVER/FOREIGN DATA WRAPPER ... CASCADE`'s
   `NOTICE: drop cascades to ...` chain plus the actual removal.
2. `pg_user_mapping` (item 6) — likely a small addition as a thin
   alias/subset of the existing `pg_user_mappings` virtual-view builder.
3. Items 1-4 (option-duplicate/redundant-clause/ALTER-syntax/unrecognized-
   option validation) are all small, independent, `foreigncmds.c`-adjacent
   additions in the same `execCompatNoop` arms this loop already touched —
   good next-loop pickups that don't need the dependency engine.
4. Items 5/8 (owner tracking; partition-attach NOT NULL propagation) are
   single-file, no cross-dependency.

## See also

- [M0134-0118](m0134-0118-dependency-sizing.md) — where the
  `pg_shdepend`-shaped dependency engine gap was first identified and
  partially addressed (table ACL/ownership only).
- [M0134-0122](m0134-0122-event-trigger-sizing.md) — the `FilterVars`
  multi-value-AST-field pattern item 2 above should mirror.
