# REF-022: Session & Transaction Management

…(existing content up to "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### Subtransactions (SAVEPOINT)

PostgreSQL supports nested transactions via `SAVEPOINT` /
`ROLLBACK TO SAVEPOINT` / `RELEASE SAVEPOINT`. Each savepoint
creates a subtransaction stored as a `TransactionState` node in
a linked list. On rollback to a savepoint, all changes made
since the savepoint are undone (via WAL redo for the aborted
range).

goopg does not implement savepoints.

### Prepared Transactions (Two-Phase Commit)

`PREPARE TRANSACTION 'id'` persists the transaction's state to
disk (`pg_twophase/`). The transaction can later be committed
or rolled back by any session via `COMMIT PREPARED 'id'` or
`ROLLBACK PREPARED 'id'`. This enables distributed transaction
coordinators (like XA) to coordinate across multiple databases.

goopg does not implement prepared transactions.

### Process Model vs Goroutine Model

PostgreSQL uses one OS process per connection. This provides:

- Strong memory isolation — a crash in one connection cannot
  corrupt other connections.
- Direct resource accounting — each process's RSS is visible
  to the OS.
- Simpler locking — no goroutine scheduler preemption at
  arbitrary points.

goopg uses one goroutine per connection. This provides:

- Lower per-connection overhead (~4 KB stack vs ~8 MB stack).
- Cheaper context switches (goroutine vs kernel thread).
- Shared memory for data structures (no shared memory setup).

### GUC Memory Management

PostgreSQL's GUCs are stored in:
- `ConfigFileLinenoStack` — for file tracking.
- `GUC_Nametab` — the main GUC array, indexed by name.
- Per-backend `guc_variables` — runtime values.

GUCs of type `PGC_POSTMASTER` require a restart to change.
`PGC_SIGHUP` can be changed by reloading the config file.
`PGC_BACKEND` is set at connection startup. `PGC_SUSET`
requires superuser. `PGC_USERSET` can be set by any user.

goopg's GUC system is simpler: all GUCs can be set via `SET`
(no PGC_POSTMASTER/PGC_SIGHUP distinction). Config file entries
are applied on top of defaults at startup.

## goopg Improvement Analysis

### P2: Savepoints

Implement SAVEPOINT by tracking subtransaction save/restore
points in the MVCC manager. On rollback to savepoint, replay
the inverse of WAL records generated since the savepoint.

### P2: GUC Categories

Add PGC_POSTMASTER, PGC_SIGHUP, PGC_BACKEND categories.
Validate that GUCs are set at the correct level.

**Impact:** Better compatibility with PostgreSQL config files
that expect certain GUC categories.

## References

- goopg: `internal/server/server.go`
- goopg: `internal/mvcc/manager.go`
- goopg: `internal/config/`
- PG transaction: `postgres/src/backend/access/transam/xact.c`
- PG subtransaction: `postgres/src/backend/access/transam/xact.c`
  (`PushTransaction`, `PopTransaction`)
- PG PREPARE TRANSACTION: `postgres/src/backend/access/transam/twophase.c`
