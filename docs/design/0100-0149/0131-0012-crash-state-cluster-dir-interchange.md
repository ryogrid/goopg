# Crash-state cluster-directory interchange — make the two engines interchangeable on a directory the previous engine did *not* shut down cleanly

**Status:** draft
**Date:** 2026-08-11
**Milestone:** M0131 (Theme F, S16–S29 umbrella)

## Problem

M0131's headline is that a goopg `$PGDATA` and a PG `$PGDATA` are the same
directory: either engine can be pointed at either one. Themes A and B prove that
claim only under a precondition neither states in its title — **the source engine
exited politely.** `0130-0002` §"WAL replay constraint" is explicit (*"the reverse
path requires a cleanly shut down source data dir"*), and S3
(`TestE2E_GoopgColdStartOnPGDataDir`) and S4 (`TestE2E_PGColdStartOnGoopgDataDir`)
both assert `DB_SHUTDOWNED` before the handover.

Two engines are not interchangeable on a directory if the interchange only works
when the previous one was asked nicely. Every real handover reason — an OOM kill,
a host reset, a container eviction, a failed upgrade — is exactly the case both
themes exclude. Theme F removes the precondition in both directions.

Two investigations at filing (2026-08-11) found this is **not merely a capability
gap**. Each direction already loses committed data today, on directories goopg is
supposed to handle. Those are S16 and S17, and they land before anything else in
the theme.

## Design

### The two live bugs

**S16 (reverse) — an unrecognised record is read as end-of-WAL.** goopg's record
header decoder rejects any rmid in `(15, 128)`
(`internal/wal/xlog_record.go:218-220`), because `MaxKnownRmgr = RmgrSeq = 15`
(`:65`, `:71`) while PG 18's real maximum is 21. A header-decode failure inside
`readAllPageAware` is not an error — it is the **end-of-WAL** signal
(`internal/wal/reader.go:152-165`), and `endOfWAL` (`reader.go:243-249`) is a
`slog.Warn` and nothing more.

Why that is data loss rather than a missing feature: every record after the first
rmid-16..21 record is dropped, `ReplayRecords` reports **success**, and the
writer's independent `detectWritePos`/`scanLastSegmentEnd` scan
(`internal/wal/writer.go:1287`, `:1494`) stops at the same byte — so goopg
appends new WAL **over** durable, unreplayed records. The loss becomes permanent
on the first write. One `pg_logical_emit_message` (rmid 21) in a PG crash tail is
enough. Mechanism, the full caller chain, and the sibling silent paths:
`0131-0013`.

**S17 (forward) — `DB_IN_PRODUCTION` is never stamped.** goopg writes
`pg_control.state` from exactly one runtime site, the checkpointer
(`internal/wal/checkpointer.go:736-748`), whose first tick is one full
`checkpoint_timeout` after start (`ticker := time.NewTicker(c.cfg.Interval)`,
`checkpointer.go:440`, with no leading immediate checkpoint). `initdb` stamps
`DB_SHUTDOWNED` and nothing in the server-startup path overwrites it.

Why that is data loss: a kill inside the first 300 s of a clean start leaves
`pg_control` claiming `DB_SHUTDOWNED`. PG then falls through ALL THREE arms of
`postgres/src/backend/access/transam/xlogrecovery.c:924-937` — `InRecovery` stays
false, `PerformWalRecovery()` is skipped entirely, and PG resumes inserting WAL
at the end of the shutdown checkpoint, overwriting goopg's tail. No PANIC, no
FATAL, nothing alarming in the log. goopg's own code already asserts the inverse
belief as if established (`internal/initdb/open.go:3190-3198`: *"pg_control left
at DB_IN_PRODUCTION; recovery on next start"*) — true only if an online
checkpoint has already run. Mechanism: `0131-0014`.

### The end-of-WAL contract

The single most load-bearing fact for this theme: **PG's crash recovery has no
"this is corruption" branch at the reader level at all.** `ReadRecord` is called
with `emode = LOG` on both the first-record path (`xlogrecovery.c:1744`) and the
main redo loop (`:1851`); `emode_for_corrupt_record` (`:4074`) only ever
*downgrades* LOG to DEBUG1 on a repeated complaint at the same LSN. Every
validation failure below therefore means one thing — **redo stops here** — and
the cluster comes up on the prefix that validated.

That makes the contract a *floor*, not a menu: goopg's tail must land on one of
these patterns, and goopg's own reader must treat exactly these — and nothing
else — as a benign stop.

| # | Byte pattern | Upstream site (`postgres/src/backend/access/transam/`) | Effect |
|---|---|---|---|
| 1 | all-zero record header / `xl_tot_len == 0` | `xlogreader.c:1142-1149` | benign stop |
| 2 | `0 < xl_tot_len < SizeOfXLogRecord` (24) | `xlogreader.c:1142-1149`; boundary-spanning pre-check `:667-674` | benign stop |
| 3 | `!RmgrIdIsValid(xl_rmid)` — i.e. above `RM_MAX_BUILTIN_ID` (21) and not a registered custom rmgr | `xlogreader.c:1150-1156` | benign stop |
| 4 | `xl_prev != PrevRecPtr` (sequential read) | `xlogreader.c:1172-1187` | benign stop |
| 4b | `!(xl_prev < RecPtr)` (random access) | `xlogreader.c:1157-1171` | benign stop |
| 5 | bad CRC32C over payload + header | `xlogreader.c:1217-1223` | benign stop |
| 6 | all-zero page — `xlp_magic != XLOG_PAGE_MAGIC` | `xlogreader.c:1247-1260` | benign stop |
| 7 | undefined bits in `xlp_info` | `xlogreader.c:1262-1275` | benign stop |
| 8 | `xlp_sysid` / `xlp_seg_size` / `xlp_xlog_blcksz` mismatch (long header) | `xlogreader.c:1281-1301` | benign stop |
| 9 | offset 0 without `XLP_LONG_HEADER` | `xlogreader.c:1303-1317` | benign stop |
| 10 | `xlp_pageaddr != recptr` — **the recycled-segment defence** | `xlogreader.c:1324-1337` | benign stop |
| 11 | `xlp_tli < latestPageTLI` on a later page | `xlogreader.c:1348-1365` | benign stop |
| 12 | `XLP_FIRST_IS_CONTRECORD` with no pending contrecord | `xlogreader.c:626-632` | benign stop |
| 13 | pending contrecord, next page lacks the flag | `xlogreader.c:757-763` | benign stop |
| 14 | `xlp_rem_len == 0` or `!= total_len - gotlen` | `xlogreader.c:769-778` | benign stop |
| 15 | next segment ENOENT | `xlogrecovery.c:3818-3825` → `:3657-3662` | benign stop, and **SILENT** — no ereport at all |
| 16 | short `pg_pread` (< 8192) | `xlogrecovery.c:3427-3454` | benign stop (at `emode`) |

Against those, the **positional** PANIC/FATALs — none of them a property of the
bytes, all of them a property of *where* the bytes are:

| Position | Upstream site | Severity |
|---|---|---|
| the checkpoint record named by `pg_control`/`backup_label` is unreadable | `xlogrecovery.c:793-804` | PANIC `could not locate a valid checkpoint record` |
| the redo-point record referenced by that checkpoint is unreadable | `xlogrecovery.c:808-816` | PANIC `could not find redo location` |
| the redo-point record exists but is not `XLOG_CHECKPOINT_REDO` | `xlogrecovery.c:1721-1738` | FATAL `unexpected record type found at redo point` |
| the final re-read of the last replayed record | `xlogrecovery.c:1541-1543` (`ReadRecord(..., PANIC, ...)`) | PANIC |
| segment open fails with anything other than ENOENT | `xlogrecovery.c:4304-4307` | PANIC `could not open file` |
| `XLOG_OVERWRITE_CONTRECORD` payload disagrees with `overwrittenRecPtr` | `xlogrecovery.c:2101-2108` | FATAL `mismatching overwritten LSN` |
| `minRecoveryPoint` not in the requested timeline's history | `xlogrecovery.c:880-887` | FATAL (S29's failure mode) |

Read together: a goopg tail is safe for PG to consume iff (a) it ends on one of
rows 1–16, and (b) the checkpoint and redo-point records `pg_control` names are
intact. Everything Theme F does on the forward side is in service of (b) — the
bytes take care of themselves (see below).

### What is already fine and must NOT be re-planned

The forward direction is in better shape than the milestone assumed. These are
verified bounds on scope, not aspirations:

- **WAL segment zeroing is a non-issue — goopg is *safer* than upstream.**
  `wal_init_zero` BootVal is `on` (`internal/config/defaults.go:398-402`), and
  `recycleSegmentFile` (`internal/wal/writer.go:2369-2379`) renames **and then
  zero-fills** via `preallocateSegment`. Upstream's `InstallXLogFileSegment`
  (`xlog.c:3559`) is a bare `durable_rename` (`:3598`) with no fill. A goopg
  crash tail is therefore followed by zeros, so PG hits row 1 or row 6 of the
  table at LOG level. (The same asymmetry is a *liability* in the reverse
  direction — see S19 in `0131-0013`.)
- **`CheckRequiredParameterValues` is entirely a no-op in crash recovery.** Both
  of its branches are gated on `ArchiveRecoveryRequested` (`xlog.c:5429`,
  `:5442`). Drop it from scope.
- **Empty `pg_twophase` / `pg_commit_ts` / `pg_multixact` are all fine forward.**
  `PrescanPreparedTransactions` over an empty dir is a no-op returning `nextXid`,
  which is exactly what `StartupSUBTRANS` wants; `StartupCommitTs` is gated on
  `track_commit_timestamp`, which goopg writes false; `TrimMultiXact` succeeds
  because initdb's zeroed `pg_multixact/offsets/0000` placeholder is
  load-bearing.
- **`checkPointCopy.oldestActiveXid` frozen at 0 is not a blocker.** It is read
  from exactly one place, `xlog.c:5835`, inside a block gated on
  `ArchiveRecoveryRequested && EnableHotStandby` (`:5822`). Crash recovery
  derives the value from `PrescanPreparedTransactions` instead (`:5833`).
- **Unlogged relations are a "too durable" divergence, not corruption.** goopg
  never creates `_init` forks, so PG's `ResetUnloggedRelations` is a no-op and a
  goopg unlogged table survives where PG would have truncated it. Wrong, but not
  a crash-recovery blocker — ledger it, do not absorb it here.

Conversely the reverse direction is **worse** than the missing-rmgr list
suggests: the bigger cost is opcode gaps *inside* rmgrs goopg already claims
(S21) — `XLOG_HEAP2_MULTI_INSERT` (every COPY), `XLOG_HEAP2_VISIBLE` (every
VACUUM), `XLOG_HEAP_LOCK` (every `SELECT … FOR UPDATE`), `XLOG_HEAP_TRUNCATE`,
`XLOG_CLOG_ZEROPAGE` (every 32768 XIDs), `XLOG_SMGR_TRUNCATE`,
`XLOG_STANDBY_LOCK`, `XLOG_XACT_ASSIGNMENT` (any subtransaction), and seven btree
opcodes. None is exotic; all are ordinary heap+btree traffic.

### Slice map

- `0131-0013-wal-reader-fail-closed.md` — **S16** fail closed: an unrecognised
  record is not end-of-WAL (**LAND FIRST**); **S19** validate
  `xlp_pageaddr`/`xlp_tli`, distrust recycled segments (RISKY).
- `0131-0014-pgcontrol-runtime-state-and-durability.md` — **S17** stamp
  `DB_IN_PRODUCTION` at startup (**LAND FIRST**); **S18** pg_control writer
  durability + full `checkPointCopy` coverage; **S20** pg_control-driven recovery
  in goopg (DBState, redo start, `pg_internal.init` hygiene).
- `0131-0015-pg-wal-opcode-coverage.md` — **S21a/b** opcode coverage inside the
  already-handled rmgrs (LARGE); **S22** CLOG replay opcode dispatch +
  `subxacts[]` parsing.
- `0131-0016-multixact-durable-slru.md` — **S24** durable `pg_multixact` SLRU +
  `multixact_redo` (LARGE/RISKY, optional — see the concurrency trigger).
- `0131-0017-crash-interchange-e2e.md` — **S27** forward crash E2E (+ stale
  pidfile, torn contrecord); **S28** reverse crash E2E.
- Rides the M0131 plan — **S23** the cheap tail (LogicalMessage / ReplOrigin /
  Generic / CommitTs); **S25** index-AM boundary: detect, refuse specifically,
  ledger; **S26** `pd_lsn` completeness audit on logged change paths; **S29**
  BASE_BACKUP stops mutating the source `pg_control`.

Ordering, and why: `0131-…-coldstart-and-system-views.md` §Dependencies (Theme F
block). S16 and S17 precede everything, including each other's direction. S19 is
not gated on the crash work — it also repairs the **clean** reverse path (S3).
S21b needs S16.3's refusal first, or a silent regression is indistinguishable
from success. Theme F does not depend on Themes C/D.

## Guards

1. The theme is **not** discharged by S16+S17 alone; those two only stop active
   data loss. The interchange claim is discharged by S27 (forward) and S28
   (reverse), and by nothing weaker.
2. No slice in this theme may weaken a row of the end-of-WAL contract table: a
   goopg tail that stops on anything outside rows 1–16 is a regression, whatever
   the unit tests say.
3. The "already fine" list is a scope *bound*. Re-opening any of those five items
   requires a citation that contradicts the one recorded here.
4. Each owning doc carries its own slice guards; this doc adds none of its own
   beyond the above.
5. UNITS + SMOKE green.

## References

- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
  §"Theme F" — the authoritative decomposition this doc expands
- `docs/design/0130-0002-pg-class-heap-persistence.md` §"WAL replay constraint"
- `postgres/src/backend/access/transam/xlogreader.c` — `ValidXLogRecordHeader`,
  `ValidXLogRecord`, `XLogReaderValidatePageHeader`, contrecord reassembly
- `postgres/src/backend/access/transam/xlogrecovery.c` — `InitWalRecovery`,
  `PerformWalRecovery`, `XLogPageRead`, `emode_for_corrupt_record`
- `postgres/src/backend/access/transam/xlog.c` — `InstallXLogFileSegment`,
  `CheckRequiredParameterValues`, `StartupXLOG`
- `postgres/src/include/access/rmgrlist.h:28-49` — the 22 resource managers
