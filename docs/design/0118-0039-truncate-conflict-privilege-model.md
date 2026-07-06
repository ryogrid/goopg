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

## Follow-up (2026-07-05, same day): SELECT privilege enforcement

Closes the "deliberately out of scope" gap noted just above. `seqScanOp.Open`
(`operators_storage.go`), `indexScanOp.openPrep` (`operators_index.go`), and
`indexOnlyScanOp.Open` (`operators_indexonly.go`) — the three operators that
read a heap relation directly — now call `dmlPrivilegePermitted(ctx, tbl,
"SELECT")` before doing any lock acquisition or scan setup, raising the same
`42501 permission denied for table %s` on failure.

**System-catalog carve-out (the blast-radius risk the ledger flagged):**
`dmlPrivilegePermitted` gained one new branch —
`if priv == "SELECT" && catalog.IsSystemRelation(tbl.OID) { return true }`
(`IsSystemRelation` = `oid < FirstUserOID`, i.e. below `FirstNormalObjectId`
16384) — checked *before* the owner/grant lookup. PostgreSQL seeds every
system catalog with an implicit PUBLIC `SELECT` grant at initdb time via
`pg_init_privis`; goopg has no equivalent default-ACL seeding mechanism (a
research pass confirmed `tableACLs` is empty for every relation, catalog or
user, until an explicit `GRANT` runs — `CREATE TABLE` never seeds it, not even
for the owner). Without the carve-out, gating SELECT would 42501 every
`psql \d`, `pg_dump` run, and `information_schema` query issued by a
non-superuser role — none of which are covered by any existing regression
test, so this would have been a silent, un-caught break. The carve-out is
scoped to `priv == "SELECT"` only: a non-superuser role writing to a system
catalog via `INSERT`/`UPDATE`/`DELETE` still needs a real grant, unchanged
from the prior loop's behavior.

**Verified no regression:** full `internal/executor`, `internal/planner`,
`internal/catalog`, `internal/server` suites pass; the role/GRANT-adjacent
isolation specs `truncate-conflict` and `intra-grant-inplace` (both drive
`SET ROLE` + table ACLs) still pass byte-identical; `tpch-spotcheck.sh`
PASS (Q12=2/Q13=33).

**Deliberately out of scope (see ledger):** views are inlined into the
querying session's own plan (`planner.go`'s `if tbl.View != nil { inner, err
:= Plan(tbl.View, cat) }`) with no view-owner/security-definer identity
anywhere in planner or executor. PostgreSQL runs a view's underlying-table
reads as the *view owner*, so `GRANT SELECT ON view TO role` with no grant on
the base table still lets `role` read through the view; goopg now denies that
same query (42501) because the inlined scan checks the *querying* role's
privilege on the base table. No existing test combines a non-superuser role
with a view-only grant, so this is a recorded scope boundary, not a caught
regression. Column-level privileges, `WITH GRANT OPTION` propagation, and
`GRANT … TO PUBLIC` remain unmodelled, same as prior loops.

Tests: `internal/executor/storage_dml_test.go`'s
`TestSeqScanRequiresSelectPrivilege` (unprivileged role denied, GRANT unblocks,
owner and superuser bypass), `TestIndexScansRequireSelectPrivilege` (sibling
pin for `indexScanOp`/`indexOnlyScanOp` — a fix scoped to `seqScanOp` alone
would leave an index-scan-chosen plan able to bypass the gate), and
`TestSystemCatalogSelectAlwaysPermitted` (catalog SELECT always permitted;
catalog INSERT still requires a grant).

Gates: `go build ./...` clean; `go vet` clean; `go test
./internal/executor/... ./internal/planner/... ./internal/catalog/...
./internal/server/...` PASS; `go test ./internal/testport/... -run
'TestPort_IsolationTruncateConflict|TestPort_IsolationIntraGrantInplace'`
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pre-commit pgbench
smoke PASS.

## Follow-up (2026-07-06): view-owner privilege check (M0122-0008)

Closes the "Remaining gap" from the SELECT-enforcement follow-up above:
`GRANT SELECT ON view TO role` alone (no matching grant on the view's base
table) was wrongly denied with 42501, because the view's underlying scans
were checked against the *querying* role instead of the *view owner* —
PostgreSQL's default (non-`security_invoker`) view semantics run a view's
underlying-table reads as the view owner.

**Root cause, part 1 (the real bug):** `catalog.InMemory.CreateView`
(`internal/catalog/catalog.go`) never set `Owner` on the new `Table` at
all — `execCreateView` (`internal/executor/operators_ddl.go`) never assigned
it either. So every view was silently "owned" by the bootstrap superuser
(`Owner == ""`) regardless of who ran `CREATE VIEW`, even though
`ALTER VIEW ... OWNER TO` (the shared table/view code path,
`operators_ddl.go`'s `s.OwnerTo != ""` branch) correctly *changes* an
existing owner — there was just no initial creator-owner to change.
`execCreateView` now stamps `vt.Owner = o.ctx.NonSuperuserRole` right after
`CreateView` returns (mirroring `currentDDLOwnerOID`'s
"current role, or bootstrap superuser" convention used elsewhere), except
on `CREATE OR REPLACE VIEW`, which keeps the replaced view's existing owner
(captured before the temporary `DropView`) — matching real PostgreSQL, where
only `ALTER VIEW ... OWNER TO` changes an existing view's owner, not a
replacing `CREATE OR REPLACE`. Owner changes here (like `ALTER ... OWNER TO`
elsewhere in this file) are in-memory only; restart persistence for
`Table.Owner` is a pre-existing, broader gap that applies uniformly to
tables and views alike (ledger row), not something this loop's scope
touches.

**Root cause, part 2 (the plumbing):** views are inlined at plan time
(`planner.go`'s `if tbl.View != nil { inner, err := Plan(tbl.View, cat) }`)
with the substituted plan's scans carrying no notion of "which role's
privileges apply here" — `dmlPrivilegePermitted` always read
`ctx.NonSuperuserRole`, the querying session's own role. Fixed with:

- `planner.SeqScan`/`IndexScan`/`IndexOnlyScan` gain a
  `PrivilegeCheckRole string` / `PrivilegeCheckRoleSet bool` pair (unset by
  default — "use the querying session's own role", the direct-table-scan
  case).
- New `tagViewOwnerScans(n Node, owner string)`
  (`internal/planner/view_privilege.go`): walks every container node type in
  the inlined plan tree (`Project`/`Filter`/`Sort`/`Limit`/`Distinct`/
  `DistinctOn`/`Aggregate`/`WindowAgg`/`ProjectSet`/`CTEScan`/`LockRows`/
  `Join`/`NestedLoopIndexJoin`/`MultiHashJoin`/`SetOp`) and tags every
  `SeqScan`/`IndexScan`/`IndexOnlyScan` leaf whose `PrivilegeCheckRoleSet` is
  still `false` with the view's owner. Called from `planScanRangeVar`
  (`planner.go`) right after `Plan(tbl.View, cat)` returns, **unless** the
  view opted into `WITH (security_invoker = true)`
  (`tbl.SecurityInvokerSet && tbl.SecurityInvoker`) — PG 15+'s escape hatch
  that runs a view's reads as the querying role instead, which
  `SecurityInvoker`/`SecurityInvokerSet` already carried on `catalog.Table`
  but (per its doc comment) had never actually been enforced anywhere before
  this loop. Leaving already-tagged scans alone is what makes nested views
  (view-of-view) correct: `Plan(tbl.View, cat)` recurses depth-first, so a
  nested view's own `tagViewOwnerScans` call (from its own frame) completes
  and stamps its immediate underlying scans with *its own* owner before the
  outer frame regains control and tags whatever is still untagged with the
  outer view's owner — each nesting level keeps its own owner instead of
  collapsing to the outermost one.
- `dmlPrivilegePermitted(ctx, tbl, priv)` is now a thin wrapper around new
  `dmlPrivilegePermittedAs(ctx, tbl, priv, checkRole string)`
  (`internal/executor/operators_storage.go`), which takes the checking role
  as an explicit parameter instead of always reading `ctx.NonSuperuserRole`.
  The querying session's own superuser bypass (`ctx.NonSuperuserRole == ""`)
  still wins unconditionally — a superuser session runs everything as
  itself regardless of view-owner semantics — checked *before* the
  `checkRole`-based owner/grant lookup. A `checkRole == ""` (the effective
  role is itself the bootstrap superuser, e.g. a superuser-owned view) also
  short-circuits to permitted. New `selectPrivilegeCheckRole(ctx, roleSet,
  role)` resolves which role each of the three SELECT-gated scan operators
  passes as `checkRole`: the tagged role when `PrivilegeCheckRoleSet`,
  otherwise `ctx.NonSuperuserRole` (unchanged behavior for direct table
  scans). `seqScanOp` extracts `PrivilegeCheckRole`/`Set` into its own struct
  fields at construction time (Phase C.3 migration: it no longer keeps a
  `*planner.SeqScan` pointer); `indexScanOp`/`indexOnlyScanOp` read them
  straight off the `*planner.IndexScan`/`*planner.IndexOnlyScan` they
  already hold.

**Still open (recorded, not fixed here):** the view's *own* ACL is never
checked against the querying role at all — there is no plan/operator node
representing "scan the view itself" (it disappears entirely into its
inlined expansion), so a role with **zero** grants anywhere (not even on the
view) can still read through a view whose owner happens to have base-table
access. This is not a regression introduced by this loop — it was already
true before the view-owner fix landed (the base-table-only check that
existed then never consulted the view's ACL either) — but it means `GRANT
SELECT ON view` is not yet a hard prerequisite for view access the way real
PostgreSQL's `ExecCheckRTPerms` (which walks the whole range table,
including the view's own un-inlined RTE) makes it. Fixing it needs a
preliminary per-statement permission pass over the plan tree (planning has
no `Context`/session-role visibility today — only the executor operators
do), a materially larger, separately-scoped change. Ledger row filed.

Tests: `internal/planner/view_privilege_test.go`'s
`TestPlanViewInliningTagsScanWithOwnerRole` (view inlining tags the SeqScan
with the view's owner) and `TestPlanViewInliningSecurityInvokerSkipsOwnerTag`
(security_invoker view leaves the scan untagged);
`internal/executor/storage_dml_test.go`'s
`TestScanOperatorsUseViewOwnerPrivilegeOverride` (all three SELECT-gated scan
operators honor the tagged override role, both allow and deny directions,
including a grant-based allow distinct from ownership);
`internal/executor/view_owner_privilege_test.go`'s
`TestCreateViewStampsCreatingRoleAsOwner` (CREATE VIEW stamps the creating
role, CREATE OR REPLACE VIEW preserves the existing owner) and
`TestViewOwnerPrivilegeGrantsThroughView` (full end-to-end: a role with
SELECT on a view only, no base-table grant, is denied until the view owner
gains SELECT on the base table, then succeeds — the reported bug's exact
scenario).

Gates: `go build ./...` clean; `go test ./internal/executor/...
./internal/planner/... ./internal/catalog/... ./internal/parser/...
./internal/server/...` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`scripts/ralph-precommit-test.sh` PASS (full suite + pgbench TPC-B/
simple-update/select-only smoke, 0 failed).
