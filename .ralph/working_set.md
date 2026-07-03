(idle — nothing in flight)

Loop #47 summary: implemented "roleError errdetail-field threading"
(M0119-0004-ACLHEAP follow-up), closing the `0119-0004ci` row's own named
residual (and the loop #46 working-set candidate list's first item). PG's
CreateRole/RenameRole (postgres/src/backend/commands/user.c:356,1388,1395)
always pairs the reserved-"pg_"-prefix errmsg with a fixed
errdetail("Role names starting with \"pg_\" are reserved.") — goopg's
roleError (internal/server/role_ddl.go) had no detail field at all, so the
wire ErrorResponse for both `CREATE ROLE pg_x` and
`ALTER ROLE x RENAME TO pg_x` was missing the 'D' field.

Files touched: internal/server/role_ddl.go (roleError gains `detail
string`; reservedRoleNameErr sets PG's fixed text; new
roleErrorDetailFields(err) []protocol.ErrorField helper; added protocol
import), internal/server/dispatch.go (both writeQueryError call sites for
role-DDL errors — the splitLeadingRoleDDL batch-recursion branch and the
single-statement tryHandleRoleDDL branch — now pass
roleErrorDetailFields(herr)... as trailing variadic args),
internal/server/role_ddl_create_reserved_test.go (extended
TestCreateRoleRejectsReservedPgPrefix to assert the exact detail-field
output; new TestRoleErrorDetailFieldsEmptyForNonReservedErrors guarding
roleDoesNotExistErr/roleAlreadyExistsErr against detail leakage).

Key symbols: roleError{code,msg,detail}, reservedRoleNameErr,
roleErrorDetailFields, roleErrorSQLState (unchanged, sibling helper).
renameRole (ALTER ROLE RENAME TO) already returns through the same
tryHandleRoleDDL call chain as CREATE, so ONE wiring point per dispatch.go
call site covers both — no separate RENAME-path change needed.

Gates run: go build ./... / go vet ./... clean; internal/server full
package suite PASS (no regression); live end-to-end proof via a real
running goopg instance (fresh build, isolated port 5533/data dir) +
postgres/local_install psql — confirmed byte-identical ERROR/DETAIL wire
output for both `CREATE ROLE pg_custom` and
`ALTER ROLE alice2 RENAME TO pg_alice2`; scripts/tpch-spotcheck.sh PASS
(Q12=2/Q13=33); pgbench smoke = pre-commit hook; make ralph-state-guard
auto-repaired the same recurring stale running/completed progress.json
mismatch as prior loops (not a genuine completion signal).

Design doc: docs/design/0119-0004-create-role-reserved-prefix.md gained a
"Follow-up: roleError detail-field threading (loop #47)" section (incl. the
live psql transcript); docs/design/README.md row 0119-0004ck added,
0119-0004ci/0119-0004cj rows' "Still open" notes updated to point at it;
.ralph/fix_plan.md entry added (right after the loop #46 predefined-role-
rows entry); .ralph/deferral_ledger.md: flipped the 0119-0004ci-landing
row's status "-" -> "resolved" (it was the row that had deferred this exact
item), plus a new resolved-status row documenting the landing.

Committed and pushed: see `git log -1` on align-data-structure-with-pg for
the commit this loop lands (created after this working-set write).

Next step: pick the next candidate below.

Next-loop candidates (unblocked, well-scoped):
- GRANT ... ON PARAMETER's reserved_class_prefix extension-namespace
  validation (needs an extension-loading/registration mechanism goopg does
  not have at all — CREATE EXTENSION is currently a compat no-op via
  execCompatNoop). Large, differently-scoped.
- check_role_grantor's inherited-privilege/superuser fallback for a bare
  REVOKE's implicit grantor (named in the 0119-0004-ACLHEAP admin-option
  ledger row as still open). Resume:
  internal/executor/operators_ddl_role_membership.go's
  checkRoleMembershipAuthorization/grantorOid handling.
- root-0023's remaining log_line_prefix escapes (%l/%c/%r/%h/%b/...) — each
  needs new per-connection state goopg doesn't track today; independently
  sized, not urgent (unchanged from loop #42/#46/#47).
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
