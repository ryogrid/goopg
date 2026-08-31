# Module: `internal/backup`

Physical backup — the `pg_basebackup`-compatible streaming backup handler.
Serves the `BASE_BACKUP` protocol command (the same one `pg_basebackup` issues)
by streaming the data directory as a **PG-compatible tar archive** (with
manifest), WAL segments, and tablespace handling — a real PG 18.3 standby can
restore the resulting tar.

## Key Files

- `basebackup.go` — the entire backup engine:
  - `Handler` / `ReplyBaseBackup` — the wire-protocol handler for
    `BASE_BACKUP` / `START_REPLICATION`.
  - `emitBaseBackupTar` — streams the data directory as tar (with
    `pg_control`-last ordering and a streaming manifest).
  - `writeTarFile` / `writeTarDir` / `writeTarFileWithMode` — tar entry
    emission (regular files, dirs, mode preservation, 0600 for pg_control/WAL).
  - `buildBackupLabel` / `buildBackupManifest` — the `backup_label` and
    `backup_manifest` (checksums: none/CRC32C/SHA224/SHA256/SHA384/SHA512).
  - `collectInPlaceTablespaces` / `emitTablespaceTar` — tablespace handling.
  - `appendWALSegments` — appends the WAL segments needed to bring the backup
    to consistency.
  - `writeRecPtrResult` — the backup `(backup_label LSN, timeline)` result line.

## Public API

```go
type Handler struct{ ... }
func NewHandler(cfg Config) *Handler
func (h *Handler) ReplyBaseBackup(conn *FrameWriter, req Request) error
func (h *Handler) Write(p []byte) (int, error)     // stream a block of bytes

type Config struct{ ... }    // server, storage, WAL access, checksum kind
```

## Internal structure

- **Backup flow** — a `BASE_BACKUP` request is answered with: the backup
  LSN/checkpoint, the tablespace list, then the data-directory tar stream
  (main tablespace + per-tablespace tars), then `appendWALSegments` to bring
  the copy up to a consistent LSN, then a trailing progress/status frame.
- **pg_control handling** — `BackupControlImage` (from `internal/initdb`)
  produces a modified control-image (patched `minRecoveryPoint`/TLI) without
  mutating the live file; the tar ships that image at its pg_control-last step,
  and the manifest records the shipped bytes.
- **Checksums** — the manifest computes per-file checksums; the CRC32C
  implementation uses the Castagnoli polynomial table (matching PG's
  `crc32c` / `pg_checksum`).
- **Exclusions** — `isExcludedFile` skips `postmaster.pid`/`.opts`,
  `pg_internal.init`, and the WAL segment directory (those are streamed
  separately); `excludeDirContents` skips `pg_wal/archive_status`.

## Dependencies

- **Used by** — `internal/replication` (walsender serves BASE_BACKUP),
  `internal/postmaster`, `internal/testport` (backup TAP).
- **Uses** — `internal/storage` (page read), `internal/access/transam/xlog`
  (LSN/WAL segment access), `internal/initdb` (control-image),
  `internal/utils/misc` (GUCs).

## Notable patterns / gotchas

- **PG-compatible tar** — the tar entries (headers, ordering, modes) must match
  what `pg_basebackup`/PG's restore expects; `pg_control` is written last so a
  real PG can start the restored directory.
- **Do not mutate the live control file** — the backup must ship a *patched
  copy* of `pg_control`; writing the patched image over the live file is a
  data-loss bug (S29/M0131).
- **Streaming** — the tar is streamed incrementally; `Write`/`ReplyBaseBackup`
  emit chunks bounded by `baseBackupChunkBytes` to avoid buffering the whole
  directory in memory.
- **Manifest checksums** — the manifest's per-file checksums must be computed
  over the *shipped* bytes (the same buffer fed to `record()`), so `pg_verifybackup`
  agrees with the streamed tar.