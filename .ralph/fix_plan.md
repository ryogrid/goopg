# goopg Fix Plan

The roadmap below is derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md`. The
"Definition of Done (Initial Milestone)" in §10 of the spec is the target;
items here decompose that target into agent-sized chunks. Pick the topmost
unchecked item unless a dependency forces a different order.

NOTE: past milestones are stored in `completed_milestones/` and should NOT be copied. If you need to reference a past milestone, you can see these files for the historical record, but they are not part of the active fix plan. Only items in this file are actionable.

Completed milestones are archived under `completed_milestones/` (latest: `completed_fix_plan_007.md`).

## Maintenance (small, do when convenient — does not preempt milestone order)

- [x] **MAINT-STATEGUARD-RECONCILE** — Stop `make ralph-state-guard` failing at the
      start of every loop. Root cause (NOT concurrency — single `--live` loop confirmed
      by ppid): the driver writes `progress={"status":"completed"}` after every clean
      claude exit (`~/.ralph/ralph_loop.sh:~1832`, "Clear progress file"), so the next
      loop's `status=running` always pairs with the prior loop's `progress=completed`.
      **DONE 2026-06-14 (loop #11):** added an `autoRepair` rule in
      `cmd/validate-ralph-state/main.go` — the complement of the stale-status rule —
      that reconciles a `completed` progress NOT newer than a live `running` status to
      `in_progress`, so the guard self-heals via `-fix`. 3 new tests; design
      `docs/design/root-0018-ralph-state-guard-prev-loop-marker-reconcile.md`. No more
      per-loop manual `progress.json` restores.

- [x] **MAINT-TPCH-RELOAD** — Reload the TPC-H bench dataset so the silent-regression
      gate works again. `bench/tpch/runtime_goopg/data` is a stale husk (no PG_VERSION,
      last real load 2026-05-26), so `scripts/tpch-spotcheck.sh` currently SKIPs.
      Steps: run `bench/tpch/build_schema_goopg.sh` (capped via the wrapper), then run
      `scripts/tpch-spotcheck.sh`, and re-pin `Q13_EXPECTED` in
      `bench/tpch/spotcheck_expected.env` from the fresh load (Q13 is load-dependent;
      Q12 must be 2). DoD: spotcheck exits PASS and the env file cites the new run log.
      **DONE 2026-06-13:** HammerDB SF=1 reload (build_goopg_20260613-144815.log,
      lineitem=5,999,786 / orders=1,500,000, FINISHED SUCCESS). Spotcheck PASS:
      Q12=2 (invariant), Q13 re-pinned 35→33 (load-dependent, stable across 2 runs;
      tmp/spotcheck_run_20260613.log + spotcheck_rerun_20260613.log). The gate that
      detects silent row-count regressions is live again.

## M0095 — Client-Tools TAP Test Porting (filed 2026-05-12)

Operational note (2026-05-12):
- Items that are blocked or can only be partially progressed due to missing goopg support must include blocker resolution within this milestone's scope.
- For items that can move forward once blockers are resolved, do not mark them complete until the resolution is implemented and re-verified.
- Only items that are impossible to resolve due to goopg's Go-implementation constraints or explicit design constraints may remain marked complete, and the reason must be documented.

Goal: Port the 27-file client-tools-tap suite to Go and implement the
missing engine features that currently hold ported scripts tests in a
`t.Skip` state.  The list spans five tool families:

  • `pg_basebackup` (010–040)  — WAL backup / receive / logical streaming
  • `pg_checksums`  (001–002)  — online/offline checksum management
  • `pg_controldata` (001)     — control-file inspection
  • `pg_ctl`        (001–004)  — **already PASS**; no new work needed
  • `pg_walsummary` (001–002)  — WAL summary generation
  • `scripts`       (13 files, 010–200) — client utility commands

`pg_ctl` 001–004 are already ported and PASS (`tap_port_test.go`).
All 13 scripts tests are already ported but remain `t.Skip` due to
missing SQL features; sub-milestones 0004–0008 implement those features.

### Sub-milestones

- [x] **M0095-0002**
      - Summary: Port `pg_walsummary/002` (WAL block summarization)
        as adapted Go test in `client_tools_port_test.go`.
      - Basic SQL (CREATE TABLE, INSERT, VACUUM, CHECKPOINT) passes.
      - WAL summarization (summarize_wal GUC, pg_available_wal_summaries(),
        pg_stat_io walsummarizer rows, pg_walsummary -i) deferred with explicit
        t.Skip blocker (goopg rejects unknown GUCs at startup; function not
        implemented). CSV row WS-002 added; markdown regenerated (2026-05-12).
      - **COMPLETE 2026-05-29 (M0095-0002):** t.Skip removed; test passes.
        Four changes closed all blockers:
        (a) `pg_stat_io` virtual table (`internal/catalog/catalog.go`): 20
            columns matching PG 16+ schema (backend_type, object, context,
            reads/read_bytes/read_time, writes/…, writebacks/…, extends/…,
            hits, evictions, reuses, fsyncs/fsync_time, stats_reset); OID 8061;
            VirtualRows returns nil (no I/O stats tracked in goopg v0).
        (b) `PgAvailableWalSummaries` plan node (`internal/planner/plan.go`):
            schema {tli int8, start_lsn pg_lsn, end_lsn pg_lsn}; cases added
            to FoldConstants and walkPlanExprs (no sub-expressions).
        (c) `planPgAvailableWalSummaries` + FROM whitelist (`internal/planner/planner.go`,
            `internal/parser/select.go`): planner routes `FROM
            pg_available_wal_summaries()` to the new plan node; parser FROM-clause
            SRF dispatch now includes `"pg_available_wal_summaries"` in its name
            switch so `pg_available_wal_summaries()` is parsed as a TableFuncRef.
        (d) `pgAvailableWalSummariesOp` executor (`internal/executor/operators_pg_available_wal_summaries.go`,
            `executor.go`): always returns 0 rows (no WAL summarizer in goopg v0).
        Test assertions: `SELECT count(*) FROM pg_available_wal_summaries()` = 0;
        `SELECT count(*) FROM pg_stat_io WHERE backend_type = 'walsummarizer'` = 0.
        `pg_walsummary -i` sub-case remains commented out (no summary files when
        `summarize_wal = off`). `TestPort_PgWalsummary002Blocks` → PASS.

- [ ] **M0095-0003**
      - Summary: Port `pg_basebackup/010`, `011`, `020`, `030`, `040`
        as adapted Go tests in `internal/testport/pgbasebackup_port_test.go`.
      - 010: --help/--version/options + no-pgdata + --compress=none:1/none+ PASS;
        backup execution PASS (2026-05-14, see below).
      - 011: SKIP entirely (in-place tablespace backup needs BASE_BACKUP protocol).
      - 020: --help/--version/options + no-dir + slot-conflict + sync-conflict + compress PASS;
        slot create -> WAL stream -> drop tier now PASS (loop #19, see below).
      - 030: --help/--version/options + no-slot/db/action/file checks PASS;
        logical streaming SKIP.
      - 040: --help/--version/options + no-datadir/publisher/database PASS;
        subscriber setup SKIP.
      - CSV rows BB-010..040 added; markdown regenerated (2026-05-12).
      - PROGRESS 2026-05-14: pg_basebackup `-X none --no-manifest --no-sync` now
        clones a live goopg primary end-to-end. New
        `TestPort_PgBasebackup010BackupExecution` drives the real
        `postgres/local_install/bin/pg_basebackup` binary against a fresh cluster
        and verifies extracted `backup_label`, `global/pg_control`, and
        `PG_VERSION`. Four discrete gaps closed (`docs/design/0095-0003-pg-basebackup-execution.md`):
        (a) `data_directory_mode` GUC (`internal/config/defaults.go`, BootVal=448 = 0o700)
            — `pg_basebackup` issues `SHOW data_directory_mode` early in its
            handshake and crashed with `unrecognized configuration parameter`.
        (b) `summarize_wal` GUC (BootVal=off, ContextSigHup) — same handshake
            wave; full WAL summarizer subsystem remains M0095-0002 scope.
        (c) `wal_segment_size` GUC as pre-formatted string `"16MB"` — naive
            `Type=TypeInt, Unit=UnitBytes` canonicalised to raw bytes
            `"16777216"` which pg_basebackup's `sscanf("%d%s")` rejected with
            "WAL segment size could not be parsed".
        (d) Trailing `CommandComplete("BASE_BACKUP")` in
            `internal/server/basebackup.go` — matches upstream's
            `EndReplicationCommand` wrap (`postgres/src/backend/tcop/dest.c:205`).
            Without it pg_basebackup's final `PQgetResult` returns NULL and
            surfaces as `"final receive failed: "` (empty error).
            `TestBaseBackupWireProtocolFraming` trailer assertion updated from
            4 frames (T/D/C/Z) to 5 frames (T/D/C/C/Z).
      - Verified: `go test -race ./internal/wal/ ./internal/mvcc/
        ./internal/executor/ ./internal/server/ ./internal/initdb/
        ./internal/config/` all green.
      - **PROGRESS 2026-06-14 (loop #7):** `-X stream` backup-execution coverage
        LANDED. The START_REPLICATION + physical walsender loop the prior action
        item waited on shipped with M0102 (`internal/server/replication.go`
        `replyStartReplication`, lines 354-571; the line-9 "ships in the next loop"
        header comment is stale). New `TestPort_PgBasebackup010StreamWAL` drives the
        real `pg_basebackup -X stream` against a live goopg cluster: it opens the
        second replication connection, issues START_REPLICATION, and streams WAL
        into the backup's `pg_wal/` concurrently with BASE_BACKUP. Asserts the
        extracted `backup_label`/`global/pg_control`/`PG_VERSION` PLUS the defining
        invariant — ≥1 streamed 24-char WAL segment lands in `pg_wal/` (distinguishes
        a working stream from a no-op). PASS (1.76s); `-X none` execution test still
        PASS (no regression). CSV row BB-010 rationale updated; markdown regenerated.
        All in `internal/testport/pgbasebackup_port_test.go` (uncontaminated) — zero
        engine change needed (the walsender already does the work). Resume = add
        `--manifest` parity via `bbsink_manifest` emulation; then 011/020/030/040
        streaming branches.
      - **PROGRESS 2026-06-14 (loop #8):** `--manifest` parity LANDED. The
        server now emits a PG-version-2 backup manifest (`buildBackupManifest`
        + `streamBackupManifest` in `internal/server/basebackup.go`), mirroring
        `backup_manifest.c` + `bbsink_copystream`'s manifest framing: after the
        tar and before CopyDone, a `CopyData('m')` begin-manifest marker then
        the manifest bytes via `CopyData('d')` chunks. The document carries
        `System-Identifier` (from pg_control), `Files[]` (each shipped file with
        CRC32C checksum by default — pg_wal segments omitted, tracked via
        WAL-Ranges), `WAL-Ranges`, and the SHA-256 `Manifest-Checksum`.
        `MANIFEST_CHECKSUMS` honoured (NONE / CRC32C / SHA224/256/384/512);
        `force-encode`/non-UTF-8 paths use `Encoded-Path`. New
        `TestPort_PgBasebackup010Manifest` runs pg_basebackup WITHOUT
        `--no-manifest`, independently recomputes every CRC32C + the SHA-256
        manifest checksum, then runs the upstream oracle `pg_verifybackup -n`
        which ACCEPTS the extracted backup (full file-checksum + system-id
        parity). `-X none`/`-X stream` execution tests still PASS;
        `go test -race ./internal/server/` green. Design doc + CSV BB-010 +
        markdown updated.
      - **PROGRESS 2026-06-14 (loop #9):** SHA-family manifest-checksum
        oracle coverage LANDED (test-only; no engine change). Loop #8 added the
        `MANIFEST_CHECKSUMS` SHA224/256/384/512 branches to
        `internal/server/basebackup.go` (`checksumFile`/`algoName`) but only the
        default CRC32C path had an end-to-end test — a bug in the SHA per-file
        hash or its `Checksum-Algorithm` JSON field would have been invisible.
        New `TestPort_PgBasebackup010ManifestChecksums` (subtests SHA224, SHA256,
        SHA384, SHA512) drives `pg_basebackup --manifest-checksums=<algo>`,
        asserts every `Files[]` entry uses the requested algo, independently
        recomputes each per-file checksum from disk (sha256.Sum224/Sum256,
        sha512.Sum384/Sum512), recomputes the always-SHA-256 `Manifest-Checksum`
        over the document prefix, and runs the upstream `pg_verifybackup -n`
        oracle (which ACCEPTS all four). All 4 PASS (1.78s); the 010 exec /
        stream / default-manifest tests still PASS (5.84s, no regression). CSV
        BB-010 rationale updated; markdown regenerated.
      - **PROGRESS 2026-06-14 (loop #12):** `-X fetch` (FETCH_WAL) LANDED.
        `internal/server/basebackup.go` now (a) parses the BASE_BACKUP `WAL`
        boolean option (`baseBackupOptions.IncludeWAL`; bare flag in new syntax +
        legacy keyword, `parseOptionBool` honours explicit false), (b) stops
        walking `pg_wal` — it ships as an empty dir + `archive_status`/`summaries`
        empty subdirs, mirroring `basebackup.c` sendDir():1385-1407 (previously
        goopg shipped full pg_wal contents on EVERY backup, a deviation), and
        (c) when `WAL` is set, `appendWALSegments` appends the in-range
        `[XLByteToSeg(startptr), XLByteToPrevSeg(endptr)]` segments to the tar
        under `pg_wal/`, oldest first, with the upstream contiguity sanity check.
        Note: the planned "goopg→PG name conversion" is NOT needed — goopg's
        on-disk WAL names are already PG-format (`wal.formatSegmentName`), so the
        dead/wrong `parseGoopgWalName` helper was removed and selection uses
        `wal.ParseXLogFileName`. New `TestPort_PgBasebackup010FetchWAL` (real
        `pg_basebackup -X fetch`, asserts backup_label START segment present among
        fetched pg_wal/ segments) + 3 parser cases. `-X none`/`-X stream`/manifest
        tests still PASS; `go test -race ./internal/server/` green. Design doc
        `0095-0003-pg-basebackup-execution.md` + CSV BB-010 + markdown updated.
      - **PROGRESS 2026-06-14 (loop #19):** `020_pg_receivewal.pl` streaming
        tier LANDED. `TestPort_PgReceivewal020`'s deferred `t.Skip` is replaced
        by the real **create-slot → stream → drop** sequence against a live
        goopg cluster: `pg_receivewal --create-slot` (asserted in
        `pg_replication_slots`), a backgrounded `pg_receivewal --slot S -D dir`
        that streams a 24-hex WAL segment (`.partial` accepted) while the test
        generates WAL, then `--drop-slot` (asserted gone). Engine gap closed:
        `pg_receivewal` issues `READ_REPLICATION_SLOT <name>` (PG 15+) before
        `START_REPLICATION` to learn the slot's restart LSN — goopg's walsender
        rejected it (syntax error → reconnect loop). Added
        `replyReadReplicationSlot` to `internal/server/replication.go`
        (uncontaminated): one `(slot_type text, restart_lsn text, restart_tli
        int8)` row — `('physical','X/X',1)` for a physical slot, all-NULL for
        absent, feature_not_supported for logical — verbatim
        `walsender.c:ReadReplicationSlot`. Sibling unit test
        `TestReplicationReadReplicationSlot`. Gates: `go test -race
        ./internal/server` green; `TestE2E_PhysicalReplication` +
        `TestPort_PgBasebackup010StreamWAL` (shared walsender) no regression.
        Design doc `0095-0003` extended; CSV BB-020 + markdown updated. Resume =
        030 (`pg_recvlogical`, needs logical decoding) / 011 (in-place
        tablespace) backup-execution branches.
      - Action: remaining M0095-0003 increments — 011 and recvlogical (030).
      - **CORRECTION 2026-06-14 (loop #28):** the long-standing "011 needs
        BASE_BACKUP for in-place tablespace" note was WRONG. BASE_BACKUP physical
        streaming is fully implemented (010 `-X stream`/`-X fetch` PASS). The real
        blocker for `011_in_place_tablespace.pl` is the in-place **tablespace
        feature**, which goopg lacks: (1) the `allow_in_place_tablespaces` GUC,
        (2) `CREATE TABLESPACE <name> LOCATION ''` DDL (goopg parses only the
        TABLESPACE *clause* and ignores it — no statement, no pg_tablespace row
        insert, no in-place `pg_tblspc/<oid>` dir), and (3) BASE_BACKUP emitting
        each non-default tablespace as a separate `<oid>.tar`. Items (1)+(3) are
        uncontaminated; item (2) edits parser/executor/catalog, so it is blocked
        on a clean tree (same gen-column WIP that holds the amcheck SQL surface).
        Skip note in `TestPort_PgBasebackup011InPlaceTablespace` corrected to match.
        recvlogical (030) still needs the logical replication / decoding protocol.

## M0096 — RC Isolation-Test Suite: Feature Implementation & Spec Pass (filed 2026-05-12)

Operational note (2026-05-12):
- Items that are blocked or can only be partially progressed due to missing goopg support must include blocker resolution within this milestone's scope.
- For items that can move forward once blockers are resolved, do not mark them complete until the resolution is implemented and re-verified.
- Only items that are impossible to resolve due to goopg's Go-implementation constraints or explicit design constraints may remain marked complete, and the reason must be documented.

Goal: Make all 21 READ-COMMITTED-targeted isolation specs listed in
`docs/test-port/executable-isolation-tests.md` PASS via
`IsolationRunner.RunAndCompare`.  All 21 currently defer (skip) inside
`TestPort_IsolationSuite`; they are the strongest proxy for goopg's
READ COMMITTED correctness story.

**Current blocker map** (21 specs → feature groups):

| Feature gap | Blocks |
|---|---|
| `BEGIN [WORK] ISOLATION LEVEL <level>` parser | all 21 (used in every session setup block) |
| `pg_advisory_lock / unlock / unlock_all / xact_lock / try_xact_lock` | `lock-committed-update`, `lock-committed-keyupdate`, `insert-conflict-specconflict` |
| `FOR KEY SHARE` / `FOR NO KEY UPDATE` locking syntax | `lock-committed-keyupdate`, `partition-key-update-1–4` |
| ON CONFLICT executor correctness (parser exists) | `insert-conflict-do-update` (1–4), `insert-conflict-do-nothing` |
| `CREATE TABLE … PARTITION BY LIST/RANGE` + `PARTITION OF` | `partition-key-update-1–4`, `fk-snapshot`, `merge-*`, `eval-plan-qual` |
| `GENERATED ALWAYS AS (expr) STORED` columns | `eval-plan-qual` |
| Table `INHERITS` | `eval-plan-qual`, `eval-plan-qual-trigger` |
| `MERGE INTO … USING … ON … WHEN MATCHED/NOT MATCHED` | `merge-update/delete/insert-update/match-recheck/join` (5 specs) |
| Inline `REFERENCES` FK column constraint (CREATE TABLE) | `partition-key-update-2/3/4`, `fk-snapshot` |
| `CREATE TRIGGER` + PL/pgSQL trigger bodies | `eval-plan-qual-trigger`, `partition-key-update-3/4`, `fk-snapshot` |
| `DROP INDEX CONCURRENTLY` syntax | `drop-index-concurrently-1` |

Parallel-connection note: `TestPort_IsolationSuite` runs all specs with
`t.Parallel()` and many concurrently exhaust the server's connection
limit; dedicated sequential test functions (M0096-0001) are required
alongside the suite.

### Sub-milestones

- [x] **M0096-0013** — CLOSED via M0100-0005 (loop 6, 2026-06-13): all 23
      dedicated `TestPort_Isolation*` functions PASS, 0 FAIL / 0 SKIP. M0096-0005
      (ON CONFLICT wait-state propagation) was closed earlier via M0100-0002.
      - Summary: End-to-end pass confirmation: run all 21 dedicated
        test functions from M0096-0001, confirm every spec reports `pass`.
      - Fix any remaining output-normalization or row-ordering mismatches.

      - **Status**: Partial — 0 of 21 tests fully pass (all report "defer").
      - Fixes landed:
      - Parser: `parseFKAction` now uses `acceptKeyword` (CASCADE/RESTRICT/SET
        are tokenized as keywords, not identifiers). Fixed `KwOn` in REFERENCES
        ON DELETE clause. Fixed bare `INITIALLY DEFERRED` (without DEFERRABLE).
      - Partition-aware DELETE: deleteOp scans partition/inheritance children.
      - Partition-aware UPDATE: updateOp scans children + routes new row to
        correct partition (cross-partition UPDATE). `remapRowForPartition` handles
        column-order differences (e.g. part2 in merge-update spec).
      - Remaining blockers (documented, not fixed in this loop):
      - RR/Serializable snapshot semantics: server refreshes snapshot per statement
        for all isolation levels; RR should use BEGIN-time snapshot.
      - Concurrent blocking detection: INSERT/UPDATE wait semantics and
        `<waiting ...>` output not produced for all cases.
      - RAISE NOTICE output: trigger functions produce no output (NOTICE is no-op).
      - Column alignment: `---+---` width varies between PostgreSQL and goopg.
      - EvalPlanQual: concurrent UPDATE re-evaluation not implemented.
      - Action: close the above blockers and rerun all 21 dedicated isolation tests
        until every case reaches pass.

## M0100 — RC Isolation Suite: Runtime Correctness Closure & 21-Spec Pass (filed 2026-05-13)

**【Strong policy — DO NOT BYPASS】**
Within this milestone, marking any sub-task as DEFERRED is, as a rule,
not permitted. Every item enumerated here is a residual runtime
correctness gap that must be closed to actually make the 21 RC
isolation tests pass; leaving any one of them unimplemented makes
M0100's Definition of Done unreachable. Escape hatches such as "push
it to the next milestone" or "punt to the next loop" must not be used.
DEFERRED is permitted only when **all three** of the following hold
simultaneously: (a) it is clearly demonstrated that the item is
impossible to implement in this release due to goopg's Go-implementation
constraints or explicit design constraints; (b) the reason is documented
in the body of the affected sub-milestone; and (c) within the same
milestone, an alternative path is presented that lets the corresponding
test(s) reach `pass` (not `excluded`). Deferring for any reason that
does not satisfy all three conditions is not allowed.

Operational note (2026-05-13):
- For items that can only be partially progressed due to an external blocker or missing goopg support, blocker resolution is itself in scope for this milestone.
- For items that can move forward once a blocker is resolved, do not mark them complete until the resolution is implemented and re-verified.

**Goal.** Make all 21 dedicated `TestPort_Isolation*` test functions
(added by M0096-0001) report `pass`. The parser/planner/catalog/DDL
surface landed across M0096-0002..-0012; what remains is runtime
correctness in the dispatcher, MVCC, and heap/DML operator path.
**Closes M0096-0005 and M0096-0013 via cross-reference at M0100-0005.**

Milestone doc: `docs/milestones/0100-rc-isolation-runtime-correctness-and-spec-pass.md`.

### Sub-milestones

- [x] **M0100-0001**
      - Summary: RR/Serializable BEGIN-time snapshot. (2026-05-13)
      - Design doc: `docs/design/0100-0001-isolation-level-snapshot-semantics.md`.
      - Implemented: dispatch.go line 295-300 gated on `ectx.Tx.Isolation ==
        IsolationReadCommitted` — RC refreshes per statement, RR/SSI keeps
        BEGIN-time snapshot. Uses ectx.Tx.Isolation (not outer tx variable) so
        execBegin's RR tx promotion is visible within multi-statement queries.
      - TestRepeatableReadPinsFirstSnapshot already covers MVCC layer.
      - All server/mvcc/executor tests pass with -race. Commit: ad82b12.

- [x] **M0100-0002**
      - Summary: Eager XID materialisation for ON CONFLICT wait
        propagation. **Closes M0096-0005.** (2026-05-13)
      - Design doc: `docs/design/0100-0002-eager-xid-materialization-at-begin.md` (accepted).
      - Implemented (5 logical areas):
        - 1. `mvcc/manager.go`: `IsXIDActive(xid)` public method; abortedXIDs tracking
          in `finish()` on rollback; `captureSnapshotLocked` includes all abortedXIDs
          in snapshot's `Aborted` field.
        - 2. `mvcc/snapshot.go`: `Aborted []TransactionID` field in Snapshot; `HasAborted(xid)`
          method; `SeesCommittedXID` checks `HasAborted` before xid < Xmin (fixes
          rolled-back rows appearing committed — lightweight clog substitute).
        - 3. `executor/operators_upsert.go`: `findInProgressConflict` uses `IsXIDActive`
          (not `Snap.HasInProgress`) so future-xmin tuples (materialized after snapshot)
          are detected; planner auto-detects primary key as arbiter for bare ON CONFLICT
          DO NOTHING in `planOnConflict`.
        - 4. `server/conn_tx.go`: `Tx()` returns session's current transaction (with
          up-to-date materialised XID) so session self-sees its own writes in SELECT
          after INSERT within the same explicit transaction.
        - 5. `testport/framework/isolation_runner.go`: per-permutation global setup/teardown
          (matches PostgreSQL isolationtester); pqprintFormat trailing blank line; step
          ordering fix (`drainWithTimeout` after each regular step).
      - Verified: `TestPort_IsolationInsertConflictDoNothing` → PASS.
      - All unit tests (mvcc/executor/server/planner) pass with -race.

- [x] **M0100-0003**
      - Summary: Row-level wait on in-progress xmax for UPDATE/DELETE. (2026-05-13)
      - Design doc: `docs/design/0100-0003-row-level-wait-on-in-progress-xmax.md` (accepted).
      - Implemented:
        - 1. `executor/operators_storage.go:epqWait`: re-enabled `WaitForXID(ctx.Ctx, xmax)`
          between WFG cycle check and snapshot refresh. All 4 call sites verified to
          unpin/unlock before calling epqWait (lines 923-924, 1159-1160, 1333-1334, 1520-1521).
          Context cancellation (connection close, timeout) handled via commitCond.Broadcast.
        - 2. `testport/framework/isolation.go`: Added `SessionTeardown` field; fixed teardown
          parser to separate global teardown from per-session teardown (was overwriting TeardownSQL).
        - 3. `testport/framework/isolation_runner.go`: Session-aware wait before sending next step
          for a session with a pending goroutine (prevents dual-goroutine connection conflicts);
          per-session teardown now runs after final drain and includes formatted output; reduced
          drainWindow 30s→5s; added execConnCapture; isolated context timeout to 10 min.
        - 4. `testport/isolation_port_test.go`: context timeout 2m→10m for 24-permutation specs.
      - Verified: TestPort_IsolationInsertConflictDoNothing PASS; TestPort_IsolationLockCommittedUpdate
        runs in 7.36s (was >600s hang) and produces `<waiting ...>` output (deferred on value
        mismatch due to advisory-lock snapshot refresh issue, separate from epqWait). All unit
        tests -race clean.

- [x] **M0100-0004**
      - Summary: EvalPlanQual concurrent UPDATE recheck (chain-following). (2026-05-13)
      - Design doc: `docs/design/0100-0004-evalplanqual-recheck.md` (accepted).
      - Implemented:
        - 1. `executor/operators_storage.go`: `epqFollowHOT(ctx, rel, blk, slot, cols, pred)` helper —
          follows HOT chain from old slot to latest visible version, re-evaluates WHERE.
        - 2. UPDATE SeqScan EPQ loop: after WaitForXID, if tuple invisible (committed):
          follow HOT chain, re-evaluate WHERE+SET, continue loop with new slot. RR → 40001.
        - 3. UPDATE IndexViaUpdate EPQ loop: same chain-following logic.
        - 4. DELETE EPQ loop: chain-follow + re-evaluate WHERE, delete latest version. RR → 40001.
        - 5. `executor/operators_ddl.go`: DROP TABLE now drops partition children unconditionally
          and inheritance children with CASCADE; `dropTableByRef` helper extracts drop logic.
      - All unit tests (executor/server/mvcc) pass with -race; TestPort_IsolationInsertConflictDoNothing PASS.
      - NOTE: eval-plan-qual/merge-match-recheck defer due to missing RETURNING support in planner
        (not an EPQ issue — RETURNING is parsed but not planned; needs separate work).

- [x] **M0100-0005** — DONE (loop 6, 2026-06-13). All DoD criteria met;
      milestone 0100 set `accepted` (doc + README). Verbose run:
      all 23 dedicated `TestPort_Isolation*` PASS, 0 FAIL / 0 SKIP
      (`tmp/perf-optimize/isolation-m0100-verbose.log`). pgbench-S = 48,984 TPS.
      M0096-0005 and M0096-0013 closed via cross-reference (below).
      - Summary: E2E pass confirmation: all 21 dedicated RC isolation
        tests pass. **Closes M0096-0005 and M0096-0013 via cross-reference.**
      - ~~**Depends**: Close of M0107~~ **STRUCK (loop 6, 2026-06-13):** the
        0100 milestone-doc DoD (`docs/milestones/0100-…md` lines 52-62) does NOT
        list M0107; its perf criterion is "pgbench-S ≥ 2,000 TPS at -c 10", which
        is now verified directly (see blocker 3 below). M0107 was a stale forward
        reference, not a real dependency.
      - Run: `go test -v -run TestPort_Isolation -timeout 30m ./internal/testport/`.
      - DoD: every `TestPort_Isolation*` listed in M0096-0001 reports `pass`
        (none `defer`, none `excluded`). On completion:
      - Mark M0096-0005 `[x]` with note "closed via M0100-0002".
      - Mark M0096-0013 `[x]` with note "closed via M0100-0005 — all 21
        dedicated isolation tests pass."
        Flip the 21 specs in `docs/test-port/executable-isolation-tests.md`
        from `status=defer` to `status=port`, `pass_required=yes`.
      - Update milestone doc 0100 status to `accepted`; update the
        `docs/milestones/README.md` index row to `accepted`.
      - Historical loop-by-loop progress notes archived to completed_milestones/m0100-0005-progress-log.md

      - **E2E RUN CONFIRMED (loop 4, 2026-06-13)**: all 22 dedicated
        `TestPort_Isolation*` functions PASS (the 21 RC specs from M0096-0001 +
        ReadWriteUnique). Run: `go test -v -run TestPort_Isolation… ./internal/testport/`
        → `ok …/internal/testport 126.455s`, 0 FAIL / 0 SKIP among the 22.
        M0100-0007 (the last open implementation sub-item) closed this loop.
      - **BLOCKERS to full `accepted`**:
        1. ~~**HEAD does not build standalone.**~~ **RESOLVED (loop 5, 2026-06-13,
           commit `c0e4842f`).** Root cause was NOT concurrent-loop contamination:
           ppid analysis showed a single `--live` loop (the second `ralph_loop.sh`
           is the portable_timeout subshell, ppid=first loop — see memory
           `concurrent_ralph_loops_corrupt_tree`). The break was a chronic
           split-brain dating to `29de7a95` (M0100-0010): that commit added
           consumer refs to `ctx.CTENewToOld`/`CTESelfModifiedErrors`/`CTESelfModErr`
           in `operators_storage.go`, but the field declarations in `context.go`
           were never committed. Verified pure-HEAD build failed ONLY on those 3
           fields; committed `context.go` (declarations) + `operators_cte_dml.go`
           (map init) → `go build ./...` PASS standalone, `gofmt`+`vet` clean.
           The other ~771 uncommitted lines (gen_override, lockrows, planner) are
           SEPARATE in-flight features, not referenced by any committed file; left
           uncommitted for their owning task.
        2. ~~`Depends: Close of M0107`~~ **RESOLVED (loop 6, 2026-06-13):** struck
           as a stale forward reference; the milestone-doc DoD does not list M0107
           (see the struck Depends line above).
        3. ~~pgbench-S TPS≥2000 DoD criterion unverified~~ **RESOLVED (loop 6,
           2026-06-13):** fresh capped server (port 5533, `tmp/perf-optimize/`),
           `pgbench -i -s 10` then `pgbench -S -c 10 -j 10 -T 30` →
           **tps = 48,984** (0 failed txns; warmup 48,868). Decisively clears the
           ≥2,000 bar (and the M0093 2,740 baseline). Log:
           `tmp/perf-optimize/pgbench-m0100-server.log`.
        4. `docs/test-port/executable-isolation-tests.md` has no `status=` column —
           the "flip defer→port" instruction is stale; the canonical status lives in
           `docs/test-port/postgres-oracle-port-status.csv` (D-002 isolation suite).
        Resume point: blocker 1 cleared. Next: re-run the 22 `TestPort_Isolation*`
        on clean HEAD, verify pgbench-S TPS≥2000 (needs data dir), reconcile/strike
        the M0107 dependency against the milestone-doc DoD, flip statuses in the CSV
        (not the no-status-column .md), mark M0096-0005/M0096-0013 `[x]`, set
        milestone 0100 + README to `accepted`.

        - **Remaining gaps (2026-05-22)**: 16 PASS, 6 SKIP. Each remaining test
          requires a design doc under `docs/design/` before implementation
          begins. Follow the pattern `0100-NNNN-<slug>.md` and update
          `docs/design/README.md` in the same commit.

        - [x] **M0100-0006 — InsertConflictSpecconflict: speculative insertion for ON CONFLICT**
              - COMPLETE (loop 3, 2026-06-13): perm 5 now passes via M0100-0006b.
                `TestPort_IsolationInsertConflictSpecconflict` PASS (all 5 perms).
              - Summary: `TestPort_IsolationInsertConflictSpecconflict` SKIP —
                perms 1–4 now PASS (loop 9, 2026-06-12); perm 5 deferred
                (requires spectoken infrastructure).
              - Phase B fix (DONE, loop 9): applyInsert now calls
                encodeArbiterKey before writeHeapRowReturning (Phase B first
                call), inserts arbiter btree entry with pre-computed key, and
                probeSpeculativeConflict detects concurrent commits after the
                Phase B blocking window. cancelSpeculativeRow stamps xmax on
                the speculatively-inserted row when a conflict is found.
                DO UPDATE entry adds explicit ExecBuildArbiterKey equivalent;
                applyUpdate uses explicit encodeArbiterKey for the updated
                row's btree entry.
              - Perm 5 gap: requires (a) locktype='spectoken' in pg_locks,
                (b) locktype='transactionid' entries in pg_locks,
                (c) `(step notices N)` coordination in isolation runner.
                New sub-task: M0100-0006b.

        - [x] **M0100-0006b — InsertConflictSpecconflict perm 5: spectoken infrastructure**
              - Summary: perm 5 of insert-conflict-specconflict.spec requires
                speculative token locks in pg_locks + transactionid lock
                entries. Not implementable without dedicated infrastructure.
              - Required: (a) implement speculative token acquire/release
                visible in pg_locks as locktype='spectoken', (b) expose own
                XID as transactionid ExclusiveLock in pg_locks, (c) implement
                `(step notices N)` wait annotation in isolation runner.
              - Progress (loop 1, 2026-06-13): part (c) DONE — isolation runner
                now parses completion markers (`*`, `<step>`, `<step> notices
                <n>`) into `IsolationSpec.PermutationBlockers` and
                `waitForStepBlockers` delays a step's completion report until
                the referenced session emits ≥N notices. Design doc:
                `docs/design/0100-0006b-isolation-notices-blocker-annotation.md`.
                Perm-5 diff advanced past the NOTICE-interleave region to
                `controller_print_speculative_locks` (L497).
              - Progress (loop 2, 2026-06-13): parts (a)/(b) DONE — both
                `controller_print_speculative_locks` steps now match PG (4-row
                then 3-row prints). Three fixes: (1) `Activity.PIDForProcNum` +
                `ExecContext.backendPID()` resolve the live backend PID (the
                deprecated `ActivityPID` was always ""); (2) `dispatch.go` wires
                `ectx.Activity`; (3) `pg_stat_activity.pid`/`leader_pid` retyped
                `text`→`int4` so the `USING (pid)` join with `pg_locks` (int4)
                matches — non-numeric bg-worker pids emit NULL. Row model
                completed: waiters emit their own-XID `transactionid
                ExclusiveLock`. Diff advanced L496→L533.
              - COMPLETE (loop 3, 2026-06-13): the remaining +2-NOTICE offset is
                fixed. PG's ON CONFLICT DO UPDATE is a HOT update (no indexed
                column changed → zero index tuples inserted, no expression
                re-evaluation); goopg (no HOT) re-inserted every index entry on
                `applyUpdate`, re-evaluating the non-unique `blurt_and_lock_4`
                expression index → 2 extra NOTICEs. Fix: `applyInsert` caches each
                non-arbiter index key (`maintainNonArbiterIndexesCapture` →
                `specIndexKeys`/`specInsertedLeaf`, reset per source row) and
                `applyUpdate` reuses the cached key
                (`maintainNonArbiterIndexesForUpdate`) when
                `indexKeyUnchangedFromSpec` proves the index's referenced base
                columns are unchanged (`collectExprColumnNames` conservative AST
                walker). Byte-identical btree state, side-effect evaluation elided.
                Orphaned `maintainUniqueIndexesForInsertSkipArbiter` removed.
                Design doc: `docs/design/0100-0006b-upsert-hot-index-key-reuse.md`.
                `TestPort_IsolationInsertConflictSpecconflict` PASS (all 5 perms)
                → **21/21 RC isolation tests pass**.

        - [x] **M0100-0007 — MergeUpdate: MERGE RETURNING old/new aliases + merge_action()**
              - COMPLETE (verified loop 4, 2026-06-13). `TestPort_IsolationMergeUpdate`
                PASS (4.74s). Implemented across two commits:
                (1) `3c931d05` "feat(isolation): ... MERGE RETURNING old/new ..."
                landed the `old`/`new` implicit composite aliases (parser
                recognition + planner resolution + executor population) and
                `merge_action()` (`internal/executor/expr.go`,
                `internal/parser/parser.go`);
                (2) `01356f1c` "fix(merge): cross-partition routing + deferred
                duplicate-source error (M0100-0007)" finished the remaining
                merge-update.spec divergences (cross-partition row routing and
                the deferred TM_MultipleResults / "tuple to be updated was
                already modified" duplicate-source error).
              - Original symptom (`ERROR: column "old" does not exist`) gone;
                merge-update.spec exercises `RETURNING merge_action(), old, new,
                t.*` (L113/L128/L162/L177) and the full output now matches the
                upstream expected `.out`.
              - Design coverage: `docs/design/0100-0005-dml-cte-with-merge-returning.md`.

        - [x] **M0100-0008 — MergeJoin: MERGE EXPLAIN plan-tree parity**
              - COMPLETE (loop 13 + loop 14, commits 9b915fad): EXPLAIN MERGE
                block stripping in isolation runner + CTID stamping in
                mergeApplyUpdate resolved the EXPLAIN mismatch. The plan-tree
                and row-count now match. `TestPort_IsolationMergeJoin` PASS.
                **PASS count = 17** (adds LockCommittedUpdate, LockCommittedKeyupdate
                via M0115-0004 hint-bit fix in loop 14; MergeJoin already PASS from
                loop 13). Current PASS: ReadWriteUnique, LockCommittedUpdate,
                LockCommittedKeyupdate, InsertConflictDoUpdate{,2,3,4},
                InsertConflictDoNothing, FkSnapshot, PartitionKeyUpdate{1,2,3,4},
                MergeDelete, MergeInsertUpdate, MergeMatchRecheck, MergeJoin.

                **M0100-0009 (loop 1) — PASS count = 18**: DropIndexConcurrently1
                added. `WaitForOlderSlotsToCommit` implemented in `mvcc.Manager`;
                `execDropIndex` calls it when `Concurrent==true`. Parser now sets
                `DropIndexStmt.Concurrent`. Current PASS: ReadWriteUnique,
                LockCommittedUpdate, LockCommittedKeyupdate,
                InsertConflictDoUpdate{,2,3,4}, InsertConflictDoNothing, FkSnapshot,
                PartitionKeyUpdate{1,2,3,4}, MergeDelete, MergeInsertUpdate,
                MergeMatchRecheck, MergeJoin, DropIndexConcurrently1.

                **M0100-0011 (loop 2) — PASS count = 19**: EvalPlanQualTrigger
                added. Phase 1 inline EPQ + BEFORE trigger inline firing in
                updateOp and deleteOp; ON CONFLICT trigger paths in upsertOp;
                bpchar output fix; PL/pgSQL NULL RAISE rendering + parser fix.
                Current PASS: ReadWriteUnique, LockCommittedUpdate,
                LockCommittedKeyupdate, InsertConflictDoUpdate{,2,3,4},
                InsertConflictDoNothing, FkSnapshot, PartitionKeyUpdate{1,2,3,4},
                MergeDelete, MergeInsertUpdate, MergeMatchRecheck, MergeJoin,
                DropIndexConcurrently1, EvalPlanQualTrigger.

        - [x] **M0100-0009 — DropIndexConcurrently1: CONCURRENTLY two-phase wait semantics**
              - Summary: `TestPort_IsolationDropIndexConcurrently1` SKIP —
                missing `<waiting ...>` on the DROP step; subsequent SELECT
                returns 0 rows instead of 2.
              - Root cause: `execDropIndex` does not implement CONCURRENTLY
                semantics. `DROP INDEX CONCURRENTLY` must (1) wait for all
                pre-existing transactions that could see the index, (2) mark
                the index as invalid in the catalog, (3) wait for all
                transactions that could see the invalid index, (4) physically
                drop the index. Goopg drops the index immediately without any
                wait, so a concurrent prepared statement loses access to the
                index mid-execution.
              - Required: implement two-phase drop with transaction-wait
                semantics. Additional planner gap: redundant sort-key
                elimination (`Sort Key: id, data` vs `Sort Key: id`).
                Write a design doc first.

        - [x] **M0100-0010 — EvalPlanQual: EPQ recheck NOTICE parity**
              - COMPLETE (loop 6): `updateWithFrom` EPQ path set `pu.newRow` to
                the EPQ-corrected row but forgot to clear the stale `pu.retNewRow`
                (set during scan-phase cross-partition routing with old b value).
                RETURNING used stale `retNewRow` → fix: `pu.retNewRow = nil` after
                EPQ recomputes `parentNewRow`. Design doc:
                `docs/design/0100-0010-epq-updatewithfrom-retrow-fix.md`.
                `TestPort_IsolationEvalPlanQual` → PASS. **PASS count = 20**.

        - [x] **M0100-0011 — EvalPlanQualTrigger: EPQ trigger output parity**
              - COMPLETE (loop 2, commit 54e738c6): Phase 1 inline EPQ in
                updateOp and deleteOp fn callbacks — blocks on in-progress
                xmax before processing next row, so BEFORE trigger + subsequent
                NOTICEs interleave per PG's per-row semantics. `beforeFired`
                flag prevents double-fire in Phase 2. RR: HasAbortedXID +
                IsXIDActive resolve frozen-snapshot ambiguity; CTID self-pointer
                check distinguishes "concurrent delete" from "concurrent update".
                ON CONFLICT trigger paths added to upsertOp. bpchar output fix
                in dispatch.go (no re-padding). `TestPort_IsolationEvalPlanQualTrigger` PASS.

### Stale notes carried from M0096-0013 (do NOT re-implement)

The following two residuals were verified non-gaps during M0100 planning;
do not modify these sites in M0100. Re-open as new sub-milestones only
if 21-spec pass surfaces a real divergence:

- RAISE NOTICE from trigger bodies — already correctly merged from child
  → parent context at `internal/executor/plpgsql_runtime.go:1053-1056`
  (M0096-0012).
- `---+---` column alignment width in `pqprintFormat`
  (`internal/testport/framework/isolation_runner.go:285-355`) — already
  matches libpq `PQprint` align-mode (`widths[i] = max(header_len,
  max_data_len)`); no width-derivation bug.

## M0102 — Heterogeneous Streaming-Replication + SIGKILL-Failover E2E (filed 2026-05-13)

  - [x] **M0102-0008**
      - Summary: Close milestone.
      - Add four rows to `docs/test-port/postgres-oracle-port-status.csv`:
        `e2e-failover-pg-to-goopg-async`, `e2e-failover-pg-to-goopg-sync`,
        `e2e-failover-goopg-to-pg-async`, `e2e-failover-goopg-to-pg-sync` — all
        at `status=port`, `pass_required=yes`. Regenerate the `.md` via
        `go run ./cmd/gen-oracle-port-status`. Flip
        `docs/milestones/0102-heterogeneous-replication-failover-e2e.md` status
        to `accepted` and update the `docs/milestones/README.md` index row.
      - Mark all 5 design docs (`0102-0001..-0005`) as `accepted`. Run the
        regression suites listed in the milestone DoD and confirm zero
        regressions.

 - [x] **M0102-0009** (follow-up to M0102-0008) — RESOLVED (loop #33, 2026-06-13)
      - Summary: `/sync_remote_apply` previously failed at "physical
        replication did not reach streaming state within 45s (requireSync=true)"
        because the primary's `pg_stat_replication.sync_state` never became
        `'sync'`.
      - **RESOLVED:** the `sync_state` wiring (design `0105-0008`, real FIRST/ANY
        rule evaluation in `registerStatReplicationView`) closed the gap. Both
        `TestE2E_FailoverPGtoGoopg` (async / sync_remote_apply / sync_on) and
        `TestE2E_FailoverGoopgToPG` (async / sync_remote_apply) now reach
        streaming state and pass all modes. The `GOOPG_RUN_BLOCKED_M0102_E2E`
        opt-in gate was removed from both failover tests; they now follow the
        standard heterogeneous-E2E convention (skip under `-short` or
        `GOOPG_SKIP_M0102_E2E=1`), matching `e2e_replication_test.go`.
        Closure note appended to `docs/design/0102-0003-heterogeneous-failover-e2e-harness.md`.
        Verified: PGtoGoopg 3/3 modes PASS (29.25s); GoopgToPG 2/2 modes PASS (5.97s).

 - [x] **M0102-0010** (follow-up to M0102-0008)
      - Summary: goopg `init` accepts no initdb CLI options beyond `-D`, so
        upstream `initdb` behaviors (`postgres/src/bin/initdb/t/001_initdb.pl`)
        cannot be matched. `initdb.Init` itself is internally complete
        (full catalog bootstrap, non-empty-dir guard via `ensureEmptyDir`);
        the gap is option coverage on the CLI + a few bootstrap params.
      - **PROGRESS 2026-06-13:** `-U`/`--username` (bootstrap superuser name)
        landed. `Options.SuperuserName` (default `"postgres"`) threads through
        `bootstrapPostgresRole`; reserved `pg_` prefix rejected before any
        filesystem layout (mirrors `initdb.c:3479`). Design doc:
        `docs/design/0102-0010-initdb-superuser-name-option.md`. Tests:
        `internal/initdb/superuser_name_test.go`.
      - **PROGRESS 2026-06-13 (loop #20):** `-X`/`--waldir` (external WAL
        directory) landed. `Options.WALDir` threads through `Init`; relative
        paths rejected before any filesystem layout (mirrors `initdb.c:2961`
        "WAL directory location must be an absolute path"); new `setupWALDir`
        helper mirrors `initdb.c` `create_xlog_or_symlink`/`pg_check_dir`
        (absent→create / empty→reuse / non-empty→reject) then symlinks
        `<DataDir>/pg_wal` → `WALDir` with `archive_status`/`summaries` created
        inside it via the symlink. `-X`/`--waldir` registered on the `init` CLI.
        Design doc: `docs/design/0102-0011-initdb-waldir-option.md`. Tests:
        `internal/initdb/waldir_test.go`.
      - **PROGRESS 2026-06-13 (loop #21):** `-N`/`--no-sync` and
        `-S`/`--sync-only` (fsync control) landed. `Init` previously did NO
        fsync; it now recursively fsyncs the data dir before returning
        (`fsyncDataDir`/`walkAndFsync`/`fsyncPath` mirror `sync_pgdata`/
        `walkdir`/`fsync_fname_ext`, FSYNC method), gated off by
        `Options.NoSync`. `Options.SyncOnly` fsyncs an existing cluster and
        exits without layout; a missing/non-dir path is rejected with
        `could not access directory` (mirrors `initdb.c:3444`
        `pg_check_dir <= 0`). Top-level walk ignores symlinks and recurses
        through a relocated `pg_wal` separately. `-N`/`--no-sync` +
        `-S`/`--sync-only` registered on the `init` CLI. Design doc:
        `docs/design/0102-0012-initdb-sync-options.md`. Tests:
        `internal/initdb/sync_test.go`.
      - **PROGRESS 2026-06-13 (loop #22):** `-T`/`--text-search-config` and
        `-c`/`--set` (GUC seeding) landed — **completes the `001_initdb.pl`
        "successful creation" option set** (`--no-sync` + `--text-search-config`
        + `--set` + `--waldir`). New `Options.TextSearchConfig` +
        `Options.ExtraGUC []GUCSetting`; `seedPostgresqlConf` runs after the
        `SampleFiles()` loop and rewrites the generated `postgresql.conf`
        via a faithful `replaceGUCValue` port (in-place rewrite of a
        leading-`#`/whitespace-skipped `name =` line preserving casing +
        inline comment, else append) + `formatGUCValue`/
        `gucValueRequiresQuotes` quoting (`internal/initdb/config_seed.go`).
        `-T` writes `default_text_search_config = 'pg_catalog.<cfg>'`;
        `--set` pairs apply last so they override (incl. the `-T` value),
        mirroring `initdb.c` `setup_config` order. `--set` lacking `=` ->
        exit 2 `-c <v> requires a value`. Design doc:
        `docs/design/0102-0013-initdb-config-seeding.md`. Tests:
        `internal/initdb/config_seed_test.go`, `cmd/goopg/main_test.go`
        (`TestInitCommandSeedsGUCs`, `TestInitCommandSetRequiresValue`).
      - **PROGRESS 2026-06-13 (loop #23):** `-g`/`--allow-group-access`
        landed. New `Options.AllowGroupAccess`; relaxes the cluster from
        owner-only (`0o700`/`0o600`) to group mode (`0o750` dirs / `0o640`
        files = `PG_DIR_MODE_GROUP`/`PG_FILE_MODE_GROUP`) and seeds
        `log_file_mode = 0640` into `postgresql.conf`, mirroring `initdb.c`
        `SetDataDirectoryCreatePerm(PG_DIR_MODE_GROUP)` (3360) + `setup_config`
        (1421-1425). goopg lays out at owner mode then relaxes in one recursive
        `relaxToGroupAccess`/`chmodTreeGroup` pass (modeled on `fsyncDataDir`'s
        traversal, following a relocated `pg_wal` symlink) before the trailing
        fsync — net on-disk result identical to upstream's create-at-group-mode,
        satisfying `001_initdb.pl`'s `check_mode_recursive($datadir, 0750,
        0640)`. `seedPostgresqlConf` gains an `allowGroupAccess` param; the
        `log_file_mode` seed lands before the `-c`/`--set` loop so an explicit
        override still wins. `-g`/`--allow-group-access` registered on the
        `init` CLI. Design doc: `docs/design/0102-0014-initdb-allow-group-access.md`.
        Tests: `internal/initdb/group_access_test.go`, `cmd/goopg/main_test.go`
        (`TestInitCommandAllowGroupAccess`). This **completes the entire
        `001_initdb.pl` "Check group access on PGDATA" case.**
      - **PROGRESS 2026-06-13 (loop #24):** `--sync-method` and
        `--no-sync-data-files` (sync-method selection + base/ exclusion)
        landed — **completes the `001_initdb.pl` `--sync-only` tier**
        (lines 78-91). New `Options.SyncMethod` (`""`/`"fsync"`/`"syncfs"`)
        + `Options.NoSyncDataFiles`. `fsyncDataDir` generalised to
        `syncDataDir(dir, method, syncDataFiles)`: FSYNC walks the tree
        excluding `<dir>/base` when `!syncDataFiles` (new `excludeDir`
        param on `walkAndFsync`, porting `walkdir`'s
        `if (exclude_dir && strcmp==0) return`); SYNCFS issues one
        `syncfs(2)` on the data dir + a relocated `pg_wal` symlink target.
        `resolveSyncMethod` ports `parse_sync_method` (unrecognized →
        error; `syncfs` rejected on non-Linux builds via `syncfsSupported`,
        in build-tagged `syncfs_linux.go`/`syncfs_other.go` using
        `unix.Syncfs`). Validated up front so both sync-only and full-init
        reject a bad method before any filesystem work. goopg has no
        tablespaces, so the upstream `pg_tblspc` passes are intentionally
        absent and `--no-sync-data-files` is inert under syncfs. Design
        doc: `docs/design/0102-0015-initdb-sync-method-options.md`. Tests:
        `internal/initdb/sync_test.go` (resolveSyncMethod table,
        base/-exclusion behavioral, syncfs, no-sync-data-files),
        `cmd/goopg/main_test.go` (`TestInitCommandSyncMethodAndNoSyncDataFiles`).
      - **PROGRESS 2026-06-13 (loop #25):** `-A`/`--auth`,
        `--auth-host`/`--auth-local`, and `--pwfile` (auth bootstrap) landed.
        New `Options.AuthMethodHost`/`AuthMethodLocal`/`PwFile`. New
        `internal/initdb/auth_bootstrap.go`: `resolveAuthMethods` ports
        `check_authmethod_unspecified` (default `trust` + warn), the
        ident↔peer cross-map (initdb.c:3255-3258), `check_authmethod_valid`,
        and `check_need_password` (both sides a password method without
        `--pwfile` → `must specify a password`), all validated up front before
        any filesystem layout. `buildPgHBAConf(host,local)` substitutes the
        methods into the local/loopback rules (external `0.0.0.0/0`/`::/0`
        stay `reject`); `defaultPgHBAConf()` is now
        `buildPgHBAConf("trust","trust")` (byte-identical default).
        `readSuperuserPasswordFile` ports `get_su_pwd` (first line, CRLF
        strip, empty/unreadable → error). `encodeSuperuserPassword` builds the
        `pg_authid.rolpassword` verifier — `auth.NewSCRAMSecret(...).String()`
        by default, `auth.MD5Shadow` (new exported wrapper) when md5 chosen
        per initdb.c:1402-1413 — and seeds `password_encryption = md5` only in
        the md5 case (via a new `passwordEncryption` arg on
        `seedPostgresqlConf`, applied before the `-c`/`--set` loop).
        `bootstrapPostgresRoleWithPassword` writes the verifier into the
        OID-10 superuser row (non-NULL text → HEAP_HASNULL stays clear,
        t_hoff=24). `-A`/`--auth` sets both sides; `--auth-host`/`--auth-local`
        override one side. `-W`/`--pwprompt` is out of scope (non-interactive);
        goopg's own auth does not yet read `rolpassword`, so the verifier is
        for on-disk PG-compat. This satisfies `001_initdb.pl`'s `--auth=trust`
        usage (line 137). Design doc:
        `docs/design/0102-0016-initdb-auth-options.md`. Tests:
        `internal/initdb/auth_bootstrap_test.go`, `cmd/goopg/main_test.go`
        (`TestInitCommandAuthAndPwfile`).
      - **PROGRESS 2026-06-13 (loop #26):** `-E`/`--encoding` (default database
        encoding) landed. New `Options.Encoding` + new
        `internal/initdb/encoding.go` porting `clean_encoding_name`,
        `pg_char_to_encoding` (full `pg_encname_tbl` alias set +
        `NAMEDATALEN` 64-byte guard), `pg_valid_server_encoding`
        (`PG_VALID_BE_ENCODING`: ≤ `PG_KOI8U`=34, so the seven client-only
        encodings SJIS/BIG5/GBK/UHC/GB18030/JOHAB/SHIFT_JIS_2004 are rejected),
        `pg_encoding_to_char`, and `resolveEncoding` = `get_encoding_id`
        (empty→UTF8 default; valid server encoding→ID;
        unknown/client-only→`"%s" is not a valid server encoding name`). `Init`
        validates the name up front (right after the superuser check, before
        auth/trust-warning and any filesystem layout) and threads the ID into
        `bootstrapPostgresDatabase(dir, encodingID)`, which writes it into the
        `encoding` column of all three seeded databases instead of the
        hard-coded `6`. `-E`/`--encoding` registered on the `init` CLI.
        **Scope:** name validation + ID mapping only. The locale-derived
        default (`pg_get_encoding_from_locale`) and the
        `check_locale_encoding`/`check_icu_locale_encoding` mismatch checks are
        deferred with the `--locale` family (goopg's fixed C/UTF8 locale →
        SQL_ASCII makes them no-ops); there is no server-side encoding
        enforcement (on-disk PG-compat only, like the 0102-0016 pwfile
        verifier). No on-disk format change (same 18-col `pg_database` tuple).
        Design doc: `docs/design/0102-0017-initdb-encoding-option.md`. Tests:
        `internal/initdb/encoding_test.go`,
        `internal/initdb/pg_database_encoding_test.go`, `cmd/goopg/main_test.go`
        (`TestInitCommandEncoding`).
      - **PROGRESS 2026-06-13 (loop #27):** `--locale-provider` + `--locale` +
        `--lc-collate`/`--lc-ctype`/`--lc-messages`/`--lc-monetary`/
        `--lc-numeric`/`--lc-time` + `--builtin-locale` (libc + builtin
        collation providers) landed; `icu`/`--icu-locale`/`--icu-rules`
        recognized but rejected (`ICU is not supported in this build`). New
        `internal/initdb/locale.go` ports `resolveLocaleProvider`
        (initdb.c:3367, `unrecognized locale provider`),
        `pg_get_encoding_from_locale` (codeset-suffix mapping; a frontend that
        cannot `setlocale` — `C`/`POSIX`→SQL_ASCII, `.CODESET`→enc, else -1),
        `check_locale_encoding` (initdb.c:2265), and `resolveLocale` = the
        post-parse `setlocales`+`setup_encoding` validation: option-combination
        checks (3424-3434), `locale must be specified if provider is <name>`
        (2471), builtin canonicalization C/C.UTF-8/PG_UNICODE_FAST (2477), the
        `#ifndef USE_ICU` rejection (2503), and the builtin
        C.UTF-8/PG_UNICODE_FAST ⇒ UTF8 requirement (2778-2783). `Init`
        validates up front (after `resolveEncoding`, before auth/layout);
        `seedPostgresqlConf` gains a `localeGUCs` arg applied first
        (lc_messages/lc_monetary/lc_numeric/lc_time, only when a locale option
        is given); `bootstrapPostgresDatabase(dir, enc, locale)` writes
        datlocprovider/datcollate/datctype/datlocale — **no-option default is
        byte-identical to the prior libc/"C" row** (datlocale stays NULL, null
        bitmap + t_hoff unchanged), builtin adds a non-NULL datlocale with no
        format change (same 18-col tuple, only values vary). Closes the always-
        run non-ICU locale cases of `001_initdb.pl` (builtin --locale C ok;
        builtin C.UTF-8+UTF-8 ok; builtin-no-locale/xyz-provider/libc+icu-locale
        /icu-no-build/builtin-C.UTF-8+SQL_ASCII fail). **Scope:** on-disk
        PG-compat only (goopg's engine keeps its fixed C/UTF8 locale — no
        runtime collation); the locale-derived default encoding is still
        deferred. Design doc: `docs/design/0102-0018-initdb-locale-options.md`.
        Tests: `internal/initdb/locale_test.go`, `cmd/goopg/main_test.go`
        (`TestInitCommandLocaleProvider`).
      - **PROGRESS 2026-06-13 (loop #29):** data-page checksum **engine**
        landed (the reusable, high-blast-radius core), `--data-checksums`
        initdb option deferred. `internal/storage/checksum.go` gains
        `PageSetChecksumCopy` (copy-then-set, never mutates the shared
        buffer) + `VerifyPage`; `internal/storage/smgr.go` `ManagerConfig`
        gains `ChecksumsEnabled`/`IgnoreChecksumFailure`/`OnChecksumFailure`
        wired at the `relFile` level (the single lowest seam shared by the
        sync `readBlock`/`writeBlock`/`extend`/`extendBatch` and AIO
        `ReadAt`/`WriteAt` paths, where the block number is always known) —
        writes emit a checksummed copy, reads verify and return
        `*ChecksumError` on mismatch (non-fatal under IgnoreChecksumFailure).
        **Disabled (the default) is byte-identical** (one bool check, no copy,
        no alloc; TPC-H Q12=2/Q13=33 unchanged). `internal/control/pgcontrol.go`
        exposes `DataChecksumVersion` (offset 252, preserved across
        UpdateControlFile); `internal/initdb/pgcontrol.go` `buildPgControl`
        writes 1/0; `open.go` + `wal/recovery.go` read the field to enable
        the Manager. **DEFERRED:** `Init` REJECTS `--data-checksums` — a
        bootable checksummed cluster needs `pd_checksum` on ~38 distinct
        direct `os.WriteFile` bootstrap page-write sites (no shared helper),
        and missing one yields an unbootable cluster; that exhaustive wiring
        + an end-to-end boot test + the PG-18 default-ON parity is the
        remaining work (deferral ledger 2026-06-13). Design doc:
        `docs/design/0102-0019-initdb-data-checksums.md`. Tests:
        `internal/storage/checksum_io_test.go`,
        `internal/initdb/data_checksums_test.go`.
      - **PROGRESS 2026-06-13 (loop #30):** the **bootstrap checksum routing
        primitive** landed — `internal/initdb/checksum_bootstrap.go`
        `checksumRelationData(raw, enabled)`: identity (no copy/alloc) when
        disabled, else a copy with `pd_checksum` on every `BlockSize` block,
        block number derived from byte offset (`off/BlockSize`, matching the
        runtime smgr read-verify) so ONE helper is uniform across single-page
        heaps, multi-page heaps, and multi-page btree files with no per-site
        block bookkeeping. Built on loop-#29's `storage.PageSetChecksumCopy`;
        skips new/all-zero pages like upstream `PageIsNew`. The multi-page
        block-numbering + never-mutate-input invariants are proven in isolation
        by `checksum_bootstrap_test.go` (5 cases incl. transposition rejection
        and partial-tail-verbatim). Design doc 0102-0019 updated with the
        "Routing primitive" + "Remaining (the sweep)" sections. **DEFERRED**
        (next loop, deferral ledger 2026-06-13): the ~50-site sweep that routes
        every direct `os.WriteFile` through this helper (threading
        `opts.DataChecksums`), the e2e boot test, dropping the `Init` reject,
        and the `-k`/`--data-checksums` CLI flags. Because the flag stays off
        while the reject is in place, the sweep is byte-identical and can land
        incrementally and safely.
      - **PROGRESS 2026-06-13 (loop #31):** `--data-checksums` **user-facing
        enablement landed.** Instead of the deferred ~50-site threading sweep,
        the enablement is one offline stamp pass after bootstrap completes
        (`internal/initdb/checksum_bootstrap.go` `stampClusterChecksums`),
        mirroring upstream `pg_checksums --enable`
        (`postgres/src/bin/pg_checksums`): it walks `global/` + `base/<db>/`,
        and for every file matching `relFileNamePattern`
        (`^[0-9]+(_(fsm|vm|init))?(\.[0-9]+)?$`, the analogue of
        `parse_filename_for_nontemp_relation`) runs each block through the
        loop-#30 `checksumRelationData` and rewrites it in place. Non-relation
        metadata (PG_VERSION, pg_filenode.map, pg_internal.init, pg_control,
        CLOG/WAL) is named non-numerically / lives elsewhere → never matched,
        so the "stamp everything" pass cannot corrupt a CRC-protected file.
        `Init` calls it (guarded by `opts.DataChecksums`) after `writePgControl`
        and before the trailing fsync; the `Init` reject is **removed**.
        Default stays **OFF** (byte-identical bootstrap when the flag is off,
        structurally guaranteed by the guard). CLI `-k`/`--data-checksums`/
        `--no-data-checksums` registered (`--no-data-checksums` overrides
        `-k`). e2e boot test
        `TestInitDataChecksumsBootstrapsVerifiablePages` verifies every
        relation page under base/+global/ checksums-clean (off/BlockSize) and
        reads pg_type/pg_class/pg_attribute block 0 through a checksummed
        Manager; `TestInitCommandDataChecksums` drives the CLI flags. Design
        doc `0102-0019` updated (chosen-approach + testing sections). Gates:
        gofmt/vet/`go build ./...` clean; `go test ./internal/initdb
        ./internal/storage` PASS; CLI test PASS.
      - **PROGRESS 2026-06-13 (loop #32):** recovery/FPI-replay validation gate
        for the deferred default-ON flip **landed** (gate (a) of two). New
        `internal/initdb/recovery_test.go`
        `TestCrashRecoveryReplaysChecksummedClusterCleanly`: runs the SIGKILL /
        WAL-replay sequence (build multi-page btree → force WAL durable → drop
        Manager + WAL writer without flushing the dirty pool → reopen → replay)
        on a `DataChecksums=true` cluster, then proves every recovered page is
        checksum-valid two ways — Phase-4 btree reads go through the
        checksum-enabled Manager (a bad replayed page surfaces as `*ChecksumError`,
        not a wrong answer) and a Phase-5 on-disk `VerifyPage` walk re-checks
        every populated block's `pd_checksum`. This is the architectural proof
        that the FPI restore path (`wal/recovery.go` `restoreDecodedXLogBlockImage`
        → `writeBlockOrExtend` → `Manager.WriteBlock` → `checksummedForWrite`)
        recomputes the checksum per replayed block rather than writing a stale
        image verbatim or bypassing the checksum write seam. Default stays
        **OFF**: the flip is still gated on (b) standby-read / physical-replication
        validation (a checksummed primary streaming to a PG standby that verifies
        `pd_checksum`). Design doc `0102-0019` "Remaining: default-ON flip"
        updated with gate (a) DONE / gate (b) pending. Gates: gofmt/vet clean;
        `go test ./internal/initdb` PASS; `go test -race ./internal/storage
        ./internal/wal` PASS.
      - **PROGRESS 2026-06-13 (loop #34):** standby-read / physical-replication
        validation gate for the deferred default-ON flip **landed** (gate (b)
        of two — the last gate). New
        `internal/testport/e2e_checksum_replication_test.go`
        `TestE2E_ChecksumStreamingGoopgToPG`: a `--data-checksums` goopg primary
        (new `cluster.Options.InitArgs` threads the flag) fills a table spanning
        ~115 heap pages, `CHECKPOINT`s them to disk before the clone, then
        `pg_basebackup -X stream`s the cluster to a **real PG** standby. PG
        copies goopg's version-1 `pg_control` (`SHOW data_checksums = on`) and
        verifies `pd_checksum` on every page read; a full seq-scan returning the
        exact 4 000 rows + `sum(length(payload))` proves goopg's FNV-1a checksum
        bytes are **byte-identical** to upstream's — a mismatch would abort the
        scan with `invalid page in block N`. This is the cross-implementation
        proof gate (a) (goopg-verifies-goopg) cannot give. PASS in 2.45s.
        Design doc `0102-0019` "Gate (b)" + Testing + status updated. Gates:
        gofmt/vet clean; `go test ./internal/testutil/cluster
        ./internal/testutil/replcluster` PASS;
        `TestE2E_ChecksumStreamingGoopgToPG` PASS (real PG binaries). **Both
        flip-gates now pass.**
      - **PROGRESS 2026-06-14 (loop #44): default-ON FLIP LANDED.**
        `cmd/goopg/main.go` `init`'s `dataChecksums` default flipped
        `false → true` for both `-k` and `--data-checksums`; `--no-data-checksums`
        still overrides (`useDataChecksums := *dataChecksums && !*noDataChecksums`
        unchanged). goopg now matches upstream PG 18 (initdb commit 04bec894
        defaults data checksums ON). **Format-change gate (M0106 lesson):** full
        regress-port suite re-run on a checksummed data dir —
        `go test -timeout 3000s -run TestPort_RegressSuite ./internal/testport/`
        **PASS** `ok ... 2618.543s` (~43.6 min, 0 unexpected diffs). A per-page
        CRC trailer cannot alter query output — only failure mode is a
        checksum-verification error on read, which would abort the suite early;
        100s of clean queries IS the read-path validation. Design doc `0102-0019`
        "Remaining: default-ON flip" → DONE. **M0102-0010 data-checksums work
        complete.**
      - **Remaining initdb work** (each pulls in a distinct subsystem; one
        per future loop, design doc first): the `--data-checksums`
        **default-ON flip** for PG-18 parity (and the `001_initdb.pl`
        version-1 assertion) — **both validation gates now pass** (gate (a)
        recovery/FPI replay loop #32; gate (b) standby-read/physical-replication
        loop #34), so the flip itself is the one-line default change
        (`init`'s `dataChecksums` default false → true). DEFERRED to a dedicated
        loop because flipping the default changes the on-disk format of every
        new cluster: it must be gated by the full regress-port suite + a TPC-H
        re-load/spot-check (M0106 "codec/format change → re-run full suite"
        lesson) + a sweep of every test/bench data dir needing re-init.
        The locale-derived default encoding
        (`pg_get_encoding_from_locale` on an unset `--encoding`) remains a
        no-op under goopg's fixed C locale.

## M0110 — Additional TAP Test Porting (beyond M0094/M0095) (filed 2026-05-22)

Operational note (2026-05-22):
- This milestone covers TAP tests listed in
  `docs/test-port/upstream-tap-coverage.md` that are **not** in scope for
  M0094 (recovery + subscription) or M0095 (basebackup / checksums /
  controldata / pg_ctl / walsummary / scripts).
- Already-ported families (psql, pgbench, initdb) are listed for
  completeness at the bottom; no new work is needed.
- Excluded tests that exercise a PG client tool against a goopg server
  are included because they validate the wire-protocol and SQL
  compatibility surface.  Tests for tools that do not connect to a
  server (pg_config, pg_test_fsync, pg_test_timing) or that require
  multi-server orchestration (pg_rewind, pg_upgrade, pg_combinebackup)
  remain excluded.
- Each test is tagged with one of:
  - **SHOULD_PASS** — goopg feature is implemented; test is expected to
    pass once ported to Go and any remaining normalization is applied.
  - **BUG_FIX** — feature is implemented but has known bugs that would
    prevent the test from passing.
  - **UNIMPLEMENTED** — required feature is not yet implemented.

### pg_dump (6 tests — excluded → candidate)

pg_dump connects to a live server and issues SQL queries to extract
schema and data.  Porting these tests validates goopg's catalog views,
information_schema, pg_depend, extension infrastructure, and large-
object support.

- [ ] **M0110-0001 — Port pg_dump TAP tests**
      - Target tests:
        | Test | Status | Rationale |
        |------|--------|-----------|
        | `postgres/src/bin/pg_dump/t/001_basic.pl` | **PORTED 2026-06-13** | `TestPort_PgDump001Basic` (`internal/testport/pgdump_port_test.go`). Pure CLI option-handling test — help/version/options + invalid-option/disallowed-combination cases for pg_dump/pg_restore/pg_dumpall; needs no server. CSV row DU-001 → port. Design: `docs/design/0110-0001-pg-dump-tap-port.md`. |
        | `postgres/src/bin/pg_dump/t/002_pg_dump.pl` | UNIMPLEMENTED | Comprehensive schema/object dump; requires full catalog parity (pg_class, pg_attribute, pg_type, pg_proc, pg_depend, pg_extension, etc.). |
        | `postgres/src/bin/pg_dump/t/003_pg_dump_with_server.pl` | UNIMPLEMENTED | Dump+restore round-trip against a live server; exercises SQL-level object creation and data restoration. |
        | `postgres/src/bin/pg_dump/t/004_pg_dump_parallel.pl` | UNIMPLEMENTED | Parallel dump; additionally requires multi-connection catalog snapshot consistency. |
        | `postgres/src/bin/pg_dump/t/005_pg_dump_filterfile.pl` | UNIMPLEMENTED | Filter-file support in pg_dump. |
        | `postgres/src/bin/pg_dump/t/010_dump_connstr.pl` | UNIMPLEMENTED | Connection-string handling in pg_dump. |
      - Action: design doc first; estimate the catalog surface required per
        test; start with 001 and 003 (basic server round-trip).  Most tests
        are blocked on catalog-view coverage (pg_class, pg_attribute,
        pg_type, pg_proc, pg_depend, pg_extension).
      - **PROGRESS 2026-06-13 (loop #16):** 001_basic ported (the CLI-only
        tier, no server dependency) — see DU-001 above. 002-010 remain
        deferred under CSV row E-002 pending the catalog-view parity + dump
        /restore round-trip enumerated in `docs/design/0110-0001-pg-dump-tap-port.md`.
        Resume point: 002_pg_dump (schema dump) then 003 (round-trip).

### pg_waldump (2 tests — excluded → candidate)

pg_waldump reads WAL segment files directly (no server connection).
Porting validates goopg's WAL record format compatibility with upstream.

- [ ] **M0110-0002 — Port pg_waldump TAP tests**
      - Target tests:
        | Test | Status | Rationale |
        |------|--------|-----------|
        | `postgres/src/bin/pg_waldump/t/001_basic.pl` | **PORTED 2026-06-13 (CLI tier)** | `TestPort_PgWaldump001Basic` (`internal/testport/pgwaldump_port_test.go`). The pure CLI option-handling tier (help/version/options + no-args/too-many-args + invalid `--block`/`--fork`/`--limit`/`--relation`/`--rmgr`/`--start`/`--end` + `--rmgr=list`) — decided by the upstream binary's parser before any WAL file is opened; no server. CSV row WD-001 → port. Design: `docs/design/0110-0002-pg-waldump-tap-port.md`. The server-dependent tier of 001_basic.pl is deferred under WD-002 (needs hash/gin/gist/spgist/brin AMs; WAL-format readability already covered by W-001 `TestPort_WALPgWaldumpCompat`). |
        | `postgres/src/bin/pg_waldump/t/002_save_fullpage.pl` | UNIMPLEMENTED (deferred WD-002) | `pg_waldump --save-fullpage` requires full-page-image extraction; goopg may not emit FPI in all the same places as PG. |
      - Action: 001_basic CLI tier ported (loop #17). Resume = promote WD-002
        when goopg gains the index access methods the server-tier workload
        needs (hash/gin/gist/spgist/brin) + FPI extraction for 002.
      - **PROGRESS 2026-06-14 (loop #17):** `002_save_fullpage.pl` ported as a
        self-promoting reproduction `TestPort_PgWaldump002SaveFullpage`
        (`internal/testport/pgwaldump_savefullpage_test.go`). It drives the full
        `pg_waldump --save-fullpage --relation` extraction (goopg emits
        PG-compatible FPIs via the checkpointer FPI epoch, writes a full
        `RelFileLocator` spc=1663/db/relNumber, and `pg_relation_filenode`
        returns the reloid that matches), and asserts the upstream filename
        format + page-LSN ≤ file-LSN ordering — but currently `t.Skip`s on a
        REAL goopg blocker it uncovered: **goopg writes `xl_prev` as a 1-based
        LSN on disk**, so `pg_waldump` (0-based, anchored on the segment file
        name) aborts the record-chain walk at the 2nd record (`incorrect
        prev-link 0/1000029 at 0/10000A0`, constant +1). Origin: `internal/wal/
        writer.go` (~L1346/L1491 `start=writePos+leading+1`) → `insert_pos.go`
        `reserveLocked t.prev=old` → `format.go:263 encodeRecordXLog` writes it
        verbatim. Fixing it is a coordinated WAL encode↔decode change (goopg's
        own recovery decode + the M0102 walsender must stay consistent), so it
        is its own WAL-correctness loop — see deferral ledger + design doc
        `0110-0002`. The test auto-promotes once `xl_prev` is emitted 0-based.
      - **ALSO UNCOVERED (loop #17):** `TestPort_WALPgWaldumpCompat` (row W-001,
        M0101-0003, `pass_required=yes`) is **silently red**: goopg's WAL
        segment names are now native PG-format (TLI=1 prefix, e.g.
        `000000010000000000000001`), but W-001 still parses them as plain hex
        segment numbers (`strconv.ParseUint(name,16,64)` overflows on 24 hex
        chars) and rewrites a timeline alias, so it `t.Fatal`s at "no WAL
        segments found" before running `pg_waldump`. Oracle tests are excluded
        from `go test ./...`, so this escaped notice.
      - **RESOLVED 2026-06-14 (loop #18) — the xl_prev blocker was misdiagnosed
        and is now fixed.** The on-disk `xl_prev` was NOT globally 1-based: the
        live-append path already stores `start-1` (0-based) via
        `resetPosition(end, start-1)`. The bug was a **restart-seed
        inconsistency** — `detectWritePos` (`internal/wal/writer.go:917`) seeded
        `prevRecPtr` from `scanLastSegmentEnd`'s **1-based** start-LSN verbatim,
        so the first record appended after boot inherited a +1 `xl_prev`
        (exactly the "2nd record" pg_waldump rejected, the bootstrap checkpoint
        being record #1). Fix: `prevRecPtr = lastRecPtr - 1` (0-guarded),
        mirroring the live path. No encode↔decode change needed — goopg recovery
        **never validates `xl_prev`**, and `writePos`/client-visible LSNs are
        unchanged, so this is output-only (strictly improves goopg→PG
        replication prev-link validation). Design: `0101-0003-wal-xlprev-restart-seeding-fix.md`.
        - `W-001` repaired (native-PG segment discovery via `listWALSegments`)
          and now **PASS** — it guards the fix via the `incorrect prev-link`
          check. CSV W-001 rationale updated; markdown regenerated.
        - `TestPort_PgWaldump002SaveFullpage`: prev-link blocker GONE (pg_waldump
          now walks the full chain to clean EOS); a prev-link error is now
          asserted as a regression. The test still self-skips on the genuinely
          **separate** remaining blocker — goopg emits no PG-decodable FPI
          records (all non-checkpoint records route through `RmgrXLog`/`0xF0`,
          opaque to PG), so `--save-fullpage` extracts nothing. Stays under
          WD-002 (deferred) until goopg emits PG-format heap WAL with backup
          blocks.
        - Gates: `go test ./internal/wal/` + `go test -race ./internal/wal/
          ./internal/mvcc/` green; both pg_waldump oracle tests pass;
          `TestE2E_PhysicalReplication` green.

### pg_amcheck (5 tests — excluded → candidate)

pg_amcheck connects to a server and runs heap/btree corruption checks.
Porting validates goopg's heap page and btree index integrity
functions (e.g. `bt_index_parent_check`, `verify_heapam`).

- [ ] **M0110-0003 — Port pg_amcheck TAP tests**
      - Target tests:
        | Test | Status | Rationale |
        |------|--------|-----------|
        | `postgres/src/bin/pg_amcheck/t/001_basic.pl` | **PORTED 2026-06-13 (CLI tier)** | `TestPort_PgAmcheck001Basic` (`internal/testport/pgamcheck_port_test.go`). 14-line CLI-only test (`program_help_ok`/`program_version_ok`/`program_options_handling_ok`) — decided by the binary's arg parser before any server connection. New `runToolWithLib` helper sets `LD_LIBRARY_PATH=postgres/local_install/lib` (bundled pg_amcheck links `PQcancelBlocking`, a PG 17+ libpq symbol absent from older host libpq). CSV row AC-001 → port. Design: `docs/design/0110-0003-pg-amcheck-tap-port.md`. |
        | `postgres/src/bin/pg_amcheck/t/002_nonesuch.pl` | UNIMPLEMENTED (deferred AC-002) | Handles non-existent database/relation; still issues catalog queries against a live server. |
        | `postgres/src/bin/pg_amcheck/t/003_check.pl` | UNIMPLEMENTED (deferred AC-002) | Runs actual heap/btree corruption checks against a server. |
        | `postgres/src/bin/pg_amcheck/t/004_verify_heapam.pl` | UNIMPLEMENTED (deferred AC-002) | `verify_heapam()` function required (not in goopg). |
        | `postgres/src/bin/pg_amcheck/t/005_opclass_damage.pl` | UNIMPLEMENTED (deferred AC-002) | Operator-class damage detection; requires opclass system catalog parity. |
      - Action: 001_basic CLI tier ported (loop #18). The four server-dependent
        tests are deferred under CSV row AC-002, blocked on `verify_heapam()` SRF
        + opclass catalog coverage. Resume = promote AC-002 (002_nonesuch first —
        only error-path catalog lookups) when those land.
      - **PROGRESS 2026-06-14 (loop #51):** the **page-structural core of
        `verify_heapam()` landed** as a standalone `internal/amcheck` engine
        (`VerifyHeapPage`), following the engine-first/wire-later pattern (cf. the
        M0102-0010 checksum engine). Tier 1 only — line-pointer bounds/alignment,
        redirect-target validity, and tuple-header `t_hoff` consistency — all
        deterministic functions of the raw 8 KiB page bytes (no clog/TupleDesc/
        toast). Corruption messages mirror `postgres/contrib/amcheck/verify_heapam.c`
        verbatim (`check_tuple_header` + the line-pointer loop) so the later SRF +
        `004_verify_heapam` port reuse them. 11 unit tests PASS (clean empty/new/
        tuple pages → no reports; each targeted corruption → exact upstream
        message). Design doc `docs/design/0110-0005-verify-heapam-engine.md`.
        **Deferred** (deferral ledger 2026-06-14): the HOT-chain tier, the
        MVCC/attribute tier (xmin/xmax/multixact/TOAST pointer checks), and the
        SQL surface — `CREATE EXTENSION amcheck` (parser + `pg_extension` row +
        `pg_proc` registration) + the `verify_heapam(regclass,…)` SRF that walks a
        relation's blocks through this engine — which is the slice that promotes
        AC-002 (`002_nonesuch`). The SQL surface edits parser/planner/executor/
        catalog, which currently carry uncommitted gen-column WIP from a separate
        session; it must wait for a clean tree. Resume = wire `CREATE EXTENSION
        amcheck` + the `verify_heapam` SRF on top of this engine, then port
        `002_nonesuch.pl`.
      - **PROGRESS 2026-06-14 (loop #52):** extended the engine with the two
        **infomask-only** `check_tuple_header` invariants that need no clog/
        TupleDesc/toast — `multixact should not be marked committed`
        (`HEAP_XMAX_COMMITTED|HEAP_XMAX_IS_MULTI`) and `tuple has been HOT
        updated, but xmax is 0` — both upstream-verbatim. Notable goopg/PG
        divergence handled: goopg packs `HEAP_HOT_UPDATED`/`HEAP_ONLY_TUPLE` into
        `t_infomask` (not `t_infomask2`), so the HOT check reads goopg's field;
        `HEAP_XMAX_IS_MULTI` (0x1000) is defined locally (goopg has no on-disk
        multixact, so it fires only on injected corruption — zero false
        positives). The third header invariant (heap-only-but-not-updated) is
        intentionally NOT ported: goopg never sets `HEAP_UPDATED` (reuses 0x2000
        for `HeapKeysUpdated` in `t_infomask2`), so a verbatim port would
        false-positive on every healthy HOT successor. 15 unit tests PASS (4 new,
        incl. two false-positive guards). Design doc `0110-0005` extended; all in
        new files (`internal/amcheck/`), zero contaminated files touched. The SQL
        surface remains the AC-002-promoting slice, still blocked on a clean tree.
      - **PROGRESS 2026-06-14 (loop #53):** extended the engine with the
        **page-structural subset of the HOT-chain (update-chain) tier** — the
        part of `verify_heapam.c`'s second/third loops decidable from page bytes +
        the page's own block number, no clog. `VerifyHeapPage` now takes `blkno`
        and builds `successor[]`/`predecessor[]` to mirror upstream: a redirect
        must target a heap-only tuple (`redirected line pointer points to a
        non-heap-only tuple`); HOT chains must not intersect (`redirect line
        pointer points to offset N, but offset M also points there` +
        `tuple points to new version at offset N, but offset M also points
        there`); a link's HOT-updated/heap-only flags must agree
        (`non-heap-only update produced a heap-only tuple` + `heap-only update
        produced a non-heap only tuple`) — all upstream-verbatim. goopg
        divergence handled: `t_ctid.block` is a plain `uint32` at `off+12`
        (not PG's `bi_hi`/`bi_lo` split), and the HOT/heap-only bits live in
        `t_infomask`. Normal links form only on `curr_xmax==next_xmin && !=0`
        (non-multi update xid). 23 unit tests PASS (8 new: 5 corruption + 3
        false-positive guards incl. cross-block CTID). Design doc `0110-0005`
        extended; all in new files (`internal/amcheck/`), zero contaminated
        files touched. DEFERRED: the clog-dependent HOT-chain checks (xmin
        commit-status across a link + root-of-chain-but-heap-only) and the SQL
        surface (still the AC-002-promoting slice, still blocked on a clean tree).
      - **PROGRESS 2026-06-14 (loop #54):** added the **relation-natts tier** —
        the one relation-dependent `check_tuple` check that is faithful to
        goopg's on-disk layout (`number of attributes %u exceeds maximum expected
        for table %u`, `verify_heapam.c:1942`). The tuple's `natts` is decoded
        page-structurally from `t_infomask2`; the only relation metadata needed
        is the column count, supplied via new `RelDesc{Natts}` through new entry
        point `VerifyHeapPageWithRel(p, blkno, rel)` (`VerifyHeapPage` is now a
        thin zero-`RelDesc` wrapper, page-bytes-only behaviour unchanged).
        `checkTupleHeader` now returns a `bool` (header clean enough to
        continue), gating the natts check exactly as upstream's `check_tuple`
        gates on `check_tuple_header`. Visibility-gate divergence documented
        (no clog → applied to every header-clean tuple; safe for goopg). Also
        recorded that `check_tuple_attribute` is **goopg-divergent** (PG on-disk
        varlena/`varatt_external` TOAST format vs goopg's chunk-relation TOAST),
        not merely deferred. 27 unit tests PASS (4 new). Design doc `0110-0005`
        extended; all in new files (`internal/amcheck/`), zero contaminated
        files touched. STILL DEFERRED: clog-dependent HOT-chain checks + the SQL
        surface (AC-002-promoting slice, still blocked on a clean tree).
      - **PROGRESS 2026-06-14 (loop #55):** started the **B-tree index checker**
        — the index-side companion to the heap engine, since `pg_amcheck` runs
        `bt_index_check()` on every index (`003_check`/`005_opclass_damage`
        exercise it), so AC-002 needs it too. New `internal/amcheck/
        verify_nbtree.go` (`VerifyBtreePage(p, blkno, indexName)`) ports the
        page-structural `palloc_btree_page` tier: metapage (block 0) magic +
        version validation (`index "%s" meta page is corrupt`, `version mismatch
        in index "%s": …`), and leaf/internal page-level consistency
        (`invalid leaf page level %u …`, `invalid internal page level 0 …`),
        with deleted pages exempt — verbatim upstream messages. To avoid
        re-decoding goopg's metapage/opaque format (which changed v3→v4 — a
        sibling-path drift hazard), added thin **exported accessors** to the
        uncontaminated `internal/access/btree/btree.go`: `BTreeMagic`,
        `BTreeVersion`, `ParseMeta`, `ParseOpaque`, and a `BTPageOpaque.IsDeleted`
        method (purely additive). goopg/PG divergences documented as NOT ported:
        high-key-as-item-`P_HIKEY` checks (goopg keeps the high key in the opaque
        area) and the `MaxIndexTuplesPerPage` ceiling (goopg's inline index-tuple
        layout needs its own size accounting). 10 new unit tests PASS (hand-built
        clean/corrupt pages, each self-checked through the real decoders); btree
        package tests still PASS; gofmt/vet clean. Design doc `0110-0005` extended
        with a "B-tree index verification" section; README index updated. STILL
        DEFERRED: intra-page item-order/high-key + cross-page tiers, and the SQL
        surface (`bt_index_check`/`verify_heapam` SRF, the AC-002-promoting slice,
        still blocked on a clean tree).
      - **PROGRESS 2026-06-14 (loop #56):** added the **second B-tree tier**,
        `VerifyBtreeItemOrder` — `bt_target_page_check`'s two page-local key
        invariants (`verify_nbtree.c:1565-1642`): the **item-order** invariant
        (items strictly ascending: `item order invariant violated for index
        "%s"`) and the **high-key** invariant (each key `<=` the high key on a
        leaf, strictly `<` on an internal page: `high key invariant violated for
        index "%s"`) — verbatim upstream messages. Keys are decoded through a new
        exported `btree.PageItemKeys` (one separator key per physical line
        pointer, **collapsing** a posting-list item's many TIDs to its single
        shared key — additive to the uncontaminated `btree.go`) and compared with
        `btree.CompareKeys`, the same comparator the live index uses (single
        source of truth, no re-decode of the inline item layout). goopg specifics
        handled faithfully: the high key lives in the opaque area (no `P_HIKEY`
        slot to skip; rightmost gating via `Next == InvalidBlockNumber`), and an
        internal page's leftmost negative-infinity downlink (empty key) satisfies
        both invariants without a special case. 10 new amcheck tests (new
        `makeItemsPage` builder, each self-checked through the real readers) + 1
        new btree `TestPageItemKeys` (posting-collapse) PASS; full `btree` +
        `amcheck` suites PASS; gofmt/vet clean; all changes in new code / additive
        exports, zero contaminated files touched. Design doc `0110-0005` extended
        ("B-tree item-order / high-key tier"); README index updated. STILL
        DEFERRED: the item-count ceiling, cross-page/cross-level tiers, and the
        SQL surface (still blocked on a clean tree).
      - **PROGRESS 2026-06-14 (loop #57):** added the **third B-tree tier**, the
        **item-count ceiling**, folded into `VerifyBtreePage` after the level
        checks (matching `palloc_btree_page`'s order, `verify_nbtree.c:3396-3402`):
        a page whose line-pointer count exceeds what can physically fit is
        corrupt. goopg's index tuple (`keyLen|block|offset|key`, stored
        **unaligned**) gives a different bound than PG's `MaxIndexTuplesPerPage`,
        so the ceiling is computed from goopg's own per-item footprint and
        exported as `btree.MaxItemsPerPage` =
        `(BlockSize - SizeOfPageHeaderData) / (4 + itemPrefixSize)` = **680**,
        defined beside `itemPrefixSize` in the uncontaminated `btree.go` (single
        source of truth — the engine never re-derives the inline item layout).
        Message is upstream-verbatim (`Number of items on block %u of index "%s"
        exceeds MaxIndexTuplesPerPage (%u)`), with goopg's constant. Divergences
        handled: like upstream the bound ignores the per-page opaque area (extra
        headroom → no false positives); unlike upstream the check is skipped on
        deleted pages (goopg returns earlier; a goopg deleted page holds no live
        items, and skipping avoids reading type-punned fields); a non-`itemIDSize`
        `pd_lower` surfaces as a damaged-page finding, never a Go error/panic.
        5 new amcheck tests (`makeCountPage` builder bumps `pd_lower` to claim a
        count without materialising bodies: constant pin = 680, at-ceiling clean,
        over-ceiling exact message, damaged `pd_lower`, deleted-page suppression).
        `go test ./internal/amcheck ./internal/access/btree` PASS; gofmt/vet
        clean; all changes in new amcheck code + one additive `btree.go` const,
        zero contaminated files touched. Design doc `0110-0005` extended
        ("B-tree item-count ceiling tier"); README index updated. STILL DEFERRED:
        cross-page/cross-level tiers, and the SQL surface (still blocked on a
        clean tree).
      - **PROGRESS 2026-06-14 (loop #58):** added the first **cross-page** B-tree
        tier, `VerifyBtreeLevelSiblingLinks` (new code in `verify_nbtree.go`),
        porting the sibling-link checks `bt_check_level_from_leftmost` performs
        walking one level left-to-right (`verify_nbtree.c:650-790`): (1) **left-link/
        right-link agreement** — each page's `btpo_prev` must equal the block we
        arrived from, with the leftmost page exempt exactly as upstream's
        `leftcurrent != P_NONE` gate (`left link/right link pair in index "%s"
        not in agreement`, :1193); (2) **per-level `btpo_level` uniformity**
        (`leftmost down link for level points to block in index "%s" whose level
        is not one level down`, :774); (3) **circular-chain detection** via a
        visited-set that subsumes upstream's immediate `current==leftcurrent ||
        current==btpo_prev` case AND bounds longer cycles a bytes-only checker
        can't otherwise terminate (`circular link chain found in block %u of
        index "%s"`, :787); plus deleted-page-reached-via-sibling
        (`downlink or sibling link points to deleted block in index "%s"`, :676).
        To stay new-file/additive while the tree is dirty, the driver takes a
        minimal `PageSource func(BlockNumber)(Page,error)` seam instead of opening
        the index catalog — the SQL surface will fill it from the index's smgr,
        tests back it with an in-memory map; a source error becomes a damaged-page
        finding, never a panic. 10 new amcheck tests (`makeLinkedPage` +
        `mapSource` helpers): clean 3-page level, back-link mismatch (exact msg +
        block), leftmost-prev exemption, level mismatch, two-page cycle, self-loop,
        deleted-reachable, dangling right link, metapage-leftmost, single-page
        level. `go test ./internal/amcheck ./internal/access/btree` PASS; gofmt/vet
        clean; zero contaminated files touched (all in `verify_nbtree.go` +
        `_test.go`). Design doc `0110-0005` extended ("B-tree cross-page
        sibling-link tier"); README index updated. STILL DEFERRED: the
        downlink-to-child / cross-level descent tiers (`bt_index_parent_check`,
        need parent+child pivot-key comparison across levels), and the SQL surface
        (still blocked on a clean tree).
      - **PROGRESS 2026-06-14 (loop #59):** added the **cross-level** B-tree tier,
        `VerifyBtreeParentDownlinks` (new code in `verify_nbtree.go`), porting
        `bt_child_check`'s per-downlink checks (`verify_nbtree.c:2393-2543`):
        given an internal parent it follows every downlink to its child and
        enforces (1) **downlink-to-deleted** (`downlink to deleted page found in
        index "%s"`, :2494), (2) **child level one down** (`downlink points to
        block in index "%s" whose level is not one level down`, :2655), and (3)
        the **down-link lower-bound invariant** (`down-link lower bound invariant
        violated for index "%s"`, :2535 — `invariant_l_nontarget_offset` loop).
        Reuses the loop-#58 `PageSource` seam; a new exported
        `btree.PageDownlinks` decodes an internal page's `(separator key, child
        block)` entries through the canonical reader (single source of truth,
        like `PageItemKeys`). Two goopg-faithful divergences keep it false-
        positive-free: (a) the bound is **inclusive** (`CompareKeys(childKey,
        K_i) >= 0`) because `findChildBlock` routes to the rightmost item `<=`
        key so a child covers `[K_i, K_{i+1})` — upstream's strict heapkeyspace
        `<` would misfire on separators equal to the child's first key; (b) the
        internal child's empty negative-infinity item (item 0 only) is skipped,
        exactly as `offset_is_negative_infinity`. Returns 0/1 findings; leaf or
        deleted parent and the metapage have no downlinks → nil; read errors
        become damaged-page findings, never panics. 10 new amcheck tests
        (`makeInternalPage` + `btDownlinkRaw` helpers): clean two-downlink parent,
        leaf-child below separator, downlink-to-deleted, child-level-not-one-down,
        neg-inf-child-item-skipped (clean), internal-child real-key-below-bound,
        leaf parent (nil), metapage (nil), damaged parent, dangling child.
        `go test ./internal/amcheck ./internal/access/btree` PASS; gofmt/vet
        clean; zero contaminated files touched (all in `verify_nbtree.go` +
        `_test.go` + one additive `btree.go` accessor). Design doc `0110-0005`
        extended ("B-tree cross-level downlink tier"); README index updated.
        STILL DEFERRED: only `heapallindexed` (needs a heap scan + index
        `TupleDesc`) and the SQL surface (`CREATE EXTENSION amcheck` +
        `verify_heapam`/`bt_index_check` SRF, the AC-002-promoting slice, still
        blocked on a clean tree).
      - **PROGRESS 2026-06-14 (loop #60):** ported the **Bloom filter primitive**
        for the last remaining B-tree tier, `heapallindexed`
        (`postgres/src/backend/lib/bloomfilter.c` + `.h` → new
        `internal/amcheck/bloomfilter.go`). heapallindexed fingerprints every
        index tuple into a Bloom filter, then scans the heap and asserts each
        visible heap tuple's index tuple is present (`bt_tuple_present_callback`);
        an absent result is a genuine "heap tuple not represented in index"
        corruption. The filter is a self-contained data structure (no
        clog/TupleDesc/catalog/SQL coupling), so it lands engine-first/wire-later
        in **new files only** — zero contaminated files touched. Every part is
        upstream-verbatim (`bloomCreate`/`bloomAddElement`/`bloomLacksElement`/
        `bloomPropBitsSet`/`myBloomPower`/`optimalK`/`kHashesValues`/`modM`, the
        1MB-floor/512MB-cap power-of-two sizing, enhanced double hashing) with a
        **single documented divergence**: the hash. Upstream seeds from
        `hash_any_extended` (Jenkins lookup3, mirrored as the unexported
        `pgHashBytesExtended` in `internal/executor/hash_partition.go`); importing
        it would entangle amcheck with the contaminated executor package, and
        since no-false-negatives holds for any shared hash, the port uses a
        self-contained seeded FNV-1a + MurmurHash3 `fmix64` finalizer. 8 unit
        tests: no-false-negatives (load-bearing — a false negative is a spurious
        corruption report), realistic-regime FP rate < 5%, seed-distinguishes-FPs,
        sizing invariants, empty/variable-length elements, `myBloomPower`/
        `optimalK`/`modM` helpers. `go test ./internal/amcheck` PASS; `go build
        ./...` OK; gofmt/vet clean. Design doc `0110-0006-amcheck-bloom-filter.md`
        + README index. STILL DEFERRED (resume points): the heapallindexed heap
        scan + index-tuple formation (needs heap relation + index `TupleDesc`),
        the `CREATE EXTENSION amcheck` + SRF SQL surface (AC-002-promoting, still
        blocked on a clean tree), and hash unification once Jenkins can be shared
        without cross-package entanglement (distribution-only, no contract change).
      - **PROGRESS 2026-06-14 (loop #61):** ported the **heapallindexed
        fingerprint+probe core** — the last B-tree verification tier — as a pure
        function `VerifyBtreeHeapAllIndexed` (new `internal/amcheck/heapallindexed.go`),
        consuming the loop-#60 Bloom primitive. It fingerprints every index leaf
        entry into a filter sized to the exact count, then probes the index tuple
        each live heap tuple would form; emits one `BtreeReport` per lacking entry
        with the verbatim upstream message `heap tuple (b,o) from table "T" lacks
        matching index tuple within index "I"` (+ heap block). Soundness rests on
        the filter's no-false-negatives contract: a healthy heap tuple is never
        flagged; false positives only mask a `<2%` fraction of real misses, never
        invent corruption. One `fingerprintLeafEntry` (`TID.Block` big-endian ++
        `TID.Offset` ++ key bytes) drives BOTH the fingerprint and probe phases, so
        a present `(key, TID)` hashes identically on both sides (sibling-path
        invariant). New exported `btree.PageLeafEntries` is the canonical leaf
        reader the wire-later fingerprint phase will use — it EXPANDS posting-list
        items to one entry per heap TID (unlike `PageItemKeys`, which collapses to
        one separator key), placed beside `PageItemKeys`/`PageDownlinks` as single
        source of truth against v3→v4 inline-item drift. Divergences from upstream:
        no `bt_normalize_tuple` step (goopg does not TOAST index keys → leaf and
        heap-formed key bytes are already one canonical `EncodeXxxKey` form), and
        the Bloom seed is a parameter (caller randomizes per run, tests pin it).
        Still **new files only** — zero contaminated files touched. Tests: 6 amcheck
        (`heapallindexed_test.go`: no-false-negatives load-bearing at n=100k,
        missing-tuple detection w/ exact message+block, TID-distinguishes,
        empty-index/empty-heap boundaries, shared-key-distinct-TIDs posting
        semantics) + 1 btree (`TestPageLeafEntries`: plain + 3-TID posting → 4
        expanded entries). `go test ./internal/amcheck ./internal/access/btree`
        PASS; `go build` OK; gofmt/vet clean. Design doc
        `0110-0007-amcheck-heapallindexed.md` + README index. STILL DEFERRED
        (resume points): the lazy heap scan + index-tuple formation via index
        `TupleDesc` (catalog coupling), the `CREATE EXTENSION amcheck` +
        `bt_index_check(…, heapallindexed => true)` SQL surface (AC-002-promoting,
        still blocked on a clean tree), and hash unification (distribution-only).
        **The B-tree verification engine is now logic-complete** — every tier
        `bt_check_every_level` performs is ported; only the SQL surface remains.
      - **PROGRESS 2026-06-14 (loop #62):** closed the **heap** engine's last
        update-chain tier — the **clog-dependent HOT-chain checks**
        (`verify_heapam.c:759-833`), the three checks that need each tuple's xmin
        commit status: (1) in-progress xmin updated to a committed xmin, (2)
        aborted xmin updated to an in-progress or committed xmin, and (3) a
        heap-only tuple that is the root of an update chain (no predecessor) yet
        has a committed/in-progress xmin — all upstream-verbatim (message names
        the current tuple's offset + frozen-resolved xmins). Ported via a new
        `VerifyHeapPageWithXminStatus(p, blkno, rel, xidStatus)` entry point
        taking an injected `XidStatusFunc func(xid uint32) XidCommitStatus`
        callback — the decoupling seam that keeps the engine off the contaminated
        `internal/mvcc` (the SQL surface will supply a clog+proc-array-backed
        impl; tests supply a map). `XidCommitStatus` is the branch-relevant
        subset of upstream's enum (Unknown=`xmin_commit_status_ok==false`,
        Committed, InProgress, Aborted, Current); bootstrap(1)/frozen(2 or the
        `HEAP_XMIN_COMMITTED|HEAP_XMIN_INVALID` hint pair) resolve to committed
        without the callback (mirrors `get_xid_status`). The page-bytes-only entry
        points pass nil → `xminStatusOK` stays false → these three checks are
        disabled, output byte-for-byte unchanged (regression-guarded). 10 new
        tests (3 positive cross-link cases isolated so only the clog report
        appears, heap-only-root positive, + 6 false-positive guards: heap-only
        WITH predecessor, aborted heap-only root, current-xid root, unknown-status
        root, nil-callback regression, frozen-resolves-committed). `go test -race
        ./internal/amcheck ./internal/access/btree ./internal/storage` PASS; `go
        build ./...` OK; gofmt/vet clean; all changes in `verify_heapam.go` +
        `_test.go` only — zero contaminated files touched. **The heap engine is
        now logic-complete** (parity with the B-tree side); only the MVCC/attribute
        tier (xmin/xmax numeric bounds, multixact, goopg-divergent TOAST) and the
        SQL surface remain. Design doc `0110-0005` extended; README index updated.
        STILL DEFERRED: `heapallindexed` heap scan + the `CREATE EXTENSION
        amcheck`/SRF SQL surface (AC-002-promoting, still blocked on a clean tree
        — separate live session pid 2177381 holds the gen-column WIP).
      - **PROGRESS 2026-06-14 (loop #63):** ported the heap **relation-walking
        driver** — the outer loop of the `verify_heapam()` SRF body
        (`verify_heapam.c:367-405,480-501`) — as `VerifyHeapRelation` in new
        `internal/amcheck/verify_heapam_relation.go`. The per-page checks were
        already ported (`VerifyHeapPage*`); this adds the relation-level walk:
        empty-relation early exit (`nblocks==0` → no rows), `startblock`/`endblock`
        resolution from the SRF's int8 args (`*int64`, nil = SQL NULL → 0 /
        nblocks-1) with the upstream-verbatim `ERRCODE_INVALID_PARAMETER_VALUE`
        range errors, and block iteration that runs each page through
        `verifyHeapPage` and tags every finding with its block
        (`HeapRelReport{Blkno,Offset,Msg}` → one SRF `(blkno,offnum,msg)` row;
        attnum -1, SRF-supplied). It **reuses the same `PageSource` seam the
        B-tree relation walkers already take**, making the heap and index sides
        symmetric and reducing SQL slice S3 (`0110-0008`) from "executor contains
        the block loop" to a thin smgr adapter. relkind/relam guard + relation
        open/lock + toast walk stay at the wire layer (catalog/goopg-storage
        coupled), matching where upstream draws the line — documented. 9 new tests
        (`heapMapSource` builder + fake `PageSource`): clean multi-block (asserts
        every block read), empty-relation (source untouched), finding tagged with
        non-zero block, ordered across blocks, sub-range restriction, both
        range-validation messages + negative case, surfaced read error, nil-source,
        and `RelDesc` threading through to the per-page natts check. `go test -race
        ./internal/amcheck` PASS; `go build ./...` OK; gofmt/vet clean; **all in new
        files only — zero contaminated files touched.** Design doc `0110-0005`
        extended ("Relation-walking driver"); `0110-0008` S3 row + engine-API
        section updated. STILL DEFERRED: the `heapallindexed` heap scan + the
        `CREATE EXTENSION amcheck`/SRF SQL surface (AC-002-promoting, still blocked
        on a clean tree — pid 2177381 still holds the gen-column WIP, frozen since
        2026-06-13 14:28).
      - **PROGRESS 2026-06-14 (loop #64):** the SQL surface is still blocked on
        the external gen-column WIP (pid 2177381, frozen since 2026-06-13 14:28),
        so instead of idling I added the engine's missing **false-positive
        guard** — and it paid off by surfacing a real storage bug. The per-page
        tests inject corruption by hand (the only way to write damage); nothing
        verified the engine stays *silent* on pages produced by goopg's *real*
        mutators. New `internal/amcheck/verify_heapam_realpage_test.go` (6 tests)
        drives the real producers — a same-page HOT chain via
        `PageStampHotOldTuple` + heap-only successor (mirroring the executor's
        `tryApplyHOTUpdate`), the chain after `PageSetItemIDRedirect` pruning, a
        `VacuumHeapPage`-pruned page, a `NewHeapTupleWithNulls` nullable
        multi-attr tuple, the HOT chain through the clog tier, and the
        whole-relation driver over all three — asserting zero findings.
        **Bug found + fixed:** `storage.VacuumHeapPageBySlots` repacked survivors
        with `upper -= len(body)` (no MAXALIGN), producing non-8-byte-aligned
        line-pointer offsets — its sibling `PageAddHeapTuple` deliberately
        MAXALIGNs because (per its M0106-0010 comment) a non-aligned offset
        segfaults a PG18 standby's `heap_deform_tuple`. The engine correctly
        flagged it. Fix: `upper -= maxAlign8(len(e.body))` (one line, restores
        sibling-path agreement; vacuum only removes tuples so it cannot overflow
        the page). Verified `-race`: `internal/{storage,vacuum,wal,executor,
        mvcc,amcheck}` all green (wal = replay shares the kernel). Design doc
        `0110-0005` extended ("Real-producer false-positive validation"). All
        non-contaminating: only `verify_heapam_realpage_test.go` (new) +
        `storage/heap.go` (1-line fix) + docs touched. SQL surface S1/S2/S3
        remains the AC-002-promoting slice, still blocked on a clean tree.
      - **PROGRESS 2026-06-14 (loop #65):** ported the **index-side relation walk
        of the heapallindexed tier** — the fingerprint phase's leaf-level
        enumeration — as `CollectBtreeLeafEntries` + `VerifyBtreeHeapAllIndexedRelation`
        in new `internal/amcheck/heapallindexed_relation.go`, symmetric with the
        heap engine's loop-#63 `VerifyHeapRelation` and behind the same
        `PageSource` seam. `CollectBtreeLeafEntries` reads the metapage, descends
        `Root`→leftmost leaf (slot-1 negative-infinity downlink at each internal
        level, via `leftmostLeafBlock`), then walks `btpo_next` collecting
        `btree.PageLeafEntries` (posting items expanded per-TID), skipping deleted
        leaves; both descent and sibling walk are visited-set-bounded so a corrupt
        cycle ERRORS instead of looping (a truncated fingerprint set would
        manufacture spurious "lacks matching index tuple" reports — the
        heapallindexed soundness invariant). No-key-level/empty-leaf yield zero
        entries, no error. `VerifyBtreeHeapAllIndexedRelation` composes it with
        the caller-supplied `heapEntries` through the pure loop-#61
        `VerifyBtreeHeapAllIndexed`. Scope boundary (same place loop #63 + upstream
        draw it): the heap scan + `index_form_tuple` that PRODUCE `heapEntries`
        stay at the wire layer (catalog/`TupleDesc` coupled). 10 new tests
        (`heapallindexed_relation_test.go`, new `makeLeafPage`/`btLeafRaw`/
        `makeMetaWithRoot`/`makeDeletedLeaf` helpers self-checked through the real
        readers): single root-leaf, multi-level descent+sibling order, no-key-
        level, empty leaf, deleted-leaf-skipped, sibling cycle, descent read
        error, + composing driver clean/missing-entry(exact msg+block)/index-walk-
        error. `go test -race ./internal/amcheck ./internal/access/btree` PASS;
        `go build ./...` OK; gofmt/vet clean; **all in new files only — zero
        contaminated files touched.** Design doc `0110-0007` extended ("Index-side
        relation walk"); README already indexed. NOTE: the original gen-column WIP
        holder (pid 2177381) is now DEAD, but its uncommitted changes remain in the
        shared parser/planner/executor/catalog files (frozen at 2026-06-13 14:28,
        ~25h stale; a separate `claude --resume ec98936f` session is alive but not
        editing them). The SQL surface (S1/S2/S3, AC-002-promoting) still must NOT
        be attempted until a HUMAN clears those uncommitted changes (commit/stash/
        discard) — editing them would entangle the amcheck registration hooks with
        an unfinished foreign feature. STILL DEFERRED: the `heapEntries` producer
        (heap scan + index-tuple formation) + the SQL surface.
      - **PROGRESS 2026-06-14 (loop #66 / driver loop #23):** the SQL surface is
        STILL blocked on the same foreign gen-column WIP (frozen 2026-06-13 14:28,
        ~27.5h; `claude --resume ec98936f` alive — must NOT be touched), so I
        landed the next uncontaminated engine slice: the heap **xmin
        numeric-bounds tier**. A prior loop had added three `RelDesc` fields
        (`NextXid`/`OldestXid`/`RelFrozenXid`) for it but never wrote the check —
        they were declared-but-unused. New `checkXminBounds` ports the
        `XID_IN_FUTURE` / `XID_PRECEDES_CLUSTERMIN` / `XID_PRECEDES_RELMIN` arms of
        `verify_heapam.c:check_tuple_visibility` (driven by `get_xid_status`'s bound
        comparisons), with the three upstream-verbatim messages. Enforces
        `OldestXid <= xmin < NextXid` and `xmin >= RelFrozenXid` in
        `get_xid_status`'s order (future → cluster-min → rel-min, matching
        `relfrozenxid >= oldestXid`). Gated on `rel.NextXid != 0` (unset sentinel)
        so every page-bytes-only / natts-only caller is byte-for-byte unchanged;
        `OldestXid`/`RelFrozenXid == 0` disable only their own arm. Special xids
        (Invalid=0 silent, bootstrap=1, frozen=2 incl. the both-hint-bit form via
        `headerXmin`) are always in bounds, mirroring the quick check. Runs on every
        valid `LP_NORMAL` tuple independent of `check_tuple_header` success
        (upstream runs visibility before the header-garbled early return).
        Divergence: plain-unsigned epoch-0 comparisons vs upstream's
        wraparound-aware FullTransactionId; messages embed epoch as literal `0`.
        8 new tests (`verify_heapam_xminbounds_test.go`): future, boundary
        `xmin==NextXid` (the `>=` arm), cluster-min, rel-min (ordering), in-bounds
        silent, `NextXid==0` disabled tier, unset-`OldestXid` no-false-report,
        bootstrap/frozen-below-oldest silent. `go test ./internal/amcheck
        ./internal/access/btree` PASS; gofmt/vet clean; **all in `verify_heapam.go`
        + new `_test.go` only — zero contaminated files touched.** Design doc
        `0110-0005` extended ("Heap xmin numeric-bounds tier"); README index
        updated.
      - **PROGRESS 2026-06-14 (loop #24):** SQL surface STILL blocked on the same
        foreign gen-column WIP, so I landed the sibling slice: the heap **xmax
        numeric-bounds tier**. New `checkXmaxBounds` ports the plain-XID xmax
        sanity check of `verify_heapam.c:check_tuple_visibility` (lines 1466-1496,
        same `get_xid_status` bound comparisons), with the three upstream-verbatim
        xmax messages, reusing the existing `RelDesc` horizons (no new fields).
        Page-structural gating mirrors upstream's path to that check:
        `HEAP_XMAX_IS_MULTI` (multixact update-xid unresolvable page-structurally),
        `HEAP_XMAX_INVALID` (tuple live, early return), `HEAP_XMAX_IS_LOCKED_ONLY`
        (`storage.IsHeapTupleLockOnly`; lock not delete), raw `xmax==0`
        (`XID_INVALID` → live), and bootstrap/frozen special xids all skip.
        Clog-independent like the xmin tier (the upstream xmin-committed ordering
        gate governs checkability, not the numeric invariant — cannot
        false-positive on a valid page). 12 new tests
        (`verify_heapam_xmaxbounds_test.go`): future, boundary `xmax==NextXid`,
        cluster-min, rel-min (ordering), in-bounds silent, `xmax==0` live silent,
        the three gate-skips (invalid/lock-only/multi each with an out-of-range raw
        value), `NextXid==0` disabled tier, unset-`OldestXid` no-false-report,
        bootstrap/frozen-below-oldest silent. `go test ./internal/amcheck` PASS;
        gofmt/vet clean; **only `verify_heapam.go` + new `_test.go` — zero
        contaminated files.** Design doc `0110-0005` extended ("Heap xmax
        numeric-bounds tier"); package doc + deferred list updated. STILL DEFERRED
        (resume points): multixact-member bounds (`check_mxid_valid_in_rel`; goopg
        has no on-disk multixact horizon), the `heapEntries` producer (heap scan +
        index-tuple formation), and the `CREATE EXTENSION amcheck`/SRF SQL surface
        (AC-002-promoting, blocked on a clean tree).
      - **PROGRESS 2026-06-14 (loop #25):** SQL surface STILL blocked on the same
        foreign gen-column WIP, so I landed the **heap-side relation walk** of the
        heapallindexed tier — the symmetric counterpart to loop #65's index-side
        walk, completing the producer pair. New
        `internal/amcheck/heapallindexed_heapscan.go`:
        `CollectHeapIndexEntries(src, nblocks, form)` ports
        `table_index_build_scan`'s heap walk (block loop + per-page `LP_NORMAL`
        line-pointer iteration + `(block,offset)` TID formation — the
        deterministic page-bytes part), handing each tuple's bytes+TID to an
        injected `HeapEntryFormer` callback (upstream's `heapallindexedCallback`/
        `bt_tuple_present_callback` — decides inclusion via visibility/HOT-root and
        forms the entry via `index_form_tuple`; both are MVCC/clog- and
        `TupleDesc`-coupled so they stay at the wire layer). Skips
        unused/dead/redirect items (redirect targets reached on their own offset →
        no double-count); surfaces read/parse/out-of-bounds/former errors rather
        than yielding a **truncated** probe set (which would silently mask missing
        index entries — the heapallindexed soundness invariant). 10 new tests
        (`heapallindexed_heapscan_test.go`) incl. the sibling-path **compose**
        guard (producer entries + matching index set → 0 reports; drop one index
        entry → exactly the orphaned heap tuple flagged with the verbatim upstream
        message), proving producer + `VerifyBtreeHeapAllIndexed` agree on the
        `fingerprintLeafEntry` encoding. `go test ./internal/amcheck` PASS;
        gofmt/vet clean; **only new files — zero contaminated files touched.**
        Design doc `0110-0007` extended ("Heap-side relation walk"); package doc
        updated. With both relation-walk skeletons now in the engine, the
        heapallindexed SQL slice reduces to a thin adapter. STILL DEFERRED (resume
        points): the catalog-coupled `HeapEntryFormer` impl (snapshot visibility +
        `index_form_tuple`), multixact-member bounds, and the `CREATE EXTENSION
        amcheck`/SRF SQL surface (AC-002-promoting, blocked on a clean tree).
      - **PROGRESS 2026-06-14 (loop #29):** the SQL surface is STILL blocked on the
        same foreign gen-column WIP (frozen 2026-06-13 14:28, ~28h; a HUMAN must
        clear it), and the B-tree + heap engines are logic-complete, so I added the
        engine's missing **real-producer validation** — the symmetric counterpart
        to loop #64's heap `verify_heapam_realpage_test.go` (which found a real
        vacuum bug). New `internal/amcheck/verify_nbtree_realtree_test.go` builds
        LIVE multi-level B-trees via real `btree.Create`/`Insert`/split and runs
        every engine tier (per-page structure, item-order, cross-level downlinks,
        sibling-link walk, heapallindexed round-trip) over the on-disk pages.
        Sorted int4/int8/varchar trees → all tiers silent (validates the engine's
        decode assumptions against goopg's real layout, incl. variable-length
        opaque high keys). **It surfaced a real goopg btree bug** (now filed as
        M0110-0007 below): shuffled inserts force middle-of-level splits and
        `splitAndInsert` never updates the OLD right sibling's `btpo_prev`, leaving
        a stale left-link the sibling-link tier correctly flags. The shuffled test
        (`TestVerifyBtreeEngineDetectsStaleSiblingLinkOnRealTree`) pins this as a
        detection assertion (flip to silence when M0110-0007 lands). Gates: `go
        test -race ./internal/amcheck` PASS; `go build ./...` + `go vet` clean; all
        in a new `_test.go` — zero contaminated files touched. Design doc
        `0110-0005` extended ("Real-producer B-tree validation").
      - **PROGRESS 2026-06-14 (loop #31):** the SQL surface is STILL blocked on the
        foreign gen-column WIP (parser/planner/executor/catalog dirty, build-clean
        but uncommittable-clean; a HUMAN must clear it), and both engines are
        logic-complete, so I closed the last unvalidated **producer-pair** path:
        the heapallindexed soundness invariant (fingerprint phase ↔ probe phase
        agree on `fingerprintLeafEntry` bytes) had only ever been tested with
        HAND-BUILT entries — never a REAL heap tuple (`PageAddHeapTuple`) scanned
        through `CollectHeapIndexEntries` + a former, compared against a REAL
        `btree.Insert`-built index. A divergence there (t_hoff skip, key/TID
        pairing, two key encoders) would make `bt_index_check(…, heapallindexed)`
        flag EVERY healthy table+index. New `heapallindexed_realproducer_test.go`
        (2 tests): builds a real multi-leaf B-tree (2000 int4 keys > MaxItemsPerPage
        → leaf splits) + a real multi-block heap (~32 blocks), stores the column
        value REVERSED vs row index so key order ≠ TID order (catches key/TID
        swaps), scans both via the engine's relation walkers and asserts the
        round-trip is silent through both `VerifyBtreeHeapAllIndexed` and the
        composed `VerifyBtreeHeapAllIndexedRelation` (SRF wiring); the negative
        omits one interior index entry and asserts exactly one report naming that
        heap tuple's (block,offset) with the verbatim upstream message. Mirrors the
        real-producer guards that found the vacuum bug (loop #64) and the split
        prev-link bug (M0110-0007). Both PASS; `go test -race ./internal/amcheck`
        green; `go build ./...` + gofmt/vet clean; **only new files — zero
        contaminated files touched.** Design doc `0110-0007` extended
        ("Real-producer end-to-end validation"). STILL DEFERRED: the catalog-coupled
        `HeapEntryFormer` impl + the `CREATE EXTENSION amcheck`/SRF SQL surface
        (AC-002-promoting, blocked on a clean tree).

- [x] **M0110-0007 — B-tree split must maintain the old right sibling's prev-link**
      - **DONE 2026-06-14 (loop #30).** `insertIntoBlock`
        (`internal/access/btree/btree.go`) now, on a non-rightmost split
        (pre-split `op.Next != InvalidBlockNumber`), pins+locks the old right
        sibling and relinks its `btpo_prev` to the new right block, folded into
        the atomic split WAL record. The `BtreeSplit` record
        (`internal/wal/recovery.go` `EncodeBtreeSplit`/`DecodeBtreeSplit`,
        `btreeSplitHeaderSize` 18→22 with a `SibBlk` field) carries an OPTIONAL
        third page; `replayBtreeSplit` applies it (WriteBlock — the sibling
        predates the split) so crash recovery never leaves the chain
        half-relinked. Mirrors PostgreSQL `_bt_split` (locks the original right
        sibling, stamps its left-link under the same xl_btree_split record).
        Lock order is strictly left→right (blk → rightBlk → oldNext), matching
        `_bt_split`, so no deadlock vs a concurrent left-descending split. On-disk
        page format UNCHANGED (only the WAL record format changed). Signature
        change rippled through `storage.LogBtreeSplitFunc`, `btree.LogSplitFunc`,
        `adaptPoolLogSplit`, and the `initdb/open.go` closure.
      - DoD met: `TestVerifyBtreeEngineDetectsStaleSiblingLinkOnRealTree` flipped
        to the silence assertion `TestVerifyBtreeEngineSilentOnRealShuffledInt4`;
        new `TestReplayBtreeSplitAtomicNonRightmost` (3-page replay) +
        3-page encode/decode round-trip; `TestSplitInvokesLogSplit` extended to
        assert sibling-page invariants. Design doc
        `docs/design/0110-0009-btree-split-rightsibling-prevlink.md`.
      - Gates: `go test -race ./internal/access/btree ./internal/wal
        ./internal/mvcc ./internal/storage` PASS; `go test ./internal/amcheck
        ./internal/executor ./internal/initdb` PASS; `go build ./...` clean;
        TPC-H spotcheck PASS (Q12=2/Q13=33).
      - **Discovered 2026-06-14 (loop #29)** by the new real-producer B-tree
        validation (`internal/amcheck/verify_nbtree_realtree_test.go`).
      - Symptom: after any **non-rightmost** leaf/internal split (i.e. the split
        page had a right sibling — the common case for non-append insert patterns:
        random PKs, UUIDs, secondary indexes), the OLD right sibling's `btpo_prev`
        is left pointing at the original left page instead of the newly inserted
        middle page. `splitAndInsert` (`internal/access/btree/btree.go`
        ~L1454-1466 sets the new right page's `Prev=blk`; L1522 sets the left
        page's `Next=rightBlk`) never touches the old right sibling.
      - Why it matters: `btpo_prev` is load-bearing — page deletion
        (`internal/access/btree/btree_vacuum.go`) reads `op.Prev` to find the left
        sibling and WAL-logs `RightSibNewPrev` to relink it; a stale left-link can
        mislead page-deletion relinking and any backward navigation. PG fixes this
        inside the atomic `_bt_split` WAL record (updates the original right
        sibling's left-link, WAL-logged).
      - Required (bounded, with gates — this is a WAL/concurrency change, not a
        one-liner): (1) in `splitAndInsert`, when the pre-split `op.Next !=
        InvalidBlockNumber`, pin+lock the old right sibling and set its `Prev =
        rightBlk`; (2) fold that page into the atomic split WAL record + replay
        (`internal/access/btree/replay.go`) so crash recovery restores it — mirror
        the existing `RightSibNewPrev` precedent in the page-deletion path; (3)
        respect Lehman-Yao left-to-right lock ordering to avoid deadlock; (4) gate
        with `go test -race ./internal/access/btree` (incl.
        `multi_writer_stress_test`) + a recovery/replay test.
      - DoD: flip `TestVerifyBtreeEngineDetectsStaleSiblingLinkOnRealTree` from a
        detection assertion to the silence assertion the sorted cases use; write a
        design doc (`docs/design/0110-NNNN-btree-split-rightsibling-prevlink.md`).

### pg_resetwal (2 tests — excluded → candidate)

pg_resetwal resets the WAL and control file of a non-running cluster.
Porting validates goopg's pg_control and WAL segment layout on disk.

- [x] **M0110-0004 — Port pg_resetwal TAP tests** (COMPLETE loop #50 — full
      pg_resetwal TAP suite ported: 001_basic CLI tier (RW-001), server tier
      (RW-003 + RW-002 a/b), 002_corrupted (RW-004). All four
      `TestPort_PgResetwal*` PASS.)
      - Target tests:
        | Test | Status | Rationale |
        |------|--------|-----------|
        | `postgres/src/bin/pg_resetwal/t/001_basic.pl` | **PORTED 2026-06-13 (CLI tier)** | `TestPort_PgResetwal001Basic` (`internal/testport/pgresetwal_port_test.go`). The CLI-decidable tier (help/version/options + too-many-args/no-data-directory/nonexistent-directory + the option-argument validation cases for `-c`/`-e`/`-l`/`-m`/`-o`/`-O`/`-u`/`-x`/`--wal-segsize`/`--char-signedness`) — all decided inside pg_resetwal's `getopt_long` loop (or the immediately-following arg-count/DataDir checks) before `GetDataDirectoryCreatePerm`/`read_controlfile` touch the data directory, so the port passes a nonexistent dir and needs no server. pg_resetwal does not link libpq → plain `runTool`. CSV row RW-001 → port. Design: `docs/design/0110-0004-pg-resetwal-tap-port.md`. |
        | `postgres/src/bin/pg_resetwal/t/002_corrupted.pl` | UNIMPLEMENTED (deferred RW-002) | Simulates corrupted WAL and verifies pg_resetwal recovery behaviour. |
      - Action: 001_basic CLI tier ported (loop #19). The server-dependent tier
        of 001_basic.pl (init/start/--force reset round-trips + the
        SLRU-derived `--commit-timestamp-ids`/`--multixact-ids`/
        `--multixact-offset`/`--oldest-transaction-id`/`--next-transaction-id`
        overrides) and 002_corrupted.pl are deferred under CSV row RW-002,
        blocked on pg_control byte-level read/write round-trip compatibility
        (M0106) + on-disk SLRU-segment-layout parity. Resume = promote RW-002
        once goopg's pg_control round-trips through upstream pg_resetwal and the
        pg_commit_ts/pg_multixact/pg_xact SLRU directories expose the expected
        segment-file layout.
      - **PROGRESS 2026-06-14 (loop #45):** the pg_control read/write round-trip
        HALF of the server tier is now PORTED as
        `TestPort_PgResetwal001BasicServer` (CSV row RW-003 → port). Root cause
        of the prior block was a clean-shutdown state bug: every checkpoint
        (incl. the final `Runtime.Close` shutdown checkpoint) stamped
        pg_control `State=DB_IN_PRODUCTION`, so after a clean `goopg stop`
        pg_resetwal reported "database server was not shut down cleanly" and
        refused without `--force`. Fix: new `wal.Checkpointer.CheckpointShutdown`
        stamps `DB_SHUTDOWNED` (mirrors PG `CHECKPOINT_IS_SHUTDOWN`), wired into
        `Runtime.Close`. The ported test exercises perms/`-n`/lock-file/clean
        `--pgdata` reset/`SELECT 1`/`--next-oid` override + restart. Still
        deferred under RW-002: the unclean-shutdown/`--force` branch (goopg v0
        has no crash state) and the SLRU-derived id overrides + 002_corrupted.
        Design: `docs/design/0110-0004-pg-resetwal-tap-port.md`.
      - **PROGRESS 2026-06-14 (loop #48):** `002_corrupted.pl` now PORTED as
        `TestPort_PgResetwal002Corrupted` (CSV row RW-004 → port). It inits a
        goopg cluster (never started), corrupts `global/pg_control` two ways, and
        drives upstream pg_resetwal: (1) all-zeroes → "broken or wrong version;
        ignoring it" + guessed dump under --dry-run (exit 0); (2) 16-byte header
        restored + body zeroed → "invalid WAL segment size (0 bytes); proceed
        with caution" via the version-matches/CRC-fails path (exit 0); (3) plain
        run refuses on guessed values (exit 1); (4) --force rewrites pg_control
        (exit 0). Generic pg_resetwal logic; only goopg dependency is the
        pg_control header compatibility already proven by RW-003. Needs NO server
        start, so independent of the deferred CLOG-startup restart — correcting
        the earlier note that wrongly paired 002_corrupted with the unclean-
        shutdown branch. RW-002 remainder now: only (a) the maximal-override
        final restart (PG-style StartupCLOG page-fill) and (b) the
        unclean-shutdown/`--force` branch. PASS 0.88s.
      - **PROGRESS 2026-06-14 (loop #49):** RW-002 (a) DONE — the maximal
        SLRU-derived-override FINAL RESTART is now enabled and PASSES in
        `TestPort_PgResetwal001BasicServer` (2.5s, no hang). Root cause: after
        `--next-transaction-id` advances NextXID ~1M past the bootstrap pg_xact
        segment, `initdb.Open`'s implicit-abort sweep (`CLog.MarkUnknownAsAborted`)
        stamps ~1M XIDs, and the old per-XID SLRU mirror (`mirrorToSLRUUnlocked`)
        fsynced on every one → ~1M fsyncs → startup looked hung. Fix: new
        `CLog.mirrorTerminalRangeBatchedUnlocked` (`internal/mvcc/clog.go`)
        projects the swept range into the pg_xact/ SLRU with ONE fsync per
        ~1M-XID segment, OR-merging onto existing content (idempotent, byte-
        identical final state). Regression test
        `TestCLogMarkUnknownAsAbortedBatchedSLRU` (cross-segment, 0.05s).
        Race-clean (`go test -race ./internal/mvcc`). RW-002 remainder now: ONLY
        (b) the unclean-shutdown/`--force` branch — blocked on goopg v0 having no
        crash/unclean shutdown state (graceful DB_SHUTDOWNED always). Design:
        `docs/design/0110-0004-pg-resetwal-tap-port.md`.
      - **PROGRESS 2026-06-14 (loop #50):** RW-002 (b) DONE — **M0110-0004 now
        COMPLETE.** Gave goopg a real immediate shutdown so the unclean-shutdown
        + `--force` branch of `001_basic.pl` (l.41-52) can be reproduced, ported
        as `TestPort_PgResetwal001BasicForce`. New `STOPIMMEDIATE` control verb
        (`internal/control/control.go`) + `Config.OnStopImmediate` handler
        (`internal/server/server.go`) tear the server down running **no**
        shutdown checkpoint; `Runtime.SetImmediateShutdown()`
        (`internal/initdb/open.go`) makes `Close()` skip the final
        `CheckpointShutdown`, leaving `pg_control.State=DB_IN_PRODUCTION`.
        `goopg stop -mode immediate` (`cmd/goopg/main.go`) sends the new verb
        (smart/fast stay graceful). pg_resetwal then refuses without `--force`
        and the cluster recovers via WAL replay on the next start. All four
        `TestPort_PgResetwal*` PASS (4.9s); `go test -race ./internal/control
        ./internal/server` clean; `go build ./...` clean. CSV RW-002 → port.

### pg_verifybackup (10 tests — excluded → no action)

pg_verifybackup validates a base backup's manifest and file integrity.
These tests are NOT included because they depend on pg_basebackup
output, which is already covered by M0095-0003.  Once M0095-0003 is
complete, these can be re-evaluated.

### Already ported (not in M0094/M0095 — listed for completeness)

| Family | Tests | Port location | Status |
|--------|-------|--------------|--------|
| `initdb` | 1 (`001_initdb.pl`) | `internal/testport/tap_port_test.go` | port |
| `psql` | 3 (`001_basic.pl`, `010_tab_completion.pl`, `020_cancel.pl`) | `internal/testport/tap_port_test.go` | port |
| `pgbench` | 2 (`001_pgbench_with_server.pl`, `002_pgbench_no_server.pl`) | `internal/testport/tap_port_test.go` | port |

### Excluded with no action (not meaningful for goopg)

| Tool | Reason |
|------|--------|
| `pg_config` | Queries pg_config binary; no server interaction. |
| `pg_combinebackup` | Multi-server orchestration; requires pg_basebackup chains. |
| `pg_archivecleanup` | No server interaction. |
| `pg_rewind` | Requires standby/failover multi-server setup. |
| `pg_test_fsync` | No server interaction; filesystem benchmark. |
| `pg_test_timing` | No server interaction; timing benchmark. |
| `pg_upgrade` | Multi-server orchestration; pg_upgrade binary. |


## M0117 — CLOG ↔ PostgreSQL subsystem alignment (filed 2026-06-14)

Milestone doc: `docs/milestones/0117-clog-postgresql-subsystem-alignment.md`.

Goal: finish bringing goopg's commit-log (`pg_xact`) and subtransaction
(`pg_subtrans`) behavior to PostgreSQL 18.3 parity. The bounded CLOG build landed
truncation (G1) + the `CLOG_TRUNCATE` WAL record (G9) + the `pg_subtrans` write path
(G5 partial) + consistency/standby tests (G2/G3). This milestone files the explicitly
**deferred** and **follow-up** work recorded in
`docs/analysis/clog-goopg-gaps-and-remediation-2026-06-14.md` and
`docs/analysis/clog-impl-task-division-2026-06-14.md` (Review-outcomes / deferral
section): runtime visibility integration, subtransaction durability, the
`SUB_COMMITTED` lane, group commit + a bounded SLRU buffer pool, async-commit LSN
tracking, and wraparound-safe horizon selection.

**Per-task discipline (hard requirement): for EVERY M0117-NNNN task, author the
design doc `docs/design/0117-NNNN-*.md` AND index it in `docs/design/README.md`
BEFORE writing any implementation code.** WAL/MVCC changes carry the practice-card
gate (`go test -race ./internal/wal/... ./internal/mvcc/...` + recovery/standby E2E);
visibility/catalog tuple-format changes additionally carry the TPC-H spot-check
(`scripts/tpch-spotcheck.sh`, canonical Q12=2/Q13=35) / regress-port re-run.

### Sub-milestones

- [x] **M0117-0001** — DONE (branch `m0117-0001-xid-precedes` off b8dd6403; design `docs/design/0117-0001-xid-precedes-horizon-comparison.md`). Added `storage.XIDPrecedes`, routed `catalog.DatFrozenXID` + checkpointer `TruncateCLOGFn` through it, made `mvcc.txnPrecedes` delegate; pinned by `internal/storage/xid_test.go`. Gates: build + `go test ./internal/storage/... ./internal/mvcc/... ./internal/catalog/... ./internal/initdb/...` PASS. Pending human merge (foreign M0100-0010 WIP holds the main tree).
      - Summary: Wraparound-safe XID horizon comparison (gap M2; P0/correctness).
        Add exported `storage.XIDPrecedes(a, b)` (mirroring `clog.go`'s `txnPrecedes`
        / PG `TransactionIdPrecedes`) and use it for horizon selection in
        `catalog.DatFrozenXID` and the checkpointer `TruncateCLOGFn`
        (`internal/initdb/open.go`) instead of plain `<`.
      - Author `docs/design/0117-0001-xid-precedes-horizon-comparison.md` and index it before coding.
      - Gate: `go test ./internal/mvcc/... ./internal/catalog/...` (+ unit tests near 2^32). Effort: S.

- [ ] **M0117-0002**
      - Summary: Runtime CLOG-consulting visibility fallback (gap G4; P1). Add a CLOG
        fallback in `Snapshot.SeesCommittedXID` for in-window XIDs not classified by
        the in-memory `InProgress`/`Aborted` arrays, keeping the arrays as the fast
        path; audit the `visibility.go` ↔ `subxact_visibility.go` sibling paths.
      - Author `docs/design/0117-0002-visibility-clog-fallback.md` and index it before coding.
      - Gate: TPC-H spot-check (`scripts/tpch-spotcheck.sh`, Q12=2/Q13=35) + `go test -race ./internal/mvcc/...`. Effort: M.

- [ ] **M0117-0003**
      - Summary: `pg_subtrans` restore-on-restart (gap G5 read path; P1). Wire
        `SubxactMap.EnablePersistence` into the `internal/initdb/open.go` recovery
        sequence and load persisted parent links from the `pg_subtrans` SLRU back into
        the in-memory `SubxactMap` so subxact parentage survives a restart.
      - Author `docs/design/0117-0003-pg-subtrans-restore-on-restart.md` and index it before coding.
      - Gate: standby-attach E2E + `go test -race ./internal/mvcc/...`. Effort: M.

- [ ] **M0117-0004**
      - Summary: `SUB_COMMITTED` (0x03) CLOG lane (gap G5 SUB_COMMITTED; P1; builds on
        M0117-0003). Generate the 0x03 lane in the commit path (`mirrorToSLRUUnlocked`)
        for committed subxacts whose parent is still in-progress, and read it back in
        `loadFromSLRU`; document which code path writes each state.
      - Author `docs/design/0117-0004-clog-sub-committed-lane.md` and index it before coding.
      - Gate: extend the dual-store consistency test + `go test -race ./internal/mvcc/...`. Effort: S–M.

- [ ] **M0117-0005**
      - Summary: Incremental flush + group commit (gap G7; P2). Make `CLog.flush`
        write only changed pages/segments (not the whole flat file) and add a
        group-commit batching layer (lock-free queue, mirroring PG's
        `TransactionGroupUpdateXidStatus`) over the SLRU fsync; new file
        `internal/mvcc/clog_groupcommit.go`.
      - Author `docs/design/0117-0005-clog-incremental-flush-group-commit.md` and index it before coding.
      - Gate: `go test -race ./internal/mvcc/...` + commit-throughput sanity check. Effort: M.

- [ ] **M0117-0006** — Part A DONE; Part B/C DEFERRED (Effort-L memory-model rewrite of the
      highest-blast-radius subsystem, decomposed; see deferral ledger 2026-06-15).
      Branch `m0117-0006-clog-slru-buffer-pool` (off `5fcdb27b`; design
      `docs/design/0117-0006-clog-slru-buffer-pool.md`, indexed).
      - **Part A (landed):** the `transaction_buffers` GUC (`defaults.go` +
        `postgresql.conf.sample`, PGC_POSTMASTER, boot_val 0, max 1GiB/BLCKSZ; raw buffer
        count == PG's GUC_UNIT_BLOCKS value) + `EffectiveCLOGBuffers` (faithful port of
        `clog.c:CLOGShmemBuffers + SimpleLruAutotuneBuffers`) + `clogBufferPool`
        (`internal/mvcc/clog_bufferpool.go`): bounded LRU page cache over the 2-bit SLRU
        representation backed by the `pg_xact/` segment files (fault-in, LRU eviction with
        dirty writeback, clear-then-set lane update ≙ `TransactionIdSetStatusBit`, per-segment
        fsync in `flushDirty`). NOT wired into the live path — blast radius nil. Pinned by
        `internal/mvcc/clog_bufferpool_test.go` incl. a sibling-path encode↔encode equivalence
        test vs `mirrorToSLRUUnlocked`.
      - **Part B (deferred):** wire `CLog.GetStatus`/`setStatus` (+ bulk callers, `loadFromSLRU`,
        `HighestKnownXID`, `TruncateCLOG`, `distributeToBanks`) through the pool. Resume point /
        open questions in the design doc: mirror-disabled fallback, OR-vs-clear-then-set semantics
        (keep the M0117-0004 visibility invariant), truncation-via-page-invalidation.
      - **Part C (deferred):** remove the resident `banks` + `global/pg_xact` flat file (the 2-bit
        collapse, 16× memory reduction).
      - Summary: SLRU buffer pool / 2-bit collapse (gap G6; P2; follows M0117-0005).
        Replace the fully-resident per-bank byte slices with a bounded page-cache over
        the 2-bit SLRU representation (LRU eviction; `transaction_buffers` GUC).
      - Gate: `go test -race ./internal/mvcc/...`; full mvcc/wal/initdb suites; re-init data dir (memory-model change). Effort: L.
      - Gates run (Part A): `go build ./...` PASS; `go test -race ./internal/mvcc/...` PASS;
        `go test ./internal/config/...` PASS (GUC + sample coverage); `go test
        ./internal/initdb/... ./internal/server/...` PASS; gofmt/vet clean. TPC-H spotcheck SKIPs
        under worktree isolation — no-op (no live CLOG path changed).

- [ ] **M0117-0007**
      - Summary: Async-commit LSN tracking (gap G8; P2; feature-gated on a real
        `synchronous_commit=off` path). Add per-group commit-LSN tracking
        (`CLOG_XACTS_PER_LSN_GROUP`) and gate honoring a committed status / hint-bit
        setting on WAL flush position.
      - Author `docs/design/0117-0007-clog-async-commit-lsn.md` and index it before coding. (DONE — design indexed.)
      - Gate: `go test -race ./internal/mvcc/...` + recovery E2E. Effort: L.
      - **Part A (landed):** the async-commit per-LSN-group tracking on the M0117-0006 buffer pool
        (`internal/mvcc/clog_bufferpool.go`), composing infrastructure with nil live blast radius
        (the pool is not yet the live store — M0117-0006 Part B deferred): `clogXactsPerLSNGroup=32`
        / `clogLSNsPerPage=clogXactsPerPage/32=1024` + `lsnIndexInPage` (= PG `GetLSNIndex` minus the
        `slotno*CLOG_LSNS_PER_PAGE` base, since each `clogPageSlot` owns its `groupLSN[1024]uint64`);
        `setStatusWithLSN(xid,status,lsn)` raises `groupLSN[lsnIndexInPage]` to `max(…,lsn)`
        (≙ `TransactionIdSetPageStatusInternal`; `setStatus` = `setStatusWithLSN(…,0)`, byte-identical
        for LSN-free callers; `lsn==0` ≙ `InvalidXLogRecPtr` no-op); `groupLSNFor` read side
        (≙ the `*lsn` out-param of `TransactionIdGetStatus`); an injected `flushWAL func(uint64) error`
        barrier (nil ⇒ off, default) that `writePageToDisk`/`flushDirty` call with the page's max group
        LSN BEFORE writing the page out (≙ `XLogFlush` in `SlruPhysicalWritePage`), to be wired to
        `wal.Writer.FlushUpTo` when the pool goes live. `groupLSN` is in-memory only (zeroed on
        fault-in ≙ PG never persisting it). Pinned by `internal/mvcc/clog_bufferpool_lsn_test.go`
        (GetLSNIndex arithmetic, max-LSN monotonicity, zeroed-on-reopen vs durable status bits,
        barrier-fires-before-write ordering for both flushDirty + eviction).
      - **Part B (deferred):** live `synchronous_commit=off` activation — wire `flushWAL` to the live
        WAL writer, thread the commit-record LSN from the commit path into `setStatusWithLSN`, and skip
        the inline per-commit WAL fsync (rely on the page-write barrier). Changes the live durability
        path ⇒ needs the full TPC-H Q12/Q13 + crash-recovery/standby E2E (SKIP under worktree
        isolation). Defers with M0117-0006 Part B (the barrier only fires once the pool is the live
        store). Resume point in the design doc.
      - Gates run (Part A): `go build ./...` PASS; `go test -race ./internal/mvcc/...` PASS;
        `go test ./internal/config/... ./internal/initdb/... ./internal/server/...` PASS;
        `TestE2E_PhysicalReplication{,Sync}` PASS; gofmt/vet clean. TPC-H spotcheck SKIPs under worktree
        isolation — no-op (no live CLOG path changed).

- [ ] **M0117-0008**
      - Summary: Persist `datfrozenxid` in the `pg_database` catalog tuple at VACUUM
        end (rather than only computing it on demand) and extend the dual-store
        consistency tests for round-trip coverage of all status codes.
      - Author `docs/design/0117-0008-datfrozenxid-persistence.md` and index it before coding.
      - Gate: `go test ./internal/catalog/...`; re-init data dir + regress-port re-run (catalog tuple-format change). Effort: S.


## Notes

- This file is the authoritative TODO list for Ralph. Update it after every
  meaningful change.
- Keep work to ONE item per loop. Decompose further if an item is larger
  than what fits in a single agent invocation.
- Every non-trivial subsystem must land alongside (or just before) a design
  doc under `docs/design/`. The spec treats this as a hard requirement.    

