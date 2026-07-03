(idle — nothing in flight)

Loop #44 summary: implemented "CREATE ROLE/USER/GROUP reserved
`pg_`-prefix rejection" (M0119-0004-ACLHEAP follow-up), closing discovery
(b) from the loop #43 ledger row. `tryHandleRoleDDL`'s CREATE branch
(internal/server/role_ddl.go) never called `isReservedRoleName` — only
`ALTER ROLE ... RENAME TO` did — so `CREATE ROLE pg_custom` silently
succeeded where real PG's `CreateRole` (postgres/src/backend/commands/
user.c) raises 42939 "role name is reserved" BEFORE its pg_authid
duplicate-name lookup (verified via upstream source read, lines 347-378).
Fix wires the existing `isReservedRoleName`/`reservedRoleNameErr` helpers
(already used by `renameRole`) into the CREATE branch immediately after
name extraction — no new helper, mirrors the RENAME TO check verbatim.

Files touched: internal/server/role_ddl.go (7-line addition in the
CREATE branch), internal/server/role_ddl_create_reserved_test.go (new,
TestCreateRoleRejectsReservedPgPrefix + TestCreateRoleAllowsNonReservedName).

Gates run (this loop): go build ./... / go vet clean; internal/server +
internal/catalog + internal/executor + internal/initdb suites PASS;
TestPort_PgDumpallRoleMembership + TestPort_PgDumpConnectionSetup PASS
(no regression); scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); pgbench
smoke PASS via pre-commit hook; make ralph-state-guard auto-repaired the
same recurring stale running/completed progress.json mismatch as prior
loops (not a genuine completion signal).

Design doc: docs/design/0119-0004-create-role-reserved-prefix.md (new);
docs/design/README.md row 0119-0004ci added; .ralph/fix_plan.md entry
added (right after the loop #43 entry); .ralph/deferral_ledger.md row
appended (1 new discovery, small, NOT required to close this loop's own
task): PG's errdetail text ("Role names starting with \"pg_\" are
reserved.") is not ported — `roleError` (internal/server/role_ddl.go)
has no detail field, an existing simplification shared with the RENAME
TO path. Resume point (if ever picked up): thread a `detail string`
field through `roleError` + whatever writes the ErrorResponse
(roleErrorSQLState and callers, ~line 621/807) — small but touches
every roleError call site's construction.

Committed and pushed: 05358c97 on align-data-structure-with-pg
(3d4be37a..05358c97). Tree is clean except the pre-existing untracked
`postgres` submodule dirtiness.

Next step: pick the next candidate below.

Next-loop candidates (unblocked, well-scoped):
- pg_authid/pg_roles predefined-role virtual rows (discovery (a) from
  loop #43/#44's ledger rows) — needed so a GRANT to a predefined role
  round-trips through pg_dumpall; medium-sized. Resume:
  internal/catalog/catalog.go's pgAuthid/pgRoles VirtualRows closures
  (~line 5833/5918 as of loop #43 — re-grep, line numbers shift), add a
  second row-emission pass over predefinedRoleSeeds with PG18
  pg_authid.dat attribute values (rolinherit=true, other privilege flags
  false, rolconnlimit=-1, rolpassword/rolvaliduntil NULL — already known
  from internal/executor/pg_authid_sync.go's buildAuthidUserRow-adjacent
  logic); pg_roles additionally needs rolconfig/membership-derived
  columns computed from pg_auth_members, not stored per-row.
- roleError detail-field threading (this loop's own small deferral,
  above) — cosmetic error-text fidelity, low priority.
- root-0023's remaining log_line_prefix escapes (%l/%c/%r/%h/%b/...) —
  each needs new per-connection state goopg doesn't track today;
  independently sized, not urgent (unchanged from loop #42/#43).
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
