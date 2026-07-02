# goopg Fix Plan

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless the Current
Priority banner below or a dependency forces another order**.

## Notes / rules

- This is the authoritative TODO list for Ralph. Update it after every meaningful
  change (tick boxes, add newly-discovered follow-ups). ONE item per loop;
  decompose any item larger than a single agent invocation.
- Every non-trivial subsystem must land with (or just before) a design doc under
  `docs/design/<id>-NNNN-*.md` **and** a `docs/design/README.md` index entry —
  hard requirement, same loop.
- Deferrals: never close a task silently with a forward reference. Append one row
  to `.ralph/deferral_ledger.md` (`date | task-id | landed | deferred | resume
  point | why`) and leave the fix_plan item unchecked. **The ledger is the source
  of truth for every "DEFERRED" note below** — consult it for full context/resume
  points.
- Completed milestones are archived under `completed_milestones/` (latest:
  `completed_fix_plan_008.md`); they are reference-only, NOT actionable, and must
  not be copied back here.

## Current Priority (per 2026-06-20 directive)

Work order: **M0117 → M0118**, then resume **M0110** (paused), with **M0095**
parked. **M0118 is now fully complete (0001–0009 all `[x]`, closed 2026-07-01
loop #44 — see M0118-0004).** M0117's remaining sub-tasks are all deferred
Effort-L parts (need dedicated full-gate sessions — see each entry + the
deferral ledger). **M0110 is next up** (its M0119-0004/0005/0006/0007 spinoffs
are the active, in-progress form of that work).

Policy for **M0117 & M0118**: fix blockers in place; do NOT defer unless
genuinely compelling (then record a ledger row). Commit + push at every clean,
green (build + pre-commit) checkpoint.

**Added 2026-07-02 (interactive session):** **M0120** (WordPress WP-CLI
verification execution & evidence capture) and **M0121** (remediation of the
failures M0120 finds) are new milestones — see their sections at the end of this
file. The enabling goopg feature (statement/query logging, `GOOPG_LOG_STATEMENT`,
design `root-0023`) has already landed. Sequence them after the current
M0110/M0119 work: **run M0120 first** (it only needs the landed logging + the
committed `wp/verification/` checklist/flow), **then M0121** consumes its triaged
failure list.

## Archived — complete (see `completed_milestones/completed_fix_plan_008.md`)

M0096 (RC isolation feature impl + spec pass), M0100 (RC isolation runtime
closure / 21-spec pass), M0102 (heterogeneous streaming-replication +
SIGKILL-failover E2E), and the two completed Maintenance fixes
(MAINT-STATEGUARD-RECONCILE, MAINT-TPCH-RELOAD). Earlier milestones:
`completed_fix_plan_001.md` .. `completed_fix_plan_007.md`.

---

## M0095 — Client-Tools TAP Test Porting (filed 2026-05-12)

Design: `docs/design/0095-0003-*`. Goal: port the client-tools-tap suite and the
engine features its `t.Skip`'d scripts need. (`pg_ctl` 001–004 already PASS.)

- [x] **M0095-0002** — `pg_walsummary/002` ported (added `pg_stat_io` virtual view,
      `pg_available_wal_summaries()`; `TestPort_PgWalsummary002Blocks` PASS).
- [ ] **M0095-0003** — `pg_basebackup` 010/011/020 PASS (backup execution,
      `-X stream`/`-X fetch`, manifest + SHA-family checksums, in-place tablespace,
      `READ_REPLICATION_SLOT`). **Remaining:** `030 recvlogical` — blocked on logical
      decoding (not implemented; tracks with the logical-replication milestone / D-004).
      Deferred: on-disk `pg_tablespace` heap visibility (independent shared-catalog
      runtime write — see ledger). **Not actionable until logical decoding lands.**

## M0110 — Additional TAP Test Porting (beyond M0094/M0095) (filed 2026-05-22) — PAUSED

> **PAUSED** per the 2026-06-20 directive: resume only after M0117 + M0118 are
> complete. Scope = `docs/test-port/upstream-tap-coverage.md` tests not covered by
> M0094 (recovery/subscription) or M0095. Tags: SHOULD_PASS / BUG_FIX / UNIMPLEMENTED.

Already complete within M0110 (detail in git history): **M0110-0004** pg_resetwal
(RW-001..004 PASS), **M0110-0007 / M0110-0010** B-tree split & vacuum sibling
prev-link fixes.

- [ ] **M0110-0001 — pg_dump TAP** — `001_basic` ported (DU-001, CLI-only).
      `002–010` (schema dump, dump/restore round-trip, parallel, filter-file, connstr)
      DEFERRED on broad catalog-view parity + round-trip; being advanced one catalog
      gap at a time via the self-promoting `TestPort_PgDumpConnectionSetup` guard
      (CSV row DU-002, slice-by-slice). Design `0110-0001-pg-dump-tap-port.md`.
      Resume = next gap in pg_dump's getter battery (latest blocker tracked in
      `.ralph/working_set.md` / ledger).
- [ ] **M0110-0002 — pg_waldump TAP** — `001_basic` CLI tier ported (WD-001);
      WAL-format readability guarded by W-001 (`TestPort_WALPgWaldumpCompat`).
      **Remaining (WD-002, deferred):** `002_save_fullpage` — needs goopg to emit
      PG-decodable FPI/heap WAL with backup blocks (+ hash/gin/gist/spgist/brin AMs
      for the server tier). Design `0110-0002-*`.
- [ ] **M0110-0003 — pg_amcheck TAP** — `001_basic` (AC-001) + `002_nonesuch`
      (AC-002) ported; CREATE SCHEMA + user-schema table restart-durability enablers
      landed. **Remaining (AC-003, deferred):** `003_check`, `004_verify_heapam`,
      `005_opclass_damage` — need `verify_heapam()` SRF + opclass catalog parity +
      index AMs. (One 002 sub-section deferred: `datconnlimit=-2` invalid-DB filter —
      runtime shared-catalog write.) Design `0110-0003-*`.

## M0117 — CLOG ↔ PostgreSQL subsystem alignment (filed 2026-06-14)

> **Policy: FIX BLOCKERS IN PLACE; DO NOT DEFER** (else a compelling-reason ledger
> row). Per-task hard requirement: author `docs/design/0117-NNNN-*.md` + README
> index BEFORE coding. Gate: `go test -race ./internal/wal/... ./internal/mvcc/...`
> + recovery/standby E2E; visibility/tuple-format changes also run the TPC-H
> spot-check (Q12=2/Q13=35) / regress-port re-run.

Milestone doc `docs/milestones/0117-clog-postgresql-subsystem-alignment.md`. Goal:
bring `pg_xact` (CLOG) + `pg_subtrans` to PG 18.3 parity.

**2026-06-29 enabler (design 0117-0009, NOT a sub-task closure): TPC-H
spot-check startup-hang cleared.** `EnablePGSLRUMirror`'s startup backfill
fsync'd once per XID (~1.5M fsyncs on the bench dir → >6 min on WSL2), so the
mandated executor/planner gate `scripts/tpch-spotcheck.sh` never reached its 60 s
readiness window and infra-FAILED — the sole reason loops #7/#8 reported BLOCKED
and deferred the whole M0117 live-path tail. Fixed by routing the backfill
through the existing batched `mirrorTerminalRangeBatchedUnlocked` (one fsync per
segment, ≈2 total); byte-equivalent, live per-commit path untouched. A fresh
spotcheck start now reaches *ready* in ~35 s (was >6 min). Regression
`TestCLogEnableMirrorBackfillBatched`.

**2026-06-29 enabler (design 0117-0010, NOT a sub-task closure): TPC-H
spot-check now actually RUNS (was silently SKIPping).** After 0117-0009 fixed
readiness, the gate still never ran Q12/Q13: it probed the `user=tpch / db=tpch`
HammerDB load identity, but goopg registers `CREATE ROLE`/`CREATE USER`
**in-memory only** (`internal/server/role_ddl.go`) and the `tpch` database is
likewise non-durable, so neither survives the gate's fresh restart — yet the
loaded tables persist in the **`postgres`** database (`lineitem` = 5,999,786).
The `role "tpch" does not exist` probe error matched the table-missing SKIP
heuristic, masking a fully-loaded data dir (correcting 0117-0009's note that the
data dir merely needed a role reload — the data was never lost, only mis-probed).
Fix: `scripts/tpch-spotcheck.sh` falls back to the superuser + `postgres`
database persistent target on a role/database-missing probe error. Verified
end-to-end: fresh start → `postgres@postgres` → **Q12=2, Q13=33, RESULT=PASS**
(matches `spotcheck_expected.env`; confirms HEAD has no row-count regression).
With 0117-0009 + 0117-0010 the populated-data Q12/Q13 gate is now demonstrably
runnable, unblocking 0117-0006 Part B / 0117-0007 Part B for a dedicated
full-gate session (which still additionally need PG-standby E2E + `-race`
mvcc/wal + crash-replay). Follow-up (not in any actionable band): durable
role/database persistence is a real goopg feature gap.

- [x] **M0117-0001..0005** — DONE (designs `0117-0001..0005`; branches pending human
      merge off clean HEAD): wraparound-safe `storage.XIDPrecedes` horizon comparison;
      runtime CLOG-consulting visibility fallback; `pg_subtrans` restore-on-restart;
      `SUB_COMMITTED` (0x03) CLOG lane; incremental flush + group commit.
- [x] **M0117-0006 — SLRU buffer pool / 2-bit collapse (gap G6; Effort L).** Part A
      landed (`transaction_buffers` GUC + `clogBufferPool`, NOT wired to the live path —
      blast radius nil). **Part B LANDED 2026-06-29 (loop #11, design `0117-0006-*`
      "Part B — LANDED"):** the pool is now the live in-memory store
      (`CLog.pool atomic.Pointer`, promoted by `EnablePGSLRUMirror` after the backfill);
      `GetStatus`/`setStatus`, the group-commit leader (`applyGroupBatchLocked`→
      `pool.flushDirty`), and the bulk callers (`InitializeAsCommitted`/
      `MarkUnknownAsAborted`/`HighestKnownXID` via new `highestSLRUXID`/`TruncateCLOG`
      via new `pool.invalidateBelow`) all route through it. The never-fsynced legacy
      `global/pg_xact` flat file is retired (SLRU = single durable store; basebackup
      already excluded it; PG has no such file). The deferral reason on record across
      loops #7–#10 — *"the mandatory gates SKIP in the autonomous WSL2 loop"* — was
      **empirically disproven**: standby-attach + checksum-streaming E2E, `-race`
      mvcc/wal (incl. xlog_replay), and **TPC-H Q12=2/Q13=33 spot-check** all RUN+PASS.
      Regression `clog_bufferpool_live_test.go`. **Follow-up LANDED 2026-06-29
      (loop #12):** the `transaction_buffers` GUC value is now threaded into
      `CLog.SetCLOGBuffers` from `initdb.Open` (new `OpenOptions.TransactionBuffers`,
      read in `cmd/goopg start` via `intGUC`). Boot default 0 keeps the auto-16 floor
      (no behaviour change); a non-zero `postgresql.conf` override now sizes the live
      pool. Regression `TestTransactionBuffersFromGUC` + `TestSetCLOGBuffersSizesPool`.
      **Part C LANDED 2026-07-01 (loop #47, design `0117-0006-*` "Part C"):** the
      resident `banks`/legacy flat-file store is fully removed (16× memory reduction —
      resident cost is now the pool's bounded page budget, independent of `NextXID`).
      `clogBank`/`banks`/`path`/`dirtyMu`/`dirtyPages`/`getOrCreateBank`/`getBank`/
      `distributeToBanks`/`markFlatDirty`/`flushDirtyPagesLocked`/`flush`/`flushLocked`/
      `mirrorTerminalRangeBatchedUnlocked`/`mirrorGroupToSLRULocked`/
      `applySegmentLanesLocked`/`loadFromSLRU` all deleted; `banksMu` renamed
      `slruDirMu`. `OpenCLog` no longer reads its `path` argument; `EnablePGSLRUMirror`
      creates the pool directly (no flat-file→SLRU backfill round-trip — it was a
      no-op for any Part-B-or-later data dir). `IsEmpty()` rewritten to a disk-truth
      check (`highestSLRUXID() == 0`) instead of a process-local flag (a process-local
      flag would misreport "empty" on every restart of a populated cluster and
      misroute into the upgrade path — caught by independent review before landing).
      ~20 test functions across `internal/mvcc` + all 4 `internal/initdb/
      xact_recovery_test.go` tests migrated to call `EnablePGSLRUMirror`; the 5 direct
      `loadFromSLRU` callers rewritten with a test-local SLRU-segment decoder to
      preserve their sibling-path-independence intent. Gates: `-race` mvcc+wal,
      `internal/initdb`+`internal/server` full suites, standby-attach +
      checksum-streaming E2E, TPC-H Q12=2/Q13=33 spot-check, `gofmt -l` — all PASS.
      Ledger row (loop #11 Part-C-deferred row flipped `resolved`, new Part-C-landed
      row appended) records one accepted, deliberate compatibility cut: a pre-Part-B
      data dir's never-mirrored flat-file bytes are now silently unrecoverable (not a
      PG-fidelity gap — PG has no such dual-store distinction).
- [x] **M0117-0007 — Async-commit LSN tracking (gap G8; Effort L).** Part A landed
      (per-LSN-group tracking + page-write WAL barrier on the M0117-0006 pool, not live).
      **Part B LANDED 2026-07-02 (loop #49, design `0117-0007-*` "Part B"):** the
      barrier is now wired to the live WAL writer (`CLog.SetFlushWALHook` ←
      `walWriter.FlushUpTo`, called from `initdb.Open`), `mvcc.Manager` gained
      `CommitAsync` (threads a new `waitLocalFlush bool` through `finish`/the
      `xactMarker` hook), the hook skips its inline `FlushUpTo` and calls the new
      `CLog.SetCommittedWithLSN` instead of `SetCommitted` when `waitLocalFlush`
      is false, and `synchronous_commit` is read for the local-flush decision for
      the first time via a new `executor.Context.AsyncCommit` +
      `Context.CommitTransaction` (wired at every live interactive commit call
      site: explicit COMMIT, simple/extended-protocol autocommit, PL/pgSQL commit
      chain; 2PC's COMMIT PREPARED reuses these). **Remaining (deferred, ledger
      2026-07-02): this does NOT yet reduce commit latency** — `groupUpdate`
      still flushes the CLOG page synchronously on every commit (M0117-0005's
      eager per-commit design), so the barrier fires immediately regardless of
      `waitLocalFlush`, same as before. Genuinely cutting latency needs
      `groupUpdate` to skip the durable write-back for an async commit (leaving
      the page dirty) PLUS a checkpoint-driven CLOG flush (`CLog`/
      `clogBufferPool` implements no `wal.DirtyPageFlusher`, so nothing bounds
      how long a would-be-deferred dirty page stays unflushed) — a separate,
      larger, checkpoint-subsystem-touching follow-up (the only remaining item
      before this box can close). **COPY's own commit sites LANDED 2026-07-02
      (loop #50):** all 4 sites in `internal/server/copy.go`
      (`dispatchCopyViaExecutor`'s `CopyTo`/file-`CopyFrom` via
      `ectx.CommitTransaction`, `handleCopyInFrame`'s two streaming
      `COPY FROM STDIN` commits via a new `commitCopyTx` helper +
      `copyInState.asyncCommit`) now honor `synchronous_commit`; regression
      `TestCommitCopyTxRespectsAsyncCommit`. Needs TPC-H + crash/standby E2E —
      both PASS (TPC-H re-verified 2026-07-02: Q12=2/Q13=33). **Part C — lazy
      write-back + checkpoint-driven flush LANDED 2026-07-02 (loop #51),
      closing the box:** `CLog.setStatusWithLSN` now skips `groupUpdate`'s
      eager durable write-back whenever `lsn != 0` (async commit; every
      synchronous caller still passes `lsn == 0`, unaffected), leaving the
      page dirty; write-back defers to a later sync commit's group flush, LRU
      eviction, or the new checkpoint-driven `CLog.FlushAll()` (thin
      `pool.flushDirty()` wrapper, structurally satisfies
      `wal.DirtyPageFlusher`) wired via a new
      `wal.CheckpointerConfig.FlushCLOGFn` hook (`internal/initdb/open.go`:
      `FlushCLOGFn: clog.FlushAll`), invoked in the checkpoint's flush phase
      before the redo LSN is sampled; unlike `PostCheckpointFn`/
      `TruncateCLOGFn`, a `FlushCLOGFn` error fails the checkpoint. Crash
      safety rests on the pre-existing `replayCLogFromWAL` backstop (recovery
      re-derives CLOG status from post-redo-LSN WAL records regardless of
      disk state), proven end-to-end by new
      `TestReplayCLogFromWAL_RecoversUnflushedAsyncCommit`
      (`internal/initdb`). Regressions: `TestCLogSetCommittedWithLSNDefersFlush`
      + `TestCLogFlushAllBeforePoolExistsIsNoop` (mvcc),
      `TestCheckpointerCallsFlushCLOGFn` +
      `TestCheckpointerFlushCLOGFnErrorFailsCheckpoint` (wal). Gates: build/vet/
      gofmt clean; `go test -count=1` mvcc+wal+initdb+server+executor PASS;
      `go test -race` mvcc+wal PASS; `TestE2E_PhysicalReplication{,Sync}` /
      `TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart` /
      `TestE2E_ChecksumStreamingGoopgToPG` (`-race`) PASS; TPC-H spot-check
      Q12=2/Q13=33 PASS. One residual gap deferred to the ledger: the
      checkpoint's redo LSN is sampled after (not before) the flush phase — a
      narrow race shared symmetrically with the heap pool's own checkpoint
      flushing (pre-existing, not CLOG-specific); closing it needs a
      whole-checkpoint-subsystem redo-pointer redesign, out of scope here.
      Design `0117-0007-*` "Part C".
- [x] **M0117-0008 — datfrozenxid persistence (Effort M). FULLY DONE 2026-07-02
      (loop #52).** Part A DONE (dual-store consistency for all 4 CLOG status codes,
      via the M0117-0004 chain; `clog_dual_store_consistency_test.go`). **Part B
      LANDED:** `vacuumOp.Next` calls the new `persistDatFrozenXID`
      (`internal/executor/operators_vacuum_datfrozenxid.go`) unconditionally at VACUUM
      end, advancing the on-disk `pg_database.datfrozenxid` tuple in place
      (`storage.PageReplaceItemRaw`) and WAL-logging it via a new
      `catalog.PgCanonicalHeapInplace` (`XLOG_HEAP_INPLACE`, FPI-based). Also fixed a
      wider gap the design doc's original scoping missed: `storage.Manager` had no
      `global/` shared-tablespace path concept at all (every `RelFileNode` resolved to
      `base/<DBOid>/...`); `DBOid==0` is now the shared-catalog sentinel
      (`sharedOrPerDBRelDir` in `internal/storage/smgr.go`). New
      `catalog.SharedCatalogRelFileNode`/`PgDatabaseColumnsPG18` unify initdb's
      bootstrap-time pg_database encode with the runtime decode/re-encode so the two
      can never drift. Verified against a live `goopg init`-bootstrapped data dir over
      the wire (datfrozenxid 3→4 after `VACUUM`, untouched template rows unchanged).
      Full gate (race mvcc/wal/storage/catalog, standby-attach + replication E2E,
      TPC-H Q12/Q13 spot-check) PASS. Design `0117-0008-*` "Part B — LANDED".

## M0118 — Upstream Isolation Spec Suite Pass-Through (filed 2026-06-20)

> **Policy: FIX BLOCKERS IN PLACE; DO NOT DEFER** (else a compelling-reason ledger
> row); commit at every clean, green checkpoint.

Goal: with REPEATABLE READ (txn-level pinned snapshot) and SERIALIZABLE (RR snapshot
+ real SSI raising SQLSTATE 40001) implemented (M0100/M0104), drive all **112
targeted specs** to `pass` vs PostgreSQL 18.3 (`./postgres/local_install`) using
`internal/testport/framework/isolation_runner.go`. Keep the 9 already-passing specs
green. Milestone doc `docs/milestones/0118-isolation-spec-suite-passthrough.md`.

Per-spec workflow (each slice): make the spec green via its `TestPort_Isolation*`
test → set its CSV row `status=pass` (rationale = the Go test func name) → regen
`go run ./cmd/gen-isolation-coverage --repo-root .` and
`go run ./cmd/gen-oracle-inventory --repo-root .`. Pick one coherent spec group per loop.

- [x] **M0118-0001** — SERIALIZABLE / SSI anomaly specs (write-skew + dangerous-structure
      40001): simple-write-skew, matview-write-skew, read-only-anomaly{,-2,-3},
      read-write-unique{,-2,-3,-4}, two-ids, total-cash, receipt-report, project-manager,
      classroom-scheduling, multiple-row-versions, update-conflict-out,
      serializable-parallel{,-2,-3}. **COMPLETE (2026-06-22, design 0118-0025).** All
      19 specs already matched PG 18.3 byte-for-byte (SERIALIZABLE pinned snapshot
      M0100 + real SSI 40001 dangerous-structure detector M0104); promoted the whole
      group from soft `runIsoSpec` to `runIsoSpecStrict` in their dedicated
      `TestPort_Isolation*` functions with NO engine change. D-002 CSV rationale updated.
- [x] **M0118-0002** — Predicate-lock granularity per access method / scan type:
      predicate-gin, predicate-gist, predicate-hash, predicate-lock-hot-tuple,
      index-only-scan, index-only-bitmapscan, partial-index.
      **GROUP COMPLETE (2026-06-29).** All seven specs strict-promoted to
      pass-required and byte-identical to PG 18.3. `predicate-gin` was the last
      (design 0118-0140): GIN per-key predicate locking via key-grain SIREAD
      (`ssiGinKeyPage` FNV pseudo-page, `ssiRecordGinKeyRead` on the `@>` seq-scan
      path replacing the relation-grain lock + `ssiRecordGinIndexInsert` twin
      conflicting-in on each inserted element; `fastupdate=on` uses a whole-index
      sentinel page toggled by the new `ALTER INDEX … SET (fastupdate=…)` parse +
      executor). Isolation tally **120 pass / 1 failed** — the lone remaining
      `failed` spec is `deadlock-parallel` (M0118-0004; needs a parallel-query
      lock-group abstraction goopg has no subsystem for — infeasible).
      **PARTIAL (2026-06-22, design 0118-0026):** probe-first ranked all 7; three
      promoted to pass-required (strict) with NO engine change —
      `predicate-lock-hot-tuple`, `partial-index`, `index-only-scan` already match
      PG 18.3 byte-for-byte (SERIALIZABLE pinned snapshot M0100 + SSI 40001
      detector M0104 form the write-skew structure across HOT-tuple updates,
      partial-index row-movement, and all-visible index-only scans). Switched
      `TestPort_IsolationPredicateLockHotTuple/PartialIndex/IndexOnlyScan`
      soft→`runIsoSpecStrict`. **Remaining (deferred, ledger 2026-06-22):**
      `index-only-bitmapscan` (global-setup connection crash — bitmap-scan path),
      `predicate-gin` (int-array `{1}` + GIN AM), `predicate-gist` (point type +
      GiST AM) need missing index access methods; `predicate-hash` OVER-detects a
      40001 where PG commits — goopg's coarse relation-grain SIREAD vs PG's finer
      hash-index predicate locking (the canonical granularity gap). Group stays open.
      **2026-06-23 (design 0118-0038, enabler — NOT a promotion):** the
      `index-only-bitmapscan` global-setup "connection crash" was a backend PANIC
      (`index out of range` in `insertOp.Next`) on `INSERT INTO t SELECT g.i, g.i`
      into a 3-column table (`pad char(1024) DEFAULT ''`) — `INSERT … SELECT` with
      no column list + fewer source columns than the table indexed past the shorter
      source row instead of default-filling the trailing columns. Fixed in
      `planInsert` (reconcile source arity with `ColumnIndex`: over-wide →42601,
      explicit-list under-wide →42601, implicit-list under-wide → truncate so the
      executor defaults the rest). Spec still deferred — remaining blockers:
      `VACUUM (TRUNCATE false)` option parse + `EXPLAIN DECLARE CURSOR` + cursor
      FETCH semantics.
      **2026-06-25 (design 0118-0108, enabler — NOT a promotion):** cleared the
      `VACUUM (TRUNCATE false)` parse blocker. `VACUUM (TRUNCATE false) <tbl>`
      (documented PG syntax) was rejected `unrecognised VACUUM option (got
      truncate)`: the option list had a `truncate` case but matched only via
      `acceptIdentKeyword`, while the lexer classifies `TRUNCATE` as the
      unreserved keyword `KwTruncate` (leads `TRUNCATE TABLE`), so it fell
      through to `default`. Fix = also accept the keyword token
      (`p.acceptKeyword(KwTruncate) || p.acceptIdentKeyword("truncate")`);
      `TRUNCATE` is the only VACUUM option word that is also a SQL keyword.
      `NoTruncate` is recorded for parity but `vacuumCore` never physically
      truncates trailing empty pages, so both `TRUNCATE false/true` are no-ops
      today. Cursor FETCH already works; re-probe shows the first divergence
      moved to `s1_explain` (`EXPLAIN (COSTS OFF) DECLARE … CURSOR` — executor
      rejects `*parser.DeclareCursorStmt`) and the spec's core requirement, the
      `BitmapOr` bitmap-scan plan the EXPLAIN must render byte-for-byte. Both
      remain Effort-L → spec stays deferred. `TestParseVacuumTruncateOption`.
      **`index-only-bitmapscan` PROMOTED (2026-06-25, design 0118-0122):** the
      `s1_explain` `DeclareCursorStmt` rejection was the SOLE remaining blocker.
      `planner.Plan`'s `ExplainStmt` case now unwraps a `DeclareCursorStmt`
      inner to its `.Query` before planning (PG `ExplainOneUtility`→
      `ExplainOneQuery`; the cursor is never created, only its query explained).
      The earlier "must render `BitmapOr` byte-for-byte" requirement was an
      over-estimate: `normalizeIsoOutput` STRIPS the EXPLAIN plan block on both
      sides (established plan-strategy policy, same as merge-join), so goopg
      rendering no `BitmapOr` node is irrelevant — only the EXPLAIN's success +
      the spec's real anomaly check (the FETCH row counts: `s1_fetch_1`→1 row,
      `s1_fetch_all`→0 rows, verifying the second VACUUM didn't wrongly mark
      dead pages all-visible) are compared, and goopg already produces those on
      its existing index-scan + cursor + VACUUM machinery. Strict
      `TestPort_IsolationIndexOnlyBitmapscan` + executor unit
      `TestExplainDeclareCursorExplainsInnerQuery`. Group stays open:
      `predicate-gin`/`predicate-gist` need real GIN/GiST AMs; `predicate-hash`
      over-detects 40001 (coarse relation-grain SIREAD). Isolation tally now
      117 pass / 4 failed.
      **2026-06-29 (design 0118-0135, enabler — NOT a promotion): `predicate-gist`
      read-step support.** Probe-first found the spec already runs (point type,
      `point(x,y)`, `CREATE INDEX … USING gist`, SERIALIZABLE all present) but its
      first read step errored on two gaps: (1) `p[0]` subscripting a point
      char-subscripted the text form (`point(10,10)[0]`→`"("`), so `sum(p[0])`
      failed `42804 sum() argument must be numeric` — now returns the 0/1
      coordinate (0-based, PG geometric: `[0]`=X `[1]`=Y) as numeric, typed float8
      by analyzer+planner; (2) `<<`/`>>` on points raised `42883 requires integer
      operands` (parsed as bit-shift) — now `p << q`⇔`p.x<q.x`, `p >> q`⇔`p.x>q.x`
      → bool. **A filtered probe confirms ZERO SSI divergences** — goopg's 40001
      behaviour already matches PG byte-for-byte across all permutations
      (relation/tuple-grain SIREAD happens to coincide with PG's gist page-level
      locking for this spec's spatial data). The **sole** remaining divergence is
      goopg's float8 *text output* (`2.23375e+06` vs PG `2233750`; `codec.go`
      `FormatFloat(…,'g',-1,64)`), a cluster-wide display path deferred to a
      dedicated `float8out` loop (ground-truth vs local PG + full regress-port
      re-run required) — after which `predicate-gist` is expected to PROMOTE with
      no further SSI work. `predicate-gin` independently needs `int4[]`-column
      array typing (`array[1]`→int4[] collapses to int4 today) + a GIN AM.
      Tests `TestPointStrictlyLeftRightOperators` (executor) +
      `TestPort_PointGeometricRead` (end-to-end). Ledger row recorded.
      **2026-06-29 (design 0118-0136, enabler — NOT a promotion): PG-faithful
      `float4out`/`float8out`.** New `executor.PGFloatOut` reproduces PG's
      `float8out`/`float4out`: shortest round-trip decimal (Ryu via Go's `'e'`
      verb) + PG's fixed-vs-scientific *display-exponent* threshold (`[-4,15)`
      float8, `[-4,6)` float4), so `2233750::float8`→`2233750` not
      `2.23375e+06`. Wired at ALL FOUR sibling sites (encode↔wire-simple↔
      wire-extended↔test-harness): `codec.go` `encodeValuePG`, `dispatch.go`
      `appendFloatText`/`appendFloat8Text`, `dispatch_extended.go` float result
      columns, `isolation_runner.go` `scanResultSet` (scan float OIDs 700/701 as
      `NullFloat64`, render via `PGFloatOut`). Cluster-wide float text is now
      byte-faithful to PG 18.3. **CORRECTION:** the prior "filtered probe →
      ZERO SSI divergences" claim was WRONG. With float output fixed, a full
      probe shows `predicate-gist`'s first divergence is a GENUINE SSI
      **over-detection**: perm `rxy3 wx3 rxy4 c1 wy4 c2` — goopg raises `40001`
      on `c2` commit where PG commits cleanly. `rxy3` reads `p>>point(6000,6000)`
      (X>6000), `rxy4` reads `p<<point(1000,1000)` (X<1000); `wx3` inserts
      high-X, `wy4` low-X — PG's GiST **page-level** predicate locks see disjoint
      spatial regions (no dangerous cycle), goopg's coarse relation/tuple-grain
      SIREAD locks the whole relation → false write-skew cycle → spurious
      `40001`. **Remaining blocker (deferred):** GiST spatial page-grain (or
      bounding-box/grid-cell) predicate locking — the granularity class
      `predicate-hash` solved with bucket-grain SIREAD (FNV→`PageLockTag`, design
      0118-0099); Effort-L. `predicate-gin` still needs `int4[]`-column array
      typing + a GIN AM. Tests `TestPGFloatOut` (28 float8 + 17 float4
      PG-captured goldens) + full `TestPort_RegressSuite` re-run + TPC-H
      Q12/Q13 spot-check. Ledger row recorded.
      **`predicate-gist` PROMOTED (2026-06-29, design 0118-0137): GiST
      page-level predicate locking via grid-cell SIREAD.** `failed`→`pass`, all
      36 perms byte-identical to PG 18.3; strict `TestPort_IsolationPredicateGist`;
      isolation tally now 119 pass / 2 failed. Closes the granularity gap the
      0118-0135/0118-0136 enablers had isolated. goopg has no native GiST AM (a
      `USING gist` index is catalog-only → spatial queries seq-scan), so the seq
      scan's relation-grain SIREAD over-aborted all 18 disjoint-region perms. Fix
      emulates GiST leaf-page granularity with a synthetic grid: `ssiGistGridCell`
      = FNV-1a of `(floor(x/256),floor(y/256))` → 31-bit pseudo-page; a
      SERIALIZABLE seq scan of a GiST-indexed table takes a per-matching-tuple
      grid-cell SIREAD on the INDEX (`ssiRecordGistGridRead`) instead of the
      relation lock (suppressed), and an INSERT conflicts-in on its point's cell
      (`ssiRecordGistIndexInsert`, the `ssiRecordHashIndexInsert` twin). Heap
      per-tuple SIREAD skipped (would coarsen to a heap-page lock → false
      positives); invisible-tuple conflict-out gated by spatial match
      (`gistTupleMatches`). `Filter`-over-`SeqScan` predicate threaded in BOTH
      build paths (`Build` + live `buildRec`/`BuildFastIterator`). Blast radius
      bounded behind `gistSSIIdxOID != 0` (0 for every non-gist scan); catalog
      `Method`/pg_am/pg_dump/WAL unchanged. Gates: strict 36-perm PASS; non-gist
      SSI regression (`predicate-hash`/`partial-index`/`index-only-scan`/
      `simple-write-skew`/`project-manager`/`classroom-scheduling`/
      `read-write-unique`) PASS; `-race` executor+mvcc + full executor/planner
      units PASS; `go build ./...` clean; pgbench smoke 0 failed; TPC-H spot-check
      infra-timed-out on WSL2 (gated path unaffected). **Remaining in M0118-0002:
      `predicate-gin`** (needs `int4[]`-column array typing + a GIN AM) — group
      stays open.
      **2026-06-29 enabler (design 0118-0138, NOT a promotion): `int4[]` user
      array column storage round-trip.** `predicate-gin.spec`'s global `setup`
      (`create table gin_tbl(p int4[]); insert … select array[1] …`) failed at
      the INSERT with `invalid input syntax for type integer: "{1}"` — a user
      array column (`catalog.Type{Name:"int4", IsArray:true}`; Name is the
      ELEMENT type, array-ness tracked separately) was treated as a scalar int4
      at five `Type.Name`-only sites. New `internal/executor/codec_array.go`
      stores array columns as PG-native `ArrayType` varlena blobs (1-D, no-NULL;
      24-byte header + typalign-packed elements; int2/int4/int8/oid/float4/
      float8/bool fixed + text/varchar/bpchar varlena) and decodes back to
      canonical `"{1,2}"` text. Wired behind `if t.IsArray` at `encodeValuePG`,
      `decodePhysicalPGValueMctx`, `physicalPGTypeAlign`; insertOp integer-range
      coercion skips array cols (`operators_storage.go`); `isAssignable` accepts
      an array source for an array dst (`analyzer.go`, fixes VALUES-path 42804);
      BOTH simple+extended RowDescription loops advertise the array OID via
      `catalog.ArrayOIDForBase` when `IsArray` (`server/dispatch.go`, sibling
      paths — fixes client-side `strconv.ParseInt: parsing "{1}"`). Zero blast
      radius outside array columns (every branch `IsArray`-gated; scalar paths
      byte-identical). First divergence advanced from permutation-0 global setup
      (blocked ALL perms) to the first read step `ra1` (`p @> array[1]` →
      `operator @>: invalid box value`). Spec stays `failed`. Remaining blockers
      (ledgered): (1) `@>` array-containment runtime (mis-dispatched to geometric
      `box @> box`); (2) GIN page-grain SSI (reuse the 0118-0137 grid-cell
      primitive keyed on the GIN search key). Tests `TestArrayCodecRoundTrip` +
      `TestArrayCodecTextElementQuoting`.
      **2026-06-29 enabler (design 0118-0139, NOT a promotion): `anyarray`
      containment/overlap operators `@>` `<@` `&&`.** Read step `ra1`
      (`select * from gin_tbl where p @> array[1]`) failed `operator @>: invalid
      box value` — these symbols were implemented only as the geometric **box**
      operators (`parseBoxText` on both operands). PG overloads them by operand
      type (`anyarray @> anyarray` → `arraycontains`/`arraycontained`/
      `arrayoverlap`). Fix: at the `OpContains`/`OpContainedBy`/`OpOverlap` case
      in `evalBinaryOp` (expr.go), BEFORE box parsing, detect both operands as
      array literals (`isArrayLiteralText`: `{`…`}`) and route to set-membership
      (`evalArraySetOp`/`arrayElemsSubset`): `a @> b` = every elem of `b` in `a`;
      `a <@ b` = every elem of `a` in `b`; `a && b` = shared elem; `{}` trivially
      contained. Zero blast radius (fires only when BOTH operands are `{`…`}`).
      First divergence advances from `ra1` (`invalid box value`) to a genuine SSI
      granularity divergence: a disjoint perm (`ra1 ro2 wo1 c1 wb2 c2`: read key
      1, insert key 2) returns correct rows but goopg over-aborts the writer
      40001 (no native GIN AM → seq-scan relation-grain SIREAD). Spec stays
      `failed`. Remaining blocker (ledgered): GIN key-grain SSI — reuse the
      0118-0137 grid-cell primitive keyed on the GIN search key (array element),
      `ssiRecordGinKeyRead` on the scan path + `ssiRecordGinIndexInsert` twin per
      inserted element + the `fastupdate=on` whole-index sentinel-key case.
      Test `TestArraySetOps`.
- [x] **M0118-0003** — Row locking (FOR UPDATE/SHARE, SKIP LOCKED, NOWAIT): **COMPLETE.**
      All 20 specs PASS vs PG 18.3 (verified 2026-06-22): skip-locked{,-2,-3,-4},
      nowait{,-2,-3,-4,-5}, lock-nowait,
      tuplelock-{conflict,partition,update,upgrade-no-deadlock},
      lock-update-{delete,traversal}, update-locked-tuple, propagate-lock-delete,
      lock-committed-{update,keyupdate}. The cumulative multixact producer +
      subxact-scoped row-lock infra (M0118-0003/0004 slices) carried the final
      batch green; CSV rows already `pass`.
      **2026-06-23 hardening (design 0118-0042):** the `SKIP LOCKED` family
      (`skip-locked{,-2,-3,-4}`) promoted from `runIsoSpec` (silent-skip on
      regression) to `runIsoSpecStrict` (hard red) — all four already byte-match PG
      18.3, no engine change.
      **2026-06-23 hardening completed (design 0118-0043):** the remaining 16
      dedicated row-lock tests promoted to `runIsoSpecStrict` —
      `nowait{,-2,-3,-4,-5}`, `lock-nowait`,
      `tuplelock-{conflict,update,partition,upgrade-no-deadlock}`,
      `lock-update-{traversal,delete}`, `update-locked-tuple`,
      `propagate-lock-delete`, `lock-committed-{update,keyupdate}`. All 16 already
      byte-match PG 18.3 (single `go test` over the family `ok` in ~83 s, strict),
      no engine change. **All 20 M0118-0003 row-lock specs are now strict — none can
      regress silently.**
- [x] **M0118-0004** — Deadlock detection: deadlock-{hard,simple,soft,soft-2,parallel},
      multixact-no-deadlock. **Done so far:** `deadlock-simple` (slice 16, design
      0118-0004) + `deadlock-hard` (general timeout-driven multi-object detector,
      `deadlock_timeout` GUC + per-session lockmgr timeout + firing-backend victim +
      runner `(*)` marker; design 0118-0005) + `deadlock-soft`/`-soft-2`
      (SOFT-deadlock wait-queue reordering — `deadlock.c` findLockCycle soft edges +
      deadLockCheck/testConfiguration + expandConstraints/topoSort + applyWaitOrders;
      reorders the queue and wakes the newly grantable waiter instead of aborting;
      design 0118-0006) + `multixact-no-deadlock` (re-acquiring a self-held row
      lock; NO new engine work — conflict-gate-before-wait in stampLockInner +
      self-filtering in activeLockHolders/stampMultiLock/MembersConflict already
      hold the invariant; design 0118-0007) + `tuplelock-upgrade-no-deadlock`
      **PARTIAL** (design 0118-0008): fixed an `ERROR: short read at block` crash
      on a committed-DELETE re-fetch (`stampLockInner` followed a goopg DELETE's
      `{InvalidBlockNumber,0}` initial CTID into a non-existent block; extracted
      shared `isChainTailCTID` helper used by both `epqFollowChainFull` and the
      row-lock path; deleted row now `epqSkipped` → `(0 rows)`). **Remaining
      (deferred):** (a) `tuplelock-upgrade-no-deadlock` is NOT promoted — the
      "likely no-engine" guess was WRONG. Two failures remain: wait-queue upgrade
      priority (perms 2,3 — existing key-share holder `s3` upgrading must wake
      before pure waiter `s2`; goopg wakes `s2` first; `stampLockInner` committed-
      updater branch ~L719 ignores a multixact xmax that carries updater+lockers)
      and a savepoint-driven lock-retry deadlock (perm 9 `s1_fornokeyupd` times
      out). Both are deeper multixact-with-updater wait-ordering work.
      **2026-06-22 read-side half landed (design 0118-0009):** `stampLockInner`'s
      real-updater branch is now multixact-aware (resolves the real updater via
      `updaterXID`, waits first on every other still-active member, excludes a
      self-upgrading holder) — fixes a latent raw-MultiXactId-fed-to-single-xid-API
      bug; regression batch green. perms 2/3 STILL fail because the PRODUCER side
      is unimplemented: goopg's UPDATE stamps the old tuple with a single updater
      xid (`PageSetHeapTupleXmax` at `operators_storage.go:2638`/`:3279` + merge/
      upsert twins) instead of preserving a non-conflicting locker into a
      `{updater+survivors}` MultiXactId (upstream `heap_update`/`MultiXactIdCreate`).
      NEXT: shared `stampUpdaterXmaxPreservingLockers` helper, gated on a
      pre-existing foreign non-conflicting active lock-only xmax (bounded blast
      radius), then make the UPDATE conflict-wait multixact-aware.
      **2026-06-22 write-side EPQ-wait half landed (design 0118-0010):** the
      UPDATE/DELETE/MERGE EvalPlanQual loops fed the RAW `xmax` (a MultiXactId
      when updater-bearing) to `epqWait`/`HasInProgress`/`HasAbortedXID`/
      `IsXIDActive`; new shared `concurrentModifierXID(hdr, mxs)` resolves the
      real updater member at all 9 EPQ-wait sites (7 storage + 2 merge), the
      write-side twin of 0118-0009. Latent-bug fix; regression batch green with
      `-race`; spec still deferred. **Producer discovery:** the spec's `name`
      UPDATE is HOT-eligible, so the old-tuple stamp goes through
      `PageStampHotOldTuple` in `tryApplyHOTUpdate`, NOT the
      `PageSetHeapTupleXmax` sites — the producer needs a NEW multi-aware
      HOT-stamp storage primitive (CTID + `HEAP_HOT_UPDATED` + multi xmax) in
      addition to the plain delete+insert / merge / upsert wiring, plus full
      UPDATE-hot-path gates (pgbench + regress-port).
      **2026-06-22 producer landed (design 0118-0011): 8/9 spec perms now match.**
      New storage primitive `PageStampHotOldTupleMulti` + shared helper
      `stampUpdaterXmaxPreservingLockers` (gated on a lock-only xmax; keeps only
      still-active foreign lockers whose mode does NOT conflict via
      `multixact.StatusesConflict`, appends our updater member, builds the multi)
      + non-HOT wrapper `stampUpdaterXmaxNonHOT`, wired at ALL old-tuple xmax
      stamp twins (HOT path = spec; index/seqscan UPDATE delete-half; index/
      seqscan DELETE + DELETE…USING; UPDATE…FROM; merge update/delete; upsert
      update + delete). A no-key UPDATE now preserves a non-conflicting FOR KEY
      SHARE holder into `{updater + survivors}`; key-UPDATE/DELETE (StatusUpdate
      conflicts with all modes) stays a plain stamp. Crash-safe: HOT/delete WAL
      records still carry the single updater xid → replay degrades to single-xid
      (transient lockers don't survive a crash; multixact WAL persistence deferred
      0118-0002). Unit tests `TestPageStampHotOldTupleMulti`/
      `TestStampUpdaterXmaxPreservingLockers`; `-race` batch + multixact/storage/
      executor/wal/mvcc PASS; pgbench smoke 0-failed.
      **2026-06-22 perm-9 Gaps A/B/C (design 0118-0012 §5):** landed three pieces
      of the savepoint-scoped row-lock subsystem — subxact-aware `IsXIDActive`/
      `WaitForXID` (`mvcc.Manager.xidActiveWithSubxact`), conflict-filtered wait
      (`lockRowsOp.conflictingLockHolders`, MultiXactIdWait semantics), and
      `commitCond.Broadcast()` on `MarkSubxactAborted`; moved the spec's first
      divergence from expected L216 → L238.
      **2026-06-22 perm-9 Gap D LANDED ⇒ `tuplelock-upgrade-no-deadlock` PROMOTED
      to `pass` (design 0118-0012 §6): all 9 permutations byte-identical.** Two
      fixes: (1) `lockRowsOp` now stamps every lock member under the effective
      writer xid (`writerXID()` = `session.EffectiveWriterXID()` = current sub-XID
      inside a savepoint) and every row-lock self/conflict gate became tree-aware
      (`isSelfXID()` via `Manager.TopLevelXid` — `mvcc.IsSelfXID` was NOT reusable:
      it resolves *currentXID* upward, but here the member is the subxid and
      `ctx.Tx.XID` is top-level, the reverse direction); `stampMultiLock` keeps
      outer-level self members as survivors (exact-`writerXID()` match) so ROLLBACK
      TO an inner savepoint reverts to the outer strength. Strict no-op outside a
      savepoint. (2) Latent split exposed: `Manager.AllocateSubXid` registered the
      sub-XID via `addSubxactEntry` (in-memory fallback map ONLY) but
      `TopLevelXid`/`IsAborted` read the *attached* pg_subtrans `SubxactMap`
      (installed by `initdb.Open` → real-server path), so `xidActiveWithSubxact`
      saw the savepoint lock as dead and waiters never blocked; fixed by routing
      `AllocateSubXid` through `RegisterSubXid` (writes the attached map). Gates:
      `-race` mvcc/multixact/executor; full row-lock+deadlock+merge+EPQ isolation
      batch PASS; pgbench smoke. CSV promoted failed→pass; coverage/inventory md
      regenerated. **Still deferred:** UPDATE/DELETE conflict-wait-on-a-conflicting-
      locker (independent slice, ledger'd).
      (b) `deadlock-parallel` needs a lock-group abstraction goopg lacks — defer.
      **2026-07-01 (loop #44, doc-only, no design doc): CLOSED — every spec named
      in this item's own title is resolved.** `deadlock-{hard,simple,soft,soft-2}`
      + `multixact-no-deadlock` + `tuplelock-upgrade-no-deadlock` all `pass`
      (CSV-verified above). `deadlock-parallel` stays `failed` in
      `postgres-oracle-target-inventory.csv` — infeasible without a parallel-query
      lock-group abstraction goopg does not have; already tracked with no
      actionable backlog under **M0119-0008** (2026-06-29 triage), so it carries
      no ledger row of its own here either. The one loose end this item's own
      narrative left open — "UPDATE/DELETE conflict-wait-on-a-conflicting-locker"
      (the producer only *preserves* a non-conflicting locker into a
      `{updater+survivors}` MultiXactId; it never makes the writer *wait* on a
      still-active *conflicting* one) — does not block any spec's pass/fail today
      (pre-existing behaviour, no regression per the 0118-0012 ledger row), so it
      is promoted to its own open ledger row (**M0119-0009**, appended this loop)
      rather than continuing to gate this checkbox. M0118 is now fully complete
      (0001–0009 all `[x]`); per the Current Priority banner, next milestone up is
      M0110.
- [x] **M0118-0005** — FK / referential-integrity concurrency: fk-contention,
      fk-deadlock{,2}, fk-partitioned-{1,2}, referential-integrity, ri-trigger,
      temporal-range-integrity. **PARTIAL (2026-06-22, design 0118-0023):** five
      specs promoted to pass-required (strict) with NO engine change —
      `referential-integrity`, `temporal-range-integrity`, `fk-snapshot`,
      `fk-contention`, `fk-deadlock2` already match PG 18.3 (FK KEY-SHARE-vs-non-key-
      UPDATE non-conflict rides the M0118-0003/0004 multixact lock-only producer; SSI
      specs ride the 40001 machinery). Switched 3 dedicated tests soft→`runIsoSpecStrict`
      + added `TestPort_IsolationFk{Contention,Deadlock2}`. **`fk-deadlock` PROMOTED
      (2026-06-25, design 0118-0094):** the FK existence scan `scanRelForFKMatch`
      was made FOR-KEY-SHARE-aware — it waits on a matched parent row's in-flight
      `xmax` only when that `xmax` is a key-changing modification (key UPDATE with
      `HEAP_KEYS_UPDATED`; structurally-detected DELETE via self-pointing/invalid
      `t_ctid`; or a multixact updater member `StatusUpdate`) and treats a
      concurrent no-key UPDATE (`StatusNoKeyUpdate`) as a clean match, so a child
      INSERT no longer blocks where PG proceeds (sibling-paths fix vs `lockRowsOp`,
      which already keyed its wait on `keysUpdated`); the only blocking left is the
      two parent no-key UPDATEs serialising via the existing UPDATE-conflict path.
      New helpers `fkXmaxIsKeyChanging` + `multixactUpdaterIsKeyChanging`; strict
      `TestPort_IsolationFkDeadlock` 14/14 byte-identical. **`ri-trigger`
      PROMOTED (2026-06-25, design 0118-0097):** trigger-based RI under
      SERIALIZABLE now matches PG 18.3 byte-for-byte (all 10 perms, strict
      `TestPort_IsolationRiTrigger`) after three plpgsql/trigger fixes: (1)
      `fireTriggers` now returns `(Row,bool,error)` and PROPAGATES a trigger
      body's `RAISE` (was silently swallowed at all ~21 INSERT/UPDATE/DELETE/
      MERGE/upsert call sites — a real correctness bug: user-trigger constraints
      were ignored), (2) `PERFORM` accepts a full query form (`PERFORM TRUE FROM
      t WHERE …` runs as `SELECT <query>`; scalar `PERFORM foo()` fast path
      kept), (3) `FOUND` implemented as a per-frame bool set from the last SQL
      statement's row count. **Remaining (deferred, ledger 2026-06-22):**
      `fk-partitioned-1/2` (`ALTER TABLE ATTACH PARTITION` + partitioned-FK
      enforcement). Group stays open until those land.
      **2026-06-25 enabler (design 0118-0118, NOT a promotion):** ATTACH PARTITION
      of a partitioned parent carrying a FOREIGN KEY now clones the referencing FK
      onto the attached partition and validates its EXISTING rows against the
      referenced table (PG `ATExecAttachPartition`→`CloneForeignKeyConstraints`→
      `RI_Initial_Check`) — new `cloneAndValidateAttachPartitionFKs` runs
      `fullTableFKCheck` (per-row `assertParentExists`→wait-aware
      `scanTableForMatchFKWait`, descends the referenced partitioned table and
      BLOCKS on an in-flight referenced-row delete); `fkConstraintName` now honours
      an explicit `fk.Name` so the clone carries the parent constraint name. Wired
      at statement time so the FOR-KEY-SHARE wait renders `<waiting ...>` and the
      23503 surfaces during the ALTER. All **Class A** `fk-partitioned-1`
      permutations (referenced row deleted before/during attach →
      `insert or update on table "pfk1" violates foreign key constraint
      "pfk_a_fkey"`) byte-identical to PG 18.3; first divergence moved to the
      first **Class B** perm. `fk-partitioned-1` stays `defer` — Class B
      (referenced-side `pfk_a_fkey_1` restrict "on table pfk" + lock-held-to-commit
      so a concurrent `delete from ppk1` `<waiting ...>` behind an uncommitted
      attach) is the remaining slice (ledger 2026-06-25).
      **`fk-partitioned-1` PROMOTED (2026-06-25, designs 0118-0119 + 0118-0120):**
      committed Class B = new `enforceFKOnDeletePartitionAncestor` walks the
      deleted leaf's partition-ancestor chain, skips committed per-partition FK
      clones (`IsPartitionChild`) so the ROOT `pfk` names the violation, appends
      the leaf's 1-based ordinal as `<fkname>_<N>` (`pfk_a_fkey_1`). Concurrent
      Class B = the deferred ATTACH records its XID (`catalog.pendingAttachXID`,
      set when the parent has FKs, cleared on COMMIT/ROLLBACK); a concurrent
      `DELETE FROM ppk1` that finds a referencing row in a not-yet-registered
      partition with an active pending-attach XID `WaitForXID`s it, refreshes the
      snapshot, and re-evaluates (now the clone is skipped + ROOT named), so the
      delete renders `<waiting ...>` then errors once the attach commits. All 18
      active perms byte-identical to PG 18.3; strict `TestPort_IsolationFkPartitioned1`.
      **`fk-partitioned-2` PROMOTED (2026-06-25, design 0118-0121) — GROUP CLOSED:**
      FK referencing a partitioned table (`pfk(a) references ppk`, both list-
      partitioned). Two gaps: (A) INSERT-side `scanTableForMatchFKWait` now raises
      `40001 could not serialize access due to concurrent update` (not `23503`)
      when the FOR-KEY-SHARE wait finds the parent row key-changed by a COMMITTED
      concurrent xact under REPEATABLE READ / SERIALIZABLE (PG `heap_lock_tuple`
      `HeapTupleUpdated`); (B) `DELETE FROM` the partitioned parent `ppk` (routed
      to leaf `ppk1`) now names the per-partition clone — `enforceFKOnDelete`
      routes the deleted row to its leaf via `routeToPartition`, skips the
      parent-named NO ACTION/RESTRICT `assertNoChildRows` for a partition-leaf row
      (the unconditional `fkChildWaitForInFlightInsert` still serialises), and runs
      `enforceFKOnDeletePartitionAncestor` from the leaf so it reports
      `pfk_a_fkey_1 on table pfk`. Follow-up: `fkDeleteAncestorPass` now honours
      DEFERRABLE INITIALLY DEFERRED FKs (queues a deduped deferred check, no
      immediate raise) so `fk-snapshot`'s delete+re-insert stays green. All six
      perms byte-identical to PG 18.3; strict `TestPort_IsolationFkPartitioned2`.
      **M0118-0005 group fully promoted to pass-required.**
- [x] **M0118-0006** — MERGE & INSERT ON CONFLICT output parity: merge-{update,delete,
      insert-update,match-recheck,join}, insert-conflict-do-update-{2,3,4},
      insert-conflict-specconflict, insert-conflict-do-nothing-2. **COMPLETE
      (2026-06-22, design 0118-0022).** All ten specs already matched PG 18.3
      byte-for-byte by construction (MERGE executor + ON CONFLICT arbiter +
      SSI/REPEATABLE-READ semantics were already correct from prior milestones);
      this loop PROMOTED the group to pass-required with no engine change. New
      `runIsoSpecStrict` helper hard-asserts a `pass` status (a non-pass result is
      now a red test, not a silent `t.Skip` as with `runIsoSpec`), and the ten
      dedicated `TestPort_IsolationMerge*` / `TestPort_IsolationInsertConflict*`
      functions were switched to it. D-002 CSV rationale updated.
- [x] **M0118-0007** — Planner / output-format blockers: eval-plan-qual (planner
      RETURNING support), drop-index-concurrently-1 (EXPLAIN EXECUTE plan-format parity).
      **COMPLETE (2026-06-25, design 0118-0106): `eval-plan-qual` PROMOTED — group
      closed.** All three specs (drop-index-concurrently-1, eval-plan-qual-trigger,
      eval-plan-qual) now strict-pass byte-for-byte vs PG 18.3. The last divergence was
      the EPQ-over-join `selectresultforupdate` case: `SELECT … FOR UPDATE OF jt` over a
      join whose locked `jt` is the inner index scan — goopg folded the index key
      condition `jt.id = y` into the per-row EvalPlanQual recheck, but `y` is another
      join input's column whose `ColumnRef.Index` lives in the join coordinate space,
      misread against the 2-col jointest tuple as `jt.id = jt.data` → post-update row
      wrongly dropped (0 rows). Fixed by only folding a row-local/constant index key into
      the recheck (`exprRefsColumnOrOuter` guard); join/correlated keys excluded (CTID-
      chain logic still catches key-column changes). Sibling fix: a lazy hash join now
      preserves build-side heap ctids (`markJoinPreserveCTID`/`preserveCTIDRel`/
      `lazyHashCTID`/`buildHashRightWithCTID`, stamped by `nextLazy`) so FOR UPDATE over
      the hash-join variant recovers the locked TID after the build scan is drained+closed
      (nil/no-op for queries without FOR UPDATE → TPC-H hash-join hot path untouched).
      `TestPort_IsolationEvalPlanQual` strict 50/50; EPQ-trigger+row-lock regression PASS;
      `-race` executor; TPC-H Q12/Q13 spot-check; pgbench smoke.
      **PARTIAL (2026-06-22, design 0118-0024):** `drop-index-concurrently-1` promoted
      to pass-required with NO engine change — it matches PG 18.3 byte-for-byte (DROP
      INDEX CONCURRENTLY two-phase invalidation + index→seqscan EXPLAIN plan-format
      fallback + READ COMMITTED visibility all already correct). Switched
      `TestPort_IsolationDropIndexConcurrently1` soft→`runIsoSpecStrict`.
      **`eval-plan-qual-trigger` PROMOTED (2026-06-25, design 0118-0095):** the
      harder half of the EPQ output-parity pair already matches PG 18.3 byte-for-byte
      with NO engine change — all 38 active permutations stack BEFORE/AFTER row
      triggers (plpgsql `trig_report`) on READ COMMITTED EPQ rechecks, key-update
      CTID-chain following, ON CONFLICT DO UPDATE upserts, and REPEATABLE READ 40001
      failures, all with RETURNING + NOTICE-emitting `noisy_oper()` WHERE quals;
      goopg's EvalPlanQual re-projects through the trigger queue and the upsert
      arbiter exactly as PG. Switched `TestPort_IsolationEvalPlanQualTrigger`
      soft→`runIsoSpecStrict`. **Remaining (deferred, ledger 2026-06-22):**
      `eval-plan-qual` — a cross-table EvalPlanQual recheck returns `(0 rows)` where
      PG re-projects the updated row after a concurrent UPDATE (EPQ-over-join
      executor + EXPLAIN/column-format work, ~L1171 of expected). Group stays open.
- [x] **M0118-0008** — DDL / VACUUM / maintenance concurrency: alter-table-{1,2,3,4},
      detach-partition-concurrently-{1,2,3,4}, partition-concurrent-attach,
      partition-drop-index-locking, reindex-concurrently{,-toast}, reindex-schema,
      multiple-cic, vacuum-{concurrent-drop,conflict,no-cleanup-lock,skip-locked},
      truncate-conflict, sequence-ddl, cluster-conflict{,-partition}, create-trigger,
      inherit-temp, plpgsql-toast.
      **2026-06-28 regression fix (design 0118-0134): `vacuum-concurrent-drop` +
      `vacuum-skip-locked` restored to green.** Both pass-required specs had been
      silently RED since commit d1f40e28 (design 0118-0090 async-notify), found by
      git-bisecting `TestPort_IsolationVacuumConcurrentDrop`. Their lock step is a
      single two-statement step `{ BEGIN; LOCK part1 IN SHARE MODE; }`; the
      async-notify loop changed the isolation runner to send a multi-statement step
      as ONE simple-query message (`execMultiStatement`). That exposed a latent
      server bug: `dispatchSimpleQueryViaExecutor` seeded `ectx.TxnLockBackendID`
      ONCE before the per-statement loop from the message-entry txn state (autocommit
      for `BEGIN; LOCK …`), so when `LOCK` ran later in the same loop it saw
      `TxnLockBackendID==0` and `acquireRelLockTxn` degraded to a display-only no-op
      — no real ShareLock, so the concurrent ANALYZE/VACUUM never `<waiting>`-blocked
      and the drop re-check WARNING never fired. Fix refreshes `ectx.TxnLockBackendID`
      at the top of the statement loop from the LIVE `connTx.InExplicit()` state.
      Strict `TestPort_IsolationVacuumConcurrentDrop`/`VacuumSkipLocked` PASS.
      **COMPLETE (2026-06-24, loop #24).** All 25 specs are strict-promoted
      (`runIsoSpecStrict`) and byte-for-byte vs PG 18.3 — `reindex-concurrently-toast`
      (design 0118-0088) was the last, closing the TOAST-exposure epic. This loop
      reconciled the lagging D-002 inventory: 34 strict-passing isolation specs
      (the M0118-0008 group + earlier M0118-0005/0006/0007 promotions whose CSV rows
      were never flipped) set `failed`→`pass` in
      `postgres-oracle-target-inventory.csv`, regenerated
      `upstream-isolation-coverage.md` + `postgres-oracle-target-inventory.md`.
      Smoke-verified `Reindex­ConcurrentlyToast`/`AlterTable4`/`PlpgsqlToast` strict
      PASS (24.6 s). Isolation tally now 101 pass / 20 failed (remaining 20 span
      M0118-0002/0004/0005/0007/0009 — distinct unbuilt subsystems).
      **PARTIAL (2026-06-22, design 0118-0027):** probe-first ranked all 25 specs;
      none passed as-is (the group's hard tail). `create-trigger` promoted to
      pass-required — CREATE TRIGGER now takes a txn-scoped ShareRowExclusiveLock
      (`acquireDDLLockTxn`) and INSERT/UPDATE/DELETE a txn-scoped RowExclusiveLock
      (`acquireWriteLockTxn`), the write/DDL siblings of the read-side
      `acquireScanReadLockTxn` (0118-0018); a concurrent UPDATE blocks until the
      CREATE TRIGGER txn commits while SELECT…FOR UPDATE (RowShareLock) proceeds.
      Same confinement (no-op in autocommit + system catalogs); RowExclusiveLock
      self-compatible so concurrent DML never blocks at table level (pgbench
      smoke 0-failed). `TestPort_IsolationCreateTrigger` strict PASS.
      **2026-06-22 second promotion (design 0118-0028): `sequence-ddl` PROMOTED.**
      `nextval()` now takes a transaction-scoped `RowExclusiveLock` on the
      sequence relation (new `acquireSequenceLockTxn`: held to commit inside an
      explicit txn; transient acquire+release under the globally-unique
      per-statement `BackendID` in autocommit so the wait still happens during
      acquisition — a single autocommit statement is its own implicit txn) and
      `ALTER SEQUENCE` takes an `AccessExclusiveLock` via `acquireDDLLockTxn`
      (mirrors upstream `lock_and_open_sequence`). Sequences are virtual catalog
      relations (`IsSequence`, user OID) reached via `LookupTable`→`RelFileNode`;
      `RowExclusiveLock` self-compatible so concurrent nextvals/SERIAL inserts
      never block. `TestPort_IsolationSequenceDdl` strict PASS (5 perms);
      `-race` lockmgr/executor; pgbench smoke 0-failed.
      **2026-06-22 third promotion (design 0118-0029): `reindex-concurrently`
      PROMOTED.** Two fixes: (1) `parseReindex` now accepts `CONCURRENTLY` in the
      modern post-type position (`REINDEX TABLE CONCURRENTLY name`), not only the
      legacy pre-type spelling; (2) new `(*Context).waitForRelationLockers` (the
      `WaitForLockers` analog) polls `tableLockMgr.Holders` and returns once no
      OTHER backend holds a lock on the table, taking NO lock of its own so
      concurrent reads/writes proceed (the CONCURRENTLY contract — REINDEX holds
      only ShareUpdateExclusive). The runner detects blocking by timing (300 ms)
      so the poll loop renders as `<waiting ...>` and completes the instant the
      lockers drain; a bare `BEGIN` registers no table lock so perm 1 returns
      immediately while blocking perms complete after the last concurrent commit.
      Read-only on lockmgr, gated on `Concurrently && TABLE` ⇒ zero hot-path
      blast radius; reusable by the other CONCURRENTLY specs.
      `TestPort_IsolationReindexConcurrently` strict PASS (6 perms); `-race`
      lockmgr/executor; parser units; pgbench smoke.
      **2026-06-22 fourth promotion (design 0118-0030): `reindex-schema`
      PROMOTED.** `reindexOp.Next` gained a `SCHEMA` case: it enumerates the
      schema's non-virtual user tables (`Catalog.TablesInSchema`), sorts by OID
      (creation) order, then per relation takes a `ShareLock` (plain reindex) or —
      for `CONCURRENTLY` — waits for lockers via the 0118-0029
      `waitForRelationLockers` primitive. `ShareLock`/`ShareUpdateExclusive`
      conflict with the `SHARE UPDATE EXCLUSIVE` held by `lock1`, so the reindex
      stalls on the earliest-created locked table (`tab_locked`) and never
      reaches `tab_dropped`, letting a concurrent `DROP TABLE` proceed. The
      autocommit lock uses the new `(*Context).acquireRelLockMaybeTransient`
      (generalised out of `acquireSequenceLockTxn`: held-to-commit in an explicit
      txn, transient acquire+release in autocommit so the wait still happens
      during acquisition). `TestPort_IsolationReindexSchema` strict PASS (2
      perms); `-race` lockmgr; lock-sibling regression PASS; pgbench smoke
      0-failed.
      **2026-06-22 fifth promotion (design 0118-0031): `multiple-cic` PROMOTED.**
      Two simultaneous `CREATE INDEX CONCURRENTLY` builds whose partial-index
      `WHERE` predicates call IMMUTABLE advisory-lock functions on EMPTY tables.
      Two engine fixes + one runner fix: (1) **const-fold** — `execCreateIndex`
      now evaluates a partial-index predicate that references no table columns
      ONCE at build time (new exported `planner.ExprContainsColumnRef` guard +
      `evalExpr(pred,nil,ctx)`), mirroring PG `eval_const_expressions` in
      `BuildIndexInfo`, so the IMMUTABLE fn's advisory-lock call fires despite
      zero rows and `s1i` blocks; stored predicate untouched (pg_get_indexdef/
      pg_dump unaffected); (2) **CIC drain** — `CreateIndexStmt.Concurrently` is
      now recorded by the parser and a concurrent build captures the active-txn
      slot set at START (`mvcc.SnapshotActiveOtherSlots`, refactored out of
      `WaitForOlderSlotsToCommit`) and drains it after the build
      (`WaitForSlotsToCommit`) so the newer build (`s2i`) completes only after the
      older (`s1i`) — start-time snapshot (not wait-time) avoids a mutual wait;
      (3) **runner** — the `(*)` star branch drains UNGATED pending steps before
      the star step's own completion (`partitionGatedOn` keeps
      `BlockerStepComplete`-gated steps like deadlock-hard `s7a8(s8a1)` reported
      after), matching isolationtester dispatch-order completion. Gated on
      `Concurrently` (plain CREATE INDEX unaffected; only multiple-cic +
      deferred prepared-transactions-cic use CIC). `TestPort_IsolationMultipleCic`
      strict PASS; every strict `(*)` spec (deadlock-{hard,soft,soft-2},
      classroom-scheduling, project-manager, serializable-parallel{,-2},
      temporal-range-integrity, multixact-no-deadlock,
      tuplelock-upgrade-no-deadlock, timeouts) PASS; lock-sibling regression PASS;
      `-race` mvcc/lockmgr; parser/planner/executor units; pgbench smoke.
      **2026-06-22 sixth promotion (design 0118-0032): `alter-table-3`
      PROMOTED.** Mixes `ALTER TABLE … ENABLE/DISABLE TRIGGER` with a concurrent
      `SELECT … FOR UPDATE` and a duplicate-key `INSERT`. Two engine fixes:
      (1) the ENABLE/DISABLE TRIGGER parser arm (was a pure no-op) now flags
      `AlterTableStmt.EnableDisableTrigger` and `execAlterTable` takes a
      transaction-scoped `ShareRowExclusiveLock` via the existing
      `acquireDDLLockTxn` (mirrors PG `AlterTableGetLockLevel`), conflicting with
      a concurrent INSERT's `RowExclusiveLock` (waits) but not FOR UPDATE's
      `RowShareLock` (proceeds); (2) `connTxState.Fail()` now releases the failed
      transaction's `tableLockMgr` locks immediately
      (`ReleaseTableLocks(LockBackendID)`), gated on `SavepointDepth()==0`,
      mirroring PG `AbortTransaction` dropping heavyweight locks at abort (NOT at
      the explicit ROLLBACK — verified zero `pg_locks` rows on real PG 18.3 while
      `idle in transaction (aborted)`), so a later conflicting ALTER doesn't wait
      on a dead failed-INSERT lock. `TestPort_IsolationAlterTable3` strict PASS
      (48 perms); lock-sibling + savepoint/abort (`delete-abort-savept{,2}`,
      `aborted-keyrevoke`) + SSI regression PASS; `-race` lockmgr/server;
      parser/executor units; pgbench smoke.
      **2026-06-22 seventh promotion (design 0118-0033): `vacuum-skip-locked`
      PROMOTED.** `VACUUM`/`ANALYZE (SKIP_LOCKED)` against a partitioned table
      while another session holds `part1` in `SHARE`/`ACCESS EXCLUSIVE`. Three
      fixes: (1) new `(*Context).tryAcquireMaintenanceLock` (mirrors PG
      `ConditionalLockRelationOid`) — conditional `TryAcquire` of
      `ShareUpdateExclusiveLock` (or `AccessExclusiveLock` for `VACUUM FULL`)
      under the active backend, release-immediately, `false` on contention ⇒ skip
      not wait; (2) `expandVacuumTargets`/`expandAnalyzeTargets` tag each relation
      explicit (user-named ⇒ `WARNING: skipping vacuum/analyze of "X" --- lock not
      available` via `AddWarning`) vs expanded (partition child ⇒ silent skip) and
      record partitioned parents; `ANALYZE` of a partitioned parent then reads
      each leaf partition under a BLOCKING `AccessShareLock`
      (`analyzeInheritanceWait` → `acquireRelLockMaybeTransient`) — `SKIP_LOCKED`
      does not cover the inheritance scan — so ANALYZE waits under `ACCESS
      EXCLUSIVE` (conflicts with AccessShare) but not `SHARE` (compatible); plain
      VACUUM never waits; (3) the isolation runner now echoes each captured server
      message with its real protocol severity (`pq.Error.Severity`) instead of a
      hard-coded `NOTICE:`, so `WARNING:` lines render (NOTICE-emitting specs
      unchanged). `TestPort_IsolationVacuumSkipLocked` strict PASS (16 perms);
      full `TestPort_Isolation*` suite PASS (no severity-change regression);
      framework + executor vacuum/analyze units; `-race` lockmgr; pgbench smoke.
      **2026-06-22 eighth promotion (design 0118-0035): `vacuum-concurrent-drop`
      PROMOTED.** `VACUUM`/`ANALYZE` of partition targets while a concurrent
      session holds `part1` in `SHARE` then DROPs `part2` and commits. Two fixes
      mirroring `vacuum.c` `vacuum_open_relation`: (1) on the **non-SKIP_LOCKED**
      path the per-target loop now takes a BLOCKING `ShareUpdateExclusiveLock`
      (or `AccessExclusiveLock` for `VACUUM FULL`) via
      `acquireRelLockMaybeTransient` so `s2` waits behind `LOCK part1 IN SHARE
      MODE` (`<waiting ...>`) instead of proceeding immediately — previously a
      heavyweight lock was taken only on the SKIP_LOCKED path; (2) after the lock
      the target is re-checked against the live catalog via the new
      `relationStillExists` helper (`InMemory.LookupTableByOID`, the
      `try_relation_open` analog) and a target DROPped by the committing session
      is skipped — `WARNING: skipping vacuum/analyze of "X" --- relation no
      longer exists` only for an explicit (`vacuumTarget.explicit`) target,
      silently for an expanded partition child. `TestPort_IsolationVacuumConcurrentDrop`
      strict PASS (6 perms); `TestPort_IsolationVacuumSkipLocked` PASS (no
      regression); executor vacuum/analyze units; `-race` lockmgr; pgbench smoke.
      **2026-06-22 SRF-in-expression enabler (design 0118-0034, NOT a spec
      promotion):** fixed a silent row-dropping correctness bug — a
      set-returning function nested inside a larger SELECT-list scalar
      expression (e.g. `generate_series(1,1000) % 4`) collapsed to ONE row
      (the SRF's start value) instead of one row per element, so
      `INSERT INTO b SELECT generate_series(1,1000) % 4` inserted 1 row not
      1000. `buildSelectSrfProjectSet` only expanded a BARE target SRF; a
      nested SRF fell through to the scalar `generate_series` fallback. Fix:
      detect a nested generate_series, expand it into a temp eval-row slot
      (`ChildWidth+k`), and apply a WRAPPER expr (the resolved target with the
      SRF `FuncCall` replaced by a `ColumnRef` to that slot) per step (new
      `findFirstNestedSRF`/`replaceExprNode`; `SrfCol.Wrapped`,
      `ProjectSet.{Wrappers,ChildWidth,EvalRowWidth}`; executor builds the
      eval row + evaluates wrappers). Bounded to generate_series-when-nested;
      bare/FROM SRFs + no-wrapper path byte-identical. Unblocks the
      `alter-table-1/2` data setup (still deferred on FK `NOT VALID` parse +
      `VALIDATE CONSTRAINT` + lock semantics). Gates:
      `TestSelectListSRFInsideExpression`; planner+executor units;
      regress-port; pgbench smoke.
      **2026-06-22 inherit-temp foundation (design 0118-0036, NOT a spec
      promotion):** laid the per-session temp-relation ownership concept the
      `inherit-temp` spec needs. goopg keeps all relations in ONE shared catalog,
      so a parent's inheritance expansion wrongly scans ANOTHER session's temp
      child (PG isolates each backend's `pg_temp_N` via `RELATION_IS_OTHER_TEMP`):
      `s1`'s `SELECT a FROM inh_parent` returns 6 rows vs PG's 4. A faithful fix
      is multi-site (planner SELECT + UPDATE/DELETE/TRUNCATE/FK/MERGE expansion)
      and must land atomically (sibling-paths discipline / silent-row-count risk),
      so this loop landed the zero-blast-radius central pieces only:
      `catalog.Table.TempOwner`, the shared filter
      `catalog.AccessibleInheritanceChildren(children, owner)` (the single
      chokepoint the wiring loop calls at every site), a stable
      `config.SessionRegistry.UniqueID()` per-connection identity, and
      `executor.sessionTempOwner(ctx)` (`"s<id>"`) stamped at both
      `CREATE TEMPORARY TABLE` sites. Nothing reads `TempOwner` in live paths yet
      (behaviour unchanged). Units `TestAccessibleInheritanceChildren`/
      `TestSessionRegistryUniqueID`/`TestSessionTempOwner`; catalog/config/
      executor packages green. Resume = the wiring fan-out + planner-token
      plumbing (enumerated in the design doc / ledger).
      **2026-06-22 ninth promotion (design 0118-0037): `inherit-temp` PROMOTED ⇒
      all 9 permutations byte-for-byte.** Wired the 0118-0036
      `catalog.AccessibleInheritanceChildren` filter into every data-path
      inheritance expansion the spec exercises: planner SELECT
      (`collectInheritanceDescendants`; owner threaded via new
      `SearchPathCatalog.TempOwnerToken`/`CurrentTempOwner()` +
      `planner.currentTempOwner` wrapper walk, set in
      `sessionPlanCatalog`/`ctxPlanCatalog`) and executor
      UPDATE/DELETE/UPDATE…FROM/DELETE…USING/TRUNCATE (owner from
      `sessionTempOwner(ctx)`). Two load-bearing additions: (1) the cross-session
      plan cache (M0098-0005) is BYPASSED when
      `catalog.HasTempInheritanceChildren()` is true (else `s1`'s cached
      `SELECT a FROM inh_parent` plan is wrongly served to `s2` →
      `s2_select_p` returned 1,2,3,4 not 1,2,5,6) — gated in BOTH simple
      (`dispatch.go`) + extended (`dispatch_extended.go`) paths; (2)
      TRUNCATE-blocks-parent-scan — `execTruncate` now takes a txn-scoped
      `AccessExclusiveLock` (`acquireDDLLockTxn`) and the scan-read hook
      `acquireScanReadLockTxn` now takes a TRANSIENT `AccessShareLock` in
      autocommit (`acquireRelLockMaybeTransient`) so a bare `s2_select_p` parks
      behind s1's in-progress `TRUNCATE inh_parent` while `s2_select_c` (own temp
      child, no parent scan) proceeds. `PartitionChildren`/FK/MERGE/VACUUM
      inheritance NOT filtered (temp partition of a permanent parent is illegal in
      PG; FK/MERGE/VACUUM unexercised by any `port` spec — bounded follow-up,
      ledger). Hot-path: only bare autocommit reads newly lock; pgbench smoke
      0-failed, `-S` ~14.7k TPS/0.136 ms. Gates: `TestPort_IsolationInheritTemp`
      strict PASS; full `TestPort_IsolationSuite` + all dedicated strict
      `TestPort_Isolation*` PASS; catalog/config/planner/server/executor units;
      pgbench smoke.
      **2026-06-23 tenth promotion (design 0118-0039): `truncate-conflict`
      PROMOTED — first of the `*-conflict` family, all 8 permutations
      byte-for-byte.** A TRUNCATE-scoped privilege model: new catalog ACL store
      (`tableACLs`; `Catalog.GrantTablePrivilege`/`HasTablePrivilege`/`DropTableACL`),
      `SET ROLE`/`RESET ROLE` now track `connTx.NonSuperuserRole`, an autocommit
      table-level `GRANT … ON … TO …` recorder (`server/grant_ddl.go`), and a
      **pre-lock** check in `execTruncate` (`NonSuperuserRole != "" &&
      !HasTablePrivilege(oid,role,"TRUNCATE")` ⇒ `42501 permission denied for
      table <name>` immediately, no wait; superuser/owner bypass). Also fixed the
      CREATE-ROLE batch-swallow (working-set bug): the setup
      `CREATE ROLE …; CREATE TABLE …;` is one batch the parser can't parse, and
      the recovery path handed the whole batch to `tryHandleRoleDDL` which
      dropped the `CREATE TABLE` — new `splitLeadingRoleDDL`/
      `firstTopLevelSemicolon` peel the leading role stmt off and recurse on the
      remainder (standalone role DDL untouched). And `execTruncate` switched from
      `acquireDDLLockTxn` (no-op in autocommit) to `acquireRelLockMaybeTransient`
      so a granted autocommit TRUNCATE waits behind a concurrent open SELECT
      (preserves `inherit-temp`, whose TRUNCATE is in an explicit txn).
      `TestPort_IsolationTruncateConflict` strict PASS; all sibling M0118-0008
      specs PASS; createuser/dropuser/amcheck PASS; catalog/parser/server/executor
      units; `-race`; pgbench smoke 0-failed ~15.2k TPS.
      **2026-06-23 eleventh promotion (design 0118-0040): `vacuum-conflict` (16
      perms) + `cluster-conflict` (2 perms) PROMOTED — ownership-based maintenance
      privilege.** Unlike `truncate-conflict`'s grantable privilege, VACUUM/
      ANALYZE/CLUSTER key on **table ownership**. Four changes: (1) new
      `catalog.Table.Owner` (role name; empty = bootstrap superuser; pg_class
      output unchanged); (2) `ALTER TABLE … OWNER TO role` now records the owner —
      parser captures it into `AlterTableStmt.OwnerTo` (was discarded), executor
      sets `tbl.Owner` + takes a txn-scoped `AccessExclusiveLock`
      (`AlterTableGetLockLevel`, no-op in autocommit); (3) new
      `maintenancePermitted(ctx,tbl)` wired into the explicit-target loop of
      `expandVacuumTargets`/`expandAnalyzeTargets` **before any lock** (mirrors
      `expand_vacuum_rel`'s no-lock pg_class ACL check) — an unprivileged explicit
      target is skipped with `WARNING: permission denied to vacuum/analyze
      "<name>", skipping it` and NO wait; after `ALTER … OWNER TO` the role owns
      the table so the command is permitted and blocks on the conflicting `LOCK …
      IN SHARE UPDATE EXCLUSIVE MODE` until commit; (4) `clusterOp.Next` now takes
      a blocking `AccessExclusiveLock` (`acquireRelLockMaybeTransient`) so CLUSTER
      waits behind the same `SHARE UPDATE EXCLUSIVE` holder then completes. Blast
      radius nil for superuser usage (`Owner` empty ⇒ always permitted; pgbench/
      TPC-H unchanged). `TestPort_IsolationVacuumConflict`/`ClusterConflict` strict
      PASS; sibling M0118-0008 specs PASS; `-race` executor/catalog; catalog/
      parser/executor units; pgbench smoke 0-failed ~15.2k TPS.
      **2026-06-23 twelfth promotion (design 0118-0041): `cluster-conflict-partition`
      (4 perms) PROMOTED — partitioned sibling of `cluster-conflict`, NO engine
      change.** `ALTER TABLE … OWNER TO` does NOT recurse to partition children
      (`tablecmds.c` `AT_ChangeOwner` "never recurses") so only the parent is owned
      by the role. Upstream CLUSTER takes an `AccessExclusiveLock` on the named
      PARENT (waits behind a concurrent `LOCK … IN SHARE UPDATE EXCLUSIVE MODE` on
      the parent then completes on commit — perms 1/2) and enumerates leaves WITHOUT
      locking them, skipping every leaf the role does not own
      (`cluster_is_permitted_for_relation` false; WARNING suppressed by
      `client_min_messages=ERROR`) so a leaf held in SHARE UPDATE EXCLUSIVE is never
      touched and CLUSTER returns immediately (perms 3/4). goopg's `clusterOp.Next`
      (no-op rewrite, 0118-0040) locks only the named parent and never processes
      leaves, so the captured output is byte-identical by both routes; the runner's
      300 ms timing threshold renders the immediate-completion perms without a
      `<waiting ...>` marker. `TestPort_IsolationClusterConflictPartition` strict
      PASS; conflict-family siblings (cluster/vacuum/truncate-conflict) PASS;
      build+vet clean; pgbench smoke (pre-commit hook).
      **2026-06-23 fifteenth promotion (design 0118-0047): `alter-table-1`
      PROMOTED ⇒ all 170 permutations byte-for-byte.** Only new piece on top of
      `alter-table-2`'s ADD FK NOT VALID: `ALTER TABLE … VALIDATE CONSTRAINT name`
      now parses (new `AlterTableValidateConstraint` action; `VALIDATE` is an
      identifier-keyword) and takes only a transaction-scoped
      `ShareUpdateExclusiveLock` via `acquireDDLLockTxn`
      (`AlterTableGetLockLevel` → `AT_ValidateConstraint`, the minimum lock) and
      flips the named FK's `convalidated` `'f'`→`'t'` (unknown name ⇒ 42704).
      `ShareUpdateExclusive` does NOT conflict with `AccessShare` reads /
      `RowShare` FOR UPDATE / `RowExclusive` INSERT, so VALIDATE never blocks the
      reader session and the only wait is the INSERT behind the still-uncommitted
      ADD CONSTRAINT's `ShareRowExclusiveLock` — exactly as alter-table-2.
      `TestPort_IsolationAlterTable1` strict PASS; sibling alter-table-2/3 strict
      PASS; parser/executor units; `go vet` clean.
      **2026-06-23 enabler (design 0118-0048, NOT a promotion):** fixed a
      `DETACH PARTITION child CONCURRENTLY`/`FINALIZE` parser-position bug — the
      optional trailer was consumed BEFORE the child name, so the valid form
      failed with `syntax error … (got concurrently)` and aborted step
      `s2detach` of all four `detach-partition-concurrently-{1,2,3,4}` specs.
      Moved the trailer accept after `parseObjectName`; new AST field
      `AlterTableAction.DetachConcurrently`; FINALIZE accepted+ignored; executor
      unchanged (synchronous detach). Probe confirms detach-1's first divergence
      moved syntax-error → the unmodelled `<waiting ...>` concurrent-wait marker
      (post-detach SELECT rows now correct). Full promotion still deferred on the
      Effort-L two-phase concurrent-detach + transactional-DDL cross-session
      catalog visibility. `TestParseAlterTableDetachPartition` PASS.
      **2026-06-23 enabler (design 0118-0049, NOT a promotion): PL/pgSQL
      transaction control.** `COMMIT;`/`ROLLBACK;` inside a non-atomic DO block
      (top-level / procedure outside an explicit txn) now commit/roll back the
      current transaction and chain into a fresh one; an atomic context (DO
      inside `BEGIN … COMMIT`) raises SQLSTATE `2D000`. New `Context.PLpgSQLCommitChain`
      callback bridges the server-owned txn lifecycle to the executor; the
      dispatch installs it only in auto-commit mode (closure commits/rolls back
      the current `tx`, releases only xact-scoped advisory locks, begins a fresh
      RC tx+snapshot, reassigns the outer `tx`/`snap` + `ctx.Tx`/`ctx.Snap`).
      New `TxControlStmt` AST/parser/runtime. Lifts `plpgsql-toast`'s first
      blocker (`unsupported PL/pgSQL statement` at `COMMIT`); its divergence now
      advances to the next gap — PL/pgSQL `SELECT … INTO var`/record handling
      (deferred, separate Effort-L slice; goopg captures `SELECT … INTO x` as
      raw embedded SQL and mis-parses it as SQL `SELECT … INTO <table>`). Blast
      radius nil (only the new `TxControlStmt` case calls the callback). Gates:
      `TestParseTransactionControl`; `TestPlpgSQLDoCommitChainDurability`/
      `…RollbackChain`/`…CommitInExplicitBlockRejected`;
      `TestPort_IsolationSubxidOverflow`/`FreezeTheDead` strict PASS (no
      regression); executor+server units; `go vet`; pgbench smoke.
      **2026-06-23 enabler (design 0118-0050, NOT a promotion): PL/pgSQL
      `SELECT … INTO`.** `select … into [strict] target[, …] from …` inside a
      PL/pgSQL body now binds the first result row to the named variable(s)
      instead of being mis-parsed as SQL's CREATE-TABLE-AS (`SELECT … INTO
      <table>`). `parseSQLStmt` special-cases a leading SELECT: detects a
      top-level (`depth==0`) `INTO`, peels off optional `STRICT` + the
      comma-separated target list (dotted names allowed), reconstructs the query
      with the INTO clause excised, returns new `*SelectIntoStmt{SQL,Targets,
      Strict}` (plain SELECT still → verbatim `*SQLStmt`). Runtime
      `bindSelectIntoRow` mirrors the FOR-loop conventions: single target+1 col
      → scalar; single target+N cols → record `_<target>_<col>` sub-fields;
      multiple targets → positional scalar; no row ⇒ NULL (schema from
      `op.Schema()` before first Next); STRICT ⇒ P0002/P0003. Lifts
      `plpgsql-toast`'s next blocker after 0118-0049: probe shows `assign1` now
      runs and emits `length(x) = 6000`; divergence advances to subquery-valued
      assignment (`assign2`: `x := (select …)`), expanded record/detoast paths
      (`assign3-6`), and the `<waiting ...>` concurrency marker — all deferred
      (Effort-L). Gates: `TestParseSelectInto`/`TestParseSelectNoIntoIsEmbeddedSQL`;
      `TestPlpgSQLSelectInto`; full executor+plpgsql units; txctl/subxid-overflow
      regression PASS; `go vet`; pgbench smoke = pre-commit hook.
      **2026-06-23 enabler (design 0118-0051, NOT a promotion): PL/pgSQL
      scalar subquery.** A top-level `(SELECT …)` in a PL/pgSQL expression
      (assignment RHS or `RETURN` operand) is now planned+executed against the
      live catalog → first column of the first row (NULL when no row; `21000`
      when >1 row), instead of the blanket `0A000 subqueries are not supported`.
      Intercepted in `evalPLpgSQLExpr` before lowering (a subquery can't lower to
      a `planner.Expr`) via new `evalScalarSubquery` mirroring the
      `SelectIntoStmt`/`ForSelectStmt` machinery (0118-0050). Lifts
      `plpgsql-toast`'s `assign2` blocker (`x := (select test1.b from test1)`);
      probe confirms `assign1`/`assign2` now byte-match PG (both `length(x) =
      6000`) and the first divergence advances to `assign3` (expanded-record
      field assignment `r.b := (select …)` + `length(r::text)` → `6004` vs
      `<NULL>`). A subquery nested inside a larger expr still `0A000` (no spec
      needs it). Executor-only; `TestPlpgSQLScalarSubquery`; `internal/executor`
      PASS; `go build` clean; pgbench smoke = pre-commit hook.
      **2026-06-23 sixteenth promotion (design 0118-0054): `plpgsql-toast`
      PROMOTED ⇒ all 7 permutations byte-for-byte.** The last blocker of the
      spec, on top of enablers 0118-0049..0053. Two executor-only fixes: (1)
      **`assign6` FOR-loop snapshot stability** — `for r in select … loop …
      delete; commit; … end loop` must iterate over all rows fetched at loop
      start even though the body deletes every row and commits each iteration.
      goopg streamed rows from a live operator (`op.Next()` between body runs),
      so after the first `DELETE`/`COMMIT` the scan hit EOF and the loop ran once
      (`6002` not `6002 9002 12002`). `ForSelectStmt` now materializes the full
      result (each row deep-copied; operator closed) BEFORE running any body
      statement, mirroring PG holding the implicit cursor's snapshot across a
      `COMMIT` (made holdable) — also strictly more PG-faithful for the no-commit
      case (a body modifying the scanned table no longer sees its own writes
      mid-scan). (2) **`fetch-after-commit` record-field ref** — `select b into t
      from test1 where a = r.a` failed `42P01 missing FROM-clause entry for table
      "r"`: the `SELECT … INTO` path did NO PL/pgSQL var substitution and the
      substitutor skipped qualified names. `SelectIntoStmt` now calls
      `substitutePlpgsqlFrameVarsInSQL` (as the general embedded-SQL path
      already did), and that function now substitutes a single-level `r.field`
      token when `r` `isRecordVar` (literal from the `_<var>_<field>` binding),
      guarded so a plain `table.column` qualifier is untouched. goopg stores
      `text` inline (no external TOAST chunk for VACUUM to orphan) so the
      missing-chunk detoast correctness the spec guards is satisfied
      structurally; the advisory-lock/VACUUM `<waiting …>` markers ride the
      runner's 300 ms timing. `TestPort_IsolationPlpgsqlToast` strict PASS;
      `TestPlpgSQLForLoopMaterializeAndRecordFieldSubst` + the
      `TestPlpgSQL{RecordForLoopAndText,RecordFieldAndText,SelectInto,
      ScalarSubquery,DoCommitChain*}` suite PASS; full `internal/executor` PASS;
      `TestPort_IsolationSubxidOverflow`/`FreezeTheDead` PASS (no regression);
      `go build` clean; pgbench smoke = pre-commit hook.
      **2026-06-24 promotion (design 0118-0061): `detach-partition-concurrently-3`
      PROMOTED ⇒ all 18 permutations byte-for-byte** (strict
      `TestPort_IsolationDetachPartitionConcurrently3`). Builds the
      incomplete-detach lifecycle: a `DETACH … CONCURRENTLY` cancelled mid-wait
      now LEAVES the partition detach-pending (interrupt path no longer reverts
      `MarkPartitionDetachPending`). Nine gated pieces — persist-on-cancel;
      `already pending detach` guard (55000); ALTER-on-pending guard (55000
      `cannot alter partition X with an incomplete detach`); `pg_partition_tree`
      skips the pending child / NULL-parent standalone root; TRUNCATE omits it
      (DROP still cascades); `DETACH … FINALIZE` clears the mark + takes
      AccessExclusive on the partition via `acquireRelLockMaybeTransient`; DROP
      of the pending child grabs AccessExclusive on the parent; partitioned-parent
      scan now locks the parent (`SeqScan.LockParentOID`→AccessShare); and
      `acquireWriteLockTxn` made symmetric with the read side so an autocommit
      write blocks behind a conflicting AEL. Changes 1–7 gated on
      `DetachPendingEpoch != 0`; 8–9 only conflict with DDL-grade locks. Gates:
      strict PASS; detach-1/2 + create-trigger/alter-table-1/2/3/inherit-temp/
      truncate-conflict/vacuum-conflict/cluster-conflict/timeouts/row-lock/
      write-skew/merge/FK siblings PASS; `-race` executor/lockmgr/mvcc; pgbench
      smoke 0-failed.
      **2026-06-24 partial (design 0118-0062): `detach-partition-concurrently-4`
      FK behaviour landed (NOT yet promoted — cursor work deferred).** detach-4
      asserts inserting a value living only in a concurrently-detaching partition
      fails its FK check EVEN UNDER REPEATABLE READ (PG's `RI_FKey_check` runs
      under the latest snapshot ⇒ a detach-pending partition is invisible to the
      FK check the instant it is marked, while a plain SELECT/cursor in the same
      RR txn still sees the row). goopg filtered detach-pending partitions out of
      the FK existence scan by the ENFORCING STATEMENT's snapshot epoch (design
      0118-0060) — correct for RC (fresh per-stmt snapshot) but wrong for RR
      (txn snapshot predates the detach ⇒ partition not filtered ⇒ FK wrongly
      found the value). Fix: `snapDetachEpoch` (`operators_fk.go`) now returns the
      global `mvcc.CurrentPartitionDetachEpoch()`; scoped to the two FK existence
      scans, ordinary query/cursor expansion untouched (preserves the RR-visible-
      row asymmetry). Probe: FK permutations (RC + RR) now byte-match; residual
      diff confined to the 8 cursor permutations. Siblings detach-1/2/3 +
      FkSnapshot/FkContention/PartitionKeyUpdate1..4 PASS; executor/catalog units
      PASS; pgbench smoke 0-failed.
      **2026-06-24 partial (design 0118-0063): `detach-partition-concurrently-4`
      cursor + abort/cancel permutations LANDED (still NOT promoted — only the 3
      `WHERE CURRENT OF` perms remain).** Three fixes: (1) eager cursor
      materialisation at DECLARE (`dispatch.go`) — mirrors PG opening the portal +
      taking snapshot/locks at DECLARE; the materialising scan takes a txn-scoped
      AccessShare (so the concurrent DETACH parks behind the open cursor) and
      buffers the declaration-time partition set; (2) abort releases the RR/SSI
      pinned snapshot — new `mvcc.ReleasePinnedSnapshot`/`WaitForPinnedSnapshotsReleased`
      + `connTxState.ReleasePinnedSnapshotOnFail` (gated `SavepointDepth()==0`),
      mirroring PG `AbortTransaction` dropping the snapshot the instant a top-level
      statement errors (so a detacher unblocks at the error, before the explicit
      ROLLBACK/COMMIT); (3) cancel-message mapping — the detach pinned-snapshot
      wait now maps through `lockWaitCancelError` (57014 "canceling statement due
      to user request"). Probe: first divergence moved spec L80 → the 3 updcur
      perms. detach-1/2/3 + vacuum-no-cleanup-lock + alter-table-1/2/3 +
      conflict-family + FK + SSI + inherit-temp + savepoint/abort siblings PASS.
      **2026-06-24 promotion (design 0118-0064): `detach-partition-concurrently-4`
      PROMOTED ⇒ all 21 permutations byte-for-byte** (strict
      `TestPort_IsolationDetachPartitionConcurrently4`). Closed the last 3
      (`WHERE CURRENT OF`) perms via two FK behaviours (NOT positioned-DML):
      (Fix 1) **UPDATE now fires the RI_FKey_check parent-existence assertion** —
      goopg only ran `checkFKInsert` from `insertOp`; `updateOp` did no parent
      lookup, so `update d4_fk set a=1` (value 1 lives only in the
      concurrently-detaching partition, invisible to the latest snapshot per
      0118-0062) silently succeeded. New `updateOp.childFKsToRecheck()` (FKs whose
      referencing cols are in the SET list — mirrors PG firing the RI AFTER-trigger
      only on a key-column change, bounding blast radius to FK-key UPDATEs on FK
      tables) + `recheckChildFKs()` (delegates to new `checkFKInsertForConstraints`),
      wired into all 3 write paths (`Next` seqscan / `updateViaIndex` /
      `updateWithFrom`) after BEFORE triggers, before the heap write ⇒ raises
      23503. (Fix 2) **DETACH re-validates `RI_PartitionRemove_Check` after the
      hybrid wait** — the first check ran before the wait under the
      statement-start snapshot, missing a referencing row a waited-on session
      committed during the wait; the detacher now re-runs it with a FRESH snapshot
      (`TxnMgr.SnapshotFor(Tx).Clone`) whose `PartitionDetachEpoch` is forced to 0
      so `routeToPartition` keeps the now-pending child in the routing set and the
      violation is recognised (`d4_fk_a_fkey_1`). `WHERE CURRENT OF` positioned-DML
      is indistinguishable here (`d4_fk` has 1 row) and is a separate project-wide
      follow-up (ledger). Gates: strict PASS (21 perms); detach-1/2/3 +
      Fk{Snapshot,Contention,Deadlock2}/ReferentialIntegrity/TemporalRangeIntegrity
      + PartitionKeyUpdate1..4 + Merge* + InsertConflictDoUpdate* PASS; regress-port
      foreign_key/update/constraints/inherit no regression; `-race`
      ./internal/executor/; `go build` clean; pgbench smoke (pre-commit).
      **Remaining (deferred):** `WHERE CURRENT OF` positioned UPDATE/DELETE
      (project-wide, parsed but no executor site consumes `CurrentOf`; needs
      per-row CTID capture in the cursor + a CTID-restricted rewrite — see ledger),
      `alter-table-4` (INHERITS + transactional-DDL cross-session visibility),
      `partition-concurrent-attach` (transactional partition visibility),
      `partition-drop-index-locking` (pg_locks view parity),
      `reindex-concurrently-toast` (toast relations as catalog objects +
      `allow_system_table_mods`). Group stays open.
      **2026-06-24 enabler (design 0118-0066, NOT a promotion): PL/pgSQL
      single-column `SELECT … INTO record` keeps field access.** `bindSelectIntoRow`
      (0118-0050) scalar-shortcut a single-column `SELECT … INTO` straight onto the
      target even when it is a `record` var (`DECLARE r record`), so the
      `_<var>_<col>` sub-field + `compositeVarFields` entry the qualified-name
      expression path reads were never registered ⇒ `r.table_name` hit the catch-all
      `qualified names are not supported in PL/pgSQL expressions (0A000)`. Guarded
      the scalar shortcut with `!frame.isRecordVar(name)` so a record target routes
      to `bindRecordRowComposite` (the multi-column `SELECT * INTO r` path,
      0118-0054); `r.field` resolves via `extractCompositeField`, matching PG
      expanded-record semantics; plain scalar targets unaffected. Lifts
      `reindex-concurrently-toast`'s post-GUC blocker: the setup `DO` block now runs
      `EXECUTE 'ALTER TABLE ' || r.table_name || …` and the first divergence advances
      to `relation "routine_column_usage" does not exist (42P01)`; spec still
      fundamentally needs real TOAST relations (`reltoastrelid=0`), stays `defer`.
      Gates: new `TestPlpgSQLSelectInto/sel_rec_field`; `go test ./internal/executor/`
      PASS; `TestPort_IsolationPlpgsqlToast` strict PASS (no regression); `go build`
      clean; pgbench smoke = pre-commit hook.
      **2026-06-24 enabler (design 0118-0067, NOT a promotion): `DROP INDEX`
      partition-tree locking.** Non-CONCURRENTLY `DROP INDEX` now takes a
      txn-scoped `AccessExclusiveLock` on the index's table + recursively on
      every partition descendant (top-down) before dropping, mirroring PG
      `RangeVarCallbackForDropRelation`. New `execDropIndex` call (gated
      `!s.Concurrent`) → `lockDropIndexTableTree` → `lockPartitionSubtreeAccessExcl`
      (`idx.Table` + `im.PartitionChildren` recursion, `acquireDDLLockTxn` each),
      riding the same `tableLockMgr` as `LOCK TABLE`. Lifts
      `partition-drop-index-locking`'s first divergence: `s2drop`/`s2dropsub` now
      `<waiting ...>` behind `s1`'s ACCESS SHARE on the leaf and complete on
      `s1commit` (byte-match). No-op in autocommit (`TxnLockBackendID==0`) ⇒ zero
      hot-path blast radius; CONCURRENTLY excluded (drop-index-concurrently-1 stays
      green). **Still deferred (full promotion):** (1) `pg_locks`→real-`tableLockMgr`
      bridge (per-backend granted/waiting + `BackendID→pid` for the
      `pg_stat_activity` join — today `s3getlocks` returns 0 rows); (2)
      partitioned-index child-index creation with PG auto-naming. Gates: live
      probe (divergence advanced to `s3getlocks`); no regression across
      DropIndexConcurrently1/ReindexConcurrently/DetachPartitionConcurrently3/
      CreateTrigger/AlterTable1/InheritTemp/TruncateConflict/ClusterConflict;
      `go test ./internal/executor/` PASS; `go build` clean; pgbench smoke = pre-commit.
      **2026-06-24 enabler (design 0118-0075, NOT a promotion):
      `partition-concurrent-attach`.** `ALTER TABLE … ATTACH PARTITION` now
      enforces the default-partition conflict check (PG
      `ATExecAttachPartition` → `check_default_partition_contents`): attaching a
      non-default partition over rows already living in the parent's visible
      DEFAULT raises `23P01 updated partition constraint for default partition
      "X" would be violated by some row`. goopg enforced this only on the
      `CREATE TABLE … PARTITION OF` path (`validatePartitionChild` →
      `checkDefaultPartitionDataConflict`); wired the same check into the
      `AlterTableAttachPartition` executor case (gated `!poc.Default &&
      !poc.IsHash`) + made `checkDefaultPartitionDataConflict` name the LEAF
      default (walks a sub-partitioned default's own default recursively, so the
      message reads `tpart_default_default` not the intermediate
      `tpart_default`; detection still scans the whole default subtree, only the
      name refined ⇒ CREATE path's non-nested behaviour unchanged). Standalone,
      plain-SQL-testable core of the spec; spec stays `defer` — remaining
      blockers (deferred-until-commit ATTACH visibility so a concurrent INSERT
      routes to the default; ATTACH AccessExclusiveLock on the default so that
      INSERT `<waiting ...>`; constraint re-validation after the wait) are the
      milestone-sized per-session MVCC-catalog work shared with `alter-table-4`
      (ledger). Gates: new `attach_default_conflict_test.go`
      (`TestAttachPartitionRejectsDefaultConflict`/`…NoConflictSucceeds`/
      `…NestedDefaultNamesLeaf`); `go test ./internal/executor/` PASS;
      `TestPort_IsolationDetachPartitionConcurrently1` strict PASS (shares attach
      setup, no false positive); `go build ./...` clean; pgbench smoke = pre-commit.
      **2026-06-24 promotion (design 0118-0079): `partition-concurrent-attach`
      PROMOTED ⇒ all 3 permutations byte-for-byte** (strict
      `TestPort_IsolationPartitionConcurrentAttach`), closing the spec on top of
      enablers 0118-0075..0078. Two final gaps. **Gap 1 (INSERT routing-path lock,
      perms 1 & 3):** PG `ExecInsert` opens every partition a tuple is routed into
      in `RowExclusiveLock`; goopg locked only the NAMED INSERT target (`tpart`)
      and nothing along the routing path, so an `INSERT INTO tpart` routing THROUGH
      the intermediate DEFAULT `tpart_default` never contended with a concurrent
      ATTACH's `AccessExclusiveLock` (0118-0076) on that default. New
      `lockRoutingPathPartitions(ctx, named, leaf)` (operators_storage.go) walks
      the parent chain from the routed leaf up to (excluding) the named target and
      `RowExclusive`-locks each INTERMEDIATE partition via `acquireWriteLockTxn`,
      wired into `insertOp.Next` right after routing resolves the leaf ⇒ locks
      `tpart_default`: perm 1's `s2i` waits behind the ATTACH then (post-commit,
      live catalog has `tpart_2`) `checkDefaultPartitionInsertConstraint` re-routes
      onto it → 23514; perm 3's `s2i` holds the lock to commit so `s1a`'s
      `lockDefaultPartitionForAttach` acquire waits. RowExclusive is
      self-compatible/DML-grade ⇒ concurrent partitioned INSERTs never block each
      other; single-level partitioned tables (leaf's parent IS the named target)
      and non-partitioned INSERTs are a no-op. **Gap 2 (fresh snapshot for the
      ATTACH re-scan, perm 3):** once `s1a` waits it is unblocked only after `s2c`
      commits, but its statement snapshot predates the wait, so
      `checkDefaultPartitionDataConflict`'s scan didn't see `s2`'s just-committed
      rows (attach wrongly succeeded → 6 rows). PG's
      `check_default_partition_contents` scans under the latest snapshot; the
      conflict scan now refreshes `synthCtx.Snap = TxnMgr.SnapshotFor(ctx.Tx)`
      before opening (mirrors detach-4 post-wait RI re-validation 0118-0064; a
      fresh snapshot also sees the txn's own writes ⇒ synchronous CREATE/ATTACH
      unaffected) ⇒ `s1a` finds the rows → 23P01 naming leaf default
      `tpart_default_default`. Gates: strict PASS (3 perms); no regression across
      DetachPartitionConcurrently1/2/3/4 + AlterTable1/2/3 + CreateTrigger +
      InheritTemp + TruncateConflict + ClusterConflict{,Partition} + VacuumConflict
      (14 strict PASS, single run); `go test ./internal/executor/` + `-race`
      partition/insert paths; `go build ./...` clean; pgbench smoke = pre-commit.
      **2026-06-24 enabler (design 0118-0080, NOT a promotion): `alter-table-4`
      perms 1 & 2 byte-for-byte.** Inheritance DDL (`c1 NO INHERIT p`,
      `c2 INHERIT p`) inside an explicit txn vs a concurrent `SELECT SUM(a) FROM p`:
      PG identifies the parent's children from the reader's snapshot THEN locks
      them, so the reader blocks on the writer's AccessExclusiveLock on `c1` and the
      change is invisible until commit. goopg mutated the SHARED catalog
      synchronously + took no lock ⇒ `s2sel` planned `{p}` and returned immediately.
      The SELECT side already locks each child AccessShare
      (`collectInheritanceDescendants`→per-child SeqScan→`acquireScanReadLockTxn`);
      added the DDL-side pieces (mirror DROP INDEX 0118-0074 / ATTACH 0118-0077):
      (1) `AlterTableInherit`/`AlterTableNoInherit` take a txn-scoped
      AccessExclusiveLock on the child via `acquireDDLLockTxn`; (2) inside an
      explicit txn (`(*ddlOp).inheritDeferSession()`) the register/unregister +
      INHERIT column-copy is recorded in `BasicSession.pendingInheritChng`
      (`PendingInheritanceChange` + Add/Take/CancelToDepth) and applied at commit by
      `ApplyPendingInheritanceChanges` on BOTH commit paths (executor + dispatch),
      discarded by `DiscardPendingInheritanceChanges` in `ProcessRollbackUndos` /
      to-depth in `rollbackToSavepointOp` (validation still immediate); (3) new
      `catalog.InMemory` O(1) counter `Has/Mark/UnmarkInheritanceChangePending`
      bypasses the cross-session plan cache while pending (`inheritanceChangePending`
      in both dispatch guards) so the 2nd `s2sel` re-plans → 1 (perm 1) / 101
      (perm 2). Blast radius nil: deferral+lock only inside an explicit txn over
      InMemory; autocommit unchanged; partitioning untouched
      (`RegisterPartitionChild` is a separate path). **Spec stays `defer`** — perm 3
      (`DROP c1`) needs deferred DROP TABLE + post-lock skip-of-vanished-child,
      perm 4 (`ALTER COLUMN TYPE`) needs deferred column-type change + post-lock
      parent-type re-validation error (ledger). Gates: probe perms 1&2 byte-match
      (divergence → perm 3); new units `TestInheritanceChangePendingCounter` +
      `TestPendingInheritanceChangeSession`; no regression across
      InheritTemp/DetachPartitionConcurrently1..4/PartitionConcurrentAttach/
      AlterTable1/AlterTable3/CreateTrigger/TruncateConflict; `go test
      ./internal/{catalog,executor,server}/` + `-race`; `go build ./...` + `go vet`
      clean; pgbench smoke = pre-commit.
      **2026-06-24 promotion (design 0118-0082): `alter-table-4` PROMOTED ⇒ all 4
      permutations byte-for-byte.** Closes the spec on top of 0118-0080 (perms 1-2)
      + 0118-0081 (perm 3). Perm 4 runs `ALTER TABLE c1 ALTER COLUMN a TYPE float`
      on the inheritance child while a concurrent `SELECT SUM(a) FROM p` waits on
      `c1`'s lock; once `s1` commits, PG `make_inh_translation_list`
      (`optimizer/util/appendinfo.c`) re-matches each parent attr to the child by
      name and raises `attribute "a" of relation "c1" does not match parent's type`
      (42611) because `a` is now float on c1 / integer on p. goopg mutates c1's type
      in the SHARED catalog immediately so the post-lock child scan from 0118-0081
      (which only skipped a vanished child) now also re-validates TYPES: new
      `planner.SeqScan.InheritParentOID` (set beside `SkipIfVanished` on every
      inheritance-child scan) drives `validateInheritedColumnTypes` in
      `seqScanOp.Open` mirroring `make_inh_translation_list` (match each non-dropped
      parent column to the child by name, compare canonical type class via new
      `canonicalTypeClass` which collapses integer/int4 + double precision/float8/
      float + resolves domains while ignoring typmod args ⇒ only a genuine base-type
      change trips it). Runs only for inheritance-child scans AFTER the lock (so the
      error appears post-`<... completed>`, not reordering `<waiting ...>`);
      partition leaves (`LockParentOID`) + direct scans untouched ⇒ zero false
      positives (confirmed: AlterTable1/AlterTable3/InheritTemp strict + full
      executor unit suite unchanged). `TestPort_IsolationAlterTable4` strict PASS (4
      perms); `go test ./internal/{executor,planner,catalog}/`; `go build ./...` +
      gofmt clean; pgbench smoke = pre-commit.
      **2026-06-24 enabler (design 0118-0076, NOT a promotion):
      `partition-concurrent-attach` piece (b) — ATTACH locks the DEFAULT
      partition.** `ALTER TABLE … ATTACH PARTITION` (non-default), inside an
      explicit txn, now takes a transaction-scoped `AccessExclusiveLock` on the
      parent's existing DEFAULT partition before the conflict check — PG
      `ATExecAttachPartition` (`get_default_oid_from_partdesc` →
      `LockRelationOid(defaultPartOid, AccessExclusiveLock)`), because the new
      partition narrows the default's implicit constraint. New
      `(*ddlOp).lockDefaultPartitionForAttach(parent)` (scans
      `InMemory.PartitionChildren` for `PartitionBound.IsDefault`, locks via
      `acquireDDLLockTxn` ⇒ no-op in autocommit / for system rels, held-to-commit
      in an explicit txn). A concurrent INSERT routing to the default
      (`RowExclusiveLock`) would then block behind the open attach. Spec stays
      `defer` — the lock is only contended once piece (a) deferred-until-commit
      attach visibility routes `s2`'s insert to the default (today the shared
      catalog makes uncommitted `tpart_2` visible ⇒ insert routes there); (a)+(c)
      remain the per-session MVCC-catalog milestone shared with `alter-table-4`.
      Gates: new `attach_default_lock_test.go`
      (`TestAttachPartitionLocksDefaultPartition`/`TestAttachPartitionNoDefaultNoLock`);
      0118-0075 attach tests + full `go test ./internal/executor/` PASS;
      `TestPort_IsolationDetachPartitionConcurrently1` strict PASS; `go build
      ./...` clean; pgbench smoke = pre-commit.
      **2026-06-23 fourteenth promotion (design 0118-0046): `alter-table-2`
      PROMOTED ⇒ all 48 permutations byte-for-byte.** Two changes: (1) the
      `ALTER TABLE … ADD FOREIGN KEY …` parser now accepts the `NOT VALID`
      trailer (in any order with `[NOT] DEFERRABLE [INITIALLY …]`) — new AST
      field `AlterTableAction.NotValid`, surfaced as
      `pg_constraint.convalidated='f'` via new `catalog.ForeignKey.NotValid`;
      (2) the `AlterTableAddForeignKey` executor case takes a transaction-scoped
      `ShareRowExclusiveLock` on the altered table via the existing
      `acquireDDLLockTxn` (PG `AlterTableGetLockLevel` → `AT_AddConstraint`). The
      standard lock matrix drives every perm: a concurrent `INSERT`
      (RowExclusiveLock) conflicts so one of `s1b`/`s2d` waits until the other
      commits, while `SELECT … FOR UPDATE` (RowShareLock) + plain reads proceed.
      No-op in autocommit / for system catalogs (pg_dump-restore / HammerDB-load
      + pgbench untouched); only the altered table is locked (the spec cannot
      distinguish the referenced-table lock — `s2e INSERT INTO a` always follows
      `s2d INSERT INTO b`). `TestPort_IsolationAlterTable2` strict PASS; sibling
      alter-table-3 / create-trigger / sequence-ddl PASS; parser/executor/catalog
      units; pgbench smoke 0-failed.
      **Remaining (deferred, ledger 2026-06-22):**
      `alter-table-{1,4}`
      (`alter-table-1`: VALIDATE CONSTRAINT lock semantics; `alter-table-4`: INHERITS),
      per-leaf CLUSTER processing + a faithful non-owner CLUSTER `must be owner of
      table` error (no `port` spec exercises a role that owns a leaf), `reindex-concurrently-toast` (`allow_system_table_mods`
      GUC + TOAST-relation reindex), partition specs (ATTACH/DETACH PARTITION),
      `plpgsql-toast` (TOAST in
      PL/pgSQL). FK/MERGE/VACUUM inherit-temp filtering (bounded follow-up). Group
      stays open.
      **2026-06-23 thirteenth promotion (design 0118-0045): `vacuum-no-cleanup-lock`
      PROMOTED ⇒ all permutations byte-for-byte.** The spec asserts
      `pg_class.relpages|reltuples` track what a non-aggressive `VACUUM` observes
      even while a cursor pins the table's only heap page (no cleanup lock). Two
      gaps: (1) registered the `vacuum_multixact_freeze_min_age` GUC (the
      `vacuumer` setup SETs it; +`postgresql.conf.sample` line for the parity
      gate); (2) the virtual `pg_class` builder (`catalog.VirtualRows`, the live
      read path) hard-coded `relpages`/`reltuples` to `0` and nothing updated them
      on VACUUM. New `catalog.(*InMemory).UpdateRelStats` (the `vac_update_relstats`
      analog) overwrites `Pages`/`RowCount` while **merging** into any existing
      `Stats` so a prior ANALYZE's per-column `pg_statistic` survives; the builder
      reads `t.Stats` (nil ⇒ `0|0`, so untouched tables/the broad catalog-reading
      surface are unchanged); `vacuumOp.Next` publishes after a successful vacuum.
      Load-bearing subtlety: `reltuples` is published from `vacuum.Analyze`'s
      **fresh-snapshot visible-tuple count**, NOT the prune's surviving-
      line-pointer count — a recently-dead tuple (deleted+committed but not
      removable because the pin holder holds `OldestXmin` back) survives the prune
      yet must be excluded from `reltuples` (PG counts `HEAPTUPLE_LIVE`, not
      `RECENTLY_DEAD`); the visible count gets it right (`22`→`21` in the pinholder
      permutation). Heap `pg_class` write-once DDL row left at `0` (not the read
      path). `TestPort_IsolationVacuumNoCleanupLock` strict PASS;
      `TestUpdateRelStatsPreservesColumns`; `TestSampleConfigCoversRegistry`;
      sibling vacuum/freeze specs PASS; `-race` vacuum/mvcc; executor/catalog/
      config PASS; pgbench smoke.
      **2026-06-23 enabler (design 0118-0044, NOT a promotion):** made the
      isolation harness's `splitSQLStatements` dollar-quote aware (new
      `dollarOpener` + active-`dollarCloser` tracking) so a `do $$ … commit; …
      $$;` step is no longer split at the first body-internal `;` — `plpgsql-toast`'s
      first divergence moved from a harness-induced `unterminated dollar-quoted
      string` lex error to the real blocker, `unsupported PL/pgSQL statement`
      (`COMMIT` inside a `DO` block). Test-harness only; `merge-update` strict
      no-regression; unit `TestSplitSQLStatementsDollarQuote`. Probe ranking of
      the remaining tail: most specs (`alter-table-4`, partition ATTACH/DETACH)
      apply DDL immediately/non-transactionally so a concurrent parent SELECT
      neither waits nor sees the pre-change child set — the hard tail needs
      transactional-DDL cross-session visibility; others need parser support
      (`NOT VALID`/`VALIDATE CONSTRAINT`, `DETACH … CONCURRENTLY`).
- [x] **M0118-0009** — Misc / system-level specs: async-notify, timeouts, stats, horizons,
      freeze-the-dead, inplace-inval, intra-grant-inplace{,-db}, subxid-overflow,
      prepared-transactions{,-cic}, temp-schema-cleanup, multixact-no-forget,
      aborted-keyrevoke, delete-abort-savept{,-2}.
      **Done so far:** `delete-abort-savept` PASS (design 0118-0013) — subxact-scoped
      DELETE/UPDATE xmax stamping (`effectiveWriterXID` at all 24 old-tuple-xmax +
      paired-WAL sites), producer preserves an outer-level self locker so ROLLBACK TO
      restores it, and `lockRowsOp` treats a subxact-aborted xmax (`IsAborted`) as a
      rolled-back updater. `aborted-keyrevoke` PASS (design 0118-0014) — SAVEPOINT now
      eagerly materialises the top-level XID before `AllocateSubXid` so the subxid→parent
      link is non-zero from birth (SAVEPOINT-before-first-write previously registered
      parent=0, making the subxact's uncommitted writes cross-session-visible and
      defeating the row-lock wait); plus sub-XID-scoped NEW-tuple xmin
      (`writeHeapRowReturning{,PG}` + HOT) and HEAP_KEYS_UPDATED on the single-xid
      old-tuple xmax stamp; descendant-direction subxact self-visibility in
      `isCurrentTxXID`. `delete-abort-savept-2` PASS (design 0118-0015) — a SELF
      lock-only upgrade inside a savepoint (FOR KEY SHARE → FOR NO KEY UPDATE under
      the sub-XID) now routes through `stampMultiLock` (gate widened by new
      `hasOuterSelfLockMember`) so the outer top-level KEY SHARE survives as a multi
      member; `ROLLBACK TO` drops only the sub-XID and a conflicting FOR UPDATE
      waiter keeps waiting on the restored KEY SHARE. `multixact-no-forget` PASS
      (design 0118-0016) — two fixes: (1) `stampLockInner`'s real-updater branch
      conflict-filters its co-member wait set via `conflictingLockHolders` (not
      `activeLockHolders`, now removed) so s3 FOR NO KEY UPDATE doesn't wait on a
      surviving non-conflicting KEY SHARE after the updater aborts, and `stampAtPtr`
      preserves survivors via `stampMultiLock`; (2) the HOT UPDATE producer carries
      the old tuple's non-conflicting lockers forward onto the new tuple version
      (`carryForwardLockersToNewTuple` + shared `survivingLockersForUpdate`, the
      new-version half of heap_update) so after the updater commits s3 FOR UPDATE
      waits on the inherited KEY SHARE. `timeouts` ROW-LEVEL half implemented
      (design 0118-0017) — `lock_timeout` GUC now armed in `lockmgr.acquire` +
      `mvcc.WaitForXID` via new leaf pkg `internal/lockwait`; lock-wait cancel
      messages corrected (statement_timeout→"statement timeout",
      lock_timeout→"lock timeout", client cancel→"user request"); `epqWait`
      propagates timeouts to ~11 write-path sites instead of swallowing into a
      spurious 40001. Verified `TestPort_TimeoutsRowLevel` 4/4. `timeouts`
      TABLE-LEVEL half LANDED ⇒ **`timeouts.spec` PROMOTED to `pass`** (design
      0118-0018): new `Context.acquireScanReadLockTxn` makes a plain SELECT take
      a txn-scoped ACCESS SHARE on `tableLockMgr` (wired at the
      seqscan/index/index-only opens) so a later bare `LOCK TABLE` (ACCESS
      EXCLUSIVE) conflicts, parks, and is cancelled by the shorter timeout.
      Bounded blast radius: no-op outside an explicit txn
      (`TxnLockBackendID==0`) and for system catalogs (`RelOid<16384`);
      idempotent re-acquire; ACCESS SHARE grants instantly absent a concurrent
      LOCK TABLE/DDL. `TestPort_TimeoutsTableLevel` 4/4; full isolation batch +
      `-race` executor/mvcc/lockmgr + pgbench smoke (0-failed, no TPS
      regression) green. `inplace-inval` PASS (design 0118-0019) — promoted
      `failed`→`pass` with NO code change: goopg is immune to the upstream
      inplace-update-revert hazard by construction because `pg_class` is virtual
      and `relhasindex` is derived live from the in-memory index set
      (`len(c.byTable[oid])>0`), so there is no heap tuple / catcache oldtup /
      `heap_inplace_update` byte to clobber; both permutations observe
      `relhasindex=t` byte-identical to PG 18.3. Dedicated test
      `TestPort_IsolationInplaceInval`. `freeze-the-dead` PASS (design 0118-0020) —
      VACUUM (`vacuumCore`) reclaimed dead tuples with a naive `xmax<horizon` slot
      removal that (1) compared a MultiXactId xmax to the xid horizon (a category
      error marking a live, still-locked HOT chain root "dead") and (2) physically
      removed the chain root, orphaning the live tip → the updated row vanished from
      index scans + the final select. Fix unifies VACUUM onto the already-correct
      opportunistic-prune kernel: shared `pagePruneCore`, new `storage.PageVacuumPrune`
      (HOT-chain-aware: dead roots → `ItemIDRedirect`; multixact-aware: resolves the
      updater before the horizon compare), and `vacuumCore` emits
      `RecordKindHeapPruneOpt` (redirects+unused, replay exists). Dedicated test
      `TestPort_IsolationFreezeTheDead`. `subxid-overflow` PASS (design 0118-0021) —
      promoted `failed`→`pass` with PL/pgSQL front-end work only (no MVCC/storage
      change). The spec's recursive `gen_subxids` opens 100 nested subxids via
      per-frame `EXCEPTION` handlers (overflowing the subxid cache) while other
      sessions probe `XidInMVCCSnapshot`/`XactLockTableWait`; goopg's subxact
      visibility/lock machinery was already correct, the function just wouldn't
      compile. Two parser gaps lifted: bare `RETURN;` (`parseReturn` now yields
      `ReturnStmt{Expr:nil}`; runtime returns NULL for VOID, errors `42601 missing
      expression` for a value function — mirrors upstream `make_return_stmt`) and
      the `NULL;` no-op statement (new `NullStmt` AST node + `parseStmt` case +
      no-op runtime arm). Dedicated test `TestPort_IsolationSubxidOverflow`; unit
      `TestParse{AcceptsBareReturn,NullStatement,ExceptionHandlerNullBody}`.
      `async-notify` PASS (designs 0118-0089/0090). `temp-schema-cleanup` PASS
      (designs 0118-0091 perm-1 + 0118-0092 cascade enabler + 0118-0093 perm-2
      process-exit) — `pg_terminate_backend(pg_backend_pid())` self-termination
      (`executor.ErrSelfTerminate` → FATAL "terminating connection due to
      administrator command" + close) + backend-exit temp cleanup ordered before
      advisory-lock release + `Context.TerminateBackend` peer path + harness
      `lib/pq` connection-death rendering; promoted `runIsoSpecStrict`.
      **Remaining (deferred, ledger 2026-06-22):**
      (b) lock carry-forward on the non-HOT update paths (delete+insert /
      `UPDATE…FROM` / MERGE / upsert) — bounded follow-up, same narrow gate, not
      exercised by any current `port` spec. Plus the other M0118-0009 misc specs
      untouched (horizons [dollar-quote lexer + EXPLAIN JSON], intra-grant-inplace
      [catalog-row lock on GRANT tuple xmax], stats [pg_stat_* infra],
      prepared-transactions [2PC]).
      **2026-06-25 enabler (design 0118-0096, NOT a promotion):** `pg_database`
      virtual catalog now projects the standard `datfrozenxid` (= `DatFrozenXID()`
      cluster-wide min relfrozenxid, bootstrap floor 2) + `datminmxid`
      (FirstMultiXactId 1) columns — clears `intra-grant-inplace-db`'s `snap3`
      `SELECT datfrozenxid FROM pg_database` 42703 first-divergence and serves real
      `age(datfrozenxid)` monitoring queries (M0117-0008 catalog-surface alignment).
      `intra-grant-inplace-db` still deferred (ledger 2026-06-25): the hard blocker
      is a runtime shared-catalog MVCC-tuple lock — `VACUUM (FREEZE)` must
      `<waiting ...>` behind an uncommitted `GRANT … ON DATABASE` row update on
      `global/1262`; same capability gates `intra-grant-inplace` on `pg_class`.
      Also reconciled a stale inventory row this loop: `fk-deadlock.spec` (promoted
      in c59eb91d / design 0118-0094) had its suite-level CSV flipped but not the
      per-spec inventory — set failed→pass + regen (isolation tally now 106 pass /
      15 failed).
      **2026-06-25 promotion (design 0118-0098): `intra-grant-inplace-db`
      PROMOTED ⇒ single permutation byte-identical.** Closes the one behavioural
      gap 0118-0096 left: `VACUUM (FREEZE)` must `<waiting ...>` behind an
      uncommitted `GRANT TEMP ON DATABASE` and resume on commit. Upstream `GRANT …
      ON DATABASE` takes NO heavyweight lock — its lock IS the `pg_database` tuple
      `xmax` — and a database-wide VACUUM's in-place `datfrozenxid` update waits on
      that xmax (`heap_inplace_update_scan` → `XactLockTableWait`). goopg serves
      `pg_database` virtually (no real tuple/xmax), so the wait is REPLAYED: parser
      flags `GRANT/REVOKE … ON DATABASE` (`CompatNoopStmt.DatabaseACL`);
      `execCompatNoop` materializes the writer XID and stores it as the
      database-ACL-change xmax (`InMemory.SetDatabaseACLChangeXID`, `atomic.Uint32`);
      a database-wide VACUUM (`len(vs.Targets)==0` — the only case PG advances
      datfrozenxid) calls `mvcc.WaitForXID` on it first (`vacuumOp.waitForDatabaseACLChange`).
      Runner decides `<waiting>` by 300 ms timeout so the XID-block reproduces the
      output exactly. Blast radius nil (targeted VACUUM never consults the marker;
      no-ACL-change marker is 0 → instant; committed marker → instant; no
      MVCC/storage/WAL surface). `TestPort_IsolationIntraGrantInplaceDb` strict
      PASS; sibling Vacuum/Cluster/Truncate-conflict + CreateTrigger PASS
      (`VacuumConcurrentDrop` fails identically on clean HEAD — pre-existing timing
      flake, unrelated); parser/catalog/executor units; build+vet+gofmt clean;
      pgbench smoke = pre-commit hook. Isolation tally now 107 pass / 14 failed.
      **2026-06-25 promotion (design 0118-0099): `predicate-hash` PROMOTED ⇒
      all 40 permutations byte-identical.** Page (bucket) level predicate locking
      in a hash index. goopg has no native hash AM (hash index built on the
      B-tree substrate, `Method` stays btree), so a SERIALIZABLE equality scan
      took a relation-grain SIREAD → over-aborted (30 vs PG's 18 serialization
      failures, the 12 different-bucket interleavings). New in-memory
      `catalog.Index.DeclaredHash` marks `USING hash`; `ssiHashBucket` →
      stable 31-bit FNV bucket of the encoded equality key;
      `ssiRecordHashBucketRead` takes a `PageLockTag(db,indexOID,bucket)` SIREAD
      on the index/index-only scan in place of the relation lock; per-tuple heap
      reads switch to `ssiConflictOutTupleRead` (conflict-out only — no heap
      SIREAD that would coarsen to a heap-page lock and re-introduce the false
      positive); `ssiRecordHashIndexInsert` runs the bucket conflict-in on the
      INSERT path. Blast radius bounded to SERIALIZABLE equality scans/INSERTs on
      declared-hash indexes; the 6 pass-required btree/seqscan SSI specs +
      predicate-lock-hot-tuple/partial-index/simple-write-skew PASS no
      regression; executor/mvcc/planner/catalog units + `-race` SSI tests;
      build+vet clean; pgbench smoke = pre-commit hook. Isolation failed-spec
      count now 12 (was 13). Follow-ups: `predicate-gin`/`predicate-gist` need
      AM-specific granularity; `DeclaredHash` not WAL-persisted (reverts to
      relation-grain after restart); UPDATE/DELETE on a hash column not yet
      bucket-locked.
      **2026-06-25 (design 0118-0100, enabler — NOT a promotion):** added JSON
      accessor operators `->`/`->>` (lexer 2-/3-char match + `OpJSONGet`/
      `OpJSONGetText` at `precJSON=6` + executor `evalJSONArrow`). Previously a
      hard lex error; broadly useful + clears `horizons`' first divergence. Spec
      stays `failed` — re-probe shows the next blockers are plpgsql
      `EXECUTE … INTO STRICT`, then `EXPLAIN (FORMAT json)` `Heap Fetches`
      emission, then the Effort-L MVCC core (index-only-scan heap-fetch counts
      reflecting pruning + prune/VACUUM respecting a concurrent older snapshot
      for permanent vs temp tables). Ledger row recorded.
      **2026-06-25 (design 0118-0101, enabler — NOT a promotion):** added plpgsql
      `EXECUTE … INTO STRICT var` (the next horizons rung): `ExecuteStmt.Strict`
      (AST) + optional `STRICT` after `INTO` in `parseExecute` + runtime row-count
      enforcement (0 rows→P0002, >1→P0003, mirroring `SELECT … INTO STRICT`).
      `horizons.spec`'s `explain_json` setup helper now creates + runs; re-probe
      shows the divergence advanced to the EXPLAIN `Heap Fetches` pruning counts.
      Spec stays `failed` — remaining blockers are the Effort-L EXPLAIN JSON
      `Heap Fetches` emission + MVCC pruning-horizon core. Tests
      `TestParseExecuteIntoStrict` + `TestPlpgSQLExecuteIntoStrict`. Ledger row.
      **2026-06-25 (design 0118-0102, enabler — NOT a promotion):** EXPLAIN
      infrastructure for the `Heap Fetches` rung — (1) `EXPLAIN (FORMAT JSON)`
      now nests the plan tree under a top-level `"Plan"` key (PG-faithful;
      `Planning/Execution Time` siblings) so `…->0->'Plan'->…` resolves (goopg
      flattened it before); (2) IndexOnlyScan renders `Index Only Scan using
      <idx> on <table>` (was the `%T` default); (3) EXPLAIN ANALYZE reports
      `Heap Fetches` for IOS nodes (JSON key + text line), counted from the
      operator's non-`ALL_VISIBLE` fallback (new `nodeStats.heapFetches` +
      `heapFetchCounter` interface). 6 internal JSON tests updated for the
      wrapper. **horizons re-probe isolates the residual blocker:** goopg's
      planner emits `Sort → Seq Scan` (not an IOS) for `SELECT * … ORDER BY
      data` and does NOT honor `enable_seqscan/indexscan/bitmapscan=false`, so
      no IOS node exists → `Heap Fetches` NULL. NEXT (Effort-L): planner
      GUC-honoring + ordered-full-index→IOS promotion, then the MVCC
      pruning-horizon core. `TestExplainHeapFetchesIndexOnlyScan` PASS. Ledger.
      **2026-06-25 (design 0118-0103, enabler — NOT a promotion):** ordered
      full-index→IndexOnlyScan promotion gated on `enable_seqscan = off` (the
      GUC `horizons`' `pruner` session sets). goopg's rule-based planner ignored
      the planner-toggle GUCs and built `Project(Sort(SeqScan))` for an
      ORDER-BY-only query, so `EXPLAIN (COSTS OFF) SELECT * FROM horizons_tst
      ORDER BY data` showed `Sort → Seq Scan` not the expected `Index Only Scan
      using horizons_tst_data_key on horizons_tst`. Three pieces: (1)
      `dispatch.go` `sessionPlanCatalog`/`ctxPlanCatalog` thread `enable_seqscan`
      into new `catalog.SearchPathCatalog.DisableSeqScan`; (2) planner
      `currentSeqScanDisabled` walks the catalog wrapper chain (same `Unwrap()`
      carrier pattern as `currentTempOwner`); (3) `tryPromoteOrderedIndexOnlyScan`
      replaces `Project(Sort(SeqScan))` with an unbounded `IndexOnlyScan` (nil
      Key/Keys/bounds → full-range `RangeScan` in ascending order, Sort dropped)
      when a non-partial B-tree index's leading key columns match the
      ASC/NULLS-LAST ORDER BY keys and its key+INCLUDE columns cover the
      projection. PG-faithful gate (PG picks the IOS *because* SeqScan is
      disabled) bounds blast radius to seqscan-disabled sessions — TPC-H/pgbench
      never set it, so the branch no-ops and their plans are byte-identical.
      Re-probe: IOS plan + `Heap Fetches` navigation now match; only 3 lines
      differ — `L125` (temp prune-despite-older-snapshot expected 0/actual 2),
      `L244`/`L254` (perm-table VACUUM-must-not-remove-still-visible expected
      2/actual 0) — isolating the residual blocker to the Effort-L MVCC
      pruning-horizon core (temp-always-prunable vs perm-respects-OldestXmin +
      matching VACUUM cutoff). Tests `TestOrderedIndexOnlyScan{Promoted…,
      NotPromotedByDefault,RequiresAscending}`; planner/catalog/executor/server
      suites PASS; build+vet clean; pgbench smoke = pre-commit hook. Ledger row.
      **2026-06-25 (design 0118-0104, enabler — NOT a promotion): the MVCC
      pruning-horizon core, 4/5 horizons permutations now match PG 18.3.** Lands
      the TEMPORARY half + no-vacuum permanent permutations: (1)
      `mvcc.OldestXminForProc(procNum)` — session-local horizon
      `min(nextXID, slot.xid, slot.xmin)` for one backend (falls back to global),
      ignores other backends' snapshots but respects the owner's own in-progress
      txn (keeps perm 3 "delete-in-open-txn" at Heap Fetches=2); (2)
      `vacuum.VacuumOptions.Horizon` override + `operators_vacuum.go` passes the
      session-local horizon for `tbl.Temp` targets → perm 5 temp VACUUM reclaims
      rows (vacuumIndexes clears the B-tree entries) → 0; (3) IOS
      `pruneTouchedTempPages` prunes the temp heap blocks the scan fetched at the
      session-local horizon (reusing PageVacuumPrune + LogHeapPruneOpt) WITHOUT
      setting VM ALL_VISIBLE (next scan stays on the fallback path, skips
      reclaimed entries rather than resurrecting deleted rows) → perm 2 drops
      2→0; (4) IOS counting rule (kill_prior_tuple analog) skips an index entry
      whose root line pointer is LP_UNUSED/LP_DEAD without a Heap Fetches tally.
      **horizons stays `failed`** — residual = perm 4 only (perm-table
      VACUUM-respects-older-snapshot, Heap Fetches must stay 2): lifeline's
      batched `BEGIN ISOLATION LEVEL REPEATABLE READ; SELECT 1;` never registers
      the RR tx's snapshot xmin (goopg captures an RR snapshot lazily at the
      first SEPARATE-message statement); capturing it at the batched first
      statement (PG-correct) was implemented in dispatch.go and REVERTED because
      it regresses `eval-plan-qual-trigger` (same batched `BEGIN RR; SELECT 1`
      shape) by exposing a latent goopg RR concurrent-update (40001) detection
      gap. NEXT: fix that detection to be snapshot-timing-robust, then re-apply
      the batched-BEGIN RR snapshot-pinning. Tests `TestPort_IsolationHorizons`
      (soft 4/5) + `TestOldestXminForProc_SessionLocalIgnoresOtherSnapshots`;
      `-race` mvcc/vacuum/storage + executor/server PASS; predicate-hash/
      eval-plan-qual-trigger non-regression confirmed; build+vet+gofmt clean;
      pgbench smoke = pre-commit hook. Ledger row.
      `horizons` PROMOTED (loop #43, design 0118-0105). **Remaining M0118-0009:**
      `intra-grant-inplace` (pg_class sibling — ALTER TABLE ADD PRIMARY KEY
      `<waiting>` behind FOR KEY SHARE on pg_class; needs runtime shared-catalog
      MVCC-tuple row locks — heavy), `stats` (pg_stat_force_next_flush + cumulative
      function-stats + stats_fetch_consistency + 2PC interaction), `prepared-
      transactions{,-cic}` (2PC PREPARE/COMMIT PREPARED — also gates the `stats`
      spec's PREPARE TRANSACTION steps). All three remaining are genuinely Effort-L
      unbuilt subsystems.
      **2026-06-25 (design 0118-0106): `eval-plan-qual` PROMOTED — closes the
      M0118-0007 planner/output-format group.** EPQ recheck over a join: the index
      key fold into the per-row recheck was unsound when `ix.Key` is a join/
      correlated reference (coordinate-space mismatch dropped the post-update row);
      fold now gated on a row-local constant key (`exprRefsColumnOrOuter`). Sibling
      latent fix: lazy hash join preserves build-side heap ctids for FOR UPDATE over
      the hash-join variant. Detail in the M0118-0007 entry above.
      **2026-06-25 (design 0118-0107): `timeouts` PROMOTED — pass-required via
      `TestPort_IsolationTimeouts`.** `statement_timeout`/`lock_timeout` against
      table-level (`LOCK TABLE`) and row-level (`DELETE` behind a concurrent
      `UPDATE`) lock waits; all 8 permutations byte-identical to PG 18.3 with NO
      engine change (the shorter of the two timeouts fires first → 57014 statement
      timeout / 55P03 lock timeout; blocked steps `(*)`-marked upstream). Found via
      a probe of remaining deferred specs — cheapest available promotion. Stable
      across 8 runs. **Remaining M0118-0009 (all Effort-L unbuilt subsystems):**
      `intra-grant-inplace{,-db only -db done}`, `stats`, `prepared-transactions{,-cic}`.
      **2026-06-25 (design 0118-0109, enabler — NOT a promotion):**
      `intra-grant-inplace` permutation 1 now byte-identical (first divergence
      L17→L62). `ALTER TABLE … ADD PRIMARY KEY` (an in-place `relhasindex=true`
      update on the `pg_class` tuple) must `<waiting ...>` behind a concurrent
      uncommitted `GRANT SELECT ON <table>`; PG takes no heavyweight lock — its
      lock IS the catalog tuple `xmax`. Replayed the `xmax` wait (the pg_class
      sibling of 0118-0098's database case): parser resolves a table-target
      GRANT/REVOKE into `CompatNoopStmt.TableACL` (`grantObjectName`/
      `grantNonTableClass`); `execCompatNoop` records the writer XID keyed by
      table OID (`InMemory.SetTableACLChangeXID`, mutex-guarded `map[oid]xid`);
      `execAlterTableAddPrimaryKey` calls `waitForTableACLChange`→`mvcc.WaitForXID`
      before building the index. Spec stays `defer` — perms 3,4,7–11 need
      `pg_class` rowmark locking (`SELECT relhasindex … FOR NO KEY UPDATE`/`FOR
      UPDATE`/`FOR KEY SHARE` + `DELETE FROM pg_class` taking a real tuple lock on
      a virtual catalog row + `LockTuple` deadlock detection), the Effort-L runtime
      shared-catalog MVCC-tuple-lock core. `TestParseGrantTableACL`; non-regression
      IntraGrantInplaceDb + TruncateConflict strict PASS.
      **2026-06-25 (design 0118-0110, enabler — NOT a promotion): same-backend
      two-phase commit.** Adds the 2PC statements `prepared-transactions`,
      `prepared-transactions-cic` and `stats` need. goopg had NO 2PC —
      `PREPARE TRANSACTION 's1'` did not parse (mis-lexed as the prepared-
      statement PREPARE), so every permutation diverged at the first prepare
      step. New parser AST (`PrepareTransactionStmt`/`CommitPreparedStmt`/
      `RollbackPreparedStmt`; `parsePrepare` branches on the `TRANSACTION`
      keyword, `parseCommit`/`parseRollback` on the unreserved `PREPARED`
      word) + same-backend executor (`internal/server/twophase.go`). Every
      target spec PREPAREs and COMMIT/ROLLBACK PREPAREs the gid from the SAME
      idle-in-between session, so goopg keeps the prepared txn OPEN as the
      connection's active txn (writes/locks/SSI predicate locks persist),
      records `connTxState.preparedGid`, and finalises COMMIT/ROLLBACK PREPARED
      by re-entering `executeOneSimpleStmt` with a synthetic CommitStmt/
      RollbackStmt — reusing the CANONICAL commit path verbatim (SSI pre-commit
      check, deferred DDL, NOTIFY publish, connTx.End) so no parallel commit
      path drifts and the SSI check fires at COMMIT PREPARED as upstream.
      PREPARE TRANSACTION outside a txn block → 25P01; unknown gid → 42704.
      `isTwoPhaseStmt` keeps them out of the plan-cache pre-plan. Blast radius
      nil (handler returns handled=false for all other statements; no port spec
      uses these). A prepared-transactions-cic probe confirms the held txn keeps
      its MVCC slot active so CREATE INDEX CONCURRENTLY waits for it and unblocks
      at COMMIT PREPARED — first divergence advanced from "parse error at p1" to
      the final cic2/c1 timing; ONLY residual gap = goopg's CIC active-slot wait
      doesn't honour `lock_timeout` (PG cancels with 55P03). Specs stay defer
      pending (1) CIC lock_timeout, (2) full 1500-perm SSI verification of
      prepared-transactions, (3) the pg_stat_* subsystem for stats. Tests
      `TestParseTwoPhaseCommit` + `TestPort_TwoPhaseCommitSameBackend`
      (commit-prepared visibility incl. cross-session isolation, rollback-
      prepared discard, 25P01, 42704).
      **2026-06-25 (design 0118-0111, PROMOTION): `prepared-transactions-cic`
      PROMOTED to pass-required** (`runIsoSpecStrict` in
      `TestPort_IsolationPreparedTransactionsCIC`, byte-identical to PG 18.3).
      Closes the 0118-0110 residual gap: `mvcc.WaitForSlotsToCommit` now arms the
      session `lock_timeout` carried on ctx (`lockwait.Timeout`) exactly like
      `lockmgr.ProcSleep`, so a CREATE INDEX CONCURRENTLY parked waiting for a
      still-running prepared txn's MVCC slot to drain is cancelled with
      "canceling statement due to lock timeout" at the 10ms budget (CIC drain
      site maps via the shared `lockWaitTimeoutError` helper — no sibling path).
      Blast radius nil: byte-unchanged when lock_timeout=0; only caller is the CIC
      drain. Gates: strict spec PASS + `-race ./internal/mvcc/...` green.
      **2026-06-25 (design 0118-0112, PROMOTION): `prepared-transactions`
      PROMOTED to pass-required** (`runIsoSpecStrict` in
      `TestPort_IsolationPreparedTransactions`, all 1500 permutations
      byte-identical to PG 18.3). The SSI dangerous-structure check now runs at
      PREPARE TRANSACTION time (`Manager.PrepareCheckForSerializationFailure`)
      and a PREPARED-but-not-committed peer is treated like a committed-first one
      in the read/write conflict hooks (`SerializableXact.Prepared`/`PrepareSeqNo`
      mirror `SXACT_FLAG_PREPARED`/`prepareSeqNo`): a dangerous structure whose
      pivot is already prepared makes the preparer commit suicide (40001) while an
      rw-edge to a prepared writer dooms the reader; PREPARE on an already-aborted
      block silently rolls back (no 25P02, matching `PrepareTransactionBlock` on
      `TBLOCK_ABORT`). Blast radius nil for non-2PC: every new branch is guarded by
      `Prepared`, never set outside `PREPARE TRANSACTION`. Gates: strict spec PASS
      (137s) + `-race ./internal/mvcc/...` green.
      **2026-06-25 (design 0118-0113, enabler — NOT a promotion):**
      `intra-grant-inplace` permutations 2–6 now byte-identical (first divergence
      L62→L141). The `pg_class` **rowmark** half (the GRANT-`xmax` half was
      0118-0109): an explicit `SELECT … FROM pg_class WHERE oid=<rel> FOR {KEY
      SHARE|NO KEY UPDATE|SHARE|UPDATE}` takes a tuple lock that ADD PRIMARY KEY's
      in-place `relhasindex` update must serialise behind (FOR KEY SHARE alone
      does NOT conflict). goopg has no real pg_class heap tuple, so
      `lockRowsOp.maybeRecordPgClassRowMark` (fires only when locked OID ==
      `catalog.RelationRelationId`, OID from the `oid=<const>` filter) records
      holder+conflict flag in a new catalog store (`pgClassRowMarks`;
      `AddPgClassRowMark`/`PgClassRowMarks`/`ClearPgClassRowMarksForXID`);
      `execAlterTableAddPrimaryKey`→`waitForPgClassRowMarks`→`mvcc.WaitForXID` on
      conflicting other-tree holders (`TopLevelXid` skips same-xact/savepoint —
      perms 5/6); commit/rollback clear the txn's marks. `TestPgClassRowMarks` +
      non-regression IntraGrantInplaceDb/TruncateConflict + row-lock family PASS;
      `-race` mvcc/catalog green. Spec stays `defer` — perms 7–10 need the
      `GRANT`/`REVOKE`/`DELETE FROM pg_class` `LockTuple` + deadlock-detection
      core (the Effort-L runtime shared-catalog MVCC-tuple-lock core).
      **2026-06-25 (design 0118-0114, enabler — NOT a promotion):**
      `intra-grant-inplace` permutations 1–7 now byte-identical AND perm 8's
      deadlock line exact (first divergence L141→L184). Added the reverse-
      direction wait + deadlock detection: (1) GRANT/REVOKE on a table now
      AWAITS a conflicting concurrent `pg_class` rowmark — `execCompatNoop`'s
      TableACL branch records its ACL-change `xmax` FIRST (so a peer ADD PRIMARY
      KEY observes it and serialises after the GRANT's commit — perm 7's
      load-bearing ordering) then calls `waitForPgClassRowMarks`; (2) deadlock
      detection via a new shared helper `waitPgClassInplaceXID` that registers
      `myXID→blockingXID` in the EXISTING process-global EPQ wait-for graph
      (`registerWFGAndCheckCycle`) and walks for a cycle BEFORE blocking — perm 8
      (`b2 sfnku2 b1 grant1 addk2`) forms `s1→s2→s1` so `addk2` (the `(*)`
      victim) raises 40P01 "deadlock detected" synchronously. All three
      `pg_class`-tuple waits route through the helper and now return `*ExecError`.
      Spec stays `defer` — perm 8's ONLY residual is a grant1/c2 completion-order
      swap (goopg keeps the deadlock victim's XID active until the explicit
      COMMIT, so grant1 unblocks at c2 not at the abort — needs
      AbortTransaction-releases-XID-but-block-open semantics); perm 9 needs
      plpgsql-DO-block `REVOKE` parsing + lockRows-awaits-ACL; perm 10 needs
      `DELETE FROM pg_class` virtual-tuple semantics. Non-regression
      IntraGrantInplaceDb/TruncateConflict/TuplelockUpgradeNoDeadlock/DeadlockHard
      strict PASS; `-race` lock/deadlock executor tests green.
      **2026-06-25 (design 0118-0116, enabler — NOT a promotion):**
      `intra-grant-inplace` permutation 9 now byte-identical (first divergence
      L206→L235). Perm 9 (`b1 grant1 b3 sfu3 revoke4 c1 r3`) needed two fixes:
      (1) the plpgsql body parser rejected a bare `REVOKE` inside `revoke4`'s
      `DO $$ … $$` block (`GRANT`/`REVOKE` are not reserved keywords, so a leading
      `REVOKE` fell through to `parseAssign` → `expected ':=' or '=' after
      "revoke"` BEFORE the EXCEPTION handler could run) — added a leading
      `grant`/`revoke`-ident case to `parseStmt` routing to `parseSQLStmt` (general
      fix: GRANT/REVOKE work in any plpgsql function/DO body now;
      `TestParseGrantRevokeEmbeddedSQL`); (2) `sfu3`'s `pg_class` rowmark
      (`SELECT … FOR UPDATE`) completed immediately instead of `<waiting ...>`
      behind grant1's uncommitted ACL change — refactored `waitForTableACLChange`
      into the free `waitTableACLChange(ctx, oid)` and made
      `lockRowsOp.maybeRecordPgClassRowMark` record the mark FIRST (so a peer
      REVOKE blocks behind it) THEN await the table's ACL `xmax` (PG order:
      acquire LockTuple, then await tuple xmax). Blast radius nil (rowmark wait
      reached only for `pg_class` + `oid=<const>`; no-op with no pending ACL
      change). Spec stays `defer` — perm 10 (`drop1` = `DELETE FROM pg_class`)
      needs virtual-catalog tuple-delete + `SearchSysCacheLocked1` find-then-none.
      **2026-06-25 (design 0118-0117): `intra-grant-inplace` PROMOTED — all 10
      permutations byte-identical to PG 18.3; `TestPort_IsolationIntraGrantInplace`
      strict.** Closed perm 10 (`b1 drop1 b3 sfu3 revoke4 c1 r3`) where `drop1` is
      the locally-adapted `DELETE FROM pg_class WHERE relname = …` (virtual-catalog
      tuple delete). Six pieces: (1) new `tablePendingDropXID` catalog store (the
      `pg_class` delete xmax, mirrors `tableACLChangeXID`); (2)
      `deleteOp.tryPgClassCatalogDelete` routes `DELETE FROM pg_class WHERE
      {relname|oid}=…` in an explicit txn to a transaction-deferred table drop —
      records the delete xmax + defers removal to COMMIT via `AddPendingTableDrop`
      (relation stays visible until then); (3) `maybeRecordPgClassRowMark` also
      `waitTablePendingDrop` (shared deadlock-aware core `waitPgClassTupleXID`) and
      RETRACTS its rowmark when the post-wait scan yields 0 rows
      (`Catalog.ClearPgClassRowMark`) — PG holds no tuple lock when it locks
      nothing; (4) `waitForPgClassRowMarks` now waits on the MARK's release
      (`waitPgClassRowMarkReleased` polls the mark + keeps WFG deadlock check) not
      the holder's whole txn (`WaitForXID`), so revoke4 unblocks the instant sfu3
      releases — before r3; (5) the REVOKE re-checks `LookupTableByOID` after the
      wait → `XX000 cache lookup failed for relation <oid>` (PG's
      `SearchSysCacheLocked1` find-then-none); (6) three latent **plpgsql EXCEPTION**
      fixes — `parseTopBlock`/`parseNestedBlock` now set `ExceptionBlock.TryBody`
      (handlers were DEAD before: body ran as siblings so an error aborted before
      any handler), the handler binds `SQLERRM`/`SQLSTATE` frame vars, and
      `RAISE WARNING` routes to `AddWarning` (WARNING severity not NOTICE) — so the
      DO block catches the elog and re-raises the REDACTED WARNING. Blast radius
      nil. Gates: strict 10/10; non-regression `IntraGrantInplaceDb`/`RiTrigger`/
      `EvalPlanQualTrigger`/`DeadlockHard`/`TuplelockUpgradeNoDeadlock`/
      `CreateTrigger`/`DeadlockSimple`/`DeadlockSoft`/`ReceiptReport`/
      `ProjectManager` + plpgsql procedure/DO-txctl tests PASS; `-race`
      executor/catalog/plpgsql green; CSV D-002 flipped failed→pass + md regen.
      **2026-06-25 (design 0118-0123, enabler — NOT a promotion): `stats` rung 1.**
      The last failed M0118-0009 spec, `stats`, exercises the `pgstat` cumulative-
      statistics subsystem. Every permutation aborted in global `setup` at
      `SELECT pg_stat_force_next_flush()` (`42883` — pg_proc seed has the rows but
      `evalFuncCall` had no case → fell through to `evalStoredRoutineFuncCall`),
      and the `SET track_functions/track_counts/stats_fetch_consistency` steps hit
      unregistered GUCs. Landed three faithful low-blast pieces: (1) registered the
      three GUCs (`track_counts` bool/on/SUSET; `track_functions` enum
      none|pl|all/none/SUSET; `stats_fetch_consistency` enum
      none|cache|snapshot/cache/USERSET) mirroring `guc_tables.c` + matching
      `postgresql.conf.sample` lines (M0108 `TestSampleConfigCoversRegistry`
      parity); (2) `pg_stat_force_next_flush()` → void no-op (goopg has no async
      stats collector to flush); (3) `pg_stat_clear_snapshot()` → void no-op (no
      per-txn stats snapshot cache). First divergence advanced from global-setup
      failure (perm 0) → first permutation's `pg_stat_get_function_calls does not
      exist`. Spec stays `defer`. Remaining rungs (each Effort-L): runner echo of a
      setup query's result block; function-stats counters/getters +
      `pg_stat_reset_single_function_counters`; relation tuple stats +
      `pg_stat_get_xact_*`; `pg_stat_reset()`; real snapshot caching; 2PC stats
      interaction (rides 0118-0110). Tests `TestPgStatFlushSnapshotVoidNoops`
      (executor) + `TestStatsGUCs` (config).
      **2026-06-25 (design 0118-0124, enabler — NOT a promotion): `stats` rung 2.**
      Built the cumulative function-statistics subsystem + the runner setup-echo
      needed to observe it; first divergence advanced **L4 → L449** (the first ≈8
      permutations — function-stats counting, multi-connection accumulation,
      cross-txn flush, `pg_stat_reset*` — now byte-identical to PG 18.3, counts and
      `total_time>0`/`self_time>0` included). Pieces: (1) process-global
      `functionStatsManager` (`internal/executor/pgstat_functions.go`) with
      per-session `pending[sessionID][oid]` → cluster-global `shared[oid]` merged on
      `pg_stat_force_next_flush()`; one server process per cluster + fresh server per
      spec ⇒ store starts empty per cluster (like fresh initdb). (2) Counting hook in
      `executeStoredRoutine` (split out `dispatchStoredRoutineByLanguage`), gated by
      `track_functions` (all/pl/none; boot none ⇒ zero hot-path overhead); times the
      dispatch, `self==total` (spec only checks `>0`, guaranteed by `pg_sleep(10µs)`
      bodies). (3) `evalFuncCall` getters `pg_stat_get_function_calls/_total_time/
      _self_time(oid)` (NULL when no flushed stats; ms as NUMERIC so `>0` compares) +
      `pg_stat_reset_single_function_counters(oid)` + `pg_stat_reset()`;
      `pg_stat_force_next_flush()` upgraded from no-op to flush. OIDs via
      `'name'::regproc`→`Routine.OID`. (4) Runner: global setup blocks run via
      `execConnSetupCapture`, the tuple-returning setup result (`SELECT
      pg_stat_force_next_flush()`) echoed right after `starting permutation:` like
      `isolationtester.c` (safe: a passing strict spec with a tuple setup would
      already have diverged). Spec stays `defer` — new first divergence is the
      **uncommitted `DROP FUNCTION` cross-session visibility** case (no per-session
      MVCC catalog; same gap as alter-table-4). Tests `TestFunctionStatsManager` +
      `TestShouldTrackFunction`.
      **Remaining M0118-0009:** `stats` (pg_stat_* cumulative subsystem, Effort-L —
      rungs 1+2 enablers landed 0118-0123/0124; remaining rungs: uncommitted-DROP
      MVCC-catalog visibility, 2PC stat-drops, `stats_fetch_consistency` snapshot/
      cache models, relation tuple stats + `pg_stat_get_xact_*`, SLRU stats).
      **2026-06-25 (design 0118-0125, enabler — NOT a promotion): `stats` rung 3.**
      Made DROP FUNCTION transactional + finished the function-stats lifecycle;
      first divergence advanced **L449 → L1587**. (1) Deferred DROP FUNCTION
      (mirrors deferred DROP TABLE): `DeferredRoutineDrop` + `BasicSession.
      deferRoutineDrops`; in an explicit txn `execDropFunction` resolves the
      target (read-only `Routines.ResolveByName`/`ResolveBySig`) but defers removal
      to COMMIT (`ApplyDeferredRoutineDrops` on both `execCommit` + simple-query
      dispatch), so a concurrent session still calls it until commit; ROLLBACK
      discards, ROLLBACK TO cancels by depth. (2) Autocommit DROP now drops the
      function's cumulative stats too (`funcStats.dropFunction`, mirrors
      `pgstat_drop_function`). (3) `dropFunction(oid)` clears shared + every
      session's pending; `resetSingle`/`resetAll` ZERO entries in place (getter
      returns 0 not NULL; absent OID stays NULL). (4) DROP-then-CREATE same
      signature in one txn handled by `TakeDeferredRoutineDropMatching` (applies
      the deferred drop early). Known limitation (accepted, like deferred DROP
      TABLE): dropping session sees its own dropped function until commit (no
      per-session catalog MVCC). New first divergence L1587 =
      `stats_fetch_consistency='cache'/'snapshot'` (per-txn stat caching), then
      L2026 = 2PC `PREPARE TRANSACTION` stats. Tests `TestDeferredRoutineDropSession`
      + `TestRoutinesResolveForDrop` + updated `TestFunctionStatsManager`.
      **2026-06-25 (design 0118-0127, enabler — NOT a promotion): `stats` rung 5.**
      Cross-backend two-phase commit for RC/RR; first divergence advanced
      **L2036 → L2180**. The four "2PC handling of stat drops" permutations
      (S1-prepares then S1 OR S2 commits/aborts prepared) now match byte-for-byte.
      Prior 2PC (0118-0110) was same-backend only (prepared txn kept open on the
      originating connection's slot). New mechanism (PG dummy-PGPROC analogue):
      (1) `mvcc.Manager.DetachToDedicatedSlot(tx)` relocates an RC/RR txn off its
      backend's proc slot to a fresh dedicated slot (same XID/iso/snapshot, stays
      `inTxn=1` so writes stay visible-as-in-progress + committable from any
      backend; `ErrUnsupportedDetach` for SERIALIZABLE — Handle-keyed SSI). (2)
      Reserved proc-array region: top `ReservedPreparedSlots=64` slots
      `[ConnSlotCount, DefaultProcArraySize)` for detached prepared txns; ALL
      low-region allocators bounded to `ConnSlotCount` (server.go×2,
      dispatch_extended.go, copy.go, Begin auto-assign) so no backend reusing a
      `procNum=(pid-1)%size` clobbers a parked slot. (3) `preparedXactStore`
      (gid→parked `*connTxState`) + `connTxState.DetachPrepared` (moves session +
      deferred DROP FUNCTION drops + NOTIFYs + enum DDL into the holder, frees the
      live connection) + `BasicSession.RelocateTransaction`. (4)
      `execFinalizePrepared` retargets the executor context at the parked
      session/tx and routes the synthetic COMMIT/ROLLBACK through the canonical
      `executeOneSimpleStmt` path (reuses SSI check, ApplyDeferredRoutineDrops→
      funcStats.dropFunction, NOTIFY publish, lock release). SERIALIZABLE keeps the
      unchanged same-backend keep-open path. New first divergence L2180 = relation
      tuple stats (`pg_stat_get_numscans`/`_tuples_*`/`_xact_*`), then SLRU.
      Tests: `TestDetachToDedicatedSlot{,RejectsSerializable}` (mvcc, also -race);
      regression `TestPort_TwoPhaseCommitSameBackend` +
      `TestPort_IsolationPreparedTransactions{,CIC}` strict PASS.

      **2026-06-25 (design 0118-0128, enabler — NOT a promotion): `stats` rung 6.**
      Relation tuple statistics; first divergence advanced **L2180 → L2704**. All
      seven non-2PC table-stats permutations (drop-removes-stats, `track_counts
      off/on` access, cumulative seq-scan/DML counts) AND the 2PC COMMIT PREPARED
      permutations now match PG 18.3 byte-for-byte. New
      `internal/executor/pgstat_relations.go` mirrors the function-stats two-tier
      shape: `relStats` with `shared[oid]` + per-session `pending[sessionID][oid]`;
      `recordScan/Insert/Update/Delete` (INSERT +1 live/row; DELETE +1 dead −1
      live/row; UPDATE +1 dead/row, live unchanged — no HOT); `flush` merges
      pending→shared (via `pg_stat_force_next_flush()`); `get` returns 0 NOT NULL
      for absent OID (PG relation-getter semantics); `dropTable(oid)` clears shared
      + every session's pending (no revival on a peer's later flush). Getters in
      expr.go: `pg_stat_get_numscans/_tuples_returned/_tuples_fetched/_tuples_{inserted,
      updated,deleted}/_live_tuples/_dead_tuples/_vacuum_count` (live/dead clamp ≥0;
      fetched/vacuum_count=0). Counting hooks (gated by `track_counts`, boot on):
      `seqScanOp` (statReturned per tuple, record one scan in Close); `scanMatching`
      gains `statOID` param (UPDATE/DELETE base scan; FK sites pass 0); insert/update/
      deleteOp.Close per-statement rowsAffected; dropTableByRefImmediate drops stats.
      Per-statement Close recording = "applied at commit" for the autocommit
      simple-query path (no per-txn staging yet). New first divergence L2704 =
      `s1_…_rollback_prepared_a`: transactional-counter abort/2PC reconciliation
      (aborted insert/update → dead not live; `truncdropped` for in-txn TRUNCATE/DROP;
      2PC handoff of staged rel counters). Then later: index-scan `tuples_fetched`,
      VACUUM `vacuum_count`/live-dead recompute, SLRU stats. Tests:
      `pgstat_relations_test.go` (accumulate/flush/get, update dead-delta,
      drop-without-revival); `TestPort_IsolationStats` soft probe L2180→L2704.
      **2026-06-28 (design 0118-0131, enabler — NOT a promotion): `stats` rung 7.**
      Made the relation insert/update/delete counters + live/dead deltas
      **transactional**; first divergence advanced **L2704 → L3072** — every
      abort/`ROLLBACK PREPARED`, TRUNCATE-in-2PC, and cross-backend
      `COMMIT`/`ROLLBACK PREPARED` permutation now matches PG 18.3 byte-for-byte.
      New **staging** tier (`relXactCounters` ≈ `PgStat_TableXactStatus`) in front
      of `pending`: DML `op.Close` stages into `staging[sessionID][oid]` via
      `recordRel{Insert,Update,Delete}`; `execCommit`/`execRollback` fold it into
      `pending` with PG commit-vs-abort math (`AtEOXact_PgStat_Relations`: aborted
      insert/update → dead, aborted delete = live/dead no-op, attempted `tuples_*`
      always count); autocommit folds immediately. TRUNCATE (`recordRelTruncate` ≈
      `pgstat_count_truncate`) saves pre-truncate counts, resets staged counters,
      rides a `truncDropped` flag through `pending`→`shared` at flush (forgets
      already-flushed live/dead). 2PC: `PrepareRelStats` moves staging into a
      per-gid record at the detached PREPARE (`twophase.go`);
      `FinalizePreparedRelStats` folds it into the FINALISING backend's pending at
      COMMIT/ROLLBACK PREPARED (`pgstat_twophase_post{commit,abort}`). Scan
      counters stay non-transactional. New first divergence L3072 = `pg_stat_slru`
      SLRU stats (final rung). Known limitation: no sub-transaction (savepoint)
      staging tier yet (top-level only). Tests: `pgstat_relations_test.go`
      (staging+commit+flush, abort `1/8`, TRUNCATE `5/1/0/1/1`, 2PC commit/abort
      cross-backend, drop clears staging/prepared); `TestPort_IsolationStats` soft
      probe L2704→L3072; 2PC + DML+commit/rollback strict isolation regression PASS.
      **2026-06-28 (design 0118-0132, enabler — NOT a promotion): `stats` rung 8
      (final).** Implemented the `pg_stat_slru` notify `blks_zeroed` counter + the
      `block_size` preset GUC; first divergence advanced **L3072 → L3732** (the
      spec's LAST permutation) — every SLRU permutation (own-/separate-/
      uncommitted-transaction + all three `stats_fetch_consistency` models with
      `pg_stat_clear_snapshot`) now matches PG 18.3 byte-for-byte. New
      `internal/executor/pgstat_slru.go`: process-global `slruStatsManager` models
      the notify SLRU by tracking a modelled queue head and counting 8192-byte
      page crossings (the `SimpleLruZeroPage()` events upstream
      `asyncQueueAddEntries()` records; goopg's notify queue is an in-memory inbox,
      not an SLRU). Hook in `server/notify.go` `publishPendingNotify` (the single
      COMMIT-time publish point) sums buffered notifications' modelled byte length
      and calls `executor.RecordNotifyQueueWrite`, gated on a listener
      (`hub.hasAnyListener()` — PG only writes the shared queue when a backend
      LISTENs; counting at COMMIT yields `f` in-txn, `t` post-commit). SLRU joins
      the per-transaction snapshot (`funcStatSnapshot.slruFrozen`/`slruCache`;
      `ensureFullSnapshot` freezes both function + SLRU stores cross-kind under
      `snapshot`). `valuesOp.Open` serves `pg_stat_slru` from snapshot-aware
      `fetchSLRURows(ctx)`; static catalog `VirtualRows` fallback corrected to
      PG-17+ names (`notify`/`commit_timestamp`/…/`other`). Registered `block_size
      = 8192` (`PGC_INTERNAL` preset) so `current_setting('block_size')` resolves
      (was NULL → empty payload → 3 notifications collapsed to one zero-length
      entry, never crossing a page). **The ONE remaining blocker (L3732) is NOT
      SLRU:** the spec's last permutation relies on `track_functions = 'all'`
      LEAKING across permutations — upstream `isolationtester.c` opens one
      connection per session ONCE and reuses it for all permutations, so session
      GUCs persist; goopg's `IsolationRunner.runPermutation` reconnects per
      permutation, resetting `track_functions` to boot `none`, so the post-clear
      `pg_stat_get_function_calls` reads NULL not `1`. **Promoting `stats` to
      `pass` needs the runner connection-reuse change** (hoist per-session conns to
      spec scope, run only session `setup` per permutation) — shared test infra
      touching ~117 strict specs, deferred to its own loop (ledger 2026-06-28).
      Tests: `TestSLRUNotifyBlksZeroed` (executor) + `TestNotifyEntryBytes`/
      `TestHasAnyListener` (server), all `-race`; `TestFetchFuncStatConsistency`/
      `TestStatsGUCs` PASS; `stats.spec` probe L3072→L3732 + async-notify + 2PC
      regression PASS.
      **2026-06-28 (design 0118-0133, PROMOTION — the final rung): `stats` is now
      `pass`/pass-required.** Implemented the isolation-runner connection-reuse
      change flagged by rung 8 as the last blocker. `RunSpec` now opens one
      persistent connection per session ONCE per spec (`sessionConns` +
      `openSessionConns`) and reuses it across EVERY permutation — mirroring
      upstream `isolationtester.c` `main()`. `runPermutation` no longer
      opens/closes connections; it still re-runs each session's `setup` SQL per
      permutation (so `SET stats_fetch_consistency='none'` re-applies) but never
      resets the connection, so a step-set GUC like `SET track_functions='all'`
      persists into later permutations exactly as upstream — fixing L3732
      (`pg_stat_get_function_calls` now reads `1` not NULL). `application_name`
      SET + backend-PID record done once (stable); `sc.drainQueues()` clears the
      transient notice/notify queues per permutation (monotonic notice `total`
      kept — `notices <n>` blockers are delta-vs-baseline). Robustness: a
      post-permutation `sc.healthy(ctx)` (`SELECT 1`/session, 1 s deadline)
      rebuilds the set if a timed-out step left a connection busy; `close()` is
      3 s-bounded against a stuck lib/pq read. `TestPort_IsolationStats` flipped
      to `runIsoSpecStrict` — PASS (byte-for-byte, all permutations); CSV `D-002`
      rationale updated + md regenerated. Full `TestPort_Isolation*` strict suite
      re-run (4 parallel batches): zero regressions from connection reuse — the
      two failures observed (`vacuum-skip-locked`, `vacuum-concurrent-drop`) fail
      identically at HEAD (pre-existing SKIP_LOCKED WARNING gap, the "4 failed" of
      the 117/4 tally), and `tuplelock-upgrade-no-deadlock` was a load-induced
      flake (passes solo). **`stats` is the LAST failed M0118-0009 spec — all
      M0118-0009 specs are now resolved.**
      **2026-06-29 (loop #2, doc reconciliation — no engine change): M0118-0009
      CLOSED + lagging per-spec inventory reconciled.** The stats promotion commit
      998b9e97 (design 0118-0133) flipped only the suite-level
      `postgres-oracle-port-status.csv` D-002 row; the per-spec
      `postgres-oracle-target-inventory.csv` row for `stats.spec` was still `failed`,
      so `upstream-isolation-coverage.md` + `postgres-oracle-target-inventory.md`
      under-counted (117 pass / 4 failed). Re-verified `TestPort_IsolationStats`
      strict PASS (3.0 s), flipped the inventory CSV row failed→pass (comma-free
      rationale), regenerated both md via `gen-isolation-coverage` +
      `gen-oracle-inventory`. Isolation tally now **118 pass / 3 failed**. The 3
      remaining failed specs are NOT M0118-0009 — they are
      `predicate-gin`/`predicate-gist` (M0118-0002: GIN/GiST AMs + GiST page-grain
      SIREAD) and `deadlock-parallel` (M0118-0004: parallel-worker lock groups, no
      parallel query in goopg), each a genuinely Effort-L unbuilt subsystem.
      M0118-0009 checkbox ticked.

## M0119 — Deferral-Ledger Backlog Consumption (filed 2026-06-29)

Milestone: `docs/milestones/0119-deferral-ledger-backlog-consumption.md`
(**living milestone** — tasks are appended over time; see below). Source of truth:
`.ralph/deferral_ledger.md`. Goal: drive every open (`status = -`) ledger row to
closure — implement the deferred scope, or verify it already landed and mark the
row `resolved`.

**Per-task rule (applies to every M0119 implementation task):** before
implementation begins, the picking agent MUST (1) create a design doc at
`docs/design/<source-id>-NNNN-*.md` and index it in `docs/design/README.md`, and
(2) have that design doc pass an **agent review**. Implementation starts only
after the reviewed design doc exists. (The M0119-0001 triage task is
documentation-only and is exempt from the design-doc requirement.)

- [x] **M0119-0001 — Ledger triage pass (doc-only, no design doc). DONE
      2026-06-29 (loop #13).** Triaged all **224** open (`status = -`) ledger rows
      against current HEAD (fix_plan promotions + `docs/test-port/*.csv` +
      `git log` + code), via 6 parallel triage agents under a strict rule (a row
      is `resolved` only if EVERY deliverable named in its `deferred` column has
      landed). Result: **178 flipped `-`→`resolved`, 46 remain genuinely open.**
      The 46 open rows cluster cleanly into the seeded backlog tasks below:
      - **AC-003 amcheck server tier** (M0119-0006): ~29 rows — `003_check` /
        `004_verify_heapam` MVCC+TOAST / `005_opclass_damage` need hash/gist/gin/
        brin/spgist index AMs, `box`/`int4range`/`int4[]` types, STORAGE EXTERNAL
        TOAST corruption, opclass parity, the heapallindexed heap-scan producer,
        and the `datconnlimit = -2` invalid-DB filter (runtime shared-catalog write).
      - **CLOG tail** (M0119-0002): M0117-0006 Part C (remove resident banks),
        M0117-0007 Part B (live `synchronous_commit=off`), M0117-0008 Part B
        (on-disk `datfrozenxid` persistence).
      - **pg_dump 002–010 / DU-002** (M0119-0004): catalog-view parity umbrella,
        plus two general SQL-engine gaps surfaced here — NULLS-NOT-DISTINCT
        *enforcement* at INSERT/UPDATE (dump fidelity only today) and
        deferred-constraint *checking at COMMIT* (goopg checks immediately).
      - **pg_waldump WD-002** (M0119-0005); **pg_basebackup 011 in-place
        tablespace + 030 recvlogical** (M0095-0003 / M0119-0007).
      - **NEW open items not in the seeded list:** `M0118-0129` (HOT-update WAL
        atomicity — grouped old+new WAL record + cross-page HOT + TOAST orphan
        revert) and `M0118-0130` (B-tree buffer-pool concurrent pin/unpin
        correctness → Lehman-Yao lock coupling / `splitMu` removal). Both are
        deferred *improvements*, not failing specs.
      Two seeded tasks are now **empty backlog** (mark before picking): **M0119-0003**
      — the triage found every listed initdb option (`--encoding`,
      `--locale`/`--lc-*`/`--locale-provider`/`--icu-locale`, `--allow-group-access`,
      `--auth*`/`--pwfile`, `--sync-method`/`--no-sync-data-files`,
      `--set`/`--text-search-config`) AND the `--data-checksums` default-ON flip
      already landed on this branch (`internal/initdb/{encoding,locale}.go`,
      `Options.{SyncMethod,NoSyncDataFiles,…}`, `cmd/goopg start` `fs.Bool("k", true)`);
      ledger rows 18/22–27 are `resolved`. **M0119-0008** — predicate-gin/gist are
      PROMOTED (M0118-0002 group COMPLETE); the only residual isolation `failed`
      spec is `deadlock-parallel` (infeasible — no parallel-query lock groups), and
      it has no open ledger row of its own.
- [x] **M0119-0002 — CLOG tail, remaining Parts. FULLY CLOSED 2026-07-02 (loop #52).**
      (source: M0117-0007 / M0117-0008; see M0117 section + ledger rows).
      `M0117-0006`'s live store swap (Part B) and bank/flat-file removal (Part C) were
      DONE 2026-06-29/2026-07-01; `M0117-0007` (async-commit LSN tracking, all 3
      parts) was completed 2026-07-02 (loops #49-51); `M0117-0008` Part B (on-disk
      `datfrozenxid` persistence) — the last item this umbrella tracked — landed this
      loop (#52; see the M0117-0008 entry above). No open sub-items remain under this
      umbrella.
- [x] **M0119-0003 — initdb remaining options. RESOLVED by triage 2026-06-29
      (M0119-0001), no separate impl loop needed.** Every listed option already
      landed on this branch — `--encoding` (`internal/initdb/encoding.go`),
      `--locale`/`--lc-*`/`--locale-provider`/`--icu-locale` (`locale.go` +
      `Options.{Locale,LCCollate,…,LocaleProvider,ICULocale,ICURules}`),
      `--allow-group-access`, `--auth*`/`--pwfile` (`auth_bootstrap.go`),
      `--sync-method`/`--no-sync-data-files` (`Options.SyncMethod`/`NoSyncDataFiles`,
      `syncfs_linux.go`), `--set`/`--text-search-config` (`Options.{ExtraGUC,
      TextSearchConfig}`), and the `--data-checksums` **default-ON flip**
      (`cmd/goopg start` `fs.Bool("k", true, …)`). All wired through
      `initdb.Options` in `cmd/goopg/main.go`. Ledger rows 18/22–27 are `resolved`.
      (If `001_initdb.pl` TAP assertions per option are later wanted, that is a
      fresh D-001-scoped test-port task, not this implementation backlog.)
- [ ] **M0119-0004 — pg_dump 002–010 TAP** (source: M0110-0001; see M0110
      section). Schema dump, dump/restore round-trip, parallel, filter-file,
      connstr — advance the catalog-view parity battery slice-by-slice.
      **2026-06-29 (loop #14, design 0119-0004): NULLS-NOT-DISTINCT *enforcement*
      sub-feature LANDED** (one of the two general SQL-engine gaps the triage
      surfaced under this task). Runtime `NULLS NOT DISTINCT` uniqueness is now
      enforced for plain INSERT/UPDATE — `checkUniqueIndexes{ForInsert,ForUpdate}`
      fall back to a heap scan (`checkNullsNotDistinctViaHeapScan`) when a key
      column is NULL on an `idx.NullsNotDistinct` index, raising 23505 for a
      duplicate NULL pattern; btree/scan-probe/codec untouched, gated dead-code
      for every non-NND index. Tests `internal/executor/nulls_not_distinct_test.go`.
      **2026-06-29 (loop #15, design 0119-0004 §8): NND ON CONFLICT/upsert
      arbiter follow-up LANDED** — a NULL-keyed `INSERT … ON CONFLICT (nndcol)
      DO UPDATE/NOTHING` against an NND arbiter index now routes to the conflict
      action instead of inserting a duplicate. New `probeArbiterNND`
      (operators_upsert.go) reuses `checkNullsNotDistinctViaHeapScan`; the upsert
      `Next` loop falls back to it after the btree arbiter probe on the
      non-reordered path; `indexKeyColumnsChanged` made NND-aware
      (`nndKeyColumnsEqual`, operators_storage.go) so a NULL→NULL no-key-change
      DO UPDATE skips the pre-stamp self-conflict probe. 3 new upsert tests +
      `TestPort_IsolationInsertConflict*`/`Merge*` PASS; zero blast radius
      outside NND.
      **2026-06-29 (loop #16, design 0119-0004 §9): NND CREATE [UNIQUE] INDEX
      build over NULL-keyed data LANDED.** Both build paths
      (`collectBTreeEntries`/`backfillBTree` via `encodeCompositeBTreeKey`,
      operators_ddl.go) raised `42804 "column is null and cannot be indexed"` on
      ANY NULL key column — rejecting CREATE INDEX over any NULL-containing
      column (PG admits NULLs). `encodeCompositeBTreeKey` now returns
      `hasNullKey` instead of erroring; default/non-unique builds SKIP NULL-keyed
      rows (mirroring the runtime maintain path — no null bitmap), and a
      `unique && nullsNotDistinct` build dedups null-bearing rows via a
      build-local `seenNull`/`nndNullKeyDedupKey` map, raising 23505 on a
      duplicate NULL pattern. NND flag threaded as a new `nullsNotDistinct`
      param through `createBTreeIndex`→`bulkBuildBTree[WithPredicate]`→
      `collectBTreeEntries`/`backfillBTree` (all 16 call sites; 5 NND-capable
      forms forward the real flag, PK/non-unique pass false). 4 new build-path
      tests; full executor suite + -race PASS; zero blast radius for default
      indexes (NULL row now skipped not errored — strict improvement).
      **2026-06-29 (loop #17, design 0119-0004 §10): NND reordered
      partition-leaf arbiter LANDED — the final NND sub-feature (c).** An
      `INSERT … ON CONFLICT (nndcol) DO UPDATE|NOTHING` routing to a partition
      leaf whose column order differs from the parent (`partLeaf != nil`)
      skipped the NND heap-scan fallback entirely (`if !conflicted && partLeaf
      == nil`), wrongly inserting a duplicate NULL row where PG routes to the
      conflict action. `probeArbiterNND`/`checkNullsNotDistinctViaHeapScan`
      resolve key columns by NAME against `cols` and read the candidate at the
      matching ordinal, so the passed row must share `cols`' order; the loop now
      passes `insertedForLeaf` (already leaf-ordered on the reordered path, ==
      `inserted` on every non-reordered path) and drops the guard. conflictRow
      is decoded in leaf order as before, so the downstream leaf→parent remap is
      unchanged. New test `TestNullsNotDistinctOnConflictReorderedPartitionLeaf`
      (parent `(a,b,c)` / leaf `(c,b,a)` / NND `(a,b)`; DO NOTHING skip + DO
      UPDATE target; confirmed RED→GREEN); `-race` Upsert/Conflict/Partition +
      `TestPort_IsolationInsertConflict*`/`Merge*` PASS; zero blast radius
      outside NND. **All three NND enforcement sub-features (a)–(c) are now
      landed.**
      **2026-06-29 (loop #18, design 0119-0004-set-constraints-deferred):
      `SET CONSTRAINTS` runtime constraint-deferral control LANDED** (the second
      general SQL-engine gap the triage surfaced under this task — the
      deferred-constraint-checking-at-COMMIT engine gap). `SET CONSTRAINTS
      { ALL | name [, …] } { DEFERRED | IMMEDIATE }` now parses
      (`parser.SetConstraintsStmt`) and controls FK-deferral timing via session
      state on `BasicSession` (`constraintsAllMode` + per-name `constraintDeferral`,
      reset per txn) consumed by a new `fkCheckDeferred(ctx, fk)` helper that
      replaces the four open-coded `Deferrable && InitiallyDeferred && inTx` sites
      in operators_fk.go (byte-identical with no override). `… IMMEDIATE` runs the
      now-immediate queued checks at the SET statement; the simple-query COMMIT
      path (which bypasses `execCommit`) gained `executor.RunDeferredFKChecks`
      **gated on `ConstraintsOverrideActive()`** so plain `INITIALLY DEFERRED`
      keeps its prior simple-query behaviour (activating the commit check
      unconditionally regressed the pass-required `fk-snapshot` spec — its
      deferred-RI check needs a fresh "latest" snapshot to see a
      concurrently-committed *partitioned* parent, a separate pre-existing gap).
      Wired through query.go (simple) + extended no-op + `SET CONSTRAINTS` command
      tag; removed the old `compatNoopCommandTag` no-op. Tests: parser
      (4 shapes), executor session (precedence + matching), end-to-end
      `TestPort_SetConstraintsDeferral` (control immediate-fail / deferred ordered
      / raise-at-COMMIT / raise-at-IMMEDIATE). fk-snapshot + full FK isolation
      group + executor/parser/server units PASS.
      **2026-06-29 (loop #19, design 0119-0004-deferred-ri-fresh-snapshot):
      deferred-RI fresh-snapshot LANDED — the `ConstraintsOverrideActive` gate is
      DROPPED.** Plain `DEFERRABLE INITIALLY DEFERRED` FKs are now enforced at
      `COMMIT` on the simple-query path (psql/lib/pq/isolation runner — bypasses
      `execCommit`), not only under a `SET CONSTRAINTS … DEFERRED` override. New
      exported `mvcc.Manager.FreshSnapshot()` (wraps `captureSnapshot()` → latest
      committed state, CLOG + partition-detach epoch attached) mirrors PG's
      `RI_FKey_check`/`ri_PerformCheck` `GetLatestSnapshot()` push;
      `runAllDeferredFKChecks` saves `ctx.Snap`, installs the fresh snapshot for
      the queued checks, restores via `defer` — one chokepoint covering BOTH the
      `execCommit` and dispatch paths. This is what let the gate drop without
      regressing `fk-snapshot`: a `REPEATABLE READ` session's pinned snapshot can't
      see a concurrently-committed partitioned parent, but the fresh snapshot can.
      Own uncommitted child rows stay visible (`TupleVisibleSubxact` self-check on
      `ctx.Tx.XID`); zero blast radius on an empty deferred queue (early return →
      no snapshot taken). `dispatch.go` gate `&& sess.ConstraintsOverrideActive()`
      removed. Tests: `TestPort_IsolationFkSnapshot` (7 perms) + full FK isolation
      group PASS; new `TestPort_InitiallyDeferredFKCommit` (plain INITIALLY
      DEFERRED, no override — ordered commit + raise-at-COMMIT + orphan rollback);
      `TestPort_SetConstraintsDeferral` PASS; `-race` mvcc + executor PASS.
      **2026-06-29 (loop #20, design 0119-0004-deferred-unique): deferred
      UNIQUE/PK constraint checking LANDED.** A `UNIQUE`/`PRIMARY KEY` declared
      `DEFERRABLE INITIALLY DEFERRED` (or deferred via `SET CONSTRAINTS …
      DEFERRED`) now queues its uniqueness check to COMMIT instead of raising
      immediately — `UPDATE t SET id = id+1` over a contiguous range succeeds;
      a genuine duplicate surviving to COMMIT raises 23505 at COMMIT. New
      `BasicSession.deferredUniqChecks` queue + `internal/executor/deferred_unique.go`
      mirror the deferred-FK structure: `uniqueCheckDeferred(ctx, idx)` (analogue
      of `fkCheckDeferred`, short-circuits on `!idx.Deferrable`) gates the enqueue
      at `checkUniqueIndexes{ForInsert,ForUpdate}`; `RunDeferredUniqueChecks`
      drains at COMMIT under `mvcc.Manager.FreshSnapshot()` and
      `recheckDeferredUniqueKey` counts distinct-live btree tuples (≥2 = 23505).
      Both commit chokepoints (`execCommit` + simple-query `dispatch.go`, sharing
      one rollback block) + `SET CONSTRAINTS … IMMEDIATE` wired. Gated on
      `idx.Deferrable` → zero blast radius (pgbench/TPC-H PK not deferrable). Tests
      `TestPort_InitiallyDeferredUniqueCommit` + `TestPort_SetConstraintsUniqueDeferral`;
      executor + `-race` + FK-deferral regression PASS. Catalog already carried
      `Index.Deferrable`/`InitiallyDeferred` (no parser/catalog change).
      **2026-06-29 (loop #22, design 0119-0004-deferred-unique-nnd): deferred
      UNIQUE with NULLS NOT DISTINCT (NULL-keyed rows) LANDED.** Composes the
      deferred-unique queue (loop #20) with NND enforcement (loops #14–#17),
      which previously did not interoperate: in
      `checkUniqueIndexes{ForInsert,ForUpdate}` the `key == nil` arm (a NULL key
      column on an NND index — found by heap scan, not the btree) ran the
      **immediate** `checkNullsNotDistinctViaHeapScan` raise unconditionally,
      never consulting `uniqueCheckDeferred`. So `UNIQUE NULLS NOT DISTINCT …
      DEFERRABLE INITIALLY DEFERRED` wrongly raised 23505 immediately on a
      transient NULL duplicate and never queued a COMMIT recheck. Fix: the NND
      arm now defers when `uniqueCheckDeferred(ctx, idx)`, queuing a
      `DeferredUniqueCheck` carrying the candidate's per-key-column NULL pattern
      (new `NNDKeyCols []DeferredNNDKeyCol{ColName,Null,Key}` on the session
      struct; dedup widened via `sameNNDKeyCols` so `(null,1)` vs `(null,2)`
      queue separately). The immediate heap scanner's per-column descriptor
      (`nndKeyCol`) is lifted to package scope and its scan loop extracted to
      `scanNNDLiveMatches(…, stopAt)`; immediate path uses `stopAt=1` (candidate
      not yet inserted, any match = dup), the new `recheckDeferredNNDUniqueKey`
      uses `stopAt=2` (candidate is itself one live match at COMMIT, ≥2 = 23505),
      both under the already-installed `FreshSnapshot()` + shared
      `isLiveForUniqueCheck`. `runAllDeferredUniqueChecks` branches on
      `c.NNDKeyCols != nil`; `SET CONSTRAINTS … IMMEDIATE` rides the same branch.
      Gated on `idx.NullsNotDistinct && rowHasNullKeyColumn && uniqueCheckDeferred`
      → zero blast radius. Oracle-grounded vs PG 18.3 (transient resolved →
      commit; surviving dup → 23505 at COMMIT `Key (a)=(null) already exists.`;
      distinct NULL patterns coexist; SET CONSTRAINTS path). Tests
      `TestPort_InitiallyDeferredNNDUniqueCommit` + `TestPort_DeferredNNDMultiColumn`
      + `TestPort_SetConstraintsNNDDeferral`; full executor + `-race` + prior
      deferred-unique/FK e2e PASS.
      **2026-06-29 (loop #23, design 0119-0004-deferred-exclusion): deferred
      EXCLUDE constraint checking LANDED — the last deferrable constraint kind.**
      An `EXCLUDE … DEFERRABLE INITIALLY DEFERRED` (or deferred via SET
      CONSTRAINTS) now tolerates a transient conflict mid-transaction and enforces
      at COMMIT (23P01), mirroring deferred-unique one-for-one. New
      `BasicSession.deferredExclChecks []DeferredExclusionCheck{TableName,
      IndexName, ExclusionOp, Key, BoxStr, Detail}` + `ExclusionConstraintDeferred`
      (wraps the shared `constraintDeferredByName`). New
      `internal/executor/deferred_exclusion.go`: `excludeCheckDeferred` gates the
      enqueue at the single INSERT chokepoint `checkExclusionConstraintsForInsert`
      (queue + continue); `queueDeferredExclusionCheck` captures the `WITH =`
      btree key or `WITH &&` box text; `RunDeferredExclusionChecks` drains at
      COMMIT under `FreshSnapshot()` with the ≥2-live rule
      (`recheckDeferredExclusionEq` btree count, `recheckDeferredExclusionOverlap`
      box-overlap count). Wired at all three boundaries (execCommit + simple-query
      dispatch + SET CONSTRAINTS IMMEDIATE) after FK + UNIQUE. Gated on
      `idx.IsExclusion && idx.Deferrable` → zero blast radius; no parser/catalog
      change (deferrable flags already populated). Oracle-grounded vs PG 18.3.
      Tests `TestPort_InitiallyDeferredExclusionCommit` +
      `TestPort_SetConstraintsExclusionDeferral`; full executor + `-race` +
      prior deferred FK/UNIQUE/NND e2e + fk-snapshot PASS.
      **2026-06-29 (loop #24, design 0119-0004-identity-sequence-options, DU-002
      slice 303): identity-column sequence options round-trip in pg_dump.** A
      `GENERATED … AS IDENTITY (sequence_options)` column's backing sequence now
      round-trips EVERY option (INCREMENT BY / MINVALUE / MAXVALUE / CACHE /
      CYCLE), not just START WITH. The identity parser previously scanned the
      parenthesised clause for ONLY the `start` keyword and the executor
      hard-coded the backing sequence to `increment=1, cycle=false, cache=1,
      type-default min/max` (`RegisterSequence(seqName, seqStart, 1, seqMin,
      seqMax, false)`), so the other options were silently dropped — wrong
      step/bounds on restore AND wrong runtime `nextval()` step. Fix: parse the
      full sequence-option grammar (mirroring `parseCreateSequenceTail`) into new
      `ColumnDef.Identity{Increment,Min,Max,Cache,Cycle}` (ast.go) + matching
      `catalog.Column` fields (the registration loop iterates catalog columns),
      threaded through the executor to `RegisterSequence` + `SetSequenceCache`.
      The dump path is unchanged (slice 120 renders the block from `pg_sequence`).
      Zero blast radius (serial columns never set the fields; new catalog fields
      default nil/false; TPC-H/pgbench carry no identity columns). Slice 303
      (`idrich` all options + CYCLE; `idbd` BY DEFAULT + explicit increment + `NO
      MINVALUE/NO MAXVALUE`) pinned byte-for-byte vs real pg_dump 18.3;
      parser+executor+catalog suites PASS. Surfaced separate gap: `CREATE
      SEQUENCE … OWNED BY schema.table.column` mis-resolves a 3-part qualified
      owner (`sequence cannot be owned by relation "public"`).
      **2026-06-29 (loop #26, design 0119-0004-replica-identity, DU-002 slice
      305): REPLICA IDENTITY round-trip in pg_dump — LANDED DEFAULT/FULL/NOTHING;
      fixes a pervasive latent bug.** pg_dump emits `ALTER TABLE ONLY <t> REPLICA
      IDENTITY {FULL|NOTHING}` whenever `pg_class.relreplident != 'd'`
      (pg_dump.c:17781). goopg HARDCODED `relreplident='n'` (NOTHING) in the heap
      pg_class row builder pg_dump reads (`buildUserPGClassRow`; comment
      mislabelled it "DEFAULT"), so EVERY dumped table got a spurious `... REPLICA
      IDENTITY NOTHING;` — latent because no slice asserted the clause's *absence*
      and goopg never parsed `ALTER TABLE ... REPLICA IDENTITY`. Fix: (1) default
      corrected to `'d'` via new `catalog.ReplIdentOrDefault` routed through both
      the heap builder and the `catalog.go` VirtualRows sibling; (2) new
      `catalog.Table.ReplicaIdentity` + `AlterTableReplicaIdentity` parser action
      (`DEFAULT`/`FULL`/`NOTHING`/`USING INDEX`; FULL/NOTHING are keyword tokens →
      both spellings accepted) → executor sets the field and flushes the pg_class
      HEAP row via the delete-old-rows + `syncTableToCatalogHeap` path (same as SET
      STORAGE/COMPRESSION). Dump-fidelity only (goopg has no logical replication).
      Zero blast radius on query/DML (the wrong `'n'` default → correct `'d'`
      removes spurious lines).
      **2026-06-29 (loop #27, design 0119-0004 slice 306): REPLICA IDENTITY USING
      INDEX (`relreplident='i'`) — LANDED.** pg_dump emits `ALTER TABLE ONLY <t>
      REPLICA IDENTITY USING INDEX <idx>` at INDEX-dump time keyed on
      `pg_index.indisreplident` (pg_dump.c:18186), NOT at table-dump time. New
      `catalog.Index.IsReplicaIdentity` projected to indisreplident in BOTH
      pg_index builders (virtual `catalog.go` VirtualRows pg_dump reads + heap
      `buildUserPGIndexRow` for restart durability). Executor
      `resolveReplicaIdentityIndex` validates the named index per PG's
      `check_replica_identity` (exists `42704`, unique `42809`, immediate `0A000`,
      non-expression `0A000`, non-partial `0A000`, NOT-NULL keys `42809`), sets the
      table to `'i'`, and — mirroring `relation_mark_replica_identity` — marks the
      chosen index + clears every other index of the table, re-syncing each
      changed index's pg_index heap row (`resyncIndexReplicaIdentHeap`,
      stamp-old + writeHeapRowCanonical). Tests
      `TestParseAlterTableReplicaIdentity` + `TestUserPGClassRowReplicaIdentity` +
      `TestUserPGIndexRowReplicaIdentity` + slices 305/306 (`ri_full`→FULL,
      `ri_nothing`→NOTHING, `ri_index`/`ri_uidx`→USING INDEX present; foo/bar/part
      default → no clause) PASS vs real pg_dump 18.3; full executor + parser +
      catalog suites PASS. **Slice 307 (loop #28):** `NOT VALID` FOREIGN KEY
      round-trip — `buildForeignKeyDefString` now appends the ` NOT VALID` tail
      (`pg_get_constraintdef_worker` ruleutils.c:2604) for `convalidated='f'`;
      previously dumped without it → silent re-validate on restore. Design
      `0119-0004-fk-not-valid-roundtrip.md`. **Slice 308 (loop #29):** `NOT VALID`
      CHECK constraint round-trip — new `catalog.NamedCheckConstraint.NotValid` +
      `AddCheckWithNotValid`; parser now captures `act.NotValid` on `ADD CONSTRAINT
      … CHECK … NOT VALID` (previously discarded); `pg_constraint` projects
      `convalidated='f'`; `pg_get_constraintdef` CHECK branch appends ` NOT VALID`.
      pg_dump dumps it as a separate post-data `ALTER TABLE … ADD CONSTRAINT …
      CHECK ((val > 0)) NOT VALID;` (separate=!validated, pg_dump.c:9757). Design
      `0119-0004-check-not-valid-roundtrip.md`. **Slice 309 (loop #30):** FK
      `MATCH FULL` round-trip — new `parseFKMatchType` helper threads a
      `MatchFull bool` through parser (all three FK forms), AST,
      `catalog.ForeignKey`, the three executor FK-build sites, the `pg_constraint`
      builder (`confmatchtype='f'` vs `'s'`), and `buildForeignKeyDefString`
      (emits ` MATCH FULL` between the REFERENCES list and ON UPDATE/DELETE, per
      ruleutils.c). pg_dump now re-emits `ADD CONSTRAINT mf_child_fk FOREIGN KEY
      (a, b) REFERENCES public.mf_ref(a, b) MATCH FULL;`. Design
      `0119-0004-fk-match-full-roundtrip.md`. **Slice 310 (loop #31):** partial
      EXCLUDE constraint `WHERE` predicate round-trip — `parseExcludeConstraint`
      never consumed a trailing `WHERE`, so a partial exclusion silently degraded
      into an all-rows exclusion on restore. New `TableConstraintDef.ExclusionWhere`
      (parsed via `p.parseExpr()`); executor `applyExclusionPredicate` threads it
      onto the backing index's `PredicateString` (`defaultExprToSQL`) at all three
      EXCLUDE build sites; `buildConstraintDefString` EXCLUDE branch appends
      ` WHERE (pred)` after the operator/INCLUDE list and before DEFERRABLE
      (mirroring `pg_get_indexdef_worker`, ruleutils.c:1564). pg_dump re-emits
      `ADD CONSTRAINT pex_excl EXCLUDE USING btree (a WITH =) WHERE (b > 0);`.
      Design `0119-0004-partial-exclude-where-roundtrip.md`. **Slice 314 (loop #35):**
      CREATE STATISTICS round-trip — pg_dump's `dumpStatisticsExt` selects
      `pg_get_statisticsobjdef(oid)`, but goopg's parser discarded the `(kinds)`
      clause AND the `ON` column list, `catalog.StatisticsObject` carried neither,
      and `pg_get_statisticsobjdef` was unimplemented (NULL) — so the object was
      silently dropped from the dump. Threaded `Kinds`/`Columns`/`HasExpr` through
      parser→`catalog.StatisticsObject` (`RegisterStatisticsFull`); new
      `StatisticsByOID` + `BuildStatisticsObjDef` (mirrors ruleutils.c
      `pg_get_statisticsobj_worker`: kinds clause suppressed when all three enabled
      or single-column; schema-qualified FROM) + `pg_get_statisticsobjdef(oid)`
      builtin. Expression statistics flagged + omitted (deparser follow-up). Also
      fixed a latent `IF NOT EXISTS` parse bug (`acceptIdentKeyword("if")` never
      matched the keyword token). Design `0119-0004-create-statistics-roundtrip.md`.
      **Slice 316 (loop #38):** expression extended-statistics round-trip —
      slice 314 declined any `CREATE STATISTICS s ON (a + b) FROM t` (`HasExpr`
      set → `BuildStatisticsObjDef` returned ""), so `dumpStatisticsExt` (which
      emits `pg_get_statisticsobjdef(oid)` verbatim) silently dropped the object.
      Parser now captures the ON-list expression via `p.parseExpr()` into
      `CreateStatisticsStmt.Exprs []Expr` (tolerant `p.idx` rewind + skip on parse
      error); executor deparses each via `defaultExprToSQL` (already parenthesizes
      binary ops / leaves bare function calls unwrapped — matches ruleutils.c
      `looks_like_function`) into `catalog.StatisticsObject.Exprs []string`;
      `BuildStatisticsObjDef` declines only when `HasExpr && len(Exprs)==0`, counts
      `ncolumns = len(Columns)+len(Exprs)`, and emits columns-then-expressions
      (PG order). No `pg_statistic_ext` view change — `getExtendedStatistics`
      reads only oid/stxname/stxnamespace/stxowner/stxrelid/stxstattarget. Design
      `0119-0004-expression-statistics-roundtrip.md`.
      **Slice 317 (loop #39):** `ALTER STATISTICS … SET STATISTICS n` round-trip —
      new parser `AlterStatisticsStmt` + `catalog.StatisticsObject.StatTarget *int`;
      `pg_statistic_ext.stxstattarget` projects the value (else -1 = NULL-equiv);
      `dumpStatisticsExt` re-emits the ALTER only when target >= 0. Design
      `0119-0004-alter-statistics-set-statistics.md`.
      **Slice 318 (loop #40):** extended-statistics ownership round-trip —
      test-only guard asserting `ALTER STATISTICS <nsp>.<name> OWNER TO <role>;`
      for all four fixture stats objects. pg_dump emits ownership from the TOC
      archive entry (`dumpStatisticsExt` `.owner = getRoleName(stxowner)`;
      `_printTocEntry` renders it because `"STATISTICS"` is in
      `_getObjectDescription`'s ALTER-able list). Exercises the goopg
      `pg_statistic_ext.stxowner = 10` projection end-to-end; no production code
      changed (already worked). Design `0119-0004-statistics-owner-roundtrip.md`.
      **Slice 319 (loop #41):** CREATE TRIGGER round-trip — goopg executes user
      triggers but they were invisible to pg_dump (lost on dump/restore). Three
      gaps fixed: (1) `pg_class.relhastriggers` hardcoded `'f'` in the heap
      pg_class builder, and pg_dump's getTriggers (pg_dump.c:8523) only probes
      pg_trigger for tables whose relhastriggers is true — now projects `'t'`
      when `len(t.Triggers)>0`; (2) `pg_trigger.VirtualRows` returned nil — now
      projects one row per trigger (oid/tgrelid/tgname, PG tgtype bitmask, tgfoid
      from routines, tgenabled='O', tgisinternal='f', tgparentid=0); (3)
      `pg_get_triggerdef` registered in pg_proc but unimplemented — new
      `evalFuncCall` case + `buildTriggerDefString` mirroring ruleutils.c
      pg_get_triggerdef_worker (timing kw; OR-joined events in fixed
      INSERT/DELETE/UPDATE/TRUNCATE order; schema-qualified table+func for
      search_path=''; quoted args). New `catalog.Trigger.OID` via AllocOID at
      CREATE TRIGGER. Scope = basic trigger form the parser captures (no
      WHEN/REFERENCING/UPDATE OF/CONSTRAINT). Dump-fidelity only; zero blast
      radius (relhastriggers='t' only for triggered tables; Trigger.OID
      defaults 0). Tests `TestBuildTriggerDefString` + slice 319 in
      `TestPort_PgDumpConnectionSetup` (BEFORE INSERT OR UPDATE row-level +
      AFTER DELETE statement-level) PASS vs real pg_dump 18.3. Design
      `0119-0004-trigger-roundtrip.md`.
      **Slice 320 (loop #42):** clustered-index round-trip — `CLUSTER <t> USING
      <idx>` selects a table's clustering index (`pg_index.indisclustered`); pg_dump
      re-emits `ALTER TABLE <t> CLUSTER ON <idx>;` after the index's CREATE INDEX
      (dumpIndex, pg_dump.c:18141) or constraint ADD CONSTRAINT (dumpConstraint,
      :18483). goopg's CLUSTER executor was a no-op that ignored USING, and both
      pg_index builders hardcoded `indisclustered='f'`, so the selection was lost
      on dump/restore. Fix mirrors REPLICA IDENTITY USING INDEX (slice 306): new
      `catalog.Index.IsClustered` projected in BOTH pg_index builders (virtual +
      heap `buildUserPGIndexRow`); `clusterOp.Next()` resolves the named index in
      IndexesOnTable (42704 if absent), sets the flag + clears the table's other
      indexes (mark_index_clustered), re-syncing each changed pg_index heap row.
      `resyncIndexReplicaIdentHeap` renamed `resyncIndexHeapRow` (full-row rewrite,
      now shared). Dump-fidelity only (no physical reorder); IsClustered defaults
      false → zero blast radius. Slice 320 in `TestPort_PgDumpConnectionSetup`
      (plain index = dumpIndex path; PK index = dumpConstraint path) PASS vs real
      pg_dump 18.3. Design `0119-0004-cluster-roundtrip.md`.
      **Slice 321 (loop #43):** `ALTER TABLE … CLUSTER ON <idx>` / `SET WITHOUT
      CLUSTER` restore form — slice 320 made the clustered index round-trip *out*
      (pg_dump emits `ALTER TABLE <t> CLUSTER ON <idx>;`) but goopg could not
      parse/execute that emitted clause, so it produced a dump it could not
      restore into itself. New parser `AlterTableClusterOn` (`CLUSTER ON ident`)
      + `AlterTableSetWithoutCluster` (`SET WITHOUT CLUSTER`, gated so it doesn't
      shadow `SET (reloptions)`); executor shares `markTableClusterIndex` /
      `clearTableClusterIndex` helpers (extracted from `clusterOp`) between the
      `CLUSTER … USING` statement and the ALTER actions. Dump-fidelity only — sets
      the same `IsClustered`/`indisclustered` state as 320, so output is
      unchanged; only widens the accepted SQL surface. CLUSTER round-trip now
      closed. Tests `TestParseAlterTableClusterOn` +
      `TestDDLAlterTableClusterOnRoundTrip` + slice-320 `TestPort_PgDumpConnectionSetup`
      PASS. Design `0119-0004-cluster-on-restore.md`.
      **Slice 323 (loop #45):** `CREATE POLICY` round-trip in pg_dump — the
      per-policy half of RLS (the ENABLE flag landed slice 322). pg_dump's
      `getPolicies` reads `pg_policy` and `dumpPolicy` re-emits `CREATE POLICY …`;
      goopg had NO CREATE POLICY (parse error) + an empty `pg_policy` stub, so a
      policy was silently lost on dump. Feasible because slice 322's `rls_t`
      proves `getPolicies` already executes (0 rows) and a PUBLIC policy
      (`polroles='{0}'`) short-circuits the lazy `CASE` before the risky `pg_roles`
      ARRAY subquery; `pg_get_expr` is a pass-through. New `CreatePolicyStmt`/
      `DropPolicyStmt` (parsed with general `parseExpr` → idempotent re-dump),
      `catalog.PolicyInfo`/`Table.Policies`, `formatExprForAttrdef` ColumnRef case,
      `pg_policy.VirtualRows` (polqual/polwithcheck retyped `pg_node_tree` for NULL
      semantics, fully-parenthesized via the catalog pg_get_expr deparser →
      `USING ((a > 0))`), `execCreatePolicy`/`execDropPolicy` (virtual catalog, no
      heap sync). Dump-fidelity only — goopg enforces no RLS. Tests
      `TestParseCreatePolicy`/`TestParseDropPolicy` + `TestDDLCreatePolicyRoundTrip`
      + slice-323 `TestPort_PgDumpConnectionSetup` (PERMISSIVE/RESTRICTIVE,
      FOR ALL/SELECT/INSERT, USING + WITH CHECK; byte-identical vs real pg_dump
      18.3) PASS. Design `0119-0004-create-policy-rls.md`. **Deferred:** named-role
      `TO role` policies (no per-role OID registry; the `pg_roles` ARRAY subquery
      path unverified).
      **Slice 326 (loop #49):** column-specific `UPDATE OF col1, col2` trigger
      round-trip — slice 319 made the basic CREATE TRIGGER round-trip but skipped
      the column-list form: `parseCreateTriggerTail` consumed only bare `UPDATE`,
      so the `OF` token tripped the event loop and the clause was lost. Fix
      (dump-fidelity only — `fireTriggers` ignores the restriction):
      `CreateTriggerStmt.UpdateColumns`/`Trigger.UpdateColumns` capture the list;
      `buildTriggerDefString` emits ` OF c1, c2` after UPDATE (each via
      `pgQuoteIdent`, ruleutils.c order); `pg_trigger.tgattr` now projects the
      1-based attnums (space-separated int2vector via `triggerUpdateColAttrs`).
      Tests `TestParseCreateTriggerUpdateOf` + `TestBuildTriggerDefString`
      (2 new cases) + slice-326 `TestPort_PgDumpConnectionSetup` (`trg_uof BEFORE
      INSERT OR UPDATE OF a, b`; byte-identical vs real pg_dump 18.3) PASS.
      Design `0119-0004-trigger-update-of-columns.md`.
      **Slice 327 (loop #50):** CONSTRAINT TRIGGER round-trip — `CREATE
      CONSTRAINT TRIGGER` could not even re-parse (the `parseCreate` CONSTRAINT
      case matched via `acceptIdentKeyword("constraint")`, but CONSTRAINT is a
      *reserved* keyword token, so the branch was dead). pg_get_triggerdef emits
      `CREATE CONSTRAINT TRIGGER … [NOT ]DEFERRABLE INITIALLY {IMMEDIATE|DEFERRED}`
      between the ON-table name and FOR EACH ROW (gated on a valid tgconstraint;
      full clause always spelled out). Fix (dump-fidelity only — no deferred
      firing): parser `CreateTriggerStmt.{IsConstraint,Deferrable,InitDeferred}`,
      `parseCreateTriggerTail(pos, isConstraint)` matches `KwConstraint` and
      reuses `parseConstraintDeferrable`; catalog
      `Trigger.{IsConstraint,Deferrable,InitDeferred,ConstraintOID}` +
      `pg_trigger.tgconstraint`/`tgdeferrable`/`tginitdeferred` projection;
      executor `execCreateTrigger` allocs ConstraintOID; `buildTriggerDefString`
      emits `CREATE CONSTRAINT TRIGGER` + the deferrability clause. Tests
      `TestParseCreateConstraintTrigger` + `TestBuildTriggerDefString` (2 new
      cases) + slice-327 `TestPort_PgDumpConnectionSetup` (`trg_cdef` default +
      `trg_cdfr` DEFERRABLE INITIALLY DEFERRED; byte-identical vs real pg_dump
      18.3) PASS. Design `0119-0004-constraint-trigger-pgdump.md`.
      **2026-06-30 (loop #51, design 0119-0004-trigger-referencing-transition-tables,
      DU-002 slice 328):** REFERENCING transition-table trigger round-trips. parser
      `CreateTriggerStmt.{OldTransitionTable,NewTransitionTable}` +
      `parseCreateTriggerTail` `REFERENCING { OLD | NEW } TABLE [AS] <name>` branch
      (any order, optional AS); catalog `Trigger.{OldTransitionTable,NewTransitionTable}`
      → `pg_trigger.tgoldtable`/`tgnewtable`; executor `execCreateTrigger` copies;
      `buildTriggerDefString` emits `REFERENCING OLD TABLE AS … NEW TABLE AS …`
      (OLD first) between the deferrability clause and FOR EACH. Tests
      `TestParseCreateTriggerReferencing` + `TestBuildTriggerDefString` (2 new
      cases) + slice-328 `TestPort_PgDumpConnectionSetup` (`trg_ref` OLD+NEW,
      `trg_refn` NEW-only; byte-identical vs real pg_dump 18.3) PASS. Design
      `0119-0004-trigger-referencing-transition-tables.md`. **Still open:** trigger
      `WHEN`/tgqual (last `pg_get_triggerdef` gap; needs OLD/NEW-qualified expr
      deparser).
      **2026-06-30 (loop #52, design 0119-0004-trigger-when-condition, DU-002
      slice 329):** WHEN-condition trigger round-trips — the LAST
      `pg_get_triggerdef` gap. parser `CreateTriggerStmt.WhenExpr` +
      `parseCreateTriggerTail` now parses `WHEN '(' a_expr ')'` (was discarded via a
      paren-balance token loop); catalog `Trigger.WhenExpr` (tgqual projection stays
      empty — pg_dump drives off `pg_get_triggerdef`); executor `execCreateTrigger`
      copies it; `buildTriggerDefString` emits `WHEN (<cond>) ` between FOR EACH and
      EXECUTE FUNCTION via the executor twin `defaultExprToSQL` (preserves the
      `ColumnRef` qualifier + fully parenthesizes OpExpr; `formatExprForAttrdef`
      drops the qualifier so it was unusable). The lexer lowercases the unquoted
      NEW/OLD qualifier so `NEW.b`→`new.b` matching PG's `get_rule_expr(varprefix)`;
      `prettyFlags=0` → `WHEN ((new.b <> old.b))`. Tests `TestParseCreateTriggerWhen`
      + `TestBuildTriggerDefString` (2 new cases) + slice-329
      `TestPort_PgDumpConnectionSetup` (`trg_when` NEW-vs-OLD, `trg_whna`
      NEW-vs-constant; byte-identical vs real pg_dump 18.3) PASS. Design
      `0119-0004-trigger-when-condition.md`. **`pg_get_triggerdef` getter battery now
      complete.** Still open under M0119-0004: runtime WHEN evaluation; GRANT/ACL
      (`relacl`) + named-role policies; extended-protocol commit-time deferral.
      **2026-06-30 (loop #53, design 0119-0004-named-role-policy-pgdump, DU-002
      slice 330):** named-role `CREATE POLICY ... TO <role>` round-trips. Added a
      per-role OID registry (`InMemory.roles` map[string]struct{}→map[string]uint32;
      `RegisterRole` mints a catalog-counter OID idempotently; new
      `Catalog.RoleOID` resolves a role, postgres→10); `pg_roles` `VirtualRows`
      now exposes every registered role with its OID; `execCreatePolicy` resolves
      each TO role → OID (42704 unknown, PUBLIC/empty→{0}) into `pg_policy.polroles`.
      Fixed a latent bug: the `quote_ident` builtin unconditionally double-quoted,
      so pg_dump's getPolicies resolver emitted ` TO "pol_role"` not ` TO pol_role`;
      now delegates to the conditional-quoting `pgQuoteIdent`. The existing pg_policy
      projection + goopg's `ARRAY(SELECT … = ANY(arr))`/`array_to_string`/`quote_ident`
      stack already worked (PUBLIC fixtures exercised the `CASE … = '{0}'`
      short-circuit). Tests `TestRoleOIDRegistry` + slice-330
      `TestPort_PgDumpConnectionSetup` (`p_role FOR SELECT TO pol_role`,
      byte-identical vs real pg_dump 18.3) PASS. **GRANT/ACL relacl now unblocked**
      (registry available).
      **Still open under M0119-0004:** the pg_dump 002–010 catalog-view parity
      battery (further slices: GRANT/ACL relacl, richer CREATE RULE,
      reserved-keyword-named-role quoting); extended-protocol commit-time deferral
      (architecturally entangled — extended protocol is auto-commit-per-statement).
      **2026-06-30 (loop #54, design 0119-0004-grant-relacl-pgdump, DU-002
      slice 331):** table-level `GRANT … ON TABLE … TO <role>` round-trips. goopg
      already recorded table grants (`Catalog.GrantTablePrivilege`,
      `server/grant_ddl.go`) but always projected `pg_class.relacl` as NULL, so the
      privilege was silently lost on dump/restore. New `InMemory.relaclTextLocked`
      renders the GRANT store as the materialized `aclitem[]` text (owner full
      `postgres=arwdDxtm/postgres` first, then each grantee with canonical
      `ACL_ALL_RIGHTS_STR` privilege letters), wired into the `pg_class`
      regular-table `VirtualRows` cell. pg_dump's `getTables` reads `c.relacl`
      directly + `acldefault('r', owner)` (already implemented, slice 2) and
      `buildACLCommands` emits the GRANT diff client-side — no new builtin. NULL
      relacl when nothing granted → zero blast radius. Tests `TestRelaclText` +
      slice-331 `TestPort_PgDumpConnectionSetup` (`GRANT SELECT ON TABLE
      public.grant_t TO grantee_role;` byte-identical vs real pg_dump 18.3) PASS.
      Still open: column-level/sequence/schema GRANT, `WITH GRANT OPTION`, REVOKE.
      **2026-06-30 (loop #55, design 0119-0004-grant-option-relacl-pgdump, DU-002
      slice 332):** `GRANT … WITH GRANT OPTION` round-trips. PG records the option
      as a `*` suffix on the privilege letter (`aclitemout` -> `grantee2_role=r*/
      postgres`); pg_dump's `buildACLCommands` splits each grantee's privileges
      into `privs`/`privswgo` and emits the latter as a dedicated `GRANT … WITH
      GRANT OPTION;`. Slice 331 dropped the option (grant_ddl.go stripped the
      clause; ACL store had no slot). Fix: ACL store inner value `map[priv]struct{}`
      -> `map[priv]bool` (bool = grant option; drop-in for the set-membership reads
      so `truncate-conflict` enforcement is unaffected); new
      `GrantTablePrivilegeWithGrantOption` (existing 3-arg method delegates with
      false), OR-ing the flag so a later plain GRANT keeps an existing option;
      `relaclTextLocked` appends `*`; `tryRecordTableGrant` passes
      `withGrantOption = (WITH-tail == "grant option")`. Plain GRANT byte-identical
      to slice 331 -> zero blast radius. Tests `TestRelaclTextGrantOption` +
      slice-332 `TestPort_PgDumpConnectionSetup` (`GRANT SELECT ON TABLE
      public.grant_g TO grantee2_role WITH GRANT OPTION;` byte-identical vs real
      pg_dump 18.3) PASS. Still open: column-level/sequence/schema GRANT, REVOKE.
      **2026-06-30 (loop #56, design 0119-0004-sequence-grant-relacl-pgdump, DU-002
      slice 333):** `GRANT … ON SEQUENCE` round-trips. pg_dump treats sequences as
      relations — `getTables` reads `c.relacl` for relkind 'S' and diffs against
      `acldefault('s', owner)` = `{postgres=rwU/postgres}`, `dumpTableSchema` passes
      objtype `"SEQUENCE"` to `dumpACL`. goopg lost it two ways: `grant_ddl.go`
      bailed on `ON SEQUENCE` (no-op), and `relaclTextLocked` was hard-wired to the
      table privilege order (would drop `USAGE`, wrong owner baseline). Fix:
      catalog adds `sequenceACLPrivOrder` (`r`/`w`/`U`) + `ownerSequenceACLString`
      "rwU", refactors `relaclTextLocked` into a core `relaclTextLockedFor` with a
      `relaclTextLockedSeq` sibling, builder calls it when `t.IsSequence`; server
      removes `sequence` from `nonTableGrantObjects`, strips a leading `SEQUENCE`
      keyword, expands `ALL`→`allSequencePrivileges`, `parseGrantPrivileges` takes
      the set as a param. Shared OID-keyed store → `truncate-conflict` untouched,
      grant-option `*` shared. Tests `TestRelaclTextSequence` + slice-333
      `TestPort_PgDumpConnectionSetup` (`GRANT USAGE ON SEQUENCE public.grant_seq
      TO seq_role;` byte-identical vs real pg_dump 18.3) PASS. Still open:
      column-level (`pg_attribute.attacl`)/schema/database GRANT, REVOKE-of-default.
      **2026-06-30 (loop #57, design 0119-0004-grant-public-relacl-pgdump, DU-002
      slice 334):** `GRANT … TO PUBLIC` round-trips. PG stores a grant to the
      PUBLIC pseudo-role with an EMPTY grantee in `relacl` (`=r/postgres`), and
      pg_dump's `buildACLCommands` renders an empty grantee (`grantee->len == 0`)
      as the keyword `PUBLIC` → `GRANT SELECT ON TABLE public.grant_pub TO
      PUBLIC;`. Slices 331–333 rendered every grantee under its stored name, so a
      grant to PUBLIC (recorded under the lower-cased reserved name `public`)
      would have materialized `public=r/postgres` → pg_dump would emit `TO public`
      (a nonexistent named role). Fix (rendering-only): catalog adds
      `publicPseudoRole = "public"` and `relaclTextLockedFor` maps that role key to
      the empty grantee `""`. No `grant_ddl.go` change (`TO PUBLIC` already records
      under `public`). PG reserves PUBLIC so no real role is named `public`;
      `HasTablePrivilege`/`truncate-conflict` read the stored key unchanged → zero
      blast radius; grant-option/sequence grants to PUBLIC round-trip via the
      shared path. Tests `TestRelaclTextPublic` + slice-334
      `TestPort_PgDumpConnectionSetup` (byte-identical vs real pg_dump 18.3) PASS.
      Still open: column-level (`pg_attribute.attacl`)/schema (`nspacl`)/database
      (`datacl`) GRANT, REVOKE-of-default modelling.
      **2026-06-30 (loop #58, design 0119-0004-schema-grant-nspacl-pgdump, DU-002
      slice 335):** `GRANT … ON SCHEMA` round-trips. pg_dump's getNamespaces reads
      `pg_namespace.nspacl`, diffs it against `acldefault('n', 10)` =
      `{postgres=UC/postgres}`, and dumpACL (objtype SCHEMA) emits a single
      `GRANT USAGE ON SCHEMA grant_sch TO schema_role;`. goopg lost the grant two
      ways: grant_ddl bailed on `schema` (no-op), and the pg_namespace builder
      hard-coded `nspacl` to NULL. Fix (dump-fidelity only): schema OIDs come from
      the same `nextOID` counter as relations (no collision) so schemas reuse the
      OID-keyed ACL store + object-agnostic `relaclTextLockedFor`; catalog adds
      `schemaACLPrivOrder` (USAGE 'U' < CREATE 'C') + `ownerSchemaACLString` "UC"
      + `NamespaceACLText(oid)`, and pg_namespace projects it. server adds
      `allSchemaPrivileges` {USAGE,CREATE} + `recordSchemaGrant` (resolves
      `SchemaOID`). HasTablePrivilege/truncate-conflict untouched; system schemas
      stay NULL; grant-option + TO PUBLIC ride the shared core. Tests
      `TestNamespaceACLText` + slice-335 `TestPort_PgDumpConnectionSetup`
      (byte-identical vs real pg_dump 18.3) PASS. Still open: column-level
      (`pg_attribute.attacl`)/database (`datacl`) GRANT, REVOKE-of-default modelling.
      **2026-06-30 (loop #59, design 0119-0004-grant-quoted-role-relacl-pgdump,
      DU-002 slice 336):** `GRANT` to a quoting-required role name round-trips. PG's
      `aclitemout`/`putid` double-quotes a grantee whose name has any char outside
      `[A-Za-z0-9_]` (hyphen/space/multibyte; internal `"` doubled) in `relacl`
      (`"weird-role"=r/postgres`); pg_dump's `getid` relies on those quotes and
      re-emits via `fmtId`. Slices 331–335 rendered grantees raw, so a hyphenated
      role emitted `weird-role=r/postgres` → pg_dump mis-parsed at the hyphen. Fix
      (rendering-only, catalog.go): new `aclQuoteName` reproduces `putid`;
      `relaclTextLockedFor` wraps the grantee (after the PUBLIC→"" mapping). No
      `grant_ddl.go` change (`splitGrantList` already trims quotes). A
      reserved-keyword name (`user`) is all-alnum → bare in the aclitem, quoted
      client-side by pg_dump's `fmtId` → already round-trips (closes the loop-#53
      "reserved-keyword-named-role quoting" item by analysis). Zero blast radius
      (`aclQuoteName` = identity for alnum/underscore + empty PUBLIC grantee;
      `HasTablePrivilege`/`truncate-conflict` read the stored key). Tests
      `TestRelaclTextQuotedGrantee` + `TestACLQuoteName` + slice-336
      `TestPort_PgDumpConnectionSetup` (`CREATE ROLE "weird-role"` + `GRANT SELECT
      ON TABLE public.grant_q TO "weird-role"`; byte-identical vs real pg_dump
      18.3) PASS. **Deferred:** mixed-case quoted-role case preservation
      (`GrantTablePrivilege*` lower-cases the stored name). Still open: column-level
      (`pg_attribute.attacl`, heap re-sync)/database (`datacl`, needs `--create`)
      GRANT, REVOKE-of-default modelling.
      **2026-06-30 (loop #60, design 0119-0004-grant-mixed-case-role-relacl-pgdump,
      DU-002 slice 337):** `GRANT` to a case-significant (mixed-case quoted) role
      round-trips. PG role names are case-significant when double-quoted;
      `aclitemout` renders the role's TRUE name in `relacl` (`MixedCase=r/postgres`,
      bare because all-alnum), and pg_dump re-quotes a mixed-case identifier via
      `fmtId` → `TO "MixedCase"`. goopg's ACL store keys privileges by the
      lower-cased name (case-insensitive `HasTablePrivilege`/`truncate-conflict`
      lookups), so `relaclTextLockedFor` rendered `mixedcase` → pg_dump emitted
      `TO mixedcase` (a different, nonexistent role) — the slice-336 deferred
      limitation. Fix (rendering-only, catalog.go): new `roleACLDisplay`
      map[lowerRole]→originalSpelling, populated in
      `GrantTablePrivilegeWithGrantOption` (only when the spelling differs from its
      lower-case — no all-lowercase fixture records one); `relaclTextLockedFor`
      resolves the lower-cased key through it AFTER the PUBLIC→"" mapping and
      BEFORE `aclQuoteName`, so mixed-case + special-char composes
      (`"Weird-Role"=r/postgres`). No grant_ddl.go change (`handleQuery` passes the
      raw original-case `matchable`; `splitGrantList` trims quotes → store gets
      `MixedCase`). Zero blast radius (display map consulted only in rendering;
      lookups read the lower-cased key, all-case-variants resolve). `CREATE ROLE`
      case-folding is irrelevant (pg_dump doesn't emit CREATE ROLE; relacl carries
      the grantee as text, no OID→pg_authid resolution). Tests
      `TestRelaclTextMixedCaseGrantee` + slice-337 `TestPort_PgDumpConnectionSetup`
      (`CREATE ROLE "MixedCase"` + `GRANT SELECT ON TABLE public.grant_mc TO
      "MixedCase"`; byte-identical vs real pg_dump 18.3) PASS. Still open:
      column-level (`pg_attribute.attacl`, heap re-sync)/database (`datacl`, needs
      `--create`) GRANT, REVOKE-of-default modelling.
      **2026-06-30 (loop #62, design 0119-0004-revoke-relacl-pgdump, DU-002 slice
      338):** `GRANT … then partial REVOKE` round-trips. A `GRANT SELECT, INSERT …
      TO revoke_role` then `REVOKE INSERT … FROM revoke_role` leaves
      `pg_class.relacl` as `revoke_role=r/postgres` (lone SELECT), and pg_dump's
      `buildACLCommands` diffs vs `acldefault` → re-emits only `GRANT SELECT ON
      TABLE public.revoke_t TO revoke_role;`. goopg treated REVOKE as a pure no-op,
      so the GRANT recorder's `ar` survived and the dump over-emitted the revoked
      INSERT (silent ACL drift on restore). Fix: catalog new
      `RevokeTablePrivilege(relOID, role, priv)` (removes the bit; drops the
      grantee entry when its set empties, the whole relOID entry when no grantees
      remain → relacl back to NULL; no-op for a never-held priv; retains the
      slice-337 `roleACLDisplay` case override). server new `tryRecordTableRevoke`
      mirrors `tryRecordTableGrant` (parses `REVOKE <privs> ON [TABLE|SEQUENCE]
      <objs> FROM <roles> [CASCADE|RESTRICT]`, shares
      `parseGrantPrivileges`/`splitGrantList`/`nonTableGrantObjects`; bails on
      column-level/`GRANT OPTION FOR`/non-table classes). dispatch intercepts a
      single-statement autocommit REVOKE symmetric with GRANT (record →
      CommandComplete("REVOKE")); explicit-txn REVOKE still falls through to the
      executor no-op. Zero blast radius (only ever removes bits → enforcement can
      only become more correct; GRANT-with-no-REVOKE renders byte-identically to
      331–337). Tests `TestRevokeTablePrivilege` + slice-338
      `TestPort_PgDumpConnectionSetup` (emits the surviving SELECT, NOT the revoked
      INSERT; byte-identical vs real pg_dump 18.3) PASS. Still open: column-level
      (`pg_attribute.attacl`, heap re-sync)/database (`datacl`, needs `--create`)
      GRANT, REVOKE-of-default (owner-side implicit-privilege) modelling.
      **2026-06-30 (loop #64, design 0119-0004-schema-revoke-nspacl-pgdump, DU-002
      slice 339):** `GRANT … ON SCHEMA` then partial `REVOKE` round-trips (the
      `nspacl` analogue of slice 338). `GRANT USAGE, CREATE ON SCHEMA revoke_sch
      TO revoke_sch_role` then `REVOKE CREATE … FROM …` leaves
      `pg_namespace.nspacl` as `revoke_sch_role=U/postgres` (lone USAGE) → pg_dump
      re-emits only `GRANT USAGE ON SCHEMA revoke_sch TO revoke_sch_role;`, NOT the
      revoked CREATE. goopg's REVOKE recorder `tryRecordTableRevoke` modelled only
      table/sequence `relacl` (schema is in `nonTableGrantObjects` → non-table
      bail), so the slice-335 GRANT's CREATE survived in nspacl and the dump
      over-emitted `GRANT CREATE, USAGE` (silent ACL drift on restore). Fix
      (server-only): `tryRecordTableRevoke` gains an `ON SCHEMA` branch (mirror of
      the grant recorder's slice-335 branch) dispatching to new `recordSchemaRevoke`
      — the mirror of `recordSchemaGrant`: expands against `allSchemaPrivileges`
      ({USAGE,CREATE}), resolves each schema via `Catalog.SchemaOID`, calls
      `Catalog.RevokeTablePrivilege(oid, role, priv)` (slice 338, already correct
      for schema OIDs — schemas share the OID-keyed `tableACLs` store, and the
      revoke drops the grantee entry when its set empties / the whole entry when no
      grantees remain → nspacl NULL). NO catalog change. Zero blast radius (only
      removes bits already present; schema GRANT with no REVOKE renders identically
      to slice 335; explicit-txn REVOKE still no-op). Tests
      `TestRevokeTablePrivilege`/`Relacl` (reused) + slice-339
      `TestPort_PgDumpConnectionSetup` (emits surviving USAGE, NOT revoked CREATE;
      byte-identical vs real pg_dump 18.3) PASS. Still open: column-level
      (`pg_attribute.attacl`)/database (`datacl`, needs `--create`) GRANT,
      REVOKE-of-default modelling.

      **2026-06-30 (loop #65, design 0119-0004-owner-revoke-relacl-pgdump, DU-002
      slice 340):** owner-side `REVOKE`-of-default round-trips. PostgreSQL leaves
      `pg_class.relacl` NULL while the owner holds its implicit default
      privileges; the first owner-side REVOKE materializes relacl as the owner
      default minus the revoked bits. `REVOKE TRIGGER ON TABLE public.ownrev_t
      FROM postgres` → `{postgres=arwdDxm/postgres}`, and pg_dump's
      `buildACLCommands` diffs that against `acldefault('r', relowner)` and
      re-emits `REVOKE ALL … FROM postgres;` + `GRANT SELECT,INSERT,REFERENCES,
      DELETE,TRUNCATE,MAINTAIN,UPDATE … TO postgres;` (TRIGGER omitted). goopg's
      REVOKE recorder modelled only non-owner grantees (the owner is implicit),
      so `REVOKE … FROM postgres` found no entry and left relacl NULL → pg_dump
      dropped the change silently. Fix: catalog new `MaterializeOwnerACL(relOID,
      owner, ownerPrivs)` (records an explicit owner aclitem = full default,
      once, never clobbering a prior revoke) + renderer `relaclTextLockedFor`
      special-cases the owner key (renders the owner's actual remaining privs via
      the extracted `renderACLLetters` helper, owner still first); server
      `tryRecordTableRevoke` calls it before `RevokeTablePrivilege` when the
      grantee is the owner. Scope: single-priv owner revoke only (REVOKE ALL from
      owner → NULL in goopg vs PG's empty `{}` array — follow-up); schema/sequence
      owner-revoke unwired (primitive is type-agnostic). Tests
      `TestMaterializeOwnerACL` (new) + reused `Relacl`/`Revoke`/`NamespaceACLText`
      + slice-340 `TestPort_PgDumpConnectionSetup` (both REVOKE/GRANT lines exact,
      no TRIGGER re-grant; byte-identical vs real pg_dump 18.3) PASS. Still open:
      column-level (`pg_attribute.attacl`)/database (`datacl`, `--create`) GRANT,
      owner revoke-ALL empty-array modelling.

      **2026-06-30 (loop #66, design 0119-0004-owner-revoke-all-empty-relacl-pgdump,
      DU-002 slice 341):** the deferred full-revoke case from slice 340. `REVOKE
      ALL ON TABLE public.ownrevall_t FROM postgres` strips every owner default
      privilege, leaving `pg_class.relacl` = `{}` — a non-NULL but empty aclitem
      array, distinct from the NULL of a never-granted table. pg_dump diffs `{}`
      against `acldefault('r', 10)` and emits a bare `REVOKE ALL … FROM postgres;`
      with NO re-GRANT. goopg's `RevokeTablePrivilege` previously dropped the
      owner's emptied entry and reverted relacl to NULL → pg_dump emitted nothing,
      silently restoring the owner default on restore. Fix (catalog-only; server
      recording unchanged — `REVOKE ALL` just expands to every privilege through
      the slice-340 owner path): new field `relACLEmptied map[uint32]bool`;
      `RevokeTablePrivilege` sets it when the *last* aclitem removed is the owner's
      own entry; `relaclTextLockedFor` returns `"{}"` for empty `byRole` when the
      flag is set (else `""`/NULL); `GrantTablePrivilege`/`DropTableACL` clear it;
      `MaterializeOwnerACL` early-returns when set (no resurrection on a second
      owner revoke). Tests `TestRevokeAllFromOwnerEmptyArray` (new) + reused
      `Relacl`/`Revoke`/`MaterializeOwnerACL`/`NamespaceACLText` + slice-341
      `TestPort_PgDumpConnectionSetup` (exact `REVOKE ALL …`, no owner re-GRANT;
      byte-identical vs real pg_dump 18.3) PASS. Still open: column-level
      (`attacl`)/database (`datacl`) GRANT, owner-zero-coexisting-with-grantee
      (`{bob=r/postgres}`) modelling, schema/sequence owner REVOKE ALL.

      **2026-06-30 (loop #67, design 0119-0004-schema-owner-revoke-all-empty-nspacl-pgdump,
      DU-002 slice 342):** the schema analogue of slice 341. `REVOKE ALL ON SCHEMA
      ownrevall_sch FROM postgres` strips the owner's implicit default schema
      privileges (USAGE, CREATE), leaving `pg_namespace.nspacl` = `{}` (non-NULL
      empty array, distinct from a never-granted schema's NULL). pg_dump diffs `{}`
      against `acldefault('n', 10)` = `{postgres=UC/postgres}` and emits a bare
      `REVOKE ALL ON SCHEMA ownrevall_sch FROM postgres;` with NO re-GRANT. goopg's
      `recordSchemaRevoke` (slice 339) modelled only grantees, so an owner-side
      schema revoke found no entry → nspacl stayed NULL → pg_dump emitted nothing.
      Fix (server-only — catalog primitives are already type-agnostic):
      `recordSchemaRevoke` now mirrors the table path, calling
      `MaterializeOwnerACL(oid, "postgres", allSchemaPrivileges)` before the
      per-priv `RevokeTablePrivilege` when the role is the owner; `REVOKE ALL`
      empties the materialized owner entry and the catalog records `nspacl = {}`
      via the shared `relACLEmptied` path (rendered through `NamespaceACLText`).
      No catalog change. Tests `TestRevokeAllFromSchemaOwnerEmptyArray` (new) +
      reused `NamespaceACLText`/`RevokeAllFromOwnerEmptyArray`/`MaterializeOwnerACL`
      + slice-342 `TestPort_PgDumpConnectionSetup` (exact `REVOKE ALL ON SCHEMA …`,
      no owner re-GRANT; byte-identical vs real pg_dump 18.3) PASS. Still open:
      column-level (`attacl`)/database (`datacl`) GRANT,
      owner-zero-coexisting-with-grantee modelling, sequence owner REVOKE ALL.

      **2026-06-30 (loop #69, design 0119-0004-owner-zero-coexists-grantee-relacl-pgdump,
      DU-002 slice 344):** owner-zero coexisting with a grantee — the follow-up the
      empty-array slices (341/342/343) deferred. After `REVOKE ALL ON TABLE
      ownerzero_t FROM postgres` empties relacl to `{}`, a later `GRANT SELECT … TO
      bob` re-materializes the array but PG keeps the owner at zero (absent):
      `relacl = {bob=r/postgres}`, NOT `{postgres=arwdDxtm/postgres,bob=r/postgres}`.
      pg_dump diffs that against `acldefault('r', 10)` and emits BOTH `REVOKE ALL ON
      TABLE public.ownerzero_t FROM postgres;` AND `GRANT SELECT ON TABLE
      public.ownerzero_t TO bob;`. goopg previously cleared `relACLEmptied` on any
      GRANT and re-inserted the owner's full default whenever the owner key was
      absent from a non-empty array → rendered the owner holding its default →
      pg_dump dropped the REVOKE ALL, silently restoring owner privs on restore. Fix
      (catalog-only): `relACLEmptied` re-interpreted as "owner explicitly zero
      (absent)", subsuming the `{}` case. `GrantTablePrivilegeWithGrantOption` clears
      the flag only for an owner-side GRANT (`role == aclOwnerRole`);
      `relaclTextLockedFor` suppresses the leading owner entry when the flag is set,
      rendering only grantees. Object-type-agnostic (OID-keyed). Tests
      `TestRevokeAllFromOwnerThenGrantGrantee` (new) + slice-344
      `TestPort_PgDumpConnectionSetup` (both lines asserted; no re-grant to the
      zeroed owner; byte-identical vs real pg_dump 18.3) PASS; catalog+server suites
      PASS; build clean. Still open under M0119-0004: column-level (`attacl`,
      heap re-sync) / database (`datacl`, `--create`-only) GRANT projection;
      extended-protocol commit-time deferral.

      **2026-06-30 (loop #70, design 0119-0004-function-grant-proacl-pgdump, DU-002
      slice 345):** function-level GRANT round-trip — the routine analogue of the
      table/schema/sequence GRANT slices (331–344). goopg projected
      `pg_proc.proacl = NULL` for every routine, silently dropping function GRANTs
      from the dump. A function's `acldefault('f', 10)` =
      `{=X/postgres,postgres=X/postgres}` grants EXECUTE to BOTH owner and PUBLIC,
      so `GRANT EXECUTE ON FUNCTION public.grantfn(integer) TO func_grantee`
      materializes `proacl = {=X/postgres,postgres=X/postgres,func_grantee=X/postgres}`;
      pg_dump's getFuncs diffs against `acldefault('f', proowner)` and emits
      `GRANT ALL ON FUNCTION public.grantfn(integer) TO func_grantee;` (EXECUTE is the
      sole function priv → renders ALL). `pg_proc` is VIRTUAL (unlike heap-backed
      pg_attribute), so this is a pure projection — no heap re-sync. Three pieces:
      catalog `functionACLPrivOrder`(EXECUTE→X)+`ownerFunctionACLString="X"`+
      `ProcACLText(procOID)` reusing the object-type-agnostic `relaclTextLockedFor`
      core (routines share the OID-keyed `tableACLs` store; routine OIDs from a
      disjoint `FirstRoutineOID=1<<17` range); server `recordFunctionGrant`
      (`tryRecordTableGrant` function/procedure/routine branches) resolving the OID
      via `Routines().Lookup`+unique-by-name fallback, paren-aware `splitFunctionList`,
      seeding the implicit PUBLIC EXECUTE; `pg_proc_view.go` projects
      `cat.ProcACLText(r.OID)` for user routines. Scope: GRANT only — function REVOKE
      and WITH GRANT OPTION not modelled. Tests `TestProcACLText` (new) + slice-345
      `TestPort_PgDumpConnectionSetup` (exact GRANT ALL ON FUNCTION line; byte-identical
      vs real pg_dump 18.3) PASS; catalog+server+initdb suites PASS; build clean. Still
      open under M0119-0004: column-level (`attacl`, heap re-sync) / database (`datacl`,
      `--create`-only) GRANT projection; function/object REVOKE; extended-protocol
      commit-time deferral.
      **2026-06-30 (loop #71, design 0119-0004-function-revoke-proacl-pgdump, DU-002
      slice 346):** function-level REVOKE round-trip — the routine REVOKE analogue of the
      table REVOKE slices (338+); the follow-up slice 345 deferred. `REVOKE EXECUTE …
      FROM PUBLIC` is the most common real-world function ACL mutation, and goopg silently
      dropped it (the REVOKE recorder bailed on the `function` object class), re-granting
      PUBLIC's default EXECUTE on restore. A function's `acldefault('f', 10)` =
      `{=X/postgres,postgres=X/postgres}` grants EXECUTE to BOTH owner and PUBLIC;
      PostgreSQL leaves proacl NULL until the first GRANT/REVOKE. `REVOKE EXECUTE ON
      FUNCTION public.revokefn(integer) FROM PUBLIC` on a never-granted routine
      materializes `proacl = {postgres=X/postgres}` (owner only; PUBLIC's implicit EXECUTE
      removed); pg_dump's getFuncs diffs against `acldefault('f', proowner)` and emits
      `REVOKE ALL ON FUNCTION public.revokefn(integer) FROM PUBLIC;`. Fix (server-only —
      catalog primitives from slices 340/345 already in place): `tryRecordTableRevoke`
      gains function/procedure/routine branches → new `recordFunctionRevoke`, which
      resolves the OID via the shared `lookupFunctionOID`/`splitFunctionList`,
      MATERIALIZES the owner's implicit default EXECUTE first via the type-agnostic
      `MaterializeOwnerACL(oid, "postgres", ["EXECUTE"])` so the surviving owner EXECUTE
      renders explicitly, then `RevokeTablePrivilege(oid, role, "EXECUTE")` per role
      (PUBLIC lower-cases to the reserved `public` pseudo-role; a never-granted PUBLIC is
      an absent-entry no-op → owner-only array). `ProcACLText` (virtual pg_proc
      projection) renders `{postgres=X/postgres}`. Generalizes to a grantee revoke (proacl
      = acldefault as a set → pg_dump emits nothing) and to an owner-side revoke (empties
      to `{}` via the shared `relACLEmptied` path). Scope: pinned case is `REVOKE … FROM
      PUBLIC`; explicit-txn REVOKE still no-op; WITH GRANT OPTION/column/database open.
      Near-zero blast radius (only removes bits; function GRANT with no REVOKE renders
      identically to slice 345). Tests `TestProcACLRevokeFromPublic` (new) + slice-346
      `TestPort_PgDumpConnectionSetup` (exact REVOKE ALL ON FUNCTION … FROM PUBLIC line;
      byte-identical vs real pg_dump 18.3) PASS; catalog+server+initdb suites PASS; build
      clean. Still open under M0119-0004: column-level (`attacl`, heap re-sync) / database
      (`datacl`, `--create`-only) GRANT projection; extended-protocol commit-time deferral.

      **2026-06-30 (loop #75, design 0119-0004-sequence-revoke-relacl-pgdump, DU-002
      slice 350):** sequence partial REVOKE round-trip — the sequence analogue of the
      table partial-REVOKE slice 338 / schema partial-REVOKE slice 339, and the REVOKE
      counterpart of the sequence GRANT slices 333/349. A sequence exposes three
      privileges (USAGE/SELECT/UPDATE), so `GRANT USAGE, SELECT ON SEQUENCE
      public.seqrev_seq TO seqrev_role` then `REVOKE SELECT …` clears only the SELECT
      bit, leaving relacl = `{postgres=rwU/postgres,seqrev_role=U/postgres}`; pg_dump's
      getTables diffs against `acldefault('s', relowner)` = `{postgres=rwU/postgres}` and
      re-emits only `GRANT USAGE ON SEQUENCE public.seqrev_seq TO seqrev_role;` — NOT the
      revoked SELECT (verified byte-identical vs real pg_dump 18.3, relacl confirmed on
      live PG 18.3). Test-only — NO engine change: the shared REVOKE recorder
      `tryRecordTableRevoke` (slice 338) already clears the named bits from a sequence's
      relacl (sequences share the OID-keyed relation ACL store with tables) and the
      diff/render pipeline is object-type-agnostic for the `s` relkind. Adds fixture +
      assert (incl. the negative: no `GRANT SELECT, USAGE …` over-emit) to the cumulative
      `TestPort_PgDumpConnectionSetup` guard. Gates: connsetup slice 350 PASS; build
      clean; pgbench smoke = pre-commit. Still open under M0119-0004: column-level
      (`attacl`, heap re-sync) / database (`datacl`, `--create`-only) GRANT projection;
      extended-protocol commit-time deferral.
      **2026-06-30 (loop #77, design 0119-0004-multi-grantee-table-relacl-pgdump, DU-002
      slice 352):** multi-grantee table round-trip — two distinct grantees on one table
      each emit their own GRANT line. `GRANT SELECT … TO mg_role_a` then `GRANT INSERT …
      TO mg_role_b` materializes relacl as
      `{postgres=arwdDxtm/postgres,mg_role_a=r/postgres,mg_role_b=a/postgres}`; pg_dump's
      `buildACLCommands` fans out one `GRANT <privs> ON TABLE … TO <grantee>;` per
      non-owner aclitem, so the dump carries BOTH the SELECT line (mg_role_a) and the
      INSERT line (mg_role_b) — it does not merge grantees (verified byte-identical vs
      real pg_dump 18.3, relacl + ACL lines captured). Test-only — NO engine change: each
      GRANT records independently under the OID-keyed `tableACLs` store and
      `relaclTextLockedFor` renders grantees in `sort.Strings` order (mg_role_a before
      mg_role_b, matching PG's grant-order array here). The catalog multi-grantee
      deterministic-sort is already unit-covered by `TestRelaclText`'s two-grantee case;
      this slice adds the end-to-end pg_dump round-trip fixture + per-grantee GRANT-line
      asserts to the cumulative `TestPort_PgDumpConnectionSetup` guard. Gates: connsetup
      slice 352 PASS; catalog suite PASS; build clean; pgbench smoke = pre-commit. Still
      open under M0119-0004: column-level (`attacl`, heap re-sync) / database (`datacl`,
      `--create`-only) GRANT projection; extended-protocol commit-time deferral.

      **2026-06-30 (loop #78, design 0119-0004-same-priv-multi-grantee-table-relacl-pgdump,
      DU-002 slice 353):** same-privilege multi-grantee table round-trip — two grantees
      granted the SAME privilege on one table still emit two separate GRANT lines.
      `GRANT SELECT … TO sg_role_a` then `GRANT SELECT … TO sg_role_b` materializes relacl
      as `{postgres=arwdDxtm/postgres,sg_role_a=r/postgres,sg_role_b=r/postgres}`; pg_dump's
      `buildACLCommands` fans out one `GRANT SELECT ON TABLE … TO <grantee>;` per non-owner
      aclitem, so the dump carries BOTH SELECT lines — PostgreSQL never merges grantees into
      `GRANT … TO a, b;` even when their privilege sets are byte-identical (verified
      byte-identical vs real pg_dump 18.3 — relacl + ACL lines captured against PG 18.3 in
      `./postgres/local_install`). The same-priv case is the most tempting target for a
      (wrong) grantee-merge optimization, distinct from slice 352's differing-priv pair, so
      this slice adds an explicit negative assertion against the merged form
      (`TO sg_role_a, sg_role_b`). Test-only — NO engine change: each GRANT records
      independently under the OID-keyed `tableACLs` store (two grantees with the same priv
      letter occupy distinct map entries) and `relaclTextLockedFor` renders grantees in
      `sort.Strings` order (sg_role_a before sg_role_b, matching PG's grant-order array
      here). Adds the fixture + both-grantee asserts + merged-form negative assert to the
      cumulative `TestPort_PgDumpConnectionSetup` guard. Gates: connsetup slice 353 PASS;
      catalog suite PASS; build clean; pgbench smoke = pre-commit. Still open under
      M0119-0004: column-level (`attacl`, heap re-sync) / database (`datacl`, `--create`-only)
      / TYPE-DOMAIN (`pg_type.typacl`, currently unmodelled) GRANT projection;
      extended-protocol commit-time deferral.

      **2026-06-30 (loop #79, design 0119-0004-acl-grant-order-relacl, DU-002 slice 354):**
      ACL grantee GRANT-ORDER preservation — fixes a real divergence the prior multi-grantee
      slices masked. goopg rendered `relacl` grantees via `sort.Strings` (alphabetical), but
      PostgreSQL's `aclupdate` (acl.c) APPENDS a brand-new grantee's aclitem to the end, so
      the array preserves grant order. They coincide only for alphabetical grant sequences
      (every prior fixture). A reverse-order grant (`GRANT SELECT … TO og_role_z` then
      `… TO og_role_a`) exposed it: real PG 18.3 = `{postgres=arwdDxtm/postgres,
      og_role_z=r/postgres,og_role_a=r/postgres}` (z before a — verified vs
      `./postgres/local_install`), goopg emitted a-first → wrong pg_dump GRANT-line order.
      Engine change (internal/catalog): new `tableACLOrder map[uint32][]string` tracks
      per-relation first-grant order of non-owner grantees; grant appends on first
      appearance, revoke drops on full revoke, all `delete(tableACLs,oid)` teardown sites
      mirror it, and `relaclTextLockedFor` iterates that list (sorted-append backstop so no
      grant is ever silently dropped) instead of sorting. One store + one render core ⇒
      covers relacl/proacl/nspacl uniformly. Four `relacl_test.go` units corrected (they had
      encoded the old alphabetical order; real-PG verification proved grant-order correct).
      New DU-002 slice 354 asserts z-before-a GRANT lines (byte-identical vs real pg_dump
      18.3). Blast radius nil for alphabetical sequences (byte-unchanged). Gates: catalog +
      executor + initdb suites PASS; connsetup slice 354 PASS; build clean; pgbench smoke =
      pre-commit.
      **2026-06-30 (loop #80, design 0119-0004-regrant-after-revoke-order-relacl-pgdump,
      DU-002 slice 355):** REVOKE-then-re-GRANT grant-order — end-to-end coverage for the
      grant-order teardown + re-append path landed by slice 354. PostgreSQL's `aclupdate`
      (acl.c) does NOT preserve a revoked grantee's slot: a full REVOKE deletes its aclitem
      and a later GRANT to the same grantee APPENDS a fresh aclitem at the END, so the
      re-granted grantee renders AFTER continuously-held grantees even though granted first
      and sorting first. `GRANT SELECT TO rg_a; GRANT SELECT TO rg_b; REVOKE SELECT FROM
      rg_a; GRANT INSERT TO rg_a` → relacl `{postgres=arwdDxtm/postgres,rg_b=r/postgres,
      rg_a=a/postgres}` (b before a — verified vs real PG 18.3 in `./postgres/local_install`),
      pg_dump emits the rg_b SELECT line before the rg_a INSERT line. Slice 354 covered only
      fresh reverse-order grants; this exercises `catalog.dropTableACLOrderRole` (full-revoke
      teardown) then a re-append. Test-only — NO engine change: `RevokeTablePrivilege`
      already drops the grantee from `tableACLOrder` on full revoke and
      `GrantTablePrivilegeWithGrantOption` re-appends it (fresh per-role map). Adds
      `internal/catalog/relacl_test.go` → `TestRelaclTextRegrantAfterRevokeMovesToEnd` (unit)
      + DU-002 slice 355 fixture (`regrant_t`/`rg_role_a`/`rg_role_b`) + `strings.Index`
      b-before-a ordering assert in `TestPort_PgDumpConnectionSetup`, byte-identical vs real
      pg_dump 18.3. Zero blast radius. Gates: `go test ./internal/catalog/ -run TestRelacl`
      PASS; connsetup slice 355 PASS; build clean; pgbench smoke = pre-commit. Still open
      under M0119-0004: column-level (`attacl`, heap re-sync) / database (`datacl`,
      `--create`-only) / TYPE-DOMAIN (`typacl`, unmodelled) GRANT projection;
      extended-protocol commit-time deferral.
      **2026-06-30 (loop #82, design 0119-0004-partial-revoke-keeps-slot-relacl, DU-002
      slice 356):** Partial-REVOKE-keeps-slot — the complement of slice 355. PostgreSQL's
      `aclupdate` (acl.c) distinguishes a FULL revoke (privilege count hits zero → aclitem
      DELETED, later GRANT re-appends at end) from a PARTIAL revoke (bits removed but the
      entry survives → modified IN PLACE, array index unchanged). A grantee that keeps ≥1
      privilege after REVOKE stays in its original grant-order slot. Verified vs real PG 18.3
      (`./postgres/local_install`): `GRANT SELECT,INSERT TO pr_a; GRANT SELECT TO pr_b;
      REVOKE INSERT FROM pr_a` → `{postgres=arwdDxtm/postgres,pr_a=r/postgres,pr_b=r/postgres}`
      (pr_a stays AHEAD of pr_b). goopg already mirrors this: `RevokeTablePrivilege` calls
      `dropTableACLOrderRole` ONLY when the grantee's privilege set empties, so a partial
      revoke leaves `tableACLOrder` untouched. Test-only — NO engine change. Adds
      `internal/catalog/relacl_test.go` → `TestRelaclTextPartialRevokeKeepsSlot`:
      partial-revoke-keeps-slot assert + contrast guard (full revoke + re-grant → pr_a appends
      after pr_b), pinning partial-vs-full paths together. End-to-end pg_dump ordering already
      covered by the slice 354/355 connsetup fixtures. Zero blast radius. Gates:
      `go test ./internal/catalog/ -run TestRelacl` PASS; build clean; pgbench smoke =
      pre-commit. Still open under M0119-0004: column-level (`attacl`, heap re-sync) /
      database (`datacl`, `--create`-only) / TYPE-DOMAIN (`typacl`, unmodelled) GRANT
      projection; extended-protocol commit-time deferral.
      **2026-06-30 (loop #83, design 0119-0004-acl-grant-heap-vs-virtual-typacl,
      ARCHITECTURAL FINDING — no code change):** the DU-002 ACL **GRANT round-trip** thread
      (slices 330–356) is **complete for every object class goopg serves virtually**
      (table/sequence `relacl`, schema `nspacl`, function `proacl`). The three still-open
      cases (`typacl`, `attacl`, `datacl`) share ONE root cause, now documented: GRANT is
      recorded **server-side** (`internal/server/query.go:69-87`) with only the in-memory
      ACL store in scope — **no executor `*Context`, no heap write**. That works for the
      virtual catalogs (`pg_class`/`pg_namespace` virtual builders + `pg_proc_view.go:388`
      project the ACL live; `execCreateFunction` writes NO `pg_proc` heap row) but NOT for
      `pg_type`, the **only** user catalog written to **real heap rows**
      (`writeHeapRowCanonical` at CREATE TYPE/DOMAIN bakes `typacl=NULL`) for M0097-0022
      PG-standby basebackup compat — there is no virtual `pg_type` overlay, so `getTypes`
      reads the baked NULL and `GRANT USAGE ON TYPE` is a silent no-op (`grant_ddl.go:137`
      bails). `attacl` (pg_attribute heap) shares it; `datacl` (pg_database heap) shares it
      AND is `--create`-only (untestable under the `--no-create` connsetup harness).
      Closing these is a NEW capability **M0119-0004-ACLHEAP** (below), not a slice: route
      GRANT on a heap-backed object through the executor (Context in scope) + re-sync the
      `pg_type` heap `typacl` (mirror `deleteTypeFromCatalogHeap` + re-insert; new
      `TypeACLText` = `relaclTextLockedFor` over `{USAGE}`/`acldefault('T',owner)`). High
      blast radius (heap mutation + MVCC + PG-standby read path) → needs TPC-H + recovery
      E2E + full regress, a dedicated loop. Gates this loop: design doc + README + ledger;
      `make ralph-state-guard` OK; pgbench smoke = pre-commit. The remaining M0119-0004 item
      is now **M0119-0004-ACLHEAP** plus extended-protocol commit-time deferral; the
      virtual-path ACL slice run is closed.
      **2026-06-30 (loop #90, design 0119-0004-conditional-rule-pgdump, DU-002 slice 359):
      conditional CREATE RULE round-trip.** The `WHERE (qual) DO INSTEAD NOTHING` form (the
      follow-up slice 324 deferred) now round-trips: parser `CreateRuleStmt.Qual Expr` parses
      the WHERE via `p.parseExpr()` and widens the first-class return to admit a captured qual;
      catalog `RuleInfo.Qual string` holds the deparsed text; `execCreateRule` deparses via
      `defaultExprToSQL` (single-paren `(old.a <> new.a)`, no extra layer — same convention as
      pg_get_indexdef's WHERE); `buildRuleDefString` emits the WHERE on its own 3-space-indented
      line with DO INSTEAD NOTHING trailing. Byte-identical vs real pg_dump 18.3 (`/tmp/du359_ref`).
      Tests `TestParseCreateRuleConditional` + `TestDDLCreateRuleConditionalRoundTrip` + slice-359
      `TestPort_PgDumpConnectionSetup` PASS; parser/catalog/executor suites PASS. **Still open
      under M0119-0004:** action-command / `DO ALSO <stmt>` rules (full query reverse-compiler);
      reserved-keyword-named-role quoting; extended-protocol commit-time deferral.
      **2026-06-30 (loop #92, design 0110-0001 slice 361): `USING hash` index dump.**
      A `CREATE INDEX … USING hash (col)` now dumps `USING hash`, not the B-tree
      substrate's `USING btree`. goopg has no native hash AM — a hash index routes
      through `createBTreeIndex` (catalog `Index.Method` stays `"btree"`; only
      `DeclaredHash` records the declared method, design 0118-0099). Fix =
      `catalog.BuildIndexDef` renders `hash` when `idx.DeclaredHash` is set, mirroring
      `pg_get_indexdef_worker`'s `USING %s` from `pg_am.amname`. Byte-verified vs a
      throwaway PG 18.3 cluster. Tests `TestBuildIndexDefDeclaredHash` (catalog) +
      slice-361 `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):** restart
      persistence — `DeclaredHash` is in-memory only, so a re-loaded hash index after
      a server restart would dump `USING btree` (same shared-catalog runtime-write gap
      as the other restart-durability slices).
      **2026-06-30 (loop #93, design 0110-0001 slice 363): compound/function-call
      DOMAIN CHECK dump (resolves slice-362 deferred-(a)).** A generic (non-IN) domain
      `CHECK (VALUE > 0 AND VALUE < 100)` now dumps `CHECK (((VALUE > 0) AND (VALUE <
      100)))` and `CHECK (length(VALUE) > 0)` dumps `CHECK ((length(VALUE) > 0))`,
      instead of the legacy token-text wrap `CHECK ((<raw>))`. New
      `renderDomainCheckPredicate` (the domain twin of `renderCheckPredicate`) re-parses
      the stored raw text and deparses via the fully-parenthesizing `defaultExprToSQL`,
      with the same re-parse round-trip fallback guard; `upcaseDomainValuePlaceholder`
      rewrites every bare `value` ColumnRef back to the uppercase `VALUE` keyword (the
      lexer case-folds it on re-parse, but PG deparses the CoerceToDomainValue placeholder
      uppercase). The dump site routes ONLY generic CHECKs through it; a `VALUE IN (...)`
      form (`len(d.CheckInValues)>0`) keeps the legacy raw wrap (pre-synthesized byte-exact
      ScalarArrayOp deparse). Single-comparison slice-96 domains byte-unchanged. Byte-
      identical vs real pg_dump 18.3. Tests `TestRenderDomainCheckPredicate` +
      slice-363 `dchkand`/`dchkfn` `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):**
      a negative literal in a domain CHECK (`VALUE < -5` → PG `'-5'::integer`) still
      byte-diverges (type-blind `defaultExprToSQL`, same gap as slice-360(a)/362(b)).
      **2026-06-30 (loop #96, design 0110-0001 slice 365): view `WITH [CASCADED|LOCAL]
      CHECK OPTION` round-trip.** A view created `WITH CHECK OPTION` now dumps the
      `\n  WITH <MODE> CHECK OPTION;` suffix after its body. PG stores the clause as the
      `check_option=<mode>` pg_class.reloption (view.c); pg_dump's getTables strips it
      from the reloptions array (array_remove, slice 5) and derives CASCADED/LOCAL via a
      `= ANY(reloptions)` CASE column, then dumpTableSchema appends the suffix
      (pg_dump.c:16982). Parser captures the mode into `CreateViewStmt.CheckOption` (bare
      clause → cascaded); `execCreateView` stores `catalog.Table.CheckOption`; the
      pg_class virtual reloptions builder appends `check_option=<mode>`. **No pg_dump-query
      change** — reuses the existing array_remove + ANY machinery. Byte-identical vs real
      pg_dump 18.3. Tests `TestParseCreateViewCheckOption` (parser) +
      `TestViewCheckOptionSurfacesInPgClassReloptions` (executor) + slice-365
      `vchk`/`vchk_local` `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):** the
      CHECK OPTION is NOT enforced on INSERT/UPDATE through the view (catalog/dump fidelity
      only); the `WITH (check_option=...)` reloption form before AS + `security_barrier`/
      `security_invoker` stay parsed-and-ignored; restart persistence (in-memory only).

      **2026-06-30 (loop #97, design 0110-0001 slice 366): view `WITH (security_barrier=<bool>)`
      round-trip.** A view created `WITH (security_barrier=true)` now dumps the
      `WITH (security_barrier='true')` clause after the view name. PG stores it as the
      `security_barrier=<bool>` pg_class.reloption; unlike check_option, pg_dump's getTables
      KEEPS it in the reloptions array (array_remove strips only check_option=*) and
      dumpTableSchema re-emits it via appendReloptionsArray (value single-quoted because
      `fmtId('true')!='true'`). Parser captures `security_barrier` into
      `CreateViewStmt.SecurityBarrier` (`*bool`; bare option → true; values normalize via
      `parseBoolReloptionValue` mirroring parse_bool); `execCreateView` sets
      `catalog.Table.SecurityBarrier`/`SecurityBarrierSet`; the pg_class virtual reloptions
      builder appends `security_barrier=<bool>` before the check_option element. **No
      pg_dump-query change.** Byte-identical vs real pg_dump 18.3. Tests
      `TestParseCreateViewSecurityBarrier` (parser) +
      `TestViewSecurityBarrierSurfacesInPgClassReloptions` (executor) + slice-366 `vsecbar`
      `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):** security_barrier has NO runtime
      effect (planner qual-fencing not implemented); `security_invoker` + the
      `WITH (check_option=...)` reloption form stay parsed-and-ignored; restart persistence
      (in-memory only).

      **2026-06-30 (loop #98, design 0110-0001 slice 367): view `WITH (security_invoker=<bool>)`
      round-trip.** The sibling of slice 366. A view created `WITH (security_invoker=true)` now
      dumps the `WITH (security_invoker='true')` clause after the view name. PG stores it as the
      `security_invoker=<bool>` pg_class.reloption; like security_barrier, pg_dump's getTables
      KEEPS it in the reloptions array and dumpTableSchema re-emits it via appendReloptionsArray.
      Parser captures `security_invoker` into `CreateViewStmt.SecurityInvoker` (`*bool`; bare
      option → true; values normalize via `parseBoolReloptionValue`); `execCreateView` sets
      `catalog.Table.SecurityInvoker`/`SecurityInvokerSet`; the pg_class virtual reloptions builder
      appends `security_invoker=<bool>` after security_barrier and before the check_option element.
      **No pg_dump-query change.** Byte-identical vs real pg_dump 18.3. Tests
      `TestParseCreateViewSecurityInvoker` (parser) +
      `TestViewSecurityInvokerSurfacesInPgClassReloptions` (executor) + slice-367 `vsecinv`
      `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):** security_invoker has NO runtime
      effect (permission-model: invoking-vs-owner ACL not implemented); the `WITH (check_option=...)`
      reloption form stays parsed-and-ignored; restart persistence (in-memory only).

      **2026-06-30 (loop #99, design 0110-0001 slice 368): trigger `EXECUTE FUNCTION f('a','b')`
      string arguments (TG_ARGV) round-trip.** A `CREATE TRIGGER … EXECUTE FUNCTION fn('arg1','arg2')`
      now has an oracle-verified fixture. PG stores the args in `pg_trigger.tgargs` and
      `pg_get_triggerdef_worker` re-renders them comma-separated, each single-quoted via
      `simple_quote_literal`. goopg's whole path already existed (parser → `FuncArgs` →
      `catalog.Trigger.Args` → `buildTriggerDefString` with `''`-doubled quoting); the lexer collapses
      `''`→`'` on input so re-escaping is symmetric. **No production change** — fixture-only slice.
      Byte-identical vs real pg_dump 18.3. Tests slice-368 `trg_arg` `TestPort_PgDumpConnectionSetup`
      (two args, second with embedded quote) + new `TestParseCreateTriggerFuncArgs` (parser);
      `TestBuildTriggerDefString` already pinned the render. **Deferred (ledger):** the parser
      silently SKIPS non-string trigger args (`fn(42)` → arg dropped); PG stores+dumps `'42'`.
      **2026-06-30 (loop #100, design 0110-0001 slice 369): trigger `EXECUTE FUNCTION fn(0042,3.14,foo)`
      non-string arguments round-trip — PRODUCTION fix resolving the slice-368 deferral.** PG gram.y
      `TriggerFuncArg` stores every arg form as a string in `tgargs` (Iconst via `psprintf("%d")` so
      `0042`→`42`, FCONST by lexeme, ColLabel by text); `pg_get_triggerdef` re-quotes them all →
      `trig_fn('42', '3.14', 'foo')`. `parseCreateTriggerTail` now captures `TokenIntLit` (canonicalised
      via new `canonicalTriggerIntArg`), `TokenNumericLit`, `TokenIdent` into `FuncArgs` instead of
      skipping; `buildTriggerDefString` already quotes every stored arg (no deparse change). Byte-identical
      vs real pg_dump 18.3 (oracle-verified). Tests slice-369 `trg_narg` `TestPort_PgDumpConnectionSetup`
      + extended `TestParseCreateTriggerFuncArgs` (int/float/ident/string). **Deferred (ledger):** int
      canonicalisation covers Go-int range only (PG rejects larger first → fallback unreachable).
      **2026-06-30 (loop #9, design 0110-0001 slice 370): `COMMENT ON TRIGGER <name> ON <table>`
      round-trip (PRODUCTION fix).** PG stores a trigger comment in pg_description keyed
      `(classoid=pg_trigger=2620, objoid=trig.oid, objsubid=0)`; pg_dump's `dumpTrigger`
      (pg_dump.c:19251) calls `dumpComment` so a dump re-emits `COMMENT ON TRIGGER … ON … IS '...';`.
      `parseCommentOnTail` had no TRIGGER branch → the statement fell to the unsupported-default arm and
      the server silently swallowed it (comment never reached pg_description). Added the parser branch
      (captures `TRIGGER <name> ON [schema.]table`, the same `<name> ON <table>` shape as COMMENT ON
      CONSTRAINT) + `execCommentOn` `case "trigger"` (`LookupTable`→`Table.Triggers`→`SetComment(2620,
      trig.OID, 0, desc)`). pg_trigger already surfaces each user trigger with oid/tableoid=2620 (slice
      319) and pg_dump's collectComments reads the keyed row — no catalog-query change. Byte-identical vs
      real pg_dump 18.3. Tests `TestParseCommentOnTrigger` (parser) + slice-370 `trg_biu` trigger-comment
      line in `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):** restart persistence (in-memory
      pg_description); COMMENT ON {RULE,POLICY,COLLATION,LANGUAGE,DATABASE,EXTENSION} still dropped (no
      parser branch — sibling slices).
      **2026-06-30 (loop #10, design 0110-0001 slice 371): `COMMENT ON POLICY <name> ON <table>`
      round-trip (PRODUCTION fix).** PG stores an RLS-policy comment in pg_description keyed
      `(classoid=pg_policy=3256, objoid=pol.oid, objsubid=0)`; pg_dump's `dumpPolicy` calls `dumpComment`
      so a dump re-emits `COMMENT ON POLICY … ON … IS '...';`. `parseCommentOnTail` had no POLICY branch →
      silently swallowed. Added the parser branch (captures `POLICY <name> ON [schema.]table`, the same
      `<name> ON <table>` shape as COMMENT ON TRIGGER) + `execCommentOn` `case "policy"`
      (`LookupTable`→`Table.Policies`→`SetComment(3256, pol.OID, 0, desc)`). CREATE POLICY already
      round-trips (slices 323/330; pg_policy exposes each policy's oid); pg_description path
      classoid-agnostic (slice 370) — no catalog-query change. Byte-identical vs real pg_dump 18.3.
      Tests `TestParseCommentOnPolicy` (parser) + slice-371 `p_simple` policy-comment line in
      `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):** restart persistence (in-memory
      pg_description); COMMENT ON {RULE,COLLATION,LANGUAGE,DATABASE,EXTENSION} still dropped (sibling
      slices).
      **2026-06-30 (loop #11, design 0110-0001 slice 372): `COMMENT ON RULE <name> ON <table>`
      round-trip (PRODUCTION fix).** PG stores a query-rewrite-rule comment in pg_description keyed
      `(classoid=pg_rewrite=2618, objoid=rule.oid, objsubid=0)`; pg_dump's `dumpRule` (pg_dump.c:19359)
      builds the prefix `"RULE %s ON"` and calls `dumpComment` so a dump re-emits
      `COMMENT ON RULE … ON … IS '...';`. `parseCommentOnTail` had no RULE branch → silently swallowed.
      Added the parser branch (captures `RULE <name> ON [schema.]table`, the same `<name> ON <table>`
      shape as COMMENT ON TRIGGER/POLICY) + `execCommentOn` `case "rule"`
      (`LookupTable`→`Table.Rules`→`SetComment(2618, r.OID, 0, desc)`). CREATE RULE already round-trips
      (slice 324; each rule modelled as `catalog.RuleInfo` with its own OID, projected into the
      pg_rewrite virtual catalog); pg_description path classoid-agnostic (slices 370/371) — no
      catalog-query change. Byte-identical vs real pg_dump 18.3. Tests `TestParseCommentOnRule` (parser)
      + slice-372 `r_noins` rule-comment line in `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):**
      restart persistence (in-memory pg_description); COMMENT ON {COLLATION,LANGUAGE,DATABASE,EXTENSION}
      still dropped (sibling slices).
      **2026-06-30 (loop #12, design 0110-0001 slice 373): TABLE column typed as a user-defined
      COMPOSITE type round-trip (PRODUCTION fix).** A composite TYPE round-trips (slices 242/243) and a
      composite FIELD typed as another composite resolves to the qualified name (slice 249), but a TABLE
      COLUMN `c public.addr` did NOT: a composite name is not a built-in, so the CREATE TABLE column path
      folds it to the text fallback (`TypeNameToOID`/`ResolveColumnType`). `buildUserPGAttributeRow` had
      enum (slice 88) + domain (slice 90) branches over the text fallback but NO composite branch, so
      `pg_attribute.atttypid` stayed text(25) and pg_dump's `getTableAttrs`→`format_type` rendered the
      column as `text` / `text[]` — an UNRESTORABLE dump. Added the composite branch (mirrors enum/domain):
      `cat.LookupCompositeType(col.Type.Name)` → composite OID (scalar) / `ct.ArrayOID` (`addr[]`,
      attndims=1), varlena/`attalign='d'`/`attstorage='x'` layout (mirrors `buildUserPGTypeRowForComposite`).
      `format_type` already resolves composite OID/array OID back to the qualified name (slices 249/250) —
      no other site changed. Byte-identical vs real pg_dump 18.3. Tests `TestUserPGAttributeCompositeColumn`
      (unit) + `public.comptcol` table in `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):**
      composite-column VALUES (INSERT/COPY) not exercised — schema-dump fidelity only; non-public-schema
      composite columns uncovered.

      **2026-06-30 (loop #13, design 0110-0001 slice 374): typed table `CREATE TABLE name OF
      composite_type` round-trip (PRODUCTION fix).** A composite TYPE round-trips (242/243) and a column
      typed as a composite round-trips (373), but a TYPED TABLE — whose whole column set is *derived* from
      a composite via `OF type` — could not be created: goopg's CREATE TABLE parser had no `OF` arm
      (syntax error). PG records `pg_class.reloftype`; pg_dump appends ` OF <type>` and SKIPS every
      type-derived column (the reloftype attr-loop branch), so the dump is `CREATE TABLE public.typedtab OF
      public.addr2type;` with NO column list. Wired end-to-end: parser arm → `CreateTableStmt.OfType`
      (new AST field), `execCreateTable` looks up the composite (`LookupCompositeType`), synthesizes a
      `ColumnDef` per field (`compositeFieldColumnType` parses the stored ColType) through the normal
      column-build path, and stamps `catalog.Table.OfTypeOID`; surfaced as `reloftype` in BOTH the virtual
      `VirtualRows` pg_class (pg_dump-read) and the heap `buildUserPGClassRow` sibling. PG keeps
      attislocal=true (pg_dump skips via reloftype, not attislocal) so no inheritance plumbing. Columns are
      real: COPY `(a, b)` + data row `7\tseven` round-trip. Byte-identical vs real pg_dump 18.3 (ref
      /tmp/du374_pgdata). Tests `TestUserPGClassRowOfType` + `TestCompositeFieldColumnType` (unit) +
      `public.addr2type`/`public.typedtab` in `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):**
      per-column `OF type (col WITH OPTIONS …)` form rejected; non-public-schema composite `OF` uncovered;
      pg_class.reltype stays 0.
      **2026-06-30 (loop #16, design 0110-0001 slice 377): `CREATE USER MAPPING FOR <user>
      SERVER <srv>` round-trip + exit-0 pipeline REPAIR (PRODUCTION fix).** Slice 376 made a foreign
      server dumpable, which makes pg_dump's `dumpForeignServer` ALWAYS call `dumpUserMappings` →
      `SELECT usename,…umoptions FROM pg_user_mappings WHERE srvid='<oid>'`. goopg had no
      `pg_user_mappings` view, so pg_dump aborted (`exit=1`, empty dump) and `TestPort_PgDumpConnectionSetup`
      silently skipped EVERY positive assertion (they run only inside `if res.ExitCode==0`) — the suite was
      green while verifying nothing. Added a `pg_user_mappings` virtual relation (umid,srvid,srvname,umuser,
      usename,umoptions) over a dedicated user-mapping registry (`catalog.UserMapping{OID,UmUser,SrvName}` +
      RegisterUserMapping/DropUserMapping/ListUserMappings + `ForeignServerOID` helper; srvid resolves to the
      server OID, umuser via RoleOID, PUBLIC→usename 'public'/umuser 0, umoptions NULL → `pg_options_to_table`
      yields 0 rows → no OPTIONS clause). Parser CREATE/DROP USER MAPPING arms caught BEFORE the generic
      `user` role/compat stubs (plain `CREATE USER <role>` still errors → server-layer role DDL handles it);
      executor `execCompatNoop` `user mapping`→RegisterUserMapping, `DropCompatStmt` `user mapping`→DropUserMapping.
      Emits bare `CREATE USER MAPPING FOR um_role SERVER goopg_srv;` byte-identical vs pg_dump 18.3. Tests
      `TestUserMappingRegistry` + `TestParseCreateUserMapping` + DU-002 slice 377 fixture (which, by reaching
      exit 0, also re-arms slices 375/376 and all earlier asserts). **Deferred (ledger):** mapping `OPTIONS`
      discarded (umoptions NULL); mappings in-memory only; user-spec kind not distinguished (non-`public`
      non-registered user → umuser=0); `pg_user_mapping` heap (OID 1418) not populated.
      **2026-06-30 (loop #17, design 0110-0001 slice 378): `CREATE SERVER … OPTIONS (name 'value', …)`
      round-trip (PRODUCTION fix).** Slice 376 dumped a foreign server but discarded its OPTIONS
      (`srvoptions` always NULL). pg_dump's `getForeignServers` expands them server-side via
      `array_to_string(ARRAY(SELECT quote_ident(option_name)||' '||quote_literal(option_value) FROM
      pg_options_to_table(srvoptions) ORDER BY option_name), E',\n    ')` and `dumpForeignServer` re-emits
      ` OPTIONS (\n    %s\n)` (options sorted by name → `dbname` before `host`). Parser: new
      `scanFDWOptionsList` consumes `OPTIONS ( name 'value', … )` into `name=value` elements stored in new
      `CompatNoopStmt.Options`. Catalog: `ForeignServer.Options []string`; `RegisterForeignServer(name,fdw,
      options)` (idempotent re-register refreshes only when non-empty); `pg_foreign_server` `srvoptions` cell
      renders the PG text[] literal `{name=value,…}` via new `optionsArrayLiteral`, which goopg's own
      `pg_options_to_table` SRF expands. Executor threads `s.Options` into RegisterForeignServer. Emits
      `CREATE SERVER goopg_srv_opt FOREIGN DATA WRAPPER goopg_fdw OPTIONS (\n    dbname 'mydb',\n    host
      'localhost'\n);` byte-identical vs pg_dump 18.3 (negative control confirms the option-order assertion is
      live). Tests `TestParseCreateServerOptions` + `opt_srv` in `TestForeignServerRegistry` + DU-002 slice 378
      fixture. **Deferred (ledger):** FDW (`fdwoptions`) / USER MAPPING (`umoptions`) OPTIONS still discarded;
      option values with array metacharacters not quoted in the text[] literal; `ALTER SERVER … OPTIONS` not
      modelled; servers in-memory only.
      **2026-06-30 (loop #18, design 0110-0001 slice 379): `CREATE USER MAPPING … OPTIONS (name 'value', …)`
      round-trip (PRODUCTION fix).** Slice 377 dumped a user mapping but discarded its OPTIONS (`umoptions`
      always NULL). Reuses the slice-378 machinery wholesale: pg_dump's `dumpUserMappings` expands `umoptions`
      server-side via the identical `array_to_string(ARRAY(SELECT quote_ident(option_name)||' '||quote_literal(
      option_value) FROM pg_options_to_table(umoptions) ORDER BY option_name), E',\n    ')` shape (options sorted
      by name → `password` before `username`). Parser: `scanUserMappingForServer` now also returns the OPTIONS
      list via the shared `scanFDWOptionsList`; CREATE arm stores it in `CompatNoopStmt.Options`, DROP discards
      it. Catalog: `UserMapping.Options []string`; `RegisterUserMapping(user,server,options)` (idempotent
      re-register refreshes only when non-empty); `pg_user_mappings` `umoptions` cell renders via the existing
      `optionsArrayLiteral`. Executor threads `s.Options` into RegisterUserMapping. Emits `CREATE USER MAPPING FOR
      um_role SERVER goopg_srv_um OPTIONS (\n    password 'secret',\n    username 'remote'\n);` byte-identical vs
      pg_dump 18.3 (negative control confirms the order assertion is live). Tests extended `TestParseCreateUserMapping`
      + `umoptions` in `TestUserMappingRegistry` + DU-002 slice 379 fixture. **Discovery (ledger):** goopg's
      `pgQuoteIdent` does NOT actually guard reserved keywords (comment lies), so a reserved-keyword option name
      like `user` emits bare `user 'x'` where real pg_dump emits `"user" 'x'` — latent at every quote_ident site;
      sidestepped here with non-keyword names. **Deferred (ledger):** FDW (`fdwoptions`) OPTIONS still discarded;
      array-metachar value quoting + in-memory-only limits carry over from slice 378.

      **2026-06-30 (loop #19, design 0110-0001 slice 380): `CREATE FOREIGN DATA WRAPPER … OPTIONS (name 'value', …)`
      round-trip (PRODUCTION fix).** Slice 375 dumped an FDW but discarded its OPTIONS (`fdwoptions` always NULL).
      Completes the FDW/SERVER/MAPPING OPTIONS trilogy (378/379/380) reusing the shared `scanFDWOptionsList` +
      `optionsArrayLiteral` machinery: pg_dump's `getForeignDataWrappers` expands `fdwoptions` server-side via the
      identical `array_to_string(ARRAY(SELECT quote_ident(option_name)||' '||quote_literal(option_value) FROM
      pg_options_to_table(fdwoptions) ORDER BY option_name), E',\n    ')` shape (options sorted by name → `debug`
      before `delimiter`). Parser: the `CREATE FOREIGN DATA WRAPPER` arm now scans for the OPTIONS token and consumes
      it via `scanFDWOptionsList` (HANDLER/VALIDATOR clauses still skipped) → `CompatNoopStmt.Options`. Catalog:
      `ForeignDataWrapper.Options []string`; `RegisterForeignDataWrapper(name,options)` (idempotent re-register
      refreshes only when non-empty); `pg_foreign_data_wrapper` `fdwoptions` cell renders via `optionsArrayLiteral`.
      Executor threads `s.Options` into RegisterForeignDataWrapper. Emits `CREATE FOREIGN DATA WRAPPER goopg_fdw_opt
      OPTIONS (\n    debug 'true',\n    delimiter 'pipe'\n);` byte-identical vs pg_dump 18.3 (negative control confirms
      the order assertion is live; `goopg_fdw` stays bare so the slice-375 no-OPTIONS assertion holds). Tests new
      `TestParseCreateFDWOptions` + `fdwoptions` in `TestForeignDataWrapperRegistry` + DU-002 slice 380 fixture.
      **Deferred (ledger):** array-metachar value quoting STILL absent (metachar-free `pipe`/`true` used); ALTER
      FOREIGN DATA WRAPPER OPTIONS, HANDLER/VALIDATOR func refs, reserved-keyword quote_ident gap, in-memory-only all
      carry over. With this slice the FDW/SERVER/USER-MAPPING OPTIONS round-trip is complete.
      **2026-07-01 (loop #24, design 0110-0001 slice 385): multiple CHECK constraints on a CREATE DOMAIN
      (PRODUCTION fix).** PG lets a domain declare several CHECKs, each a separate `pg_constraint` row that pg_dump
      emits inline `ORDER BY conname`. goopg modelled only ONE check (scalar `Domain.CheckExpr/CheckName/CheckOID`);
      the parser silently dropped every CHECK after the first. Refactored to a slice: `parser.CreateDomainStmt.Checks
      []DomainCheckClause` (parser now appends each clause); `catalog.Domain.Checks []DomainCheck` with new
      `AddDomainCheck` (per-check OID + PG `ChooseConstraintName` auto-disambiguation `<domain>_check`/`_check1`/…);
      `buildPgConstraintRows` + `pg_get_constraintdef` + cast-time IN-values enforcement (`expr.go`) + `execCreateDomain`
      all iterate the slice; `RegisterDomain` dropped its unused `checkInValues` variadic. Emits `multichk` (two unnamed
      checks → `multichk_check`/`multichk_check1`) + `mixchk` (explicit `mix_pos` + auto-named `mixchk_check`)
      byte-identical vs pg_dump 18.3. Tests new `TestAddDomainCheckNaming` (catalog) + DU-002 slice 385 fixtures in
      `TestPort_PgDumpConnectionSetup`. **Deferred (ledger):** runtime enforcement of GENERIC (non-IN) domain CHECK
      predicates still absent (dumped/round-tripped but not evaluated on cast) — pre-existing gap, now spans all checks.

      **2026-07-01 (loop #26, design 0110-0001 slice 386): COMMENT ON SERVER round-trip.** A foreign server
      (`pg_foreign_server`, classoid 1417) can carry a comment; pg_dump's `dumpForeignServer` re-emits
      `COMMENT ON SERVER <name> IS '...'`. goopg's `parseCommentOnTail` had no SERVER branch, so the statement was
      silently swallowed and never reached `pg_description`. Added a `server` arm to `parseCommentOnTail` (bare,
      schema-less name → `ObjKind="server"`) and a `"server"` case to `execCommentOn` that resolves the server OID via
      `catalog.InMemory.ForeignServerOID` and stores the comment under classoid 1417 (new `oidPgForeignSrv` constant).
      Tests new `TestParseCommentOnServer` (parser) + `COMMENT ON SERVER goopg_srv IS 'a server comment'` fixture/assert
      in `TestPort_PgDumpConnectionSetup` (byte-identical vs pg_dump 18.3). No new deferral.

      **2026-07-01 (loop #27, design 0110-0001 slice 387): COMMENT ON FOREIGN DATA WRAPPER round-trip** (sibling of
      slice 386). A foreign-data wrapper (`pg_foreign_data_wrapper`, classoid 2328) can carry a comment; pg_dump's
      `dumpForeignDataWrapper` re-emits `COMMENT ON FOREIGN DATA WRAPPER <name> IS '...'`. goopg's `parseCommentOnTail`
      had no FOREIGN DATA WRAPPER branch, so the statement was silently swallowed and never reached `pg_description`.
      Added a `case p.acceptKeyword(KwForeign)` arm to `parseCommentOnTail` (consumes the DATA WRAPPER ident-keyword pair;
      `ObjKind="foreign data wrapper"`, bare schema-less name) and a `"foreign data wrapper"` case to `execCommentOn` that
      resolves the FDW OID via `catalog.InMemory.ForeignDataWrapperOID` and stores the comment under classoid 2328 (new
      `oidPgFdw` constant). Tests new `TestParseCommentOnForeignDataWrapper` (parser) +
      `COMMENT ON FOREIGN DATA WRAPPER goopg_fdw IS 'a fdw comment'` fixture/assert in `TestPort_PgDumpConnectionSetup`
      (byte-identical vs pg_dump 18.3). No new deferral.

      **2026-07-01 (loop #28, design 0110-0001 slice 388): COMMENT ON EXTENSION round-trip** (sibling of slices
      386/387). An installed extension (`pg_extension`, classoid 3079) can carry a comment; pg_dump's `dumpExtension`
      re-emits `COMMENT ON EXTENSION <name> IS '...'` after the `CREATE EXTENSION` line. goopg's `parseCommentOnTail` had
      no EXTENSION branch, so the statement was silently swallowed and never reached `pg_description`. Added a
      `case p.acceptIdentKeyword("extension")` arm to `parseCommentOnTail` (`ObjKind="extension"`, bare schema-less name)
      and an `"extension"` case to `execCommentOn` that resolves the extension OID via the new
      `catalog.InMemory.ExtensionOID` and stores the comment under classoid 3079 (new `oidPgExtension` constant). Tests new
      `TestParseCommentOnExtension` (parser) + `CREATE EXTENSION amcheck` / `COMMENT ON EXTENSION amcheck IS 'an extension
      comment'` fixture/assert in `TestPort_PgDumpConnectionSetup` (amcheck is goopg's one shipped extension). No new
      deferral.
      **2026-07-01 (loop #29, design 0110-0001 slice 389): CREATE COLLATION round-trip** (NEW dumpable object — the
      COMMENT-ON seam is exhausted for every object goopg dumps). A user collation lives in `pg_collation` with a user
      namespace; pg_dump's `getCollations` filters out the BKI-pinned `pg_catalog` built-ins and `dumpCollation`
      reconstructs `CREATE COLLATION <schema>.<name> (provider = …, locale = …)` from the catalog columns. goopg had no
      `CREATE COLLATION` parser at all (hard parse error). Added `parser.parseCreateCollationTail` (new `CreateCollationStmt`;
      option-list + `FROM existing` forms; LOCALE/LC_COLLATE/LC_CTYPE/PROVIDER/DETERMINISTIC), `catalog.CreateCollation`
      (allocates a user OID, appends to `userCollations`, surfaced as extra `pg_collation` rows) + `CollationAttrsByName`,
      and `executor.execCreateCollation` (libc LOCALE → collcollate/collctype; builtin/icu → colllocale). Planner `DDL` wrap +
      `CREATE COLLATION` command tag. Tests `TestParseCreateCollation`, `TestCreateCollationVirtualRows`, and a
      `CREATE COLLATION public.mycoll (LOCALE = 'C')` fixture in `TestPort_PgDumpConnectionSetup` asserting
      `CREATE COLLATION public.mycoll (provider = libc, locale = 'C');`. Deferral row appended (dump-only; icu/builtin +
      FROM not yet asserted; in-memory only; no ALTER COLLATION / collation comments).

      **2026-07-01 (loop #30, design 0110-0001 slice 390): COMMENT ON COLLATION round-trip** (follow-on to slice 389).
      `dumpCollation` ends with a `dumpComment(fout, "COLLATION", …, collinfo->dobj.catId, 0, …)` call (`pg_dump.c:15050`)
      that re-emits `COMMENT ON COLLATION <schema>.<name> IS '...'` from the `pg_description` row keyed on the collation's
      `catalogId` (tableoid=pg_collation=3456, objsubid=0). goopg's `parseCommentOnTail` had no `COLLATION` branch, so the
      statement was silently swallowed and never reached `pg_description`. Added a `case p.acceptIdentKeyword("collation")`
      arm (schema-qualifiable name → `ObjKind="collation"`) and an `execCommentOn` `"collation"` case that resolves the
      collation OID via the existing `CollationAttrsByName` registry and stores the comment under classoid 3456
      (`oidPgCollation`). Tests `TestParseCommentOnCollation` (parser) + a `COMMENT ON COLLATION public.mycoll IS
      'case-sensitive C collation'` fixture/assertion in `TestPort_PgDumpConnectionSetup` (byte-identical vs real pg_dump
      18.3). Deferral row appended (built-in collation comments are a no-op since they report OID 0; in-memory only — same
      pg_collation-heap-persistence gap as slice 389).

      **2026-07-01 (loop #31, design 0110-0001 slice 391): ICU / non-deterministic / FROM collation round-trip** (PRODUCTION
      fix + virtual-NULL infra). Closes the slice-389 deferral that only the default libc provider was asserted. The remaining
      `dumpCollation` branches now round-trip: `provider = icu` (collprovider 'i'; locale rides colllocale, collcollate/collctype
      NULL), `deterministic = false` (collisdeterministic 'f', emitted right after the provider), and `CREATE COLLATION new FROM
      existing`. Found + fixed two fidelity bugs: (1) `execCreateCollation`'s FROM branch copied provider/locale but **dropped
      `Deterministic`** (PG `DefineCollation` reads `collform->collisdeterministic`) — now copies `src.Deterministic`; (2) empty
      `text` virtual cells decoded as `''` not NULL, so `dumpCollation`'s ICU branch emitted a spurious `, rules = ''`. Added a
      general `catalog.VirtualNull` sentinel that `planner.TypedVirtualCell` maps to a NULL constant before any type parsing
      (shared by the executor `rematerialiseVirtualRows` sibling); the pg_collation user-row builder emits `VirtualNull` for
      absent locale/rules columns per provider — matching PG's NULL layout. Tests: extended `TestCreateCollationVirtualRows`
      (a non-deterministic ICU collation resolved by name reports `Deterministic=false`) + `ci_coll`/`ci_from` fixtures in
      `TestPort_PgDumpConnectionSetup`, both dumping `(provider = icu, deterministic = false, locale = 'und-u-ks-level2')`
      byte-identical vs real pg_dump 18.3. Deferral row appended (ICU `rules` still unmodelled; in-memory only; BKI built-in
      rows still carry `''` not NULL in unused locale columns — harmless, pg_dump skips pinned collations).

      **2026-07-01 (loop #32, design 0110-0001 slice 392): ICU collation `rules` round-trip** (closes the slice-391 rules
      deferral). The last unexercised limb of `dumpCollation`'s `provider = icu` branch — the `if (collicurules) { …, rules =
      … }` clause (`pg_dump.c:14988`). Parser's `parseCreateCollationTail` moved `RULES` out of the accept-and-ignore default
      into an explicit `case "rules": stmt.Rules = val` (new `CreateCollationStmt.Rules`); `catalog.UserCollation.Rules` is
      surfaced as `collicurules` via the virtual-row builder's `nz(uc.Rules)` (→ `VirtualNull` when unset, so a rules-less
      collation reads NULL and dumps no clause — slice-391 infra); `execCreateCollation` stores `s.Rules` for the icu provider
      only (PG `DefineCollation` rejects RULES for libc/builtin) and the FROM branch copies `src.Rules`. Tests: extended
      `TestParseCreateCollation` (rules captured) + `TestCreateCollationVirtualRows` (NULL when unset / verbatim when set / FROM
      copies) + a `ci_rules` `TestPort_PgDumpConnectionSetup` fixture dumping `(provider = icu, locale = 'und', rules = '&V <<
      w <<< W')` byte-identical vs real pg_dump 18.3. Deferral row appended (registry still in-memory; BKI built-ins still '' not
      NULL in unused locale columns — harmless).
      **2026-07-01 (loop #33, design 0110-0001 slice 393): libc two-clause + builtin provider round-trip** (TEST-ONLY;
      closes the last two unexercised `dumpCollation` provider limbs, `pg_dump.c:14934+`). Slices 389–392 only drove the libc
      *collapse* path (`collcollate == collctype` → single `locale =`) and the icu branch; the libc `else` limb (`strcmp(
      collcollate, collctype) != 0` → `lc_collate = '...', lc_ctype = '...'`) and the PG17+ `provider = builtin` (collprovider
      'b') branch were already modeled by `execCreateCollation`/the virtual builder but never round-tripped end-to-end. Two new
      `TestPort_PgDumpConnectionSetup` fixtures — `CREATE COLLATION public.libc_diff (LC_COLLATE='C', LC_CTYPE='POSIX')` →
      `(provider = libc, lc_collate = 'C', lc_ctype = 'POSIX')` and `CREATE COLLATION public.builtin_coll (provider = builtin,
      locale = 'C')` → `(provider = builtin, locale = 'C')` — both byte-identical vs real pg_dump 18.3. No production change
      (the executor/catalog already handled both providers). With this every dumpCollation provider limb goopg can emit (libc
      collapse, libc two-clause, icu locale/rules/deterministic, builtin) is round-trip-asserted; collation pg_dump fidelity is
      complete. Deferral row appended (carry-forward only: registry still in-memory; BKI '' vs NULL; dump-only no real ordering).
      **2026-07-01 (loop #34, design 0110-0001 slice 394): a column collated with a USER collation round-trip.** Slices
      389–393 made a user collation itself dump (`CREATE COLLATION`) and slice 188 made a column collated with a *built-in*
      collation dump (`a text COLLATE pg_catalog."C"`), but the composition — a table column / composite field declared
      `COLLATE <user-collation>` — was silently dropped: the attcollation surfacing
      (`buildUserPGAttributeRow{,ForCompositeField}`) resolved the declared name to an OID only via `collationNameToOID`
      (the seven built-ins), so a user-collation name fell through to the type default (`typcollation`), pg_dump's
      `getTableAttrs` saw no `attcollation <> typcollation` difference, and no COLLATE clause was emitted. Both sibling
      sites now fall back to a new `catalog.InMemory.UserCollationOIDByName(name)` (bare-name, case-insensitive search of
      `userCollations`) when `collationNameToOID` returns 0, so the shadowed `attcollation` carries the real user-collation
      OID whose `pg_collation` row pg_dump already reads (slices 389+); the COLLATE rendering / schema-qualification
      (`public.usercoll`, vs the built-in `pg_catalog."C"` form) is all pg_dump-client-side and unchanged. One
      `TestPort_PgDumpConnectionSetup` fixture (`CREATE COLLATION public.usercoll (LOCALE = 'C')` +
      `CREATE TABLE public.usercollcol (a text COLLATE public.usercoll, b text)`), byte-identical vs real pg_dump 18.3;
      negative assertion scoped to the usercollcol block (collcol.b legitimately carries `COLLATE pg_catalog."POSIX"`).
      Deferred: bare-name resolution doesn't disambiguate same-named collations across schemas (dump-OID fidelity only;
      goopg does not actually collate); in-memory-registry/no-restart-persistence carry-forward from 389–393.

      **2026-07-01 (loop #35, design 0110-0001 slice 395): a user-defined CAST round-trip — a NEW object family.** pg_dump's
      `getCasts` reads all `pg_cast` rows (built-in casts excluded by OID at dump-out time via `selectDumpableCast`), then
      `dumpCast` renders `CREATE CAST (<src> AS <tgt>) WITHOUT FUNCTION[ AS ASSIGNMENT|IMPLICIT];`. goopg previously had NO
      CREATE CAST dispatch case — `parseCreate` fell through to its `expected TABLE, INDEX, …` error, rejecting the statement
      outright. Three-layer fix: (parser) new `case "cast"` → `parseCreateCastTail` parses `(src AS tgt) {WITHOUT FUNCTION |
      WITH INOUT | WITH FUNCTION …} [AS ASSIGNMENT|IMPLICIT]`, recording source/target (`ArgTypes`), castmethod
      (`CastMethod` b/i/f) and castcontext (`CastContext` e/a/i) on a `CompatNoopStmt`; (executor) `execCompatNoop` `case
      "cast"` calls `catalog.RegisterCast` for the binary/INOUT forms (castfunc=0), and DROP CAST calls `DropCast`; (catalog)
      a `Cast` registry + `pg_cast` virtual view surfacing each cast, resolving type names to castsource/casttarget OIDs via
      `TypeNameToOID` (text=25, bytea=17). Two `TestPort_PgDumpConnectionSetup` casts (`text→bytea` explicit;
      `bytea→text` AS ASSIGNMENT), byte-identical vs real pg_dump 18.3 (reference /tmp/castpg). Deferred: WITH FUNCTION casts
      parsed but not dumped (castfunc=0 — needs a pg_proc row for `dumpCast`'s findFuncByOid); only built-in binary-coercible
      type pairs reachable (PG rejects composite/enum/array/domain WITHOUT FUNCTION, goopg cannot create base types);
      bare/built-in TypeNameToOID only; in-memory registry (no restart persistence).

      **2026-07-01 (loop #36, design 0110-0001 slice 396): COMMENT ON CAST round-trip** (follow-on to slice 395). `dumpCast`
      appends a trailing `COMMENT ON CAST (<src> AS <tgt>) IS '...';` when the cast's `dobj` carries
      `DUMP_COMPONENT_COMMENT` — i.e. when `collectComments` found a `pg_description` row keyed on the cast's catalogId
      (`classoid = pg_cast = 2605`, objsubid 0). The COMMENT identity is the type pair `(source AS target)`, not a name.
      goopg's COMMENT-ON parser had no CAST branch and silently swallowed the statement, so the comment never reached
      pg_description. Three-layer fix mirroring slice 390 (COMMENT ON COLLATION): (parser) new `case
      p.acceptIdentKeyword("cast")` parses `(src AS tgt)` via `parseCastTypeName` into new `CommentOnStmt.CastSource`/
      `CastTarget`; (executor) new `case "cast"` resolves the cast OID via `catalog.CastByTypes` and stores under pg_cast
      with `SetComment(2605, cast.OID, 0, desc)` (built-in/unknown cast → OID 0 → harmless no-op); (catalog) new
      `CastByTypes` lookup; the generic `pg_description` virtual view (walks `AllComments`) surfaces the row, no view change.
      `TestPort_PgDumpConnectionSetup` asserts `COMMENT ON CAST (text AS bytea) IS 'binary-coercible text to bytea';`,
      verified byte-identical vs real pg_dump 18.3; parser coverage `TestParseCommentOnCast`. In-memory only (no restart
      persistence), like the cast registry.
      **2026-07-01 (loop #37, design 0110-0001 slice 397): WITH FUNCTION cast round-trip** (closes the slice-395 WITH
      FUNCTION deferral). `dumpCast`'s `COERCION_METHOD_FUNCTION` arm renders `WITH FUNCTION <ns>.<signature>` via
      `findFuncByOid(castfunc)` + `format_function_signature` (signature read from the function's real
      `pg_proc.proargtypes`), so the only requirement is `pg_cast.castfunc == the function's pg_proc.oid`. Slice 395 left
      `castfunc = 0` and deferred this form. Three-layer fix: (parser) `parseCreateCastTail`'s `method="f"` branch now
      parses `WITH FUNCTION funcname[(argtypes)]` into new `CompatNoopStmt.CastFuncName`/`CastFuncArgs` (was discarded);
      (executor) `execCompatNoop` "cast" resolves the routine via `Routines().Lookup` (explicit arg list → exact overload,
      mirrors COMMENT ON FUNCTION slice 147; `LookupByName` sole-overload fallback when parens omitted) and passes the
      routine OID as the new `funcOID` param to `RegisterCast`; (catalog) `RegisterCast` stores it on `Cast.FuncOID` (the
      pg_cast virtual row already surfaced FuncOID/Method). `Routine.OID` == the pg_proc virtual-view OID, so the func/cast
      cross-reference matches. Fixture `public.text_as_int(text) RETURNS integer` + `CREATE CAST (text AS integer) WITH
      FUNCTION public.text_as_int(text)` (text→integer has no built-in cast per pg_cast.dat) →
      `CREATE CAST (text AS integer) WITH FUNCTION public.text_as_int(text);` byte-identical vs real pg_dump 18.3 (live
      /tmp/castfn_pg); parser coverage `TestParseCreateCastWithFunction`. In-memory only (no restart persistence).
      **2026-07-01 (loop #38, slice 398): CREATE CAST argument/return-type validation** (closes the slice-397 deferral
      (c)) — `validateCreateCast`/`castTypeOIDMatch` in `internal/executor/operators_ddl.go` port PG's CreateCast
      argument/return-type rules (42P17 on mismatch); `TestValidateCreateCast` (18 cases). Full detail + remaining
      deferrals (binary-coercibility, unresolved-function leniency, permission checks) in the ledger (slice-398 row).
      **2026-07-01 (loop #39, slice 399): CREATE [DEFAULT] CONVERSION round-trip** — new object family. Parser
      `parseCreateConversionTail`, catalog `UserConversion` registry + populated `pg_conversion` VirtualRows, new
      `pg_encoding_to_char(int4)` builtin. Byte-identical vs pg_dump 18.3. Ledger (slice-399 row) tracks the three
      opened deferrals: (a) encoding-alias resolution, (b) FROM-function pg_proc validation, (c) restart persistence.
      **2026-07-01 (loop #40, slice 400): CONVERSION encoding-alias resolution** (closes slice-399 (a)) —
      `catalog/encoding.go` `pgConvEncAliases` mirrors `pg_encname_tbl`; `unicode`→UTF8 etc. now resolve.
      **2026-07-01 (loop #42, slice 401): CREATE CONVERSION encoding-name validation** (closes slice-399 (b)'s
      encoding half) — `validateCreateConversionEncodings` raises 42704 (unknown encoding) / 42P17 (SQL_ASCII).
      **2026-07-01 (loop #43, slice 402): CONVERSION FROM-function existence/return-type validation** (closes the
      slice-399/400/401 deferral (b)-remainder) — `resolveConversionFunc` mirrors `LookupFuncName(...,
      {int4,int4,cstring,internal,int4,bool}, false)` + `get_func_rettype != INT4OID`; 42883/42P17 on mismatch.
      `TestResolveConversionFunc` (7 cases). Opened: conproc is still as-written text, not a FuncOID→pg_proc
      cross-reference (unlike `pg_cast.castfunc`).
      **2026-07-01 (loop #44, slice 403): conproc FuncOID→pg_proc cross-reference** (closes the slice-402 deferral) —
      `catalog.UserConversion.FuncOID` is now set from the routine `resolveConversionFunc` resolves at CREATE time;
      `pg_conversion`'s `VirtualRows` renders `conproc` via a live `Routines().LookupByOID(FuncOID)` lookup (falling
      back to the as-written `ProcSchema`/`ProcName` text only when `FuncOID` is unset), mirroring how `pg_cast.castfunc`
      (slice 397) tracks the function rather than capturing a name snapshot — a later `ALTER FUNCTION ... RENAME` on
      the conversion function now propagates to the dump. Tests: `TestPgConversionVirtualRowsFuncOID` (catalog,
      resolves via OID / tracks rename / falls back when unresolved); existing `TestPgConversionVirtualRows` and the
      slice-399..402 `TestPort_PgDumpConnectionSetup` assertions unaffected (function names unchanged so output is
      byte-identical). Full detail in the ledger (slice-403 row). Still open under M0119-0004: the EXECUTE ACL check,
      the runtime `OidFunctionCall6` probe (needs an encoding-conversion engine), and restart persistence (recurring
      shared-catalog runtime-write gap).
      **2026-07-01 (loop #45, slice 404): CREATE/DROP TRANSFORM skeleton** — new object family. Parser
      `parseCreateTransformTail` (`FOR type LANGUAGE lang (FROM SQL WITH FUNCTION f1 [, TO SQL WITH FUNCTION f2] |
      ...)`, either half alone or both in either order) + a dedicated `DROP TRANSFORM FOR type LANGUAGE lang` case
      (previously mis-routed through the generic ident-based DROP stub list, which cannot parse `FOR ... LANGUAGE
      ...`). Shared `parseSimpleTypeName(stopKeywords...)` generalizes `parseCastTypeName`. Catalog `Transform`
      registry (`RegisterTransform`/`TransformExists`/`DropTransform`/`ListTransforms`) + `LanguageNameToOID` +
      populated `pg_transform.VirtualRows`. Executor `resolveTransformFunc` ports PG's `CreateTransform` +
      `check_transform_function` rules (return-type role check, not volatile/procedure/SETOF, exactly one `internal`
      arg), reusing CAST's WITH-FUNCTION lookup style. Tests: parser (`TestParseCreateTransform`/
      `TestParseDropTransform`), catalog (`TestLanguageNameToOID`/`TestTransformRegistry`/
      `TestPgTransformVirtualRows`), executor (`TestResolveTransformFunc`, 10 cases). Live smoke-tested against the
      exact PG 18.3 TAP fixture SQL — registers/drops correctly, all error paths (unknown language, duplicate,
      not-found) fire the right SQLSTATE. **NOT yet wired into the DU-002 connsetup fixture**: discovered (via 2
      Explore-agent passes + a live empirical check) that goopg's server-side `pg_proc` catalog exposes NO builtin
      functions at all (`SELECT * FROM pg_proc WHERE proname='int4recv'` → 0 rows), so the fixture's builtin
      `prsd_lextype`/`int4recv` references can't resolve to non-zero `trffromsql`/`trftosql` — this is a
      **pre-existing, previously-undocumented gap that already silently affects CAST's WITH FUNCTION (slice 397) and
      CONVERSION's FROM-function (slice 402) too**, just never exercised by their fixtures. Ledger (slice-404 row)
      has the full resume point: expose builtin pg_proc rows queryably (leaf-package name→metadata table, or a
      catalog-level `LookupBuiltinProcByName` backed by whatever serves the live `pg_proc` view) so all three
      WITH-FUNCTION-on-builtin call sites can resolve consistently.
      **2026-07-01 (loop #46) — builtin pg_proc exposure LANDED; slice 404 now fully closed (connsetup
      fixture wired + byte-identical).** Closed the slice-404 resume point with shape (b): new
      `catalog.BuiltinProc`/`LookupBuiltinProc`/`BuiltinProcs()` (`internal/catalog/catalog.go`) hand-curates
      `int4recv`/`prsd_lextype` (the two functions the CREATE TRANSFORM fixture needs) from real
      `pg_proc.dat` values; `internal/catalog` is a leaf package both `internal/initdb` and
      `internal/executor` already import cycle-free. `resolveTransformFunc` now falls back to this table
      (validated via a new `validateBuiltinTransformFunc` running the same `check_transform_function` rules)
      when no user routine matches, and `pg_proc_view.go`'s `registerPgProcView` appends the same rows to the
      SQL-queryable view (after user routines, so existing `rows[len(builtinProcs):]` test slicing is
      unaffected). A second gap surfaced mid-loop: `formatTypeOID` (`internal/executor/expr.go`) had no case
      for OID 2281 (`internal` pseudo-type), so pg_dump's server-side `format_type` catalog query rendered
      `(???)` instead of `(internal)` in the WITH FUNCTION clause — fixed with a one-line case, matching the
      existing `record` (2249) pseudo-type precedent. The `'CREATE TRANSFORM FOR int'` DU-002 connsetup
      fixture (exact upstream `002_pg_dump.pl` SQL) is now wired into `TestPort_PgDumpConnectionSetup` and
      byte-identical vs real pg_dump 18.3. Tests: `TestLookupBuiltinProc`/`TestBuiltinProcs` (catalog),
      `TestPgProcViewBuiltinTransformFuncs` (initdb), 5 new `TestResolveTransformFunc` subtests,
      `TestFormatTypeOIDInternalPseudoType` (executor). Gates: full catalog/executor/initdb/parser suites +
      `go vet` clean; TPC-H spotcheck Q12=2/Q13=33 PASS; the whole `TestPort_PgDumpConnectionSetup` suite
      PASS; pgbench smoke = pre-commit. Deliberately still deferred (ledger slice-404-follow-up row): CAST's
      WITH FUNCTION (397) / CONVERSION's FROM function (402) don't yet call `LookupBuiltinProc` (no fixture
      forces it yet, trivial follow-up); restart persistence stays in-memory only (recurring gap since 389);
      the curated table stays intentionally narrow (2 entries, not a full `pg_proc.dat` port).
      **2026-07-01 (loop #47) — CREATE/DROP TRANSFORM restart persistence LANDED (first of the slice-389
      "in-memory only" backlog to close, WAL/MVCC practice card gates run).** goopg's `catalog.InMemory.transforms`
      map (and its siblings `casts`/`conversions`/`collations`) had no on-disk representation, so any of these
      objects vanished on restart — a recurring deferral since slice 389. Closed for TRANSFORM only, as a template
      for the other three: new WAL record kinds `RecordKindCreateTransform`(36)/`RecordKindDropTransform`(37) +
      `Encode/DecodeCreateTransform`/`Encode/DecodeDropTransform` (`internal/wal/recovery.go`, mirrors
      `RecordKindCreateSchema`/`DropSchema` M0110-0003 exactly — physical replay is a no-op, goopg has no
      per-transform on-disk page state); new `RegisterTransformDuringRecovery`/`DropTransformDuringRecovery`
      idempotent catalog hooks (`internal/catalog/catalog.go`); new recovery driver
      `internal/initdb/transform_ddl_recovery.go` (`replayTransformDDLRecords`, mirrors `schema_ddl_recovery.go`),
      wired into `internal/initdb/open.go` right after the existing schema replay call; executor
      (`internal/executor/operators_ddl.go`) emits the WAL record at both the CREATE `case "transform"` arm and the
      DROP `objType == "transform"` arm, mirroring the CREATE/DROP SCHEMA call sites. A real bug — not just a
      hypothetical — was caught by the round-trip unit test: the first `EncodeCreateTransform` under-allocated the
      output buffer by 2 bytes (missed the `langLen` field in the size expression), silently corrupting the lang
      field; `TestEncodeDecodeCreateTransformRoundTrip` failed immediately and pinpointed it. Verified two ways:
      (1) new unit tests `TestEncodeDecodeCreate/DropTransformRoundTrip` + reject-wrong-kind/truncated
      (`internal/wal`), `TestTransformDDLRecoveryReplaysCreate`/`...ReplaysDropAfterCreate`/
      `TestReplayTransformDDLRecordsHandlesMissingWalDir` (`internal/initdb`, full Init→Open→WAL.Append→Close→
      re-Open cycle); (2) live manual restart test — built a throwaway binary, ran the exact upstream
      `'CREATE TRANSFORM FOR int'` fixture SQL via real `psql`, `kill`ed the server (not graceful), restarted
      against the same data dir, confirmed `pg_transform` still showed the row with the same OIDs, then
      `DROP TRANSFORM`, a third restart, confirmed `pg_transform` was empty again. Gates: `go build`/`go vet`
      clean; `-race` on `internal/wal`+`internal/catalog` (WAL/MVCC practice card); full
      wal/catalog/initdb/executor/parser suites PASS; `TestPort_PgDumpConnectionSetup` (whole suite, confirms this
      purely-durability change didn't disturb the connsetup fixture) PASS; `TestE2E_PhysicalReplication` PASS
      (WAL/MVCC practice card recovery-path gate); TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit.
      Design doc `0110-0001-pg-dump-tap-port.md` new "CREATE TRANSFORM restart persistence" subsection; ledger
      row appended (this closes part of the slice-389/396/399/404 restart-persistence deferral chain — cast/
      conversion/collation still open, template now exists at bytes 38/39 next-free). Deliberately bounded to ONE
      object kind per the "ONE task per loop" rule — the identical pattern for Cast/Conversion/Collation is each
      its own dedicated loop.
      **2026-07-01 (loop #48) — CREATE/DROP CAST restart persistence LANDED (second of the slice-389 "in-memory
      only" backlog to close).** Repeats loop #47's exact template for `catalog.Cast`: new WAL record kinds
      `RecordKindCreateCast`(38)/`RecordKindDropCast`(39) + `Encode/DecodeCreateCast`/`Encode/DecodeDropCast`
      (`internal/wal/recovery.go`, mirrors `RecordKindCreateTransform`/`DropTransform` exactly — physical replay
      is a no-op); new `RegisterCastDuringRecovery`/`DropCastDuringRecovery` idempotent catalog hooks
      (`internal/catalog/catalog.go`); new recovery driver `internal/initdb/cast_ddl_recovery.go`
      (`replayCastDDLRecords`, mirrors `transform_ddl_recovery.go`), wired into `internal/initdb/open.go` right
      after the transform replay call; executor (`internal/executor/operators_ddl.go`) captures the `*catalog.Cast`
      returned by `RegisterCast` and emits the WAL record at the CREATE `case "cast"` arm and the DROP
      `objType == "cast"` arm. A real bug — again caught by the round-trip unit test, not inspection — the first
      `EncodeCreateCast` under-allocated the output buffer by 1 byte; `TestEncodeDecodeCreateCastRoundTrip` failed
      immediately and pinpointed it. Verified two ways: (1) new unit tests
      `TestEncodeDecodeCreate/DropCastRoundTrip` + reject-wrong-kind/truncated (`internal/wal`),
      `TestCastDDLRecoveryReplaysCreate`/`...ReplaysDropAfterCreate`/`TestReplayCastDDLRecordsHandlesMissingWalDir`
      (`internal/initdb`, full Init→Open→WAL.Append→Close→re-Open cycle); (2) live manual restart test on the
      isolated perf-optimize port (5533) — `CREATE CAST (int4 AS float8) WITHOUT FUNCTION` +
      `CREATE CAST (float4 AS text) WITH INOUT AS ASSIGNMENT` + a `WITH FUNCTION ... AS ASSIGNMENT` cast via real
      `psql`, graceful restart confirmed all three persisted with correct castsource/casttarget/castfunc/
      castcontext/castmethod, then `DROP CAST`, a second restart, confirmed only the other two remained. Gates:
      `go build`/`go vet` clean; `-race -count=1` on `internal/wal`+`internal/catalog`+`internal/initdb`+
      `internal/executor` PASS; `TestE2E_PhysicalReplication` PASS (WAL/MVCC practice card recovery-path gate);
      TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit. Design doc `0110-0001-pg-dump-tap-port.md`
      new "CREATE CAST restart persistence" subsection; ledger row appended. A genuine pre-existing gap surfaced
      during manual verification (NOT this loop's scope, recorded not fixed): `DropCast`'s lookup key uses the raw
      parsed type spelling, not a canonical name, so `DROP CAST (real AS text)` fails to find a cast created via
      `CREATE CAST (float4 AS text) ...` — PG type-name synonyms don't cross-resolve in the slice-395 key scheme.
      Conversion/collation restart persistence still open, template now exists at bytes 40/41 next-free.
      **2026-07-01 (loop #49) — CREATE/DROP CONVERSION restart persistence LANDED (third of the slice-389
      "in-memory only" backlog to close), plus a real cross-cutting WAL-scan bug found and fixed.** Repeats the
      loop #47/#48 template for `catalog.UserConversion` (schema-scoped, unlike Cast/Transform, so replay runs
      after `replaySchemaDDLRecords` in `internal/initdb/open.go`): new `RecordKindCreateConversion`(40)/
      `RecordKindDropConversion`(41) + `Encode/DecodeCreateConversion`/`Encode/DecodeDropConversion`
      (`internal/wal/recovery.go`); new `CreateConversionDuringRecovery`/`DropConversionDuringRecovery` catalog
      hooks (`internal/catalog/catalog.go` — the latter a thin wrapper, since `DropConversion` was already
      replay-safe); new `internal/initdb/conversion_ddl_recovery.go` (`replayConversionDDLRecords`); executor
      WAL emission at the CREATE `case "conversion"` arm and the DROP fallthrough's `case "conversion"` arm
      (`internal/executor/operators_ddl.go`). Third-in-a-row hand-derived byte-offset bug (`EncodeCreateConversion`
      under-allocated by 8 bytes — forgot the four 2-byte length prefixes), caught immediately by
      `TestEncodeDecodeCreateConversionRoundTrip`. **Second, more serious bug found while landing this — NOT
      conversion-specific, shared by all four DDL-recovery drivers (schema/transform/cast/conversion):** all four
      blindly switched on `rec.Payload[0]` without checking whether `rec` was a PG-native/canonical XLogRecord
      (`rec.XLog != nil`, e.g. an `XLOG_CHECKPOINT_SHUTDOWN` written by `Init`) whose `MainData` has no goopg kind
      tag at all — a checkpoint's raw `redo` LSN low byte coincidentally matched `RecordKindCreateConversion`(40)
      and made `TestConversionDDLRecoveryReplaysCreate` fail on a bare Init+Open with zero conversions ever
      appended. Same collision class `wal.ApplyRecord` already guards against for physical replay (M0106-0011
      comment), just never guarded in the catalog-only scanners. Fixed uniformly via new exported
      `wal.IsGoopgNativeRecord(r Record) bool` (`internal/wal/recovery.go`, next to `ApplyRecord`) wired into all
      four `replay*DDLRecords` loops — confirmed load-bearing (not defensive-only) by first trying the naive
      `rec.XLog != nil` skip, which broke all four `...ReplaysCreate` tests (the legitimate CREATE records are
      *also* canonical-framed in this WAL format; only the `Rmid==RmgrXLog && Info==0xF0` check correctly
      distinguishes goopg-native from real-PG-native). Verified: unit tests (`internal/wal/conversion_ddl_test.go`
      + full re-run of schema/cast/transform recovery test families); `go build`/`go vet` clean; `-race -count=1`
      on `internal/wal`+`internal/catalog`+`internal/initdb` PASS; `TestE2E_PhysicalReplication`/
      `...Sync` PASS; TPC-H spotcheck; pgbench smoke = pre-commit. Design doc `0110-0001-pg-dump-tap-port.md` new
      "CREATE CONVERSION restart persistence" subsection (full bug narrative); ledger row appended. Collation
      restart persistence is the last item in the four-object queue, bytes 42/43 next-free — the
      `wal.IsGoopgNativeRecord` guard is already generalized so that loop needs no further WAL-scan fix.
      **2026-07-01 (loop #50) — CREATE/DROP COLLATION restart persistence LANDED (fourth and LAST of the
      slice-389 "in-memory only" backlog — CLOSES the backlog), plus a genuine DROP COLLATION functionality
      gap fixed.** Discovered mid-loop that DROP COLLATION had no implementation at all — no `catalog.DropCollation`
      method existed and `execDropCompat` had no `case "collation"`, so `DROP COLLATION <name>` on an EXISTING
      user collation always raised "does not exist" and `IF EXISTS` always no-op'd without touching the registry
      (slice-389's own recorded deferral (d)). Implemented DROP COLLATION itself first (new `DropCollation`/
      `ListUserCollations` catalog methods; a dedicated `objType == "collation"` block in `execDropCompat`,
      placed before the generic IF-EXISTS fallthrough, looping over `s.Names`), then restart persistence on top
      following the loop #47-49 template: `RecordKindCreateCollation`(42)/`RecordKindDropCollation`(43) +
      `Encode/DecodeCreateCollation`/`Encode/DecodeDropCollation` (`internal/wal/recovery.go` — 15-byte fixed
      header + 6 length-prefixed strings, since `UserCollation`'s libc/ICU/builtin provider fields don't share
      one string set); `CreateCollationDuringRecovery`/`DropCollationDuringRecovery` catalog hooks; new
      `internal/initdb/collation_ddl_recovery.go` wired into `open.go` after `replayConversionDDLRecords`
      (collation is schema-scoped like conversion); WAL emission in `execCreateCollation` (gated on
      `uc.OID != 0` so an `IF NOT EXISTS` hit that didn't mutate the registry doesn't spuriously WAL) and in the
      new collation-drop block, both using the already-generalized `wal.IsGoopgNativeRecord` guard (no further
      WAL-scan fix needed). Used a string-slice loop in the encoder instead of hand-unrolling 6
      PutUint16+copy blocks — deliberate response to 3-for-3 prior hand-derived byte-offset bugs in this series
      (Cast/Transform/Conversion); `TestEncodeDecodeCreateCollationRoundTrip` passed on the first run. Verified:
      new unit tests (`internal/wal/collation_ddl_test.go`, `internal/initdb/collation_ddl_recovery_test.go`,
      `TestDropCollation` in `internal/catalog/create_collation_test.go` pinning the actual functionality fix);
      `go build`/`go vet` clean; `-race -count=1` on `internal/wal`+`internal/catalog`+`internal/initdb` PASS
      (full recovery suite, 252s); `TestE2E_PhysicalReplication`/`...Sync` PASS; `internal/executor` full suite
      PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook; `make ralph-state-guard` OK
      (self-repaired a stale status/progress marker, same pattern as prior loops). Design doc
      `0110-0001-pg-dump-tap-port.md` new "CREATE/DROP COLLATION restart persistence" subsection (full
      narrative); ledger row appended. **The slice-389 four-object restart-persistence backlog
      (Transform → Cast → Conversion → Collation) is now CLOSED.** Still open, independent: ALTER COLLATION
      (OWNER/RENAME/REFRESH VERSION) unhandled; collation ordering/enforcement is still dump-only (goopg never
      uses a collation for actual string comparison); the DROP CAST type-name-synonym key gap (loop #48) remains.
- [x] **DROP CAST type-name-synonym key fix (closes the loop #48 deferral)**
      **COMPLETE 2026-07-01:** `RegisterCast`/`DropCast`/`CastByTypes` (`internal/catalog/catalog.go`) keyed the
      cast registry on the raw lowercased parsed type spelling, so `DROP CAST (real AS text)` failed to find a
      cast created via `CREATE CAST (float4 AS text) ...` — PG built-in type-name synonyms (`real`/`float4`,
      `integer`/`int4`, `double precision`/`float8`, …) never cross-resolved. Fixed via a new `castKey`/
      `castKeyTypeName` pair that resolves each side through `TypeNameToOID` before keying, matching how the
      `pg_cast` virtual view already renders `castsource`/`casttarget`. Caught and fixed a second, previously
      latent bug in the same change: `TypeNameToOID` falls back to `OIDText` for any name it doesn't recognize
      (domains/enums/composites aren't in its builtin switch), so naively keying on its raw result would have
      collapsed every distinct user-defined-type cast into one shared bucket, letting unrelated casts silently
      overwrite each other. `castKeyTypeName` detects the fallback case (result is `OIDText` but the input
      wasn't literally `"text"`) and keys on the lowercased name instead, preserving per-type distinctness for
      anything `TypeNameToOID` can't resolve. New tests in `internal/catalog/cast_synonym_test.go`:
      `TestDropCastResolvesTypeNameSynonyms`, `TestDropCastResolvesMultiWordSynonyms`,
      `TestRegisterCastIdempotentAcrossSynonyms`, `TestCastByTypesDistinctForUnrelatedTypes`,
      `TestCastByTypesDistinctForUnknownUserDefinedTypes`. Gates: `go build`/`go vet` clean;
      `-race -count=1` on `internal/wal`+`internal/catalog`+`internal/initdb`. Catalog-only fix, no WAL format
      change. Design doc `0110-0001-pg-dump-tap-port.md` new "DROP CAST type-name-synonym key fix" subsection;
      ledger row appended.
- [x] **M0119-0004-ACLHEAP — ACL re-sync from the GRANT path for heap-backed catalogs**
      **COMPLETE 2026-06-30 (loop #89):** both heap-backed user-facing ACL columns round-trip
      through real pg_dump 18.3 — `typacl` (TYPE/DOMAIN GRANT, loop #87) and now `attacl`
      (column GRANT, loop #89). `datacl` (pg_database) stays permanently deferred: heap-backed
      **and** `pg_dump --create`-only, so untestable under the `--no-create` connsetup harness
      (ledger). Box checked: all testable heap-ACL objects are covered.
      (source: design `0119-0004-acl-grant-heap-vs-virtual-typacl.md`). Round-trip
      `pg_type.typacl` (TYPE/DOMAIN GRANT), then `pg_attribute.attacl` (column GRANT), by
      routing GRANT/REVOKE on a heap-backed object through `dispatchSimpleQueryViaExecutor`
      and adding an executor GRANT operator that updates the OID-keyed ACL store AND
      re-syncs the heap row (delete-old + re-insert with the projected ACL, mirroring
      `deleteTypeFromCatalogHeap`). New `TypeACLText`/`AttrACLText` renderers reuse
      `relaclTextLockedFor`. Gates: new DU-002 TYPE-grant connsetup slice +
      `TestE2E_PhysicalReplication`/recovery testport (standby reads pg_type) + TPC-H
      Q12/Q13 + full executor/catalog/parser suites + pgbench. `datacl` stays deferred
      (`--create`-only; untestable under the current harness).
      **2026-06-30 (loop #84) — renderer building block LANDED (low blast, no GRANT
      path calls it yet):** `catalog.InMemory.TypeACLText(typeOID)` (the pg_type
      analogue of `ProcACLText`) + `typeACLPrivOrder = {USAGE/'U'}` +
      `ownerTypeACLString = "U"`, added to the `Catalog` interface. A type's
      `acldefault('T', owner) = {=U/owner,owner=U/owner}` (owner + PUBLIC USAGE) is
      structurally identical to the function EXECUTE default, so the projection reuses
      `relaclTextLockedFor` verbatim. Unit tests `TestTypeACLText` /
      `…GrantWithGrantOption` / `…RevokeFromPublic` / `…RevokeFromOwner` mirror the
      ProcACL goldens. Box stays unchecked: the high-blast-radius half (dispatch
      reroute + heap re-sync + the full gate set) is still a dedicated loop. Design
      doc updated with a "Progress" section.
      **2026-06-30 (loop #85) — parser-capture building block LANDED (behaviour-neutral,
      no consumer reads it yet):** `parser.CompatNoopStmt.TypeACL *TypeACLChange`
      (`{Revoke, IsDomain, Privileges, TypeNames, Grantees, WithGrantOption}`) is set
      only for `GRANT`/`REVOKE … ON TYPE|DOMAIN …`; the parser's GRANT/REVOKE scan gained
      explicit `ON TYPE`/`ON DOMAIN` cases + `buildTypeACLChange` token-split helpers.
      `DatabaseACL`/`TableACL` capture unchanged (ON TYPE/DOMAIN was already a
      non-table no-op → `TableACL==""`). Unit tests `TestParseGrantTypeACL` (USAGE/ALL/
      ALL PRIVILEGES/DOMAIN/REVOKE-FROM-PUBLIC/WITH GRANT OPTION/multi-name/multi-grantee/
      CASCADE+GRANTED-BY stripping) + `TestParseGrantNonTypeLeavesTypeACLNil`; full parser
      suite green, `go build ./...` clean. This unblocks the executor wiring: GRANT
      details now reach a parsed AST node `execCompatNoop` runs with a full
      `*executor.Context`. Box stays unchecked: remaining = `query.go` dispatch reroute
      + the `execCompatNoop` heap-resync branch + the full gate set.
      **2026-06-30 (loop #87) — typacl heap-write + read half LANDED; the TYPE GRANT
      round-trip is COMPLETE (design `0119-0004-*` "the heap-write + read half").**
      `GRANT USAGE ON TYPE public.gtype TO typg_grantee` → real pg_dump emits
      `GRANT ALL ON TYPE public.gtype TO typg_grantee;`, byte-identical to PG 18.3
      (`TestPort_PgDumpConnectionSetup` slice 357, strict). Three pieces wired end-to-end:
      (1) **dispatch** — `query.go` excludes `" ON TYPE "`/`" ON DOMAIN "` from the
      autocommit GRANT/REVOKE server fast path (`isHeapACLObject`) so it falls through to
      `dispatchSimpleQueryViaExecutor`; (2) **write** — `execCompatNoop`'s new
      `s.TypeACL != nil` branch (`execTypeACLChange`) updates the OID-keyed ACL store like
      `recordFunctionGrant`/`Revoke` but with USAGE (`typeACLAllPrivs`), then
      `resyncTypeACLHeapRow` rebuilds the `pg_type` row via
      `buildUserPGTypeRowFor{Enum,Domain,Composite}` with `typacl` =
      `NewBytesDatum(encodeAclItemArrayText(TypeACLText(oid), RoleOID))` and applies the
      proven `deleteTypeFromCatalogHeap` + `writeHeapRowCanonical` +
      `mirrorCatalogRelToPostgresDB` template (gated on `catalogHeapSyncAvailable`); (3)
      **read** — `decodePhysicalPGValueMctx` returns an `aclitem[]` heap value as KindBytes
      (full varlena), and the `seqScanOp` pg_type hook renders it to canonical aclitemout
      text via `decodeAclItemArrayText` + the new `catalog.InMemory.RoleNameForOID` reverse
      resolver, so the heap is the single read source (no virtual overlay). Gates ALL PASS:
      DU-002 connsetup (TYPE grant); `TestE2E_PhysicalReplication`; `-race`
      executor/catalog/storage/mvcc; executor/catalog/parser/server units; TPC-H Q12=2/Q13=33;
      pgbench smoke (pre-commit). Units `TestRoleNameForOID` + `TestAclItemHeapDecodeCase`.
      Box stays unchecked: **`attacl`** (column-level GRANT) reuses this exact template
      against the heap-backed `pg_attribute` — the remaining follow-up; **`datacl`** stays
      deferred (`pg_database` heap-backed AND `--create`-only, untestable in the `--no-create`
      connsetup harness).
      **2026-06-30 (loop #88) — `attacl` step 2 renderer + column-ACL store LANDED
      (low blast, no GRANT path calls it yet; the column analogue of loop #84's
      `TypeACLText`).** `internal/catalog/catalog.go`: new composite-keyed stores
      `attrACLs`/`attrACLOrder` (key `attrACLKey{relOID, attNum int16}` — a struct, NOT a
      packed `relOID<<16|attnum` uint32 which would overflow for real table OIDs >2^16);
      `attrACLPrivOrder = {INSERT/a,SELECT/r,UPDATE/w,REFERENCES/x}` (the column-grantable
      subset); `GrantColumnPrivilege`/`GrantColumnPrivilegeWithGrantOption`/
      `RevokeColumnPrivilege` + `AttrACLText(relOID, attNum)` renderer, all added to the
      `Catalog` interface. **Key design divergence:** a column's `acldefault('c', owner)` is
      EMPTY (no owner/PUBLIC implicit privilege), so `attacl` is NULL until the first column
      GRANT, has NO leading owner aclitem, and returns to NULL once the last privilege is
      revoked (a table empties to `{}`, a column to NULL) — the renderer therefore CANNOT
      reuse `relaclTextLockedFor` (which always prepends the owner entry) and the
      `relACLEmptied`/`relACLOwnerRevoked` machinery does not apply. Tests `TestAttrACLText`/
      `…GrantWithGrantOption`/`…Revoke`/`…GranteeNameRendering`; full catalog pkg + `go vet`
      executor/server/catalog clean; design `0119-0004-*` "attacl step 2" + ledger row.
      Remaining `attacl` (the high-blast-radius half, a dedicated loop): parser `AttrACLChange`
      capture + `execAttrACLChange`/`resyncAttrACLHeapRow` + pg_attribute seqscan `attacl`
      decode hook + DU-002 column-GRANT connsetup slice.
      **2026-07-01 (loop #52) — COMMENT ON CAST type-name-synonym re-verification
      CLOSED (test-only, no production change):** closes the last open item from
      the loop #51 DROP CAST synonym-key fix (`castKey`/`castKeyTypeName` resolving
      through `TypeNameToOID` before keying `internal/catalog.RegisterCast`/
      `DropCast`/`CastByTypes`). New `internal/executor/comment_on_cast_synonym_test.go`
      (`TestCommentOnCastResolvesTypeNameSynonym`) proves `COMMENT ON CAST (real AS
      text) IS ...` against a cast created as `CREATE CAST (float4 AS text) WITHOUT
      FUNCTION` resolves via the SAME `catalog.CastByTypes` choke point the
      `execCompatNoop` `case "cast"` comment handler already calls
      (`operators_ddl.go` ~13756), so the loop #51 fix covers comment resolution
      with no further code change needed. Gates: targeted test PASS; `go build
      ./...` clean; `go vet` executor+catalog clean; full `internal/catalog`+
      `internal/executor` suites PASS. Deferral ledger row appended (resolved).
      **2026-07-01 (this loop, design `0119-0004-alter-collation.md`): `ALTER
      COLLATION RENAME TO / OWNER TO / REFRESH VERSION` LANDED** — closes the
      loop #50 row's "ALTER COLLATION ... still unhandled" item. The
      `collation` keyword was entirely absent from `parseAlter()`'s if-chain,
      so all three forms previously failed to parse. New `AlterCollationStmt`
      AST node (`Action` one of `rename`/`owner`/`refresh`; an unmodelled
      trailing form like `SET SCHEMA` parses to a no-op, mirroring
      `AlterStatisticsStmt`); parser branch modeled on
      `AlterAggregateRenameStmt`'s RENAME TO shape + the existing `ALTER TABLE
      … OWNER TO` role-name parsing; catalog `RenameCollation`/
      `SetCollationOwner` mutators beside `CreateCollation`/`DropCollation`;
      executor `execAlterCollation` (dispatch + planner passthrough + `ddlTag`
      wired) resolving the target via `CollationAttrsByName`/`RoleOID`,
      raising `42704` when unknown without `IF EXISTS`. `REFRESH VERSION`
      mirrors PG's own no-detectable-version branch (`collationcmds.c:423-503`
      when `get_collation_actual_version()` returns NULL): always a
      `"version has not changed"` NOTICE, no catalog write — goopg's registry
      has no real ICU/glibc binding to version. Tests
      `TestParseAlterCollationRename`/`…Owner`/`…RefreshVersion` (parser) +
      `TestAlterCollationRenameOwnerRefresh` (executor). Deliberately **not**
      WAL-logged (mirrors the pre-existing, also-unlogged `ALTER TABLE
      RENAME`/`OWNER TO`) — ledger row appended for the restart-persistence
      follow-up (needs net-new `RecordKind` values, no existing ALTER sibling
      to copy). Gates: `go build ./...` clean; `go vet`
      parser/catalog/executor/planner/server clean; those 5 packages' suites
      PASS.
      **2026-07-01 (loop #54): ALTER COLLATION RENAME TO / OWNER TO restart
      persistence LANDED** — closes the "(a) not WAL-logged" deferral from the
      loop directly above. New `RecordKindAlterCollationRename`(44)/
      `RecordKindAlterCollationOwner`(45) (`internal/wal/recovery.go`,
      `Encode/DecodeAlterCollationRename`/`…Owner`) emitted from
      `execAlterCollation`'s two mutation sites (`operators_ddl.go`); replayed
      by two new cases in `replayCollationDDLRecords`
      (`internal/initdb/collation_ddl_recovery.go`) via new
      `RenameCollationDuringRecovery`/`SetCollationOwnerDuringRecovery`
      (`catalog.go`, mirroring `DropCollationDuringRecovery`); `ApplyRecord`
      folds both new kinds into the existing no-op-physical-redo case for
      collation records. Tests:
      `TestEncodeDecodeAlterCollationRenameRoundTrip`/`…OwnerRoundTrip` + guard
      tests (`internal/wal/collation_ddl_test.go`);
      `TestCollationDDLRecoveryReplaysRenameAfterCreate`/`…OwnerAfterCreate`
      (`internal/initdb/collation_ddl_recovery_test.go`, full
      close→reopen→replay, OID preserved across rename). Gates: `go build
      ./...` clean; `go vet` wal/initdb/catalog/executor clean; `go test -race
      ./internal/wal/...` clean; full `internal/wal`+`internal/initdb`+
      `internal/catalog`+`internal/executor` suites PASS; TPC-H spotcheck run.
      Design doc `0119-0004-alter-collation.md` + README index updated.
      Deferral ledger row appended (resolved).
      **2026-07-01 (loop #55, design `0110-0001-pg-dump-tap-port.md` slice
      405): `CREATE AGGREGATE` round-trip LANDED — new object family,
      `pg_aggregate` made SQL-queryable for the first time.** Ports the
      upstream `'CREATE AGGREGATE dump_test.newavg'` fixture
      (public-schema-adapted). Two previously-hidden, aggregate-agnostic
      blockers surfaced and were fixed generally: (1) `pg_aggregate` was not
      SQL-queryable AT ALL — not even the 161 built-in aggregates goopg has
      shipped since M0106-0010 — because the heap file
      (`bootstrapPgAggregateTuples`) was only ever wired for PG18-standby byte
      fidelity, never registered as a `catalog.Table`; fixed by a new
      `registerPgAggregateView` Virtual table
      (`internal/initdb/pg_aggregate_view.go`, mirroring `pg_index`'s existing
      heap-write-plus-Virtual-read split) combining the 161 BKI rows with
      `cat.ListUserAggregates()`. (2) `regproc`/`oid` cross-type comparison was
      unimplemented in the analyzer, so `pg_dump`'s own `dumpAgg` prepared
      query (`a.aggfnoid = p.oid`) raised 42804; fixed by a new `isOIDFamily`
      helper in `internal/analyzer/analyzer.go`'s `isComparable` covering the
      whole oid-alias family (regproc/regclass/regtype/...), which real
      PostgreSQL treats as binary-coercible with `oid`. Also landed:
      `parser.CreateAggregateStmt.FinalFuncModify` (previously silently
      discarded); `catalog.UserAggregate.OID` + `ListUserAggregates()`;
      `pg_proc` now emits a `prokind='a'` row per aggregate
      (`registerPgProcView`); `executor.routineOrAggregateArgs` fallback so
      `pg_get_function_arguments` works for a non-Routine aggregate OID (was
      silently returning `""`, hence a `newavg()` empty-signature symptom);
      two curated builtins (`int4_avg_accum` OID 1963, `int8_avg` OID 1964 —
      the exact functions PG's own `avg(int4)` uses internally); a live
      `pg_aggregate` heap-row write (`executor.buildUserPGAggregateRow`) for
      PG18-standby fidelity, independent of the Virtual SQL-read path.
      Verified against a real running server + real pg_dump 18.3
      byte-identical, AND that the aggregate actually executes at runtime
      (`SELECT newavg(a) FROM t`), not just dump-fidelity. Tests: slice-405
      fixture/assertion in `TestPort_PgDumpConnectionSetup`. Gates: `go build
      ./...` clean; `go vet` analyzer/catalog/executor/initdb/parser/testport
      clean; `internal/analyzer`+`internal/catalog`+`internal/executor`+
      `internal/initdb`+`internal/parser`+`internal/planner`+`internal/server`
      suites PASS; full `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck
      Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook. Design doc updated
      (new "Slice 405" section). Deferred (ledger row appended): `pronamespace`
      hardcoded to `public` (`UserAggregate` has no `Schema` field); the 161
      built-in aggregates' `aggtransfn`/`aggfinalfn`/... still render raw
      numeric OIDs (not names) on a direct query (irrelevant to `pg_dump`,
      which never dumps pinned objects); restart persistence (not WAL-logged —
      same "round-trip lands first" split as TRANSFORM/CAST/CONVERSION/COLLATION).
      **2026-07-01 (loop #53): CAST WITH-FUNCTION arm + `resolveConversionFunc`
      wired to `catalog.LookupBuiltinProc` LANDED** — closes the loop #46
      ledger row's explicit resume point (the trivial CAST/CONVERSION
      follow-up to the TRANSFORM builtin-fallback wiring). Two curated
      builtins added (`age` OID 1386 timestamptz->interval;
      `iso8859_1_to_utf8` OID 4374, the 6-arg encoding-conversion signature).
      `resolveConversionFunc` falls back to the curated table (mirrors
      `resolveTransformFunc`'s identical guard); the CAST WITH-FUNCTION arm
      synthesizes a `*catalog.Routine` from the builtin so `validateCreateCast`
      still runs its full signature checks. Discovered and closed two
      previously-unported upstream DU-002 fixtures in the same loop —
      `'CREATE CAST FOR timestamptz'` (`age(timestamptz)`) and `'CREATE
      CONVERSION dump_test.test_conversion'` (`iso8859_1_to_utf8`) — now wired
      into `TestPort_PgDumpConnectionSetup`, byte-identical vs real pg_dump
      18.3. Tests: `TestResolveConversionFuncBuiltinFallback`,
      `TestCreateCastWithFunctionResolvesBuiltin`/
      `...RejectsBuiltinSignatureMismatch`, 2 new connsetup assertions. Gates:
      `go build ./...` clean; `go vet` catalog/executor/initdb/testport clean;
      `internal/catalog`+`internal/executor`+`internal/initdb` suites PASS;
      `TestPort_PgDumpConnectionSetup` (full suite) PASS; TPC-H spotcheck
      Q12=2/Q13=33 PASS. Deferred: the curated table is still name-only (no
      overload resolution) — see ledger row for the `map[string][]BuiltinProc`
      generalization resume point, not attempted since no current fixture
      needs a second overload of any curated name.
      **2026-07-01 (loop #56): CREATE/ALTER AGGREGATE restart persistence
      LANDED** — closes the slice-405 row's "restart persistence" deferral
      above, following the same "round-trip lands first, restart persistence
      follows separately" split already used for
      TRANSFORM/CAST/CONVERSION/COLLATION. New `wal.RecordKindCreateAggregate`
      (46) / `RecordKindAlterAggregateRename` (47) + `Encode`/`Decode` pairs
      (`internal/wal/recovery.go`); `catalog.InMemory.RegisterUserAggregateDuringRecovery`/
      `RenameUserAggregateDuringRecovery` (OID-preserving, `nextOID`-advancing,
      mirrors `RegisterCastDuringRecovery`); new
      `internal/initdb/aggregate_ddl_recovery.go` (`replayAggregateDDLRecords`)
      wired into `Open` right after `replayCollationDDLRecords`; `execCreateAggregate`/
      `execAlterAggregateRename` (`internal/executor/operators_ddl.go`) each
      append the corresponding WAL record. `ALTER AGGREGATE OWNER TO` has no
      wiring to persist because goopg's aggregate DDL surface never grew an
      OWNER TO arm (only RENAME TO, M0097-0035); `DROP AGGREGATE` is
      untouched — confirmed it is not wired to actually remove a registered
      user aggregate at all today (`operators_ddl.go`'s DROP AGGREGATE arm
      only validates args and always reports "does not exist", a pre-existing
      M0097-regress-era gap, unrelated to this slice). Tests:
      `internal/wal/aggregate_ddl_test.go` (Encode/Decode round-trip +
      truncated-payload guard), `internal/initdb/aggregate_ddl_recovery_test.go`
      (3 tests: create-replay, rename-after-create-replay, missing-wal-dir
      no-op) — full second-`Open` WAL-replay proof mirroring
      `collation_ddl_recovery_test.go`. Design doc updated (new "Slice 405
      follow-up" section in `0110-0001-pg-dump-tap-port.md`). Gates: `go build
      ./...` clean; `go vet` wal/catalog/initdb/executor clean; `internal/wal`
      (including `-race` on wal+mvcc) + `internal/catalog` + `internal/initdb`
      + `internal/executor` suites PASS; `TestPort_PgDumpConnectionSetup`
      PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit
      hook. Deferred (unchanged from slice 405, ledger row appended):
      `pronamespace` hardcoded to `public`; built-in aggregates'
      `aggtransfn`/... still render raw OIDs on direct query.
      **2026-07-01 (loop #57): DROP AGGREGATE wiring + restart persistence
      LANDED** — closes the loop #56 row's "DROP AGGREGATE is not wired"
      deferral. `internal/executor/operators_ddl.go`'s "DROP AGGREGATE" arm
      previously only validated arg types and always reported "does not
      exist", never touching `catalog.InMemory.userAggregates` at all (a
      pre-existing M0097-regress-era gap). Now wired end-to-end mirroring the
      CREATE/DROP COLLATION template: new `catalog.InMemory.DropUserAggregate`
      (mirrors `DropCollation`) + `DropUserAggregateDuringRecovery`; the DROP
      executor arm calls it before falling through to the existing
      "does not exist" error path and WAL-logs on success (name-only match,
      no overload resolution — same as every other aggregate DDL arm); new
      `wal.RecordKindDropAggregate` (48) with `Encode`/`DecodeDropAggregate`,
      same no-op physical-redo path as CREATE; `aggregate_ddl_recovery.go`
      gained a DROP replay case. Tests:
      `TestEncodeDecodeDropAggregateRoundTrip`,
      `TestDecodeDropAggregateRejectsTruncatedPayload` (wal),
      `TestAggregateDDLRecoveryReplaysDropAfterCreate` (initdb, full
      second-`Open` CREATE-then-DROP replay proof),
      `TestDDLDropAggregateRemovesUserAggregate` (executor — DROP actually
      removes the aggregate, re-DROP without IF EXISTS now raises 42883,
      IF EXISTS on missing is a no-op). Design doc updated (`0110-0001-pg-dump-
      tap-port.md` "DROP AGGREGATE wiring + restart persistence" subsection).
      Gates: `go build ./...` clean; `internal/wal` + `internal/catalog` +
      `internal/initdb` + `internal/executor` suites PASS;
      `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33
      PASS. Deferred (unchanged): `ALTER AGGREGATE ... OWNER TO` still has no
      DDL arm at all; slice 405 resume points (a) `pronamespace` hardcoded to
      `public` and (b) built-in-aggregate raw-OID rendering remain open.
      **2026-07-01 (loop #58): `ALTER AGGREGATE ... OWNER TO` wiring + restart
      persistence LANDED** — closes the loop #57 row's "no DDL arm at all"
      deferral. Previously this form fell into `parser/ddl.go`'s "other ALTER
      AGGREGATE forms: consume as no-op" branch, parsing to a bare
      `*AlterTableStmt{}` that silently discarded the target aggregate and new
      owner. Wired end-to-end mirroring `ALTER COLLATION ... OWNER TO`: new
      `catalog.UserAggregate.Owner` field + `OwnerOrDefault()` (zero value =
      bootstrap superuser, so pre-existing/replayed aggregates need no
      migration); `catalog.InMemory.SetUserAggregateOwner`/
      `SetUserAggregateOwnerDuringRecovery` (name-only match, not on the
      `Catalog` interface — same precedent as `SetCollationOwner`); new AST
      node `parser.AlterAggregateOwnerStmt` + parser `OWNER TO` branch
      (`CURRENT_USER`/`SESSION_USER`/`CURRENT_ROLE` → `"current_user"`
      sentinel); `executor.execAlterAggregateOwner` (42704 unknown role /
      42883 unknown aggregate) wired into the DDL dispatch switch + planner
      passthrough; `pg_proc_view.go`'s aggregate `proowner` column now reads
      `agg.OwnerOrDefault()` instead of the loop #55 hardcoded `"10"` literal;
      new `wal.RecordKindAlterAggregateOwner` (49) +
      `Encode`/`DecodeAlterAggregateOwner` (schema-less, unlike
      `RecordKindAlterCollationOwner` — aggregates still have no `Schema`
      field) + `aggregate_ddl_recovery.go` replay case. Also fixed, as a
      byproduct: `internal/server/dispatch.go`'s `ddlTag` had no case for
      `CreateAggregateStmt`/`AlterAggregateRenameStmt` at all (every aggregate
      DDL statement's command tag fell through to the generic `"OK"` — a
      pre-existing, unrelated gap); added `"CREATE AGGREGATE"`/
      `"ALTER AGGREGATE"` cases since the new `AlterAggregateOwnerStmt` needed
      one anyway. Tests:
      `internal/parser/alter_aggregate_owner_test.go`
      (`TestParseAlterAggregateOwner`, `TestParseAlterAggregateRenameStillWorks`),
      `internal/executor/storage_ddl_test.go` (`TestDDLAlterAggregateOwner`),
      `internal/wal/aggregate_ddl_test.go`
      (`TestEncodeDecodeAlterAggregateOwnerRoundTrip`,
      `TestDecodeAlterAggregateOwnerRejectsTruncatedPayload`),
      `internal/initdb/aggregate_ddl_recovery_test.go`
      (`TestAggregateDDLRecoveryReplaysOwnerAfterCreate` — full second-`Open`
      CREATE-then-OWNER replay proof, OID preserved). Gates: `go build ./...`
      clean; `go vet` wal/catalog/executor/initdb/parser/planner/server
      clean; `go test -race ./internal/wal/... ./internal/mvcc/...` clean;
      `internal/catalog`+`internal/executor`+`internal/initdb`+
      `internal/parser`+`internal/planner`+`internal/server` suites PASS;
      `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33
      PASS. Design doc updated (`0110-0001-pg-dump-tap-port.md` "`ALTER
      AGGREGATE ... OWNER TO` wiring + restart persistence" subsection).
      Deferred (unchanged, ledger row appended): slice 405 resume points (a)
      `pronamespace` hardcoded to `public` and (b) built-in-aggregate raw-OID
      rendering remain open; no `pg_dump` fixture yet exercises a non-default
      aggregate owner (untested end-to-end dump round-trip, though the
      renderer/catalog plumbing is in place).
      **2026-07-01 (loop #59): `ALTER AGGREGATE ... OWNER TO` pg_dump fixture
      LANDED** — closes the loop #58 row's resume point (a) ("no `pg_dump`
      fixture yet exercises a non-default aggregate owner"). New setup SQL in
      `TestPort_PgDumpConnectionSetup` (`CREATE ROLE agg_owner_role` +
      `ALTER AGGREGATE public.newavg(int4) OWNER TO agg_owner_role`) plus an
      assertion that a real pg_dump 18.3 run emits `ALTER AGGREGATE
      public.newavg(integer) OWNER TO agg_owner_role;` — byte-identical,
      confirming `dumpAgg`'s `pg_proc.proowner` read + `_getObjectDescription`'s
      AGGREGATE-as-FUNCTION/OPERATOR OWNER-TO-target derivation
      (`pg_backup_archiver.c`) against the loop #58 `execAlterAggregateOwner`/
      `pg_proc_view.go` `OwnerOrDefault()` wiring — no new engine code needed,
      pure verification. Design doc updated (new "`ALTER AGGREGATE ... OWNER
      TO` pg_dump fixture" subsection in `0110-0001-pg-dump-tap-port.md`).
      Gates: `go build ./...` clean; `go vet ./internal/testport/...` clean;
      `TestPort_PgDumpConnectionSetup` PASS; `internal/catalog`+
      `internal/executor`+`internal/parser` suites PASS; TPC-H spotcheck
      Q12/Q13. Deferred (unchanged, ledger row appended): slice 405 resume
      point (b) — `pronamespace` hardcoded to `public` and built-in-aggregate
      raw-OID rendering remain open.
      **2026-07-01 (loop #60): `UserAggregate.NamespaceOID` LANDED — closes
      slice 405 resume point (a)** ("`pronamespace` hardcoded to `public`").
      `catalog.UserAggregate` gains `NamespaceOID`/`NamespaceOIDOrDefault()`
      (mirrors `Owner`/`OwnerOrDefault()`); `execCreateAggregate` resolves
      `s.Name.Schema` via `Catalog.SchemaOID` (unknown→public fallback, same
      as `execCreateCollation`) and sets it before `RegisterUserAggregate`;
      `pg_proc_view.go`'s aggregate row now renders `NamespaceOIDOrDefault()`
      instead of the literal `"2200"`. Restart persistence:
      `wal.Encode/DecodeCreateAggregate` gained a `schema` name field
      (mirrors `EncodeCreateCollation`); `RegisterUserAggregateDuringRecovery`
      now takes `schema` and resolves `NamespaceOID` against the catalog's
      schema registry like `CreateCollationDuringRecovery` does
      (`replayAggregateDDLRecords` already ran after `replaySchemaDDLRecords`
      in `open.go`, so no reordering was needed). New tests:
      `TestEncodeDecodeCreateAggregateRoundTrip` non-public-schema case;
      `TestAggregateDDLRecoveryReplaysNonPublicSchema` (CREATE SCHEMA +
      CREATE AGGREGATE WAL replay proof); `TestPort_PgDumpConnectionSetup`
      fixture `CREATE AGGREGATE s.schemedavg(...)` asserting a
      schema-qualified dump (`dumpAgg` always schema-qualifies —
      `pg_dump.c:15492` — so this couldn't hide behind a public-schema
      default). Design doc updated (new "`UserAggregate.NamespaceOID` —
      closes slice 405 resume point (a)" subsection in
      `0110-0001-pg-dump-tap-port.md`). Gates: `go build`/`go vet` clean;
      `TestPort_PgDumpConnectionSetup` full-suite PASS; `internal/wal`+
      `internal/initdb` targeted `Aggregate` PASS; `-race -count=1` on
      `internal/wal`+`internal/mvcc`; `internal/catalog`+`internal/executor`+
      `internal/parser`+`internal/initdb` suites PASS; TPC-H spotcheck
      Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook. Deferred
      (unchanged, ledger row appended): slice 405 resume point (b) — built-in
      aggregates' `aggtransfn`/`aggfinalfn`/... still render raw numeric OIDs
      on a direct `pg_aggregate`/`pg_proc` query (needs a curated reverse
      OID→proc-name table); no current fixture reads these columns directly.
      Also unaffected: `RENAME`/`OWNER TO`/`DROP AGGREGATE` still resolve by
      bare name only (aggregates are a single global map, not schema-scoped
      like `userCollations`) — a pre-existing simplification, out of scope
      here.
      **2026-07-01 (loop #61): built-in `pg_aggregate` regproc columns now
      render function names — closes slice 405 resume point (b).**
      `registerPgAggregateView`'s 161 BKI rows rendered
      `aggtransfn`/`aggfinalfn`/`aggcombinefn`/`aggserialfn`/`aggdeserialfn`/
      `aggmtransfn`/`aggminvtransfn`/`aggmfinalfn` as bare numeric OID text;
      real PG's `regprocout` always renders these as the function name on a
      direct query (e.g. `SELECT aggtransfn FROM pg_aggregate` →
      `int8_avg_accum`, not `2746`), matching what user-defined aggregates
      already did (`aggFuncNameOrDash`). New `pgProcNameForOID`
      (`internal/initdb/pg_aggregate_view.go`) indexes the already
      machine-generated `pgProcAllEntries()` (3397-row PG18 `pg_proc.dat`)
      by OID via a lazy `sync.Once`-cached map — reusing existing generated
      data instead of hand-curating a second ~161-entry table — wrapped by
      `aggBuiltinFuncName` (OID 0 → `"-"`, mirrors the existing
      `aggFuncNameOrDash` convention). No planner change needed:
      `TypedVirtualCell` already falls a non-numeric `regproc` cell through
      to `StringConst`. `aggfnoid` (the join key) stays numeric/untouched.
      New tests (`internal/initdb/pg_aggregate_view_test.go`):
      `TestAggBuiltinFuncNameInvalidOidRendersDash`,
      `TestAggBuiltinFuncNameResolvesKnownOID`,
      `TestPgAggregateBKIRegprocColumnsAllResolveToNames` (guards ALL 161
      BKI rows' non-zero regproc OIDs resolve to real names, not a numeric
      fallback), `TestRegisterPgAggregateViewRendersBuiltinFuncNames`
      (end-to-end `VirtualRows()` check on avg(int8)). Design doc updated
      (new "Built-in `pg_aggregate` regproc columns render names — closes
      slice 405 resume point (b)" subsection in
      `0110-0001-pg-dump-tap-port.md`). Gates: `go build`/`go vet` clean;
      new tests PASS; `TestPort_PgDumpConnectionSetup` full-suite PASS
      (unaffected — `dumpAgg` never reads these columns directly);
      `internal/catalog`+`internal/executor`+`internal/parser`+
      `internal/initdb` suites PASS; TPC-H spotcheck Q12=2/Q13=33 PASS;
      pgbench smoke = pre-commit hook. Deferred (ledger row appended): goopg
      still has **no general OID→name resolution for `regproc`-typed
      columns at query-output time** — `pg_type.typinput`/`typoutput`/...,
      `pg_operator.oprcode`/`oprrest`/`oprjoin`, `pg_am.amproc` all still
      render raw OIDs on a direct SELECT (no `case "regproc"` in either
      `internal/server/dispatch.go`'s or `dispatch_extended.go`'s
      per-column-type text-formatting switch, unlike the existing
      `regclass` case in `dispatch.go`). Also newly confirmed (pre-existing,
      unrelated to this fix): `dispatch_extended.go`'s type-formatting
      switch is missing several cases `dispatch.go` has
      (`regclass`/`date`/`time`/`bytea`).
      **2026-07-01 (loop #33, design 0119-0004-create-operator-roundtrip.md
      "Loop #33"): `ALTER OPERATOR name (left_type, right_type) SET (...)`
      LANDED — closes the DU-002 slice 407 ledger follow-up.** `ALTER
      OPERATOR` previously fell into `parseAlter`'s generic
      `ALTER VIEW/SCHEMA/COLLATION/.../OPERATOR/...` compat-stub loop and was
      swallowed as a total no-op — a user-written
      `ALTER OPERATOR foo(int,int) SET (RESTRICT = ...)` silently did
      nothing. New branch in `parseAlter` (checked before the generic stub):
      `ALTER OPERATOR CLASS|FAMILY` and any non-`SET(...)` tail (`OWNER TO`/
      `SET SCHEMA`/other) still fall back to the prior no-op; only
      `SET ( option = value, ... )` produces a new `AlterOperatorSetStmt`
      AST node, reusing `parseOperatorRefName` (COMMUTATOR/NEGATOR) and the
      existing bare/`=value` MERGES/HASHES scan; LEFTARG/RIGHTARG/FUNCTION/
      PROCEDURE inside SET raise a syntax error (immutable after CREATE).
      Added to the planner's DDL-passthrough case list. New
      `execAlterOperatorSet` (`internal/executor/operators_ddl.go`) mirrors
      `AlterOperator`'s (`operatorcmds.c`) exact semantics: RESTRICT/JOIN
      change freely (incl. clearing via `= NONE`); COMMUTATOR/NEGATOR/
      MERGES/HASHES may only be **set** if not already set (same-value
      restatement is a no-op, a different value is 42P13, self-negation
      rejected); not-found is 42883. CREATE OPERATOR's inline RESTRICT/JOIN-
      resolution and COMMUTATOR/NEGATOR two-pass-resolution closures were
      extracted into shared `(*ddlOp).resolveOperatorSupportFunc`/
      `resolveOperatorRef` methods (zero behavior change — full pre-existing
      CREATE OPERATOR suite passed unchanged) so CREATE and ALTER share one
      resolution path instead of risking future divergence. New builtins
      `eqsel`/`eqjoinsel`/`neqjoinsel` (OIDs 101/105/106, PG's own `=`
      operator's oprrest/oprjoin) curated in `builtinProcsByName` so a
      RESTRICT=/JOIN= test fixture resolves to a real OID. Tests:
      `TestParseAlterOperatorSet`/`TestParseAlterOperatorSetRestrictNone`/
      `TestParseAlterOperatorSetImmutableAttr`/
      `TestParseAlterOperatorOwnerToIsNoop` (parser);
      `TestAlterOperatorSetRestrictJoin`/
      `TestAlterOperatorSetCommutatorNegatorOnceOnly`/
      `TestAlterOperatorSetMergesHashesOnceOnly`/
      `TestAlterOperatorSetMissingOperator` (executor). Gates: `go build
      ./...` clean; `go vet` parser/catalog/executor/planner clean;
      `internal/parser`+`internal/catalog`+`internal/executor`+
      `internal/planner` suites PASS; `TestPort_PgDumpConnectionSetup` PASS
      (confirms zero pg_dump regression); TPC-H spotcheck Q12=2/Q13=33
      PASS; pgbench smoke = pre-commit hook. Not exercised by any pg_dump
      TAP fixture — pg_dump never emits this statement (everything a dump
      needs is captured by CREATE OPERATOR's own forward-reference shell
      mechanism); this closes real DDL semantics, not a dump-parity slice.
      Deferred (ledger row appended, `resolved` status): operator ownership
      is not enforced (no `object_ownercheck` equivalent, consistent with
      every other operator DDL arm); `regoper`/`regoperator` OID→name
      resolution remains open (unchanged, unrelated).
      **2026-07-01 (loop #34, design `0119-0004-create-operator-roundtrip.md`
      "Loop #34"): `CREATE OPERATOR FAMILY name USING method` LANDED —
      DU-002 slice 408, a new object family.** `CREATE OPERATOR FAMILY` had
      no parse path at all before this loop (`pg_opfamily.VirtualRows` was
      hardcoded `nil`), so pg_dump's `getOpfamilies` always read 0 rows.
      Matches upstream's own bare `002_pg_dump.pl` `op_family` fixture — PG's
      grammar has no `AS` clause here (unlike CREATE OPERATOR CLASS); the
      family starts empty, members added later via a separate `ALTER
      OPERATOR FAMILY ... ADD` (not implemented — deferred). New parser
      `parseCreateOpFamilyTail` (`[schema.]name USING method`) stashes the
      method on `CompatNoopStmt.OpFamilyMethod`; new catalog
      `UserOperatorFamily` + `RegisterUserOperatorFamily`/
      `DropUserOperatorFamily`/`ListUserOperatorFamilies` (keyed
      `"<schema>.<name>/<method-oid>"`, since PG scopes opfamily uniqueness
      per namespace+access-method) + `AccessMethodOIDByName`;
      `pg_opfamily.VirtualRows` now renders the registry; new
      `execCompatNoop` `case "operator family":` resolves method (42704 if
      unrecognized) + namespace and registers. No planner change needed.
      Tests: `TestParseCreateOperatorFamily`/
      `TestParseCreateOperatorFamilyUnqualified`/
      `TestParseCreateOperatorClassStillWorks` (parser);
      `TestCreateOperatorFamily`/`TestCreateOperatorFamilyIdempotent`/
      `TestCreateOperatorFamilyUnknownMethod` (executor); new DU-002 slice
      408 assertions in `TestPort_PgDumpConnectionSetup` (byte-identical
      CREATE + OWNER TO vs live PG 18.3, plus a negative check that no
      spurious `ALTER OPERATOR FAMILY ... ADD` line appears for an empty
      family). Gates: `go build ./...` clean; `go vet`
      parser/catalog/executor/planner clean; `internal/parser`+
      `internal/catalog`+`internal/executor`+`internal/planner` suites PASS;
      `gofmt -l` flags only the same pre-existing go1.25/1.26-drift files as
      loop #33 (verified via `git stash`); TPC-H spotcheck Q12=2/Q13=33
      PASS; pgbench smoke = pre-commit hook. Deferred (ledger row appended):
      `ALTER OPERATOR FAMILY ... ADD` (loose OPERATOR/FUNCTION members) not
      implemented; full `CREATE OPERATOR CLASS` round-trip (still the
      pre-existing M0097-0027 minimal stub — does not populate `pg_opclass`)
      + the `op_class_custom` ordering fixture (range-type
      `subtype_opclass` binding) remain a separate, larger follow-up.
      **2026-07-01 (loop #35, design `0119-0004-create-operator-roundtrip.md`
      "Loop #35"): `CREATE OPERATOR CLASS` populates a real `pg_opclass` row
      LANDED — DU-002 slice 409, partially closes the loop #34 resume point
      (b).** Bounded to upstream's own `op_class_empty` fixture (`FOR TYPE
      bigint USING btree FAMILY dump_test.op_family AS STORAGE bigint` — no
      OPERATOR/FUNCTION members). `CREATE OPERATOR CLASS` was the pre-existing
      M0097-0027 minimal stub (tracked only `FUNCTION 2` hash-extended support
      func + a bare schema association), never touching `pg_opclass` —
      `getOpclasses` always read 0 rows. Parser: `parseCreateOpClassTail` now
      parses a schema-qualified name, captures `DEFAULT`/`USING method`
      (previously discarded), a new optional `FAMILY family_name` clause, and
      a `STORAGE type` AS-list entry. Catalog: new `UserOperatorClass`
      (OID/Name/NamespaceOID/Owner/Method/FamilyOID/InTypeOID/IsDefault/
      KeyTypeOID) + `RegisterUserOperatorClass`/`DropUserOperatorClass`/
      `ListUserOperatorClasses` (keyed `"<schema>.<name>/<method-oid>"`,
      mirroring `userOpFamilyKey`); new `LookupUserOperatorFamily` resolves an
      explicit FAMILY clause; `pg_opclass.VirtualRows` now renders the
      registry. Executor: `execCreateOpClass` resolves method (42704 if
      unrecognized)/namespace/intype/storage-type; an explicit FAMILY clause
      must already exist (42704 if not); an omitted FAMILY clause
      auto-creates an anonymous family sharing the class's schema+name (PG's
      `DefineOpClass` — `opcfamily` is NOT NULL); `DROP OPERATOR CLASS` now
      also calls `DropUserOperatorClass`. Verified byte-identical vs a
      freshly-built, live PG 18.3 instance run in this loop. Tests:
      `TestParseCreateOperatorClassFullShape`/
      `TestParseCreateOperatorClassDefaultKeyword` (parser);
      `TestCreateOperatorClassPopulatesOpclassRow`/
      `TestCreateOperatorClassAutoCreatesFamily`/
      `TestCreateOperatorClassUnknownFamily` (executor); DU-002 slice 409
      (`TestPort_PgDumpConnectionSetup`). Gates: `go build`/`go vet` clean;
      `internal/parser`+`internal/catalog`+`internal/executor`+
      `internal/planner` suites PASS; `gofmt -l` flags only the same
      pre-existing go1.25/1.26-drift files as loop #34 (verified via `git
      stash`); TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit
      hook. Deferred (ledger row appended, still open): `OPERATOR`/`FUNCTION`
      class members are not tied to a `pg_amop`/`pg_amproc` member store —
      the SAME underlying gap as the still-open `ALTER OPERATOR FAMILY ...
      ADD` resume point, since `dumpOpclass`/`dumpOpfamily` read the identical
      `pg_amop`/`pg_amproc`-via-`pg_depend` shape (now a combined follow-up);
      `op_class_custom` additionally needs a range-type `subtype_opclass`
      binding; `KeyTypeOID == 0` → `"-"` regtype rendering is unverified (no
      fixture omits STORAGE yet).
      **2026-07-01 (loop #36): `::regprocedure` output-formatting prerequisite
      LANDED — DU-002 slice 410, one prerequisite of the slice 408/409
      pg_amop/pg_amproc member-store follow-up (member store itself still
      NOT implemented).** While scoping that follow-up, found `dumpOpclass`/
      `dumpOpfamily` cast `pg_amproc.amproc::pg_catalog.regprocedure`, and
      goopg's `::regprocedure` cast (`executor/expr.go`) and direct
      regprocedure-typed column rendering (`server/dispatch.go`
      `appendTypedCellText`) both rendered the SAME bare name as `::regproc`
      — missing PG's `format_procedure`/`regprocedureout` argument-type-list
      suffix (`name(argtype1,argtype2)`), which would have made any FUNCTION
      member's dumped signature wrong regardless of member-store completion.
      Fixed via new `catalog.RegprocedureName` (builtin via a newly generated
      `pgProcArgTypeNamesByOID` OID->raw-argtype-name leaf index —
      `cmd/gen-pg-proc-data -names` extended to also parse+emit
      `proargtypes`'s typname tokens verbatim — or a CREATE FUNCTION via the
      live `Routines` registry, filtering OUT-mode params) +
      `pgArgTypeDisplayAlias` (int4->integer, bool->boolean, etc., mirroring
      `format_type_be`'s base-type display aliases; duplicated from
      executor's `pgFormatTypeName` since catalog cannot import executor).
      Wired at both sibling call sites so they stay in sync. Tests:
      `TestRegprocedureName` (catalog, builtin + user-routine + OUT-param
      filtering); updated `TestRegprocOIDCastResolvesName` and
      `TestAppendTypedCellTextRegprocRendersName` (previously pinned the
      bare-name no-op as correct; now pin the full signature — e.g.
      `43::regprocedure::text` = `"int4out(integer)"` not `"int4out"`).
      Gates: `go build ./...` clean; `go vet` catalog/executor/server/cmd
      clean; `internal/catalog`+`internal/executor`+`internal/server` suites
      PASS; `gofmt -l` flags only the same pre-existing go1.25/1.26-drift
      files as loop #35 (verified via `git stash`) plus the freshly
      regenerated `pg_proc_names_generated.go` (gofmt -w'd once, safe since
      the whole file is machine-generated output being replaced, not
      hand-edited unrelated code); `TestPort_PgDumpConnectionSetup` PASS (zero
      pg_dump regression — no table declares a regprocedure-typed column with
      actual row data in that fixture); TPC-H spotcheck Q12=2/Q13=33 PASS;
      pgbench smoke = pre-commit hook. Deferred (ledger row appended, still
      open): the pg_amop/pg_amproc + pg_depend member store itself (slice
      408/409's actual follow-up) remains unimplemented; a SEPARATE, larger
      prerequisite also surfaced — goopg has no builtin-operator catalog at
      all (`pg_operator` VirtualRows renders only user-defined operators), so
      `regoper`/`regoperator` resolution for a BUILTIN operator (needed by
      `dumpOpclass`'s `amopopr::pg_catalog.regoperator` cast) is a second,
      independent gap blocking the exact upstream `op_family`/`op_class`
      fixtures (which reference real builtin cross-type btree operators).
      **2026-07-01 (loop #37, design `0119-0004-create-operator-roundtrip.md`
      "Loop #37"): pg_amop/pg_amproc member store LANDED — DU-002 slice 411,
      closes the slice 408/409 follow-up (member store itself).** Parser's
      `parseCreateOpClassTail` now captures full `OPERATOR`/`FUNCTION`
      AS-list entries (`parser.OpClassMember`) via new
      `parseOperatorRefName`/`parseTypeNameAfterCast` helpers, instead of
      skip-only parsing. Catalog gains `AmOpMember`/`AmProcMember` +
      `RegisterAmOpMember`/`RegisterAmProcMember`/`ListAmOpMembers`/
      `ListAmProcMembers`; `pg_amop`/`pg_amproc.VirtualRows` render the
      registry; `dependVirtualRows` appends the matching 2 `pg_depend` rows
      per member (opclass owns operator/function, mirroring PG's
      `AddSubDependency` from `DefineOpClass`); `DropUserOperatorClass`
      cascades member cleanup. `execCreateOpClass` resolves each member via
      new `resolveOpClassOperator`/`resolveOpClassFunction` (custom
      user-defined operators/functions only — builtins still blocked, see
      below) and calls `registerOpClassMembers`. Two adjacent bugs fixed
      while live-diffing against PG 18.3: `::regtype` rendered `InvalidOid`
      as `"0"` instead of `"-"` (catalog.go + expr.go, both branches); new
      `regoper`/`regoperator` `CastExpr` support (new
      `catalog.RegoperatorName`/`LookupUserOperatorByName`, mirroring
      slice 410's `RegprocedureName` shape — `name(lefttype,righttype)`).
      Verified byte-identical against a live PG 18.3 instance for a
      custom-operator/custom-function opclass round trip (`CREATE OPERATOR
      public.~=~`; `CREATE OPERATOR CLASS ... AS OPERATOR 1 ~=~(int4,int4),
      FUNCTION 1 btint4cmp(int4,int4)`). Tests: 3 new executor tests in
      `create_operator_test.go` (member registration, pg_amop/pg_amproc
      rendering, pg_depend rows). Gates: `go build ./...`/`go vet` clean;
      `internal/catalog`+`internal/executor`+`internal/parser`+
      `internal/server`+`internal/planner`+`internal/initdb` suites PASS;
      `TestPort_PgDumpConnectionSetup` PASS; `gofmt -l` flags only the same
      pre-existing go1.25/1.26-drift files as loop #36 (verified via `git
      stash`); TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke =
      pre-commit hook. Deferred (ledger row appended, still open): `FOR
      ORDER BY` sort-family clause is parsed and discarded (no sort-family
      member kind); the builtin-operator-catalog gap (loop #36's finding)
      still blocks resolving BUILTIN `OPERATOR`/`FUNCTION` references in
      realistic (non-custom-operator) opclass fixtures — this is now the
      single largest remaining blocker for the upstream `op_family`/
      `op_class` fixtures; NEW finding — `regoperator`/`regprocedure` never
      schema-qualify their rendered name (PG's pg_dump connection always
      runs with `search_path=''`, forcing `format_operator`/
      `format_procedure` to always qualify; goopg's renderers don't), a
      small isolated fix (unconditional `schema.` prefix via an OID→schema
      lookup) tracked separately from the builtin-catalog work.
      **2026-07-01 (loop #38, design `0119-0004-create-operator-roundtrip.md`
      "Loop #38"): the schema-qualification gap LANDED — DU-002 slice 412,
      closes the loop #37 finding above.** Re-reading PG's
      `format_operator_extended`/`format_procedure_extended` (`regproc.c`)
      before implementing found that loop #37's proposed "unconditionally
      qualify" fix was wrong in one case: `pg_catalog` is always implicitly
      searched regardless of `search_path`'s content, so a builtin
      FUNCTION/OPERATOR reference (e.g. `dumpOpclass`'s own `btint4cmp`)
      must stay bare even under pg_dump's `search_path=''`. New
      `catalog.RegprocedureNameAndSchema`/
      `(*InMemory).RegoperatorNameAndSchema` resolve each object's schema
      (`pg_catalog` for a builtin; the routine's/operator's declared
      namespace, default `public`, for a user-defined one — with a
      `PublicNamespaceOID` special-case, since `NewInMemory`'s `schemas` map
      aliases both `public` and `pg_toast` to OID 2200 and a generic
      `SchemaNameForOID` reverse lookup nondeterministically picks either);
      new `executor.regObjectSchemaVisible` mirrors the real visibility rule
      and gates qualification in both `regprocedure`/`regoperator`
      `CastExpr` branches (`internal/executor/expr.go`), reusing the
      pre-existing `searchPathSchemas` effective-search-path resolver.
      Verified byte-identical against a live PG 18.3 instance: the loop #37
      fixture now dumps `OPERATOR 1 public.~=~(integer,integer)` (qualified)
      alongside `FUNCTION 1 (integer, integer) btint4cmp(integer,integer)`
      (bare). Tests: `TestRegprocedureRegoperatorSchemaQualification`
      (executor). Gates: `go build ./...`/`go vet` clean;
      `internal/catalog`+`internal/executor`+`internal/parser`+
      `internal/server`+`internal/planner` suites PASS (repeated runs to
      confirm a map-iteration flake this loop found and fixed is gone);
      `TestPort_PgDumpConnectionSetup` PASS; `gofmt -l` flags only
      pre-existing go1.25/1.26 comment-smart-quote drift outside this loop's
      edited line ranges; live PG 18.3 diff above; TPC-H spotcheck
      Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook. Deferred (ledger
      row appended, unchanged): `FOR ORDER BY` sort-family resolution; the
      builtin-operator catalog, now the single largest remaining blocker
      for a realistic upstream `op_family`/`op_class` fixture.
      **2026-07-01 (loop #39, design `0119-0004-create-operator-roundtrip.md`
      "Loop #39"): a curated builtin-operator catalog LANDED — DU-002 slice
      413, closes the loop #37/#38 "builtin-operator catalog" finding for
      the exact upstream `op_class` fixture.** New `catalog.BuiltinOperator`
      + `builtinOperatorsByKey`/`builtinOperatorsByOID` +
      `LookupBuiltinOperator`/`LookupBuiltinOperatorByOID`
      (`internal/catalog/catalog.go`, mirroring `builtinProcsByName`'s
      hand-curated-not-full-port pattern — the full 799-row
      `pg_operator.dat` data already exists via
      `internal/initdb/pg_operator_seed_data.go`/`cmd/gen-pg-operator-data`
      for the PG18-standby heap-fidelity bootstrap, but that package can't
      be imported from `internal/catalog`/`internal/executor`, mirroring
      the pre-existing `pg_proc` heap-bootstrap-vs-leaf-name-index split).
      Curated: the 5 int8 btree comparison strategies (`pg_operator.dat`
      oids 410/412/413/414/415) plus `btint8cmp` (oid 842, added to
      `builtinProcsByName`) — exactly what the upstream `op_class` fixture's
      `OPERATOR`/`FUNCTION 1` entries need. `resolveOpClassOperator`'s typed
      branch (`internal/executor/operators_ddl.go`) and
      `catalog.RegoperatorNameAndSchema` + the bare-name `regoper` CastExpr
      (`internal/executor/expr.go`) gain a builtin fallback, mirroring
      `resolveOpClassFunction`'s pre-existing `LookupBuiltinProc` fallback.
      **A second, independent bug found via the live-PG-18.3 diff (not part
      of the builtin-catalog feature, but directly exposed while verifying
      it):** `execCreateOpClass`'s `keyTypeOID` never reset to `InvalidOid`
      when `STORAGE` names the same type as the class's own `FOR TYPE` —
      real PG does (`opclasscmds.c` `DefineOpClass`: `if (storageoid ==
      typeoid) storageoid = InvalidOid`) — so a members-bearing class
      declaring a redundant `STORAGE` clause (like `op_class`'s own
      `AS STORAGE bigint, OPERATOR 1 ...`) spuriously dumped a leading
      `STORAGE bigint ,` line real PG's `dumpOpclass` never prints. Fixed;
      `op_class_empty` (no members) is unaffected on observable output
      because pg_dump's own client-side "dummy STORAGE clause" fallback
      re-adds the same text through a different branch when the AS-list
      would otherwise render empty. `TestCreateOperatorClassPopulatesOpclassRow`'s
      stale `opckeytype` assertion (previously pinning the un-reset value)
      corrected to `0`, with a comment explaining why. Verified against a
      freshly-built, live PG 18.3 instance: the exact upstream
      `op_class`/`op_class_empty` fixture pair (schema renamed to `public`)
      dumps byte-identical on both engines, including the upstream test's
      own documented omission of `btint8sortsupport`/`btequalimage`
      (deliberately not curated, so `resolveOpClassFunction` silently drops
      them — same mechanism as every other unresolvable builtin). Tests:
      `TestLookupBuiltinOperator`/`TestLookupBuiltinOperatorByOID`/
      `TestRegoperatorNameAndSchemaBuiltinFallback`
      (`internal/catalog/builtin_operator_test.go`);
      `TestCreateOperatorClassMembersResolveBuiltinOperators` (executor,
      `create_operator_test.go`, ports the upstream fixture verbatim).
      Gates: `go build ./...`/`go vet` clean;
      `internal/catalog`+`internal/executor`+`internal/parser`+
      `internal/server`+`internal/planner` suites PASS;
      `TestPort_PgDumpConnectionSetup` PASS; live PG 18.3 diff above; TPC-H
      spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook.
      Deferred (ledger row appended, narrowed in scope): the
      builtin-operator catalog is still only a 5-row curated slice, not a
      full `pg_operator.dat` port — the next fixture referencing a
      different builtin operator needs its own curated addition until a
      generated leaf-package index exists; `FOR ORDER BY` sort-family
      resolution remains open and unexercised by any fixture in scope.
      **2026-07-01 (loop #45, design `0119-0004-create-operator-roundtrip.md`
      "Loop #45"): per-AM `amadjustmembers` dependency-strength policy for
      GiST/SP-GiST LANDED — closes the loop #40 ledger row's own resume
      point, flagged in the loop #44 working-set carry as the largest
      remaining structural gap for a real GiST/SP-GiST opclass to round-trip
      through pg_dump.** `dependVirtualRows` (`internal/catalog/catalog.go`)
      previously decided every `AmOpMember`/`AmProcMember`'s pg_depend
      hardness purely from `ClassOID == 0` (loose vs class-attributed), with
      no per-AM distinction — real PG's `gistadjustmembers`/
      `spgadjustmembers` (`gistvalidate.c`/`spgvalidate.c`) force EVERY
      OPERATOR member of a GiST/SP-GiST opfamily to a soft/family-level
      dependency regardless of class-attribution, and force every *optional*
      FUNCTION member (outside the AM's own required-support-proc set) soft
      too. New `amForcesSoftOperatorDependency`/`amForcesSoftFunctionDependency`
      + `gistRequiredSupportProcs`/`spgistRequiredSupportProcs` tables
      (`internal/catalog/catalog.go`, values read off `gist.h`/`spgist.h`);
      `AmProcMember` gains a `Method` field (mirroring `AmOpMember.Method`)
      so the FUNCTION-side check has the owning AM without a family-OID
      indirection; `dependVirtualRows`'s two member loops extend their
      existing `ClassOID == 0` soft-dependency guard with an `||
      amForcesSoft*Dependency(...)` clause — no other code changed since
      everything downstream already branched on the same three-variable
      dependency-shape state introduced in slice 411/415. **Regression
      correction, not just addition:** this flips
      `TestCreateOperatorClassForOrderBySortFamily` (loop #40)'s stale `"n"`
      (NORMAL) assertion for a `USING gist` FOR-ORDER-BY member's sort-family
      pg_depend row to the real-PG-correct `"a"` (AUTO) — that test's
      pre-fix expectation was always wrong per `DefineOpClass` calling
      `amadjustmembers` unconditionally before `storeOperators`, just never
      exercised end-to-end until now. New
      `TestCreateOperatorClassGistMembersGetSoftDependencies` (executor)
      proves the 3-way split (OPERATOR always soft; required FUNCTION hard;
      optional FUNCTION soft) plus a negative assertion. **Live PG 18.3
      end-to-end proof** (two side-by-side servers — goopg on
      `tmp/perf-optimize` port 5533, a genuinely fresh `initdb`-created real
      PostgreSQL 18.3 on port 5534 — identical DDL, same real `pg_dump`
      binary against both): `CREATE OPERATOR CLASS ... AS FUNCTION 1
      (integer, integer) int4eq(integer,integer);` (only the required
      FUNCTION 1 inline) plus `ALTER OPERATOR FAMILY ... USING gist ADD
      OPERATOR 1 public.~=~(integer,integer) , FUNCTION 3 (integer, integer)
      int4eq(integer,integer);` byte-identical on both engines (only the
      cross-object dump *ordering* differs, a pre-existing, unrelated,
      separately-scoped gap). Notably the OPERATOR/optional-FUNCTION
      round-trip needed **zero new dump-side code** — real pg_dump's
      `dumpOpfamily` query is unconditional on how a pg_depend row was
      created, so correcting the row's `refclassid` alone made it visible
      via the loop #41 `ALTER OPERATOR FAMILY ADD` machinery. Gates: `go
      build ./...`/`go vet` clean; `internal/catalog`+`internal/executor`+
      `internal/parser`+`internal/planner`+`internal/server` suites PASS;
      `TestPort_PgDumpConnectionSetup` PASS (unaffected — no automated
      fixture in that suite exercises a gist/spgist opclass); live PG 18.3
      diff above; TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke =
      pre-commit hook; `gofmt -l` flags only the same pre-existing
      go1.25/1.26-drift files as every prior loop in this chain (verified
      via `git stash`). Design doc + ledger row appended. Deferred: dump
      object-ordering (goopg has no dependency-graph topological sort at
      all, unrelated to opclasses specifically); btree/hash's own
      `amadjustmembers` cross-type-driven rule (no fixture forces it — every
      existing btree opclass fixture is same-type); the builtin-operator
      catalog / `op_class_custom` / ALTER OPERATOR FAMILY DROP
      cascade-semantics gaps remain unchanged from loops #39/#41/#43.
      **2026-07-02 (loop #54, design `0119-0004-create-operator-roundtrip.md`
      "Loop #54"): `CREATE FOREIGN TABLE ... SERVER ... OPTIONS (...)`
      round-trip LANDED — DU-002 slice 417, closes the `pg_foreign_table`
      gap open since M0110-0001 (view hardcoded to `func() [][]string {
      return nil }`).** `CreateTableStmt` gains `ForeignServer`/
      `ForeignOptions` (`internal/parser/ast.go`); `parseCreateForeignTableTail`
      (`internal/parser/ddl.go`) now captures `SERVER name` and an optional
      table-level `OPTIONS (...)` (reusing the pre-existing
      `scanFDWOptionsList` helper `CREATE SERVER`/`CREATE USER MAPPING`
      already use) instead of skipping to `;`; `parseColumnDef` also
      accepts-and-discards a per-column `OPTIONS (...)` clause so the
      column list still parses. `catalog.Table` mirrors both fields
      (`internal/catalog/catalog.go`); `pg_class`'s relkind derivation gains
      `t.ForeignServerName != "" → 'f'`; `pg_foreign_table.VirtualRows`
      (previously hardcoded empty) now scans live tables for a non-empty
      `ForeignServerName`, resolving `ftserver` via the existing
      `foreignServers`/`ForeignServerOID` registry and `ftoptions` via the
      existing `optionsArrayLiteral` helper. `execCreateTable`
      (`internal/executor/operators_ddl.go`) validates the `SERVER` name
      against the registry before `CreateTable` (42704 if unknown, mirroring
      real PG's `DefineRelation` → `GetForeignServerByName`), so a bad name
      never leaves a half-created relation behind. Verified byte-identical
      against real pg_dump 18.3 via `TestPort_PgDumpConnectionSetup`
      (new fixture reuses the pre-existing `goopg_srv` foreign server).
      Gates: `go build ./...`/`go vet ./...` clean;
      `internal/parser`+`internal/catalog`+`internal/executor` suites PASS
      (`-count=1`); `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck
      Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook. Deferred (ledger
      row appended): per-column `OPTIONS (...)` is parsed but discarded —
      `pg_attribute.attfdwoptions` is not modelled, so no fixture can assert
      column-level FDW options yet; real FDW-handler execution (reading from
      an actual remote source at query time) remains entirely out of scope
      — goopg's foreign-table support is compat/dump-only.
      **2026-07-02 (loop #55, design `0119-0004-create-operator-roundtrip.md`
      "Loop #55", DU-002 slice 418): per-column `OPTIONS (...)` round-trip
      LANDED — closes the loop #54 resume point.** `ColumnDef.FDWOptions`
      (`internal/parser/ast.go`) is now captured by `parseColumnDef`'s
      `OPTIONS (...)` case instead of being discarded, threaded through both
      CREATE TABLE column-construction sites in `operators_ddl.go` onto a new
      `catalog.Column.FDWOptions []string` (mirrors the pre-existing
      `Options`/attoptions field). `buildUserPGAttributeRow`
      (`internal/executor/pg18_user_catalog_rows.go`) renders it into the
      attfdwoptions text-array literal (was hardcoded `NullDatum`). Verified
      against real pg_dump 18.3 via the extended `TestPort_PgDumpConnectionSetup`
      fixture — the pre-existing `goopg_ftable` column `c1 int OPTIONS
      (column_name 'col1')` now makes pg_dump emit the trailing `ALTER FOREIGN
      TABLE ONLY public.goopg_ftable ALTER COLUMN c1 OPTIONS (\n    column_name
      'col1'\n);` statement (positively asserted). New tests
      `TestParseColumnDefFDWOptions` + `TestUserPGAttributeFDWOptionsOverride`.
      Gates: `go build`/`go vet` clean; `internal/parser`+`internal/catalog`+
      `internal/executor` suites PASS (`-count=1`); `TestPort_PgDumpConnectionSetup`
      PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; `gofmt -l` clean; pgbench smoke =
      pre-commit hook. Deferred (ledger row appended): the `ALTER FOREIGN TABLE
      ONLY <t> ALTER COLUMN <c> OPTIONS (...)` statement this loop makes pg_dump
      newly *emit* is not itself parseable by goopg (`parseAlter` has no
      `FOREIGN` lookahead before `ALTER TABLE` dispatch) — a goopg-to-goopg
      restore replay of a foreign table with column options would fail; no
      fixture in scope exercises that path yet.
      **2026-07-02 (loop #56, design `0119-0004-create-operator-roundtrip.md`
      "Loop #56", DU-002 slice 419): `ALTER FOREIGN TABLE ... ALTER COLUMN
      col OPTIONS ([ADD|SET|DROP] name ['value'], …)` parsing+execution
      LANDED — closes the loop #55 resume point.** `parseAlter`
      (`internal/parser/ddl.go`) gains a `FOREIGN` lookahead (consumed only
      when `TABLE` follows) right before the `KwTable` expect, so
      `ALTER FOREIGN TABLE` now shares the plain `ALTER TABLE` grammar
      (IF EXISTS/ONLY/name/ALTER COLUMN); the existing `ALTER COLUMN` block
      gained an `OPTIONS (...)` case using a new `scanAlterFDWOptionsList`
      (verb-tagged sibling of `scanFDWOptionsList`; bare `name 'value'`
      defaults to Add) producing `[]parser.FDWOptionChange` on a new
      `AlterTableAlterColumnOptions` action kind. `execAlterTable`'s new case
      (`internal/executor/operators_ddl.go`) gates on
      `tbl.ForeignServerName != ""` (42809 otherwise), locates the column
      (42703 if absent), and merges via new `applyFDWOptionChanges` onto
      `catalog.Column.FDWOptions` exactly like PG's `transformGenericOptions`
      (`postgres/src/backend/commands/foreigncmds.c:120-206`, read directly
      from the upstream source this loop): ADD/bare errors 42710 if the
      option already exists, SET/DROP each error 42704 if it does not; then
      re-syncs pg_attribute via the same `syncTableToCatalogHeap` path
      `AlterTableSetCompression` uses. New tests
      `TestParseAlterForeignTableAlterColumnOptions` (parser) +
      `TestAlterForeignTableAlterColumnOptionsRoundtrip`/
      `TestAlterForeignTableAlterColumnOptionsErrors` (executor, full
      CREATE SERVER → CREATE FOREIGN TABLE → ALTER FOREIGN TABLE ADD/SET/DROP
      sequence plus all 4 SQLSTATEs). Gates: `go build`/`go vet` clean;
      `internal/parser`+`internal/catalog`+`internal/executor` suites PASS
      (`-count=1`); `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck
      Q12=2/Q13=33 PASS; `gofmt -l` clean on every touched/new file; pgbench
      smoke = pre-commit hook. Deferred (ledger row appended): table-level
      `ALTER FOREIGN TABLE <t> OPTIONS (...)` (PG's `AT_GenericOptions`,
      setting `pg_foreign_table.ftoptions` — distinct from the per-column
      form landed here) remains unmodeled; `ALTER FOREIGN DATA WRAPPER`
      (a structurally different, TABLE-keyword-less statement) remains
      entirely unparseable; no fixture pipes a literal `pg_dump | psql`
      goopg-to-goopg restore of a foreign table.
      **2026-07-02 (loop #57, design `0119-0004-create-operator-roundtrip.md`
      "Loop #57", DU-002 slice 420): table-level `ALTER FOREIGN TABLE ...
      OPTIONS (...)` parsing+execution LANDED — closes the loop #56 resume
      point.** New `AlterTableSetForeignOptions` action kind
      (`internal/parser/ast.go`); `parseAlter` (`internal/parser/ddl.go`)
      gains a bare `OPTIONS (...)` case (no `ALTER COLUMN` prefix) as a
      sibling of the existing `ALTER COLUMN ... OPTIONS` block, reusing
      `scanAlterFDWOptionsList` unchanged. `execAlterTable`'s new case
      (`internal/executor/operators_ddl.go`) mirrors the column-level case's
      42809 check, then merges via the existing `applyFDWOptionChanges`
      helper directly onto `catalog.Table.ForeignOptions` (same 42710/42704
      SQLSTATEs). Confirmed `pg_foreign_table.VirtualRows`
      (`internal/catalog/catalog.go`) is fully virtual — reads
      `ForeignOptions` live on every scan — so unlike the column-level
      `attfdwoptions` case, no heap delete+resync step is needed. New tests
      `TestParseAlterForeignTableSetForeignOptions` (parser) +
      `TestAlterForeignTableSetForeignOptionsRoundtrip`/
      `TestAlterForeignTableSetForeignOptionsErrors` (executor), mirroring
      the loop #56 tests one-for-one (ADD→SET+bare-ADD→DROP sequence, all 4
      SQLSTATEs). Gates: `go build ./...`/`go vet ./...` clean;
      `internal/parser`+`internal/catalog`+`internal/executor` suites PASS
      (`-count=1`); `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck
      Q12=2/Q13=33 PASS; `gofmt -l` clean on every touched/new file (no diff
      overlaps new code, pre-existing go1.25/1.26 drift only,
      `goopg_gofmt_version_mismatch_no_w` memory); pgbench smoke =
      pre-commit hook. Deferred (ledger row appended): real pg_dump never
      actually emits this standalone statement for a foreign table it
      created (`dumpTableSchema` always inlines table-level options into the
      `CREATE FOREIGN TABLE ... SERVER ... OPTIONS (...)` clause at creation
      time — confirmed against the existing `goopg_ftable` fixture); this
      loop is real ALTER-grammar/executor parity for direct post-creation
      use, not a pg_dump round-trip gap. `ALTER FOREIGN DATA WRAPPER` remains
      entirely unparseable; no fixture pipes a literal `pg_dump | psql`
      goopg-to-goopg restore of a foreign table.
      **2026-07-02 (loop #58, design `0119-0004-create-operator-roundtrip.md`
      "Loop #58"): `ALTER FOREIGN DATA WRAPPER name [HANDLER h|NO HANDLER]
      [VALIDATOR h|NO VALIDATOR] [OPTIONS (...)]` parsing+execution LANDED —
      closes the loop #57 resume point ("`ALTER FOREIGN DATA WRAPPER` remains
      entirely unparseable").** A structurally distinct statement from
      `ALTER [FOREIGN] TABLE` (PG's `AlterFdwStmt`, `gram.y:5481-5499`, read
      from upstream source this loop: no `TABLE` keyword, no relation-action
      list). `parseAlter` (`internal/parser/ddl.go`) gains a new branch,
      checked BEFORE the pre-existing FOREIGN-TABLE lookahead, recognising
      `FOREIGN` followed by the bare ident `data`; mirrors `CREATE FOREIGN
      DATA WRAPPER`'s skip-and-scan-for-OPTIONS loop (HANDLER/VALIDATOR
      parsed-and-discarded) but scans `OPTIONS` with the verb-tagged
      `scanAlterFDWOptionsList` (ALTER merges, unlike CREATE's flat replace).
      New `CompatNoopStmt.FDWOptionChanges` field (`internal/parser/ast.go`);
      new `Tag: "ALTER"` branch in `execCompatNoop`
      (`internal/executor/operators_ddl.go`), checked before the pre-existing
      CREATE-only `switch s.ObjType`; new read-only
      `(*catalog.InMemory).LookupForeignDataWrapper` (42704 if the FDW
      doesn't exist — ALTER must not silently create one, unlike
      `RegisterForeignDataWrapper`); merges via the existing
      `applyFDWOptionChanges` helper (same 42710/42704 SQLSTATEs). New tests
      `TestParseAlterForeignDataWrapperOptions` (parser) +
      `TestAlterForeignDataWrapperOptionsRoundtrip`/
      `TestAlterForeignDataWrapperOptionsErrors` (executor), mirroring the
      loop #57 tests one-for-one. Gates: `go build ./...`/`go vet ./...`
      clean; `internal/parser`+`internal/catalog`+`internal/executor` suites
      PASS (`-count=1`); `TestPort_PgDumpConnectionSetup` PASS; TPC-H
      spotcheck Q12=2/Q13=33 PASS; `gofmt -l` clean on every touched/new file
      (`goopg_gofmt_version_mismatch_no_w` memory — pre-existing drift only);
      pgbench smoke = pre-commit hook. Deferred (ledger row appended): real
      pg_dump never emits this standalone statement either
      (`dumpForeignDataWrapper` inlines OPTIONS into the CREATE-time
      statement); `HANDLER`/`VALIDATOR` remain parsed-and-discarded (goopg
      tracks no FDW handler/validator functions at all, unchanged); no
      fixture exercises a goopg-to-goopg `pg_dump | psql` restore replay for
      any FDW-family object (FDW, SERVER, FOREIGN TABLE, USER MAPPING).
      **2026-07-02 (loop #59, design `0119-0004-create-operator-roundtrip.md`
      "Loop #59"): `pg_publication.pubowner` populated LANDED — DU-002 slice
      422.** The FDW-family thread ran dry (no forcing fixture for the
      remaining follow-ups), so this loop researched the next divergence:
      real pg_dump 18.3 `pg_fatal()`s outright ("role with OID 0 does not
      exist") on ANY database containing a publication, because
      `pg_publication.VirtualRows` (`internal/initdb/replication_views.go`)
      hardcoded `pubowner` to `""` and `catalog.Publication`
      (`internal/catalog/pubsub.go`) had no owner field — not a cosmetic
      diff, a total dump abort. New `Publication.Owner uint32`, set to the
      bootstrap superuser OID (10) by `PubSub.CreatePublication` (same
      hardcoded-owner convention as `CREATE CONVERSION`/`CREATE AGGREGATE`'s
      `OwnerOrDefault` fallback, pending real per-session ownership
      tracking); the view renders `fmt.Sprintf("%d", pub.Owner)` instead of
      `""`. `TestPort_PgDumpConnectionSetup` extended with `CREATE
      PUBLICATION goopg_pub1 FOR ALL TABLES` plus assertions for `CREATE
      PUBLICATION goopg_pub1 FOR ALL TABLES WITH (publish = 'insert, update,
      delete');` and `ALTER PUBLICATION goopg_pub1 OWNER TO postgres;`
      (verified against real pg_dump 18.3 semantics: `_getObjectDescription`
      supports the `"PUBLICATION"` desc, so the archiver's generic
      owner-stamping path fires). Gates: `go build ./...`/`go vet ./...`
      clean; `internal/catalog`+`internal/initdb`+`internal/executor` suites
      PASS; `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck
      Q12=2/Q13=33 PASS; `gofmt -l` clean on new fields (pre-existing
      go1.25-vs-go1.26.3 drift on an unrelated `var (...)` block in
      `pubsub.go` only, [[goopg_gofmt_version_mismatch_no_w]]); pgbench
      smoke = pre-commit hook. Deferred (ledger row appended):
      `pg_subscription.subowner` has the identical gap (hardcoded `""`,
      same `getRoleName()` crash risk) but no fixture yet issues `CREATE
      SUBSCRIPTION`; also, `catalog.Publication.Owner` is always the
      bootstrap superuser — a non-superuser-created publication or an
      `ALTER PUBLICATION ... OWNER TO` isn't tracked (same limitation every
      other hardcoded-owner object already has).
      **2026-07-02 (loop #60, design `0119-0004-create-operator-roundtrip.md`
      "Loop #60"): `CREATE SUBSCRIPTION` round-trip + `is_superuser` GUC
      LANDED — DU-002 slice 423.** Closed the loop #59 resume point via live
      repro against real pg_dump 18.3, which surfaced three independently
      forcing bugs: (1) `is_superuser` never reflected the connecting role —
      pg_dump's `getSubscriptions()` gates on the startup-captured
      `ParameterStatus` value, not a live `SHOW`, so the bootstrap `postgres`
      superuser was silently treated as unprivileged and the whole
      subscription dump was skipped; fixed via new
      `SessionRegistry.SetInternal` (`internal/config/session.go`), wired at
      connection startup (`internal/server/server.go`'s new
      `isSuperuserRoleName`) and kept in sync at every `SET`/`RESET
      ROLE`/`SESSION AUTHORIZATION` site (`query.go`+`dispatch.go`'s new
      `setIsSuperuserGUC`). (2) `pg_subscription.subdbid` never matched any
      live `pg_database.oid` — fixed by rendering `catalog.FirstUserOID`
      instead of the unrelated storage-identity `DBOID()`. (3) added the
      missing PG16/17 `subpasswordrequired`/`subrunasowner`/`suborigin`/
      `subfailover` columns. New `catalog.Subscription.Owner uint32`
      (hardcoded bootstrap superuser OID 10, same convention as
      `Publication.Owner`) renders `subowner`. `TestPort_
      PgDumpConnectionSetup` extended with a `CREATE SUBSCRIPTION ... WITH
      (connect = false, ...)` fixture plus exact-text assertions for the
      `CREATE SUBSCRIPTION`/`ALTER SUBSCRIPTION ... OWNER TO postgres;` dump
      lines. Gates: `go build ./...` clean; `internal/config`+
      `internal/catalog`+`internal/server`+`internal/initdb` suites PASS;
      `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33
      PASS; pgbench smoke = pre-commit hook. Deferred (ledger row appended):
      `Subscription.Owner` non-bootstrap-role tracking, extended-protocol
      (`dispatch_extended.go`) has no SET-role-tracking at all, and
      `DBOID()` vs. `pg_database.oid`'s `FirstUserOID` split remains two
      independently-tracked database-identity numbers.
      **2026-07-02 (loop #63, design `0119-0004-create-operator-roundtrip.md`
      "Loop #63"): extended-protocol `SET ROLE`/`SET SESSION AUTHORIZATION`
      role-tracking LANDED** — closes this row's own extended-protocol
      resume point. Live-probing Parse/Bind/Execute/Sync found the bug was
      worse than a silent no-op: `extended.go`'s fast-path switch mis-treated
      `ROLE`/`SESSION` as a GUC name, erroring `22023 unrecognized
      configuration parameter "ROLE"` (confirmed RED via a temporary
      revert). The parser also discarded the `SET ROLE` role name entirely
      and the shared executor path (`utilitySettingsOp`) treated `SET
      ROLE`/`RESET ROLE` as unconditional no-ops — the same executor entry
      point multi-statement simple-query batches use, so this was never
      extended-protocol-specific. Fixed: parser captures the role name
      (`internal/parser/parser.go`); new `executor.Context.SetRole`
      callback (sibling of `SetSessionAuthorization`) wired in both
      `dispatch.go` and the newly-`connTx`-threaded
      `dispatch_extended.go`/`extended.go`
      (`handleExecuteFrame`→`executeExtendedQuery`→
      `executeExtendedQueryViaExecutor` all gained a `connTx` parameter);
      `extended.go`'s fast-path switch gained dedicated `SET ROLE`/`SET
      [LOCAL] SESSION AUTHORIZATION`/`RESET ROLE`/`RESET SESSION
      AUTHORIZATION` cases mirroring `query.go`'s. New
      `TestExtendedProtocolSetRoleTracksNonSuperuserRole`
      (`internal/server`) + parser subtests in `TestParseShowSetReset`.
      Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
      `internal/executor`+`internal/server` suites PASS;
      `TestPort_IsolationTruncateConflict` PASS (no regression to the
      existing simple-query TRUNCATE-ownership spec); `TestPort_
      PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33 PASS;
      pgbench smoke = pre-commit hook. Deferred (ledger row appended): `SET
      LOCAL ROLE`/`SET LOCAL SESSION AUTHORIZATION` still don't revert at
      transaction end (pre-existing limitation, now also true for ROLE by
      construction); `Subscription.Owner`/`Publication.Owner` non-bootstrap
      tracking and the `DBOID()`-vs-`FirstUserOID` split remain open.
      **2026-07-02 (loop #64, design `0119-0004-create-operator-roundtrip.md`
      "Loop #64"): `SET LOCAL ROLE`/`SET LOCAL SESSION AUTHORIZATION`
      transaction-scoped revert LANDED** — closes the loop #63 row's own
      resume point. Live repro against a real running server (psql, one
      statement per Simple Query message — the common client shape) found a
      more severe bug with the same root cause: `SET LOCAL ROLE <name>` sent
      alone raised `unrecognized configuration parameter "role"` instead of
      doing anything (`server/query.go`'s fast-path switch had a dedicated
      `"SET LOCAL SESSION AUTHORIZATION "` case but no analogous `"SET LOCAL
      ROLE "` case, so it fell through to the generic `"SET LOCAL "` handler,
      which mis-parsed "ROLE <name>" as GUC name "role" — not a
      `config.Registry` variable, since SET ROLE is tracked entirely via
      `connTx.NonSuperuserRole`). The identical gap existed in
      `internal/server/extended.go`'s extended-protocol fast path. Fixed both
      the routing bug and the revert semantics together: new
      `connTxState.LocalRolePriorValue *string` +
      `SnapshotLocalRoleIfNeeded(local bool)` (`internal/server/conn_tx.go`)
      captures `NonSuperuserRole`'s pre-change value on the FIRST `SET LOCAL`
      within an active explicit transaction (mirrors PostgreSQL's
      `GUC_ACTION_LOCAL` stack, `guc.c` — a second `SET LOCAL` in the same
      transaction does not move the restore target); `End()` (the shared
      COMMIT/ROLLBACK teardown) restores and clears it unconditionally.
      `executor.Context.SetSessionAuthorization`/`SetRole` gained a `local
      bool` parameter threaded from `stmt.Local`; `dispatch.go`/
      `dispatch_extended.go`'s closures snapshot before mutating, and
      `EndLocalTransaction` now also re-syncs `is_superuser` after
      `connTx.End()` restores. New `"SET LOCAL ROLE "` fast-path cases added
      to both `query.go` and `extended.go`; `extended.go` gained a shared
      `setRoleFastPath` helper (sibling of `setSessionAuthorizationFastPath`).
      Verified live: `SET LOCAL ROLE` alone no longer errors; reverts
      correctly at both COMMIT and ROLLBACK; chained `SET LOCAL ROLE` calls
      revert to the pre-transaction value; plain (non-LOCAL) `SET ROLE`
      still persists past COMMIT unchanged. Tests:
      `internal/server/conn_tx_local_role_test.go` (unit-level) +
      `internal/server/set_local_role_test.go` (full wire-protocol). Gates:
      `go build ./...`/`go vet ./...` clean; `internal/server`+
      `internal/executor` suites PASS; `go test -race -count=1
      ./internal/wal/... ./internal/mvcc/...` PASS; `TestPort_
      IsolationTruncateConflict` PASS; `TestPort_PgDumpConnectionSetup`
      PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit
      hook. Deferred (ledger row appended): extended-protocol
      `BEGIN`/`COMMIT`/`ROLLBACK` are no-op command tags
      (`dispatch_extended.go`), so `connTx.active` never becomes true from a
      purely extended-protocol transaction — `SET LOCAL ROLE` issued
      entirely over the extended protocol still has no transaction boundary
      to revert at (pre-existing "extended protocol is auto-commit-per-
      statement" architectural limitation, not a new gap);
      `Subscription.Owner`/`Publication.Owner` non-bootstrap tracking and the
      `DBOID()`-vs-`FirstUserOID` split remain open.
      **2026-07-02 (loop #65, design `0119-0004-create-operator-roundtrip.md`
      "Loop #65"): `Publication.Owner`/`Subscription.Owner` non-bootstrap-role
      tracking LANDED — DU-002 slice 424.** Closes the loop #60/#63/#64 rows'
      "non-bootstrap-role tracking" resume point, now that loop #63/#64 give a
      connection's `SET ROLE`/`SET SESSION AUTHORIZATION` state
      (`Context.NonSuperuserRole`) somewhere to read from. New
      `PubSub.CreatePublicationAsOwner`/`CreateSubscriptionAsOwner`
      (`internal/catalog/pubsub.go`) take an explicit owner OID;
      `CreatePublication`/`CreateSubscription` become thin `owner=10`
      wrappers so every other caller keeps the bootstrap-superuser default.
      New `ddlOp.currentDDLOwnerOID()` (`internal/executor/operators_ddl.go`)
      resolves `o.ctx.NonSuperuserRole` via `Catalog.RoleOID` (falling back to
      10 on an unresolvable name, which should not happen since
      `NonSuperuserRole` is only ever set from a previously-validated role);
      `execCreatePublication`/`execCreateSubscription` now call the
      `...AsOwner` variants with it, so `pg_publication.pubowner`/
      `pg_subscription.subowner` reflect the actual creating role, not always
      "postgres". Tests: `TestCreatePublicationOwnerDefaultsToBootstrapSuperuser`
      (pins the pre-existing no-`SET ROLE` behavior),
      `TestCreatePublicationOwnerTracksEffectiveRole`,
      `TestCreateSubscriptionOwnerTracksEffectiveRole`
      (`internal/executor/operators_ddl_pubsub_test.go`). Gates: `go build
      ./...` clean; `internal/catalog`+`internal/executor`+`internal/server`
      suites PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke =
      pre-commit hook. Deferred (ledger row appended): `ALTER PUBLICATION
      ... OWNER TO`/`ALTER SUBSCRIPTION ... OWNER TO` still don't exist (owner
      is fixed at CREATE time only); the `DBOID()`-vs-`FirstUserOID` split
      (loop #60) remains open, untouched by this loop.
      **2026-07-02 (loop #67): `ALTER PUBLICATION`/`ALTER SUBSCRIPTION ... OWNER
      TO` LANDED — DU-002 slice 425.** Closes the loop #65 row's own resume
      point. New parser nodes `AlterPublicationOwnerStmt`/
      `AlterSubscriptionOwnerStmt` (`internal/parser/ast.go`) with dedicated
      parsing in `parseAlter` (`internal/parser/ddl.go`), mirroring
      `AlterCollationStmt`'s "owner" case (name, `OWNER TO {role|CURRENT_USER|
      SESSION_USER|CURRENT_ROLE}`; any other tail — RENAME TO, SET, ADD/DROP
      TABLE, REFRESH PUBLICATION — drains to the statement end as the
      pre-existing no-op). Found and fixed a **pre-existing, independently
      dead parse path** while wiring this: the old generic compatibility-stub
      loop listed `"publication"`/`"subscription"` alongside `"schema"`/
      `"collation"`/etc, but matched them via `acceptIdentKeyword` (requires
      `TokenKind == TokenIdent`) — both words are registered *keywords*
      (`KwPublication`/`KwSubscription`, needed by `CREATE SUBSCRIPTION ...
      PUBLICATION p`), so `TokenKind == TokenKeyword` and that branch could
      never match; `ALTER PUBLICATION`/`ALTER SUBSCRIPTION` of any form
      actually fell through to the ALTER-TABLE default and errored
      (`expected keyword table`), not the silent no-op the comment claimed.
      The new dedicated case uses `p.acceptKeyword(KwPublication/
      KwSubscription)` instead. New catalog mutators
      `PubSub.SetPublicationOwner`/`SetSubscriptionOwner`
      (`internal/catalog/pubsub.go`, `ErrPublication/SubscriptionNotFound` on
      an unknown name) + executor `execAlterPublicationOwner`/
      `execAlterSubscriptionOwner` + shared `resolveNewOwnerOID` helper
      (`internal/executor/operators_ddl.go`) resolving the "current_user"
      sentinel to the bootstrap superuser OID (10) or a real role via
      `Catalog.RoleOID` (42704 on an unresolvable name). Planner `Plan()`
      needed the two new statement types added to the existing pub/sub `case`
      arm (`internal/planner/planner.go`) — omitting this surfaces as `0A000
      unsupported statement type`, not a parse error, since planning is a
      separate dispatch from parsing. Like CREATE/DROP PUBLICATION/
      SUBSCRIPTION, the change is in-memory-only (PubSub has no WAL/restart
      persistence at all yet — pre-existing, separately-tracked gap, not
      reopened by this loop). Tests: `TestParseAlterPublicationOwner`/
      `TestParseAlterSubscriptionOwner`/`TestParseAlterPublicationOtherFormsStillNoop`
      (`internal/parser/alter_pubsub_owner_test.go`);
      `TestAlterPublicationOwnerTo`/`TestAlterPublicationOwnerToUnknownRoleErrors`/
      `TestAlterPublicationOwnerToUnknownPublicationErrors`/
      `TestAlterSubscriptionOwnerTo`
      (`internal/executor/operators_ddl_pubsub_test.go`). Gates: `go build
      ./...` clean; `internal/parser`+`internal/catalog`+`internal/planner`+
      `internal/executor`+`internal/server` suites PASS; TPC-H spotcheck
      Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook. Still open
      (unchanged): PubSub restart persistence; the `DBOID()`-vs-`FirstUserOID`
      split (loop #60).
      **2026-07-02 (loop #68, design `0119-0004-create-operator-roundtrip.md`
      "Loop #68"): `PubSub` WAL/restart persistence LANDED.** Closes the
      loop #67 row's own "PubSub restart persistence" resume point. New WAL
      record kinds 50-55 (`RecordKindCreatePublication`/`DropPublication`/
      `AlterPublicationOwner`/`CreateSubscription`/`DropSubscription`/
      `AlterSubscriptionOwner`, `internal/wal/recovery.go`) with matching
      `Encode.../Decode...` pairs, mirroring `EncodeCreateCollation`'s
      length-prefixed-string format (publication's `Tables`/subscription's
      `Publications` list use the same 2-byte-count-then-length-prefixed-
      strings shape `EncodeCreateAggregate` uses for `argTypes`). Physical
      redo is a no-op for all six (goopg has no per-publication/subscription
      file namespace) — a new `internal/initdb/pubsub_ddl_recovery.go`
      (`replayPubSubDDLRecords`) scans the WAL after physical replay and
      reapplies each record to `catalog.PubSub` via 6 new recovery mutators
      (`Create{Publication,Subscription}DuringRecovery`,
      `Drop{Publication,Subscription}DuringRecovery`,
      `Set{Publication,Subscription}OwnerDuringRecovery`,
      `internal/catalog/pubsub.go`) that overwrite-by-name (PubSub is
      already name-keyed, unlike collation's OID-keyed-slice) and bump
      `nextOID` past the recovered OID. Unlike the collation/aggregate
      recovery drivers, `replayPubSubDDLRecords` takes the concrete
      `*catalog.PubSub` directly instead of `catalog.Catalog` +
      interface-assertion — PubSub has exactly one implementation, so the
      indirection those drivers need (multiple `Catalog` implementations in
      tests) doesn't apply here. Wired in `internal/initdb/open.go`
      immediately after `pubsub := catalog.NewPubSub()` (before the view
      registrations); no ordering dependency on schema replay since PubSub
      isn't schema-scoped. `internal/executor/operators_ddl.go`'s
      `execCreatePublication`/`execCreateSubscription` now capture the
      previously-discarded `*Publication`/`*Subscription` return value and
      WAL-append the create record when `o.ctx.WAL != nil`;
      `execDropPublication`/`execDropSubscription`/
      `execAlterPublicationOwner`/`execAlterSubscriptionOwner` append their
      matching record after a successful catalog mutation (skipped on the
      `IF EXISTS` not-found no-op path, matching every other DROP's
      WAL-skip-on-noop convention). Tests: `internal/wal/pubsub_ddl_test.go`
      (`TestEncodeDecode{Create,Drop}{Publication,Subscription}RoundTrip` +
      2 `AlterOwner` round-trip tests + a combined wrong-kind/truncated-
      payload guard across all 6 kinds) and
      `internal/initdb/pubsub_ddl_recovery_test.go` (8 tests: real
      `Init`→`Open`→`WAL.Append`→`Close`→re-`Open` round trips for
      CREATE/CREATE+DROP/CREATE+ALTER-OWNER on both publication and
      subscription, plus the missing-WAL-dir/nil-PubSub no-op guards).
      Gates: `go build ./...` clean; `go test -race -count=1
      ./internal/wal/... ./internal/mvcc/...` PASS; `internal/wal`+
      `internal/catalog`+`internal/initdb`+`internal/executor`+
      `internal/parser`+`internal/planner`+`internal/server` suites PASS;
      `TestE2E_PhysicalReplication` PASS; `TestPort_PgDumpConnectionSetup`
      PASS (no regression to the DU-002 connsetup slice); TPC-H spotcheck
      Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook. Box stays
      unchecked: the `DBOID()`-vs-`FirstUserOID` split (loop #60) remains
      open; `SubscriptionRel` (tablesync state) rows still have no WAL
      persistence (no forcing fixture — runtime apply-worker state, not a
      `pg_dump`-visible catalog row).
- [x] **CREATE/DROP EVENT TRIGGER round-trip (M0119-0004, loop #69).**
      **COMPLETE 2026-07-02:** `pg_event_trigger` was a scaffolded-but-
      always-empty virtual view — CREATE EVENT TRIGGER had never been
      parsed at all. New `catalog.EventTrigger` registry (mirrors
      `ForeignDataWrapper`) + parser `CreateEventTriggerStmt` (`CREATE
      EVENT TRIGGER name ON event [WHEN TAG IN (...)] EXECUTE FUNCTION
      fn()`) + `execCreateEventTrigger` (event-name/filter-variable/
      login-tag validation, niladic function resolution); `DROP EVENT
      TRIGGER` reuses `DropCompatStmt`'s new `"event trigger"` case
      (fixing a pre-existing, previously-unreachable dead parse path
      where bare `"event"` never consumed the following `TRIGGER`
      keyword token). Also fixed, found via a live PG 18.3 diff against a
      genuinely separate freshly-`initdb`'d instance: the plain `regproc`
      OID→name `CastExpr` branch (`internal/executor/expr.go`, distinct
      from the already-fixed `regprocedure`/`regoperator` branches) never
      schema-qualified a user-defined function — `evtfoid::regproc`
      dumped `et_func()` instead of PG's `public.et_func()`; fixed via
      the same `regObjectSchemaVisible` gate the sibling branches use.
      Verified byte-identical against real pg_dump 18.3 for the full
      `CREATE FUNCTION ... RETURNS event_trigger; CREATE EVENT TRIGGER
      ... WHEN TAG IN (...) EXECUTE FUNCTION ...` fixture. Tests:
      `internal/parser/event_trigger_test.go`,
      `internal/executor/operators_ddl_event_trigger_test.go`. Gates:
      `go build ./...`/`go vet ./...` clean; `internal/parser`+
      `internal/catalog`+`internal/planner`+`internal/executor`+
      `internal/server` suites PASS; `TestPort_PgDumpConnectionSetup`
      PASS (no regression from the `regproc` fix); TPC-H spotcheck
      Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook. Deferred
      (ledger row appended): `ALTER EVENT TRIGGER`
      (ENABLE/DISABLE/RENAME/OWNER TO), full DDL-command-tag-list
      validation, superuser privilege enforcement, WAL/restart
      persistence — none have a forcing fixture today.
- [x] **`ALTER EVENT TRIGGER` round-trip (M0119-0004, loop #70).**
      **COMPLETE 2026-07-02:** closes the loop #69 row's own `ALTER EVENT
      TRIGGER` deferral for the 4 forms PostgreSQL's `event_trigger.c`
      supports: `DISABLE`, `ENABLE [REPLICA|ALWAYS]`, `RENAME TO`, `OWNER
      TO` (incl. `CURRENT_USER`/`SESSION_USER`/`CURRENT_ROLE`). New parser
      `AlterEventTriggerStmt` + `parseAlter()` `"event"`+`TRIGGER` branch;
      new `catalog.InMemory.SetEventTriggerEnabled`/`SetEventTriggerOwner`/
      `RenameEventTrigger` mutators + `ErrEventTriggerNotFound`/
      `ErrEventTriggerAlreadyExists` sentinels; new
      `execAlterEventTrigger` (mirrors `execAlterPublicationOwner`); planner
      DDL allow-list extended (the `0A000 unsupported statement type` gap
      hit first in testing — a new `Stmt` type needs its own planner case).
      No pg_dump-side change needed: `pg_event_trigger.evtenabled`/
      `evtname`/`evtowner` already rendered from the mutated struct (loop
      #69), and real pg_dump's own `dumpEventTrigger` already emits the
      trailing `ALTER EVENT TRIGGER ... {DISABLE|ENABLE ALWAYS|ENABLE
      REPLICA}` line into the same archive entry whenever `evtenabled !=
      'O'`. Verified byte-identical against a real, separately-`initdb`'d
      PG 18.3 instance (`postgres/local_install/bin/{psql,pg_dump}`) for
      all 4 forms, including that returning to `ENABLE` (evtenabled='O')
      correctly *omits* the trailing ALTER line. Tests:
      `TestParseAlterEventTrigger` (parser, table-driven, 6 forms +
      2 owner sentinels), `TestAlterEventTriggerEnableDisable`/
      `...RenameTo`/`...OwnerTo`/`...UnknownNameErrors` (executor). Gates:
      `go build ./...` clean; `internal/parser`+`internal/catalog`+
      `internal/planner`+`internal/executor`+`internal/server` suites
      PASS; `TestPort_PgDumpConnectionSetup` PASS (no regression); live PG
      18.3 diff (byte-identical); TPC-H spotcheck Q12=2/Q13=33 PASS;
      pgbench smoke = pre-commit hook. Design doc: `0119-0004-create-
      operator-roundtrip.md` "Loop #70". Deferred (ledger row appended):
      WAL/restart persistence for the whole event-trigger feature (CREATE,
      DROP, and now ALTER) still has no forcing fixture; the loop #69
      `validate_ddl_tags`/superuser-privilege gaps are untouched by this
      loop.
- [x] **Event trigger CREATE/DROP/ALTER WAL/restart persistence
      (M0119-0004, loop #71).** **COMPLETE 2026-07-02:** closes the loop #70
      row's own WAL-persistence resume point (that ledger row is now
      `resolved`), mirroring the PubSub WAL/restart persistence pattern from
      loop #68. Five new WAL record kinds (`RecordKindCreateEventTrigger`/
      `DropEventTrigger`/`AlterEventTriggerEnabled`/`AlterEventTriggerRename`/
      `AlterEventTriggerOwner`, kinds 56-60, `internal/wal/recovery.go`) each
      with an `Encode*`/`Decode*` pair; goopg has no per-event-trigger file
      namespace so `wal.ApplyRecord`'s physical-redo path is a no-op for all
      five. New post-replay driver `internal/initdb/
      event_trigger_ddl_recovery.go` (`replayEventTriggerDDLRecords`) applies
      each record via five new `*DuringRecovery` catalog mutators
      (`internal/catalog/catalog.go`, OID-preserving, idempotent-overwrite);
      wired into `internal/initdb/open.go` right after the PubSub replay
      call. `execCreateEventTrigger`/`execAlterEventTrigger`/
      `execDropCompat`'s `"event trigger"` case (`internal/executor/
      operators_ddl.go`) now WAL-log before returning success. Also added
      `catalog.InMemory.LookupEventTrigger` (deep-copy accessor, mirrors
      `PubSub.LookupPublication`). Verified via a real `goopg stop`/`start`
      restart cycle against the same data dir with real `psql`/`pg_dump`
      (not just unit tests): CREATE+DISABLE survives one restart, RENAME TO+
      OWNER TO+re-ENABLE survives a second. Tests:
      `internal/wal/event_trigger_ddl_test.go` (5 record formats,
      round-trip + truncated/wrong-kind guard),
      `internal/initdb/event_trigger_ddl_recovery_test.go` (full
      Init→Open→WAL-append→Close→Open cycles for CREATE, CREATE+DROP,
      CREATE+DISABLE+RENAME+OWNER chained, plus missing-dir/nil-catalog
      guards). Gates: `go build ./...`/`go vet ./...` clean;
      `internal/wal`+`internal/catalog`+`internal/initdb`+
      `internal/executor`+`internal/planner`+`internal/parser`+
      `internal/server` suites PASS; TPC-H spotcheck Q12=2/Q13=33 PASS
      (`scripts/tpch-spotcheck.sh`); full pre-commit gate incl. pgbench
      TPC-B smoke PASS (`scripts/ralph-precommit-test.sh`). Design doc:
      `0119-0004-create-operator-roundtrip.md` "Loop #71". Deferred (ledger
      row appended): the loop #69 `validate_ddl_tags`/superuser-privilege
      gaps remain untouched (still tracked open via the loop #69 row);
      newly discovered while verifying live — `CREATE FUNCTION` itself has
      no WAL/restart persistence in goopg (a pre-existing, broader gap, not
      specific to event triggers), so an event trigger's `evtfoid` can
      dangle post-restart if its backing function was created post-initdb.
- [ ] **M0119-0005 — pg_waldump server tier** (source: M0110-0002; see M0110
      section). `002_save_fullpage` + per-rmgr/relation/block filtering; needs
      PG-decodable FPI/heap WAL (+ index AMs for the server tier).
- [ ] **M0119-0006 — pg_amcheck server tier** (source: M0110-0003; see M0110
      ledger rows). `002_nonesuch` … `005_opclass_damage`; `CREATE EXTENSION
      amcheck` + `verify_heapam()` SRF on top of `internal/amcheck` + opclass
      catalog parity.
- [ ] **M0119-0007 — pg_basebackup recvlogical** (source: M0095-0003; see M0095
      section). `030 recvlogical` — blocked on logical decoding (tracks the
      logical-replication milestone / D-004).
- [x] **M0119-0008 — isolation residual. RESOLVED by triage 2026-06-29
      (M0119-0001) — no actionable backlog.** predicate-gin AND predicate-gist are
      PROMOTED (M0118-0002 group COMPLETE; designs 0118-0137/0118-0140); their
      ledger rows (227–235) are `resolved`. The ONLY remaining `failed` isolation
      spec in the whole suite is `deadlock-parallel`, which is **infeasible** in
      goopg today (needs a parallel-query worker lock-group abstraction goopg has
      no subsystem for) and carries no open ledger row of its own. Nothing
      actionable until parallel query lands.
- [x] **M0119-0009 — UPDATE/DELETE conflict-wait-on-a-conflicting-locker**
      (source: M0118-0004 loop #44 closure note; see ledger row). **2026-07-01
      (loop #46, design 0119-0009): sibling-path wiring LANDED.**
      `waitForConflictingRowLock` (M0118-0003) was already wired at the three
      canonical write sites (`updateViaIndex`, `updateOp.Next` seqscan,
      `deleteOp.Next`) but never at 5 sibling sites sharing the same
      `stampUpdaterXmaxNonHOT` producer: `updateWithFrom`, `deleteWithUsing`,
      `mergeApplyUpdate`/`mergeApplyDelete`, `upsertOp.applyUpdate`. Wired all
      5. Confirmed RED→GREEN for the two MERGE sites (genuine gaps, no other
      blocking mechanism); the other three turned out already protected by
      pre-existing mechanisms discovered mid-loop (upsert's arbiter-conflict
      scan; `scanMatching`'s older M0021-era lockmgr block for the
      FROM/USING sites) — this loop's fix there closes the narrower
      scan-then-stamp race window instead. Full detail + deferred residuals
      (NND arbiter path, `scanMatching`'s non-conflict-aware block) in the
      design doc. Gates: race batch + full executor suite + `-race`
      mvcc/multixact/wal + catalog/planner/server + `TestPort_
      PgDumpConnectionSetup` + TPC-H Q12=2/Q13=33 all PASS; pgbench smoke =
      pre-commit hook.

- [x] **Event trigger DDL-tag validation + superuser enforcement
      (M0119-0004, loop #72).** **COMPLETE 2026-07-02:** closes the loop #69
      ledger row's other two deferrals — `validate_ddl_tags`/
      `validate_table_rewrite_tags` command-tag membership checking and
      `CreateEventTrigger`'s superuser privilege check — the last open
      thread on the `CREATE`/`ALTER`/`DROP EVENT TRIGGER` family; that
      ledger row is now `resolved`. New `internal/executor/cmdtag_table.go`
      is a mechanically-generated (not hand-transcribed) Go map of all 192
      real PostgreSQL command tags to their `event_trigger_ok`/
      `table_rewrite_ok` flags (from `postgres/src/include/tcop/
      cmdtaglist.h`); `validateDDLTags`/`validateTableRewriteTags` reproduce
      PG's two distinct error shapes (42601 unrecognized vs. 0A000
      recognized-but-disallowed; table_rewrite has no unknown-tag special
      case, unlike ddl tags) verified against a real PG 18.3 instance.
      Superuser enforcement via the existing `Context.NonSuperuserRole`
      mechanism: `execCreateEventTrigger` now 42501s a non-superuser CREATE
      (checked first, matching PG's own ordering); `execAlterEventTrigger`'s
      `"owner"` case now 42501s when the resolved new-owner OID isn't 10
      (goopg's role model never marks a `CREATE ROLE`'d role superuser),
      mirroring `AlterEventTriggerOwner_internal`'s target-superuser check —
      `TestAlterEventTriggerOwnerTo`'s prior `OWNER TO alice` success
      assertion encoded a real PG gap, not intended behavior, so it was
      narrowed and the rejection got its own new test. Tests:
      `TestCreateEventTriggerNonSuperuserErrors`,
      `TestCreateEventTriggerTagValidation` (8 cases),
      `TestAlterEventTriggerOwnerToNonSuperuserErrors`. Gates: `go build
      ./...` clean; `internal/parser`+`internal/catalog`+
      `internal/planner`+`internal/executor`+`internal/wal`+
      `internal/initdb`+`internal/server` suites PASS; `TestPort_
      PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; full
      pre-commit gate incl. pgbench TPC-B smoke PASS. Design doc:
      `0119-0004-create-operator-roundtrip.md` "Loop #72". Nothing left
      open on the event-trigger family itself; the broader, unrelated
      `CREATE FUNCTION` WAL/restart persistence gap (loop #71 discovery)
      remains open under its own ledger row.
- [x] **CREATE/ALTER/DROP FUNCTION + CREATE/ALTER PROCEDURE WAL/restart
      persistence (M0119-0004, loop #73).** **COMPLETE 2026-07-02:** closes
      the loop #71 ledger row's own resume point — `catalog.Routines` was a
      pure in-memory registry with no restart persistence at all, so every
      user-defined function/procedure vanished on server restart (and any
      event trigger's `evtfoid` reference could dangle). New WAL record
      kinds 61-64 (`RecordKindCreateFunction`/`DropFunction`/
      `AlterFunctionRename`/`AlterFunctionFlags`) with a struct-based
      `CreateFunctionPayload`/`FunctionArgPayload` Encode/Decode pair (too
      many fields — full arg list + return type + body + 8 attributes —
      for a flat positional signature); `Body` gets a 4-byte length prefix
      since a plpgsql body can exceed 65535 bytes. `DropFunction` carries
      the resolved OID (not name+signature) so replay sidesteps overload
      ambiguity. New `catalog.Routines` recovery mutators
      (`CreateDuringRecovery`/`DropByOIDDuringRecovery`/
      `RenameByOIDDuringRecovery`/`SetFlagsByOIDDuringRecovery`) + new
      driver `internal/initdb/function_ddl_recovery.go`, wired into
      `open.go` after the event-trigger replay. `executor.extractRoutineDeps`
      exported to `ExtractRoutineDeps` so recovery recomputes SQL-routine
      dependency data post-replay instead of serializing it. WAL logging
      covers CREATE FUNCTION/PROCEDURE (+OR REPLACE), ALTER
      FUNCTION/PROCEDURE/ROUTINE (RENAME TO + the four mutable attributes),
      and DROP FUNCTION on both its autocommit-immediate and
      deferred-to-COMMIT paths (the latter logged inside
      `ApplyDeferredRoutineDrops` itself — the single chokepoint both
      commit paths funnel through, which never runs on ROLLBACK, making it
      inherently rollback-safe) plus CASCADE-dependent drops. Verified via
      a real `goopg stop`/`start` restart cycle (not just unit tests):
      CREATE FUNCTION (DEFAULT arg + numeric(10,2) typmod) + CREATE
      FUNCTION IMMUTABLE + CREATE PROCEDURE + DROP FUNCTION all survive one
      restart with correct absence of the dropped/renamed-away names;
      ALTER FUNCTION RENAME TO + STRICT SECURITY DEFINER survive with all
      flags intact; the restored procedure/function both still execute.
      Tests: `internal/wal/function_ddl_test.go`,
      `internal/initdb/function_ddl_recovery_test.go` (6 tests). Gates:
      `go build`/`go vet` clean; `internal/wal`+`internal/catalog`+
      `internal/executor`+`internal/initdb`+`internal/planner`+
      `internal/parser`+`internal/server` suites PASS; `TestPort_
      PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; full
      pre-commit gate incl. pgbench TPC-B smoke (0 failed transactions)
      PASS. Design doc: `0119-0004-create-operator-roundtrip.md` "Loop
      #73". Deferred (ledger row appended): `DROP PROCEDURE` has NO WAL
      persistence — it uses a different, scattered 7-call-site
      rollback-undo mechanism than `DROP FUNCTION`'s single
      `ApplyDeferredRoutineDrops` chokepoint, and logging at its immediate
      mutation point would be unsafe (a rolled-back `DROP PROCEDURE` would
      still show as dropped post-restart) — needs its own commit-time hook
      first.
- [x] **DROP PROCEDURE WAL/restart persistence (M0119-0004, loop #74).**
      **COMPLETE 2026-07-02:** closes the loop #73 ledger row's own resume
      point via option (b) — unified `DROP PROCEDURE` onto the exact
      `DeferredRoutineDrop`/`ApplyDeferredRoutineDrops` mechanism `DROP
      FUNCTION` already uses, instead of a parallel commit-time hook.
      `execDropProcedure` already resolved the target routine before
      mutating anything, so only its final drop step changed: inside an
      explicit transaction it now calls `bsess.AddDeferredRoutineDrop(...)`
      (identical to `execDropFunction`'s branch — procedure stays
      resolvable/callable until COMMIT) instead of an immediate
      `DropRoutine` + `AddPendingRoutineDrop`; autocommit still drops
      immediately but now also calls `walLogDropRoutine` (previously
      DROP-FUNCTION-only). `ApplyDeferredRoutineDrops` and the WAL replay
      driver needed zero code changes — both are already OID-keyed and
      kind-agnostic between function and procedure. This closes two gaps
      in one motion, as anticipated: the missing WAL persistence, and a
      pre-existing transactional-visibility bug where a concurrent session
      saw a `DROP PROCEDURE`'d name vanish before the dropping transaction
      committed. The now-fully-dead `BasicSession.pendingRoutineDrops`/
      `AddPendingRoutineDrop`/`TakePendingRoutineDrops` mechanism (its only
      caller) was deleted outright across all 7 call sites
      (`operators_tx.go`, `dispatch.go` ×5, `server.go`, `twophase.go`)
      rather than left as dead code — safe because a deferred drop's
      ROLLBACK needs no restore action, and every site already calls
      `EndExplicitTransaction`, which unconditionally resets
      `deferRoutineDrops = nil`. Tests:
      `TestExecDropProcedureRemovesEntry`/
      `TestExecDropProcedureDeferredToCommit`/
      `TestExecDropProcedureRollbackLeavesEntry`
      (`internal/executor/operators_function_test.go`). Verified via a
      real `goopg stop`/`start` restart cycle: an explicit-transaction
      DROP PROCEDURE + ROLLBACK stays callable, the same DROP + COMMIT is
      gone, an autocommit DROP PROCEDURE is gone, all stay absent
      post-restart, and an undropped procedure survives the same restart.
      Gates: `go build`/`go vet` clean; `internal/executor`+
      `internal/server`+`internal/wal`+`internal/catalog`+
      `internal/initdb`+`internal/parser`+`internal/planner` suites PASS;
      `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33
      PASS; full pre-commit gate incl. pgbench TPC-B smoke (0 failed
      transactions) PASS. Design doc:
      `0119-0004-create-operator-roundtrip.md` "Loop #74". The
      `CREATE`/`ALTER`/`DROP FUNCTION`/`PROCEDURE` surface is now entirely
      WAL-persisted; the loop #73 ledger row is resolved.
- [x] **TOAST chunk_id restart durability (root-0022, interactive session).**
      **COMPLETE 2026-07-02:** closes the WordPress `wp_options`/
      `wp_user_roles` neighbor-row corruption ledger row (2026-07-02). Root
      cause was NOT missing TOAST (goopg has had real TOAST since
      M0046-0006, `internal/executor/toast.go`) but
      `executor.toastOIDCounter` — a process-local, non-persisted
      `atomic.Int64` — resetting to 0 on every restart while the TOAST
      relation it writes `chunk_id` rows into survives on disk; the first
      TOASTed value written after any restart (no crash needed) reissued
      `chunk_id 1`, colliding with a pre-restart value's still-resident
      `chunk_id 1` in the same table's TOAST relation, and
      `DetoastValue`'s oid-only scan spliced the two unrelated values'
      bytes together. Fix: new `executor.SeedToastOIDCounter`/
      `MaxToastChunkIDInRel`/`AdvanceToastOIDCounterPast`
      (`internal/executor/toast.go`) scan every user table's TOAST
      relation once at startup (wired into `internal/initdb/open.go`
      right after the existing M0106-0013 catalog-OID-advance loop,
      unconditionally — even on the M0114 cache-hit path) and advance the
      counter past the max `chunk_id` found, mirroring the established
      catalog-OID restart pattern. `MaxToastChunkIDInRel` short-circuits
      via `Pool.Exists` before touching `NBlocks`/`Pin` to avoid the smgr
      O_CREATE-recreates-removed-files pitfall
      ([[goopg_smgr_ocreate_recreates_removed_files]]). Tests:
      `TestToastOIDCounterCollisionAcrossRestart` (executor-level,
      confirmed to FAIL with byte-exact reproduction of the reported
      corruption when the reseed call is removed) +
      `TestMaxToastChunkIDInRelNoFile` +
      `TestSeedToastOIDCounterAdvancesPastExisting`, plus a real
      cluster-restart e2e `TestPort_ToastValueSurvivesRestartWithoutCollision`
      (`internal/testport/toast_oid_restart_durability_test.go`, mirrors
      `serial_sequence_durability_test.go`). Gates: `go build`/`go vet`
      clean; full `internal/executor`+`internal/initdb`+`internal/storage`
      suites PASS; `go test -race ./internal/wal/... ./internal/mvcc/...`
      PASS; TPC-H spotcheck PASS; pgbench smoke = pre-commit hook. Design
      `docs/design/root-0022-toast-oid-restart-durability.md`. Deferred
      (ledger row appended): TOAST chunk writes are not per-insert
      WAL/FPI-protected (`writeHeapTupleToRel` uses plain `MarkDirty`, not
      the `MarkDirtyChangeRecord` discipline the main-heap insert path
      uses), so an unclean crash (not just a restart) before the next
      checkpoint could still lose chunks written after the first one on an
      already-dirty TOAST page — a narrower, separate fix from this loop's
      actual reported bug.

- [x] **TOAST per-chunk WAL durability (root-0022 follow-up, interactive
      session, loop #2).** **COMPLETE 2026-07-02:** closes the previous
      loop's own ledger row. `writeHeapTupleToRel`
      (`internal/executor/toast.go`) dirtied the TOAST page via a bare
      `ctx.Pool.MarkDirty(slot)`, which never invokes any per-insert WAL
      emitter — only `maybeEmitFPI`'s first-dirty-in-epoch full-page-image.
      Since up to ~4 TOAST chunks fit on one 8 KiB page, chunks 2-4 written
      into an already-dirty page in the same checkpoint epoch produced zero
      WAL output and would be lost on an unclean crash before the next
      checkpoint. Fix: both branches of `writeHeapTupleToRel` now call
      `markHeapInsertDirty(ctx.Pool, slot, ctx.Pool.LogHeapInsert(), rel,
      blk, lineSlot, raw)` — the same helper the main heap-insert path uses
      (`operators_storage.go:7750`) — which routes through
      `Pool.MarkDirtyLogicalChange` and unconditionally emits a WAL record
      on every call, not just the page's first dirty. No on-disk format
      change; replay is the generic `RecordKindHeapInsert` record. Logical
      decoding unaffected (`pgoutput.Change` already filters unregistered
      relations, and TOAST relations were never registered as publishable
      tables). Test: `TestToastChunkInsertsAreIndividuallyWALLogged`
      (`internal/executor/toast_test.go`) — wires a real
      `LogHeapInsert`/`LogPageImage` pool, writes a 3-chunk value on one
      page, asserts 3 WAL emissions (confirmed 0 on the pre-fix code by
      reverting and re-running). Gates: `go build`/`go vet` clean; full
      `internal/executor` suite PASS; `go test -race
      ./internal/wal/... ./internal/mvcc/... ./internal/storage/...` PASS;
      pre-commit gate (incl. TPC-H spotcheck + pgbench TPC-B smoke) run.
      Design: `docs/design/root-0022-toast-oid-restart-durability.md`
      "Follow-up: per-chunk WAL durability". This closes the
      counter-collision AND per-chunk-crash-durability TOAST gaps
      discovered from the WordPress workload; no further TOAST durability
      deferrals remain open in the ledger as of this loop.

- [x] **`CREATE ACCESS METHOD` round-trip in pg_dump (M0119-0004, DU-002
      slice 426, interactive session).** **COMPLETE 2026-07-02:** `CREATE
      ACCESS METHOD name TYPE {INDEX|TABLE} HANDLER handler_name` was a
      bare parse error (no parse path existed at all), and
      `pg_am.VirtualRows` only ever emitted the 7 built-in AM rows, so
      pg_dump's `getAccessMethods()` always read 0 dumpable rows.
      goopg has no pluggable table/index storage engine and never invokes
      a user-defined AM's handler — this is dump-fidelity only, same scope
      as the existing CREATE OPERATOR/OPERATOR CLASS compat-registration
      slices. Parser: new `CreateAccessMethodStmt` AST node +
      `parseCreateAccessMethodTail` (mirrors `gram.y`'s `CreateAmStmt`);
      `DROP ACCESS METHOD` already parsed generically via the ident-DROP-
      target list (M0097-0071). Catalog: new `AccessMethod{Name, OID,
      AMType, HandlerOID}` registry (`RegisterAccessMethod`/
      `DropAccessMethod`/`ListAccessMethods`, keyed by amname, mirrors the
      `ForeignDataWrapper`/`EventTrigger` compat-registry shape);
      `pg_am.VirtualRows` appends user AM rows after the 7 built-ins (no
      oid-range filtering needed — pg_dump's own `selectDumpableAccessMethod`
      does that client-side). Executor: `execCreateAccessMethod` mirrors
      `CreateAccessMethod`'s validation order (superuser 42501 → handler
      resolution → duplicate-name 42710, including a built-in-name
      collision via `catalog.AccessMethodOIDByName`);
      `resolveAccessMethodHandlerFunc` mirrors `lookup_am_handler_func`
      (`amcmds.c`) — the handler must resolve to a routine with exactly
      one argument of type `internal`, returning the AM-type-matching
      pseudo-type (`index_am_handler`/`table_am_handler`; 42883 if
      unresolved, 42809 if wrong return type); `execDropCompat`'s existing
      `"access method"` case now also calls `DropAccessMethod`. New
      pseudo-type OIDs 325 (`index_am_handler`)/269 (`table_am_handler`)
      in `typeNameToOIDStr` let a `CREATE FUNCTION ... RETURNS
      index_am_handler` handler stub resolve a `prorettype` at all. Tests:
      `TestParseCreateAccessMethod`/`TestParseCreateAccessMethodErrors`/
      `TestParseDropAccessMethod` (`internal/parser/access_method_test.go`),
      `TestCreateAccessMethodRegistersRow`/`TestCreateAccessMethodTableType`/
      `TestCreateAccessMethodUnknownFunctionErrors`/
      `TestCreateAccessMethodWrongReturnTypeErrors`/
      `TestCreateAccessMethodDuplicateNameErrors`/
      `TestDropAccessMethodRemovesRow`
      (`internal/executor/operators_ddl_access_method_test.go`), plus
      slice 426 in `TestPort_PgDumpConnectionSetup` (real `LANGUAGE c`
      handler stub + `CREATE ACCESS METHOD` verified byte-identical vs
      live PG 18.3, plus a built-in-AM-not-dumped regression guard).
      Gates: `go build`/`go vet` clean; `internal/parser`+
      `internal/catalog`+`internal/executor`+`internal/planner`+
      `internal/initdb` suites PASS; `TestPort_PgDumpConnectionSetup`
      PASS (7.5s); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); full
      pre-commit gate incl. pgbench TPC-B/simple-update/select-only smoke
      (0 failed transactions across 5181+6815+398183 transactions) PASS.
      Design doc: `docs/design/0119-0004-access-method-roundtrip.md`.
      Deferred (ledger row appended): no WAL/restart persistence yet — the
      `accessMethods` registry is a plain in-process map, so a `CREATE`/
      `DROP ACCESS METHOD` vanishes on server restart (a dump taken before
      a restart still round-trips correctly; only the live registry itself
      is non-durable). Resume point: add a WAL record + startup replay
      following the exact pattern already landed for sibling compat
      registries (event triggers loop #71, functions/procedures loops
      #73/74).
      **`CREATE`/`DROP ACCESS METHOD` WAL/restart persistence LANDED
      2026-07-02 (design `0119-0004-access-method-roundtrip.md` "Follow-up:
      WAL/restart persistence"):** closes the resume point directly above.
      New `RecordKindCreateAccessMethod`/`RecordKindDropAccessMethod` (kinds
      70/71, `internal/wal/recovery.go`) mirror the event-trigger pattern
      (loop #71); physical redo is a no-op (no page-level state). New
      `internal/initdb/access_method_ddl_recovery.go`
      (`replayAccessMethodDDLRecords`) + `RegisterAccessMethodDuringRecovery`/
      `DropAccessMethodDuringRecovery` catalog mutators
      (`internal/catalog/catalog.go`), wired into `internal/initdb/open.go`
      right after the function/procedure DDL replay call.
      `execCreateAccessMethod`/`execDropCompat`'s `"access method"` case
      (`internal/executor/operators_ddl.go`) now WAL-append on success.
      Tests: `internal/wal/access_method_ddl_test.go` (encode/decode round
      trips + wrong-kind/truncated-payload guards) +
      `internal/initdb/access_method_ddl_recovery_test.go` (4 tests: real
      `Init`/`Open`/`WAL.Append`/`Close`/re-`Open` round trips for CREATE and
      CREATE+DROP, plus missing-WAL-dir/nil-catalog no-op guards). Gates:
      `go build ./...` clean; `go test -race ./internal/wal/...
      ./internal/mvcc/...` PASS; `internal/wal`+`internal/catalog`+
      `internal/executor`+`internal/initdb`+`internal/planner`+
      `internal/parser`+`internal/server` suites PASS;
      `TestPort_PgDumpConnectionSetup` PASS (no regression); TPC-H
      spotcheck Q12=2/Q13=33 PASS; full pre-commit gate incl. pgbench
      TPC-B smoke PASS. Nothing left open on this family.

- [x] **`ALTER ROLE/USER … RENAME TO` restart persistence (root-0021
      follow-up, M0119-0004, interactive session).** **COMPLETE 2026-07-02:**
      closes the root-0021 ledger row's part (a) — RENAME TO previously fell
      through to the legacy compat no-op (`roleNameFromAlter` returned
      `hasAttrs=false`, so nothing was parsed, persisted, or even validated).
      New `renameRole`/`roleRenameFromAlter` (`internal/server/role_ddl.go`)
      intercept the RENAME TO form ahead of the attribute-form parse and
      mirror PostgreSQL's `RenameRole` (`postgres/src/backend/commands/
      user.c`) check order: role-exists (42704), reserved `pg_`-prefix on
      the new name (42939), new-name-already-exists/`postgres` (42710).
      Success re-keys three places together: the catalog role registry
      (new `catalog.InMemory.RenameRole`, preserves the OID so
      `pg_policy.polroles`/ownership references stay valid), `Server.roles`,
      and the live `auth.MapUserStore` credential. New
      `RecordKindAlterRoleRename` (kind 72, `internal/wal/recovery.go`) is
      the WAL tail entry; `internal/initdb/role_ddl_recovery.go` replays it
      after physical redo (a no-op, same as `RecordKindRoleState`/
      `RecordKindDropRole` — role DDL never touches the pg_authid heap
      directly at runtime, only the periodic full-registry
      `SyncPgAuthidFile` rewrite does). Renaming the bootstrap superuser
      (`postgres`) is rejected (`FeatureNotSupported`) since its name is
      hardcoded in several places (RoleOID, initdb's pg_authid seeding) —
      out of this slice's scope. Tests:
      `internal/server/role_ddl_rename_test.go`
      (`TestRoleRenameFromAlterParsing`/`TestAlterRoleRenameSuccess`/
      `TestAlterRoleRenameErrors`/`TestCatalogRenameRolePreservesOID`) +
      case (e) added to `TestPort_CreateRoleSurvivesRestart`
      (`internal/testport/role_auth_durability_test.go`: rename survives a
      real cluster restart, old name gone from `pg_roles`, attributes
      carried to the new name). Gates: `go build ./...` clean; `go vet`
      server/catalog/wal/initdb clean; targeted rename unit tests PASS;
      `TestPort_CreateRoleSurvivesRestart` PASS (via
      `scripts/goopg-test-run.sh`). Design doc updated:
      `docs/design/root-0021-role-auth-persistence.md` "Follow-up: ALTER
      ROLE/USER … RENAME TO restart persistence"; `docs/design/README.md`
      row updated. Deferred (ledger row appended): `SET`/`RESET` forms
      remain the legacy compat no-op (unrelated to rename, a distinct
      GUC-storage problem); role membership/CREATEDB/REPLICATION/etc
      attributes remain accept-and-ignore (unchanged); PG's
      session/current-user-cannot-be-renamed guard and the
      superuser-may-only-rename-superuser privilege check are not modelled
      (this SQL-string-level handler has no per-connection session-role
      context, same accept-and-ignore posture as every other role-DDL
      privilege check here).

- [x] **FOREIGN SERVER GRANT (`pg_foreign_server.srvacl`) round-trip in
      pg_dump (M0119-0004, DU-002 slice 427, loop #12).** **COMPLETE
      2026-07-02:** `GRANT USAGE ON FOREIGN SERVER <name> TO <role>` was
      silently dropped from every dump — `tryRecordTableGrant`/
      `tryRecordTableRevoke` classified the leading `foreign` keyword as a
      non-table object and bailed, and `pg_foreign_server.VirtualRows`
      hard-coded `srvacl` to `""` (NULL). Foreign servers now share the
      OID-keyed ACL store with relations/schemas/routines/types via the
      existing object-type-agnostic `relaclTextLockedFor` core: new
      `foreignServerACLPrivOrder`/`ownerForeignServerACLString = "U"`
      (owner-only default, no implicit PUBLIC — mirrors schema/table, not
      function/type) + `ForeignServerACLText(srvOID)`
      (`internal/catalog/catalog.go`); new `Catalog.ForeignServerOID`
      interface method (concrete impl already existed from slice 377); server
      gains `allForeignServerPrivileges = {"USAGE"}` + a `foreign`→`server`
      branch in `tryRecordTableGrant`/`tryRecordTableRevoke` dispatching to
      new `recordForeignServerGrant`/`recordForeignServerRevoke`
      (`internal/server/grant_ddl.go`, mirrors `recordSchemaGrant`/
      `recordSchemaRevoke`). USAGE is FOREIGN SERVER's sole privilege, so
      `buildACLCommands` collapses the full grant to `GRANT ALL ON FOREIGN
      SERVER goopg_srv TO srv_grantee;` (NOT `GRANT USAGE …`) — same as the
      single-privilege FUNCTION/EXECUTE case (slice 345). This loop picked up
      an uncommitted slice from the prior loop's background agent whose guard
      assertion had the wrong expected form (`GRANT USAGE …`); a standalone
      e2e repro (`internal/testport`, real cluster + real pg_dump 18.3)
      isolated the true expected output and the assertion/comments were
      corrected before landing. Tests: `TestForeignServerACLText`
      (`internal/catalog/relacl_test.go`: NULL with no grants, plain
      grant/grant-option/PUBLIC-grantee materialization, owner-side REVOKE
      ALL empties to `{}`); `TestPort_PgDumpConnectionSetup` DU-002 slice 427
      (byte-identical vs real pg_dump 18.3). Gates: `go build ./...` clean;
      `go vet` catalog/server/testport clean; `internal/catalog`+
      `internal/server` suites PASS; `TestPort_PgDumpConnectionSetup` PASS
      (5.8s); pgbench smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-foreign-server-grant-srvacl-pgdump.md`;
      `docs/design/README.md` row `0119-0004bq` added. Still open under
      M0119-0004 (unchanged, no new ledger row needed — piggybacks the
      existing M0119-0004-ACLHEAP thread): column-level (`pg_attribute.attacl`,
      heap re-sync) / database (`datacl`, `--create`-only) GRANT projection;
      `GRANT … ON FOREIGN DATA WRAPPER` (`fdwacl`) unmodelled;
      extended-protocol commit-time deferral.

- [x] **FOREIGN DATA WRAPPER GRANT (`pg_foreign_data_wrapper.fdwacl`)
      round-trip in pg_dump (M0119-0004, DU-002 slice 428, loop #13).**
      **COMPLETE 2026-07-02:** same-shaped gap one object class over from
      slice 427 — `GRANT USAGE ON FOREIGN DATA WRAPPER <name> TO <role>` was
      silently dropped from every dump because `tryRecordTableGrant`/
      `tryRecordTableRevoke`'s `foreign` branch only recognized a following
      `server` keyword, and `pg_foreign_data_wrapper.VirtualRows` hard-coded
      `fdwacl` to `""` (NULL). `OBJECT_FDW` is byte-identical in shape to
      `OBJECT_FOREIGN_SERVER` in PG's `acldefault` (world default
      `ACL_NO_RIGHTS`, owner-only `USAGE`), so the fix mirrors slice 427
      exactly: new `foreignDataWrapperACLPrivOrder`/
      `ownerForeignDataWrapperACLString = "U"` + `ForeignDataWrapperACLText`
      (`internal/catalog/catalog.go`, delegates to `relaclTextLockedFor`); new
      `Catalog.ForeignDataWrapperOID` interface method (concrete impl already
      existed from slice 375); server gains
      `allForeignDataWrapperPrivileges = {"USAGE"}` + a `data`→`wrapper`
      sub-branch alongside the slice-427 `server` sub-branch, dispatching to
      new `recordForeignDataWrapperGrant`/`recordForeignDataWrapperRevoke`
      (`internal/server/grant_ddl.go`, mirrors
      `recordForeignServerGrant`/`recordForeignServerRevoke`). USAGE is FDW's
      sole privilege, so `buildACLCommands` collapses the full grant to
      `GRANT ALL ON FOREIGN DATA WRAPPER goopg_fdw TO fdw_grantee;`, same as
      the FOREIGN SERVER case. Tests: `TestForeignDataWrapperACLText`
      (`internal/catalog/relacl_test.go`); `TestPort_PgDumpConnectionSetup`
      DU-002 slice 428 (byte-identical vs real pg_dump 18.3). Gates:
      `go build ./...` clean; `internal/catalog`+`internal/server` suites
      PASS; `TestPort_PgDumpConnectionSetup` PASS (5.7s, uncached); pgbench
      smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-foreign-data-wrapper-grant-fdwacl-pgdump.md`;
      `docs/design/README.md` row `0119-0004br` added. Foreign-server- and
      FDW-level GRANT round-trip are both now fully modelled under
      M0119-0004. Still open (unchanged): column-level (`pg_attribute.attacl`,
      heap re-sync) / database (`datacl`, `--create`-only) GRANT projection;
      extended-protocol commit-time deferral (M0119-0004-ACLHEAP).

- [x] **DATABASE GRANT (`pg_database.datacl`) round-trip in pg_dump
      (M0119-0004-ACLHEAP, datacl half, loop #16).** **COMPLETE 2026-07-02:**
      closes the last object class left open under M0119-0004-ACLHEAP (loop
      #89's ledger row had marked `datacl` "PERMANENTLY DEFERRED" — its ACL
      section is only emitted by pg_dump under `-C`/`--create`, and no test
      harness had ever exercised `--create`). Landed the typacl/srvacl/
      fdwacl-template GRANT/REVOKE parser capture + executor writer
      (`internal/executor/operators_ddl_database_acl.go`) + `DatabaseACLText`
      catalog renderer + heap-decode hook, AND built the first `pg_dump
      --create` test harness (`TestPort_PgDumpDatabaseGrantACL`), which
      exposed that goopg's SQL-visible `pg_database` virtual catalog was
      missing 8 columns pg_dump's `--create` query needs (`datcollate`/
      `datctype`/`datlocprovider`/`datlocale`/`daticurules`/`datcollversion`/
      `dattablespace`/`datacl`) and that two catalogs it queries directly
      (`pg_shseclabel`, `pg_db_role_setting`) were never registered — both now
      correctly-empty virtual tables. Added `shobj_description` builtin. A
      mid-loop regression (changing the displayed `postgres` row's oid to
      match the ACL-store key broke `TestPort_PgDumpConnectionSetup`'s CREATE
      SUBSCRIPTION round-trip via `subdbid`) was caught by stash-comparing
      against the pre-change tree and fixed by decoupling the display-oid
      from the internal `c.DBOID()`-keyed ACL lookup. Tests:
      `TestParseGrantDatabaseACL`/`TestParseGrantNonDatabaseLeavesDatabaseACLChangeNil`
      (`internal/parser`); `TestDatabaseACLText`/`TestDatabaseACLRevokeFromOwner`
      (`internal/catalog`); `TestPort_PgDumpDatabaseGrantACL`
      (`internal/testport`, byte-identical vs real pg_dump 18.3 `--create`).
      Gates: `go build`/`go vet` clean; `internal/catalog`+`internal/parser`+
      `internal/executor`+`internal/planner`+`internal/initdb` suites PASS;
      `TestPort_PgDumpConnectionSetup` PASS (regression-checked);
      `TestPort_IsolationIntraGrantInplaceDb` PASS; `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33); pgbench smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-database-grant-datacl-pgdump.md`;
      `docs/design/README.md` row `0119-0004bs` added. With typacl/attacl/
      relacl/nspacl/proacl/srvacl/fdwacl/datacl all landed,
      M0119-0004-ACLHEAP's object-class coverage is complete. Still open
      (deferral ledger row appended): `datacl` only round-trips for the single
      live-connected database (no true multi-database support); `ALTER
      DATABASE ... SET`/`SECURITY LABEL` remain unimplemented (now
      correctly-empty catalogs, not missing-relation errors);
      extended-protocol commit-time deferral (M0119-0004-ACLHEAP) remains
      open.

- [x] **`ALTER DATABASE ... SET`/`RESET`/`RESET ALL` (`pg_db_role_setting.setconfig`)
      round-trip in pg_dump (M0119-0004-ACLHEAP follow-up, loop #17).**
      **COMPLETE 2026-07-02:** closes the loop #16 datacl-half row's own
      "`ALTER DATABASE ... SET` remains unimplemented" residual. goopg's
      parser has no `ALTER DATABASE` grammar at all, so `SET`/`RESET`/
      `RESET ALL` are intercepted at the wire-protocol dispatch layer
      (`parseAlterDatabaseConfig`, `internal/server/database_ddl.go`, mirrors
      the existing CREATE/DROP DATABASE string-prefix bypass) rather than
      teaching `parseAlter` a new statement shape; every other `ALTER
      DATABASE` sub-form (`CONNECTION LIMIT`, `RENAME TO`, `OWNER TO`, ...)
      stays unrecognised and keeps falling through to the pre-existing
      `compatNoopCommandTag` no-op. New `catalog.InMemory.dbRoleSettings`
      store (`SetDatabaseConfig`/`ResetDatabaseConfig`/
      `ResetAllDatabaseConfig`/`DatabaseConfigEntries`, in-place upsert by
      case-insensitive GUC name) backs the previously permanently-empty
      `pg_db_role_setting.VirtualRows`. Keying gotcha caught by the pg_dump
      round-trip test, not assumed: the store must key by
      `catalog.FirstUserOID` (16384, the SQL-visible placeholder oid
      `pg_database` displays), NOT `catalog.InMemory.DBOID()` (the real
      on-disk oid `datacl`'s heap resync uses) — `dumpDatabaseConfig` issues
      a separate query cross-referencing the oid a prior `pg_database` query
      already read, unlike `datacl` which is read in the same row/query.
      Three new WAL kinds (73-75) + `internal/initdb/database_config_recovery.go`
      give it restart persistence. Tests: `internal/catalog/database_test.go`
      (5 new); `internal/server/database_ddl_test.go` `TestParseAlterDatabaseConfig`
      (7 positive + 6 negative); `internal/wal/database_config_ddl_test.go`;
      `internal/initdb/database_config_recovery_test.go`;
      `internal/testport/pgdump_database_config_test.go`
      `TestPort_PgDumpDatabaseConfigSet` (byte-identical vs real pg_dump 18.3
      `--create`). Gates: `go build`/`go vet` clean; `internal/catalog`+
      `internal/server`+`internal/wal`+`internal/initdb` suites PASS;
      `TestPort_PgDumpDatabaseConfigSet`/`TestPort_PgDumpDatabaseGrantACL`/
      `TestPort_PgDumpConnectionSetup` PASS; `scripts/tpch-spotcheck.sh` PASS
      (Q12=2/Q13=33); pgbench smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-database-config-set-pgdump.md`;
      `docs/design/README.md` row `0119-0004bt` added; deferral ledger row
      appended. Still open: extended-protocol path has no equivalent hook
      (standing M0119-0004-ACLHEAP gap); `ALTER ROLE ... SET`/`ALTER ROLE ...
      IN DATABASE ... SET` (`setrole != 0`) entirely unimplemented; `SET TIME
      ZONE`/`SET SESSION AUTHORIZATION`/`SET ... FROM CURRENT` special forms
      unrecognised (fall through to the no-op); multi-database scope
      unchanged from `datacl`.

- [x] **`ALTER ROLE ... [IN DATABASE ...] SET`/`RESET`/`RESET ALL`
      (`pg_db_role_setting.setconfig`, `setrole != 0`) round-trip in pg_dump
      (M0119-0004-ACLHEAP follow-up, interactive session).** **COMPLETE
      2026-07-02:** closes the loop #17 row's own "`ALTER ROLE ... SET`/
      `ALTER ROLE ... IN DATABASE ... SET` entirely unimplemented" residual —
      the `setrole != 0` complement of `pg_db_role_setting`.
      `parseAlterRoleConfig` (`internal/server/role_ddl.go`) mirrors
      `parseAlterDatabaseConfig`'s wire-dispatch bypass (reusing
      `database_ddl.go`'s tokenizer helpers), tried first in
      `tryHandleRoleDDL`'s `alter role`/`alter user` case, ahead of RENAME and
      the attribute form. `tryHandleRoleDDL` gained a `dbName` parameter
      (both `dispatch.go` call sites now pass `connTx.DBName`) so `IN
      DATABASE` gets the same v0-scope restriction as `ALTER DATABASE ...
      SET`/`datacl`: naming any database other than the connection's own is a
      silent no-op. New `catalog.InMemory.roleSettings
      map[roleSettingKey][]string` (`roleSettingKey{RoleOID, DBOid}`, DBOid
      0=cluster-wide or `FirstUserOID`=`IN DATABASE`) backs `SetRoleConfig`/
      `ResetRoleConfig`/`ResetAllRoleConfig`/`RoleConfigEntries`, mirroring
      the `*DatabaseConfig` quartet's exact semantics; `AllRoleConfigRows()`
      (sorted) feeds `pg_db_role_setting.VirtualRows` alongside the
      pre-existing `setrole=0` row. **Scope note (not a gotcha):** PG splits
      this catalog's dump across `pg_dump --create` (`IN DATABASE`,
      `setdatabase != 0`) and `pg_dumpall` (plain cluster-wide,
      `setdatabase = 0`); since M0119-0004 is the pg_dump-only TAP battery,
      the round-trip test exercises `IN DATABASE` only, though the engine
      plumbing (and unit tests) already cover `dbOid=0` too. Three new WAL
      kinds (76-78) + `internal/initdb/role_config_recovery.go` mirror the
      ALTER-DATABASE-SET restart-persistence pattern; ordering vs. role DDL
      replay doesn't matter (records key off the role's stable OID, not
      name). Tests: `internal/catalog/database_test.go` (4 new);
      `internal/server/role_config_test.go` `TestParseAlterRoleConfig` (9
      positive + 6 negative) + `TestTryHandleRoleDDLAlterRoleConfig`;
      `internal/wal/role_config_ddl_test.go`;
      `internal/initdb/role_config_recovery_test.go`;
      `internal/testport/pgdump_role_config_test.go`
      `TestPort_PgDumpRoleConfigSet` (byte-identical vs real pg_dump 18.3
      `--create`, confirms a cluster-wide override does NOT leak into the
      pg_dump-only surface). Gates: `go build ./...`/`go vet` clean;
      `internal/catalog`+`internal/server`+`internal/wal`+`internal/initdb`
      suites PASS; `TestPort_PgDumpRoleConfigSet`/
      `TestPort_PgDumpDatabaseConfigSet`/`TestPort_PgDumpDatabaseGrantACL`/
      `TestPort_PgDumpConnectionSetup` PASS; `scripts/tpch-spotcheck.sh` PASS
      (Q12=2/Q13=33); pgbench smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-role-config-set-pgdump.md`;
      `docs/design/README.md` row `0119-0004bu` added; deferral ledger row
      appended. Still open: extended-protocol gap (standing); multi-database
      scope (standing); `pg_dumpall`'s cluster-wide dump surface untested
      (separate future TAP-porting task, not a new engine capability); `SET
      TIME ZONE`/`SET SESSION AUTHORIZATION`/`SET ... FROM CURRENT`
      unrecognised; `ALTER ROLE ALL SET ...` unsupported.

- [x] **`pg_dumpall --globals-only` unblocked; cluster-wide `ALTER ROLE ...
      SET` round-trip (M0119-0004-ACLHEAP follow-up, interactive session).**
      **COMPLETE 2026-07-02:** closes the prior row's own "`pg_dumpall`'s
      cluster-wide dump surface untested" residual, which had wrongly
      assumed this was "pure TAP-porting work, not a new engine capability."
      Probing the real `pg_dumpall --globals-only` binary against goopg
      failed immediately with `relation "pg_authid" does not exist` —
      `pg_dumpall`'s `dumpRoles`/`dumpUserConfig` query `pg_authid` directly
      (not `pg_roles`), a 12-column relation goopg's virtual catalog had
      never registered. Three new virtual system catalogs
      (`internal/catalog/catalog.go`): `pg_authid` (OID 1260, sourced from
      the same live `c.roles`/`c.roleAttrs` state as `pg_roles`, NOT the
      on-disk `global/1260` crash-recovery heap file — a separate concern;
      `pg_roles`' stale placeholder OID reassigned to synthetic 1259102);
      `pg_auth_members` (OID 1261) and `pg_parameter_acl` (OID 6243)
      registered correctly-empty (role-membership GRANT and parameter-ACL
      GRANT are both genuinely unimplemented). With all three resolving,
      `pg_dumpall --globals-only` now runs to completion and correctly
      dumps cluster-wide `ALTER ROLE <name> SET <guc> TO <value>;` — the
      prior slice's `roleSettings` store already supported this without
      further change. Tests:
      `internal/testport/pgdumpall_role_config_test.go`
      `TestPort_PgDumpallGlobalsOnly` (real pg_dumpall 18.3 binary). Gates:
      `go build`/`go vet` clean; `internal/catalog`+`internal/executor`+
      `internal/server`+`internal/planner`+`internal/initdb` suites PASS;
      full `TestPort_PgDump*` regression set PASS; `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33); pgbench smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-pgdumpall-globals-only.md`;
      `docs/design/README.md` row `0119-0004bv` added; deferral ledger row
      appended. Still open: `GRANT <role> TO <role>` role membership (new
      grammar + storage); `GRANT ... ON PARAMETER` (same shape); seven
      unmodelled `pg_authid` attribute columns (report PG defaults, honest
      until `ALTER ROLE` gains real attribute parsing); standing
      M0119-0004-ACLHEAP items (extended-protocol gap, multi-database
      scope, `SET TIME ZONE`/etc.) unchanged.

- [x] **`GRANT`/`REVOKE` role membership (`pg_auth_members`) round-trip in
      pg_dumpall (M0119-0004-ACLHEAP follow-up).** **COMPLETE 2026-07-03:**
      closes the prior row's own "`GRANT <role> TO <role>` role membership —
      new parser grammar + `pg_auth_members` real storage" residual. Parser's
      shared GRANT/REVOKE token loop now tracks `sawOn bool` (role membership
      has no `ON <object>` clause, the discriminator vs. every other
      variant); `buildRoleMembershipChange` captures
      `CompatNoopStmt.RoleMembership`. Server's virtual-ACL fast path
      (`query.go`) now excludes any GRANT/REVOKE with no `" ON "` substring
      so it reaches the executor. New `execRoleMembershipChange`
      (`internal/executor/operators_ddl_role_membership.go`) resolves
      role/grantee names via `RoleOID` (unresolvable name incl. PUBLIC →
      42704, matching real PG), rejects a membership cycle incl. self-grant
      via new `catalog.InMemory.RoleIsMemberOf` (0LP01), and drives new
      `GrantRoleMembership`/`RevokeRoleMembership` on a
      `roleMembers map[roleMembershipKey]*RoleMembership` registry (mirrors
      `roleSettings`'s shape; re-grant upserts in place, admin_option never
      downgrades). `pg_auth_members.VirtualRows` now projects the live
      registry; `UnregisterRole` cascades membership-row removal. Two new
      WAL kinds (79/80) + `internal/initdb/role_membership_recovery.go`
      replay AFTER role DDL replay (not alongside `roleSettings`'s replay)
      since `GrantRoleMembership` mints a fresh OID at replay time and an
      earlier position risked colliding with a role OID loaded afterward.
      Tests: `internal/parser/op_grant_rolemembership_test.go`;
      `internal/catalog/role_membership_test.go`;
      `internal/wal/role_membership_ddl_test.go`;
      `internal/initdb/role_membership_recovery_test.go`;
      `internal/testport/pgdumpall_role_membership_test.go`
      `TestPort_PgDumpallRoleMembership` (byte-identical vs real pg_dumpall
      18.3, incl. the `WITH ADMIN OPTION, INHERIT TRUE, SET FALSE GRANTED BY
      postgres` clause and a revoked membership's correct absence). Gates:
      `go build ./...`/`go vet` clean; `internal/parser`+`internal/catalog`+
      `internal/wal`+`internal/initdb`+`internal/executor`+`internal/server`
      suites PASS; `TestPort_PgDumpallRoleMembership`/
      `TestPort_PgDumpallGlobalsOnly`/`TestPort_PgDumpRoleConfigSet`/
      `TestPort_PgDumpDatabaseConfigSet` PASS; `scripts/tpch-spotcheck.sh`
      PASS; pgbench smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-grant-role-membership.md`;
      `docs/design/README.md` row `0119-0004bw` added; deferral ledger row
      appended. Still open: `WITH INHERIT`/`WITH SET` clauses unparsed
      (always report PG defaults); grantor-chain (member-grantor loop)
      circularity check unimplemented (only role-member-loop is checked);
      `GRANT ... ON PARAMETER` unimplemented; `roleSettings`/
      `dbRoleSettings` not purged on DROP ROLE (pre-existing sibling gap,
      unrelated to this slice); standing M0119-0004-ACLHEAP items
      (extended-protocol gap, multi-database scope) unchanged.

- [x] **`UnregisterRole` purges `roleSettings` on DROP ROLE (M0119-0004-ACLHEAP
      follow-up, loop #4).** **COMPLETE 2026-07-03:** closes the prior row's
      own "`roleSettings`/`dbRoleSettings` not purged on DROP ROLE" residual
      — the `roleSettings` half only; `dbRoleSettings` is keyed purely by
      `DBOid` (setrole=0, `ALTER DATABASE ... SET`) with no per-role
      dimension, so there was nothing there to purge. `internal/catalog/
      catalog.go`'s `UnregisterRole` now also sweeps `c.roleSettings` for any
      `roleSettingKey` whose `RoleOID` matches the dropped role's OID
      (cluster-wide `dbOid=0` and IN-DATABASE `FirstUserOID` scopes both
      swept, mirroring the pre-existing `roleMembers` sweep alongside it).
      New test `TestUnregisterRoleDropsRoleConfigRows`
      (`internal/catalog/database_test.go`). Gates: `go build ./...`/`go vet`
      clean; `internal/catalog` suite PASS (incl. all `RoleConfig`/
      `UnregisterRole` tests); `scripts/tpch-spotcheck.sh` PASS (Q12=2/
      Q13=33); pgbench smoke = pre-commit hook. No design-doc-worthy new
      subsystem (a one-function bugfix in an already-documented registry);
      deferral ledger row appended. Still open: `WITH INHERIT`/`WITH SET`
      clauses, grantor-chain circularity check, `GRANT ... ON PARAMETER`,
      and the standing extended-protocol/multi-database-scope gaps — all
      unchanged from the prior row.

- [x] **`GRANT`/`REVOKE ... ON PARAMETER ...` (`pg_parameter_acl`) round-trip
      in pg_dumpall (M0119-0004-ACLHEAP follow-up).** **COMPLETE 2026-07-03:**
      closes the "`GRANT ... ON PARAMETER` unimplemented" residual carried by
      the three prior rows. Unlike TYPE/DATABASE, `pg_parameter_acl` has no
      heap relfilenode, so no re-sync step and no PUBLIC default seed are
      needed. New `parser.ParameterACLChange` (mirrors `DatabaseACLChange`)
      captures `GRANT/REVOKE {SET|ALTER SYSTEM|ALL} ON PARAMETER <names>
      TO/FROM <roles>`; names are raw dotted strings via new
      `splitTokDottedNames` (a GUC name's `.` is not a schema separator).
      `query.go`'s `isHeapACLObject` gained `" ON PARAMETER "` so the
      statement reaches the executor instead of the virtual-ACL fast path's
      no-op. New `catalog.InMemory.ParameterACLOID`/`ParameterACLText`/
      `ParameterACLEntries` mint a lazy synthetic OID per GUC name and share
      the existing `tableACLs` store; `pg_parameter_acl.VirtualRows` now
      projects real rows. New `execParameterACLChange`
      (`internal/executor/operators_ddl_parameter_acl.go`). Also added the
      `pg_get_userbyid` SQL builtin (`internal/executor/expr.go`, new
      `catalog.InMemory.RoleNameForOIDOrUnknown`), needed by
      `pg_dumpall`'s `dumpRoleGUCPrivs` query. Tests:
      `internal/parser/op_grant_parameteracl_test.go`;
      `internal/catalog/relacl_test.go`
      (`TestParameterACL{OID,Text,RevokeFromOwner,Entries}`,
      `TestRoleNameForOIDOrUnknown`);
      `internal/testport/pgdumpall_parameter_acl_test.go`
      `TestPort_PgDumpallParameterACL` (byte-identical vs real `pg_dumpall`
      18.3). Gates: `go build ./...`/`go vet` clean; `internal/parser`+
      `internal/catalog`+`internal/executor`+`internal/server` suites PASS;
      `TestPort_PgDumpallParameterACL` PASS; `scripts/tpch-spotcheck.sh`
      PASS; pgbench smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-grant-on-parameter-pgdumpall.md`;
      `docs/design/README.md` row `0119-0004bz` added; deferral ledger row
      appended. Still open: GUC-name validation against a real compiled-in
      parameter table (goopg has none, so any string is accepted); the
      grantor-chain circularity check and REVOKE's recursive/cascade
      dependent-privilege walk (both role-membership, unrelated to this
      slice) remain open, unchanged from prior rows.

- [x] **`GRANT ROLE` grantor-chain circularity check (M0119-0004-ACLHEAP
      follow-up, loop #18).** **COMPLETE 2026-07-03:** closes the
      "grantor-chain circularity check" residual carried by every
      M0119-0004-ACLHEAP row since the original `GRANT`/`REVOKE` role
      membership slice. New `catalog.InMemory.GrantRoleWouldCreateGrantorCycle`
      is a direct port of `AddRoleMems`' second cycle guard (`user.c` ~1751,
      `initialize_revoke_actions`/`plan_member_revoke`/`plan_recursive_revoke`),
      scoped to one `roleOid`'s `pg_auth_members` rows: simulates
      cascading-revoking every row the new grantee batch implicates, then
      checks whether the grantor's own `admin_option` row survives untouched;
      also unconditionally rejects granting ADMIN OPTION to the bootstrap
      superuser (new exported `catalog.BootstrapSuperuserOID = 10`).
      `execRoleMembershipChange` (`internal/executor/
      operators_ddl_role_membership.go`) runs this once per `roleOid` for the
      whole grantee batch — gated on `rc.AdminOption == true && grantorOid !=
      BootstrapSuperuserOID` — before applying any of that roleOid's grants,
      matching `AddRoleMems`' sanity-checks-then-whole-batch-admin-check-then-
      updates ordering; violation returns `0LP01`/PG's exact message ("ADMIN
      option cannot be granted back to your own grantor"). Tests:
      `TestGrantRoleWouldCreateGrantorCycle{,RetainsUntouchedAdmin,
      RejectsBootstrapSuperuserGrantee}` (`internal/catalog/
      role_membership_test.go`). Live end-to-end `psql` smoke against a
      running goopg instance confirmed the chained-cycle scenario errors
      while 1-hop and unrelated-role grants succeed. Gates: `go build ./...`/
      `go vet` clean; `internal/catalog`+`internal/executor`+
      `internal/parser`+`internal/server`+`internal/wal`+`internal/initdb`
      suites PASS; `scripts/tpch-spotcheck.sh` PASS; pgbench smoke =
      pre-commit hook. Design doc:
      `docs/design/0119-0004-grant-role-admin-circularity.md`;
      `docs/design/README.md` row `0119-0004ca` added; deferral ledger row
      appended. Still open: goopg's `roleMembers` map is keyed by `(RoleOID,
      MemberOID)` only (one row per pair) unlike real PG's `(roleid, member,
      grantor)` composite key, so the "retained via another grantor's row"
      escape hatch can never observe a second legitimate grantor for the same
      membership; `GRANT ... ON PARAMETER` GUC-name validation and REVOKE's
      recursive/cascade dependent-privilege walk (both unrelated to this
      slice) remain open, unchanged from prior rows.

- [x] **`REVOKE ROLE` CASCADE/RESTRICT dependent-privilege walk
      (M0119-0004-ACLHEAP follow-up).** **COMPLETE 2026-07-03:** closes the
      "REVOKE's recursive/cascade dependent-privilege walk" residual carried
      by every M0119-0004-ACLHEAP row since the REVOKE-OPTION-FOR
      generalization row. `CASCADE`/`RESTRICT` were parsed and trimmed but
      never read by the executor; new `parser.RoleMembershipChange.Cascade
      bool` is now populated by `buildRoleMembershipChange`, also fixing a
      latent ordering bug where `GRANTED BY <role>` unconditionally
      terminated the option scan so a trailing `CASCADE` after `GRANTED BY`
      was never reached. New
      `catalog.InMemory.RevokeRoleMembershipCascadeSet` is a read-only
      simulation of `plan_recursive_revoke` (`user.c`); `RESTRICT`/the
      unwritten default with dependents raises `2BP01` (hint "Use CASCADE to
      revoke them too."), `CASCADE` applies the full transitive chain first.
      Tests: `TestParseGrantRoleMembership` new cases;
      `TestRevokeRoleMembershipCascadeSet{NoAdminOptionNeverCascades,
      BlocksWithoutCascade,WalksTransitiveChain}`. Live end-to-end `psql`
      smoke confirmed both the block and the cascade path. Gates: `go build
      ./...`/`go vet` clean; full suite PASS; `scripts/tpch-spotcheck.sh`
      PASS; pgbench smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-revoke-role-cascade.md`; `docs/design/README.md`
      row `0119-0004cb` added; deferral ledger row appended. Still open:
      multi-grantor `roleMembers` keying and `GRANT ... ON PARAMETER`
      GUC-name validation, unchanged from prior rows.

- [x] **`pg_auth_members` multi-grantor rows (M0119-0004-ACLHEAP
      follow-up).** **COMPLETE 2026-07-03:** closes the "goopg's
      `roleMembers` map is keyed by `(RoleOID, MemberOID)` only" residual
      carried by every M0119-0004-ACLHEAP row since the original role-
      membership slice. `roleMembershipKey` (`internal/catalog/catalog.go`)
      gained a `GrantorOID` field, matching real PG's `(roleid, member,
      grantor)` unique index (`pg_auth_members_role_member_index`).
      `GrantRoleMembership` now mints an independent row for a DIFFERENT
      grantor granting the same `(role, member)` pair instead of silently
      overwriting the existing row's grantor (a real, demonstrable
      divergence from PG the old model had); a re-grant BY THE SAME grantor
      still upserts in place. `RevokeRoleMembership` gained a `grantorOid`
      parameter and only ever touches the one row identified by the full
      triple, leaving any other grantor's row untouched.
      `RevokeRoleMembershipCascadeSet` gained the same parameter and now
      returns `[]DependentRoleMembership{MemberOID, GrantorOID}` (was
      `[]uint32`) so cascade dependents are revoked at their own specific
      grantor — this also makes `plan_recursive_revoke`'s "would the member
      still hold admin via ANOTHER untouched row" escape hatch reachable for
      the first time. `UnregisterRole` (DROP ROLE) now also purges rows
      where the dropped role is the grantor (pre-existing gap fixed
      incidentally). `execRoleMembershipChange`
      (`internal/executor/operators_ddl_role_membership.go`) now resolves
      `grantorOid` once, shared by GRANT and REVOKE — REVOKE previously
      silently ignored `rc.GrantedBy` entirely.
      `EncodeRevokeRoleMembership`/`DecodeRevokeRoleMembership`
      (`internal/wal/recovery.go`) gained a `grantorOid` field (14-byte
      payload, up from 10) so a grantor-scoped REVOKE replays the correct
      row after a restart. Tests: `TestGrantRoleMembershipUpsertsInPlace`
      rewritten for same-grantor-upserts-in-place vs.
      different-grantor-mints-independent-row; new
      `TestRevokeRoleMembershipTargetsOnlyItsOwnGrantorRow`; cascade tests
      updated for the new parameter/return type
      (`internal/catalog/role_membership_test.go`); WAL round-trip tests
      updated (`internal/wal/role_membership_ddl_test.go`); new
      `TestRoleMembershipRecoveryReplaysMultiGrantorRows`
      (`internal/initdb/role_membership_recovery_test.go`). Gates: `go build
      ./...`/`go vet` clean; `internal/catalog`+`internal/executor`+
      `internal/wal`+`internal/initdb`+`internal/parser`+`internal/server`
      suites PASS; `scripts/tpch-spotcheck.sh` PASS; pgbench smoke =
      pre-commit hook. Design doc:
      `docs/design/0119-0004-role-membership-multi-grantor.md`;
      `docs/design/README.md` row `0119-0004cc` added; deferral ledger row
      appended. Still open: `check_role_grantor`'s inherited-privilege/
      superuser fallback for a bare REVOKE's implicit grantor (goopg always
      uses the effective DDL-owner role); `check_role_membership_
      authorization`'s ADMIN-OPTION-required permission check (any DDL-owner
      role can currently GRANT/REVOKE any role membership); `GRANT ... ON
      PARAMETER` GUC-name validation (unrelated to this slice) remain open.

- [x] **`check_role_membership_authorization` ADMIN OPTION permission gate
      (M0119-0004-ACLHEAP follow-up).** **COMPLETE 2026-07-03:** closes the
      "any DDL-owner role can currently GRANT/REVOKE any role membership"
      residual carried by every M0119-0004-ACLHEAP row since the original
      role-membership slice. New `catalog.InMemory.IsSuperuser(oid) bool` +
      `IsAdminOfRole(memberOid, roleOid) bool` (mirrors `is_admin_of_role`,
      `acl.c`: superuser member always true; a role is never its own admin;
      otherwise a BFS over `roleMembers` inheriting ADMIN OPTION transitively
      through any membership chain, matching `ROLERECURSE_MEMBERS`).
      `execRoleMembershipChange` (`internal/executor/operators_ddl_role_membership.go`)
      now calls new `checkRoleMembershipAuthorization` once per target role in
      both the GRANT and REVOKE branches, right after the role name resolves:
      a superuser-flagged target role requires a superuser grantor/revoker
      regardless of ADMIN OPTION; otherwise the invoking user must hold ADMIN
      OPTION on the target role. Both raise `42501` with PG's exact
      `errmsg`/`errdetail` text. Tests: `TestIsSuperuser`/`TestIsAdminOfRole`
      (`internal/catalog/role_membership_test.go`); new
      `internal/executor/operators_ddl_role_membership_test.go`
      (`TestExecRoleMembershipChangeRequiresAdminOption`,
      `TestExecRoleMembershipChangeSuperuserRoleRequiresSuperuserGrantor`,
      `TestExecRoleMembershipChangeRevokeRequiresAdminOption`). Live
      end-to-end `psql` smoke against a running goopg instance confirmed all
      scenarios match PG's exact error text. Gates: `go build ./...`/`go vet`
      clean; `internal/catalog`+`internal/executor`+`internal/parser`+
      `internal/wal`+`internal/initdb`+`internal/server` suites PASS;
      `TestPort_PgDumpallRoleMembership` PASS (unaffected — runs as the
      bootstrap superuser); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      pgbench smoke = pre-commit hook. Design doc:
      `docs/design/0119-0004-role-membership-admin-option-authz.md`;
      `docs/design/README.md` row `0119-0004cd` added; deferral ledger row
      appended. Still open: the `ROLE_PG_DATABASE_OWNER` "cannot have
      explicit members" carve-out (unreachable today — predefined roles are
      never registered in the live role-name registry); `check_role_grantor`'s
      inherited-privilege/superuser fallback for a bare REVOKE's implicit
      grantor; `GRANT ... ON PARAMETER` GUC-name validation (both unrelated to
      this slice).

> This task list is **seeded, not exhaustive.** M0119-0001 triage plus every
> future deferral-ledger entry (any new `status = -` row) feed additional M0119
> tasks over time. Finalize the themed-task set from the ledger's distinct open
> task-ids; the milestone's living nature means it need not be complete at filing.

---

## M0120 — WordPress WP-CLI Verification Execution & Evidence Capture (filed 2026-07-02)

Milestone: `docs/milestones/0120-wordpress-wpcli-verification-execution.md`.
Artifacts (committed): `wp/verification/CHECKLIST.md` (40 items — 32 write, 8
read) and `wp/verification/FLOW.md` (execution + evidence-capture procedure).
Depends on the landed statement-logging feature (`docs/design/root-0023-statement-query-logging.md`,
`GOOPG_LOG_STATEMENT=all`). Goal: run every checklist item against the live
WordPress-on-goopg stack, capture WP-CLI output + goopg statement log + PG4WP
SQL + a confirming read for **every** item (passing ones too), produce a
PASS/FAIL `report.md`, and triage each failure. No engine fixes here — that is
M0121. Run each capture through the memory cap (`scripts/goopg-test-run.sh`,
`GOOPG_CG_UNIT=goopg-wp`).

- [ ] **M0120-0001 — Verification harness + pre-run capture setup.** Implement
  FLOW.md §1–2: restart the wp goopg instance with `GOOPG_LOG_STATEMENT=all`
  (capped), enable PG4WP debug logging (`PG4WP_DEBUG=true`), snapshot baseline
  counts, and write the `run_item` capture script (byte-offset log slicing).
- [ ] **M0120-0002 — Execute + capture write items WP-01…WP-16** (posts, pages,
  post-meta, taxonomy, term-meta, user create/update). Store per-item evidence
  under `wp/verification/results/<ts>/`; record each confirming read.
- [ ] **M0120-0003 — Execute + capture write items WP-17…WP-32** (user
  role/delete, comments + comment-meta, options/transients incl. the TOAST-sized
  value WP-28, plugin activate/deactivate, raw INSERT/UPDATE/DELETE via
  `wp db query`). Watch WP-28 for a root-0022-class TOAST regression.
- [ ] **M0120-0004 — Execute + capture read items WP-R1…WP-R8** (list/get/count,
  `option get`, raw SELECT, `db size`/`core version`).
- [ ] **M0120-0005 — Aggregate `report.md` + triage.** Per-item PASS/FAIL; class
  each FAIL (`goopg-bug`/`goopg-missing`/`pg4wp-limitation`/`harness`, FLOW.md
  §4); for every goopg failure append a `.ralph/deferral_ledger.md` row and file
  the cross-referenced `M0121-NNNN` task (the M0120→M0121 handoff).

## M0121 — WordPress WP-CLI Verification Failure Remediation (filed 2026-07-02)

Milestone: `docs/milestones/0121-wordpress-wpcli-verification-remediation.md`.
Depends on M0120 (its `report.md` + the deferral rows it files). Goal: drive
every M0120 `goopg-bug`/`goopg-missing` failure to a verified PASS — fix the bug
or implement the missing capability in **goopg** (never PG4WP, never a
`goopg_compat` branch) — with a design doc (non-trivial) and a regression test,
then re-verify via `wp/verification/FLOW.md`. Reserve design-doc filenames
`docs/design/0121-NNNN-<slug>.md` (or `root-00NN-*.md` for cross-cutting engine
work) and index each in `docs/design/README.md`. Gates per change: units +
pgbench-smoke hook, `scripts/tpch-spotcheck.sh` for executor/planner/codec, race
gate for concurrency-critical packages.

- [ ] **M0121-0001 — Populate from M0120 triage.** This task list is **seeded,
  not exhaustive**: after M0120-0005, add one `M0121-000N` task per
  `goopg-bug`/`goopg-missing` failure (cross-referenced from its deferral-ledger
  row), each closing its ledger row (`- → resolved`) when the checklist item
  passes its confirming read on a fresh run and a regression test guards it.
  Failures classed `pg4wp-limitation`/`harness` are documented, not fixed in
  goopg.
