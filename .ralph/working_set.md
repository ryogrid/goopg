(idle — nothing in flight)

Loop #42 summary: implemented `log_line_prefix` GUC wiring (root-0023
follow-up), a well-scoped next-loop candidate flagged by loop #41.
Registered `log_line_prefix` (internal/config/defaults.go) as
ContextSigHup/BootVal "%m [%p] ", matching upstream guc_tables.c exactly —
config-file-only like real PG (not client-SET-able, unlike the
ContextSuset log_statement/log_min_duration_statement siblings), so it's
picked up via the existing postgresql.conf/-c ParseConfigFile/
ApplyConfigEntries path, no new env var. New `formatLogLinePrefix`/
`expandLogLineVerb`/`logLineFields` (internal/server/statement_log.go)
mirror elog.c's log_status_format, expanding the %m/%p/%u/%d/%a/%x/%%
subset (with numeric/negative padding) goopg's two statement/duration log
call sites have real data for; any other escape is dropped, matching PG's
own "ignore unrecognised specifier" default. `(*Server).logLinePrefix`/
`prefixAttr` attach the expansion as a leading "prefix" slog attr on both
logStatement and logDuration, omitted when the GUC is "". Tests:
TestFormatLogLinePrefix (every supported escape/padding/unknown-field/
unrecognised-escape-drop case), TestServerLogLinePrefix (registry-driven
attach/omit through logStatement) in statement_log_test.go. Also added the
commented default to postgresql.conf.sample (required by
TestSampleConfigCoversRegistry) and a nil-Registry guard in logLinePrefix
(existing unit tests construct &Server{cfg: Config{...}} without a
Registry). Design doc: docs/design/root-0023-statement-query-logging.md
new "Follow-up: log_line_prefix GUC (loop #42)" section + expanded "Out of
scope" list; docs/design/README.md root-0023 row updated; .ralph/fix_plan.md
[x] entry added; .ralph/deferral_ledger.md row appended (still-open:
%l/%c/%e/%r/%h/%i/%t/%n/%s/%v/%P/%b/%L/%Q escapes, server-wide prefix
application, logging_collector/log_directory).

Gates run (this loop): go build ./... / go vet ./internal/server/...
./internal/config/... clean; internal/server + internal/config suites PASS
(including the two new tests); scripts/tpch-spotcheck.sh PASS (Q12=2/
Q13=33); make ralph-state-guard auto-repaired the same recurring stale
running/completed progress.json mismatch as loops #37/#39/#41 (not a
genuine completion signal).

Next step: commit this loop's diff (statement_log.go/statement_log_test.go/
defaults.go/postgresql.conf.sample + the four bookkeeping files above),
push, then run the pgbench smoke via the pre-commit hook.

Next-loop candidates (unblocked, well-scoped) — unchanged from loop #41
except log_line_prefix is now partially done:
- root-0023's remaining `log_line_prefix` escapes (%l per-process counter,
  %c session id, %r/%h remote host, %b backend type, ...) each need new
  per-connection state goopg doesn't track today at the statement/duration
  log call sites — independently sized, not urgent.
- `logging_collector`/`log_directory` file sink — bigger, no existing goopg
  capability to build on (background log-rotation writer).
- M0119-0004-ACLHEAP residuals per ledger rows 375-378: predefined PG role
  live-registration (16 roles in internal/initdb/initdb.go's `predefined`
  slice -> catalog.InMemory.roles/RoleOID at bootstrap) unlocks the
  ROLE_PG_DATABASE_OWNER carve-out; `reserved_class_prefix` extension-GUC
  namespace needs an extension-loading mechanism goopg doesn't have (larger).
- M0119-0004 pg_dump 002-010 TAP (fix_plan.md — grep for current position,
  line numbers shift every loop that edits fix_plan.md).
- M0095-0003 pg_basebackup 010/011/020 (fix_plan.md:63).
- M0119-0005/0006/0007 — pg_waldump/pg_amcheck server tier, pg_basebackup
  recvlogical (fix_plan.md, re-grep — line numbers shift).

M0120-0001 (WordPress verification harness restart) remains STANDING
BLOCKED — needs human action (systemctl --user stop goopg-wp.scope denied
twice by the permission classifier, loops #36/#37). Do not retry without
new signal.
