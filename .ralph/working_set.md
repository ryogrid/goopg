Task: M0095-0003 (pg_basebackup TAP port) — `-X stream` backup-execution coverage.

LANDED loop #7 (committed): `TestPort_PgBasebackup010StreamWAL` in
internal/testport/pgbasebackup_port_test.go. Drives real `pg_basebackup -X stream`
against a live goopg cluster → second replication connection issues START_REPLICATION,
streams WAL into the backup pg_wal/ concurrently with BASE_BACKUP. Asserts
backup_label/global/pg_control/PG_VERSION + ≥1 streamed 24-char WAL segment. PASS
(1.76s); `-X none` execution test still PASS (no regression). CSV BB-010 updated +
markdown regenerated + fix_plan note. All in uncontaminated files; ZERO engine change
(walsender already did the work — replication.go replyStartReplication L354-571; the
line-9 "ships next loop" header comment is stale but left untouched for scope).

WHY this task (not M0110-0003): the amcheck engine is logic-complete since loop #61
("every tier bt_check_every_level performs is ported; only the SQL surface remains").
The SQL surface + M0110-0001 (catalog/pg_extension) + M0110-0002 (index AMs) are ALL
gated by the contaminated tree / huge missing features. M0095-0003's `-X stream` was
the one open increment in uncontaminated files (server/wal), and the START_REPLICATION
dependency it waited on had already landed via M0102. Pivoted to make real progress
instead of re-asserting the M0110-0003 block a 4th loop (busy-work).

CONTAMINATION (unchanged): 18 files modified at identical mtime 2026-06-13 14:28:14,
static ~21h — single foreign snapshot (774 ins / 237 del; lockrows +393, join_agg,
planner, analyzer, catalog, parser, dispatch). Do NOT `git add -A` / commit it. The
M0110-0003 SQL surface still needs to edit catalog.go/parser/dispatch → still blocked.

Next step (M0095-0003 resume): add `--manifest` parity via `bbsink_manifest` emulation
(next committable increment in basebackup.go, uncontaminated). After that: 011 in-place
tablespace + 020/030/040 streaming branches. M0110-0003 SQL surface still needs the
foreign WIP stashed/committed by a human.

Gates run loop #7: go test TestPort_PgBasebackup010* PASS; gofmt clean;
gen-oracle-port-status regen OK; make ralph-state-guard (before status block).
