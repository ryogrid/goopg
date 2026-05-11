# 0094-0003 — Recovery TAP Test Porting Strategy (D-003 Subset)

**Status:** draft
**Date:** 2026-05-11
**Milestone:** M0094-0003

## Background

`D-003` in `docs/test-port/postgres-oracle-port-status.csv` defers all 47 TAP
tests under `postgres/src/test/recovery/t/` pending replication capability growth.
M0094-0001 closes the physical-replication E2E gap. This doc defines which tests
to port first, how to adapt the Perl/TAP idioms to Go, and what remains deferred.

## Selection Criteria

A recovery test is portable in M0094 if:

1. Its core assertion relies only on features already in goopg
   (physical streaming replication, replication slots, crash recovery via WAL replay).
2. It does not require: PITR, WAL archiving, timeline branching, two-phase commit,
   synchronous replication, logical decoding via contrib plugins, or multi-database.
3. The upstream Perl helpers it uses (`PostgreSQL::Test::Cluster`,
   `PostgreSQL::Test::Utils`) have equivalents in the goopg test framework
   (`internal/testutil/replcluster`, `internal/testport/framework`).

## Ported Tests

### TestPort_Recovery001StreamRep
**Upstream:** `postgres/src/test/recovery/t/001_stream_rep.pl`
**Rationale:** Minimal streaming replication test. Creates primary + standby,
verifies WAL streaming, checks that INSERT on primary is visible on standby.
Directly verifiable once M0094-0001 lands.

**Key assertions:**
- Standby connects and streams WAL from primary.
- Row inserted on primary is visible on standby within a timeout.
- Standby status updates (`pg_stat_replication`) show non-zero sent/written LSN.

**Adaptation notes:**
- `$primary->safe_psql(...)` → `rc.Primary.Exec(ctx, ...)`
- `$standby->poll_query_until(...)` → polling loop with `rc.Standby.Query(...)`
- No archiving required; skip archive-related assertions.

### TestPort_Recovery013CrashRestart
**Upstream:** `postgres/src/test/recovery/t/013_crash_restart.pl`
**Rationale:** Tests that a server killed with SIGKILL restarts cleanly and WAL
replay recovers the committed state. Exercises goopg's crash-recovery path which
is already tested in isolation but benefits from oracle-level verification.

**Key assertions:**
- After kill -9 and restart, rows committed before kill are present.
- Rows in an open transaction at kill time are absent (rolled back via WAL replay).

**Adaptation notes:**
- `$node->stop('immediate')` → `syscall.Kill(pid, syscall.SIGKILL)` + restart.
- The test verifies via psql; use `framework.Conn.Query(...)`.
- No standby needed; single-node crash test.

### TestPort_Recovery019ReplslotLimit
**Upstream:** `postgres/src/test/recovery/t/019_replslot_limit.pl`
**Rationale:** Verifies `max_replication_slots` GUC. Creating more slots than
the limit must return an error; existing slots must not be affected.

**Key assertions:**
- `CREATE_REPLICATION_SLOT` succeeds up to `max_replication_slots`.
- Next `CREATE_REPLICATION_SLOT` returns error SQLSTATE 55000 or equivalent.
- Dropping a slot allows a new one to be created.

**Adaptation notes:**
- Start server with `max_replication_slots=2` in postgresql.conf.
- Use replication protocol directly (`framework.ReplConn`) to create/drop slots.
- No standby needed.

### TestPort_Recovery038SaveLogicalSlots
**Upstream:** `postgres/src/test/recovery/t/038_save_logical_slots_shutdown.pl`
**Rationale:** Verifies logical replication slots are persisted to disk on clean
shutdown and survive restart. Depends on M0094-0002 (logical replication working)
and the slot persistence in `internal/wal/slots.go`.

**Key assertions:**
- Create a logical slot, shut down cleanly.
- After restart, the slot is listed in `pg_replication_slots` with the same
  `restart_lsn` and `confirmed_flush_lsn`.

**Adaptation notes:**
- Start server, create logical slot via replication protocol.
- Stop server cleanly (`goopg stop` / SIGTERM).
- Restart; query `pg_replication_slots`; verify slot survives.

### TestPort_Recovery039EndOfWal
**Upstream:** `postgres/src/test/recovery/t/039_end_of_wal.pl`
**Rationale:** Tests WAL segment boundary handling — specifically that the server
correctly handles transitioning from one WAL segment to the next and that the
standby can follow through the segment boundary.

**Key assertions:**
- WAL switches from segment N to N+1 during sustained writes.
- Standby follows the segment switch and stays in sync.
- No data loss across the segment boundary.

**Adaptation notes:**
- Use small `wal_segment_size` (1 MiB) to trigger rapid segment rotation.
- Confirm via `pg_current_wal_lsn()` that LSN crosses segment boundary.
- Verify standby's `pg_last_wal_replay_lsn()` follows.

### TestPort_Recovery047CheckpointPhysicalSlot
**Upstream:** `postgres/src/test/recovery/t/047_checkpoint_physical_slot.pl`
**Rationale:** Verifies that a physical replication slot's `restart_lsn` is
advanced to at least the checkpoint LSN during a checkpoint, preventing
unnecessary WAL retention.

**Key assertions:**
- Create physical slot; run checkpoint; verify `restart_lsn` advances.
- WAL segments below the new `restart_lsn` are eligible for removal.

**Adaptation notes:**
- Create physical slot in `replication` mode.
- Issue `CHECKPOINT` SQL command.
- Query `pg_replication_slots.restart_lsn` and verify it advanced.

## Port Pattern

All tests live in `internal/testport/recovery_port_test.go`. The file starts with:

```go
// Package testport contains oracle TAP test ports for goopg.
// Run: go test -v -run TestPort_Recovery ./internal/testport/
package testport
```

Each test function follows the pattern established in `tap_port_test.go`:

```go
// TestPort_Recovery001StreamRep ports postgres/src/test/recovery/t/001_stream_rep.pl
// Upstream: minimal streaming replication test.
func TestPort_Recovery001StreamRep(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping replication test in short mode")
    }
    // ... test body using replcluster or framework helpers ...
}
```

Tests are **not** run as part of `go test ./...` (they are slow). Run explicitly:

```bash
go test -v -run TestPort_Recovery ./internal/testport/
go test -v -run TestPort_Recovery001StreamRep ./internal/testport/
```

## Deferred Tests (Out of Scope for M0094)

| Test | Deferral reason |
|------|----------------|
| 002_archiving | Requires archive_command / restore_command infrastructure |
| 003_recovery_targets | Requires PITR (recovery_target_{time,name,xid}) |
| 004_timeline_switch | Requires multi-timeline WAL branching |
| 005_replay_delay | Requires recovery_min_apply_delay GUC |
| 006_logical_decoding | Requires contrib/test_decoding plugin |
| 007_sync_rep | Requires synchronous_standby_names GUC + sync mode |
| 009_twophase | Requires PREPARE TRANSACTION (two-phase commit) |
| 010_logical_decoding_timelines | Requires timeline branching + logical decoding |
| 012_subtransactions | Complex subtransaction recovery scenarios |
| 013–048 (remainder) | Various combinations of the above missing features |

## CSV Updates

For each ported test, add a row to `docs/test-port/postgres-oracle-port-status.csv`:

```
R-001,postgres/src/test/recovery/t/001_stream_rep.pl,tap,port,yes,Ported as TestPort_Recovery001StreamRep in internal/testport/recovery_port_test.go,-
R-013,postgres/src/test/recovery/t/013_crash_restart.pl,tap,port,yes,Ported as TestPort_Recovery013CrashRestart,-
R-019,postgres/src/test/recovery/t/019_replslot_limit.pl,tap,port,yes,Ported as TestPort_Recovery019ReplslotLimit,-
R-038,postgres/src/test/recovery/t/038_save_logical_slots_shutdown.pl,tap,port,yes,Ported as TestPort_Recovery038SaveLogicalSlots,-
R-039,postgres/src/test/recovery/t/039_end_of_wal.pl,tap,port,yes,Ported as TestPort_Recovery039EndOfWal,-
R-047,postgres/src/test/recovery/t/047_checkpoint_physical_slot.pl,tap,port,yes,Ported as TestPort_Recovery047CheckpointPhysicalSlot,-
```

Update D-003 row: add `deferred_to=M0094` (subset ported); remaining tests still
deferred at the suite level pending further capability growth.

## PostgreSQL Reference

- `postgres/src/test/recovery/t/` — upstream test sources.
- `postgres/src/backend/replication/` — walsender, walreceiver, slot lifecycle.
- `postgres/src/backend/access/transam/xlog.c` — checkpoint, WAL segment switch.
