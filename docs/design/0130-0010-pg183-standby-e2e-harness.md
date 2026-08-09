# PG 18.3 standby E2E harness — basebackup + streaming + failover + reverse attach

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S10)

## Problem

M0130's ultimate acceptance vehicle: a PG 18.3 standby must start from a
goopg-created base backup, stream WAL, replay DDL+DML, survive failover,
and continue operating. No single test exercises this full path today
(components exist: `TestE2E_FailoverGoopgToPG` covers a narrow slice).

## Design

### E2E scenario (happy path)

1. **Start goopg primary** — fresh init, CREATE TABLE + INSERT workload.
2. **Create replication slot** (physical) on goopg primary.
3. **pg_basebackup** from goopg primary using the real `pg_basebackup` binary
   from `./postgres/local_install/bin/`.
4. **Configure PG standby:**
   - Write `postgresql.conf` with `primary_conninfo` pointing to goopg
     primary, `restore_command` empty, `primary_slot_name` = the slot name.
   - Create `standby.signal` file.
5. **Start PG standby** via `pg_ctl start`.
6. **Verify catch-up:** INSERT additional rows on goopg primary; verify
   they appear on PG standby via `psql` SELECT.
7. **DDL on primary:** CREATE TABLE, ADD COLUMN, CREATE INDEX, CREATE VIEW
   (requires S4–S6 completion).
8. **Verify DDL replays** on PG standby without FATAL.
9. **Failover:** promote PG standby via `pg_ctl promote`.
10. **Verify promotion:** PG standby is now writable; INSERT succeeds.
11. **Re-attach:** goopg can connect as standby to the promoted PG primary
    (reverse direction, requires S8 multi-timeline).

### Harness extension

- Extend `internal/testutil/replcluster/` to manage a real PG 18.3 instance
  alongside goopg instances.
- PG binary path: `./postgres/local_install/bin/` (the project's oracle PG).
- Test function: `TestE2E_PGStandbyFullCycle` in `internal/testport/`.

### Reverse attach (S10.4)

- After PG promotion, start a goopg standby against the PG primary's data dir:
  - The data dir was originally a goopg base backup → goopg should start
    against it (validates goal 1 forward direction).
  - Connect via WalReceiver to the promoted PG primary on the new timeline
    (validates goal 3 reverse direction + S8 multi-timeline).

## Guards

1. Full cycle: goopg primary → PG standby → promote → goopg standby.
2. DDL replays on PG standby without rmid-128 FATAL.
3. Rows written at each stage are visible at the next stage.
4. The existing replication family stays green (no regressions).
5. UNITS + SMOKE green.

## What was built

- **`TestE2E_PGStandbyFullCycle`** (`internal/testport/e2e_pg183_standby_full_cycle_test.go`):
  the M0130 acceptance vehicle. Four-phase E2E test:
  1. **Phase A (forward):** goopg primary → `pg_basebackup -X stream -C -S <slot> -R` →
     PG 18.3 standby via `pgcluster.OpenExisting` + direct `postgres` binary boot
     (pg_ctl can't read PMStatus from a goopg backup). Verify base-backup and
     post-backup rows are visible on the standby.
  2. **Phase B (DDL/DML replay):** CREATE TABLE, CREATE INDEX, ADD COLUMN, INSERT
     on goopg primary → verify each replays on the PG standby without FATAL.
     Exercises WAL fidelity (S4–S7).
  3. **Phase C (failover):** kill goopg primary → `pg_ctl promote` →
     verify promoted PG is writable.
  4. **Phase D (reverse attach):** `pg_basebackup` from promoted PG →
     start goopg standby against the new-timeline PG primary → verify
     streaming + INSERT visibility + all historical rows survived the
     full cycle. Exercises multi-timeline (S8) and bidirectional
     cluster-directory compatibility (goal 1).
- **`waitForPhysicalStreamingPGtoGoopg`** — new helper for the PG→goopg
  streaming direction (PG's pg_stat_replication + goopg's pg_stat_wal_receiver).
- Gated on `testing.Short()` and `GOOPG_SKIP_M0130_E2E` env var.

## References

- `internal/testport/` — `TestE2E_PhysicalReplication`, `TestE2E_FailoverGoopgToPG`
- `internal/testutil/replcluster/` — harness
- `internal/server/basebackup.go` — BASE_BACKUP
- `internal/server/walreceiver.go` — WalReceiver
- `postgres/local_install/bin/pg_basebackup` — oracle PG client
- `postgres/local_install/bin/pg_ctl` — oracle PG server control
