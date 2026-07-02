(idle — nothing in flight)

Loop #39 summary: found loop #38's work (root-0023 follow-up — per-session
`log_statement` GUC wiring) fully implemented but uncommitted in the working
tree (working_set.md itself was stale, still describing loop #37). Verified
the diff (internal/server/statement_log.go/query.go/dispatch_extended.go/
statement_log_test.go + docs/fix_plan/ledger updates), re-ran gates myself
(`go build ./...`, `go vet ./...`, `go test -count=1 ./internal/server/...
./internal/config/...`, `scripts/tpch-spotcheck.sh` PASS Q12=2/Q13=33) before
trusting the prior loop's narrative, then committed+pushed as c87a1b62
(pgbench smoke pre-commit hook PASS). No new code authored this loop; this
was a verify-and-land of already-written work per the
`ralph_verify_background_agent_hardoff_before_commit` memory note.

M0120-0001 (WordPress verification harness restart) remains STANDING BLOCKED
— needs human action (systemctl --user stop goopg-wp.scope denied twice by
the permission classifier, loops #36/#37). Do not retry without new signal.

Next-loop candidates (unblocked, well-scoped):
- M0119-0004-ACLHEAP residuals still open per ledger rows 375-378: predefined
  PG role live-registration (16 roles in internal/initdb/initdb.go's
  `predefined` slice → catalog.InMemory.roles/RoleOID at bootstrap) unlocks
  the ROLE_PG_DATABASE_OWNER carve-out; `reserved_class_prefix` extension-GUC
  namespace needs an extension-loading mechanism goopg doesn't have (larger).
- M0119-0004 pg_dump 002-010 TAP (fix_plan.md:2183) — long-running slice,
  many sub-features already landed (NND enforcement, SET CONSTRAINTS); check
  tail of that entry for the current open sub-item before picking it up.
- M0095-0003 pg_basebackup 010/011/020 (fix_plan.md:63).
- M0119-0005/0006/0007 — pg_waldump/pg_amcheck server tier, pg_basebackup
  recvlogical (fix_plan.md:5071-5078).
- root-0023 sibling GUC stubs (log_min_duration_statement, log_line_prefix,
  logging_collector/log_directory) — independently sized, not urgent.

Gates run (this loop): go build/vet clean; internal/server+internal/config
PASS; scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); pgbench smoke PASS
(pre-commit hook); make ralph-state-guard auto-repaired a stale
running/completed progress.json mismatch (unrelated to this task, same
pattern as loop #37).
