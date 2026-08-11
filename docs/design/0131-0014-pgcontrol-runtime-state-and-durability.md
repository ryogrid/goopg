# pg_control runtime state and durability — stamp `DB_IN_PRODUCTION`, make the writer crash-safe, start from pg_control

**Status:** S17 **accepted — landed 2026-08-11**; S18.1 + S18.2 **accepted —
landed 2026-08-11**; S18.3 / S18.4 / S20 / S29 still draft
**Date:** 2026-08-11
**Milestone:** M0131 (Theme F, S17 + S18 + S20)

## Findings — S17 as built (2026-08-11)

The design held without correction. `Open` (`internal/initdb/open.go`) now calls a
new `stampInProduction` immediately before it returns the `Runtime` — after WAL
replay, the buffer pool, the VM/FSM loads and the background workers, and
strictly before `cmd/goopg/main.go` hands the runtime to the server's accept
loop. It is the only non-test caller of `initdb.Open` (`main.go:453`, inside
`runStart`), so the stamp is server-start-only: `goopg init`, `goopg stop` and
`goopg checkpoint` do not go through it.

Three things worth recording:

1. **Failure policy.** A genuinely absent `pg_control` warns and continues; every
   other failure (unreadable, short, unwritable) aborts `Open` after releasing
   the pool, WAL writer and storage manager. Upstream PANICs on the same
   conditions; the absent-file exemption exists only for hand-assembled
   directories, since `verifyInitialized` checks `PG_VERSION` and nothing else.
2. **The guard is proven fail-when-broken**, not merely green: with the stamp
   short-circuited, `TestOpenStampsDBInProduction`
   (`internal/initdb/pgcontrol_runtime_state_test.go`) reports `state after Open:
   got 1, want 6`. It also asserts the precondition (a fresh `initdb` directory
   is `DB_SHUTDOWNED`), `LastCheckpointLSN() == 0` at the moment of the
   assertion — so a pass cannot be a checkpoint in disguise — and the inverse
   direction, that a clean `Close` still lands on `DB_SHUTDOWNED`.
3. **The stamp is not yet crash-safe itself** — it rides `UpdateControlFile`,
   whose `os.WriteFile` (`O_TRUNC`, no fsync) is exactly the S18.1 defect. A
   SIGKILL inside that write can leave a zero-length `pg_control`, which upstream
   reads as `PANIC: could not read file "global/pg_control": read 0 of 296`. S17
   narrows the window from `checkpoint_timeout` (300 s) to a single unsynced
   write; S18.1 closes it. Ledgered.

S17.3's check came back negative and stays open: goopg still never reads
`State`, so it does not act on its own stamp — a goopg restart over a
`DB_IN_PRODUCTION` directory replays WAL for unrelated reasons (the checkpoint
scan), not because it recognised a crash. That is S20.1, ledgered.

## Findings — S18.1 + S18.2 as built (2026-08-11)

Both landed as designed; the §S18.2 offset table needed no correction — the real
`pg_controldata` agrees with every one of its nine rows on a goopg-authored
directory. Four things worth recording:

1. **The writer's real failure mode is "cannot set", not "writes zeros".** The
   filing said the nine fields "survive only by read-modify-write". That is
   right, but it has a consequence the guards had to be designed around: with an
   *encode* line missing, RMW **preserves** the on-disk value, so both the
   oracle comparison and a byte-for-byte round-trip stay green — the field is
   merely unsettable. Only a *decode* line missing is destructive (the field
   decodes as 0 and encode writes that 0 over live data). So the golden test
   needs three distinct assertions, and they were each proven fail-when-broken
   by scripted revert: byte-for-byte identity (catches a wrong offset — an
   88→89 slip reports `changed byte 89`), oracle agreement (catches goopg and
   PG disagreeing about the same bytes), and **settability** (seed 55, read back
   55 — the only one that catches a dropped encode line, and it reports
   `oldestMulti = 55, want 9005`).
2. **`writeControlFileDurably` is a no-`O_CREATE` writer.** `os.WriteFile` would
   conjure a pg_control out of nothing; the new one requires the file to exist,
   which is correct — initdb creates it, updates only overwrite it. The
   short-file promotion at the top of `UpdateControlFile` stays as the
   grow-to-8192 path, and the write is now explicitly `buf[:8192]`, so a
   hypothetical longer file keeps its tail instead of being truncated away
   (upstream's `O_RDWR` + fixed-size `write()` has the same property).
3. **The truncation guard is testable without a crash.** Proving "there is no
   window in which pg_control is zero bytes" directly needs a fault injector.
   The observable proxy is a sentinel past byte 8192: an `O_TRUNC` writer erases
   it and shortens the file, a `WriteAt` writer does not. That, plus the
   no-`O_CREATE` assertion, is what pins the open flags — the design's guard #3
   proposed "assert by construction", and this is strictly better.
4. **The fsync is not measurably on any hot path.** `UpdateControlFile`'s
   callers are the checkpointer, promotion, `XLOG_PARAMETER_CHANGE` replay and
   BASE_BACKUP — none per-transaction. No GUC gate (`fsync = off`) was wired in:
   upstream passes `do_sync = true` from every backend caller of
   `update_controlfile`, and pg_control is the one file whose loss is
   unrecoverable.

`oldestActiveXid` is now written correctly rather than left at 0, as the design
asked. S18.3 (live TLI) and S18.4 (`encodeCheckPointStruct`'s constants) remain
open — and S18.4 gained a fifth constant while reading it: `PrevTimeLineID`
(payload offset 12) is never written at all, so every goopg checkpoint record
carries `PrevTimeLineID = 0` where PG writes the current TLI.

## Problem

Three defects in one 296-byte file, from the two Theme F investigations
(`0131-bidirectional-cluster-dir-coldstart-and-system-views.md` §"Theme F"),
separated here only by cost.

**S17 is a live data-loss bug and the cheapest fix in the milestone.** A grep for
`DBStateInProduction|DBStateShutdowned` across non-test Go returns exactly three
sites: the constant block (`internal/control/pgcontrol.go:29-36`),
`UpdateControlCheckpoint` (`internal/initdb/pgcontrol.go:148`, reached only from
BASE_BACKUP — §S29), and the checkpointer (`internal/wal/checkpointer.go:736-748`,
assignments at `:745`/`:747`). **There is no `State` write anywhere in the
server-startup path.** `initdb` stamps `DB_SHUTDOWNED`
(`internal/initdb/pgcontrol.go:185` — *Theme F cites `:187`; the byte is written
at `:185`*), and the checkpointer's first tick is one full `checkpoint_timeout`
after start: `ticker := time.NewTicker(c.cfg.Interval)` (`checkpointer.go:440`),
**no leading immediate checkpoint**. `checkpoint_timeout` BootVal is `300` s
(`internal/config/defaults.go:348`, plumbed at `cmd/goopg/main.go:701-705`); the
`max_wal_size` volume trigger (BootVal `1024` MB, `defaults.go:360`; ticker
`checkpointer.go:447-453`, case `:463-472`, predicate `:483-494`) never fires
under a light workload.

So a SIGKILL inside the first 300 s of an otherwise clean start leaves
`pg_control` claiming `DB_SHUTDOWNED` over a WAL tail full of committed work.
Upstream then takes the **third** arm of:

```c
/* postgres/src/backend/access/transam/xlogrecovery.c:924-936 */
	if (checkPoint.redo < CheckPointLoc)
	{
		if (wasShutdown)
			ereport(PANIC,
					(errmsg("invalid redo record in shutdown checkpoint")));
		InRecovery = true;
	}
	else if (ControlFile->state != DB_SHUTDOWNED)
		InRecovery = true;
	else if (ArchiveRecoveryRequested)
	{
		/* force recovery due to presence of recovery signal file */
		InRecovery = true;
	}
```

Arm 1 does not fire — goopg writes `checkPointCopy.redo` consistent with
`checkPoint` at every checkpoint, so `redo < CheckPointLoc` is not reliably true.
Arm 2 does not fire because `state` says `DB_SHUTDOWNED`. Arm 3 needs a recovery
signal file a crash-restart has none of. `InRecovery` stays false,
`PerformWalRecovery()` (`xlog.c:5886`, inside `if (InRecovery)`) is **skipped
entirely** (`:5889-5890` takes `performedWalRecovery = false`), and PG resumes
inserting WAL at the end of the stale checkpoint — over goopg's tail. No PANIC,
no FATAL, nothing alarming in the log; every committed transaction there is gone.

goopg's own code asserts the inverse as if established, at
`internal/initdb/open.go:3190-3198`: *"immediate shutdown: skipping final
checkpoint … pg_control left at DB_IN_PRODUCTION; recovery on next start"*. That
holds **only if an online checkpoint has already run**; on a cluster younger than
`checkpoint_timeout` that never crossed `max_wal_size`, `goopg stop -mode
immediate` leaves `DB_SHUTDOWNED` — the exact opposite of the stated postcondition.

**S18** is durability and completeness of the writer. **S20** is the read side:
goopg never consults `State` at all, so it cannot distinguish a crashed directory
from a clean one — including its own.

## Design

### S17 — stamp `DB_IN_PRODUCTION` before the first client

Mirror `xlog.c:6204-6205`: upstream takes `ControlFileLock`, sets
`ControlFile->state = DB_IN_PRODUCTION`, and calls `UpdateControlFile()` (`:6211`)
at the end of `StartupXLOG`, before backends are admitted. Home: the runtime open
path in `internal/initdb/open.go`, ordered next to where
`internal/server/server.go:677` writes the PID file — after replay and storage are
up, strictly **before** `acceptLoop`. One `control.UpdateControlFile` call setting
`State = DBStateInProduction` and `Time = now`. Also correct the comment at
`open.go:3190-3198`, which asserts a postcondition the code does not establish
(and which S17 makes true again). S17 must agree with S20: a start that stamps
`DB_IN_PRODUCTION` and then crashes is exactly the input S20.1 must recognise.

### S18.1 — the writer cannot survive its own crash

`UpdateControlFile` (`internal/control/pgcontrol.go:213-237`) is `os.ReadFile` →
mutate → `os.WriteFile(path, buf, 0o600)`. `os.WriteFile` opens
`O_WRONLY|O_CREAT|O_TRUNC` and does **not** fsync. Upstream's `update_controlfile`
(`postgres/src/common/controldata_utils.c:189`) opens `O_RDWR|PG_BINARY` (`:222`),
writes all `PG_CONTROL_FILE_SIZE` bytes in one `write()` (`:237`), and `pg_fsync`s
when `do_sync` (`:255-263`) — it never truncates.

The `O_TRUNC` window is not theoretical: a SIGKILL between truncate and write
leaves a **zero-length pg_control**, and `ReadControlFile` (`xlog.c:4363-4376`)
PANICs `could not read file "global/pg_control": read 0 of 296`. Rewrite as
`os.OpenFile(path, os.O_RDWR, 0o600)` + `WriteAt(buf, 0)` + `Sync()` + `Close()`,
keeping the 8192-byte full image and the CRC32C over `buf[:292]`. The short-file
promotion (`:223-227`) stays as the create path for a fresh cluster.

### S18.2 — the CheckPoint struct is only half decoded

`checkPointCopy` occupies `ControlFileData` offsets 40..127 and is declared at
`postgres/src/include/catalog/pg_control.h:35-65`. Offsets below are **within
`ControlFileData`** (struct offset + 40), derived from declaration order with
x86_64 alignment (`MultiXactId`/`MultiXactOffset` are both `uint32` —
`postgres/src/include/c.h:631`, `:633`; `pg_time_t` is `int64`, forcing a 4-byte
pad before `time`). The table closes exactly: `sizeof(CheckPoint) == 88`, and
goopg already decodes `UnloggedLSN` at 128.

| off | field | C type | goopg today |
|---|---|---|---|
| 40 | `redo` | XLogRecPtr | decoded + encoded (`pgcontrol.go:130`, `:162`) |
| 48 | `ThisTimeLineID` | TimeLineID | decoded + encoded, but **hardcoded 1** by both writers |
| 52 | `PrevTimeLineID` | TimeLineID | decoded + encoded, **hardcoded 1** |
| 56 | `fullPageWrites` | bool | decoded + encoded, **hardcoded true** |
| 60 | `wal_level` | int | decoded + encoded |
| 64 | `nextXid` | FullTransactionId | decoded + encoded (monotonic guard, `checkpointer.go:761-762`) |
| 72 | `nextOid` | Oid | decoded + encoded (monotonic guard) |
| 76 | `nextMulti` | MultiXactId | **never decoded** |
| 80 | `nextMultiOffset` | MultiXactOffset | **never decoded**; never written anywhere ⇒ 0 |
| 84 | `oldestXid` | TransactionId | **never decoded** |
| 88 | `oldestXidDB` | Oid | **never decoded** |
| 92 | `oldestMulti` | MultiXactId | **never decoded** |
| 96 | `oldestMultiDB` | Oid | **never decoded** |
| 104 | `time` | pg_time_t | decoded + encoded |
| 112 | `oldestCommitTsXid` | TransactionId | **never decoded** |
| 116 | `newestCommitTsXid` | TransactionId | **never decoded** |
| 120 | `oldestActiveXid` | TransactionId | **never decoded** |

Add the nine missing fields to `ControlFileData` (`:44-119`),
`decodeControlFileData` (`:123-154`) and `encodeControlFileData` (`:158-202`).
They survive today **only** because `UpdateControlFile` re-reads and rewrites the
whole 8192-byte image, so an initdb- or PG-authored value is preserved by
accident, not by design; S20 needs them as *data*.

**`oldestActiveXid` frozen at 0 is not a blocker** and must not be sold as one:
it is read at exactly one site, `xlog.c:5835`, inside
`if (ArchiveRecoveryRequested && EnableHotStandby)` (`:5821`), and crash recovery
takes the `wasShutdown` branch that derives it from `PrescanPreparedTransactions`
(`:5833`). S18 should still write it correctly — one `uint32`, and it removes a
permanent asterisk from the hot-standby lane.

### S18.3 — the hardcoded TLI stomps promotion into a PG PANIC

Both writers pin the timeline identically: `internal/wal/checkpointer.go:753-754`
and `internal/initdb/pgcontrol.go:153-154`. *(Theme F cites
`checkpointer.go:757-759`; the three hardcoded assignments are `:753-755`.)*
After M0130-S8.5's `finalizePromotion` moves the cluster to TLI 2, segments are
named for TLI 2 while **the very next checkpoint rewrites pg_control's TLI back to
1**; a PG reading the directory looks for the checkpoint on TLI 1, finds no
matching segment, and PANICs `could not locate a valid checkpoint record`.

Thread the live TLI into `Checkpointer.Config` and `UpdateControlCheckpoint`'s
signature — the value is already on disk via `initdb.LoadOrCreateTimelineID`,
which BASE_BACKUP calls at `internal/server/basebackup.go:225`. Thread
`full_page_writes` the same way: the GUC reaches the buffer pool
(`cmd/goopg/main.go:719-721`) but not pg_control, where both writers hardcode
`true` (`checkpointer.go:755`, `initdb/pgcontrol.go:155`) — so a cluster run with
`full_page_writes = off` advertises FPI protection it does not have.

### S18.4 — `encodeCheckPointStruct` writes constants

`internal/wal/recovery.go:724-798` builds the 88-byte CheckPoint payload of the
checkpoint WAL record. It hardcodes `nextMulti = 1` (`:783`), `oldestXid = 3`
(`:784`), `oldestMulti = 1` (`:785`), `oldestCommitTsXid = 3` (`:792`),
`newestCommitTsXid = 3` (`:793`); only `oldestActiveXid` (`:794`) is
parameterised. Five more are never written and are therefore zero in the emitted
record: **`PrevTimeLineID` (struct offset 12) and `fullPageWrites` (16)** — a
divergence Theme F does not list, and the same class of bug as S18.3 one layer
down — plus `nextMultiOffset` (40), `oldestXidDB` (48), `oldestMultiDB` (56).
Write live values for all of them from the sources S18.3 threads, and reconcile
the initdb-vs-checkpoint `oldestMultiDB` divergence: `buildPgControl` writes
`pgTemplate1DbOID = 1` into pg_control (`internal/initdb/pgcontrol.go:221`, const
`:56`) while this encoder leaves the record's copy at 0 — a bug either way.

### S20 — goopg reads pg_control

**goopg has no reader of `State`.** Widening the §Problem grep to `\.State` adds
only `encodeControlFileData`'s own write (`internal/control/pgcontrol.go:160`).
goopg treats `DB_IN_PRODUCTION` and `DB_SHUTDOWNED` identically. `Open()` reads
pg_control for `DataChecksumVersion` (`internal/initdb/open.go:292`) and later for
`CheckPointCopyRedo`/`nextOid`/`nextXid` (`:1188-1206`) — nothing else.

- **S20.1** Read `State` before replay. If it is neither `DBStateShutdowned` nor
  `DBStateShutdownedInRecovery`: log "crash recovery required", stamp
  `DB_IN_CRASH_RECOVERY`, replay, then stamp `DB_IN_PRODUCTION` (S17's write on
  the recovery path) and force an end-of-recovery checkpoint.
- **S20.2** Redo start. `replayStart` (`internal/wal/recovery.go:4408-4432`)
  **scans the whole decoded record list** for the last `isCheckpointRecord`
  (`:4438`). *Refinement to Theme F's wording:* it does read a redo LSN — but from
  the **checkpoint record's own** payload (`checkpointStructOf(...)[0:8]`,
  `:4421-4422`, helper `:4464`), never from `pg_control.CheckPoint` /
  `CheckPointCopyRedo`. Add an explicit redo-LSN parameter fed from pg_control
  when the caller has one; keep the scan as the fallback for goopg-authored dirs.
  Teach `isCheckpointRecord` about `XLOG_CHECKPOINT_REDO` (0xE0, PG17+).
- **S20.3** `RelationCacheInitFileRemove` equivalent. Upstream unlinks
  unconditionally from `StartupXLOG` (`xlog.c:5633`), before any replay. goopg
  unlinks only **reactively**, via `RelcacheInitFileUnlink`
  (`internal/catalog/relcache_inval.go:14-33`), which takes a single `dboid` and
  touches `global/` plus exactly one `base/<dboid>/` — an invalidation hook, not a
  startup sweep. Add an unconditional pre-replay unlink of
  `global/pg_internal.init` and every `base/<all-digit>/pg_internal.init`.
  Symmetric with S10: goopg must do to PG's init file exactly what PG already does
  to goopg's (`0131-0004` §"three deltas" item 1 is the mirror proof).
- **S20.4** Seed the MultiXact allocator. `multixact.NewStoreAt`
  (`internal/multixact/store.go:88`) exists, is documented as *"seed the allocator
  from pg_control's nextMulti"*, and **has no non-test caller** —
  `cmd/goopg/main.go:560` always calls `NewStore()` (`store.go:82`). Wire it to
  the decoded `nextMulti` once S18.2 lands. This does **not** make MultiXacts
  durable; that is S24.
- **S20.5** `minRecoveryPoint` policy, written down: for **crash** recovery (not
  archive recovery) PG leaves it invalid and goopg must not invent one. The
  checkpointer already writes `MinRecoveryPoint = 0` / `MinRecoveryPointTLI = 0`
  (`internal/wal/checkpointer.go:771-772`), which is correct; S20 records it as
  policy so a future change has to argue against a decision. The one violator is
  S29.

### S29 — BASE_BACKUP mutates the source pg_control (rides the plan, not this doc)

Named here because it is the same subsystem; **not** designed here.
`internal/server/basebackup.go:223` calls `initdb.UpdateControlCheckpoint` on the
**live** directory, writing `MinRecoveryPoint = 1`
(`internal/initdb/pgcontrol.go:158`), `MinRecoveryPointTLI = 1` (`:159`) and
`BackupEndPoint = redo` (`:162`) — plus S18.3's hardcoded TLI 1 — into the running
cluster's control file. On a promoted (TLI ≥ 2) cluster, a crash in the
BASE_BACKUP → next-checkpoint window then makes PG FATAL `requested timeline %u
does not contain minimum recovery point %X/%X on timeline %u`
(`xlogrecovery.c:880-887` — *Theme F cites `:878-886`; the test is `:880-882`, the
FATAL text `:884`*). Fix: build the backup's control image into the tar stream
rather than mutating the source.

## Guards

1. **S17, no PG needed.** `Init` → `Start` with `checkpoint_timeout` beyond the
   test's lifetime → `control.ReadControlFile(dir).State == DBStateInProduction`
   **before any checkpoint can have run** (also assert `lastCheckpointLSN == 0`,
   so a pass cannot be a checkpoint in disguise) → `cluster.Kill()` → still
   `DBStateInProduction`.
2. **S17, the inverse.** After `Stop(cluster.ShutdownFast)` the byte is
   `DBStateShutdowned`; `TestE2E_PGColdStartOnGoopgDataDir` already asserts it
   (`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go:176-179`) and must
   stay green, or S17 broke the clean path.
3. **S18.1.** `UpdateControlFile` over a pre-existing 8192-byte file leaves the
   on-disk size at 8192 at every observable point, and the open flags carry no
   `O_TRUNC` (one call site — assert by construction).
4. **S18.2, golden bytes.** `decode → encode` round-trips a real-`initdb` control
   file byte-for-byte over all 8192 bytes; then
   `postgres/local_install/bin/pg_controldata` on a goopg-authored directory
   agrees with the decoded struct on **every row of the §S18.2 table**. That
   binary is in the tree; no cluster needed.
5. **S18.3.** Promote to TLI 2, `goopg checkpoint -D <dir>`, re-read pg_control:
   `CheckPointCopyThisTLI == 2` (fails today). With `full_page_writes = off`,
   `CheckPointCopyFullPageWrites == false`.
6. **S20.1.** A `DB_IN_PRODUCTION` directory produces the crash-recovery banner
   and an end-of-recovery checkpoint on the next start; a `DB_SHUTDOWNED` one
   produces neither.
7. **S20.3.** Plant `pg_internal.init` in `global/` and two `base/<n>/` dirs
   before start; all three are gone after `Open()` returns.
8. **S20.4.** Seed `nextMulti = 4242`; the store's first allocation is ≥ 4242.
9. UNITS + SMOKE green —
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` plus the git
    hook's mandatory pgbench smoke (never `--no-verify`, never `-count=1`). S18
    touches the checkpoint path, so `scripts/tpch-spotcheck.sh` too. Any guard
    that starts a server runs under the cgroup cap:
    `GOOPG_CG_UNIT=<name> scripts/goopg-test-run.sh …`.

## References

- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
  §Theme F — cite by section title, not line number (the plan is a living doc)
- `docs/design/0131-0004-forward-coldstart-e2e.md` — the clean-shutdown sibling.
  Its §Findings F1–F4 are filed as `M0131-S12`–`S15` (Theme B/C work measured by
  S4, landed by the concurrent loop). **There is no collision with Theme F**,
  which occupies S16–S29 — an earlier draft of this doc claimed one, as residue
  of the pre-commit renumbering.
- `docs/design/0130-0002-pg-class-heap-persistence.md` §"WAL replay constraint"
- goopg: `internal/control/pgcontrol.go:29-36`, `:44-119`, `:123-154`, `:158-202`,
  `:213-237`; `internal/initdb/pgcontrol.go:134-165`, `:185`, `:221`;
  `internal/initdb/open.go:292`, `:1188-1206`, `:3190-3198`;
  `internal/wal/checkpointer.go:440`, `:447-494`, `:736-772`;
  `internal/wal/recovery.go:724-798`, `:4408-4464`;
  `internal/catalog/relcache_inval.go:14-33`; `internal/multixact/store.go:82`, `:88`;
  `internal/server/server.go:677`; `internal/server/basebackup.go:223`, `:225`;
  `cmd/goopg/main.go:560`, `:701-705`, `:719-721`;
  `internal/config/defaults.go:348`, `:360`
- upstream: `postgres/src/include/catalog/pg_control.h:35-65`;
  `postgres/src/include/c.h:631`, `:633`;
  `postgres/src/common/controldata_utils.c:189`, `:222`, `:237`, `:255-263`;
  `postgres/src/backend/access/transam/xlogrecovery.c:880-887`, `:924-936`;
  `postgres/src/backend/access/transam/xlog.c:4363-4376`, `:5633`, `:5821`,
  `:5833`, `:5835`, `:5886-5890`, `:6204-6211`
