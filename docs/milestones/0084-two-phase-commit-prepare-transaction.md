# Milestone 0084 — PREPARE TRANSACTION + pg_twophase persistence

**Status:** planned
**Depends on:** M0080 (persistence audit), M0050 (subxact
infrastructure)
**Drives:** PostgreSQL `PREPARE TRANSACTION` / `COMMIT
PREPARED` / `ROLLBACK PREPARED` support — required for XA
distributed transactions and any application that uses 2PC.

## Context

goopg currently rejects `PREPARE TRANSACTION` at the executor.
PostgreSQL implements 2PC by persisting prepared-transaction
state in `pg_twophase/<XID>` files: the transaction's locks,
subxids, and committable state remain visible to other
backends after the originating session disconnects, until a
later `COMMIT PREPARED` or `ROLLBACK PREPARED` resolves them.

## Required design docs

- `docs/design/0084-0001-prepare-transaction-overview.md`
  (state machine, persistence format, recovery story for
  prepared txns surviving a crash).
- `docs/design/0084-0002-twophase-wal-records.md`
  (`XLOG_XACT_PREPARE`, `XLOG_XACT_COMMIT_PREPARED`,
  `XLOG_XACT_ABORT_PREPARED`).

## Tasks

Tasks will be detailed when this milestone is picked up. See the
fix_plan.md note at the top of this file.

## Definition of Done (sketch)

- `PREPARE TRANSACTION 'gid'` writes a `pg_twophase/<XID>`
  file and a WAL record.
- After session disconnect, the prepared transaction is
  visible to other backends via `pg_prepared_xacts`.
- `COMMIT PREPARED 'gid'` / `ROLLBACK PREPARED 'gid'` resolves
  the transaction and removes the `pg_twophase` file.
- Prepared transactions survive a non-graceful restart
  (recovery loads `pg_twophase/` and applies the prepared
  state).
