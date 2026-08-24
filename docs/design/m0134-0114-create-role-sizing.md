# M0134-0114 — `create_role.sql`: sizing + CREATEROLE-attribute-giveaway fix

**Status:** PARKED (`failed`, 0% parity). Two contained fixes landed; the
remaining gap is a single large subsystem (ownership/ADMIN-option-scoped
`ALTER ROLE`/`DROP ROLE`/object-ownership enforcement) plus several smaller
independent items, none of which is contained enough for this loop's budget.

## Oracle case

`postgres/src/test/regress/sql/create_role.sql` exercises role-privilege
enforcement end to end: a `CREATEROLE`-only (non-superuser) role's ability
to create/alter roles with `SUPERUSER`/`REPLICATION`/`BYPASSRLS`/`CREATEDB`
attributes it does or doesn't itself hold, role-membership grants requiring
`ADMIN OPTION` (including on the built-in `pg_*` predefined roles), object
ownership checks (`DROP`/`ALTER ... OWNER TO` on tables/indexes/views a
different `CREATEROLE` user doesn't own), `REASSIGN OWNED BY`, the
`createrole_self_grant` GUC and its interaction with `GRANT ... WITH
INHERIT/SET`, and `DROP ROLE` self-protection (can't drop yourself, a
superuser, or a role you lack `ADMIN OPTION` on).

Sized live via `scripts/pg-regress-runner.sh -v create_role` against the PG
18.3 oracle: 259-line diff before any fix, 204 lines after the two fixes
below (21% reduction), still 0% parity (single-test file, so parity is
binary).

## Landed this loop

goopg's `CREATE ROLE`/`ALTER ROLE`/`DROP ROLE` are handled entirely outside
the parser/executor, by a text-substitution intercept
(`internal/postmaster/role_ddl.go`'s `tryHandleRoleDDL`) that runs before
`parser.Parse` even gets a chance (the parser has no `CREATE ROLE` grammar
at all). That handler had **zero privilege enforcement**: any session,
regardless of its own role attributes, could freely create or alter a role
with `SUPERUSER`/`CREATEDB`/`REPLICATION`/`BYPASSRLS`, silently succeeding
where PG raises `42501 permission denied to create/alter role`.

**Fix 1 — `CREATE ROLE` attribute-giveaway gate** (`checkCreateRolePrivileges`,
`internal/postmaster/role_ddl.go`), porting `CreateRole`'s "Check some
permissions first" block (`postgres/src/backend/commands/user.c` ~313-343):
when the acting session is not the bootstrap superuser, it must itself hold
`CREATEROLE` to create any role at all, and beyond that may only hand out
`SUPERUSER` (never, regardless of the actor's own attributes — matching
user.c's unconditional `if (issuper)`, the one attribute with no "unless you
have it yourself" escape) or `CREATEDB`/`REPLICATION`/`BYPASSRLS` (each
gated on the actor holding that exact attribute) to the new role.

**Fix 2 — `ALTER ROLE` attribute-touch gate** (`checkAlterRoleAttrPrivileges`,
same file), porting the simple (non-ownership-scoped) half of `AlterRole`'s
gate (user.c ~757-816): touching `SUPERUSER` **at all** — `SUPERUSER` or
`NOSUPERUSER`, regardless of the target role's current superuser status —
always requires the acting session itself be superuser; once past that,
touching `CREATEDB`/`REPLICATION`/`BYPASSRLS` in either polarity requires
the acting session hold that same attribute itself. Attribute-touch
detection reuses `applyRoleAttrOptions`' own substring convention (`"
createdb"` / `" nocreatedb"` etc.) via a new `alterRoleTouchesAttr` helper,
so the two functions can't drift out of sync on what counts as "this clause
appeared in the statement."

**Plumbing:** `tryHandleRoleDDL` gained a variadic `actingRole ...string`
trailing parameter (omitting it — as ~30 pre-existing direct-call unit
tests do — defaults to `""`, the `connTx.NonSuperuserRole` convention for
"the bootstrap superuser," which bypasses every new check exactly as those
tests' behavior already assumed; only the two real wire-dispatch call sites,
`dispatch.go` and `dispatch_extended.go`, now pass the caller's actual
`connTx.NonSuperuserRole`/`actingRole`). This kept the fix's blast radius to
3 non-test files with zero test-file edits required.

Verified live: the diff's first 8 lines (the four `CREATE ROLE
regress_nosuch_*` cases and four `ALTER ROLE regress_role_limited *` cases)
no longer diverge from the oracle — goopg now raises the exact same
`42501`/DETAIL pair PG does for each. `go test ./internal/postmaster/...`
(all pre-existing role-DDL tests, unmodified) and
`./internal/parser/... ./internal/executor/...` all still pass.

## Remaining gaps (why this case is still PARKED)

1. **`ALTER ROLE`/`DROP ROLE`/object-ownership enforcement needs real
   `ADMIN OPTION`-on-target-role tracking, which this loop deliberately did
   not add.** PG's full `AlterRole` gate additionally requires the acting
   role hold both `CREATEROLE` *and* `ADMIN OPTION` specifically on the
   target role (not just the attribute itself) before touching **any**
   attribute (`INHERIT`/`CONNECTION LIMIT`/`VALID UNTIL`/etc, not just the
   four privileged ones) or renaming it; `DROP ROLE` has the same gate plus
   "can't drop yourself" and "can't drop a role that still owns objects."
   goopg's role-membership `GRANT`/`REVOKE` path
   (`internal/executor/operators_ddl_role_membership.go`) already has a
   real, well-tested `IsAdminOfRole`/`checkRoleMembershipAuthorization`
   implementation — but `role_ddl.go`'s text-substitution `ALTER`/`DROP ROLE`
   path is entirely separate code with no access to it. The observable
   symptom in the diff: `ALTER ROLE regress_role_normal RENAME TO
   regress_role_abnormal` (run by a `CREATEROLE` user who did **not**
   create `regress_role_normal` and has no `ADMIN OPTION` on it) succeeds
   in goopg where PG rejects it with `Only roles with the CREATEROLE
   attribute and the ADMIN option on role "..." may rename this role` — the
   rename actually happens, so the *next* statement in the file
   (`ALTER ROLE regress_role_normal NOINHERIT ...`) now fails with
   `role "regress_role_normal" does not exist` instead of PG's permission
   error, a knock-on divergence from the same root cause. This is a
   genuinely new engine feature (bridge `role_ddl.go` to
   `catalog.InMemory.IsAdminOfRole`/`RoleAttrs.CreateRole`, then add the
   "does the target role still own live objects" `DROP ROLE` check, which
   needs an object-ownership index by role OID that doesn't currently
   exist in a queryable form) — out of this loop's contained-fix scope.

2. **Object-ownership checks (`DROP`/`ALTER ... OWNER TO`/`ALTER TABLE ADD
   COLUMN`) don't consult the actual table/index/view owner at all.** PG's
   `must be owner of {index,table,view} ...` errors never fire in goopg —
   `DROP INDEX tenant_idx` (run by a role that didn't create the table)
   silently succeeds. This is the same class of gap as item 1 (ownership
   tracking) but on the DDL-statement side rather than the role-attribute
   side; likely needs the same underlying "who owns this OID" lookup item
   1 needs, so the two should probably be fixed together in a future loop.

3. **`REASSIGN OWNED BY ... TO ...` has zero parser support** — hard syntax
   error (`unsupported statement (got reassign)`), not merely unenforced.
   A wholly new statement type.

4. **`createrole_self_grant` GUC is unrecognized** (`SET
   createrole_self_grant = 'set, inherit'` → `unrecognized configuration
   parameter`). PG's *default* for this GUC (`'set, inherit'`, matching
   what the test explicitly sets) already governs the automatic `ADMIN
   OPTION`/`INHERIT`/`SET OPTION` self-grant a `CREATEROLE` user receives
   on any role it creates — goopg has neither the GUC registration nor the
   self-grant behavior it controls, so even before this `SET` statement is
   reached, roles `CREATEROLE`-created earlier in the file never receive
   the implicit grantor privileges PG's default behavior would have given
   them. This explains most of the diff's second half: `REVOKE INHERIT
   OPTION FOR regress_tenant2 FROM regress_createrole` and `GRANT
   regress_tenant2 TO regress_createrole WITH INHERIT TRUE, SET FALSE` both
   now correctly exercise goopg's real `ADMIN OPTION` enforcement in
   `operators_ddl_role_membership.go` — but reach the *wrong* answer,
   because the missing self-grant means `regress_createrole` never actually
   held `ADMIN OPTION` on `regress_tenant2` in the first place. Needs (a)
   registering `createrole_self_grant` as a real GUC and (b) wiring
   `role_ddl.go`'s `CREATE ROLE` success path to call
   `im.GrantRoleMembership` on behalf of the creator per the GUC's value,
   whenever the creator itself isn't a superuser.

5. **`SYSID <int>` backward-compat clause is silently accepted with no
   `NOTICE`.** PG emits `NOTICE: SYSID can no longer be specified`;
   goopg's `CREATE ROLE ... SYSID ...` already parses/ignores the clause
   correctly (no false error) but has no notice-emission path at all
   plumbed through `tryHandleRoleDDL`'s `(bool, error)` return shape —
   unlike `handleDatabaseDDLBypass`, which writes directly to the wire
   `FrameWriter` it's given, `tryHandleRoleDDL` has no writer/notice
   channel. Adding one would need a `(bool, string, error)` (or similar)
   signature change threaded through both dispatch paths (simple query's
   `w.WriteCommandComplete` call site and the extended-protocol
   `extendedQueryResult.Notice` field, which *does* already exist for
   `tryHandleDatabaseDDL`) — small in isolation but touches the same
   call-site set item 1's ownership work would, so bundling them is likely
   more efficient than a standalone loop.

## Resume point

Re-run `scripts/pg-regress-runner.sh -v create_role` after any of the above
lands. Highest-leverage next fix is item 1 (ADMIN-OPTION-on-target-role
enforcement for `ALTER`/`RENAME`/`DROP ROLE`) — it's the root cause of both
the direct diff lines it touches AND several knock-on "role does not exist"
divergences later in the file caused by an erroneous rename actually
succeeding. `internal/executor/operators_ddl_role_membership.go`'s
`checkRoleMembershipAuthorization`/`IsAdminOfRole` is the existing,
oracle-verified implementation to bridge `role_ddl.go` into, rather than
reimplementing admin-option resolution a second time.
