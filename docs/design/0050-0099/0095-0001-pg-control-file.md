# 0095-0001 — pg_control File at initdb Time

**Status:** accepted
**Date:** 2026-05-14
**Milestone:** M0095-0001

## Problem

Several PostgreSQL client tools — `pg_controldata`, `pg_checksums`,
`pg_basebackup` (offline path), `pg_rewind`, `pg_resetwal` — open
`<datadir>/global/pg_control` before doing any real work. Goopg's
initdb did not write that file, so all of those tools failed against
a fresh goopg data directory with "could not open file ... No such
file or directory".

In particular `TestPort_PgControldata001` (M0095-0001) had to assert
the failure path instead of the upstream output check (`qr/checkpoint/`),
which left CD-001 partially ported. Closing this also unblocks the
deferred sub-cases of C-002 (pg_checksums option validation against a
real cluster).

## Solution

Write a PG18-format `pg_control` file at `<datadir>/global/pg_control`
during `initdb.Init`, immediately after the cluster system identifier
is persisted. The file is 8192 bytes (PG_CONTROL_FILE_SIZE), with a
296-byte ControlFileData struct at the start and zeros elsewhere.
Mirrors upstream's `WriteControlFile` in
`postgres/src/backend/access/transam/xlog.c`.

### Field layout

Match `postgres/src/include/catalog/pg_control.h` ControlFileData
byte-for-byte (PG18, x86_64 layout):

| Offset | Size | Field | Value |
|---|---|---|---|
| 0 | 8 | system_identifier | LoadOrCreateSystemID |
| 8 | 4 | pg_control_version | 1800 |
| 12 | 4 | catalog_version_no | 202506291 |
| 16 | 4 | state (DBState) | 1 (DB_SHUTDOWNED) |
| 24 | 8 | time (pg_time_t) | unix timestamp |
| 32 | 8 | checkPoint (XLogRecPtr) | 0 |
| 40 | 88 | checkPointCopy (CheckPoint) | zero |
| 128 | 8 | unloggedLSN | 0 |
| 136 | 8 | minRecoveryPoint | 0 |
| 144 | 4 | minRecoveryPointTLI | 0 |
| 152 | 8 | backupStartPoint | 0 |
| 160 | 8 | backupEndPoint | 0 |
| 168 | 1 | backupEndRequired | false |
| 172 | 4 | wal_level | 1 (replica) |
| 176 | 1 | wal_log_hints | false |
| 180 | 4 | MaxConnections | 100 |
| 184 | 4 | max_worker_processes | 8 |
| 188 | 4 | max_wal_senders | 10 |
| 192 | 4 | max_prepared_xacts | 0 |
| 196 | 4 | max_locks_per_xact | 64 |
| 200 | 1 | track_commit_timestamp | false |
| 204 | 4 | maxAlign | 8 |
| 208 | 8 | floatFormat (double) | 1234567.0 |
| 216 | 4 | blcksz | 8192 |
| 220 | 4 | relseg_size | 131072 |
| 224 | 4 | xlog_blcksz | 8192 |
| 228 | 4 | xlog_seg_size | 16777216 |
| 232 | 4 | nameDataLen | 64 |
| 236 | 4 | indexMaxKeys | 32 |
| 240 | 4 | toast_max_chunk_size | 1996 |
| 244 | 4 | loblksize | 2048 |
| 248 | 1 | float8ByVal | true |
| 252 | 4 | data_checksum_version | 0 |
| 256 | 1 | default_char_signedness | true |
| 257 | 32 | mock_authentication_nonce | crypto/rand |
| 292 | 4 | crc (pg_crc32c) | CRC32C of [0,292) |

Padding bytes (offsets 20–23, 148–151, 169–171, 177–179, 201–203, 249–251,
289–291) come from C struct alignment rules; goopg writes them as zero.

CRC32C uses the Castagnoli polynomial (0x1EDC6F41), matching upstream's
`pg_crc32c`; computed via Go's `hash/crc32` with `crc32.Castagnoli`.

### Implementation

`internal/initdb/pgcontrol.go`:
- `buildPgControl(systemID, now)` builds the 8192-byte file image.
- `writePgControl(dataDir, systemID)` writes it to
  `<dataDir>/global/pg_control` with mode 0o600.

`internal/initdb/initdb.go::Init` calls `writePgControl(abs, sysID)`
right after `LoadOrCreateSystemID(abs)` so the file appears in every
freshly-init'd directory.

### Endianness

PostgreSQL writes pg_control in *native* byte order. Goopg writes
little-endian, which matches every supported platform (x86_64 / ARM64
Linux, the targets in `.ralph/specs/GOAL_AND_REQUIREMENTS.md`).
`pg_controldata` cross-checks via the `pg_control_version` byte-order
test; a wrong-endian file would print the "possible byte ordering
mismatch" warning.

## Verification

`./postgres/local_install/bin/pg_controldata /tmp/pgcontrol-test` against
a freshly init'd goopg cluster prints the full upstream output with
no version, CRC, or alignment warnings:

```
pg_control version number:            1800
Catalog version number:               202506291
Database system identifier:           13608597193876605698
Database cluster state:               shut down
...
Mock authentication nonce:            e46032aa35b07153cb12fa4fadb07566...
```

`TestPort_PgControldata001` now asserts exit-code 0 + `checkpoint` in
stdout (the upstream-shape check), replacing the previous v0-failure
assertion. C-002 and CD-001 are unchanged in CSV status (already `port`)
but the sub-case coverage now matches the upstream TAP test's positive
path.

## Scope limits / follow-ups

This sub-milestone only writes `pg_control` at initdb time. It does
**not**:

- update pg_control at checkpoint, restart, or backup boundaries
  (`UpdateControlFile`),
- track live `state` transitions (DB_IN_PRODUCTION ↔ DB_SHUTDOWNED
  ↔ DB_SHUTDOWNING),
- populate `checkPointCopy` with real CheckPoint values,
- support `pg_checksums --enable / --disable`, which still needs
  page-level checksum support over every relfile and is left to a
  later milestone.

`pg_controldata` still reports zeros for every checkpoint and
recovery-state field; that is faithful to the "freshly init'd, no
activity yet" state. Once the WAL/checkpointer paths write pg_control
on each shutdown checkpoint the field values will become live.

The CRC-corruption sub-case in upstream's `001_pg_controldata.pl`
remains deferred: pg_controldata only emits a *warning* and still
exits 0 on a bad CRC, so any test that overwrites pg_control with
zeros would still pass the positive output check (no useful signal).
Promote that sub-case once goopg's pg_control reflects live state and
a corruption check can compare expected vs. observed field values.
