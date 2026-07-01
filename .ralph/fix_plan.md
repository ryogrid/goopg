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
- [ ] **M0117-0006 — SLRU buffer pool / 2-bit collapse (gap G6; Effort L).** Part A
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
      Regression `clog_bufferpool_live_test.go`. **Part C (DEFERRED, ledger):** remove
      the resident `banks` (16× memory reduction) once the no-mirror unit tests are
      migrated; re-init data dir on the memory-model change. Box stays unchecked until
      Part C lands. **Follow-up LANDED 2026-06-29 (loop #12):** the
      `transaction_buffers` GUC value is now threaded into `CLog.SetCLOGBuffers`
      from `initdb.Open` (new `OpenOptions.TransactionBuffers`, read in `cmd/goopg
      start` via `intGUC`). Boot default 0 keeps the auto-16 floor (no behaviour
      change); a non-zero `postgresql.conf` override now sizes the live pool.
      Regression `TestTransactionBuffersFromGUC` + `TestSetCLOGBuffersSizesPool`.
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
- [ ] **M0119-0002 — CLOG store swap, Part B** (source: M0117-0006 / M0117-0007 /
      M0117-0008; see M0117 section + ledger rows). Live CLOG store swap (pool
      replaces banks) per the design-0117-0006 Part B blueprint. Highest blast
      radius (Hard-won Rule #1): dedicated full-gate session (`-race` mvcc+wal,
      xlog_replay, heterogeneous PG-standby E2E, fresh-server TPC-H Q12/Q13).
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

> This task list is **seeded, not exhaustive.** M0119-0001 triage plus every
> future deferral-ledger entry (any new `status = -` row) feed additional M0119
> tasks over time. Finalize the themed-task set from the ledger's distinct open
> task-ids; the milestone's living nature means it need not be complete at filing.
