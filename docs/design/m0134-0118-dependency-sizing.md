# M0134-0118 — `dependency.sql`: sizing + `DROP ROLE` dependency-check port

**Status:** PARKED (`failed`). Two contained fixes landed, taking the case
from 187 diff lines / 10 `^-ERROR` / 16 `^+ERROR` to 156 diff lines / 5
`^-ERROR` / 14 `^+ERROR` (0% parity throughout — the residual diff needs a
pg_shdepend-shaped `DROP OWNED BY`/`REASSIGN OWNED BY` engine, a
multi-milestone feature, not a contained fix).

## Oracle case

`postgres/src/test/regress/sql/dependency.sql` exercises PostgreSQL's shared
(cross-database) dependency tracking: `DROP ROLE`/`DROP USER`/`DROP GROUP`
refusing to drop a role that still holds table/database ACL grants or owns an
object, `DROP OWNED BY <role>` (dropping every object a role owns, with a
privilege gate requiring `ADMIN OPTION`-equivalent membership of the target
role), and `REASSIGN OWNED BY <role> TO <role>` (re-pointing ownership of
every object a role owns to another role, same privilege gate). It finishes
with a composite-type/relation ownership consistency check
(`typowner = relowner` after a `REASSIGN OWNED`).

Sized live via `scripts/pg-regress-runner.sh --verbose dependency` against
the PG 18.3 oracle. To get an apples-to-apples before/after measurement
unaffected by the fix itself, the fix was stashed (`git stash push -- <4
touched files>`), the case re-run for a clean baseline, then popped back and
re-run:

- **Before:** 187-line diff, 10 `^-ERROR`, 16 `^+ERROR`.
- **After:** 156-line diff, 5 `^-ERROR`, 14 `^+ERROR`.

No new false positives were introduced — every diff line that superficially
looks "worse" after the fix (a `DROP USER` that now fails where it silently
succeeded before) was already wrong before the fix too, just failing to
error at all instead of erroring with an incomplete DETAIL (see "Residual
gap" below).

## Root cause #1: `DROP ROLE`/`USER`/`GROUP` never checked dependencies

goopg's `DROP ROLE`/`DROP USER`/`DROP GROUP` always succeeded unconditionally
as long as the role existed (or `IF EXISTS` covered a missing one). PG's real
`DropRole` (`postgres/src/backend/commands/user.c`) calls
`checkSharedDependencies` (`postgres/src/backend/catalog/pg_shdepend.c`
~660-870) first, which scans `pg_shdepend` for every
`SHARED_DEPENDENCY_ACL` (a live GRANT naming the role as grantee) and
`SHARED_DEPENDENCY_OWNER` (the role owns some object) row referencing the
role, and refuses the drop with:

```
ERROR:  role "<name>" cannot be dropped because some objects depend on it
DETAIL:  privileges for table <table>
owner of table <table>
...
```

(each dependency on its own DETAIL line, `storeObjectDescription`,
pg_shdepend.c ~1276-1310).

### Where the fix actually had to land

The first attempt wired the check into `internal/postmaster/role_ddl.go`'s
`tryHandleRoleDDL` "drop role/user/group" case — the same file that handles
`CREATE`/`ALTER ROLE`. Live verification showed this was **dead code for
`DROP`**: `dispatch.go`'s comment on `ectx.OnRoleDropped` already documented
why —

> `DROP ROLE parses as a generic DropStmt, bypassing tryHandleRoleDDL`

Unlike `CREATE`/`ALTER ROLE` (which the parser genuinely cannot parse, so
`tryHandleRoleDDL` only ever runs on the `parser.Parse` failure path in
`dispatch.go`), `DROP ROLE`/`USER`/`GROUP` parses fine as a generic
`parser.DropCompatStmt` (`internal/parser/ddl.go` line ~6289 lists
`"group", "role", "user"` as recognised `DropCompatStmt` object types) and is
executed entirely by `internal/executor/operators_ddl.go`'s
`execDropCompat`'s `objType == "user" || objType == "role" || objType ==
"group"` arm. `tryHandleRoleDDL`'s DROP case only still fires for tests that
call it directly — a pre-existing test-only survivor of an earlier refactor,
left in place with a new comment explaining the split rather than deleted
(no behavior change to trim there).

### The port: `RoleDropDependencyDescriptions`

`internal/catalog/catalog.go` gained
`(*InMemory) RoleDropDependencyDescriptions(roleName string) []string`, a
bounded port of `checkSharedDependencies` covering exactly the two
dependency shapes goopg's existing registries can answer today:

- **ACL grants** (`SHARED_DEPENDENCY_ACL`): scans the OID-keyed `tableACLs`
  store — shared between table ACLs and database ACLs, per
  `execDatabaseACLChange`'s doc comment ("datacl shares the same
  tableACLs/tableACLGrantor store via relaclTextLockedFor") — for any entry
  keyed by `roleName` with at least one recorded privilege. **Pitfall hit
  live:** a privilege's boolean value in `tableACLs[oid][role][priv]` records
  whether it was granted `WITH GRANT OPTION`, not whether it is held at all
  — the map *key*'s presence is the grant (an empty inner map is deleted
  entirely by `RevokeTablePrivilege`). The first implementation iterated the
  booleans looking for a `true` and silently found nothing (every ordinary
  grant records `false` there), so the check never fired; fixed to test
  `len(privs) > 0` instead.
- **Table ownership** (`SHARED_DEPENDENCY_OWNER`, tables only): scans
  `AllTables()` for `Table.Owner == roleName`.

Each match renders PG's exact `storeObjectDescription` text ("privileges for
table X" / "privileges for database X" / "owner of table X"), returned in
OID order — a reasonable approximation of PG's
`shared_dependency_comparator` sort for the single-object-type cases this
covers, but not a full port of that comparator (see "Residual gap").

Wired into `execDropCompat`'s role arm
(`internal/executor/operators_ddl.go`): when the role exists, the dependency
check runs before the `IF EXISTS`/plain-drop branch, raising `2BP01` with a
newline-joined DETAIL on any hit — mirroring PG's ordering, where `IF
EXISTS` only suppresses the "does not exist" case, not the dependency check.

## Root cause #2: `GRANT/REVOKE ... TO/FROM GROUP <role>` mis-recorded the role name

Verifying the fix live against the file's first block (`GRANT SELECT ON
TABLE deptest TO GROUP regress_dep_group; ... DROP GROUP regress_dep_group`)
exposed a second, independent bug: `DROP GROUP regress_dep_group` still
succeeded even though the dependency check was correctly wired and the GRANT
had visibly taken effect (confirmed via `\z deptest`).

Root cause: `internal/postmaster/grant_ddl.go`'s `tryRecordTableGrant` (and
its `REVOKE`/schema/function/foreign-server/foreign-data-wrapper siblings)
split the post-`TO`/`FROM` role list with the generic `splitGrantList`, which
has no awareness of PostgreSQL's grammar-level legacy `GROUP` keyword
(`gram.y`'s `RoleSpec: ... | GROUP_P role_name` — accepted before *any*
individual role name in the list, purely for backward compatibility with
pre-8.1 syntax; it is a no-op keyword, not a distinct role class). So `TO
GROUP regress_dep_group` recorded the grant under the literal string
`"group regress_dep_group"` — invisible to a later `REVOKE ... FROM GROUP
regress_dep_group` or the new `DROP GROUP` dependency check, both of which
look up the bare role name.

Fixed with a new `splitRoleGrantList` (`internal/postmaster/grant_ddl.go`):
`splitGrantList` plus a per-item `cutLeadingKeyword(v, "group")` strip,
swapped in at all 10 `roles := splitGrantList(rolePart)` call sites in the
file (GRANT/REVOKE on TABLE/SEQUENCE/SCHEMA/FUNCTION/PROCEDURE/ROUTINE/
FOREIGN SERVER/FOREIGN DATA WRAPPER all share the same role-list parsing
shape, so the keyword can legally appear before any of them, not just table
grants).

## Verification

Live, byte-for-byte against the oracle: the file's first ~30 lines (every
plain ACL-dependency and table-ownership-dependency `DROP ROLE`/`USER`/
`GROUP` case, including the `GRANT`/`REVOKE ... TO/FROM GROUP` forms) now
match exactly — this was the entire first diff hunk before the fix (diff
lines 8-42 of the original 187-line diff) and is gone from the after-diff.

Gates run:

```
go build ./...
go test ./internal/parser/... ./internal/executor/... ./internal/postmaster/... ./internal/catalog/...
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh
```

All pass.

## Residual gap (why this case is still PARKED)

Three independent gaps remain in the 156-line residual diff, none landed
this loop:

1. **`DROP OWNED BY <role>` / `REASSIGN OWNED BY <role> TO <role>` are
   entirely unparsed.** `internal/executor/cmdtag_table.go` carries the
   command tags (`"DROP OWNED"`, `"REASSIGN OWNED"`) but no grammar arm
   exists for either statement — both are hard syntax errors at the wire
   layer (`ERROR: syntax error at or near "expected TABLE, INDEX, VIEW, ...
   or TRIGGER after DROP (got owned)"` / `"unsupported statement (got
   reassign)"`). This blocks the entire back half of the file: the
   permission-check cases (both statements gate on `ADMIN OPTION`-equivalent
   membership of the target role — PG's `has_privs_of_role`), the actual
   object-reassignment cases (schema/table/function/type/sequence ownership
   handoff), and the composite-type `typowner = relowner` consistency
   check that depends on a successful `REASSIGN OWNED` having run first.
   PG's `DropOwnedObjects`/`ReassignOwnedObjects`
   (`postgres/src/backend/commands/user.c`) both walk every object-type
   catalog the role could own via a `pg_shdepend`-anchored scan — a
   multi-milestone object-enumeration engine goopg does not have, not a
   bounded fix.

2. **`RoleDropDependencyDescriptions` is table-only.** It has no schema,
   function, type, or sequence ownership lookup, and no default-privilege
   (`ALTER DEFAULT PRIVILEGES`) ownership tracking. So once the file's later
   `SET SESSION AUTHORIZATION regress_dep_user1` block creates a schema, a
   function, two types, and two more tables owned by `regress_dep_user1`,
   `DROP USER regress_dep_user2` (which by then owns most of those objects,
   post-`REASSIGN OWNED` in real PG — a step goopg can't execute per gap #1)
   still only reports the table-shaped dependencies it can see, not the
   schema/function/type/sequence ones PG's oracle DETAIL lists. This will
   need per-object-kind owner lookups threaded in as each kind's ownership
   API stabilizes (several already exist, e.g. `SchemaOwnerOID`,
   `SetUserAggregateOwner` — just need wiring into a shared enumeration
   point).

3. **DETAIL ordering is only OID-order, not PG's real
   `shared_dependency_comparator`.** Irrelevant to the cases now passing
   (each has only one dependency, or dependencies of only one object type),
   but will matter once gap #2 is closed and a single DROP can surface both
   ACL and multi-kind ownership dependencies together — PG sorts by
   `(classid, objid)` across every SHARED_DEPENDENCY_* type at once, not
   per-bucket.

## Resume point

1. Highest-value next slice: a `DROP OWNED BY`/`REASSIGN OWNED BY` grammar
   arm (start with a bypass-layer text intercept mirroring
   `internal/postmaster/role_ddl.go`'s pattern for a fast first cut, or a
   proper `gram.y`-mirroring grammar rule for a cleaner long-term fit) plus
   an executor that walks each ownable object kind, reusing
   `RoleDropDependencyDescriptions`'s enumeration groundwork (same OID
   sources, opposite action — rewrite the owner/drop the object instead of
   just reporting it).
2. Extend `RoleDropDependencyDescriptions` with schema/function/type/
   sequence owner lookups as each is wired into the shared enumeration
   point built for #1.
3. Only after #2: port `shared_dependency_comparator`
   (`postgres/src/backend/catalog/pg_shdepend.c`) for byte-exact multi-kind
   DETAIL ordering.

Re-run `scripts/pg-regress-runner.sh --verbose dependency` after any of the
above lands.
