# M0134-0162 — `[NO]INHERIT` / `pg_authid.rolinherit`

**Status:** landed. **Case:** `postgres/src/test/regress/sql/roleattributes.sql`
(`not-tried` → **`pass`**, 28 diff lines → **0**).

## What the case found

`roleattributes.sql` is 98 lines of one shape: create or alter a role with one
attribute, then read all eleven `pg_authid` columns back. Sized live for the
first time, goopg's whole divergence was **two hunks with one root cause** —
`rolinherit` reported `t` where PG reports `f`:

```
 CREATE ROLE regress_test_inherit WITH NOINHERIT;
-regress_test_inherit | f | f | ...          <- PG
+regress_test_inherit | f | t | ...          <- goopg
```

`INHERIT`/`NOINHERIT` was **accept-and-ignore, engine-wide**:

- `catalog.RoleAttrs` — the sidecar every other role attribute lives in — had
  no `Inherit` field at all.
- `applyRoleAttrOptions` (`internal/postmaster/role_ddl.go`) probed for
  `superuser`/`login`/`createdb`/`createrole`/`replication`/`bypassrls` and
  never for `inherit`.
- Both `pg_authid` row builders hardcoded the column:
  `internal/catalog/catalog.go`'s virtual `VirtualRows` emitted the literal
  `"t"`, and `internal/executor/pg_authid_sync.go`'s `buildAuthidUserRow`
  emitted `NewBoolDatum(true)`. Both carried a comment saying so.

## The part that is not cosmetic

`rolinherit` is not display-only in PostgreSQL. `AddRoleMems`
(`postgres/src/backend/commands/user.c:1924-1939`) uses it as the **default for
`pg_auth_members.inherit_option`** when a `GRANT role TO member` names no
`WITH INHERIT`:

```c
if ((popt->specified & GRANT_ROLE_SPECIFIED_INHERIT) != 0)
    new_record[...inherit_option...] = popt->inherit;
else
{
    mrtup = SearchSysCache1(AUTHOID, ObjectIdGetDatum(memberid));
    ...
    new_record[...inherit_option...] = mrform->rolinherit;   /* :1937 */
}
```

goopg's `GrantRoleMembership` hardcoded `inherit == nil || *inherit` — always
true — and its doc comment stated the assumption explicitly: *"inherit = the
grantee's rolinherit (goopg has no per-role NOINHERIT tracking … so every
role's rolinherit is always true, matching this default exactly)."*

That assumption is exactly what modelling `rolinherit` invalidates. `InMemory`'s
`HasPrivsOfRole` and `SelectBestAdmin` (mirroring `acl.c`'s `has_privs_of_role`
/ `select_best_admin`) traverse **only** `InheritOption`-marked membership rows.
Had the attribute landed without the default, a `NOINHERIT` role would have
silently kept inheriting every privilege of every role granted to it — the
precise thing `NOINHERIT` exists to prevent. So the two changes are one slice:
the catalog column is meaningless without the default, and the default cannot be
written without the column.

## What landed

| file | change |
|---|---|
| `internal/catalog/catalog.go` | `RoleAttrs.Inherit` field; virtual `pg_authid` builder emits it; predefined-role seed sets `Inherit: true`; `GrantRoleMembership` defaults `InheritOption` via new `inheritOptionDefault` + `roleInheritsByOIDLocked` |
| `internal/postmaster/role_ddl.go` | `applyRoleAttrOptions` parses `[NO]INHERIT`; the three `RoleAttrs` seed sites (CREATE, ALTER, RENAME) set `Inherit: true`; `syncAuthidHeapRow` threads it |
| `internal/executor/sys_pg_authid.go`, `pg_authid_sync.go` | `SyncAuthidRow` / `buildAuthidUserRow` take and write `inherit` |
| `internal/initdb/catalog_heap_reload.go` | reload reads `rolinherit` (`d[3]`) so `NOINHERIT` survives a restart |

`internal/initdb/initdb.go`'s two bootstrap row builders already hardcoded
`rolinherit = true` and stay as they are: every role in `pg_authid.dat` — the
bootstrap superuser and all 16 predefined `pg_*` roles — carries
`rolinherit => 't'`.

### The true-default trap

`rolinherit` is the **only** `pg_authid` boolean whose PG default is TRUE
(`user.c` `CreateRole`'s `bool inherit = true`, written unconditionally at
`:411`). The Go zero value is therefore the *wrong* seed — the same hazard
`RoleAttrs.ConnLimit`'s `-1` already carried and documents. Every `RoleAttrs`
built as a "no attributes given yet" starting point must set `Inherit: true`
explicitly; the struct comment now names all five such sites.

Two `RoleAttrs{}` literals are deliberately left alone:
`checkCreateRolePrivileges` and `checkAlterRoleAttrPrivileges` use them as
"fail closed, no privileges" sentinels, and PG does not gate `INHERIT` in
either.

### Option-parsing note

`applyRoleAttrOptions` matches options as leading-space substrings. That leading
space is load-bearing here in a way it is not for the siblings: the case's own
role names are `regress_test_inherit` and `regress_test_def_inherit`, so a probe
for `"inherit"` without the space would match the **role name** and mark every
such role `NOINHERIT`. `TestCreateRoleInheritDefaultsTrue` pins this with a
name of exactly that shape.

## Verification

- `roleattributes` standalone: **PASS**, 28 → 0 diff lines.
- 8-case regress A/B against a `HEAD` worktree (`create_role`, `dependency`,
  `init_privs`, `password`, `privileges`, `rowsecurity`, `security_label`,
  `roleattributes`): every diff byte-identical after normalising the runner's
  tmpfile headers and the pre-existing nondeterministic Go pointer address in
  `rowsecurity`'s policy-expression rendering (`&{64 0x…}`, whose hex width also
  shifts one ruler line). **Zero regressions.**
- New `internal/postmaster/role_ddl_inherit_test.go` — six tests covering both
  polarities, the ALTER-preserves-unnamed-attributes rule, the virtual
  `pg_authid` read path, and both arms of the membership `inherit_option`
  branch (including that `HasPrivsOfRole` now denies a `NOINHERIT` member).

## Deferred (ledgered)

1. **`pg_authid`'s rolname btree (OID 2676) cannot split.** In the sequenced
   8-case A/B run, `roleattributes` fails from its very first `CREATE ROLE` with
   `split: unsupported system btree OID 2676` — `keyMetaForSysBtree`
   (`internal/executor/sys_catalog_btree_split.go:437`) has no entry for 2676 or
   2677, so once earlier cases have created enough roles to fill one index page,
   every further `CREATE ROLE` fails. Pre-existing and identical in both arms;
   it caps a cluster at roughly one page of roles.
2. **`pg_roles` exposes only 4 columns** (`oid`, `rolname`, `rolsuper`,
   `rolcanlogin`) — no `rolinherit`, and none of the other seven `pg_authid`
   attributes. psql's `\du` renders "No inheritance" from `rolinherit`, so it
   cannot report the attribute even though `pg_authid` now does.
3. **`SET ROLE` does not consult `rolinherit`/`set_option`.** The attribute now
   controls *implicit* privilege inheritance; PG additionally lets a `NOINHERIT`
   member reach the role's privileges via an explicit `SET ROLE`, which goopg's
   session-identity path does not gate on membership options.

## Upstream references

- `postgres/src/backend/commands/user.c` — `CreateRole` `:291`, `:411`;
  `AlterRole` `:879-880`; `AddRoleMems` `:1924-1939`
- `postgres/src/backend/utils/adt/acl.c` — `has_privs_of_role`,
  `select_best_admin`
- `postgres/src/include/catalog/pg_authid.dat` — every seeded role is
  `rolinherit => 't'`
