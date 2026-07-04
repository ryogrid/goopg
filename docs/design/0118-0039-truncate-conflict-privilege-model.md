# 0118-0039 — `truncate-conflict` isolation spec: TRUNCATE privilege model + role-DDL batch fix (M0118-0008)

Status: accepted
Date: 2026-06-23
Milestone: M0118-0008 (DDL / VACUUM / maintenance concurrency) — **tenth promotion**

## Summary

Promotes the upstream isolation spec **`truncate-conflict`** to `pass`-required,
byte-identical to PostgreSQL 18.3 across all eight permutations. This is the
first of the `*-conflict` family (truncate/vacuum/cluster), which the prior
deferral notes flagged as needing "CREATE ROLE / GRANT / SET ROLE privilege
infrastructure + permission-denied". The spec exercises exactly one object
privilege — `TRUNCATE` — so this loop builds a deliberately minimal, TRUNCATE-
scoped privilege model rather than a general ACL subsystem.

The spec:

```
setup     { CREATE ROLE regress_truncate_conflict; CREATE TABLE truncate_tab (a int); }
session s1 …  s1_tab_lookup { SELECT count(*) >= 0 FROM truncate_tab; }   (inside BEGIN)
session s2 …  s2_grant  { GRANT TRUNCATE ON truncate_tab TO regress_truncate_conflict; }
              s2_auth   { SET ROLE regress_truncate_conflict; }
              s2_truncate { TRUNCATE truncate_tab; }
              s2_reset  { RESET ROLE; }
```

Without the grant, `TRUNCATE` under `SET ROLE` fails **immediately** with
`ERROR: permission denied for table truncate_tab` (SQLSTATE 42501) — it does NOT
wait for a lock. With the grant, `TRUNCATE` succeeds and instead **blocks**
behind a concurrent session holding the table open (`s1_tab_lookup` in a `BEGIN`),
completing only after that session commits.

## Two distinct problems

### 1. The setup batch was being silently truncated (root-cause blocker)

The spec's setup runs as **one** simple-query batch:
`CREATE ROLE …; CREATE TABLE …;`. goopg's parser does not yet recognise
`CREATE ROLE`, so the whole batch fails to parse and lands in
`dispatchSimpleQueryViaExecutor`'s parse-failure recovery path. There,
`tryHandleRoleDDL(sql)` is handed the **entire** batch, matches the leading
`create role ` prefix, registers the role, and returns `handled=true` — emitting
`CREATE ROLE` + `ReadyForQuery` and **dropping the trailing `CREATE TABLE`
entirely**. Result: `truncate_tab` never exists, every permutation errors with
`relation "truncate_tab" does not exist`. (This is the batch-swallow bug noted
in design 0118-0038 / the working set.)

DROP ROLE is unaffected — the parser already produces a `DropStmt` for
`DROP ROLE` (executor `execDrop` handles `objType == "role"`), so the teardown
`DROP TABLE …; DROP ROLE …;` parses and runs fine. Only `CREATE ROLE` is missing.

**Fix (server, `role_ddl.go` + `dispatch.go`):** in the parse-failure recovery
path, before the single-statement role intercept, `splitLeadingRoleDDL(sql)`
peels the leading `CREATE/DROP ROLE/USER/GROUP` statement off a multi-statement
batch (via `firstTopLevelSemicolon`, a quote-/dollar-quote-/comment-aware
scanner) and, when there is a remainder, handles the role statement through the
existing `tryHandleRoleDDL` (which keeps populating both `Server.roles` — read at
connection time by `roleExists` to reject unknown users — and the catalog role
registry) and then **recurses** into `dispatchSimpleQueryViaExecutor` on the
remainder so the `CREATE TABLE` actually executes. Standalone role DDL and
single-statement behaviour are completely untouched (the split is a no-op when
there is no second statement). We deliberately did **not** move `CREATE ROLE`
into the parser/executor, because that would bypass `Server.roles` population
and regress the "connect as a freshly-created role" path
(`createuser`/`dropuser`/`pg_amcheck` port tests).

### 2. No privilege model (the feature the spec actually tests)

goopg had no object-level ACL store and treated `SET ROLE` / `GRANT` as pure
no-ops. Added, all TRUNCATE-scoped:

- **Catalog ACL store** (`catalog.InMemory.tableACLs`, `map[relOID]map[role]privset`):
  `GrantTablePrivilege(relOID, role, priv)` / `HasTablePrivilege(...)` /
  `DropTableACL(relOID)` (also wired into `DropTable` so a recycled OID cannot
  inherit stale grants). Roles are stored lower-cased, privileges upper-cased.
  New methods on the `Catalog` interface (one implementer, `*InMemory`).
- **Effective-role tracking** (`server/query.go`): `SET ROLE <name>` now
  populates `connTx.NonSuperuserRole` and `RESET ROLE` clears it — mirroring the
  existing `SET SESSION AUTHORIZATION` handling. `NonSuperuserRole != ""` means
  "running as a non-superuser role; enforce object privileges"; `NONE`/`DEFAULT`/
  the bootstrap superuser clear it. The executor already receives this via
  `ectx.NonSuperuserRole` (`dispatch.go`).
- **Autocommit table-level GRANT recorder** (`server/grant_ddl.go`):
  `GRANT <privs> ON [TABLE] <tables> TO <roles>` is intercepted in `handleQuery`
  (single-statement, autocommit only — inside an explicit transaction it falls
  through to the existing no-op executor path so transaction state and the
  protocol response are unchanged) and recorded in the ACL store. `ALL
  [PRIVILEGES]` expands to the full table privilege set. Any form we cannot
  confidently parse (column-level, `ON SCHEMA/DATABASE/SEQUENCE/…`, role
  membership, `TO PUBLIC`) is left to the permissive no-op path — the command
  still reports success, it just records nothing. REVOKE stays a no-op.
- **Pre-lock TRUNCATE privilege check** (`executor/operators_ddl.go`
  `execTruncate`): when `ctx.NonSuperuserRole != ""`, each explicitly named
  relation requires `HasTablePrivilege(oid, role, "TRUNCATE")`, else
  `ExecError{42501, "permission denied for table <name>"}`. This runs **before**
  any lock is acquired, so an unprivileged TRUNCATE fails immediately rather than
  waiting — matching PG's pre-lock ACL check. The owning superuser
  (`NonSuperuserRole == ""`) bypasses the check; a non-superuser role that *owns*
  the table is out of scope (the spec's role owns nothing; documented limitation).

### 3. Autocommit TRUNCATE must wait for a conflicting lock

In the granted permutations `s2_truncate` runs in **autocommit**, yet must block
behind `s1`'s still-open `SELECT … FROM truncate_tab` (which holds an
`AccessShareLock` to commit because it runs inside `s1`'s `BEGIN`, via the
`acquireScanReadLockTxn` hook). `execTruncate` previously used
`acquireDDLLockTxn`, which is a **no-op in autocommit**, so the TRUNCATE
completed instantly instead of waiting. Switched to `acquireRelLockMaybeTransient`
(`AccessExclusiveLock`): held-to-commit inside an explicit transaction (preserving
`inherit-temp`'s blocking permutations, whose TRUNCATE is wrapped in `s1_begin …
s1_commit` — identical behaviour) and acquired **transiently** in autocommit —
the WAIT still happens during acquisition, so the autocommit TRUNCATE blocks
until the holder commits, then proceeds. Same primitive already used by
sequence-ddl / vacuum-concurrent-drop.

## Files changed

- `internal/catalog/catalog.go` — `tableACLs` field + init; `Catalog` interface
  methods `GrantTablePrivilege`/`HasTablePrivilege`/`DropTableACL` + `*InMemory`
  impls; `DropTable` clears the ACL.
- `internal/server/query.go` — `SET ROLE`/`RESET ROLE` now track
  `connTx.NonSuperuserRole`; autocommit `GRANT ` interceptor before the switch.
- `internal/server/grant_ddl.go` (new) — table-level GRANT parser/recorder.
- `internal/server/role_ddl.go` — `splitLeadingRoleDDL` + `firstTopLevelSemicolon`
  + `scanDollarTag`.
- `internal/server/dispatch.go` — peel leading role DDL off a batch and recurse.
- `internal/executor/operators_ddl.go` — `execTruncate` pre-lock privilege check;
  lock via `acquireRelLockMaybeTransient`.
- `internal/testport/isolation_port_test.go` — `TestPort_IsolationTruncateConflict`
  (strict).

## Oracle

- Privilege check / error text: `aclchk.c` `pg_class_aclcheck` →
  `aclcheck_error(ACLCHECK_NO_PRIV, OBJECT_TABLE, …)` ⇒ `ERROR: permission denied
  for table %s` (42501). The check precedes the AccessExclusiveLock in
  `ExecuteTruncate` (`tablecmds.c` → `truncate_check_perms`).
- TRUNCATE lock level: `AlterTableGetLockLevel` / `ExecuteTruncate` take
  `AccessExclusiveLock`.

## Gates

- `TestPort_IsolationTruncateConflict` strict PASS (8 permutations); `-race` PASS.
- Sibling M0118-0008 specs PASS: `inherit-temp` (shares the TRUNCATE lock path),
  `create-trigger`, `alter-table-3`, `sequence-ddl`, `vacuum-{skip-locked,
  concurrent-drop}`, `reindex-{concurrently,schema}`, `multiple-cic`.
- `createuser` / `dropuser` / `pg_amcheck 002` port tests PASS (role-DDL batch
  change is behaviour-preserving for standalone role DDL).
- Unit suites: `internal/catalog`, `internal/parser`, `internal/server`,
  `internal/executor` PASS.
- pgbench CI-parity smoke: 0 failed, `-S` ~15.2k TPS / 0.131 ms.

## Remaining (M0118-0008 group stays open)

`vacuum-conflict` / `cluster-conflict` need **ownership**-based privilege checks
(VACUUM/CLUSTER require relation ownership or `MAINTAIN`, not a grantable table
privilege) — a follow-up extending this ACL store with owner tracking.
`alter-table-{1,2,4}` (ADD/VALIDATE CONSTRAINT lock semantics; INHERITS),
partition ATTACH/DETACH specs, `reindex-concurrently-toast`,
`vacuum-no-cleanup-lock`, `plpgsql-toast` remain deferred (ledger). A
non-superuser role that *owns* a table, REVOKE, column-level grants, and
`GRANT … TO PUBLIC` are unmodelled bounded follow-ups.

## Follow-up (2026-07-05, M0097-0040): INSERT/UPDATE/DELETE privilege enforcement

`GRANT`/`REVOKE INSERT|SELECT|UPDATE|DELETE` were already tracked in
`tableACLs` (needed for byte-identical `pg_dump` `relacl` round-tripping,
M0119-0004), but `HasTablePrivilege` was only ever *consulted* for `TRUNCATE`
(above) and `MAINTAIN` (`operators_vacuum.go`'s `maintenancePermitted`) — plain
DML never checked it, so `REVOKE INSERT ON t FROM role; SET ROLE role; INSERT
INTO t …` incorrectly succeeded (confirmed-open `unimplemented_feat.json`
entry `M0097-0040`, filed 2026-05-27).

**Fix:** a new `dmlPrivilegePermitted(ctx, tbl, priv) bool` helper in
`internal/executor/operators_storage.go` — mirrors `maintenancePermitted`'s
three-tier shape (bootstrap superuser bypass → table-owner bypass → grant
lookup via the existing `HasTablePrivilege`) rather than TRUNCATE's simpler
grant-only check, so an owner who has never explicitly granted themselves a
privilege still passes, matching PostgreSQL's implicit owner privileges.
Called from `insertOp.Open`/`updateOp.Open`/`deleteOp.Open` before any lock is
acquired (same pre-lock-check ordering as `execTruncate`), raising
`ExecError{42501, "permission denied for table %s"}` on failure. No analyzer/
planner change — the check is purely an executor-side gate keyed off the
already-resolved `plan.Table`.

**Verified non-interference with cascades:** `fkCascadeDelete`
(`operators_fk.go`) manipulates heap pages directly (`Pool.Pin`/
`PageGetHeapTuple`) rather than going through `deleteOp`, so an `ON DELETE
CASCADE` firing from a parent-table `DELETE` the role *does* have privilege on
is never blocked by a missing `DELETE` grant on the child — matching
PostgreSQL, where FK-enforcement triggers are not subject to the invoking
role's ordinary object ACL. The logical-replication apply worker
(`applyworker.go`) does not construct `insertOp`/`updateOp`/`deleteOp` at all
(it also writes heap pages directly), so it is unaffected.

**Deliberately out of scope (see ledger):** `SELECT` privilege enforcement on
`seqScanOp`/index-scan read paths — a much larger blast radius (every SELECT,
including internal system-catalog scans issued on behalf of a non-superuser
session) that needs its own bounded loop with a dedicated regression pass, not
folded into this DML-write-path fix. Column-level privileges, `WITH GRANT
OPTION` propagation, and `GRANT … TO PUBLIC` remain unmodelled, same as the
TRUNCATE-era limitations above.

Tests: `internal/executor/storage_dml_test.go`'s
`TestDMLRequiresTablePrivilege` (unprivileged role denied on all three
statements, incremental per-privilege GRANT unblocks each, table owner and
bootstrap superuser always pass without a GRANT).

Gates: `go build ./...` clean; `go test ./internal/executor/...
./internal/planner/... ./internal/server/... ./internal/catalog/...` PASS (no
regressions); pre-commit pgbench smoke PASS.
