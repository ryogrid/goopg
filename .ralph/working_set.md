(idle — nothing in flight)

Last completed (loop #50): M0110-0004 — RW-002 (b), the unclean-shutdown +
--force branch of pg_resetwal 001_basic.pl. **M0110-0004 is now COMPLETE**
(full pg_resetwal TAP suite ported: RW-001/002/003/004 all `port`).
Committed + pushed.

What this loop did: gave goopg a *real* immediate shutdown so the
"database server was not shut down cleanly" + `--force` branch can be
reproduced (the last RW-002 remainder, previously deferred as "goopg v0 has
no unclean shutdown state").
  - internal/control/control.go: new STOPIMMEDIATE verb + OnStopImmediate
    callback (falls back to OnStop).
  - internal/server/server.go: Config.OnStopImmediate + cl.OnStopImmediate
    handler — runs NO CheckpointNow, invokes the hook, then runCancel; pidfile
    still removed by the normal defer s.stopControlPlane().
  - internal/initdb/open.go: Runtime.SetImmediateShutdown() sets a flag;
    Close() then SKIPS the final CheckpointShutdown → pg_control stays
    DB_IN_PRODUCTION (unclean; recovered via WAL replay next start).
  - cmd/goopg/main.go: `goopg stop -mode immediate` sends STOPIMMEDIATE and
    wires cfg.OnStopImmediate→rt.SetImmediateShutdown(); smart/fast stay STOP.
  - internal/testport/pgresetwal_port_test.go: TestPort_PgResetwal001BasicForce
    (start → checkpoint to stamp DB_IN_PRODUCTION → stop immediate → pg_resetwal
    refuses w/o --force → --force → restart → SELECT 1).
Docs: design 0110-0004 (Status→complete, loop-#50 section), README index row,
CSV RW-002→port + regenerated .md, fix_plan M0110-0004 [x] + loop-#50 note,
deferral_ledger closure line.

Gates: all 4 TestPort_PgResetwal* PASS (4.9s); go test -race
./internal/control ./internal/server clean; go test ./internal/initdb PASS
(109s); go build ./... clean.

Faithfulness note: upstream stamps DB_IN_PRODUCTION at end of startup recovery;
goopg stamps it on each running checkpoint (initdb leaves DB_SHUTDOWNED), so the
test runs one explicit checkpoint after start. A startup-time stamp stays
intentionally deferred (a standby in recovery must not be flagged in-production).

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming
-X stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump
002 / index AMs), M0110-0003 (pg_amcheck verify_heapam + opclass).

⚠ TREE NOTE: a SEPARATE manual claude session has uncommitted WIP across
internal/{executor,planner,catalog,analyzer,parser,mvcc,server}/dispatch.go +
untracked test files + postgres/ + validate-ralph-state. NOT ralph's — stage
only your own files (git add <paths>), never `git add -A`.
