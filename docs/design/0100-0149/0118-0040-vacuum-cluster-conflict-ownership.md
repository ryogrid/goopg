# 0118-0040 — `vacuum-conflict` / `cluster-conflict`: ownership-based maintenance privilege (M0118-0008)

**Status:** accepted
**Milestone:** M0118-0008 (Upstream Isolation Spec Suite Pass-Through — DDL / VACUUM / maintenance concurrency)
**Date:** 2026-06-23
**Specs promoted:** `vacuum-conflict` (16 permutations), `cluster-conflict` (2 permutations) — both now byte-identical to PostgreSQL 18.3.

## Summary

Eleventh M0118-0008 promotion, and the second/third members of the `*-conflict`
family after `truncate-conflict` (design 0118-0039). Both specs exercise a
**maintenance-privilege check keyed on table ownership** rather than on a
grantable table privilege:

- A non-superuser session (`SET ROLE regress_*_conflict`) may run
  `VACUUM` / `ANALYZE` / `CLUSTER` on a relation **only if it owns the relation**
  (or holds `MAINTAIN` on it). This mirrors PostgreSQL's
  `vacuum_is_permitted_to_vacuum` / the CLUSTER owner check.
- Ownership is established with `ALTER TABLE … OWNER TO <role>`.
- For `VACUUM`/`ANALYZE` the permission check happens **before any lock is
  taken** (PostgreSQL does it in `expand_vacuum_rel` from the `pg_class`
  syscache with no lock), so an unprivileged maintenance command **skips the
  relation immediately with a `WARNING`** instead of waiting behind a
  conflicting `LOCK … IN SHARE UPDATE EXCLUSIVE MODE`.
- Once the role owns the table, the command is permitted and **blocks** on the
  conflicting lock until the holder commits, then completes.

This required two pieces of new infrastructure plus one executor lock change.

## Background — what the specs do

`vacuum-conflict.spec`: session `s1` holds `vacuum_tab` in
`SHARE UPDATE EXCLUSIVE MODE`; session `s2` does `SET ROLE
regress_vacuum_conflict` and then `VACUUM`/`ANALYZE vacuum_tab`.

- Without `s2_grant`: the role is **not** the owner ⇒ every `VACUUM`/`ANALYZE`
  emits `WARNING: permission denied to vacuum/analyze "vacuum_tab", skipping it`
  and returns immediately, regardless of whether `s1` holds the lock (no wait).
- With `s2_grant` (`ALTER TABLE vacuum_tab OWNER TO regress_vacuum_conflict`):
  the role owns the table ⇒ `VACUUM`/`ANALYZE` is permitted and blocks behind a
  concurrent `s1_lock` (`<waiting ...>`), completing after `s1_commit`.

`cluster-conflict.spec`: the table is owned by `regress_cluster_conflict` from
**setup** (`ALTER TABLE cluster_tab OWNER TO …`). After `SET ROLE`, `CLUSTER
cluster_tab USING cluster_ind` is permitted; CLUSTER takes an
`AccessExclusiveLock`, so it blocks behind `s1`'s `SHARE UPDATE EXCLUSIVE` lock
and completes after commit. (No skip path is exercised — the role always owns.)

## Why `truncate-conflict`'s ACL store was not enough

`truncate-conflict` (0118-0039) modelled `GRANT TRUNCATE … TO role`: a grantable
**privilege** recorded in the per-relation ACL store (`tableACLs`). VACUUM /
ANALYZE / CLUSTER are **not** grantable in the same way — PostgreSQL requires the
caller to be the relation **owner** (or hold the role-level `pg_maintain` / the
`MAINTAIN` privilege). The spec grants ownership via `ALTER TABLE … OWNER TO`,
which goopg previously parsed as a pure no-op. So the missing ingredient was
**per-relation owner tracking**, not another ACL entry.

## Changes

### 1. Per-relation owner field — `catalog.Table.Owner`

New field `Owner string` on `catalog.Table` (`internal/catalog/catalog.go`). It
holds the owning **role name** (case-insensitive), or empty for the bootstrap
superuser (OID 10) — goopg's default for every freshly created relation. The
`pg_class.relowner` OID column is still rendered as the bootstrap superuser, so
catalog/dump output is unaffected; the field exists purely to drive the
maintenance-privilege check against `Context.NonSuperuserRole`.

### 2. `ALTER TABLE … OWNER TO role` records the owner

- **Parser** (`internal/parser/ddl.go`, main ALTER TABLE path): `OWNER TO role`
  now captures the role name into the new `AlterTableStmt.OwnerTo`
  (`internal/parser/ast.go`) instead of discarding it. `CURRENT_USER` /
  `SESSION_USER` / `CURRENT_ROLE` map to the sentinel `"current_user"` (the
  executor resolves it to the bootstrap superuser = empty owner) so the
  statement is still recognised as an OWNER-TO action.
- **Executor** (`execAlterTable`, `internal/executor/operators_ddl.go`): a new
  early-return arm sets `tbl.Owner = s.OwnerTo` (empty for the `current_user`
  sentinel) and takes a transaction-scoped `AccessExclusiveLock` via
  `acquireDDLLockTxn` (PostgreSQL `AlterTableGetLockLevel` returns
  `AccessExclusiveLock` for `AT_ChangeOwner`). In autocommit — how every spec
  permutation issues the grant — `acquireDDLLockTxn` is a no-op, so no
  permutation deadlocks on it.

### 3. Maintenance-privilege check — `maintenancePermitted`

New helper in `internal/executor/operators_vacuum.go`:

```go
func maintenancePermitted(ctx *Context, tbl *catalog.Table) bool {
    role := ctx.NonSuperuserRole
    if role == "" {
        return true // bootstrap superuser: full privileges
    }
    if tbl.Owner != "" && strings.EqualFold(tbl.Owner, role) {
        return true
    }
    return ctx.Catalog.HasTablePrivilege(tbl.OID, role, "MAINTAIN")
}
```

Wired into the **explicit-target** loop of both `expandVacuumTargets`
(`operators_vacuum.go`) and `expandAnalyzeTargets` (`operators_analyze.go`),
**before** the target is added to the work list and therefore before any
per-relation lock is acquired. An unpermitted explicit target is skipped with:

- VACUUM: `WARNING: permission denied to vacuum "<name>", skipping it`
- ANALYZE: `WARNING: permission denied to analyze "<name>", skipping it`

This reproduces PostgreSQL's `expand_vacuum_rel` behaviour: the ACL check uses
the `pg_class` tuple with **no lock**, so an unprivileged command never waits on
a conflicting lock holder — matching the spec's "immediately skip without
waiting for a lock" comment. The check is applied only to explicitly named
relations (database-wide VACUUM and expanded partition children are not
re-checked here, matching `expand_vacuum_rel`'s skip-flag handling).

### 4. CLUSTER takes a blocking `AccessExclusiveLock`

`clusterOp.Next` (`internal/executor/operators_cluster.go`) previously verified
the target existed and returned (CLUSTER is otherwise a no-op in goopg — no
physical heap reorder). It now also acquires the table's `AccessExclusiveLock`
via `acquireRelLockMaybeTransient` (held to commit inside an explicit
transaction; transient acquire+release in autocommit so the wait still happens
during acquisition). `AccessExclusiveLock` conflicts with every other mode, so
CLUSTER blocks behind `s1`'s `SHARE UPDATE EXCLUSIVE` lock and completes once
`s1` commits — the only behaviour `cluster-conflict` observes. No ownership
*error* path was added for CLUSTER (PostgreSQL errors `must be owner of table`
rather than skipping; the spec never exercises a non-owner CLUSTER, and adding
the error would widen blast radius with no test coverage — left as a bounded
follow-up).

## Faithfulness / blast radius

- `Table.Owner` defaults to empty (bootstrap superuser); with no `SET ROLE` the
  session is the superuser and `maintenancePermitted` always returns true, so
  the new skip path is dormant for all normal (superuser) usage — pgbench, TPC-H,
  every existing VACUUM/ANALYZE caller is unchanged.
- The new CLUSTER lock is transient in autocommit and only held-to-commit inside
  an explicit transaction; goopg's CLUSTER is otherwise a no-op, and CLUSTER is
  not on any hot path.
- `ALTER TABLE … OWNER TO` previously discarded the role; now it records it and
  takes the same DDL lock other ALTER subcommands take (no-op in autocommit).
- The maintenance-privilege check is gated on `NonSuperuserRole != ""`, so it
  costs nothing for the common superuser path.

## Oracle

- `postgres/src/backend/commands/vacuum.c` — `expand_vacuum_rel` (pre-lock ACL
  check + `WARNING: permission denied to vacuum/analyze`),
  `vacuum_is_permitted_to_vacuum` (owner-or-MAINTAIN logic),
  `vacuum_open_relation` (lock level).
- `postgres/src/backend/commands/cluster.c` — `cluster_rel`
  (`LockRelationOid(AccessExclusiveLock)` + owner check).
- `postgres/src/backend/commands/tablecmds.c` — `AlterTableGetLockLevel`
  (`AT_ChangeOwner` ⇒ `AccessExclusiveLock`).

## Tests / gates

- `TestPort_IsolationVacuumConflict` (strict) — 16 permutations PASS.
- `TestPort_IsolationClusterConflict` (strict) — 2 permutations PASS.
- Sibling M0118-0008 specs: `truncate-conflict`, `vacuum-skip-locked`,
  `vacuum-concurrent-drop`, `sequence-ddl`, `reindex-*`, `multiple-cic`,
  `alter-table-3`, `create-trigger`, `inherit-temp` — PASS (no regression).
- `-race` on `internal/executor` + `internal/catalog`.
- catalog / parser / executor unit suites.
- pgbench TPC-B smoke (0 failed).

## Follow-ups (deferred, ledger 2026-06-23)

- `cluster-conflict-partition` (CLUSTER of a partitioned table — needs partition
  child enumeration + per-child lock).
- A faithful non-owner CLUSTER `must be owner of table` error (no `port` spec
  exercises it).
- Remaining M0118-0008 tail: `alter-table-{1,2,4}`, partition ATTACH/DETACH
  specs, `reindex-concurrently-toast`, `vacuum-no-cleanup-lock`, `plpgsql-toast`.
