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
parked. **M0118 is currently in progress.** M0117's remaining sub-tasks are all
deferred Effort-L parts (need dedicated full-gate sessions — see each entry +
the deferral ledger).

Policy for **M0117 & M0118**: fix blockers in place; do NOT defer unless
genuinely compelling (then record a ledger row). Commit + push at every clean,
green (build + pre-commit) checkpoint.

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

- [x] **M0117-0001..0005** — DONE (designs `0117-0001..0005`; branches pending human
      merge off clean HEAD): wraparound-safe `storage.XIDPrecedes` horizon comparison;
      runtime CLOG-consulting visibility fallback; `pg_subtrans` restore-on-restart;
      `SUB_COMMITTED` (0x03) CLOG lane; incremental flush + group commit.
- [ ] **M0117-0006 — SLRU buffer pool / 2-bit collapse (gap G6; Effort L).** Part A
      landed (`transaction_buffers` GUC + `clogBufferPool`, NOT wired to the live path —
      blast radius nil). **Part B (DEFERRED, ledger 2026-06-15):** route `GetStatus`/
      `setStatus` + bulk callers / `loadFromSLRU` / `TruncateCLOG` through the pool
      (open Qs in design `0117-0006-*`: mirror-disabled fallback, OR-vs-clear-then-set
      semantics, truncation-via-page-invalidation). **Part C (DEFERRED):** remove the
      resident `banks` + `global/pg_xact` flat file (16× memory reduction). Re-init data
      dir on the memory-model change.
- [ ] **M0117-0007 — Async-commit LSN tracking (gap G8; Effort L).** Part A landed
      (per-LSN-group tracking + page-write WAL barrier on the M0117-0006 pool, not live).
      **Part B (DEFERRED):** live `synchronous_commit=off` — wire `flushWAL` to the WAL
      writer, thread the commit-record LSN into `setStatusWithLSN`, drop the inline
      per-commit fsync. Defers WITH M0117-0006 Part B (the barrier only fires once the
      pool is the live store). Needs TPC-H + crash/standby E2E. Design `0117-0007-*`.
- [ ] **M0117-0008 — datfrozenxid persistence (Effort M).** Part A DONE (dual-store
      consistency for all 4 CLOG status codes, satisfied via the M0117-0004 chain;
      `clog_dual_store_consistency_test.go`). **Part B (DEFERRED, ledger 2026-06-15):**
      on-disk in-place `pg_database.datfrozenxid` at VACUUM end — goopg has no runtime
      shared-catalog RelFileNode resolver (pg_database is shared at `global/1262`), and a
      faithful `heap_inplace_update` needs buffer-lock + WAL + a PG-standby-attach E2E.
      Purely external (standby/tooling) parity — goopg's own CLOG truncation reads
      in-memory `cat.DatFrozenXID()` directly. 5-step plan in design `0117-0008-*`.

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
- [ ] **M0118-0002** — Predicate-lock granularity per access method / scan type:
      predicate-gin, predicate-gist, predicate-hash, predicate-lock-hot-tuple,
      index-only-scan, index-only-bitmapscan, partial-index.
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
- [ ] **M0118-0004** — Deadlock detection: deadlock-{hard,simple,soft,soft-2,parallel},
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
- [ ] **M0118-0005** — FK / referential-integrity concurrency: fk-contention,
      fk-deadlock{,2}, fk-partitioned-{1,2}, referential-integrity, ri-trigger,
      temporal-range-integrity. **PARTIAL (2026-06-22, design 0118-0023):** five
      specs promoted to pass-required (strict) with NO engine change —
      `referential-integrity`, `temporal-range-integrity`, `fk-snapshot`,
      `fk-contention`, `fk-deadlock2` already match PG 18.3 (FK KEY-SHARE-vs-non-key-
      UPDATE non-conflict rides the M0118-0003/0004 multixact lock-only producer; SSI
      specs ride the 40001 machinery). Switched 3 dedicated tests soft→`runIsoSpecStrict`
      + added `TestPort_IsolationFk{Contention,Deadlock2}`. **Remaining (deferred,
      ledger 2026-06-22):** `fk-deadlock` (goopg's FK-check KEY SHARE wait
      over-conflicts — INSERT-into-child blocks where PG proceeds; needs a
      non-conflicting KEY-SHARE join on the wait path), `ri-trigger` (user RI
      constraint-trigger firing), `fk-partitioned-1/2` (`ALTER TABLE ATTACH
      PARTITION` + partitioned-FK enforcement). Group stays open until those land.
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
- [ ] **M0118-0007** — Planner / output-format blockers: eval-plan-qual (planner
      RETURNING support), drop-index-concurrently-1 (EXPLAIN EXECUTE plan-format parity).
      **PARTIAL (2026-06-22, design 0118-0024):** `drop-index-concurrently-1` promoted
      to pass-required with NO engine change — it matches PG 18.3 byte-for-byte (DROP
      INDEX CONCURRENTLY two-phase invalidation + index→seqscan EXPLAIN plan-format
      fallback + READ COMMITTED visibility all already correct). Switched
      `TestPort_IsolationDropIndexConcurrently1` soft→`runIsoSpecStrict`. **Remaining
      (deferred, ledger 2026-06-22):** `eval-plan-qual` — a cross-table EvalPlanQual
      recheck returns `(0 rows)` where PG re-projects the updated row after a concurrent
      UPDATE (EPQ-over-join executor work, ~L1171 of expected). Group stays open.
- [ ] **M0118-0008** — DDL / VACUUM / maintenance concurrency: alter-table-{1,2,3,4},
      detach-partition-concurrently-{1,2,3,4}, partition-concurrent-attach,
      partition-drop-index-locking, reindex-concurrently{,-toast}, reindex-schema,
      multiple-cic, vacuum-{concurrent-drop,conflict,no-cleanup-lock,skip-locked},
      truncate-conflict, sequence-ddl, cluster-conflict{,-partition}, create-trigger,
      inherit-temp, plpgsql-toast.
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
      **Remaining (deferred):** `alter-table-4` (INHERITS + transactional-DDL
      cross-session visibility), `detach-partition-concurrently-{1,2,3,4}` +
      `partition-concurrent-attach` (transactional partition visibility),
      `partition-drop-index-locking` (pg_locks view parity),
      `reindex-concurrently-toast` (toast relations as catalog objects +
      `allow_system_table_mods`), `plpgsql-toast` (COMMIT in DO + detoast).
      Group stays open.
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
- [ ] **M0118-0009** — Misc / system-level specs: async-notify, timeouts, stats, horizons,
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
      **Remaining (deferred, ledger 2026-06-22):**
      (b) lock carry-forward on the non-HOT update paths (delete+insert /
      `UPDATE…FROM` / MERGE / upsert) — bounded follow-up, same narrow gate, not
      exercised by any current `port` spec. Plus the other M0118-0009 misc specs
      untouched (async-notify [LISTEN/NOTIFY unimpl], horizons [dollar-quote lexer +
      EXPLAIN JSON], intra-grant-inplace [catalog-row lock on GRANT tuple xmax],
      stats [pg_stat_* infra], temp-schema-cleanup [pg_my_temp_schema + temp
      cleanup], prepared-transactions [2PC]).
