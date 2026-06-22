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
- [x] **M0118-0003** — Row locking (FOR UPDATE/SHARE, SKIP LOCKED, NOWAIT): **COMPLETE.**
      All 20 specs PASS vs PG 18.3 (verified 2026-06-22): skip-locked{,-2,-3,-4},
      nowait{,-2,-3,-4,-5}, lock-nowait,
      tuplelock-{conflict,partition,update,upgrade-no-deadlock},
      lock-update-{delete,traversal}, update-locked-tuple, propagate-lock-delete,
      lock-committed-{update,keyupdate}. The cumulative multixact producer +
      subxact-scoped row-lock infra (M0118-0003/0004 slices) carried the final
      batch green; CSV rows already `pass`.
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
      **Remaining (deferred, ledger 2026-06-22):** `alter-table-{1,2,4}`
      (ADD/VALIDATE CONSTRAINT lock semantics; INHERITS), the `*-conflict` family —
      truncate/vacuum/cluster — (need CREATE ROLE/GRANT/SET ROLE privilege infra +
      permission-denied), `reindex-concurrently-toast` (`allow_system_table_mods`
      GUC + TOAST-relation reindex), partition specs (ATTACH/DETACH PARTITION),
      `vacuum-no-cleanup-lock` (reltuples accounting), `inherit-temp`
      (RELATION_IS_OTHER_TEMP exclusion), `plpgsql-toast` (TOAST in PL/pgSQL).
      Group stays open.
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
