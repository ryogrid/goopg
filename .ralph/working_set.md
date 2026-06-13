(idle — nothing in flight)

Last completed: M0110-0004 server-tier pg_control round-trip ported (loop #45).

**Root cause found + fixed (WAL/control):** every checkpoint — including the
final `Runtime.Close` shutdown checkpoint — stamped pg_control
`State=DB_IN_PRODUCTION`. So after a clean `goopg stop`, `pg_controldata`
showed "in production" and upstream `pg_resetwal` refused without `--force`
("database server was not shut down cleanly"). This was the sole blocker for
RW-002's pg_control round-trip tier.

**Fix:** `wal.Checkpointer.CheckpointShutdown()` (new) runs the checkpoint with
a `shutdown` flag → stamps `DB_SHUTDOWNED` (mirrors PG CHECKPOINT_IS_SHUTDOWN);
wired into `Runtime.Close` (the last durable checkpoint). OnStop checkpoint
stays DB_IN_PRODUCTION on purpose (crash in the OnStop→Close window stays
unclean). goopg startup replays WAL regardless of State, so restart/crash
recovery unaffected (verified).

Files: internal/wal/checkpointer.go (+CheckpointShutdown, shutdown flag),
internal/wal/checkpointer_test.go (+TestCheckpointerShutdownSetsDBShutdowned),
internal/initdb/open.go (Close → CheckpointShutdown),
internal/testport/pgresetwal_port_test.go (+TestPort_PgResetwal001BasicServer),
docs/test-port CSV+md (RW-003 port, RW-002 narrowed), design 0110-0004.

Gates run: build OK; `go test -race ./internal/wal ./internal/control
./internal/initdb` PASS; new wal unit test PASS; both pg_resetwal tiers PASS;
`TestPort_Recovery` PASS; ralph-state-guard PASS.

⚠️ Pre-existing failure NOT mine: `TestPort_WALPgWaldumpCompat` fails
("no WAL segments found") even with my source edits stashed — likely from the
concurrent session's uncommitted executor/parser/planner changes in the tree.
Out of RW-002 scope; flag separately.

Follow-up (deferred, documented in design doc): a *running* goopg shows
DB_SHUTDOWNED until the first online checkpoint; a startup DB_IN_PRODUCTION
stamp was deferred to avoid the standby-in-recovery edge (replication blast
radius). RW-002 still open for SLRU-derived overrides + unclean/--force +
002_corrupted.
