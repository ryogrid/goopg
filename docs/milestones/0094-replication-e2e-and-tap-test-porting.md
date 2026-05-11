# Milestone 0094 — Replication E2E Completion & TAP Test Porting (D-003 / D-004)

**Status:** in-progress
**Depends on:** M0005 (streaming replication), M0008 (logical replication)
**Blocks:** D-003 (recovery TAP suite), D-004 (subscription TAP suite)

## Context

Streaming replication (M0005) and logical replication (M0008) are substantially
implemented — WAL senders, receivers, slot machinery, pgoutput encoder/decoder,
apply worker, system views — but two critical gaps prevent the end-to-end tests
from running:

1. **Physical replication:** `replcluster.Setup()` has no hook for running SQL
   on the primary before the standby data-directory clone. Without it, tables
   created in the test body are created after the clone point, and the test
   cannot verify that DDL-class WAL records (heap page-init for catalog tables)
   are replayed on the standby. Test skipped with:
   `"v0 does not replicate DDL through WAL; replcluster.Setup() has no pre-clone hook"`

2. **Logical replication:** `applyworker.go::applyDelete()` is a no-op; UPDATE
   is unhandled. Test skipped with:
   `"logical replication requires DDL WAL records, not supported in v0"`

Closing these gaps also unblocks the two largest deferred TAP suites:
- **D-003** (`postgres/src/test/recovery/t/`) — 47 Perl tests, deferred to M0094
- **D-004** (`postgres/src/test/subscription/t/`) — 36 Perl tests, deferred to M0094

This milestone closes both gaps, ports a prioritised subset of each suite, and
marks M0005 and M0008 complete once their DoD checklists are verified.

## In Scope

### M0094-0001 — Streaming Replication E2E Gap Close

- Add `PreCloneHook func(*Conn) error` to `replcluster.Options`. `Setup()` calls
  the hook (if non-nil) after the primary starts and before `pg_basebackup`-style
  clone, allowing callers to create tables while the primary is running.
- Audit `internal/wal/stream_replayer.go` and `internal/wal/recovery.go` for
  record types silently skipped. Confirm catalog-mutation WAL records (heap
  insert/delete on system tables) are replayed correctly on the standby.
- Un-skip `TestE2E_PhysicalReplication` with a `PreCloneHook` that creates
  `repl_t (id int)`, inserts a row on the primary, waits for standby to catch
  up, and reads back the row from the standby.

Design doc: `docs/design/0094-0001-streaming-replication-e2e-gap.md`

### M0094-0002 — Logical Replication Apply Completeness (DELETE + UPDATE)

- Implement `applyDelete()` in `internal/executor/applyworker.go`: decode the
  key tuple from the pgoutput `D` message and locate the target row via a heap
  scan using the decoded key columns as equality filters.
- Extend `internal/wal/pgoutput.go` to emit pgoutput `U` (Update) messages
  instead of a paired D+I for the executor's HOT-update path.
- Fold consecutive `(xid, rel, Delete)` + `(xid, rel, Insert)` events in
  `internal/wal/reorder.go` into a single Update event so the apply worker
  receives a unified `U` message.
- Implement `applyUpdate()` in `applyworker.go`: use old-tuple key to locate
  the row, replace it with the new tuple.
- Un-skip `TestE2E_LogicalReplication` exercising INSERT + DELETE + UPDATE.

Design doc: `docs/design/0094-0002-logical-apply-delete-update.md`

### M0094-0003 — Port D-003 Recovery TAP Tests (Subset)

Port 6 recovery tests from `postgres/src/test/recovery/t/` to
`internal/testport/recovery_port_test.go`:

| Upstream file | Go test function | What it covers |
|---|---|---|
| `001_stream_rep.pl` | `TestPort_Recovery001StreamRep` | Core physical streaming replication |
| `013_crash_restart.pl` | `TestPort_Recovery013CrashRestart` | Server crash + WAL recovery restart |
| `019_replslot_limit.pl` | `TestPort_Recovery019ReplslotLimit` | `max_replication_slots` GUC enforcement |
| `038_save_logical_slots_shutdown.pl` | `TestPort_Recovery038SaveLogicalSlots` | Logical slot persistence across clean shutdown |
| `039_end_of_wal.pl` | `TestPort_Recovery039EndOfWal` | End-of-WAL segment boundary handling |
| `047_checkpoint_physical_slot.pl` | `TestPort_Recovery047CheckpointPhysicalSlot` | Physical slot LSN advance during checkpoint |

Update `docs/test-port/postgres-oracle-port-status.csv`: add one row per ported
test with `status=port, pass_required=yes`.
Regenerate `docs/test-port/postgres-oracle-port-status.md` via
`go run ./cmd/gen-oracle-port-status`.

Design doc: `docs/design/0094-0003-recovery-tap-porting-strategy.md`

### M0094-0004 — Port D-004 Subscription TAP Tests (Subset)

Port 3 subscription tests from `postgres/src/test/subscription/t/` to
`internal/testport/subscription_port_test.go`:

| Upstream file | Go test function | What it covers |
|---|---|---|
| `001_rep_changes.pl` | `TestPort_Subscription001RepChanges` | Basic INSERT/DELETE/UPDATE logical replication |
| `004_sync.pl` | `TestPort_Subscription004Sync` | Initial table sync (copy + streaming handoff) |
| `026_stats.pl` | `TestPort_Subscription026Stats` | `pg_stat_subscription` and `pg_stat_replication` metrics |

Update `docs/test-port/postgres-oracle-port-status.csv` and regenerate `.md`.

Design doc: `docs/design/0094-0004-subscription-tap-porting-strategy.md`

### M0094-0005 — Mark M0005 and M0008 Complete

Verify each DoD checklist for M0005 and M0008 against the current codebase.
Update `docs/milestones/0005-streaming-replication-support.md` status to
`complete` and `docs/milestones/0008-logical-replication-support.md` status to
`complete` once both E2E tests pass and the ported TAP tests pass.

## Out of Scope

The following are explicitly deferred from M0094:

**Recovery TAP tests deferred:**
002 (WAL archiving), 003 (PITR), 004 (timeline switch), 005 (replay_delay),
006 (logical decoding via contrib), 007 (synchronous replication mode),
009 (two-phase commit), 010 (timelines + logical decoding), 012 (subtransactions),
014 (unlogged tables reinit), 015–018, 020–037, 040–046, 048.

**Subscription TAP tests deferred:**
002 (complex datatypes / arrays), 005 (multi-encoding), 006 (heap rewrite),
007 (DDL replication), 008 (schema divergence), 009 (materialized views),
010 (TRUNCATE replication), 011 (generated columns), 012 (non-deterministic collation),
013 (partitioned tables), 014 (binary format), 015–019 (pgoutput v2 streaming),
020 (decoding messages), 021–023 (two-phase commit), 024 (ADD/DROP PUBLICATION),
025 (schema publications), 027 (non-superuser permissions), 028 (row filters),
029 (on-error handling), 030 (origin parameter), 031 (column lists),
032 (subscriber index hints), 033 (table-owner permissions), 034 (temporal tables),
035 (multiple unique conflicts), 100 (bug regressions).

**Other out-of-scope items:**
- Automatic failover / leader election
- Synchronous replication quorum policies
- Cross-version (goopg ↔ upstream PostgreSQL) logical replication
- Archiving (archive_command / restore_command)
- Point-in-time recovery (PITR)
- Multi-timeline WAL branching

## Required Design Docs

- `docs/design/0094-0001-streaming-replication-e2e-gap.md`
- `docs/design/0094-0002-logical-apply-delete-update.md`
- `docs/design/0094-0003-recovery-tap-porting-strategy.md`
- `docs/design/0094-0004-subscription-tap-porting-strategy.md`

## Definition of Done

1. `TestE2E_PhysicalReplication` passes: primary → standby, table created in
   `PreCloneHook`, row inserted on primary, row visible on standby after replay.
2. `TestE2E_LogicalReplication` passes: publisher → subscriber, INSERT + DELETE
   + UPDATE all applied correctly.
3. All 6 ported recovery TAP tests pass (`TestPort_Recovery*`).
4. All 3 ported subscription TAP tests pass (`TestPort_Subscription*`).
5. `docs/test-port/postgres-oracle-port-status.csv` updated for all 9 ported
   tests (`status=port, pass_required=yes`); regenerated `.md` matches.
6. `docs/milestones/0005-streaming-replication-support.md` status → `complete`.
7. `docs/milestones/0008-logical-replication-support.md` status → `complete`.
8. All 4 required design docs merged with status `accepted`.
9. `go test ./...` (non-oracle suite) shows no regressions.
10. `make ralph-state-guard` passes.
