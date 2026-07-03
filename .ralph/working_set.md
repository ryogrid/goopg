(idle — nothing in flight)

Loop #43 summary: implemented "Predefined-role name resolution +
ROLE_PG_DATABASE_OWNER carve-out" (M0119-0004-ACLHEAP follow-up), closing the
`0119-0004cd` ledger row's deferred residual, which was unreachable (not
merely unimplemented) — goopg's 16 PG18 predefined "pg_*" roles were only
ever heap-seeded/heap-resynced, never registered in the live catalog
role-name registry, so `resolveRole("pg_database_owner")` failed with 42704
before the carve-out check could ever run, and `GRANT pg_read_all_data TO
alice` (a standard PG idiom) didn't work at all.

New `catalog.InMemory.predefinedRoles` (internal/catalog/catalog.go) is a
dedicated, read-only map populated at NewInMemory() construction from a new
`predefinedRoleSeeds` list; consulted by RoleOID/RoleExists (predefined
checked FIRST so a hypothetical CREATE ROLE name collision can't shadow the
real OID — CREATE ROLE doesn't yet port PG's IsReservedName "pg_"-prefix
rejection, a separate pre-existing gap, recorded not fixed) and a new shared
roleNameForOIDLocked helper feeding RoleNameForOID/RoleNameForOIDOrUnknown
(reverse direction, moved together per sibling-path rule) — but structurally
separate from `roles`, so RegisterRole/UnregisterRole/RenameRole/
AllRoleStates never see it (no pg_authid heap-sync duplication risk). New
IsPredefinedRole (added to Catalog interface) backs a new DROP ROLE/USER/
GROUP pinned-object guard (execDropCompat, operators_ddl.go) — without it,
DROP ROLE pg_read_all_data would silently succeed, which real PG 18.3
rejects (verified live, scratch initdb+pg_ctl port 5599) with 2BP01 "cannot
drop role %s because it is required by the database system", unaffected by
IF EXISTS. checkRoleMembershipAuthorization (operators_ddl_role_membership.go)
now raises 0A000 for GRANT pg_database_owner TO ... (isGrant-only, matches
user.c ordering/text exactly).

Tests: TestPredefinedRoleResolution (catalog/role_membership_test.go);
TestExecRoleMembershipChangeGrantsPredefinedRole/GrantPgDatabaseOwnerRejected/
TestExecDropCompatPredefinedRolePinned (executor/operators_ddl_role_
membership_test.go); initdb/role_ddl_recovery_test.go's
TestPgAuthidSyncLoadRoundTrip updated (its old "predefined role must never
appear in RoleExists" assertion encoded exactly the gap this loop closes).

Gates run (this loop): go build ./... / go vet clean; internal/catalog +
internal/executor + internal/server + internal/parser + internal/wal +
internal/initdb suites PASS (full internal/initdb run, not just targeted,
since I touched a shared reverse role-name lookup); TestPort_
PgDumpallRoleMembership + TestPort_PgDumpConnectionSetup PASS (no
regression); scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); make
ralph-state-guard auto-repaired the same recurring stale running/completed
progress.json mismatch as loops #37/#39/#41/#42 (not a genuine completion
signal). pgbench smoke runs via the pre-commit hook on `git commit`.

Design doc: docs/design/0119-0004-predefined-role-resolution.md (new);
docs/design/README.md row 0119-0004ch added; .ralph/fix_plan.md entry added
(right before the root-0023 chain, matching where sibling M0119-0004-ACLHEAP
entries live); .ralph/deferral_ledger.md row appended (2 new discoveries,
both independently-sized, NOT required to close this loop's own residual):
(a) pg_authid/pg_roles virtual views still omit the 16 predefined roles
entirely, so a GRANT to a predefined role won't survive a real pg_dumpall
round-trip (LEFT JOIN produces NULL, row silently dropped) — resume at
catalog.go's pgAuthid/pgRoles VirtualRows closures (~line 5833/5918), add a
second row-emission pass over predefinedRoleSeeds; (b) CREATE ROLE doesn't
reject "pg_"-prefixed names (isReservedRoleName only guards RENAME TO today)
— resume at internal/server/role_ddl.go's "create role "/"create user "/
"create group " branch (~line 54).

Next step: commit this loop's diff, push, then run the pgbench smoke via the
pre-commit hook (git commit already invokes it automatically — hooksPath is
configured).

Next-loop candidates (unblocked, well-scoped):
- Fix (a) above: pg_authid/pg_roles predefined-role virtual rows — needed so
  a GRANT to a predefined role round-trips through pg_dumpall; medium-sized,
  clear resume point recorded in the ledger row appended this loop.
- Fix (b) above: CREATE ROLE reserved-"pg_"-prefix rejection — small, clear
  resume point, mirrors the existing RENAME TO check almost verbatim.
- root-0023's remaining log_line_prefix escapes (%l/%c/%r/%h/%b/...) — each
  needs new per-connection state goopg doesn't track today; independently
  sized, not urgent (unchanged from loop #42).
- logging_collector/log_directory file sink — bigger, no existing goopg
  capability to build on (background log-rotation writer).
- M0119-0004 pg_dump 002-010 TAP (fix_plan.md — grep for current position,
  line numbers shift every loop that edits fix_plan.md).
- M0095-0003 pg_basebackup 010/011/020 (fix_plan.md, re-grep).
- M0119-0005/0006/0007 — pg_waldump/pg_amcheck server tier, pg_basebackup
  recvlogical (fix_plan.md, re-grep — line numbers shift).

M0120-0001 (WordPress verification harness restart) remains STANDING
BLOCKED — needs human action (systemctl --user stop goopg-wp.scope denied
twice by the permission classifier, loops #36/#37). Do not retry without
new signal.
