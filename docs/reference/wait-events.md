# Wait Events in goopg

## Overview

PostgreSQL's `pg_stat_activity` exposes two wait-event columns —
`wait_event_type` and `wait_event` — that show what a backend is
currently waiting for.  goopg implements the same taxonomy (subset)
and records wait events at blocking-operation boundaries.

## Wait Event Types

Every wait event has a **type** (the broad category) and a **name**
(the specific operation).  The type corresponds to upstream's
`PG_WAIT_*` class constants.

| Type constant    | PG class            | Meaning                              |
|------------------|---------------------|--------------------------------------|
| `IO`             | `PG_WAIT_IO`        | File / device I/O                    |
| `Lock`           | `PG_WAIT_LOCK`      | Heavyweight lock wait                |
| `Client`         | `PG_WAIT_CLIENT`    | Client socket read / write           |
| `IPC`            | `PG_WAIT_IPC`       | Inter-process communication          |
| `Timeout`        | `PG_WAIT_TIMEOUT`   | Timer / delay wait                   |
| `Activity`       | `PG_WAIT_ACTIVITY`  | Background process main-loop idle    |
| `LWLock`         | `PG_WAIT_LWLOCK`    | Lightweight lock wait                |
| `BufferPin`      | `PG_WAIT_BUFFERPIN` | Buffer pin wait                      |

## Wait Event Names By Type

### IO (`WaitTypeIO`)

| Constant              | Display name              | When fired                                 |
|-----------------------|---------------------------|--------------------------------------------|
| `WaitAIO`             | `AIO`                     | `Handle.Wait()` — async I/O completion     |
| `WaitDataFileRead`    | `DataFileRead`            | `Manager.ReadBlock` (synchronous read)     |
| `WaitDataFileWrite`   | `DataFileWrite`           | `Manager.WriteBlock` (synchronous write)   |
| `WaitDataFileExtend`  | `DataFileExtend`          | `Manager.Extend`                           |
| `WaitDataFileSync`    | `DataFileSync`            | `Manager.Sync`                             |
| `WaitDataFileFlush`   | `DataFileFlush`           | `Manager.Flush` / `fdatasync`              |
| `WaitDataFilePrefetch`| `DataFilePrefetch`        | `Manager.PrefetchBlock` (AIO prefetch)     |
| `WaitWALRead`         | `WALRead`                 | WAL segment read                           |
| `WaitWALWrite`        | `WALWrite`                | WAL segment write                          |
| `WaitWALSync`         | `WALSync`                 | WAL `fdatasync` / `Fsync`                 |
| `WaitWALInitWrite`    | `WalInitWrite`            | WAL segment initialisation write           |
| `WaitWALInitSync`     | `WalInitSync`             | WAL segment init sync                      |
| `WaitControlFileRead` | `ControlFileRead`         | `pg_control` file read                     |
| `WaitControlFileWrite`| `ControlFileWrite`        | `pg_control` file write                    |
| `WaitControlFileSync` | `ControlFileSync`         | `pg_control` `fdatasync`                  |
| `WaitBuffileRead`     | `BuffileRead`             | Temporary file read (sort/hash)            |
| `WaitBuffileWrite`    | `BuffileWrite`            | Temporary file write                       |

### Lock (`WaitTypeLock`)

| Constant             | Display name      | When fired                                    |
|----------------------|-------------------|-----------------------------------------------|
| `WaitRelationLock`   | `relation`        | `acquireRelLock` — relation-level lock wait   |
| `WaitTupleLock`      | `tuple`           | Tuple-level lock wait (FOR UPDATE)            |
| `WaitTransactionID`  | `transactionid`   | Waiting for transaction commit                |
| `WaitPageLock`       | `page`            | Page-level lock wait                          |
| `WaitExtendLock`     | `extend`          | Relation extension lock wait                  |
| `WaitAdvisoryLock`   | `advisory`        | Advisory lock wait                            |
| `WaitVirtualXID`     | `virtualxid`      | Virtual transaction ID wait                   |
| `WaitObjectLock`     | `object`          | Database object lock wait                     |
| `WaitUserLock`       | `userlock`        | User-defined lock wait                        |
| `WaitSpecToken`      | `spectoken`       | Speculative token lock wait                   |

### Client (`WaitTypeClient`)

| Constant           | Display name   | When fired                                   |
|--------------------|----------------|----------------------------------------------|
| `WaitClientRead`   | `ClientRead`   | Before every `ReadFrame` / `ReadStartupPacket` |
| `WaitClientWrite`  | `ClientWrite`  | Before every `WriteFrame` / `WriteRaw` / `Flush` |

### IPC (`WaitTypeIPC`)

| Constant                | Display name         | When fired                        |
|-------------------------|----------------------|-----------------------------------|
| `WaitSyncRep`           | `SyncRep`            | Synchronous replication confirm   |
| `WaitCheckpointDone`    | `CheckpointDone`     | Waiting for checkpoint completion |
| `WaitCheckpointStart`   | `CheckpointStart`    | Waiting for checkpoint to start   |
| `WaitBufferIO`          | `BufferIo`           | Waiting for buffer I/O completion |
| `WaitBackendTermination`| `BackendTermination` | Waiting for another backend exit  |

### Activity (`WaitTypeActivity`)

| Constant               | Display name          | When fired                              |
|------------------------|-----------------------|-----------------------------------------|
| `WaitCheckpointerMain` | `CheckpointerMain`    | Checkpointer main-loop idle             |
| `WaitWalWriterMain`    | `WalWriterMain`       | WAL writer main-loop idle               |
| `WaitWalSenderMain`    | `WalSenderMain`       | WAL sender main-loop idle               |
| `WaitAutoVacuumMain`   | `AutovacuumMain`      | Autovacuum launcher main-loop idle      |
| `WaitLogicalApplyMain` | `LogicalApplyMain`    | Logical apply worker main-loop idle     |
| `WaitLogicalLauncherMain`| `LogicalLauncherMain`| Logical replication launcher main-loop idle |
| `WaitBgwriterHibernate`| `BgwriterHibernate`   | Background writer hibernation           |

### Timeout (`WaitTypeTimeout`)

| Constant                  | Display name           | When fired                   |
|---------------------------|------------------------|------------------------------|
| `WaitPgSleep`             | `PgSleep`              | `pg_sleep()` call            |
| `WaitCheckpointWriteDelay`| `CheckpointWriteDelay` | Checkpoint cost-based delay  |
| `WaitVacuumDelay`         | `VacuumDelay`          | Vacuum cost-based delay      |

## goroutine Registration

Most wait-event hooks are fired from a backend's connection goroutine.
The connection goroutine is registered in `serveConn` via:

```go
activity.RegisterCurrentGoroutine(reg, pidStr)
```

and deregistered via:

```go
activity.ClearCurrentGoroutine()
```

The `LookupGoroutine()` function returns `(*Registry, pid)` for the
calling goroutine.  Hooks use this to find the correct backend entry.

**Background goroutines** (checkpointer, WAL writer, autovacuum
launcher, etc.) are NOT automatically registered.  To report wait
events from those goroutines, call `RegisterCurrentGoroutine` at the
start of their `Run` method and `ClearCurrentGoroutine` at the end.
Their backend entries must also be registered in the activity registry
(see `BackendType`).

## Implementation Status

| Wait event                | Status    | Location                           |
|---------------------------|-----------|------------------------------------|
| `ClientRead`              | ✅ Done   | `protocol/frame.go` hooks          |
| `ClientWrite`             | ✅ Done   | `protocol/frame.go` hooks          |
| `AIO`                     | ✅ Done   | `aio/aio.go` Handle.Wait           |
| `relation` (lock)         | ✅ Done   | `executor/context.go` acquireRelLock |
| `DataFileRead`            | ⬜ Pending| `storage/smgr.go` ReadBlock        |
| `DataFileWrite`           | ⬜ Pending| `storage/smgr.go` WriteBlock       |
| `DataFileExtend`          | ⬜ Pending| `storage/smgr.go` Extend           |
| `DataFileSync`            | ⬜ Pending| `storage/smgr.go` Sync             |
| `WALRead/Write/Sync`      | ⬜ Pending| `wal/writer.go` ops path           |
| `BufferPin`               | ⬜ Pending| `storage/bufpool.go` Pin wait      |
| `CheckpointerMain`        | ⬜ Pending| `wal/checkpointer.go` Run          |
| `WalWriterMain`           | ⬜ Pending| `wal/writer.go` loop               |
| `BuffileRead/Write`       | ⬜ Pending| temporary file I/O in executor     |
| `WalSenderMain`           | ⬜ Pending| replication walsender              |

## Reference

- Upstream PostgreSQL: `postgres/src/include/utils/wait_event.h`
- Upstream event definitions: `postgres/src/backend/utils/activity/wait_event_names.txt`
- goopg constants: `internal/activity/activity.go`
- goopg goroutine registration: `internal/activity/activity.go` (`RegisterCurrentGoroutine`)
- goopg client-I/O hooks: `internal/protocol/frame.go`
