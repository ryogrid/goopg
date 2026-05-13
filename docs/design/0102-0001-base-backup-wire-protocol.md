# 0102-0001 — BASE_BACKUP Wire-Protocol Command on goopg Primary

**Status:** draft
**Date:** 2026-05-13
**Milestone:** M0102-0002
**Upstream reference:** `postgres/src/backend/replication/walsender.c:1984` (`exec_replication_command`), `postgres/src/backend/backup/basebackup.c:990` (`SendBaseBackup`), `postgres/src/bin/pg_basebackup/pg_basebackup.c:2356` (client `main`).

## Problem

`pg_basebackup -h <goopg> -D <out>` currently fails because goopg's replication
command dispatcher does not handle `BASE_BACKUP`. The existing
`internal/testutil/replcluster/replcluster.go` works around this with an
**offline file copy** of the primary's data directory — not a viable path for
the M0102 heterogeneous E2E tests, since `pg_basebackup` is the bridge that
clones a goopg primary to a PostgreSQL standby (Scenario B) and the symmetric
operation against a PG primary in Scenario A uses upstream `pg_basebackup`
which expects a real BASE_BACKUP-speaking server only when it is the source.

## Upstream contract

From `postgres/src/backend/replication/walsender.c:1984` `exec_replication_command`,
the BASE_BACKUP command flow is:

1. Client sends `BASE_BACKUP [LABEL <label>] [PROGRESS] [CHECKPOINT 'fast'|'spread'] [WAIT 0|1] [MAX_RATE <kib>] [TABLESPACE_MAP] [VERIFY_CHECKSUMS 0|1] [MANIFEST <opt>] [TARGET <opt>] [<more PG18 options>]`.
2. Server executes a `pg_backup_start`-equivalent: takes a fresh checkpoint
   (`do_pg_backup_start` in `postgres/src/backend/backup/basebackup.c`), notes
   the start-LSN and start-TLI.
3. Server sends a first result-set (header) with the start position.
4. Server then streams a sequence of CopyData chunks: tablespace listings
   followed by per-tablespace tar streams of every data file and (optionally)
   pg_wal segments needed for consistency (`-X stream` opens a parallel
   replication stream; `-X fetch` includes them in the tar).
5. After the tar streams complete, server runs `do_pg_backup_stop`, notes
   end-LSN/end-TLI, sends a final result-set with the stop position.
6. Server returns to ready-for-query state.

The wire form is a sequence of `CopyOutResponse` → many `CopyData` →
`CopyDone` per major step, framed by `RowDescription`/`DataRow` pairs for the
start/stop result-sets. See upstream `basebackup_copy.c` for the tar emission
and `basebackup_progress.c` for progress messages.

## Solution

### Server-side dispatcher

In `internal/server/replication.go`, add a `BASE_BACKUP` arm to the existing
replication-command switch (currently handling IDENTIFY_SYSTEM,
CREATE_REPLICATION_SLOT, DROP_REPLICATION_SLOT, START_REPLICATION).

```go
case "BASE_BACKUP":
    return s.handleBaseBackup(ctx, args)
```

`handleBaseBackup` runs the upstream flow above against goopg's storage
layout:

- `internal/storage/`: walk `base/`, `global/` and assemble a deterministic
  file list. (Tablespaces beyond the default are out of scope; if the client
  requests an unknown one, return a tar stream containing only the default.)
- `internal/wal/`: call the existing checkpoint API
  (`internal/wal/checkpoint.go` — checkpoint trigger used by control-socket
  CHECKPOINT command) to acquire the start LSN. Use the new
  `internal/wal/timeline_history.go` (M0102-0003) to surface the current TLI.
- `pg_wal/` inclusion: when the client requests `-X stream` (the M0102 tests'
  default), skip pg_wal tar entries and rely on the client's parallel
  walreceiver. When the client requests `-X fetch`, include the WAL segment
  files that cover [start_lsn, stop_lsn].

### Backup label + manifest

PG writes `backup_label` and `backup_label.old` into the tar stream as
synthetic files; goopg follows the same convention. The `backup_label`
content is the upstream format (`postgres/src/backend/backup/basebackup.c`'s
`labelfile_create`). Backup manifest is optional: when the client passes
`MANIFEST yes`, emit a JSON manifest of file paths + sizes + checksums per
the upstream `parse_manifest.h` schema; when `MANIFEST no` (default for the
M0102 tests), skip.

### Tar stream details

- Format: POSIX ustar (PG uses this; libtar-compatible).
- `pax_global_header` and `pax_header` not required for M0102 (PG omits
  them for base backups).
- Directory entries needed for empty dirs (e.g., `pg_logical/`, `pg_replslot/`).
- File modes preserved as 0o600 (data) / 0o700 (dirs); ownership uid/gid 0.

### Failure modes

- **No replication-mode connection**: BASE_BACKUP is replication-only; reject
  if the session was not opened with `replication=true` in the startup packet.
- **Concurrent BASE_BACKUP**: PG allows multiple concurrent backups (via
  exclusive-vs-non-exclusive semantics deprecated in PG15+). For M0102, take
  a simple primary-side mutex; subsequent BASE_BACKUP requests wait.
- **Checkpoint failure**: surface as `ERROR` then return to ready-for-query.

## Files to create / modify

| File | Change |
|---|---|
| `internal/server/replication.go` | Add `case "BASE_BACKUP":` in dispatcher; new `handleBaseBackup` method |
| `internal/server/basebackup.go` (new) | Tar emission, file walking, backup_label, manifest |
| `internal/wal/checkpoint.go` | Expose `BeginBackup` / `EndBackup` (start/stop-LSN helpers) if not already public |
| `internal/initdb/open.go` | Confirm the data directory layout matches what BASE_BACKUP advertises |

## Verification

```bash
# Start a fresh goopg primary
./bin/goopg start -D /tmp/goopg_src ...

# Run upstream pg_basebackup against goopg
./postgres/local_install/bin/pg_basebackup \
    -h 127.0.0.1 -p <goopg-port> -U <user> \
    -D /tmp/goopg_clone -X stream -P -v

# Result must be: pg_basebackup exits 0, /tmp/goopg_clone is a valid data dir
# that can be started as a goopg standby (with standby.signal + primary_conninfo)
```

Unit test in `internal/server/basebackup_test.go`: drive BASE_BACKUP via the
in-process libpq client; assert tar stream framing matches PG's format
(parsable by `archive/tar`).

## Risks

- **Tar format edge cases.** PG's exact byte layout matters; pg_basebackup
  parses it strictly. Mitigation: use `archive/tar` stdlib + a golden-file
  test comparing a small goopg backup against a PG-produced backup of an
  equivalent dataset.
- **Checkpoint coupling.** M0102-0002 (BASE_BACKUP) depends on a working
  checkpoint primitive that returns a consistent start-LSN. M0089 closed
  checkpoint durability; verify the LSN surface is stable.
- **Concurrent writes during backup.** Upstream's "consistent backup"
  semantics rely on WAL between start-LSN and stop-LSN being applied during
  recovery. Verify that goopg's pg_wal contains all such records and that
  the standby's recovery loop reads them (the WAL format must be M0101-PG-
  compatible for a PG standby to read them in Scenario B).
