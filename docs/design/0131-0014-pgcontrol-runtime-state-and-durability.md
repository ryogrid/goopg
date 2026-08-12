# pg_control runtime state and durability — stamp `DB_IN_PRODUCTION`, make the writer crash-safe, start from pg_control

**Status:** S17 **accepted — landed 2026-08-11**; S18.1 + S18.2 **accepted —
landed 2026-08-11**; S18.3 + S18.4 **accepted — landed 2026-08-11** (S18 now
complete); S20.1 + S20.2 **accepted — landed 2026-08-11**; S20.3 + S20.4 + S20.5
**accepted — landed 2026-08-11** (S20 now complete); S29 still draft
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

## Findings — S18.3 + S18.4 as built (2026-08-11)

Landed as designed, with three deviations worth recording.

1. **The record and pg_control now come from ONE sample, not two.** The design
   treats S18.3 (pg_control TLI) and S18.4 (record constants) as separate
   slices, and the code had them in separate places — `EncodeCheckpointPG`'s
   literal `1` and `cd.CheckPointCopyThisTLI = 1`, forty lines apart in
   `runCheckpoint`. Implementing them separately would have left two
   independently-driftable copies of the same state, and PG cross-checks the
   two at startup. So `runCheckpoint` samples once into a `CheckPointFields`
   before encoding and the pg_control writer reads that struct rather than
   re-deriving anything. This is why the new tests assert both halves against
   the same expected values.
2. **`CheckPointFields` replaces the positional signature, and defaults are
   PG's — including two corrections.** `encodeCheckPointStruct` had five
   positional parameters and eight literals; with the two never-written members
   added it would have taken fifteen. It now takes one struct with a
   `withDefaults()` that mirrors upstream's bootstrap floors. Two of those
   defaults are **not** what goopg used to write, and the change is deliberate:
   `oldestCommitTsXid`/`newestCommitTsXid` were hardcoded `3`, but PG writes
   `InvalidTransactionId` (0) while `track_commit_timestamp` is off — the
   reference cluster's `pg_controldata` confirms `0` — and `oldestXidDB`/
   `oldestMultiDB` were left `0`, where upstream's bootstrap names
   `Template1DbOid` (1). Neither had a defender; both are now pinned to the
   oracle by `TestCheckPointFieldsDefaultsMatchPG`.
3. **`full_page_writes` is sampled from the buffer pool, not the GUC registry.**
   Upstream reads `Insert->fullPageWrites` under the WAL insert lock
   (`xlog.c:7041`), not the `postgresql.conf` text, because a runtime change
   only takes effect at the next `XLOG_FPW_CHANGE`. goopg's equivalent of that
   shadow copy is `Pool.fullPageWrites` (where `cmd/goopg/main.go` lands the
   GUC), so `FullPageWritesFn` is wired to `Pool.FullPageWrites`.

Two smaller notes. `UpdateControlCheckpoint` took the TLI as a new parameter
rather than reading it internally, so BASE_BACKUP passes the same
`LoadOrCreateTimelineID` value its `START_TIMELINE` reply carries — the call
moved *after* the timeline load for that reason; `MinRecoveryPointTLI` moved
off its own hardcoded `1` at the same time, since a mismatch there is the exact
`xlogrecovery.c:878-886` FATAL that S29 is filed for. And the multixact hook is
a **setter** (`SetNextMultiXactFn`), not a `CheckpointerConfig` field like the
other two, because the process-shared multixact store is created in
`cmd/goopg/main.go` long after `initdb.Open` has built the checkpointer.

Guards, all proven fail-when-broken by scripted revert over a `/tmp` backup —
eight break directions, each caught by a different assertion:
`TestEncodeCheckPointStructSettable`, `TestCheckPointFieldsDefaultsMatchPG`,
`TestCheckpointerStampsLiveTimelineAndFPW` (`internal/wal/`), and
`TestCheckpointerWritesLiveTimelineToPgControl` (`internal/initdb/`, which runs
a real checkpointer against a real `initdb` directory and cross-checks the
result with the real `pg_controldata`, including the text-valued
`full_page_writes: off` line no numeric helper covers). The settability
discipline from the S18.1/S18.2 findings above carried over unchanged and
earned its keep immediately: dropping the new `PrevTimeLineID` line is
invisible to any round-trip, and only the seed-then-read-back assertion catches
it.

**Still deferred after this slice:** `oldestXid` remains the `3` floor rather
than a real datfrozenxid horizon (goopg computes one for CLOG truncation but it
is not plumbed here), `oldestMulti` is pinned to `FirstMultiXactId` because
goopg's multixact store never truncates, and the multixact counter is still
in-memory-only — S24 owns its durability. Ledgered.

## Findings — S20.1 + S20.2 as built (2026-08-11)

The design held, with **one correction to its own S20.1 wording** and **one
subtask dropped as wrong**.

**Correction — `DB_SHUTDOWNED_IN_RECOVERY` is not clean.** The plan (and this
doc's §S20.1) said "neither `DBStateShutdowned` nor
`DBStateShutdownedInRecovery`". Upstream's test is a single equality:
`else if (ControlFile->state != DB_SHUTDOWNED) InRecovery = true`
(`xlogrecovery.c:931`). A standby shut down tidily still has to replay its way
back to consistency, so the two-value set would have skipped recovery on
exactly the cluster that needs it. `beginRecovery`
(`internal/initdb/recovery_state.go`) implements upstream's test, and a subtest
pins the distinction.

**Second arm implemented too.** `redo < CheckPoint` (the checkpoint record's
own location) is the signature of an ONLINE checkpoint, i.e. a cluster that
never ran its shutdown checkpoint — upstream forces recovery on it
(`xlogrecovery.c:924-929`) regardless of the state byte. goopg can evaluate it
because S18 made both fields real. Verified it does *not* misfire on a clean
goopg restart: `runCheckpoint` samples a shutdown checkpoint's redo at the WAL
frontier where the record is then appended and nothing may be written in
between, so `redo == CheckPoint` exactly. Where upstream PANICs on this arm
with `state == DB_SHUTDOWNED` ("invalid redo record in shutdown checkpoint"),
goopg logs and recovers: refusing to start is the worse outcome for a directory
goopg may not have written, and replaying from an online checkpoint's redo is
correct, only slower.

**S20.2's "teach `isCheckpointRecord` about `XLOG_CHECKPOINT_REDO` (0xE0)" is
dropped as a mis-specification.** `XLOG_CHECKPOINT_REDO` is not a checkpoint
record: it carries a 4-byte `wal_level`, not an 88-byte `CheckPoint` struct, and
`isCheckpointRecord` is consumed by `checkpointStructOf` and by
`DiscoverLastCheckpointLSN`, both of which want the struct. Classifying the
marker as a checkpoint would have made `DiscoverLastCheckpointLSN` return a
location with no checkpoint at it. It is also unnecessary: the marker's whole
purpose is to *be* the redo point of the online checkpoint that follows, and
that address is precisely what `checkPointCopy.redo` now supplies directly.
Recorded here rather than silently skipped.

**Where the pointer wins.** `replayStartAt` (`internal/wal/recovery.go`) takes
the anchor from `pg_control` alone when it has one; the scan survives for
`redo == 0` (fresh cluster, hand-assembled directory with no control file, the
standalone `ReplayFromDir` entry point) and still supplies the *reported*
`CheckpointLSN`, which is bookkeeping rather than an anchor. Trusting the
pointer is safe in the one direction it can be stale: goopg appends the
checkpoint record before updating pg_control, so a crash between the two leaves
an *older* redo, which replays a superset — idempotent via pd_lsn guards and
terminal-state CLOG stamps.

**End-of-recovery checkpoint.** `Open` forces one (`CheckpointNow`) after a
crash-recovery replay, mirroring `CHECKPOINT_END_OF_RECOVERY`. Without it the
recovered state is undurable until the first scheduled checkpoint — one
`checkpoint_timeout` (300 s) away — and a second crash replays the same span.
It costs a clean start nothing, which is why the S17 guard's
`LastCheckpointLSN() == 0` assertion is also a guard on *this*: misclassifying
a normal boot as a crash would fire a checkpoint on every start and break it.

**Discovery — crash recovery loses and duplicates rows, and it is NOT this
slice.** The in-situ smoke (scale-5 pgbench, 18 459 txns, SIGKILL, restart) came
back with `500000 → 499949` rows, and a `generate_series` anti-join found **218
missing `aid`s against only 64 net missing rows — i.e. ~154 duplicated ones**.
Reproduced identically on a worktree build of the parent commit `15e73de3`
(`500000 → 499936`), so it is pre-existing, not a regression from the redo
pointer. The shape matches the known non-atomic non-HOT update path
(`updateOp.Next()` emits `HeapDelete` + `HeapInsert` as two separate records —
ledger M0118-0129 / M0130-S7.2): a crash between them leaves either both
versions or neither. Ledgered and filed as its own fix_plan item; the
crash-recovery smoke is the repro.

**Still deferred after this slice:** S20.3 (unconditional pre-replay
`pg_internal.init` sweep), S20.4 (`NewStoreAt` seeding from `nextMulti`) and
S20.5 (write the `minRecoveryPoint` policy down) are untouched — S20 stays
unchecked.

## Findings — S20.3 + S20.4 + S20.5 as built (2026-08-11)

S20 closes here. All three landed as designed; the interesting part is a guard
that was **green while proving nothing**, and what fixing it revealed about
which init file the sweep can actually be observed on.

**S20.3 — the sweep.** `catalog.RelcacheInitFileRemoveAll(dataDir)` mirrors
`RelationCacheInitFileRemove` plus its `RelationCacheInitFileRemoveInDir`
helper: `global/pg_internal.init`, then every all-digit entry of `base/`, then
every all-digit entry of `pg_tblspc/` descended through
`config.TablespaceVersionDirectory`. `clearRelcacheInitFiles`
(`internal/initdb/recovery_state.go`) calls it under the existing
`WithRelCacheInitLock` and **before** replay, from `Open`. Failure warns and
continues — upstream passes `elevel = LOG` to `unlink_initfile` from this call
site, deliberately not `ERROR`. The all-digit test is upstream's
`strspn(d_name, "0123456789") == strlen(d_name)`, and it is load-bearing:
without it the sweep descends into `pgsql_tmp` and treats any future
non-database directory as a database.

**The false guard, and the fix.** The obvious assertion — plant a foreign
`global/pg_internal.init`, `Open`, assert it is gone — **passes with the sweep
deleted outright**, and also passes with the sweep made conditional on
`crashRecovery`. Both break directions were scripted and both stayed green.
The reason: `Open` *regenerates* `global/`, `base/1/` and `base/5/` after
replay (`relcache_init.go:31-47`, called from `open.go:1690/1844`), so those
three files are overwritten whether or not anything swept them. The guard has
to plant in a database directory goopg does **not** regenerate for —
`base/16384` — which is not a testing detail but the actual gap S20.3 closes:
the pre-existing reactive path takes ONE `dboid`, so nothing ever reached a
database the running session had not itself invalidated. Retargeted, both
break directions fail as they should.

**S20.4 — the seed.** `beginRecovery` now also returns
`checkPointCopy.nextMulti`, `Open` publishes it as `Runtime.NextMultiXact`, and
`cmd/goopg/main.go` builds the process-shared store with
`multixact.NewStoreAt(...)` instead of `NewStore()`. The split is deliberate:
the store is process-shared and constructed after `Open` returns, so `initdb`
publishes the value and `cmd` applies it. `NewStoreAt` clamps anything below
`FirstMultiXactId`, so the no-control-file case (0) degrades exactly to the old
behaviour. What this fixes is not a hypothetical: MultiXactIds are stamped into
tuple `xmax` fields, which are **on disk and outlive the process**, so an
allocator that rewinds to 1 on every restart re-issues ids that existing tuples
already carry. Note the asymmetry with S18.4, which made the counter *visible*
to a checkpoint — this makes it *survive* one. Membership is still in-memory
and transient (the SLRU is S24), so a seeded id resolves to an *unknown* member
set rather than a wrong one, which is the same answer upstream gives for a
truncated-away multixact.

**S20.5 — the policy, as an executable assertion.** Upstream's rule is one line
and one comment in `CreateCheckPoint`: `/* crash recovery should always recover
to the end of WAL */`, then `minRecoveryPoint = InvalidXLogRecPtr` and
`minRecoveryPointTLI = 0`, unconditionally, on every checkpoint a
non-recovering cluster writes (`xlog.c:7295-7297`). The reader half agrees:
`InitWalRecovery` adopts the control file's value only when `InArchiveRecovery`
and uses `InvalidXLogRecPtr` otherwise (`xlog.c:5778-5794`), with the comment
explaining why — a stale location makes the startup process declare consistency
early and then complain about invalid page references. goopg's checkpointer
already writes `0`/`0` (`internal/wal/checkpointer.go`), and `beginRecovery`
writes only `State`, so the policy already held; what it lacked was anything
that would notice it being broken. `TestCrashRecoveryLeavesMinRecoveryPointInvalid`
poisons **both** fields (`0xDEAD` / tli 9) before the crash-shaped `Open`, so a
pass requires the end-of-recovery checkpoint to actively clear them rather than
merely never having written them. Deleting the checkpointer's two clearing
lines fails it.

The one deliberate violator remains **S29**: `UpdateControlCheckpoint` writes
`MinRecoveryPoint = 1` so a PG standby restoring a BASE_BACKUP passes
`XLogRecPtrIsInvalid()` in `CheckRecoveryConsistency`. Confirmed this loop that
`internal/server/basebackup.go:232` is its **only** caller, so the divergence is
confined to the backup lane — but that call mutates the **live primary's**
control file, which upstream's `basebackup.c` never does (it sends a modified
*copy* in the stream). So a crash in the BASE_BACKUP → next-checkpoint window
leaves the primary advertising a minimum recovery point it invented. Ledgered
against S29 rather than fixed here.

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

### S29 — BASE_BACKUP mutates the source pg_control — **LANDED 2026-08-12**

As built, in three edits:

- `control.BuildUpdatedControlImage(dataDir, fn)`
  (`internal/control/pgcontrol.go`) is `UpdateControlFile`'s
  read → decode → mutate → encode → CRC half **without the write**, returning
  the 8192-byte image. `UpdateControlFile` is now a two-liner over it, so the
  two paths cannot drift in what they encode or how they checksum.
- `initdb.UpdateControlCheckpoint` **becomes** `initdb.BackupControlImage`
  (same field mutations, returns the image). Renaming rather than adding an
  alternative is deliberate: no caller can reach the mutating variant again,
  because it no longer exists.
- `emitBaseBackupTar` takes a trailing `pgControlImage []byte`. Non-nil ships in
  place of the on-disk bytes at the pg_control-last step; nil keeps the historical
  read-the-file behaviour, which is what happens when there is no checkpoint REDO
  yet (`redoLSN == 0`) or the control file cannot be read — the pre-S29 code
  discarded that error too, and an unpatched backup beats a failed one.

The manifest entry records the shipped bytes, not the on-disk ones, because
`record()` is fed the same buffer that was written to the tar.

**Measured while landing.** The gate's fixture had to seed
`checkPointCopy.ThisTimeLineID` explicitly: `LoadOrCreateTimelineID` resolves the
timeline from **pg_control**, not from the `timeline_id` file the fixture also
writes, so the 0xC0-filled stub yielded TLI `0xC0C0C0C0` in both the shipped
image and the START_TIMELINE reply. Anything asserting a timeline against this
fixture needs the same seed.

Original diagnosis (kept for the record):
`internal/server/basebackup.go:223` called `initdb.UpdateControlCheckpoint` on the
**live** directory, writing `MinRecoveryPoint = 1`
(`internal/initdb/pgcontrol.go:158`), `MinRecoveryPointTLI = 1` (`:159`) and
`BackupEndPoint = redo` (`:162`) — plus S18.3's hardcoded TLI 1 — into the running
cluster's control file. On a promoted (TLI ≥ 2) cluster, a crash in the
BASE_BACKUP → next-checkpoint window then makes PG FATAL `requested timeline %u
does not contain minimum recovery point %X/%X on timeline %u`
(`xlogrecovery.c:880-887` — *Theme F cites `:878-886`; the test is `:880-882`, the
FATAL text `:884`*). Fix: build the backup's control image into the tar stream
rather than mutating the source.

**S29 guard.** `TestBaseBackupDoesNotMutateLiveControlFile`
(`internal/server/basebackup_control_test.go`) drives a real BASE_BACKUP over the
replication protocol and asserts **both halves**, because either alone is
passable by accident:

1. the live `global/pg_control` is byte-identical across the backup (the fixture
   pre-zeroes `minRecoveryPoint`, `minRecoveryPointTLI`, `backupEndPoint`,
   `checkPoint`, so a non-zero reading afterwards can only come from the backup);
2. the **shipped** image still differs from it, re-verifies its CRC when read
   back through `control.ReadControlFile`, and carries
   `minRecoveryPoint != 0` on the live TLI — otherwise half 1 would pass simply
   by patching nothing.

Proven fail-when-broken in both directions: re-adding an `os.WriteFile` of the
image onto the live directory fails half 1 (`live minRecoveryPoint=1 tli=1
backupEndPoint=8191`), and running with no checkpoint REDO (so no patch is built)
fails half 2 (`shipped pg_control is byte-identical to the live one`).

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
7. **S20.3 — corrected as built.** The plan's "plant in `global/` and two
   `base/<n>/` dirs; all three are gone after `Open()`" **cannot detect the
   bug**: `Open` regenerates `global/`, `base/1/` and `base/5/` after replay, so
   that assertion passes with the sweep deleted (verified by scripted revert).
   The guard plants in `base/16384/`, a database goopg does not regenerate for.
   `TestOpenRemovesPreexistingRelcacheInitFile` (`internal/initdb/`) does this on
   a **clean** start, pinning that the sweep is unconditional;
   `TestClearRelcacheInitFilesSweepsWholeCluster` covers `global/`, three
   `base/<n>/`, a `pg_tblspc/<oid>/<verdir>/<n>/`, and the three non-numeric
   entries that must survive.
8. **S20.4.** Seed `nextMulti = 4242`; `Runtime.NextMultiXact == 4242`
   (`TestOpenSeedsMultiXactFromPgControl`), and a directory with no pg_control
   yields 0, which `NewStoreAt` clamps
   (`TestOpenWithoutControlFileLeavesMultiXactUnseeded`).
9. **S20.5.** Poison `minRecoveryPoint`/`TLI` to `0xDEAD`/`9`, crash-shape the
   control file, `Open`: both are back to 0 afterwards
   (`TestCrashRecoveryLeavesMinRecoveryPointInvalid`). Poisoning is what makes
   the guard mean "actively cleared" rather than "never written".
10. UNITS + SMOKE green —
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
