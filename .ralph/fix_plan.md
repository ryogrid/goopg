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
      **Remaining M0118-0009:** `intra-grant-inplace` (perm 10 — DELETE FROM
      pg_class virtual-tuple delete), `stats` (pg_stat_* subsystem).
