(idle — nothing in flight)

Loop #88 COMPLETE and pushed (commit 4a05623c): fixed the
compatNoopCommandTag multi-statement-batch masking bug discovered in loop
#87's deferral ledger row. A simple-query batch whose first statement
matched a compatNoopCommandTag prefix (GRANT/REVOKE/COMMENT ON/SECURITY
LABEL/CREATE SCHEMA/DATABASE/ROLE DDL) followed by a later genuinely-invalid
statement was silently absorbed as a bare CommandComplete success instead
of reporting the real 42601 syntax error — now fixed via two new helpers in
internal/server/dispatch.go: isMultiStatementSQL + splitLeadingCompatNoopDDL
(mirrors splitLeadingRoleDDL, M0118-0008). Closes the loop #87 ledger row
in full (marked resolved) across all ~12 compatNoopCommandTag prefixes.

Files touched: internal/server/dispatch.go (2 new helpers + call-site gate
at ~line 180); internal/server/dispatch_extended_ddl_test.go (2 new tests:
TestSimpleQueryMultiStatementCompatNoopBatchRejectsLaterSyntaxError +
TestSimpleQueryMultiStatementCompatNoopDDLStillRecurses); design doc
docs/design/0119-0004-database-config-set-pgdump.md ("Follow-up: ...
multi-statement-batch masking fix (loop #88)" section) + README.md row
0119-0004cz; .ralph/fix_plan.md + deferral_ledger.md entries.

Gates run this loop: go build ./... clean; go vet ./... clean; gofmt -l
clean; go test ./internal/server/... PASS (full package); new test
verified RED against pre-fix dispatch.go via a temporary
`git stash push -- internal/server/dispatch.go` round-trip;
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); make ralph-state-guard
self-repaired a stale completed-marker, OK after repair; pgbench smoke
(pre-commit hook) PASS, 0 failed transactions across TPC-B/simple-update/
select-only.

Next step (pick one, no strong precedent forcing either):
1. Continue M0119-0004-ACLHEAP backlog per "Current Priority" banner — no
   other open items are currently named in the deferral ledger for this
   task-id (grep `M0119-0004` with `status: -` in .ralph/deferral_ledger.md
   to confirm nothing else is outstanding before picking a fresh item).
2. M0120-0002 (WordPress verification WP-01..16) — still BLOCKED per
   fix_plan.md: needs human authorization to run `DROP TABLE wp_* CASCADE`
   + `wp core install` + `wp/seed/seed.sh` on the shared wp goopg instance
   (stale pre-serial-fix schema, root cause already diagnosed and recorded).
   Do not attempt the DROP TABLE without explicit human go-ahead.
