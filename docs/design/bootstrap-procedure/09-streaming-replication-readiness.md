# 09 — Streaming Replication Readiness

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility) — physical
replication slice; supersedes ad-hoc notes in
`docs/design/0005-0001-streaming-replication-architecture.md`,
`docs/design/0005-0002-standby-recovery-and-replay.md`, and
`docs/design/0005-0005-promotion.md` for the per-artefact "what must
be on disk" question.

---

## Scope

This file covers everything **beyond** the static catalog / control-
file state that a vanilla PG18 backend needs in order to (a) attach
as a hot standby against a goopg primary via `pg_basebackup
-X stream -R`, and (b) keep running as a hot standby while goopg
mutates the cluster. The artefact set is:

- Signal files (`standby.signal`, `recovery.signal`).
- Timeline-history files (`pg_wal/<TLI>.history`).
- Replication-slot state files (`pg_replslot/<slot>/state`).
- The GUC echoes in `ControlFile` and the standby-side
  `CheckRequiredParameterValues` enforcement they feed.
- The `pg_basebackup` server-side exclusion list (goopg-as-source
  must never ship these entries — copying them would corrupt the
  resulting standby).
- The timeline-switch sequence on promotion (history-file writeback +
  `ControlFile` state transition + WAL timeline-switch record).

**Out of scope.**

- ControlFile field layout, CRC, offsets — see
  [`02-pg-control-and-checkpoint.md`](02-pg-control-and-checkpoint.md).
- WAL segment layout and record formats — see
  [`03-wal-bootstrap-segment.md`](03-wal-bootstrap-segment.md).
- Logical replication apply (M0103 territory).
- vanilla-primary → goopg-standby direction (M0102 territory, mostly
  in `internal/wal/recovery.go` + `internal/wal/stream_replayer.go`).

---

## Upstream references

| Symbol | File:line |
|---|---|
| `readRecoverySignalFile` | `src/backend/access/transam/xlogrecovery.c:1046` |
| `STANDBY_SIGNAL_FILE` test | `src/backend/access/transam/xlogrecovery.c:1075` |
| `RECOVERY_SIGNAL_FILE` test | `src/backend/access/transam/xlogrecovery.c:1088` |
| `writeTimeLineHistory` | `src/backend/access/transam/timeline.c:304` |
| `writeTimeLineHistoryFile` (raw bytes) | `src/backend/access/transam/timeline.c:463` |
| `TLHistoryFileName` (`%08X.history`) | `src/include/access/xlog_internal.h:178` |
| `XLogInitNewTimeline` + history call site | `src/backend/access/transam/xlog.c:5996-6021` |
| `SaveSlotToPath` (atomic `state.tmp` → `state`) | `src/backend/replication/slot.c:2319` |
| `ReplicationSlotOnDisk` struct | `src/backend/replication/slot.c:65-83` |
| `SLOT_MAGIC = 0x1051CA1` / `SLOT_VERSION = 5` | `src/backend/replication/slot.c:140-141` |
| `ReplicationSlotPersistentData` | `src/include/replication/slot.h:70-137` |
| `RestoreSlotFromDisk` (magic / version / CRC check) | `src/backend/replication/slot.c:2482, 2556-2604` |
| `CheckRequiredParameterValues` | `src/backend/access/transam/xlog.c:5423` |
| `RecoveryRequiresIntParameter` callers | `src/backend/access/transam/xlog.c:5445-5459` |
| `XLogReportParameters` (GUC echo + WAL emission) | `src/backend/access/transam/xlog.c:8147-8199` |
| `xlog_redo(XLOG_PARAMETER_CHANGE)` (standby-side ControlFile update) | `src/backend/access/transam/xlog.c:8581-8587` |
| `excludeDirContents[]` | `src/backend/backup/basebackup.c:151-186` |
| `excludeFiles[]` | `src/backend/backup/basebackup.c:191-225` |
| basebackup exclude scan (files) | `src/backend/backup/basebackup.c:1290-1296` |
| basebackup exclude scan (dirs) | `src/backend/backup/basebackup.c:1369-1371` |
| `CreateCheckPoint` (writes `checkPoint`, `unloggedLSN`) | `src/backend/access/transam/xlog.c:7290-7307` |

---

## Initdb-time output

### Files NOT present after initdb (but required to make the cluster a standby)

| Path | Provenance | When it appears |
|---|---|---|
| `standby.signal` | Operator (or `pg_basebackup -R`). | After basebackup of the standby's `$PGDATA` and just before the standby's first `pg_ctl start`. PG checks for it in `readRecoverySignalFile` (`xlogrecovery.c:1075`). Body is empty; PG only checks `stat(2)` then `pg_fsync`s the open fd. |
| `recovery.signal` | Operator. | One-shot archive recovery only; not used for streaming replication. Mutually exclusive with `standby.signal`; `standby.signal` wins when both present (`xlogrecovery.c:1102-1108`). |
| `pg_wal/<NEW_TLI>.history` | Primary, on every promotion (or on point-in-time recovery). | None at initdb (`BootstrapTimeLineID = 1` has no parent, so no history file is written). |
| `pg_replslot/<slot>/state` | Primary, via SQL function `pg_create_physical_replication_slot` or replication-command `CREATE_REPLICATION_SLOT`. | None at initdb. |

### Files that ARE present at initdb and are load-bearing for standby attach

| Path | Why the standby needs it | Cross-reference |
|---|---|---|
| `global/pg_control` | `walreceiver`'s `IDENTIFY_SYSTEM` compares `system_identifier` with the primary's; `CheckRequiredParameterValues` reads the seven GUC echoes; `StartupXLOG` consumes `checkPointCopy.redo` to seed replay. | [`02-pg-control-and-checkpoint.md`](02-pg-control-and-checkpoint.md) |
| `pg_wal/000000010000000000000001` | Replay must begin at `checkPointCopy.redo = 0/01000028`; the segment file must exist on the standby side of a `pg_basebackup -X stream` so the recovery loop has a starting page. | [`03-wal-bootstrap-segment.md`](03-wal-bootstrap-segment.md) |
| `pg_wal/archive_status/` | `RemoveOldXlogFiles` / `XLogArchiveNotify` peek here; missing directory FATALs in `ValidateXLOGDirectoryStructure` (`xlog.c:5562`). Empty at initdb. | `03-` |
| `pg_replslot/` | `RestoreSlotFromDisk` is called over this directory at startup; missing it FATALs. Empty at initdb. | This file. |
| `pg_wal/summaries/` | `validateRecoveryParameters` PANICs on a missing directory even when `summarize_wal = off`. | `03-` |

### `pg_basebackup` exclusion table

Goopg-as-primary must filter these out of any `BASE_BACKUP` stream
(see `internal/server/basebackup.go`). Upstream's `excludeFiles[]` is
*file-name* match (with `match_prefix` for `pg_internal.init*`),
`excludeDirContents[]` is *directory-name* match — the directory
itself is shipped as an empty dir to preserve mode bits.

| Path | Kind | Excluded? | Why | Upstream citation |
|---|---|---|---|---|
| `pg_stat_tmp/*` | dir contents | yes | Per-process stats files; recreated on standby start. | `basebackup.c:157` |
| `pg_replslot/*` | dir contents | yes | Slot files belong to the primary; copying them would have the standby believe it owns slots it does not. The empty directory is still shipped. | `basebackup.c:164` |
| `pg_dynshmem/*` | dir contents | yes | Cleared on startup (`dsm_cleanup_for_mmap`). | `basebackup.c:167` |
| `pg_notify/*` | dir contents | yes | Cleared on startup (`AsyncShmemInit`). | `basebackup.c:170` |
| `pg_serial/*` | dir contents | yes | Optional, not required for replay. | `basebackup.c:176` |
| `pg_snapshots/*` | dir contents | yes | Cleared on startup (`DeleteAllExportedSnapshotFiles`). | `basebackup.c:179` |
| `pg_subtrans/*` | dir contents | yes | Zeroed on startup (`StartupSUBTRANS`). | `basebackup.c:182` |
| `postgresql.auto.conf.tmp` | file | yes | Atomic-write tempfile from `ALTER SYSTEM`. | `basebackup.c:194` |
| `current_logfiles.tmp` | file | yes | Logger tempfile. | `basebackup.c:197` |
| `pg_internal.init*` | file (prefix) | yes | Relcache init file — standby rebuilds on first relcache access; shipping a stale one would race the rebuild. | `basebackup.c:203` |
| `backup_label` | file | yes | Synthesised by the basebackup itself; a stale one from a previous backup would mis-anchor recovery. | `basebackup.c:209` |
| `tablespace_map` | file | yes | Same: synthesised per backup. | `basebackup.c:210` |
| `backup_manifest` | file | yes | Belongs to the basebackup that produced this primary; not valid for a new backup. | `basebackup.c:218` |
| `postmaster.pid` | file | yes | Postmaster lockfile; would falsely look "running" on the standby. | `basebackup.c:220` |
| `postmaster.opts` | file | yes | Postmaster argv echo. | `basebackup.c:221` |

`pg_logical/` is **not** in `excludeDirContents[]`. Most of its
subtree is intentionally streamed so logical-decoding slots can
survive a basebackup; only `pg_logical/snapshots` is special-cased by
upstream's tar walker. Goopg has no logical-decoding-snapshot
producer yet, so the directory is shipped as-is.

---

## Continuous maintenance

### Timeline-history file write on promotion

Trigger: any TLI bump — operator-driven promotion (`pg_ctl promote` /
`promote.signal`), PITR target reached, or an explicit
`pg_promote()` SQL function.

Upstream sequence (`xlog.c:5996-6021`):

1. Choose `newTLI = findNewestTimeLine(recoveryTargetTLI) + 1`.
2. `XLogInitNewTimeline(EndOfLogTLI, EndOfLog, newTLI)` either
   `XLogFileCopy`s the trailing segment to the new TLI's segno or
   `XLogFileInit`s the next segment on `newTLI`.
3. Remove `standby.signal` / `recovery.signal` from `$PGDATA`.
4. `writeTimeLineHistory(newTLI, recoveryTargetTLI, EndOfLog,
   reason)` writes `pg_wal/<newTLI>.history`
   (`timeline.c:304-444`). The format is the upstream parent file's
   bytes verbatim (or absent if no parent file existed) followed by
   one new line:

   ```
   <parent_tli>\t<high32_hex>/<low32_hex>\t<reason>\n
   ```

   `LSN_FORMAT_ARGS` produces the `X/X` notation (`timeline.c:401-406`).
5. `XLogArchiveNotify(<newTLI>.history)` flips the archiver to scan
   the directory immediately (history files get archive priority).

A standby attaching after a promotion follows the chain by reading
`<newTLI>.history` before it requests segments; a missing or partial
file is FATAL inside `validateRecoveryParameters`.

The goopg promotion path already calls `wal.WriteHistory` from
`cmd/goopg/standby.go:279`; what is missing is the call from a
*primary-initiated* promotion (i.e., a goopg primary that itself was
promoted from a former standby and now needs to advertise its new
TLI to downstream standbys). See "What goopg must produce" below.

### Replication-slot lifecycle

| Operation | On-disk effect | Upstream citation |
|---|---|---|
| `pg_create_physical_replication_slot(name)` / `CREATE_REPLICATION_SLOT name PHYSICAL` | `mkdir pg_replslot/<name>/`, write `state.tmp` then `rename → state`. Magic `0x1051CA1`, version `5`. | `slot.c:2319` (`SaveSlotToPath`) |
| `pg_create_logical_replication_slot(name, plugin)` | Same on-disk layout; payload carries `plugin` and `database`. | `slot.c:2319` |
| `START_REPLICATION` advances `restart_lsn` | `state.tmp` → `rename` on every checkpoint (and at clean shutdown via `CheckPointReplicationSlots`). The file rewrite is atomic w.r.t. crash. | `slot.c:2438-2451` |
| `pg_replication_slot_advance(name, target_lsn)` | Same rewrite. | `slot.c:2319` |
| `pg_drop_replication_slot(name)` | `rmdir pg_replslot/<name>/` (after the slot directory is unlinked of `state`). | `slot.c` `ReplicationSlotDropPtr` |
| Persistent vs ephemeral | `ReplicationSlotPersistentData.persistency` ∈ `{RS_PERSISTENT, RS_EPHEMERAL, RS_TEMPORARY}`. Only `RS_PERSISTENT` survives a restart; ephemeral slots are removed during `StartupReplicationSlots`. | `slot.h:81` |

`ReplicationSlotOnDisk` struct, exact wire bytes
(`slot.c:65-83`):

```
offset  size  field
  0     4     magic           = 0x1051CA1
  4     4     checksum        = CRC32C(version || length || slotdata)
  8     4     version         = 5
 12     4     length          = sizeof(ReplicationSlotPersistentData)
 16     ...   slotdata        (ReplicationSlotPersistentData)
```

The fields *not* covered by the checksum are `magic` and `checksum`
itself (`ReplicationSlotOnDiskNotChecksummedSize = 8`, `slot.c:131`).

`ReplicationSlotPersistentData` payload (`slot.h:70-137`): `name`
(`NameData[64]`), `database` (`Oid`), `persistency`
(`ReplicationSlotPersistency`), `xmin` (`TransactionId`),
`catalog_xmin`, `restart_lsn` (`XLogRecPtr`), `invalidated`
(`ReplicationSlotInvalidationCause` enum), `confirmed_flush`,
`two_phase_at`, `two_phase` (`bool`), `plugin` (`NameData[64]`),
`synced` (`char`), `failover` (`bool`). Total
`ReplicationSlotOnDiskV2Size` bytes.

Goopg currently encodes slots as JSON (`internal/wal/slots.go:393`
`writeSlotLocked`) — adequate for goopg-internal use but incompatible
with the standby's `RestoreSlotFromDisk` magic/version/CRC check at
`slot.c:2556-2604`. The basebackup path papers over this by stripping
`pg_replslot/*` from the tar (`basebackup.go:384`), so a vanilla
standby cloned from goopg never sees a slot file at all. That's
sufficient for the standby itself but means the standby cannot
inherit physical slots created on the primary.

### GUC echo into ControlFile

`ControlFile` carries seven hot-standby-relevant GUCs (eight if we
count `wal_log_hints`, which the standby reads but does not gate on):
`wal_level`, `wal_log_hints`, `MaxConnections`,
`max_worker_processes`, `max_wal_senders`, `max_prepared_xacts`,
`max_locks_per_xact`, `track_commit_timestamp`.

The fields are written:

1. **At initdb**, by `InitControlFile` (`xlog.c:4223-4230`).
2. **At every checkpoint**, indirectly: `CreateCheckPoint`
   (`xlog.c:7290-7306`) re-`UpdateControlFile`s, picking up the
   in-memory `ControlFile->*` values that the postmaster set at
   startup or that `XLogReportParameters` updated below. Checkpoint
   itself does **not** re-derive the GUC values from the live GUC
   tables — that is the job of `XLogReportParameters`.
3. **When a GUC change is observed at postmaster startup**, by
   `XLogReportParameters` (`xlog.c:8147-8199`): compare each of the
   eight fields against the live GUC; on any mismatch emit
   `XLOG_PARAMETER_CHANGE` (so the standby can update its own
   `ControlFile` in `xlog_redo`) and then `UpdateControlFile` to
   imprint the new values.

The cross-link to the standby is:

- Primary's `ControlFile->{MaxConnections, max_worker_processes,
  max_wal_senders, max_prepared_xacts, max_locks_per_xact}` form the
  *lower bound* the standby's compiled-in GUC must meet.
- Primary's `ControlFile->wal_level` must be `≥ replica` (i.e. not
  `WAL_LEVEL_MINIMAL`).

### `CheckRequiredParameterValues` enforcement table

Called by `StartupXLOG` after every shared-memory state change that
affects WAL applicability. FATAL on the standby if any row fails
(`xlog.c:5423-5461`).

| Standby GUC | Compared against `ControlFile->` field | Condition | FATAL site |
|---|---|---|---|
| (implicit) `wal_level` | `wal_level` | `wal_level != WAL_LEVEL_MINIMAL` (must be `replica` or `logical`) | `xlog.c:5429-5435` |
| `max_connections` | `MaxConnections` | standby ≥ primary | `xlog.c:5445-5447` |
| `max_worker_processes` | `max_worker_processes` | standby ≥ primary | `xlog.c:5448-5450` |
| `max_wal_senders` | `max_wal_senders` | standby ≥ primary | `xlog.c:5451-5453` |
| `max_prepared_transactions` | `max_prepared_xacts` | standby ≥ primary | `xlog.c:5454-5456` |
| `max_locks_per_transaction` | `max_locks_per_xact` | standby ≥ primary | `xlog.c:5457-5459` |

For the goopg primary to pass these checks against a default-tuned
vanilla PG18 standby, the `ControlFile` GUC echoes must be:

- `wal_level = 1` (`WAL_LEVEL_REPLICA`) at minimum.
- `MaxConnections = 100` (PG18 default).
- `max_worker_processes = 8`.
- `max_wal_senders = 10`.
- `max_prepared_xacts = 0`.
- `max_locks_per_xact = 64`.

These are the values goopg currently hard-codes in
`internal/initdb/pgcontrol.go:183-198` — sufficient at initdb,
inadequate for any production-tuned primary where the operator
raises e.g. `max_connections` to 500 without `XLogReportParameters`
echoing the change.

### `pg_basebackup` wire-side behaviour

`internal/server/basebackup.go::replyBaseBackup` is the goopg-side
analogue of upstream's `SendBaseBackup`. The exclusion list
(`baseBackupExcluded` at `basebackup.go:93`) currently lists only
`postmaster.pid`, `postmaster.opts`, `.goopg.ctl.sock`. Comparing
against upstream's full `excludeFiles[]` /`excludeDirContents[]`
above, the following are missing or partial:

- Files: `postgresql.auto.conf.tmp`, `current_logfiles.tmp`,
  `pg_internal.init*` (prefix-match), `backup_label`,
  `tablespace_map`, `backup_manifest`.
- Directory contents: `pg_stat_tmp`, `pg_dynshmem`, `pg_notify`,
  `pg_serial`, `pg_snapshots`, `pg_subtrans` — none currently
  filtered. Goopg does not yet create most of these, so the gap is
  latent: the day an extension creates a `pg_stat_tmp/<file>`, the
  basebackup will ship it and the standby will rebuild stats over
  the stale snapshot. `pg_replslot/*` is already special-cased (good)
  but the trapdoor at `basebackup.go:384` is hard-coded; converting
  to the table-driven form will catch the others as they appear.

---

## What goopg must produce

| Artefact | Status | Existing site | Gap |
|---|---|---|---|
| `standby.signal` create / detect / remove helpers | `done` | `internal/initdb/standby.go:29-86` (`StandbySignalFile`, `IsStandby`, `CreateStandbySignal`, `RemoveStandbySignal`) | — |
| `recovery.signal` constant + handling | `partial` | `internal/initdb/standby.go:35` declares the constant; no create / detect path. | Wire `RecoverySignalFile` into the startup decision tree (`open.go:977`) once archive-recovery support lands; not needed for streaming. |
| Timeline-history file writer | `done` | `internal/wal/timeline_history.go:121` (`WriteHistory`) — atomic `tmp` + `rename`, mode `0o600`. | — |
| Timeline-history caller on standby promotion | `done` | `cmd/goopg/standby.go:279` (`runPromote` → `finalizePromotion`) reads parent history, appends `{TLI, SwitchLSN, "no recovery target specified"}`, writes new history. | — |
| Timeline-history caller on **primary** promotion (rare; PITR or `pg_promote()` against a primary that came up from crash recovery) | `missing` | none. | Add a hook in the (future) `internal/wal/recovery.go` end-of-recovery path that calls `wal.WriteHistory` if the running mode transitions to `DB_IN_PRODUCTION` with a new TLI. |
| Replication-slot directory layout | `partial` | `internal/wal/slots.go:101-130` (`OpenSlots`) creates `pg_replslot/` at mode `0o700` and reads existing slot files. | — |
| Slot file format (PG-compatible binary) | `missing` | `internal/wal/slots.go:393` (`writeSlotLocked`) writes JSON; PG18's `RestoreSlotFromDisk` will FATAL on `cp.magic != SLOT_MAGIC`. | Add a binary encoder mirroring `ReplicationSlotOnDisk`: 4-byte magic `0x1051CA1`, 4-byte CRC32C placeholder, 4-byte version `5`, 4-byte length, then `ReplicationSlotPersistentData` body. Compute the CRC32C over bytes `[8 .. end]`. Atomic `state.tmp` → `rename → state`, `fsync` both file and parent dir. New file: `internal/wal/slots_pg.go`. |
| Atomic slot file rewrite (`state.tmp` → `state`) | `done` | `internal/wal/slots.go:393-425` (tempfile + `os.Rename`). | Reuse this scaffolding for the binary writer above. |
| Slot drop (`pg_replslot/<slot>/` rmdir) | `done` | `internal/wal/slots.go:216` (`Drop`). | — |
| `CheckRequiredParameterValues`-feeding GUC echoes at initdb | `partial` | `internal/initdb/pgcontrol.go:183-198` writes the six fields with hard-coded PG18 defaults. | Read from `internal/config` at initdb time so an operator who sets `max_connections=500` in `postgresql.conf` and re-runs `goopg init` gets the right echo. Cross-ref `02-` "Initdb-time field status" rows for `MaxConnections`, `max_worker_processes`, `max_wal_senders`, `max_prepared_xacts`, `max_locks_per_xact`. |
| `XLogReportParameters` equivalent (runtime GUC change → `XLOG_PARAMETER_CHANGE` + `UpdateControlFile`) | `missing` | none. | New code in `internal/wal/checkpointer.go` (or a dedicated `internal/wal/parameter_change.go`): at postmaster start and on `SIGHUP`, diff each of the eight GUCs against `ControlFile->*`; on mismatch emit `XLOG_PARAMETER_CHANGE` then call `initdb.updateControlFile` (the helper proposed in `02-`'s "Recommended Go API"). |
| `xlog_redo(XLOG_PARAMETER_CHANGE)` on standby side | `partial` | `internal/wal/recovery.go` decodes WAL records but does not yet imprint XLOG_PARAMETER_CHANGE into a local `ControlFile`. | Wire to `initdb.updateControlFile` from the recovery replay loop, mirroring `xlog.c:8581-8587`. |
| `pg_basebackup` server-side exclusion list | `partial` | `internal/server/basebackup.go:93` covers 3 / 14 upstream entries. | Replace `baseBackupExcluded` with two tables `excludeFiles` and `excludeDirContents`; add the missing 11 entries listed in the exclusion table above; convert `pg_replslot` from the inline `HasPrefix` (`basebackup.go:384`) to a `excludeDirContents` row that ships an empty directory instead. |
| `pg_replslot/` empty-directory shipping during basebackup | `done` | `internal/server/basebackup.go:413-418` writes an empty `pg_replslot/` tar entry. | — |
| Timeline-switch WAL record on promotion (`XLOG_END_OF_RECOVERY`) | `missing` | none. | The standby controller currently bumps `timeline_id` on disk and writes the `.history` file but does not insert an `XLOG_END_OF_RECOVERY` WAL record. A downstream cascaded standby attaching mid-promotion would miss the TLI bump. Defer until cascaded replication is in scope; record the gap. |

### Single-helper Go API

The slot-file writer should grow a binary path alongside the JSON
one, gated by a build flag or by the slot's `Kind`:

```go
// SavePhysicalSlotPG writes pg_replslot/<slot.Name>/state in upstream
// PG18 binary format (SLOT_MAGIC=0x1051CA1, SLOT_VERSION=5,
// CRC32C(payload), ReplicationSlotPersistentData body). Mirrors
// SaveSlotToPath in src/backend/replication/slot.c:2319.
func SavePhysicalSlotPG(slotDir string, data ReplicationSlotPersistentData) error
```

For the GUC-echo path, plumb `updateControlFile` from `02-`'s
"Recommended Go API" into a single `ReportParameters` entry point in
`internal/wal/parameter_change.go`.

---

## Verification

1. **`pg_basebackup -h goopg -D /tmp/sb -X stream -R` succeeds.**

   ```bash
   goopg init -D /tmp/pg-goopg && goopg start -D /tmp/pg-goopg &
   pg_basebackup -h 127.0.0.1 -p 5433 -D /tmp/sb -X stream -R
   test -f /tmp/sb/standby.signal
   test -f /tmp/sb/global/pg_control
   test ! -f /tmp/sb/postmaster.pid
   test ! -f /tmp/sb/backup_manifest
   test ! -d /tmp/sb/pg_stat_tmp/anything  # directory present, contents stripped
   test ! -e /tmp/sb/pg_replslot/some_slot/state
   ```

2. **`pg_controldata /tmp/sb` returns sane values.** No "WAL file is
   corrupt" or "control file contains invalid data" warnings; the
   six GUC echoes match the primary's `postgresql.conf`.

3. **Standby starts and reaches hot standby.**

   ```bash
   pg_ctl -D /tmp/sb -l /tmp/sb.log start
   psql -h 127.0.0.1 -p 5435 -d template1 -c 'SELECT pg_is_in_recovery();'
   # expect: t
   ```

   The standby log must contain `database system is ready to accept
   read-only connections` and must NOT contain `requires <GUC> ≥ N`.

4. **Promotion writes `<NEW_TLI>.history`.**

   ```bash
   pg_ctl -D /tmp/sb promote
   test -f /tmp/sb/pg_wal/00000002.history
   grep -E '^1\s+[0-9A-F]+/[0-9A-F]+\s+' /tmp/sb/pg_wal/00000002.history
   ```

5. **Slot advance rewrites `state` atomically.**

   ```sql
   SELECT pg_create_physical_replication_slot('s1');
   SELECT pg_replication_slot_advance('s1', pg_current_wal_lsn());
   ```

   In a parallel shell, `inotifywait -m /tmp/pg-goopg/pg_replslot/s1`
   must show `state.tmp CREATE`, `state.tmp MOVED_FROM`, and
   `state MOVED_TO` in sequence, with no `state OPEN .* WRITE`
   between them — i.e. the rename is the only mutator of `state`.

6. **`xxd` confirms slot file is PG-binary, not JSON.** Once
   `SavePhysicalSlotPG` lands:

   ```bash
   xxd -l 4 /tmp/pg-goopg/pg_replslot/s1/state
   # expect: 00000000: a1 c1 05 01  (little-endian 0x1051CA1)
   ```

7. **CRC self-check.** A Go unit test computes
   `crc32.Checksum(payload, crcCastagnoliTable)` over bytes 8..end
   of the slot file and compares to `binary.LittleEndian.Uint32(buf[4:8])`.

8. **E2E coverage.** `TestE2E_FailoverGoopgToPG/async`
   (`internal/testport/e2e_failover_goopg_to_pg_test.go:298`) writes
   `standby.signal` on the cloned datadir, starts the standby, and
   asserts the standby's `pg_is_in_recovery()` returns `t`. The test
   already covers steps 1–3 above; once the binary slot format and
   the full exclusion table land it will also cover steps 5–7
   without further harness changes.
