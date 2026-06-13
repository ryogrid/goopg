Task: M0110-0003 — Port pg_amcheck TAP tests. Landed the CLI-only tier of
001_basic.pl; server-dependent tests (002–005) deferred under AC-002.
(idle — nothing in flight on this task once committed.)

Files (this loop):
- internal/testport/pgamcheck_port_test.go (NEW) — TestPort_PgAmcheck001Basic +
  runToolWithLib helper (sets LD_LIBRARY_PATH=postgres/local_install/lib).
  Reuses clientToolBin + repoRoot from the same package.
- docs/test-port/postgres-oracle-port-status.csv — AC-001 (port) + AC-002
  (defer) added below WD-002.
- docs/test-port/postgres-oracle-port-status.md — regenerated.
- docs/design/0110-0003-pg-amcheck-tap-port.md (NEW) + README index row.
- .ralph/fix_plan.md (M0110-0003 progress), .ralph/deferral_ledger.md.

Key facts learned this loop:
- pg_amcheck/t/001_basic.pl is only 14 lines: program_help_ok /
  program_version_ok / program_options_handling_ok / done_testing. Pure CLI.
- WRINKLE vs pg_dump/pg_waldump: bundled pg_amcheck links PQcancelBlocking
  (PG 17+ libpq cancel API). Run against the host's older libpq.so.5 it dies at
  startup: "undefined symbol: PQcancelBlocking". Fix = LD_LIBRARY_PATH at
  postgres/local_install/lib (new runToolWithLib helper; runTool left untouched
  so pg_dump/pg_waldump ports keep behaviour). util.CommandSpec.Env appends to
  os.Environ() (later wins), so setting just LD_LIBRARY_PATH is enough.
- Test-only port (internal/testport, drives upstream binary); no
  executor/planner/catalog code touched → TPC-H spotcheck gate not applicable.

Next step: pick next topmost fix_plan item. Candidates still isolated to
testport: M0110-0004 (pg_resetwal 001_basic — 247 lines, control-file parse,
depends on M0106 pg_control byte-compat; check whether a CLI-only tier exists).
Or M0110-0001/0002 server-tier resume once catalog/AM parity lands. Note
M0102-0009 (sync_remote_apply 45s timeout) and M0102-0010 (more initdb options)
are also open but heavier.

Gates run: gofmt clean; go vet ./internal/testport PASS;
go test -run TestPort_PgAmcheck001Basic ./internal/testport PASS;
go build ./... PASS; gen-oracle-port-status regenerated OK.
Pending: make ralph-state-guard (run immediately before status block).
