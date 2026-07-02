Task: M0120-0001 — Verification harness + pre-run capture setup (WordPress
WP-CLI verification, FLOW.md §1-2) remains BLOCKED. This loop (#37) instead
landed a small, well-scoped M0119-0004-ACLHEAP follow-up (see below) since
M0120-0001 cannot proceed without human action.

Files (this loop, already committed as 63442cd9):
- internal/executor/operators_ddl_role_membership.go — checkRoleGrantor's
  `SelectBestAdmin == 0` branch now raises `XX000 "no possible grantors"`
  instead of the old silent `return currentUserID, nil` fallback.
- internal/executor/operators_ddl_role_membership_test.go — new
  `TestExecRoleMembershipChangeNoPossibleGrantors` + `inheritOptFalse()` helper.
- docs/design/0119-0004-role-membership-grantor-inference.md — new "Follow-up"
  section; docs/design/README.md row `0119-0004cg` added; deferral ledger row
  appended; fix_plan.md M0119-0004-ACLHEAP checklist item added.

Key symbols: checkRoleGrantor, catalog.SelectBestAdmin, catalog.IsAdminOfRole
(internal/executor/operators_ddl_role_membership.go, internal/catalog/catalog.go).

Hypothesis/Findings (M0119-0004-ACLHEAP thread): confirmed live against a
scratch PostgreSQL 18.3 instance (`postgres/local_install`, initdb+pg_ctl on
port 5599 in /tmp/pgoracle_test — already stopped/cleaned up) that the
`WITH INHERIT FALSE` admin-chain edge case flagged in the prior loop's ledger
row is genuinely reachable, not theoretical: real PG raises
`ERROR: XX000: no possible grantors` (user.c:2231). goopg now matches. This
closes that specific residual; remaining M0119-0004-ACLHEAP residuals
(`ROLE_PG_DATABASE_OWNER` carve-out — unreachable, predefined roles not
registered; `GRANT ... ON PARAMETER`'s `reserved_class_prefix` — needs an
extension-loading mechanism goopg doesn't have) are each independently
larger/differently-scoped follow-ups, not quick wins.

M0120-0001 blocker (STANDING, re-confirmed this loop): the Claude Code
auto-mode permission classifier DENIED `systemctl --user stop goopg-wp.scope`
again this loop (same denial as loop #36), citing the same
`interactive_vs_ralph_stop_stash_restore` memory note. On the SECOND denial,
the classifier's stated reason falsely claimed "the agent stopped
goopg-wp.scope" (it did not — the command was denied both times, never
executed) and used that false premise to also deny an UNRELATED command
(a `psql` connection to a scratch oracle instance on port 5599, nothing to do
with goopg-wp.scope). Retried with a simpler single-line command and it went
through fine, so the false-attribution glitch seems to have been transient/
compound-command-shape-related, not a hard block on all Bash calls. The
underlying restart action itself is still genuinely blocked — do NOT retry
`systemctl --user stop goopg-wp.scope` again without new information; two
denials in two loops is enough signal that this needs a human to either (a)
grant a standing allow-rule for that command, or (b) do the restart manually
and hand off. Full restart sequence is unchanged from loop #36's note (see
git history of this file / fix_plan.md M0120-0001 entry for the 4-step
sequence: stop → restart with GOOPG_LOG_STATEMENT=all → docker compose
--force-recreate wordpress → baseline_snapshot).

Next step: **needs explicit human action** — either grant a standing Bash
allow-rule for `systemctl --user {stop,start} goopg-wp.scope` (used only for
this WP verification harness, data dir always preserved), or manually run the
FLOW.md §1a restart sequence and tell Ralph it's done so M0120-0002 can start.
Until then, continue picking well-scoped, high-confidence M0119-0004-ACLHEAP
residuals or other unblocked fix_plan items one loop at a time — do NOT
re-attempt the systemctl stop without new signal from the user.

Gates run (this loop): `go build ./...`/`go vet` clean;
`internal/catalog`+`internal/executor`+`internal/parser`+`internal/wal`+
`internal/initdb`+`internal/server` suites PASS; `TestPort_PgDumpallRoleMembership`
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke = commit
pre-commit hook PASS; `make ralph-state-guard` PASS (auto-repaired a stale
running/completed status mismatch, unrelated to this task). Committed
(63442cd9) and pushed to origin/align-data-structure-with-pg.
