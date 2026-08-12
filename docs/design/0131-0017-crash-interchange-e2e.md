# Crash-interchange E2Es — each engine recovers the other's SIGKILLed cluster directory

**Status:** draft
**Date:** 2026-08-11
**Milestone:** M0131 (Theme F, S27 + S28)

## Problem

S3 and S4 both scope their handover to a **cleanly shut down** source directory,
and both assert `pg_control.State == DB_SHUTDOWNED` before handing over
(`internal/testport/e2e_goopg_coldstart_on_pgdata_test.go:137-140`;
`e2e_pg_coldstart_on_goopgdata_test.go:176-179`). That is the precondition
`0130-0002` §"WAL replay constraint" states outright. The milestone's headline
claim — two interchangeable engines on one directory — is therefore half-true:
the interchange works only when the previous engine exited politely.

S27 and S28 are the two tests that remove the precondition. They are also the
only place where Theme F's implementation slices are proven *together*: S27 is
worthless without S17 (without the `DB_IN_PRODUCTION` stamp, PG never enters
recovery and the test proves nothing), and S28 needs S21a at minimum.

## Design

### S27 — forward: `TestE2E_PGCrashStartOnGoopgDataDir`

New file `internal/testport/e2e_pg_crashstart_on_goopgdata_test.go`, sibling of
S4's. Gated identically: `testing.Short() || os.Getenv("GOOPG_SKIP_M0131_E2E")`
plus `pgcluster.Available(t, <repo>/postgres/local_install/bin)`
(`e2e_pg_coldstart_on_goopgdata_test.go:34-42`). Eight steps:

1. `cluster.New` + `Init()` + `Start()` (`internal/testutil/cluster/cluster.go:95`,
   `:172`, `:214`).
2. Workload with a **known committed/uncommitted split**. The committed half via
   `runSQLSimple` (`e2e_scenarios_test.go:109`); the uncommitted half needs a
   session that stays open across the kill — `StartPSQL` (`cluster.go:471`) with
   stdin holding `BEGIN; INSERT …;` and no COMMIT, left running.
3. One online checkpoint: `g.Checkpoint()` (`cluster.go:356`), which shells out to
   `goopg checkpoint -D <dir>` — a real subcommand, registered at
   `cmd/goopg/main.go:71` (`{"checkpoint", runCheckpoint}`), implemented at
   `:1341`. *(Theme F cites `main.go:1340`; that is the last line of the doc
   comment.)* This makes `state` `DB_IN_PRODUCTION` **and** puts a real tail past
   redo — both are load-bearing.
4. More committed work after the checkpoint.
5. `g.Kill()` (`cluster.go:303-333`). **Correction to the Theme F text, which says
   "kill -9 by PID file":** the helper already exists and does not read the PID
   file — it resolves `syscall.Getpgid(c.cmd.Process.Pid)` and sends
   `syscall.Kill(-pgid, syscall.SIGKILL)` (`:317-322`), killing the `go run`
   wrapper's whole process group. It satisfies the constraint the PID-file route
   was reaching for — **never `pkill -f goopg`**, which self-matches the invoking
   shell and exits 144. Use `g.Kill()`; add nothing.
6. Assert `control.ReadControlFile(dir).State == control.DBStateInProduction`.
   Before S17 this fails, and that failure *is* the S17 bug — the assertion is the
   S17 regression lock, not a setup step.
7. `pgcluster.OpenExisting` + `Start()` + `WaitReady` (`pgcluster/cluster.go:120`,
   `:236`, `:455`), `User: "postgres"`. `OpenExisting` runs neither `initdb` nor
   `appendConf`, and `Start` puts `-D`/`-p`/`-h` on argv, so goopg's
   `postgresql.conf` is handed over untouched — the S4 property, reused.
8. Assertions. The PG log (`pgLogPathFor`, `:333`; truncated by `pgcluster.Start`)
   contains `redo starts at` and `redo done at`, contains **no** PANIC/FATAL, and
   any `invalid record length` / `invalid magic number` line is the *last* WAL
   complaint (benign end-of-WAL). Then every committed row is visible, the
   uncommitted ones are not, and aggregates match values captured from goopg
   before the kill via `coldStartScalar`
   (`e2e_goopg_coldstart_on_pgdata_test.go:325`), read back with `pgQueryColumn`
   (`e2e_pg_coldstart_on_goopgdata_test.go:696`).

#### Stale `postmaster.pid`

A SIGKILL leaves it: `control.WritePIDFile` runs from `startControlPlane`
(`internal/server/server.go:677`) and `control.RemovePIDFile` only from
`stopControlPlane` (`:808-815`), i.e. only on the clean path. The file is
`PIDFile.Marshal` (`internal/control/control.go:61-69`) — **five** newline-
terminated lines, `pid / dataDir / StartedAt.UnixMilli() / listenAddr /
socketPath`. *(Theme F cites `control.go:59-68`; the function is `:61-69` and the
five `Fprintf`s are `:63-67`.)*

PG's `CreateLockFile` (`postgres/src/backend/utils/init/miscinit.c:1210`, reached
from `CreateDataDirLockFile`, `:1517`) then:

- tries `open(O_RDWR|O_CREAT|O_EXCL)` (`:1275`); on `EEXIST` re-opens and reads,
  taking `encoded_pid = atoi(buffer)` — goopg's line 1 parses correctly;
- if `kill(other_pid, 0) == 0` (or fails with anything but ESRCH/EPERM), **FATAL
  `lock file "…" already exists`** (`:1354-1372`). The failure is therefore
  **non-deterministic**: it fires exactly when the dead goopg PID has been
  recycled by another live process;
- otherwise, for the data-directory lock, it walks to line
  `LOCK_FILE_LINE_SHMEM_KEY` = 7 (`src/include/utils/pidfile.h:43`) and
  `PGSharedMemoryIsInUse`-checks it (`:1387-1412`). goopg's file has five lines,
  so `strchr(ptr, '\n')` returns NULL at line 6 and **the shmem check is skipped
  entirely**; then `unlink` and retry (`:1415-1419`).

Until goopg writes a PG-shaped 8-line file or removes a stale one at start, the
test must delete `postmaster.pid` as an explicit, commented handover step — and
assert it was *present* first, so the day goopg starts cleaning it up the test
demands the step be removed rather than silently passing.

S28 does **not** inherit this. goopg has no `CreateLockFile` equivalent:
`WritePIDFile` writes a temp file and renames over whatever is there
(`internal/control/control.go:104-118`), and no `goopg start` path calls
`ParsePIDFile` (its callers are stop/status/reload/checkpoint,
`cmd/goopg/main.go:1177`–`:1426`). A stale PG-authored `postmaster.pid` is
silently overwritten.

#### Torn contrecord

Kill during a large `INSERT … SELECT` so the tail ends inside a multi-page
record. goopg does emit contrecords — `XLPFirstIsContRecord` is stamped at
`internal/wal/xlog_emit.go:129` and honoured by the reader at
`internal/wal/reader.go:119` — so the shape is reachable.

Upstream path, verified end to end: `XLogReadRecord` reports `there is no
contrecord flag at %X/%X` (`postgres/src/backend/access/transam/xlogreader.c:760`)
or `invalid contrecord length %u (expected %lld) at %X/%X` (`:773`), and sets
`state->abortedRecPtr` / `state->missingContrecPtr` (`:939-940`). `ReadRecord`
copies them into the recovery globals **only when `!ArchiveRecoveryRequested`**
(`xlogrecovery.c:3188-3193`) — the gate that makes this path **crash-only and
structurally unreachable from the standby lane**. `StartupXLOG` then rewinds
`EndOfLog = missingContrecPtr` (`xlog.c:6039-6050`) and, once writes are allowed,
emits the marker via `CreateOverwriteContrecordRecord` (`:6153-6156`, function
`:7480`, inserted as the first record on the page at `:7521`); replaying it logs
`successfully skipped missing contrecord at %X/%X, overwritten at %s`
(`xlogrecovery.c:2115`). Assert the "no contrecord flag" (or "invalid contrecord
length") line, then the "successfully skipped" line, in that order.

#### Honesty note — this test is NOT torn-page coverage

A `kill -9` does **not** exercise torn *pages*. The process dies but its writes
have already reached the page cache, and from any subsequent reader's
perspective they are atomic; the kernel is still alive to serve them. Torn-page
coverage requires a machine-level crash (power loss / VM reset) or a deliberate
half-page-write fault injector. **This test must never be cited as FPI or
`full_page_writes` coverage**, and the file carries that sentence as a comment
so the claim cannot drift.

#### S27 MAIN VARIANT LANDED 2026-08-12 — and it found two defects

`internal/testport/e2e_pg_crashstart_on_goopgdata_test.go` runs green: the
committed/uncommitted split, the online checkpoint plus a post-checkpoint tail,
`cluster.Kill()`, the `DB_IN_PRODUCTION` assertion, the stale-`postmaster.pid`
handover (asserted PRESENT, then removed, so a future goopg-side cleanup fails
this test instead of silently passing), the `redo starts at` / `redo done at` /
no-PANIC / benign-end-of-WAL log contract, and row equality against goopg's own
pre-crash answers.

Two production defects surfaced, both fixed in the same commit — the reason the
test is worth its runtime:

1. **A ranged `UPDATE` on an indexed column panicked the backend.**
   `updateOp.Next` routed to `updateViaIndex` for ANY child `*planner.IndexScan`,
   but that helper probes one equality key (`evalExpr(ix.Key, …)`). A range
   predicate (`WHERE id BETWEEN 600 AND 650`) plans as an IndexScan with
   `Key == nil` and `LowKey`/`HighKey` set — as does a composite `Keys` probe —
   so the deref crashed the connection goroutine (`invalid memory address or nil
   pointer dereference` in `evalExprSlot`; the client saw `driver: bad
   connection`). Fix: the fast path now requires `Key != nil` and every other
   predicate shape falls through to the SeqScan path, which handles all of them.
   Guard: `internal/executor/update_range_index_scan_test.go`, proven
   fail-when-broken (the panic returns verbatim when the guard is removed).
   Teaching `updateViaIndex` to drive a range/composite probe is a performance
   follow-up, ledgered.

2. **goopg's `xl_heap_prune` omitted `XLHP_CLEANUP_LOCK`, and an
   assert-enabled PG aborted replaying it.** Upstream derives
   `lp_truncate_only = (flags & XLHP_CLEANUP_LOCK) == 0`
   (`heapam_xlog.c:106`), which claims every now-unused slot was ALREADY
   `LP_DEAD` and carries no storage; goopg's prune reclaims slots that still
   have storage and redirects chain roots. PG 18.3 TRAPped in the startup
   process — `failed Assert("ItemIdIsDead(lp) && !ItemIdHasStorage(lp)")`,
   `pruneheap.c:1677` — and the postmaster shut down, so the cluster was
   **unstartable**. (A second assert, `heapam_xlog.c:50`, independently forbids
   redirections without the flag.) An assert-disabled build would have applied a
   truncate-only prune to live-storage slots instead: silent divergence. Fix:
   `EncodeHeapPruneOptPG` and `EncodeHeapFreezePG` both set
   `XLHP_CLEANUP_LOCK`, which is the honest description of goopg's prune — it
   runs under the page's exclusive buffer lock. This is a **standby-lane blind
   spot**: the flag only matters when PG replays a prune record it did not
   write, and `TestE2E_PGStandbyFullCycle` never produced one.

Still deferred out of S27 (ledger, 2026-08-12): the **torn-contrecord variant**
described below — it needs a kill timed against WAL page boundaries, not just a
kill during a large statement.

### S28 — reverse: `TestE2E_GoopgCrashStartOnPGDataDir`

The mirror, in `internal/testport/e2e_goopg_crashstart_on_pgdata_test.go`:
`pgcluster.New` → `Start` → workload → capture answers with `pg.QueryScalar` →
crash → `cluster.New(… DataDir: pgDir …)` and `Start()` **without `Init()`**
(the S3 idiom, `e2e_goopg_coldstart_on_pgdata_test.go:165-182`, which is what
keeps PG's `postgresql.conf` untouched) → compare with `coldStartScalar`.

**Harness gap, fix first.** `pgcluster.Kill()`
(`internal/testutil/pgcluster/cluster.go:325-336`) is **not** a SIGKILL — it runs
`pg_ctl -D … -m immediate -w stop`, i.e. SIGQUIT. That is crash-*equivalent* for
WAL purposes (no shutdown checkpoint, `state` stays `DB_IN_PRODUCTION`), but the
postmaster still exits through `ExitPostmaster` → `proc_exit`
(`postgres/src/backend/postmaster/postmaster.c:3171`, `:3680`) → the
`on_proc_exit(UnlinkLockFiles)` registered at `miscinit.c:1495`, so it **removes
`postmaster.pid` and is a clean process exit**. S28 needs a true
`syscall.Kill(-pgid, SIGKILL)` helper alongside it, mirroring `cluster.Kill()`;
`pgcluster.Start` must `Setpgid` the postmaster for that to be safe.

**S28.0 — LANDED (2026-08-12).** `pgcluster.Start` now sets
`SysProcAttr{Setpgid: true}` and `pgcluster.KillHard()` group-kills with
`syscall.Kill(-pgid, SIGKILL)`, reaps, and marks the cluster stopped;
`Stop()`'s 20 s escape hatch was upgraded from `Process.Kill()` to the same
group kill so a `PM_RECOVERY` postmaster cannot leave backends behind.
`Kill()` keeps its `pg_ctl -m immediate` meaning and its doc comment now says
so. `internal/testutil/pgcluster/kill_hard_test.go` is the paired probe: the
`killhard` subtest asserts `postmaster.pid` survives, that the pinned backend
PID (`pg_backend_pid()`) dies with the group, and that PG replays its own WAL
with all 500 committed rows intact; the `pg_ctl_immediate` subtest asserts the
lock file is *removed*, so an upstream behaviour change that made `Kill()`
sufficient would show up as a failure rather than as silently redundant code.

Two facts S28 must build on, both found while landing S28.0:

- After a real SIGKILL the directory carries a stale `postmaster.pid`, and the
  *restarting* engine has to deal with it. The test removes it before
  restarting PG. goopg does not need that step — and that is itself the gap:
  `internal/server/server.go:677` calls `control.WritePIDFile` unconditionally,
  with no equivalent of upstream's `CreateLockFile` stale-lock check
  (`miscinit.c`), so goopg silently overwrites a *live* peer's lock file
  instead of refusing to start. Ledger row filed; S28 asserts goopg starts on
  the crashed directory and must not be read as blessing that behaviour.
- `Setpgid` moves the postmaster out of the test binary's process group, so a
  hard-killed `go test` no longer takes its PG clusters down with it. Teardown
  (`Stop()`/`t.Cleanup`) is now the only reaper — the same trade-off
  `internal/testutil/cluster` already makes for goopg servers.

The workload exercises precisely the S21/S22 opcode set. Every element maps to
one opcode, and the table is the test's specification:

| workload element | rmgr (rmid) | opcode | value |
|---|---|---|---|
| `COPY t FROM …` | Heap2 (9) | `XLOG_HEAP2_MULTI_INSERT` | 0x50 |
| `VACUUM t` | Heap2 (9) | `XLOG_HEAP2_VISIBLE` | 0x40 |
| `SELECT … FOR UPDATE` | Heap (10) | `XLOG_HEAP_LOCK` | 0x60 |
| `BEGIN; CREATE TABLE t2; …; TRUNCATE t2; COMMIT` | Storage (2) | `XLOG_SMGR_TRUNCATE` | 0x20 |
| ~~`TRUNCATE t2`~~ | ~~Heap (10)~~ | ~~`XLOG_HEAP_TRUNCATE`~~ | ~~0x30~~ — **struck, see correction below** |
| `SAVEPOINT s1; …` | Transaction (1) | `XLOG_XACT_ASSIGNMENT` | 0x50 |
| …its `COMMIT` | Transaction (1) | `XLOG_XACT_COMMIT` + `subxacts[]` | 0x00 |
| index-heavy INSERT | Btree (11) | `INSERT_LEAF` / `_UPPER` / `_META` / `_POST` / `SPLIT_L`/`_R` / `DEDUP` | 0x00/0x10/0x20/0x50/0x30/0x40/0x60 |

Values from `postgres/src/include/access/heapam_xlog.h:36`, `:39`, `:63-64`;
`…/catalog/storage_xlog.h:31`; `…/access/xact.h:169`, `:174`;
`…/access/nbtxlog.h:27-43`. rmids are `PG_RMGR` ordinal positions in
`…/access/rmgrlist.h:28-49`. `CLOG_ZEROPAGE`
(`…/access/clog.h:55`, value 0x00 — *Theme F writes `XLOG_CLOG_ZEROPAGE`; the
upstream macro is `CLOG_ZEROPAGE`*) is deliberately **out** of the workload: it
needs 32768 XIDs to fire, which is a load test, not an E2E. Cover it with an
S21a unit test over a captured record instead.

**Opcode-table correction (2026-08-12, found by running it).** Guard 7 — dump the
crash tail with `pg_waldump` and require every table row to be present — earned
its keep on the first run by failing on two rows the table got wrong:

- **`TRUNCATE t2` emits neither truncate record** when `t2` pre-exists.
  `ExecuteTruncateGuts` (`postgres/src/backend/commands/tablecmds.c:2200`) only
  truncates in place — `heap_truncate_one_rel` → `RelationTruncate` →
  `XLOG_SMGR_TRUNCATE` — when `rel->rd_createSubid == mySubid`. Otherwise it takes
  the transaction-safe path, `RelationSetNewRelfilenumber`, which emits a
  Storage/CREATE for the new relfilenode and no truncate record at all. The
  workload therefore wraps `CREATE TABLE` + `INSERT` + `TRUNCATE` in one
  transaction.
- **`XLOG_HEAP_TRUNCATE` is unreachable at `wal_level = replica`** and is dropped
  from the required set. It is emitted only under `relids_logged != NIL`, guarded
  by `Assert(XLogLogicalInfoActive())` (`tablecmds.c:2303`), and upstream's own
  `heap_redo` treats it as a no-op: *"TRUNCATE is a no-op because the actions are
  already logged as SMGR WAL records. TRUNCATE WAL record only exists for logical
  decoding"* (`postgres/src/backend/access/heap/heapam_xlog.c:1201-1207`).
  Requiring it would force this crash-recovery test onto an unrepresentative
  `wal_level = logical` cluster to cover a record that changes nothing on replay.
  goopg's `RmgrHeap` arm has no case for it and refuses — a real gap, but one
  that belongs to S21 and a ledger row, not to this test's guard.

**S28 — LANDED (2026-08-12), self-arming against S21.** `internal/testport/
e2e_goopg_crashstart_on_pgdata_test.go` runs the PG-side workload, captures
twelve answers, `KillHard()`s, asserts `postmaster.pid` survives and
`pg_control.State != DB_SHUTDOWNED`, and clears guard 7 — all green today.
goopg then refuses the tail at `wal: unsupported xlog record rmid=0 info=0x30`,
i.e. **`XLOG_NEXTOID`**, which every PG cluster emits routinely: S21 is
unchecked, and the design said from the start that S28 needs S21a at minimum.

Rather than land a red test that re-states a known-open task on every nightly,
the goopg half is a **self-arming skip**: `unsupportedXLogOpcode` recognises
*that specific refusal* in the server log and skips with the opcode in the
message. Any other startup failure — checksum, decode error, panic — stays
fatal, and the moment S21/S22/S23 close the gap the start succeeds and the
assertions run with nobody having to remember to un-skip anything. The
`_Concurrent` variant ships alongside as the S24 re-arm trigger, `t.Skip`ped
with the rmid-6 reason in its message (guard 9).

Two assertions beyond row equality: the SAVEPOINT rows must be **visible**
(S22's `subxacts[]` gap makes committed subtransactions invisible, and
`internal/initdb/xact_recovery.go:87-92` stamps an `ASSIGNMENT` record ABORTED),
and the `FOR UPDATE` row must be readable and updatable (a mis-replayed
`XLOG_HEAP_LOCK` leaves a bogus xmax).

**Second variant — the GIN refusal.** Same shape, but `CREATE INDEX … USING gin`
before the workload. Assert goopg **refuses**: `g.Start()` returns an error, the
goopg log names the access method and the LSN (S25's specific message, not
`wal: unsupported xlog record rmid=13 info=0x…` from
`unsupportedDecodedXLogRecord`, `internal/wal/recovery.go:2605-2613`), and the
exit code is non-zero. This is the only test that proves S25's boundary is a
*boundary* and not a crash.

#### This test decides S24's fate — recommendation: **single-session, defer S24**

S24 (durable `pg_multixact` + `multixact_redo`) is triggered by *concurrency*, not
by any statement above, so **the test design is what decides S24's fate**. A
single-session workload produces no MultiXact record and S24 can be deferred. Two
concurrent `FOR SHARE` sessions, or two sessions inserting children referencing
the same parent row (FK RI takes `FOR KEY SHARE`), make it mandatory: RM_MULTIXACT
(6) is unhandled and goopg's reader takes the *safe* path for rmid 6 (decodes,
hits `replayDecodedXLogRecord`'s `default:` at `internal/wal/recovery.go:2525`,
refuses), so the test would simply fail to start.

**Recommendation: single-session, defer S24** — and say why in the test file. S24
is ~4 loops and LARGE/RISKY, it needs an SLRU abstraction extracted from
`internal/mvcc/clog_bufferpool.go` before a line of redo is written, and it is not
on the path to the theme's headline claim. Add a **third** S28 variant, written
and `t.Skip`ped, that takes two concurrent `FOR SHARE` locks with `t.Skip("re-arm
trigger for M0131-S24")` — so the trigger is code, not a ledger sentence.

## Guards

1. `TestE2E_PGCrashStartOnGoopgDataDir` runs the full eight-step chain green, and
   reverting S17 locally turns step 6 red — verify that once before committing.
2. The PG log after the crash start contains `redo starts at` **and** `redo done
   at`, and zero `PANIC`/`FATAL`.
3. Committed rows are all visible, uncommitted rows all absent, and every captured
   aggregate matches goopg's pre-kill answer exactly.
4. The torn-contrecord sub-test asserts `there is no contrecord flag at` (or
   `invalid contrecord length`) **followed by** `successfully skipped missing
   contrecord at`, in that order, in the PG log.
5. `postmaster.pid` is asserted **present** after `g.Kill()` before the test
   deletes it; the day goopg removes or PG-shapes it, this guard fails and forces
   the handover step out.
6. The file carries the torn-page honesty note verbatim as a comment, and no guard
   in it mentions FPI or `full_page_writes`.
7. `TestE2E_GoopgCrashStartOnPGDataDir` is green, and every row of the §S28 opcode
   table is confirmed present in the crash tail by
   `postgres/local_install/bin/pg_waldump` over `pg_wal` **before** goopg starts —
   so a workload that silently stops producing an opcode fails loudly rather than
   passing for the wrong reason.
8. The GIN variant asserts a **non-zero exit** and a log line naming the access
   method; a bare `unsupported xlog record rmid=13` fails the guard.
9. The concurrency variant exists and is `t.Skip`ped with the S24 re-arm trigger in
   its skip message.
10. Both tests gate on `testing.Short()` + `pgcluster.Available` and run under the
    cgroup cap: `GOOPG_CG_UNIT=<name> scripts/goopg-test-run.sh go test -run
    '^TestE2E_(PGCrashStartOnGoopgDataDir|GoopgCrashStartOnPGDataDir)$'
    ./internal/testport/`. Never `-count=1` in a gate.
11. The E2E family stays green: `go test -v -run '^TestE2E_' ./internal/testport/`.
12. UNITS + SMOKE green —
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` plus the git
    hook's mandatory pgbench smoke on every commit (never `--no-verify`).

## References

- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
  §Theme F: S21 `:822-863`, S22 `:865-883`, S24 `:906-942`, S25 `:944-961`,
  S27 `:983-1017`, S28 `:1019-1027`
- `docs/design/0131-0004-forward-coldstart-e2e.md` (S4, the clean forward
  sibling); `0131-0003-reverse-coldstart-e2e.md` (S3, the clean reverse sibling);
  `0131-0014-pgcontrol-runtime-state-and-durability.md` (S17/S18/S20, which S27
  depends on)
- goopg: `internal/testport/e2e_pg_coldstart_on_goopgdata_test.go:34-42`, `:176-179`,
  `:333`, `:696`; `…/e2e_goopg_coldstart_on_pgdata_test.go:137-140`, `:165-182`, `:325`;
  `…/e2e_scenarios_test.go:109`; `internal/testutil/cluster/cluster.go:95`, `:172`,
  `:214`, `:303-333`, `:356`, `:471`; `internal/testutil/pgcluster/cluster.go:91`,
  `:120`, `:236`, `:325-336`, `:455`; `internal/control/control.go:61-69`, `:104-118`;
  `internal/server/server.go:677`, `:808-815`; `internal/wal/xlog_emit.go:129`;
  `internal/wal/reader.go:119`; `internal/wal/recovery.go:2525`, `:2605-2613`;
  `internal/initdb/xact_recovery.go:87-92`; `cmd/goopg/main.go:71`, `:1341`
- upstream: `postgres/src/backend/utils/init/miscinit.c:1210`, `:1275`, `:1354-1372`,
  `:1387-1412`, `:1415-1419`, `:1495`, `:1517`; `src/include/utils/pidfile.h:37-44`;
  `…/access/transam/xlogreader.c:760`, `:773`, `:939-940`;
  `…/access/transam/xlogrecovery.c:2115`, `:3188-3193`;
  `…/access/transam/xlog.c:6039-6050`, `:6153-6156`, `:7480`, `:7521`;
  `…/postmaster/postmaster.c:3171`, `:3680`;
  `src/include/access/{heapam_xlog.h,nbtxlog.h,xact.h,clog.h,rmgrlist.h}`,
  `src/include/catalog/storage_xlog.h`
