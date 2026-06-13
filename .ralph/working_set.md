Task: M0110-0004 — Port pg_resetwal TAP tests. Landed the CLI-decidable tier
of 001_basic.pl; server-dependent tier + 002_corrupted.pl deferred under RW-002.
(idle — nothing in flight on this task once committed.)

Files (this loop):
- internal/testport/pgresetwal_port_test.go (NEW) — TestPort_PgResetwal001Basic.
  Reuses programHelpOk/programVersionOk/programOptionsHandlingOk/
  commandFailsContaining/clientToolBin/runTool (no new helper needed).
- docs/test-port/postgres-oracle-port-status.csv — RW-001 (port) + RW-002
  (defer) added below AC-002.
- docs/test-port/postgres-oracle-port-status.md — regenerated.
- docs/design/0110-0004-pg-resetwal-tap-port.md (NEW) + README index row.
- .ralph/fix_plan.md (M0110-0004 progress).
- .ralph/progress.json — reconciled stale "completed" → "in_progress" (the
  guard's auto-repair could not match: progress ts was within max-skew and
  OLDER than status ts, so its only rule — mark status completed — didn't fire;
  the loop was actively running so in_progress is the correct mid-loop state).

Key facts learned this loop:
- pg_resetwal/t/001_basic.pl is 247 lines, two tiers. CLI tier = help/version/
  options + too-many-args/no-data-dir/nonexistent-dir + option-arg validation
  (-c/-e/-l/-m/-o/-O/-u/-x/--wal-segsize/--char-signedness).
- ORDERING (pg_resetwal.c main): --help/--version short-circuit; then getopt_long
  loop emits every option-arg error and exit(1) INSIDE the loop; then
  too-many-args / no-data-dir checks; only THEN GetDataDirectoryCreatePerm /
  chdir / read_controlfile touch the dir. So all CLI-tier cases are decided
  before any directory access → pass a nonexistent dir, stays server-free.
- The two upstream cases that SUCCEED (`-m 0,10`, control-override block) need a
  real initialized dir → server tier, deferred.
- pg_resetwal does NOT link libpq → plain runTool (no LD_LIBRARY_PATH shim,
  unlike pg_amcheck's runToolWithLib in M0110-0003).
- Test-only port (drives upstream binary); no executor/planner/catalog code
  touched → TPC-H spotcheck gate not applicable.

Next step: commit + push. Then pick next topmost fix_plan item. Remaining M0110:
M0110-0001/0002/0003 server tiers (need catalog/AM parity), M0110-0004 RW-002
server tier (needs pg_control round-trip M0106 + SLRU layout parity). Also open:
M0095-0003 (basebackup streaming), M0102-0009 (sync_remote_apply 45s),
M0102-0010 (more initdb options — encoding/locale/waldir/checksums/auth/...).

Gates run: gofmt clean; go vet ./internal/testport PASS;
go test -run TestPort_PgResetwal001Basic ./internal/testport PASS;
go build ./... PASS; gen-oracle-port-status regenerated OK;
make ralph-state-guard PASS (after progress.json reconcile).
