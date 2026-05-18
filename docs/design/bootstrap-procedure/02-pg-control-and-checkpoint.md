# 02 — global/pg_control and the Initial Checkpoint

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility); supersedes the
ad-hoc `docs/design/0095-0001-pg-control-file.md`.

---

## Scope

This file specifies the byte layout, initdb-time values, CRC32C
trailer, and runtime mutation rules for `global/pg_control` — the
cluster `ControlFileData` struct that every PG18 backend, and every
PG client tool (`pg_controldata`, `pg_checksums`, `pg_basebackup`,
`pg_resetwal`), reads to learn `system_identifier`, format versions,
sizing constants, the last shutdown checkpoint, and the GUC values
the standby must match.

In scope:

- The full `ControlFileData` struct (`src/include/catalog/pg_control.h:104`),
  field by field, including offsets, initdb-time values, and which
  fields participate in `ReadControlFile`'s FATAL validation chain.
- The embedded `CheckPoint` struct (`pg_control.h:35`) — i.e.
  `checkPointCopy` — and its initdb-time contents from
  `BootStrapXLOG` (`src/backend/access/transam/xlog.c:5071`).
- The CRC32C computation and the 8192-byte zero-pad rule
  (`xlog.c::WriteControlFile`, `common/controldata_utils.c::update_controlfile`).
- The set of call sites that re-write pg_control at runtime, plus the
  reading-side asserts a standby applies on attach.

Out of scope (covered elsewhere in this doc set):

- First WAL segment headers and the
  `XLOG_CHECKPOINT_SHUTDOWN` record body bytes on disk — see
  [`03-wal-bootstrap-segment.md`](03-wal-bootstrap-segment.md).
- `CheckRequiredParameterValues` GUC-echo policy on the standby side
  — referenced in `09-streaming-replication-readiness.md`.
- `BootStrapCLOG`, `BootStrapCommitTs`, `BootStrapSUBTRANS`,
  `BootStrapMultiXact` — these run after `WriteControlFile`; see
  `01-data-directory-layout.md`.

---

## Upstream references

| Symbol | File:line |
|---|---|
| `ControlFileData` struct | `src/include/catalog/pg_control.h:104` |
| `CheckPoint` struct | `src/include/catalog/pg_control.h:35` |
| `DBState` enum | `src/include/catalog/pg_control.h:89` |
| `PG_CONTROL_VERSION` | `src/include/catalog/pg_control.h:25` |
| `PG_CONTROL_FILE_SIZE` (8192) | `src/include/catalog/pg_control.h:256` |
| `PG_CONTROL_MAX_SAFE_SIZE` (512) | `src/include/catalog/pg_control.h:247` |
| `MOCK_AUTH_NONCE_LEN` (32) | `src/include/catalog/pg_control.h:28` |
| `FLOATFORMAT_VALUE` (1234567.0) | `src/include/catalog/pg_control.h:201` |
| `CATALOG_VERSION_NO` (202506291) | `src/include/catalog/catversion.h:60` |
| `FirstNormalTransactionId` (3) | `src/include/access/transam.h:34` |
| `FirstGenbkiObjectId` (10000) | `src/include/access/transam.h:195` |
| `FirstMultiXactId` (1) | `src/include/access/multixact.h:25` |
| `FirstNormalUnloggedLSN` (1000) | `src/include/access/xlogdefs.h:37` |
| `Template1DbOid` (1) | `src/include/catalog/pg_database_d.h:58` |
| `BootstrapTimeLineID` (1) | `src/backend/access/transam/xlog.c:112` |
| `SizeOfXLogLongPHD` | `src/include/access/xlog_internal.h:69` |
| `InitControlFile` | `src/backend/access/transam/xlog.c:4200` |
| `WriteControlFile` | `src/backend/access/transam/xlog.c:4235` |
| `ReadControlFile` | `src/backend/access/transam/xlog.c:4344` |
| `UpdateControlFile` | `src/backend/access/transam/xlog.c:4582` |
| `update_controlfile` | `src/common/controldata_utils.c:189` |
| `BootStrapXLOG` | `src/backend/access/transam/xlog.c:5071` |
| `CheckRequiredParameterValues` | `src/backend/access/transam/xlog.c:5423` |

---

## Initdb-time output

`BootStrapXLOG` (`xlog.c:5071`) produces a single 8192-byte file at
`global/pg_control` containing a zero-initialised `ControlFileData`
struct of 296 bytes followed by 7896 bytes of zero padding. The
fixed-position layout on x86_64 is set by the C compiler's natural
alignment of the struct in `pg_control.h:104`; goopg must reproduce
those exact offsets.

`BootStrapXLOG` populates the struct in three passes:

1. `InitControlFile(sysidentifier, data_checksum_version)`
   (`xlog.c:4200`) zeros the struct and sets `system_identifier`,
   `mock_authentication_nonce`, `state = DB_SHUTDOWNED`,
   `unloggedLSN = FirstNormalUnloggedLSN`, the seven GUC echoes, and
   `data_checksum_version`.
2. `BootStrapXLOG` then overwrites `time`, `checkPoint`, and
   `checkPointCopy` (`xlog.c:5214-5216`) with the values from the
   freshly assembled `CheckPoint` record.
3. `WriteControlFile()` (`xlog.c:4235`) fills the compatibility
   constants (`pg_control_version`, `catalog_version_no`, `maxAlign`,
   `floatFormat`, `blcksz`, `relseg_size`, `xlog_blcksz`,
   `xlog_seg_size`, `nameDataLen`, `indexMaxKeys`,
   `toast_max_chunk_size`, `loblksize`, `float8ByVal`,
   `default_char_signedness`), recomputes the CRC32C, and `pwrite`s
   the 8192-byte buffer.

### `ControlFileData` field table

Offsets are for x86_64 Linux (`MAXIMUM_ALIGNOF = 8`, `FLOAT8PASSBYVAL = true`).
"Validated on read?" lists the FATAL site in `ReadControlFile` that
trips when the field disagrees with the running backend's compile-time
constants.

| Field | C type | Offset | Initdb value | Validated on read? | Citation |
|---|---|---|---|---|---|
| `system_identifier` | `uint64` | 0 | gettimeofday(): `sec<<32 | usec<<12 | pid&0xFFF` | (CRC indirect) | `xlog.c:5098-5101` |
| `pg_control_version` | `uint32` | 8 | `PG_CONTROL_VERSION = 1800` | FATAL at `xlog.c:4388, 4398` | `xlog.c:4243` |
| `catalog_version_no` | `uint32` | 12 | `CATALOG_VERSION_NO = 202506291` | FATAL at `xlog.c:4424` | `xlog.c:4244`, `catversion.h:60` |
| `state` | `DBState (uint32)` | 16 | `DB_SHUTDOWNED = 1` | — | `xlog.c:4219` |
| `time` | `pg_time_t (int64)` | 24 | `(pg_time_t) time(NULL)` (== `checkPoint.time`) | — | `xlog.c:5131, 5214` |
| `checkPoint` | `XLogRecPtr (uint64)` | 32 | `wal_segment_size + SizeOfXLogLongPHD` = `0x01000028` (16 MiB + 40 B) | — | `xlog.c:5115, 5215` |
| `checkPointCopy` | `CheckPoint` (88 B) | 40 | — see substructure below — | — | `xlog.c:5115-5132, 5216` |
| `unloggedLSN` | `XLogRecPtr` | 128 | `FirstNormalUnloggedLSN = 1000` | — | `xlog.c:4220` |
| `minRecoveryPoint` | `XLogRecPtr` | 136 | `0` (`InvalidXLogRecPtr`) | — | `xlog.c:4215` (zeroed) |
| `minRecoveryPointTLI` | `TimeLineID (uint32)` | 144 | `0` | — | `xlog.c:4215` (zeroed) |
| `backupStartPoint` | `XLogRecPtr` | 152 | `0` | — | `xlog.c:4215` (zeroed) |
| `backupEndPoint` | `XLogRecPtr` | 160 | `0` | — | `xlog.c:4215` (zeroed) |
| `backupEndRequired` | `bool` | 168 | `false` | — | `xlog.c:4215` (zeroed) |
| `wal_level` | `int` | 172 | GUC `wal_level` (replica=1, logical=2) | — | `xlog.c:4228` |
| `wal_log_hints` | `bool` | 176 | GUC `wal_log_hints` | — | `xlog.c:4229` |
| `MaxConnections` | `int` | 180 | GUC `max_connections` (default 100) | recovery requires ≥ (`xlog.c:5445`) | `xlog.c:4223` |
| `max_worker_processes` | `int` | 184 | GUC (default 8) | recovery requires ≥ (`xlog.c:5448`) | `xlog.c:4224` |
| `max_wal_senders` | `int` | 188 | GUC (default 10) | recovery requires ≥ (`xlog.c:5451`) | `xlog.c:4225` |
| `max_prepared_xacts` | `int` | 192 | GUC (default 0) | recovery requires ≥ (`xlog.c:5454`) | `xlog.c:4226` |
| `max_locks_per_xact` | `int` | 196 | GUC (default 64) | recovery requires ≥ (`xlog.c:5457`) | `xlog.c:4227` |
| `track_commit_timestamp` | `bool` | 200 | GUC (default off) | — | `xlog.c:4230` |
| `maxAlign` | `uint32` | 204 | `MAXIMUM_ALIGNOF = 8` | FATAL at `xlog.c:4434` | `xlog.c:4246` |
| `floatFormat` | `double` | 208 | `FLOATFORMAT_VALUE = 1234567.0` | FATAL at `xlog.c:4444` | `xlog.c:4247` |
| `blcksz` | `uint32` | 216 | `BLCKSZ = 8192` | FATAL at `xlog.c:4450` | `xlog.c:4249` |
| `relseg_size` | `uint32` | 220 | `RELSEG_SIZE = 131072` (1 GiB / 8 KiB) | FATAL at `xlog.c:4460` | `xlog.c:4250` |
| `xlog_blcksz` | `uint32` | 224 | `XLOG_BLCKSZ = 8192` | FATAL at `xlog.c:4470` | `xlog.c:4251` |
| `xlog_seg_size` | `uint32` | 228 | `wal_segment_size = 16 MiB` | range checked (`xlog.c:4541`) | `xlog.c:4252` |
| `nameDataLen` | `uint32` | 232 | `NAMEDATALEN = 64` | FATAL at `xlog.c:4480` | `xlog.c:4254` |
| `indexMaxKeys` | `uint32` | 236 | `INDEX_MAX_KEYS = 32` | FATAL at `xlog.c:4490` | `xlog.c:4255` |
| `toast_max_chunk_size` | `uint32` | 240 | `TOAST_MAX_CHUNK_SIZE = 1996` | FATAL at `xlog.c:4500` | `xlog.c:4257` |
| `loblksize` | `uint32` | 244 | `LOBLKSIZE = 2048` | FATAL at `xlog.c:4510` | `xlog.c:4258` |
| `float8ByVal` | `bool` | 248 | `FLOAT8PASSBYVAL = true` on 64-bit | FATAL at `xlog.c:4522, 4530` | `xlog.c:4260` |
| `data_checksum_version` | `uint32` | 252 | initdb `--data-checksums` arg (default 0) | exposed as GUC (`xlog.c:4573`) | `xlog.c:4231` |
| `default_char_signedness` | `bool` | 256 | `true` (PG18: unconditionally true on new clusters) | — | `xlog.c:4287` |
| `mock_authentication_nonce` | `char[32]` | 257 | 32 random bytes from `pg_strong_random()` | — | `xlog.c:4202-4218` |
| `crc` | `pg_crc32c (uint32)` | 292 | CRC32C(file[0:292]) | FATAL at `xlog.c:4414` | `xlog.c:4290-4294` |

Total active payload: 296 bytes; bytes 296–8191 are zero pad. The
struct is statically asserted to fit in `PG_CONTROL_MAX_SAFE_SIZE = 512`
so a single sector write is atomic (`pg_control.h:261`).

### `CheckPoint` field table (the `checkPointCopy` substructure)

The 88-byte body of the initial `XLOG_CHECKPOINT_SHUTDOWN` record is
inlined into pg_control at offset 40. Field offsets are relative to
the start of `checkPointCopy`.

| Field | C type | Offset | Initdb value | Citation |
|---|---|---|---|---|
| `redo` | `XLogRecPtr (uint64)` | 0 | `wal_segment_size + SizeOfXLogLongPHD` = `0x01000028` | `xlog.c:5115` |
| `ThisTimeLineID` | `TimeLineID (uint32)` | 8 | `BootstrapTimeLineID = 1` | `xlog.c:5116` |
| `PrevTimeLineID` | `TimeLineID (uint32)` | 12 | `BootstrapTimeLineID = 1` | `xlog.c:5117` |
| `fullPageWrites` | `bool` | 16 | GUC `full_page_writes` (default on) | `xlog.c:5118` |
| `wal_level` | `int` | 20 | GUC `wal_level` | `xlog.c:5119` |
| `nextXid` | `FullTransactionId (uint64)` | 24 | `FullTransactionIdFromEpochAndXid(0, FirstNormalTransactionId)` = `0x0000000000000003` | `xlog.c:5120` |
| `nextOid` | `Oid (uint32)` | 32 | `FirstGenbkiObjectId = 10000` | `xlog.c:5122` |
| `nextMulti` | `MultiXactId (uint32)` | 36 | `FirstMultiXactId = 1` | `xlog.c:5123` |
| `nextMultiOffset` | `MultiXactOffset (uint32)` | 40 | `0` | `xlog.c:5124` |
| `oldestXid` | `TransactionId (uint32)` | 44 | `FirstNormalTransactionId = 3` | `xlog.c:5125` |
| `oldestXidDB` | `Oid (uint32)` | 48 | `Template1DbOid = 1` | `xlog.c:5126` |
| `oldestMulti` | `MultiXactId (uint32)` | 52 | `FirstMultiXactId = 1` | `xlog.c:5127` |
| `oldestMultiDB` | `Oid (uint32)` | 56 | `Template1DbOid = 1` | `xlog.c:5128` |
| `time` | `pg_time_t (int64)` | 64 | `(pg_time_t) time(NULL)` | `xlog.c:5131` |
| `oldestCommitTsXid` | `TransactionId (uint32)` | 72 | `InvalidTransactionId = 0` | `xlog.c:5129` |
| `newestCommitTsXid` | `TransactionId (uint32)` | 76 | `InvalidTransactionId = 0` | `xlog.c:5130` |
| `oldestActiveXid` | `TransactionId (uint32)` | 80 | `InvalidTransactionId = 0` | `xlog.c:5132` |

Note the 4-byte alignment pad between `oldestMultiDB` (offset 56,
4 bytes) and `time` (offset 64, 8-byte aligned) — this padding is
part of the on-disk representation and must be zeroed by goopg.

### CRC32C and the 8192-byte pad rule

`WriteControlFile` (`xlog.c:4289-4294`) computes the CRC over exactly
`offsetof(ControlFileData, crc) = 292` bytes using the CRC32C
(Castagnoli) polynomial — `INIT_CRC32C`, `COMP_CRC32C`,
`FIN_CRC32C` resolve to the SSE4.2 instruction on x86_64 and to a
table-driven fallback otherwise. The resulting `pg_crc32c` is written
as a little-endian `uint32` at offset 292.

`WriteControlFile` (`xlog.c:4302-4304`) then `memcpy`s the
`ControlFileData` struct into a 8192-byte buffer pre-zeroed via
`memset`, opens `global/pg_control` with
`BasicOpenFile(O_RDWR | O_CREAT | O_EXCL | PG_BINARY)`, `write`s the
full 8192 bytes, `pg_fsync`s, and `close`s. All errors `ereport(PANIC)`.

The "8192 bytes total, 296 bytes active, CRC at offset 292" invariant
is what makes `pg_controldata` reject a wrong-version file with a
helpful message rather than EOF (`pg_control.h:249-254`).

---

## Continuous maintenance

`pg_control` is rewritten in-place every time a `ControlFile->*` field
changes. The writer is `update_controlfile` in
`src/common/controldata_utils.c:189`, invoked through the static
`UpdateControlFile()` wrapper in `xlog.c:4582` which sets `do_sync =
true`. `update_controlfile` re-derives `ControlFile->time` from
`time(NULL)` (`controldata_utils.c:197`), recomputes the CRC, zeros a
fresh 8192-byte buffer, `memcpy`s the struct, opens the existing file
with `BasicOpenFile(O_RDWR | PG_BINARY)`, `write`s, optionally
`pg_fsync`s, and closes. Every error path is `PANIC`.

### Atomicity contract

Because the active payload (296 B) is well below the
`PG_CONTROL_MAX_SAFE_SIZE = 512` single-sector limit, the kernel write
is expected to be torn-write-safe on commodity hardware. `pg_control`
updates are **not** WAL-logged; the on-disk file is the authoritative
record of cluster state. The corresponding WAL counterparts —
`XLOG_PARAMETER_CHANGE` (`xlog.c:8183`), `XLOG_CHECKPOINT_SHUTDOWN` /
`XLOG_CHECKPOINT_ONLINE` (`pg_control.h:68-69`),
`XLOG_BACKUP_END` (`pg_control.h:73`), `XLOG_END_OF_RECOVERY`
(`pg_control.h:77`) — exist so that a *standby* applying the WAL can
re-derive the same ControlFile state into its *own*
`global/pg_control`; they do not relieve the primary of writing
pg_control directly.

### `UpdateControlFile` call-site matrix

| Caller | Trigger | Fields touched | WAL record emitted |
|---|---|---|---|
| `BootStrapXLOG → WriteControlFile` `xlog.c:5219` | initdb (once) | all | `XLOG_CHECKPOINT_SHUTDOWN` (written separately to first WAL segment) |
| `UpdateMinRecoveryPoint` `xlog.c:2760` | flush during archive recovery | `minRecoveryPoint`, `minRecoveryPointTLI` | none |
| `StartupXLOG` `xlog.c:5749` | startup: imprint chosen checkpoint into `state` etc. | `state`, `checkPoint`, `checkPointCopy`, `minRecoveryPoint`, … | none |
| `StartupXLOG` (end of recovery) `xlog.c:6211` | promotion / end of recovery | `state = DB_IN_PRODUCTION` | none (state-only) |
| `SwitchIntoArchiveRecovery` `xlog.c:6267` | crash→archive recovery transition | `state = DB_IN_ARCHIVE_RECOVERY`, `minRecoveryPoint`, `minRecoveryPointTLI` | none |
| `ReachedEndOfBackup` `xlog.c:6306` | replay reaches `XLOG_BACKUP_END` | `backupStartPoint = 0`, `backupEndPoint = 0`, `backupEndRequired = false`, `minRecoveryPoint`, `minRecoveryPointTLI` | none (it processes the WAL record) |
| `CreateCheckPoint` (shutdown handshake) `xlog.c:6981` | enter `CHECKPOINT IS_SHUTDOWN` | `state = DB_SHUTDOWNING` | none |
| `CreateCheckPoint` (post-flush) `xlog.c:7306` | every shutdown / online checkpoint | `state`, `checkPoint`, `checkPointCopy`, `minRecoveryPoint = 0`, `minRecoveryPointTLI = 0`, `unloggedLSN` | `XLOG_CHECKPOINT_SHUTDOWN` or `XLOG_CHECKPOINT_ONLINE` (written *before* this call inside the same critical section) |
| `CreateEndOfRecoveryRecord` `xlog.c:7447` | end-of-recovery checkpoint | `minRecoveryPoint`, `minRecoveryPointTLI` | `XLOG_END_OF_RECOVERY` (written just before) |
| `CreateRestartPoint` (skip path) `xlog.c:7691` | shutdown of standby with no new checkpoint | `state = DB_SHUTDOWNED_IN_RECOVERY` | none |
| `CreateRestartPoint` (apply path) `xlog.c:7789` | every restart point on the standby | `checkPoint`, `checkPointCopy`, `state`, `minRecoveryPoint`, `minRecoveryPointTLI` | none (replays primary's checkpoint WAL) |
| `XLogReportParameters` `xlog.c:8197` | postmaster sees GUC change at startup | `MaxConnections`, `max_worker_processes`, `max_wal_senders`, `max_prepared_xacts`, `max_locks_per_xact`, `wal_level`, `wal_log_hints`, `track_commit_timestamp` | `XLOG_PARAMETER_CHANGE` (written just before) |
| `xlog_redo(XLOG_PARAMETER_CHANGE)` `xlog.c:8615` | standby replays primary's GUC change | same 8 fields + possibly `minRecoveryPoint` | none (consumes WAL) |
| `do_pg_backup_start` (sets `backupStartPoint`, `backupEndRequired = true`) | `pg_backup_start()` / `BASE_BACKUP` | `backupStartPoint`, `backupEndRequired` | called via `update_controlfile` directly; see `xlog.c:8842` |
| `do_pg_backup_stop` | `pg_backup_stop()` / `BASE_BACKUP` end | `backupEndPoint` (on standby-side backups), inserts `XLOG_BACKUP_END` | `XLOG_BACKUP_END`; see `xlog.c:9170` |

### Reading-side asserts on standby attach

After every shared-memory state change that materially affects WAL
applicability, `CheckRequiredParameterValues` (`xlog.c:5423`) re-reads
`ControlFile->wal_level` and the five sizing GUCs and FATALs the
standby if any of them is below the primary's recorded value:

- `wal_level = WAL_LEVEL_MINIMAL` and `ArchiveRecoveryRequested`
  → FATAL at `xlog.c:5431`.
- Each of `MaxConnections`, `max_worker_processes`, `max_wal_senders`,
  `max_prepared_xacts`, `max_locks_per_xact` is fed through
  `RecoveryRequiresIntParameter` at `xlog.c:5445-5459` — if the
  standby's compiled-in GUC is **less** than the value pg_control
  carries, the standby FATALs.

This is the mechanism by which the goopg primary's pg_control GUC
echoes must dominate (or at least equal) the standby's. See
[`09-streaming-replication-readiness.md`](09-streaming-replication-readiness.md)
for the GUC-defaults policy.

---

## What goopg must produce

Existing implementation:

- `internal/initdb/pgcontrol.go::writePgControl` (the Go counterpart of
  `WriteControlFile`) is invoked from
  `internal/initdb/initdb.go:442` at the end of `Init`.
- `internal/initdb/pgcontrol.go::buildPgControl` renders the 8192-byte
  buffer.
- `internal/initdb/pgcontrol.go::UpdateControlCheckpoint` is invoked
  from `internal/server/basebackup.go:153` to imprint a fresh redo LSN
  into pg_control immediately before streaming a base backup.

### Initdb-time field status

| Field | Status | Gap |
|---|---|---|
| `system_identifier` | done | Loaded from `global/system_identifier`; matches xlog page header. |
| `pg_control_version` | done | `pgcontrol.go:35` (`1800`). |
| `catalog_version_no` | done | `pgcontrol.go:36` (`202506291`); must track `catversion.h:60` on submodule bump. |
| `state` | done | `DB_SHUTDOWNED = 1`. |
| `time` | done | `time.Now().Unix()`. |
| `checkPoint` | **missing** | Currently `0`; must be `wal_segment_size + SizeOfXLogLongPHD = 0x01000028`. |
| `checkPointCopy.redo` | **missing** | Currently `0`. |
| `checkPointCopy.ThisTimeLineID` / `PrevTimeLineID` | **missing** | Currently `0`; must be `1`. |
| `checkPointCopy.fullPageWrites` | **missing** | Currently `0`. |
| `checkPointCopy.wal_level` | **missing** | Currently `0`. |
| `checkPointCopy.nextXid` | **missing** | Currently `0`; must be `FullTxn(0, 3)`. |
| `checkPointCopy.nextOid` | **missing** | Currently `0`; must be `10000`. |
| `checkPointCopy.nextMulti` / `oldestMulti` | **missing** | Currently `0`; must be `1`. |
| `checkPointCopy.oldestXid` | **missing** | Currently `0`; must be `3`. |
| `checkPointCopy.oldestXidDB` / `oldestMultiDB` | **missing** | Currently `0`; must be `1` (`Template1DbOid`). |
| `checkPointCopy.time` | **missing** | Currently `0`. |
| `unloggedLSN` | **partial** | `pgcontrol.go:169` writes `0`; must be `FirstNormalUnloggedLSN = 1000`. |
| `minRecoveryPoint`, `…TLI` | done | Zero on a freshly-initialised primary. |
| `backupStartPoint`, `backupEndPoint`, `backupEndRequired` | done | Zero / false on initdb. |
| `wal_level` | done | Hard-coded `1` (replica) — adequate while wiring is in progress; must read live GUC. |
| `wal_log_hints` | done | Zero. |
| `MaxConnections` | **partial** | Hard-coded 100 (`pgcontrol.go:188`); must read live GUC for correct standby attach. |
| `max_worker_processes` | **partial** | Hard-coded 8. |
| `max_wal_senders` | **partial** | Hard-coded 10. |
| `max_prepared_xacts` | **partial** | Hard-coded 0. |
| `max_locks_per_xact` | **partial** | Hard-coded 64. |
| `track_commit_timestamp` | done | Zero. |
| `maxAlign`, `floatFormat`, `blcksz`, `relseg_size`, `xlog_blcksz`, `xlog_seg_size`, `nameDataLen`, `indexMaxKeys`, `toast_max_chunk_size`, `loblksize`, `float8ByVal` | done | Match upstream constants. |
| `data_checksum_version` | **partial** | Hard-coded 0; must accept the initdb `--data-checksums` flag once that wiring exists. |
| `default_char_signedness` | done | `pgcontrol.go:226` sets `1` (true). |
| `mock_authentication_nonce` | done | `crypto/rand.Read` 32 B at `pgcontrol.go:228`. |
| `crc` | done | CRC32C/Castagnoli over `hdr[:292]` at `pgcontrol.go:238`. |

The largest single gap is the entire `checkPointCopy` substructure
(40 bytes of zero where PG expects a populated `CheckPoint`). Because
the initial `XLOG_CHECKPOINT_SHUTDOWN` record body that lives in the
first WAL segment must match this struct byte-for-byte
(`xlog.c:5165`), the missing values must be filled coherently with
[`03-wal-bootstrap-segment.md`](03-wal-bootstrap-segment.md).

### Continuous-maintenance gaps

| Trigger | Goopg call site | Status | Action |
|---|---|---|---|
| Shutdown checkpoint | `internal/wal/checkpointer.go::runCheckpoint` (`checkpointer.go:299`) | **missing** | Add `updateControlFile()` after a successful flush+WAL append: set `state = DB_SHUTDOWNED` (or `DB_IN_PRODUCTION` for online), `checkPoint`, `checkPointCopy`, `minRecoveryPoint = 0`, `unloggedLSN`. |
| Online checkpoint | same | **missing** | Same fields with `state = DB_IN_PRODUCTION`. |
| Restart point on a standby | not yet implemented (no walreceiver-driven restart point) | **missing** | New call from the standby checkpoint loop once walreceiver lands. |
| Promotion (`StartupXLOG` end) | not yet implemented | **missing** | New call writing `state = DB_IN_PRODUCTION` and a fresh timeline. |
| `pg_backup_start`/`stop` | `internal/server/basebackup.go::handleBaseBackup` uses `UpdateControlCheckpoint` (`basebackup.go:153`) only | **partial** | `UpdateControlCheckpoint` already rewrites `state`, `time`, `checkPoint`, `checkPointCopy.redo/TLI`, `fullPageWrites`, `minRecoveryPoint`, `backupEndPoint`, CRC; missing: `backupStartPoint`, the `backupEndRequired` flag, and the WAL `XLOG_BACKUP_END` emission. |
| GUC parameter change | not yet implemented | **missing** | Wire an `XLogReportParameters` equivalent that re-writes the 8 GUC-echo fields + emits `XLOG_PARAMETER_CHANGE`. |

### Recommended Go API

The pg_control writer should grow a single helper, `updateControlFile`,
inside `internal/initdb/pgcontrol.go` (or its eventual move to a
dedicated `internal/control/pgcontrol.go` shared with the `control`
package):

```go
// updateControlFile mutates a subset of ControlFileData on disk,
// recomputes the CRC, and re-pwrites the 8192-byte file. Mirrors
// upstream's update_controlfile() in src/common/controldata_utils.c.
func updateControlFile(dataDir string, fn func(*ControlFileData)) error
```

`runCheckpoint`, the basebackup handler, the (future) walreceiver
restart-point loop, the promotion path, and the GUC-change reporter
all funnel through this single helper. The `ControlFileData` Go struct
should be defined explicitly with byte-tagged offsets (or asserted
against the C layout in a test) so a future field addition fails the
build rather than silently writing zero.

---

## Verification

1. **`pg_controldata` cross-diff.**

   ```bash
   PGDATA=$(mktemp -d) initdb -D "$PGDATA"
   GOOPGDATA=$(mktemp -d) goopg init -D "$GOOPGDATA"
   diff <(pg_controldata "$PGDATA"   | grep -v -e 'identifier' -e 'time') \
        <(pg_controldata "$GOOPGDATA" | grep -v -e 'identifier' -e 'time')
   ```

   Excluding `system_identifier` (randomised) and the two `time`
   fields, every line must match. Today the diff is non-empty on
   `Latest checkpoint location`, `Latest checkpoint's REDO location`,
   `Latest checkpoint's TimeLineID`, `Latest checkpoint's NextOID`,
   `Latest checkpoint's NextXID`, `Latest checkpoint's NextMultiXactId`,
   `Latest checkpoint's oldestXID`, `Latest checkpoint's oldestXIDDB`,
   `Fake LSN counter for unlogged rels` — i.e. precisely the
   `checkPointCopy` + `unloggedLSN` gaps listed above.

2. **Byte-level invariant diff.**

   Goopg `internal/initdb/pg_control_test.go` (new file) should assert,
   against a freshly-initdb-ed reference directory bundled in
   `internal/testutil/pgcluster/golden/`, that bytes
   `[0..8) ∪ [16..292)` are byte-equal except for the known
   randomness windows (`mock_authentication_nonce` at 257..289 and
   `system_identifier` at 0..8) and timestamp windows
   (`time` at 24..32 and `checkPointCopy.time` at offset 40+64..40+72).

3. **CRC self-check.** The same test recomputes
   `crc32.Checksum(buf[:292], crcCastagnoliTable)` and compares
   against `binary.LittleEndian.Uint32(buf[292:296])`.

4. **`ReadControlFile` simulation.** A table-driven Go test feeds
   each "FATAL on" trigger field a deliberately-wrong value and
   verifies the file is rejected by `pg_controldata -d` (which prints
   the relevant compatibility line).

5. **E2E coverage.** `TestE2E_FailoverGoopgToPG/async` exercises the
   full surface: a vanilla PG18 backend reads goopg's pg_control via
   `ReadControlFile` (`xlog.c:4344`), and must reach `PM_HOT_STANDBY`
   without tripping a `pg_control_version`, CRC, or
   `catalog_version_no` FATAL, nor `CheckRequiredParameterValues`.
   Today the test fails earlier (relcache init / catalog seed); once
   those are resolved, the pg_control gaps in the table above become
   the next blockers.

6. **`update_controlfile` parity.** Once `updateControlFile` lands in
   goopg, an integration test runs a SQL `CHECKPOINT`, then re-reads
   pg_control with the C client tool `pg_controldata`, and asserts
   that `Latest checkpoint location` and `Latest checkpoint's REDO
   location` advance to the LSN goopg's checkpointer wrote.
