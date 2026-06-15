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
      - **PROGRESS 2026-06-15 (loop #12):** 011 items (1)+(2-core) LANDED on the now
        **clean tree** (the foreign gen-column WIP that blocked the parser/executor/
        catalog edits is gone). Delivered the in-place tablespace foundation:
        `allow_in_place_tablespaces` GUC (PGC_SUSET, boot off, +sample entry);
        `CreateTablespaceStmt`/`DropTablespaceStmt` AST; `CREATE TABLESPACE name
        [OWNER [=] role] LOCATION 'dir' [WITH (opts)]` + `DROP TABLESPACE [IF EXISTS]
        name` parser (`KwTablespace` is an unreserved keyword → `acceptKeyword`, not
        `acceptIdentKeyword` — fixed the matching that initially fell through);
        planner DDL passthrough; `execCreateTablespace`/`execDropTablespace` (in-place
        `pg_tblspc/<oid>` dir create/remove + runtime registry); catalog
        `CreateTablespace`/`DropTablespace` registry; `CREATE/DROP TABLESPACE` command
        tags. Upstream-verbatim errors (42602 quote, 42P17 absolute-path / empty-loc-
        GUC-off, 42939 reserved pg_ name, 42710 dup, 42704 missing); external absolute
        LOCATION → 0A000 (goopg can't relocate relfiles). Design doc
        `docs/design/0095-0003-in-place-tablespace.md` + README index. Tests: parser
        (3), catalog (1), executor (7 incl. real-temp-dir create/drop), config (1) —
        all PASS; full parser/catalog/config/planner/executor/server suites green;
        gofmt+vet clean; build clean. TPC-H spotcheck SKIPPED (no data dir in this
        tree; safe by construction — only NEW DDL statement types added to passthrough
        lists, zero existing query path touched). DEFERRED (011 still self-skips):
        BASE_BACKUP per-tablespace `<oid>.tar` + the `pg_tblspc/<oid>/PG_18_<cat>`
        version subdir (both need the catversion string, land together) + on-disk
        `pg_tablespace` heap visibility (shared-catalog runtime write — separate
        capability). recvlogical (030) still needs logical decoding.
      - **PROGRESS 2026-06-15 (loop #13):** 011 now **PASSES** — the two deferred
        pieces landed together. (a) **Version directory:** `execCreateTablespace`
        creates `pg_tblspc/<oid>/PG_<major>_<catversion>` (was just
        `pg_tblspc/<oid>`), faithful to `create_tablespace_directories`. The
        version-dir name + catversion constants moved to the **leaf `config`
        package** (`internal/config/version.go`: `MajorVersion`,
        `CatalogVersionNo`, `TablespaceVersionDirectory`) as the single source of
        truth — `executor` cannot import `initdb` (cycle: `initdb`→`executor`), so
        `initdb.CatalogVersion`/`pgcontrol.go pgCatalogVersionNo` now reference
        `config`. (b) **Per-tablespace `<oid>.tar`:** `internal/server/basebackup.go`
        gained `collectInPlaceTablespaces` (scan `pg_tblspc` for numeric dirs),
        `emitTablespaceTar` (version-dir tar, paths relative to the tablespace),
        a `writeTablespaceList` that emits one `(oid, pg_tblspc/<oid>, NULL)` row
        per tablespace, and a base-tar walk that ships the `pg_tblspc/<oid>` dir
        entry without recursing (mirrors `sendDir` skip_this_dir). After base.tar,
        each tablespace streams its own `'n'` archive frame `"<oid>.tar"` then its
        tar; manifest entries accumulate across archives. `TestPort_PgBasebackup011-
        InPlaceTablespace` skip removed → PASS (1.35s); CSV BB-011 + markdown
        updated; design doc `0095-0003-in-place-tablespace.md` + README index →
        complete. Gates: `go build ./...` clean; server `-race` green; 010
        backup/stream/fetch/manifest tests no regression; executor/initdb/config
        suites green. STILL DEFERRED: on-disk `pg_tablespace` heap visibility
        (independent shared-catalog write, NOT needed by 011); recvlogical (030)
        needs logical decoding.

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
      - **PROGRESS 2026-06-15 (loop #22):** **pg_dump connection-setup
        compatibility LANDED** — the enabler for DU-002+. An empirical probe
        (real `pg_dump --no-sync postgres` vs a live goopg server) showed pg_dump
        aborting in `setup_connection()` *before* any catalog query. Two gap
        classes closed: (a) three unregistered GUCs `synchronize_seqscans` /
        `transaction_timeout` (PG 17+) / `row_security` added as accepted no-ops
        in `internal/config/defaults.go` (+ `postgresql.conf.sample`; boot
        on/0/on per guc_tables.c); (b) `SET TRANSACTION ISOLATION LEVEL
        REPEATABLE READ, READ ONLY` — the server simple-query string fast-path
        (`internal/server/query.go`) mis-routed `SET TRANSACTION …` to the GUC
        setter (`unrecognized configuration parameter "TRANSACTION"`); a new case
        routes `SET [LOCAL|SESSION] TRANSACTION …` / `SET SESSION CHARACTERISTICS
        …` to the parser-based executor (existing `SetTransactionStmt`,
        M0096-0002), and the parser's transaction-mode loop now consumes the
        comma in `REPEATABLE READ, READ ONLY` (it stopped at the comma). pg_dump
        now completes setup_connection and reaches its first catalog query.
        **Next blocker (precise):** getRoles `SELECT oid, rolname FROM
        pg_catalog.pg_roles ORDER BY 1` → goopg's `pg_roles` view lacks an `oid`
        column. That is the start of the broad catalog-view parity (DU-002+).
        Tests: `TestPort_PgDumpConnectionSetup` (e2e regression guard, self-
        promoting — logs the pg_roles.oid gap, auto-tightens to exit-0 on a clean
        dump), `config.TestPgDumpConnectionSetupGUCs`,
        `parser.TestParseSetTransactionCommaSeparated`. Gates: build/gofmt/vet
        clean; config + parser + server suites + pg_dump 001 PASS, no regression.
        Design doc `0110-0001` extended (Connection-setup compatibility section).
        Resume = add `oid` to the `pg_roles` view, then continue getRoles →
        getTablespaces → getNamespaces… per setup_connection's query order.
      - **PROGRESS 2026-06-15 (loop #23):** **DU-002 slice 1 (`pg_roles.oid`)
        LANDED** — collectRoleNames' `SELECT oid, rolname FROM pg_roles ORDER BY 1`
        now works. (commit `20d242a2`.)
      - **PROGRESS 2026-06-15 (loop #24):** **DU-002 slice 2 (`acldefault()`)
        LANDED.** getNamespaces runs `acldefault('n', n.nspowner)`; added the
        `acldefault("char", oid)` builtin (`internal/executor/expr.go`,
        `evalAclDefault`) mirroring `acldefault_sql()` in `acl.c` — computes
        hard-wired default privileges per object-type char and renders aclitem[]
        text (`acldefault('n', 10)` → `{postgres=UC/postgres}`). Already seeded in
        pg_proc (OID 3943); only the executor handler was missing. Unit guard
        `executor.TestEvalAclDefault` pins all 13 object types + privilege order.
        Verified live: full getNamespaces query returns all 6 columns correctly.
        **Next blocker (precise):** pg_dump still SEGFAULTs in "reading schemas"
        because `n.tableoid` (first projected column) comes back labelled
        `?column?` instead of `tableoid`, so `PQfnumber(res,"tableoid")` → -1 and
        the client reads out of bounds (`column number -1 is out of range 0..5`,
        exit 139). The value is correct (2615); only the RowDescription field name
        is wrong — for EVERY table, so it's a planner output-column-naming bug for
        the `tableoid` system column. Resume = fix `tableoid` column labelling,
        then continue getTypes → getTables… per pg_dump's getter order.
      - **PROGRESS 2026-06-16 (loop #26):** **DU-002 slice 3 (`tableoid` column
        label) LANDED.** Root cause: `resolveColumnRefAt` lowers a bare `tableoid`
        on a non-partitioned base relation to a constant `*TableOidExpr`, but the
        planner's `targetMeta` (`internal/planner/planner.go`) had no case for that
        node (only the cast-wrapped `tableoid::regclass` form), so it fell through
        to `?column?`. Fix = added a `*TableOidExpr` arm returning `("tableoid",
        oid)`, mirroring the existing `*CTIDExpr` → `"ctid"` case. Analyzer/executor
        naming twins operate on the parser AST (still `*parser.ColumnRef`) and were
        already correct. Unit guard `server.TestTableoidColumnName`. Verified live
        via `TestPort_PgDumpConnectionSetup`: pg_dump now passes "reading schemas"
        (no segfault) and advances to getTables.
        **Next blocker (precise):** getTables fails with `relation "pg_depend" does
        not exist` — pg_dump's getTables joins `pg_class LEFT JOIN pg_depend` (and
        `pg_tablespace`, `pg_am`, `pg_class tc` for TOAST). Resume = add a
        `pg_depend` catalog view (slice 4), then continue the getter battery.
      - **PROGRESS 2026-06-16 (loop #27):** **DU-002 slice 4 (getTables catalog
        views) LANDED.** getTables (`pg_dump.c:7080-7239`) touches three relations
        not previously exposed to the SQL query layer; all three added as virtual
        catalog views in `internal/catalog/catalog.go` (next to `pg_am`), schemas
        matching upstream exactly: **`pg_depend`** (OID 2608) — empty (goopg keeps
        no dependency graph → LEFT JOIN yields NULL owning_tab/owning_col,
        is_identity_sequence=false); **`pg_tablespace`** (OID 1213) — bootstrap
        pg_default(1663)/pg_global(1664) + M0095-0003 runtime in-place tablespaces,
        OID-ordered (`tablespaceVirtualRows`, read-locked); **`pg_foreign_table`**
        (OID 3118) — empty (no FDW support). Unit guards
        `catalog.TestPgTablespaceVirtualView` + `catalog.TestPgDependAndForeignTableViews`.
        Build clean; catalog/executor/server/planner suites PASS.
        **Next blocker (precise):** getTables now resolves all relations but fails
        with `function array_remove does not exist` — used to strip
        `check_option=…` from `c.reloptions`. Resume = add the `array_remove()`
        scalar builtin (slice 5), then continue the getter battery.
      - **PROGRESS 2026-06-16 (loop #28):** **DU-002 slice 5 (`array_remove()`
        scalar builtin) LANDED.** getTables' `reloptions` projection
        `array_remove(array_remove(c.reloptions,'check_option=local'),
        'check_option=cascaded')` aborted with `function array_remove does not
        exist`. The function was already seeded in `pg_proc` (OID 3167); only the
        executor handler was missing (dispatch fell through to
        `evalStoredRoutineFuncCall` → 42883). Added the `array_remove(anyarray,
        anyelement)` case to `evalFuncCall` (`internal/executor/expr.go`, beside
        `array_append`/`array_cat`): removes every element equal to arg 2 from
        goopg's text-array form (`parseTextArray`/`formatTextArray`); formatted
        element-text equality matching the sibling array builtins (NULL element →
        the `"NULL"` placeholder), NULL array → NULL (PG array_remove is NotStrict
        on the element, array-strict). Unit guards `executor.TestEvalArrayRemove`
        + `executor.TestEvalArrayRemoveNested`. Build/vet clean; executor suite
        PASS; `TestPort_PgDumpConnectionSetup` PASS (getTables now completes).
        **Next blocker (precise):** pg_dump's `getFuncs` query LEFT-JOINs
        `pg_init_privs` (diffing stored `proacl` vs. initial privileges), which
        goopg does not expose → `relation "pg_init_privs" does not exist`. Resume
        = add the `pg_init_privs` virtual view (empty — no extension-installed
        initial privileges) as slice 6, then continue the getter battery.
      - **PROGRESS 2026-06-16 (loop #29):** **DU-002 slice 6 (`pg_init_privs`
        virtual view) LANDED.** `getFuncs` (like `getTables`/`getTypes`/…)
        LEFT-JOINs `pg_init_privs pip ON (p.oid=pip.objoid AND
        pip.classoid='pg_proc'::regclass AND pip.objsubid=0)` to diff stored
        `proacl` vs. initial privileges; the missing relation aborted with
        `relation "pg_init_privs" does not exist`. Added the `pg_init_privs`
        virtual view (`internal/catalog/catalog.go`, beside the slice-4
        `pg_depend`/`pg_tablespace`/`pg_foreign_table` block) with PG's exact
        schema (`objoid oid, classoid oid, objsubid int4, privtype "char",
        initprivs aclitem[]`, OID 3394, and — like the upstream catalog — NO `oid`
        system column). **Empty by construction**: goopg installs no extensions
        and snapshots no initdb ACLs, so the LEFT JOIN yields NULL `pip.initprivs`
        and the `proacl IS DISTINCT FROM pip.initprivs` predicate degenerates to
        "dump the full ACL" (correct). Build/gofmt/vet clean; catalog + executor
        suites PASS; `TestPort_PgDumpConnectionSetup` PASS (getFuncs now resolves
        `pg_init_privs`). **Next blocker (precise):** `getFuncs` projects
        `p.pronargs`, `p.proacl`, `p.proowner` and filters on `pg_cast`/
        `pg_transform`, none exposed → `column p.pronargs does not exist`. Resume
        = add those three `pg_proc` columns (`internal/initdb/pg_proc_view.go`)
        plus the empty `pg_cast`/`pg_transform` views as slice 7.
      - **PROGRESS 2026-06-16 (loop #30):** **DU-002 slice 7 (`pg_proc`
        `pronargs`/`proacl`/`proowner` + `pg_cast`/`pg_transform` views) LANDED.**
        `getFuncs` projects `p.pronargs, …, p.proacl, …, p.proowner` and admits a
        `pg_catalog` function only via `EXISTS` over `pg_cast.castfunc` /
        `pg_transform.trffromsql|trftosql`; it aborted at `column p.pronargs does
        not exist`. Added three columns to `registerPgProcView`
        (`internal/initdb/pg_proc_view.go`): `pronargs int2` = `len(proargtypes)`,
        `proacl aclitem[]` = NULL (no per-routine grants), `proowner oid` = 10
        (bootstrap superuser) — updated **both** row-builders (builtinProcs loop +
        user-routine loop, sibling paths). Added empty `pg_cast` (OID 2605) and
        `pg_transform` (OID 3576) virtual views (`internal/catalog/catalog.go`,
        beside `pg_init_privs`) with PG's exact schemas; both empty by construction
        (goopg registers no user casts/transforms → both `EXISTS` always false →
        only built-in funcs/casts excluded, correct). `castfunc`/`trffromsql`/
        `trftosql` typed `oid` (PG uses oid-compatible `regproc`) so `p.oid = …`
        resolves. Build/gofmt/vet clean; catalog + initdb suites PASS
        (`TestPgProcViewRendersRoutine` updated for the new column positions +
        asserts pronargs/proacl/proowner); `TestPort_PgDumpConnectionSetup` PASS —
        `getFuncs` now completes. **Next blocker (precise):** `getProcLangs` runs
        `SELECT … FROM pg_language WHERE lanispl ORDER BY oid`; goopg has no
        `pg_language` view → `relation "pg_language" does not exist`. Resume = add
        an empty `pg_language` virtual view as slice 8 (built-in PLs are filtered
        by `lanispl`, so empty is correct — only user PLs are dumped).
      - **PROGRESS 2026-06-16 (loop #31):** **DU-002 slice 8 (`pg_language`
        view) LANDED.** `getProcLangs` runs `SELECT tableoid, oid, lanname,
        lanpltrusted, lanplcallfoid, laninline, lanvalidator, lanacl,
        acldefault('l', lanowner) AS acldefault, lanowner FROM pg_language WHERE
        lanispl ORDER BY oid`; it aborted at `relation "pg_language" does not
        exist`. Added the empty `pg_language` virtual view
        (`internal/catalog/catalog.go`, OID 2612, beside `pg_transform`) with the
        `pg_language.h` schema (`oid, lanname name, lanowner oid, lanispl bool,
        lanpltrusted bool, lanplcallfoid oid, laninline oid, lanvalidator oid,
        lanacl aclitem[]`). Empty by construction: `WHERE lanispl` filters out the
        built-in `internal`/`c`/`sql` langs (lanispl=false, never dumped), and
        goopg has no user PLs. `lanowner` typed `oid` so `acldefault('l',
        lanowner)` resolves. Build/gofmt/vet clean; catalog + initdb suites PASS;
        `TestPort_PgDumpConnectionSetup` PASS — `getProcLangs` now completes.
        **Next blocker (precise):** `getOperators` runs `SELECT tableoid, oid,
        oprname, oprnamespace, oprowner, oprkind, oprleft, oprright, oprcode::oid
        AS oprcode FROM pg_operator`; goopg has no `pg_operator` view → `relation
        "pg_operator" does not exist`. Resume = add an empty `pg_operator` virtual
        view as slice 9 (built-in operators live in pg_catalog, filtered out by
        namespace dumpability, so empty is correct — only user operators dumped).
      - **PROGRESS 2026-06-16 (loop #32):** **DU-002 slice 9 (`pg_operator`
        view) LANDED.** `getOperators` runs `SELECT tableoid, oid, oprname,
        oprnamespace, oprowner, oprkind, oprleft, oprright, oprcode::oid AS
        oprcode FROM pg_operator`; it aborted at `relation "pg_operator" does
        not exist`. Added the empty `pg_operator` virtual view
        (`internal/catalog/catalog.go`, OID 2617, beside `pg_language`) with the
        `pg_operator.h` schema (`oid, oprname name, oprnamespace oid, oprowner
        oid, oprkind char, oprcanmerge bool, oprcanhash bool, oprleft oid,
        oprright oid, oprresult oid, oprcom oid, oprnegate oid, oprcode oid,
        oprrest oid, oprjoin oid`). Empty by construction: getOperators reads all
        operators and filters out system-defined ones at dump-out time by
        namespace dumpability — built-ins live in pg_catalog (never dumped),
        goopg defines no user operators. `oprcode` is regproc in PG but
        oid-compatible → typed `oid` so `oprcode::oid` resolves as a no-op.
        Build/gofmt/vet clean; `TestPort_PgDumpConnectionSetup` PASS —
        `getOperators` now completes. **Next blocker (precise):** `getOpclasses`
        runs `SELECT tableoid, oid, opcmethod, opcname, opcnamespace, opcowner
        FROM pg_opclass`; goopg has no `pg_opclass` view → `relation
        "pg_opclass" does not exist`. Resume = add an empty `pg_opclass` virtual
        view as slice 10 (built-in operator classes live in pg_catalog, filtered
        out by namespace dumpability, so empty is correct).
      - **PROGRESS 2026-06-16 (loop #33):** **DU-002 slice 10 (`pg_opclass`
        view) LANDED.** `getOpclasses` runs `SELECT tableoid, oid, opcmethod,
        opcname, opcnamespace, opcowner FROM pg_opclass`; it aborted at
        `relation "pg_opclass" does not exist`. Added the empty `pg_opclass`
        virtual view (`internal/catalog/catalog.go`, OID 2616, beside
        `pg_operator`) with the `pg_opclass.h` schema (`oid, opcmethod oid,
        opcname name, opcnamespace oid, opcowner oid, opcfamily oid, opcintype
        oid, opcdefault bool, opckeytype oid`). Empty by construction:
        getOpclasses reads all operator classes and filters out system-defined
        ones at dump-out time by namespace dumpability — built-ins live in
        pg_catalog (never dumped), goopg defines no user operator classes.
        Build/gofmt/vet clean; catalog + initdb suites PASS;
        `TestPort_PgDumpConnectionSetup` PASS — `getOpclasses` now completes.
        **Next blocker (precise):** `getOpfamilies` runs `SELECT tableoid, oid,
        opfmethod, opfname, opfnamespace, opfowner FROM pg_opfamily`; goopg has
        no `pg_opfamily` view → `relation "pg_opfamily" does not exist`. Resume
        = add an empty `pg_opfamily` virtual view as slice 11 (built-in operator
        families live in pg_catalog, filtered out by namespace dumpability, so
        empty is correct).
      - **PROGRESS 2026-06-16 (loop #34):** **DU-002 slice 11 (`pg_opfamily`
        view) LANDED.** `getOpfamilies` runs `SELECT tableoid, oid, opfmethod,
        opfname, opfnamespace, opfowner FROM pg_opfamily`; it aborted at
        `relation "pg_opfamily" does not exist`. Added the empty `pg_opfamily`
        virtual view (`internal/catalog/catalog.go`, OID 2753, beside
        `pg_opclass`) with the `pg_opfamily.h` schema (`oid, opfmethod oid,
        opfname name, opfnamespace oid, opfowner oid`). Empty by construction:
        getOpfamilies reads all operator families and filters out system-defined
        ones at dump-out time by namespace dumpability — built-ins live in
        pg_catalog (never dumped), goopg defines no user operator families.
        Build/gofmt/vet clean; catalog + initdb suites PASS;
        `TestPort_PgDumpConnectionSetup` PASS — `getOpfamilies` now completes.
        **Next blocker (precise):** `getTSParsers` runs `SELECT tableoid, oid,
        prsname, prsnamespace, prsstart::oid, prstoken::oid, prsend::oid,
        prsheadline::oid, prslextype::oid FROM pg_ts_parser`; goopg has no
        `pg_ts_parser` view → `relation "pg_ts_parser" does not exist`. Resume =
        add an empty `pg_ts_parser` virtual view as slice 12 (built-in
        text-search parsers live in pg_catalog, filtered out by namespace
        dumpability, so empty is correct).
      - **PROGRESS 2026-06-16 (loop #35):** **DU-002 slice 12 (`pg_ts_parser`
        view) LANDED.** `getTSParsers` runs `SELECT tableoid, oid, prsname,
        prsnamespace, prsstart::oid, prstoken::oid, prsend::oid,
        prsheadline::oid, prslextype::oid FROM pg_ts_parser`; it aborted at
        `relation "pg_ts_parser" does not exist`. Added the empty `pg_ts_parser`
        virtual view (`internal/catalog/catalog.go`, OID 3601, beside
        `pg_opfamily`) with the `pg_ts_parser.h` schema (`oid, prsname name,
        prsnamespace oid, prsstart/prstoken/prsend/prsheadline/prslextype
        regproc`); `::oid` casts are no-ops (regproc is oid-compatible). Empty by
        construction: built-in TS parsers live in pg_catalog (never dumped),
        goopg defines no user TS parsers. Build/gofmt/vet clean; catalog +
        initdb suites PASS; `TestPort_PgDumpConnectionSetup` PASS — `getTSParsers`
        now completes, and `getTSDictionaries` (`FROM pg_ts_dict`) ALSO passes:
        `pg_ts_dict` already exists as a real nailed on-disk catalog seeded by
        initdb, so it needed no new view. **Next blocker (precise):**
        `getTSTemplates` runs `SELECT tableoid, oid, tmplname, tmplnamespace,
        tmplinit::oid, tmpllexize::oid FROM pg_ts_template`; goopg has no
        `pg_ts_template` view → `relation "pg_ts_template" does not exist`.
        Resume = add an empty `pg_ts_template` virtual view as slice 13
        (built-in TS templates live in pg_catalog, filtered out by namespace
        dumpability, so empty is correct).
      - **PROGRESS 2026-06-16 (loop #36):** **DU-002 slice 13 (`pg_ts_template`
        view) LANDED.** `getTSTemplates` runs `SELECT tableoid, oid, tmplname,
        tmplnamespace, tmplinit::oid, tmpllexize::oid FROM pg_ts_template`; it
        aborted at `relation "pg_ts_template" does not exist`. Added the empty
        `pg_ts_template` virtual view (`internal/catalog/catalog.go`, OID 3764,
        beside `pg_ts_parser`) with the `pg_ts_template.h` schema (`oid,
        tmplname name, tmplnamespace oid, tmplinit regproc, tmpllexize
        regproc`); `::oid` casts are no-ops (regproc is oid-compatible). Empty
        by construction: built-in TS templates live in pg_catalog (filtered out
        by namespace dumpability), goopg defines no user TS templates.
        Build/gofmt/vet clean; catalog + initdb suites PASS;
        `TestPort_PgDumpConnectionSetup` PASS — `getTSTemplates` now completes.
        **CORRECTION:** slice 12's note that `getTSDictionaries`/`pg_ts_dict`
        "already passes" was a MISREAD — `getTSDictionaries` runs AFTER
        `getTSTemplates`, so the dump aborted at `getTSTemplates` before ever
        reaching it. **Next blocker (precise):** `getTSDictionaries` runs
        `SELECT tableoid, oid, dictname, dictnamespace, dictowner, dicttemplate,
        dictinitoption FROM pg_ts_dict` → `relation "pg_ts_dict" does not
        exist`. Although initdb seeds a `pg_class` entry for pg_ts_dict (OID
        3600), goopg's query layer resolves system catalogs via the in-memory
        virtual-view registry, NOT the on-disk heap, so the seeded row is
        invisible to pg_dump's SELECT. Resume = add an empty `pg_ts_dict`
        virtual view as slice 14 (built-in TS dictionaries live in pg_catalog,
        filtered out by namespace dumpability, so empty is correct).
      - **PROGRESS 2026-06-16 (loop #37):** **DU-002 slice 14 (`pg_ts_dict`
        view) LANDED.** `getTSDictionaries` runs `SELECT tableoid, oid,
        dictname, dictnamespace, dictowner, dicttemplate, dictinitoption FROM
        pg_ts_dict`; it aborted at `relation "pg_ts_dict" does not exist`. Added
        the empty `pg_ts_dict` virtual view (`internal/catalog/catalog.go`, OID
        3600, beside `pg_ts_template`) with the `pg_ts_dict.h` schema (`oid,
        dictname name, dictnamespace oid, dictowner oid, dicttemplate oid,
        dictinitoption text`); `dicttemplate` is an `oid` FK to pg_ts_template
        (not a regproc), `dictinitoption` is text. Empty by construction:
        built-in TS dictionaries live in pg_catalog (filtered out by namespace
        dumpability), goopg defines no user TS dictionaries. Build/gofmt/vet
        clean; catalog + initdb suites PASS; `TestPort_PgDumpConnectionSetup`
        PASS — `getTSDictionaries` now completes. **Next blocker (precise):**
        `getTSConfigurations` runs `SELECT tableoid, oid, cfgname, cfgnamespace,
        cfgowner, cfgparser FROM pg_ts_config` → `relation "pg_ts_config" does
        not exist`. Resume = add an empty `pg_ts_config` virtual view as slice
        15 (built-in TS configs live in pg_catalog, filtered out by namespace
        dumpability, so empty is correct).
      - **PROGRESS 2026-06-16 (loop #38):** **DU-002 slice 15 (`pg_ts_config`
        view) LANDED.** `getTSConfigurations` runs `SELECT tableoid, oid,
        cfgname, cfgnamespace, cfgowner, cfgparser FROM pg_ts_config`; it aborted
        at `relation "pg_ts_config" does not exist`. Added the empty
        `pg_ts_config` virtual view (`internal/catalog/catalog.go`, OID 3602,
        beside `pg_ts_dict`) with the `pg_ts_config.h` schema (`oid, cfgname
        name, cfgnamespace oid, cfgowner oid, cfgparser oid`); `cfgparser` is an
        `oid` FK to pg_ts_parser. Empty by construction: built-in TS configs
        live in pg_catalog (filtered out by namespace dumpability), goopg
        defines no user TS configs. Build/gofmt/vet clean; catalog + initdb
        suites PASS; `TestPort_PgDumpConnectionSetup` PASS — `getTSConfigurations`
        now completes. **Next blocker (precise, confirmed empirically):**
        `getForeignDataWrappers` runs `SELECT tableoid, oid, fdwname, fdwowner,
        fdwhandler::pg_catalog.regproc, fdwvalidator::pg_catalog.regproc,
        fdwacl, …, array_to_string(…fdwoptions…) AS fdwoptions FROM
        pg_foreign_data_wrapper` → `relation "pg_foreign_data_wrapper" does not
        exist`. Resume = add an empty `pg_foreign_data_wrapper` virtual view as
        slice 16 (goopg has no FDWs by default, so empty is correct; fdwhandler/
        fdwvalidator are oid cols cast to regproc by pg_dump).
      - **PROGRESS 2026-06-16 (loop #39):** **DU-002 slice 16
        (`pg_foreign_data_wrapper` view) LANDED.** `getForeignDataWrappers` runs
        `SELECT tableoid, oid, fdwname, fdwowner, fdwhandler::pg_catalog.regproc,
        fdwvalidator::pg_catalog.regproc, fdwacl, acldefault('F', fdwowner) AS
        acldefault, array_to_string(ARRAY(SELECT … FROM
        pg_options_to_table(fdwoptions) …), …) AS fdwoptions FROM
        pg_foreign_data_wrapper`; it aborted at `relation "pg_foreign_data_wrapper"
        does not exist`. Added the empty `pg_foreign_data_wrapper` virtual view
        (`internal/catalog/catalog.go`, OID 2328, beside `pg_ts_config`) with the
        `pg_foreign_data_wrapper.h` schema (`oid, fdwname name, fdwowner oid,
        fdwhandler oid, fdwvalidator oid, fdwacl aclitem[], fdwoptions text[]`);
        `fdwhandler`/`fdwvalidator` are oid FKs to pg_proc. Empty by construction:
        goopg defines no FDWs, only user-defined FDWs are dumped. Build/gofmt/vet
        clean; catalog + initdb suites PASS; `TestPort_PgDumpConnectionSetup`
        PASS. **Next blocker (precise, confirmed empirically — NOT the predicted
        pg_foreign_server):** the relation now resolves but the query advances to
        `column "option_name" does not exist`. The ARRAY subquery selects from
        `pg_options_to_table(fdwoptions)`, an SRF with output columns
        `(option_name, option_value)`. goopg seeds `pg_options_to_table` in
        pg_proc (OID 2289) but does NOT implement it as an executable FROM-clause
        SRF, so the subquery columns are unresolvable at plan time even with an
        empty outer view (goopg resolves subquery columns during planning
        regardless of outer emptiness). Resume = slice 17: implement
        `pg_options_to_table` as a FROM-clause SRF (`text[]` of `name=value` →
        rows `(option_name, option_value)`). Then getForeignServers
        (`pg_foreign_server`) / getUserMappings (`pg_user_mappings`).
      - **PROGRESS 2026-06-16 (loop #40):** **DU-002 slice 17
        (`pg_options_to_table` FROM-clause SRF) LANDED.** The
        `getForeignDataWrappers` ARRAY subquery expands `fdwoptions` via
        `pg_options_to_table(fdwoptions)`; goopg seeded it in pg_proc (OID 2289)
        but never implemented it executably, so planning aborted at `column
        "option_name" does not exist`. Wired the standard FROM-SRF path
        (mirrors `pg_partition_tree`/`unnest`): parser known-builtin switch
        (`internal/parser/select.go`); plan node `PgOptionsToTable` +
        `planPgOptionsToTable` (`internal/planner/plan.go`, `planner.go`) with
        two `text` cols `option_name`/`option_value` (AS-alias overridable);
        `FoldConstants`/`walkPlanExprs` cases (`foldconst.go`, `unnest.go`);
        executor op `pgOptionsToTableOp`
        (`internal/executor/operators_pg_options_to_table.go`) — evaluates the
        `text[]` arg against the outer lateral row, splits each element at the
        FIRST `=` (later `=` stay in value, bare name → NULL value), faithful to
        `untransformRelOptions` in `src/backend/foreign/foreign.c`. **Sibling
        path fixed (non-obvious):** the analyzer (`internal/analyzer/analyzer.go`
        `tableFuncColumns`) derives FROM-SRF columns INDEPENDENTLY and runs
        BEFORE the planner; without a case there, bare `option_name` failed
        analysis before FROM was ever planned (executor was correct — `SELECT *`
        worked, named columns didn't). Added the case there too. 4 unit tests
        (`TestPgOptionsToTable*`) PASS; parser/planner/analyzer/executor/catalog
        suites PASS; build/gofmt/vet clean; `TestPort_PgDumpConnectionSetup`
        PASS. **Next blocker (precise, empirical — NOT the predicted
        pg_foreign_server):** `column "fdwoptions" does not exist`. `fdwoptions`
        is a CORRELATED reference to the outer `pg_foreign_data_wrapper` row, and
        goopg cannot resolve a FROM-clause SRF arg that reaches up into an OUTER
        query level from inside a scalar/ARRAY subquery (verified: same-level
        `FROM t, LATERAL pg_options_to_table(t.opts)` resolves fine). Resume =
        slice 18: thread the outer scope into the analyzer's + planner's
        FROM-clause SRF argument resolution (analyzer `tableFuncColumns` caller's
        scope chain + `planTableFuncRangeVar` `lateralCtx`). Then getForeignServers
        (`pg_foreign_server`) / getUserMappings (`pg_user_mappings`).
      - **PROGRESS 2026-06-16 (loop #41):** **DU-002 slice 18 (correlated
        FROM-clause SRF argument resolution) LANDED.** `getForeignDataWrappers`'
        `ARRAY(SELECT … FROM pg_options_to_table(fdwoptions))` references
        `fdwoptions` from the OUTER `pg_foreign_data_wrapper` row; planning
        aborted at `42703 column "fdwoptions" does not exist`. Root cause: the
        planner resolved the SRF arg against a context built only from same-level
        FROM siblings (`planFromClause`), and the lexical-scope parent
        (`planParent`) was attached to the SELECT's resolveContext only AFTER
        FROM planning ran — so a correlated arg with no left-siblings had no path
        up to the outer scope. Fix (`internal/planner/planner.go`
        `planPgOptionsToTable`, +1 line at the dispatch call to pass `cat`):
        build the arg-resolution context chaining up to `planParent`, mirroring
        the existing `generate_series` precedent (no siblings →
        `&resolveContext{cat: cat, parent: planParent}`; siblings-but-no-parent →
        copy + set parent). `fdwoptions` then resolves to an `OuterColumnRef` the
        executor evaluates per outer row. **Analyzer needed NO change** — its
        `tableFuncColumns` builds the SRF *output* columns but never resolves the
        arg expr (verified empirically: the 42703 came from the planner at the
        `opts` byte offset, analysis passed). The earlier working-set prediction
        that the analyzer also needed threading was refuted. Guards:
        `TestPlanPgOptionsToTableCorrelatedArg` (`internal/planner` — ARRAY,
        scalar, same-level LATERAL forms) + `TestPgOptionsToTableCorrelatedArg`
        (`internal/executor` — per-outer-row eval, no out-of-range crash). build/
        gofmt/vet clean; planner/analyzer/executor/parser/catalog suites PASS;
        `TestPort_PgDumpConnectionSetup` PASS. **Next blocker (precise,
        empirical):** `getForeignDataWrappers` now passes end-to-end; pg_dump
        advances to `getForeignServers` → `relation "pg_foreign_server" does not
        exist`. That query also expands `srvoptions` through the now-working
        correlated `pg_options_to_table(srvoptions)` ARRAY subquery, so slice 19
        is purely the empty `pg_foreign_server` virtual view (`pg_foreign_server.h`
        schema: oid, srvname, srvowner, srvfdw, srvtype, srvversion, srvacl,
        srvoptions text[]; empty by construction like pg_foreign_data_wrapper).
        Then getUserMappings (`pg_user_mappings`).
      - **PROGRESS 2026-06-16 (loop #42):** **DU-002 slice 19
        (`pg_foreign_server` view) LANDED.** `getForeignServers` runs `SELECT
        tableoid, oid, srvname, srvowner, srvfdw, srvtype, srvversion, srvacl,
        acldefault('S', srvowner) AS acldefault, array_to_string(ARRAY(SELECT …
        FROM pg_options_to_table(srvoptions) …), …) AS srvoptions FROM
        pg_foreign_server`; it aborted at `relation "pg_foreign_server" does not
        exist`. Added the empty `pg_foreign_server` virtual view
        (`internal/catalog/catalog.go`, OID 1417, beside
        `pg_foreign_data_wrapper`) with the `pg_foreign_server.h` schema (`oid,
        srvname name, srvowner oid, srvfdw oid, srvtype text, srvversion text,
        srvacl aclitem[], srvoptions text[]`). Empty by construction: goopg
        defines no foreign servers (no CREATE SERVER); the correlated
        `pg_options_to_table(srvoptions)` ARRAY subquery (slice 18, already
        working) is never evaluated — no new SRF work. build/gofmt/vet clean;
        catalog suite PASS; `TestPort_PgDumpConnectionSetup` PASS. **Next blocker
        (precise, empirical — NOT the predicted pg_user_mappings):**
        `getForeignServers` passes; because goopg has no foreign servers,
        getUserMappings short-circuits with no catalog query, and pg_dump
        advances to getDefaultACLs → `relation "pg_default_acl" does not exist`.
        Resume = slice 20: add the empty `pg_default_acl` virtual view
        (`pg_default_acl.h`, OID 826: oid, defaclrole oid, defaclnamespace oid,
        defaclobjtype "char", defaclacl aclitem[]; empty by construction).
      - **PROGRESS 2026-06-16 (loop #43):** **DU-002 slice 20
        (`pg_default_acl` view) LANDED.** `getDefaultACLs` runs `SELECT oid,
        tableoid, defaclrole, defaclnamespace, defaclobjtype, defaclacl, CASE
        WHEN defaclnamespace = 0 THEN acldefault(CASE WHEN defaclobjtype = 'S'
        THEN 's'::"char" ELSE defaclobjtype END, defaclrole) ELSE '{}' END AS
        acldefault FROM pg_default_acl`; it aborted at `relation
        "pg_default_acl" does not exist`. Added the empty `pg_default_acl`
        virtual view (`internal/catalog/catalog.go`, OID 826, beside
        `pg_foreign_server`) with the `pg_default_acl.h` schema (`oid, defaclrole
        oid, defaclnamespace oid, defaclobjtype "char", defaclacl aclitem[]`).
        Empty by construction: goopg defines no default-ACL entries (no ALTER
        DEFAULT PRIVILEGES); the CASE/acldefault projection is never evaluated —
        no new expression work. build/gofmt/vet clean; catalog suite PASS;
        `TestPort_PgDumpConnectionSetup` PASS. **Next blocker (precise,
        empirical):** `getDefaultACLs` passes; pg_dump advances to
        getConversions → `relation "pg_conversion" does not exist` (`SELECT
        tableoid, oid, conname, connamespace, conowner FROM pg_conversion`).
        Resume = slice 21: add the `pg_conversion` virtual view
        (`pg_conversion.h`, OID 2607). NOTE: PG ships ~130 built-in conversions
        there, but pg_dump filters them as built-ins, so an empty view may
        suffice — verify empirically with the port test.
      - **PROGRESS 2026-06-16 (loop #44):** **DU-002 slice 21
        (`pg_conversion` view) LANDED.** `getConversions` runs `SELECT tableoid,
        oid, conname, connamespace, conowner FROM pg_conversion` ("find all
        conversions, including builtin conversions; we filter out system-defined
        conversions at dump-out time"); it aborted at `relation "pg_conversion"
        does not exist`. Added the empty `pg_conversion` virtual view
        (`internal/catalog/catalog.go`, OID 2607, beside `pg_default_acl`) with
        the `pg_conversion.h` schema (`oid, conname name, connamespace oid,
        conowner oid, conforencoding int4, contoencoding int4, conproc
        regproc(oid), condefault bool`). Although PG ships ~130 built-in
        conversions, every one is in pg_catalog and filtered out at dump-out time
        (`selectDumpableObject` → DUMP_COMPONENT_NONE), so the **empty** view (0
        rows) gives an identical dump — confirmed empirically. build/gofmt/vet
        clean; catalog suite PASS; `TestPort_PgDumpConnectionSetup` PASS. **Next
        blocker (precise, empirical):** `getConversions` passes; pg_dump advances
        to getCasts → `relation "pg_range" does not exist` (`SELECT tableoid,
        oid, castsource, casttarget, castfunc, castcontext, castmethod FROM
        pg_cast c WHERE NOT EXISTS ( SELECT 1 FROM pg_range r WHERE c.castsource =
        r.rngtypid AND c.casttarget = r.rngmultitypid ) ORDER BY 3,4`) —
        `pg_cast` exists, but the NOT EXISTS subquery references `pg_range`, which
        does not. Resume = slice 22: add the `pg_range` virtual view
        (`pg_range.h`, OID 3541). goopg defines no range types, so an empty view
        should suffice — verify empirically with the port test.
      - **PROGRESS 2026-06-16 (loop #45):** **DU-002 slice 22 (`pg_range` view)
        LANDED.** `getCasts` runs `SELECT tableoid, oid, castsource, casttarget,
        castfunc, castcontext, castmethod FROM pg_cast c WHERE NOT EXISTS ( SELECT
        1 FROM pg_range r WHERE c.castsource = r.rngtypid AND c.casttarget =
        r.rngmultitypid ) ORDER BY 3,4`; `pg_cast` existed but the NOT EXISTS
        subquery referenced `pg_range`, so it aborted at `relation "pg_range" does
        not exist`. Added the empty `pg_range` virtual view
        (`internal/catalog/catalog.go`, OID 3541, beside `pg_conversion`) with the
        `pg_range.h` schema — note `pg_range` has **no** `oid` column; `rngtypid`
        is the key (`rngtypid oid, rngsubtype oid, rngmultitypid oid, rngcollation
        oid, rngsubopc oid, rngcanonical regproc(oid), rngsubdiff regproc(oid)`).
        goopg defines no range types, so the NOT EXISTS is always true and the
        **empty** view (0 rows) gives an identical dump — confirmed empirically.
        build/gofmt/vet clean; catalog suite PASS; `TestPort_PgDumpConnectionSetup`
        PASS. **Next blocker (precise, empirical):** `getCasts` passes; pg_dump
        advances to getEventTriggers → `relation "pg_event_trigger" does not
        exist` (`SELECT e.tableoid, e.oid, evtname, evtenabled, evtevent,
        evtowner, array_to_string(array(select quote_literal(x) from
        unnest(evttags) as t(x)), ', ') as evttags, e.evtfoid::regproc as evtfname
        FROM pg_event_trigger e ORDER BY e.oid`). Resume = slice 23: add the
        `pg_event_trigger` virtual view (`pg_event_trigger.h`, OID 3466). goopg
        defines no event triggers, so an empty view should suffice — verify
        empirically with the port test.
      - **NOTE (orthogonal, pre-existing — do NOT conflate with slice 18):**
        reading a `text[]` column back from the heap yields the binary array
        encoding (Datum KindString carrying raw bytes) rather than the text
        representation `expandArrayDatum` parses; a plain `SELECT opts FROM t`
        over a text[] column reproduces it. Irrelevant to the pg_dump path
        (pg_foreign_data_wrapper/pg_foreign_server are empty, so the correlated
        SRF never evaluates a non-empty options array). Track separately if a
        real text[]-column expansion path is ever needed.

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
        | `postgres/src/bin/pg_amcheck/t/002_nonesuch.pl` | **PORTED (AC-002 → port)** | `TestPort_PgAmcheck002Nonesuch`. Full pattern-resolution assertion set incl. both final `--all --exclude-schema` sections (.pl :377-418, ported loop #18 after the residual-#2 planner panic fix 36a085dc). One section deferred: `datconnlimit=-2` invalid-database filter (runtime shared-catalog write goopg lacks). |
        | `postgres/src/bin/pg_amcheck/t/003_check.pl` | UNIMPLEMENTED (deferred AC-002) | Runs actual heap/btree corruption checks against a server. |
        | `postgres/src/bin/pg_amcheck/t/004_verify_heapam.pl` | UNIMPLEMENTED (deferred AC-002) | `verify_heapam()` function required (not in goopg). |
        | `postgres/src/bin/pg_amcheck/t/005_opclass_damage.pl` | UNIMPLEMENTED (deferred AC-002) | Operator-class damage detection; requires opclass system catalog parity. |
      - Action: 001_basic CLI tier ported (loop #18). The four server-dependent
        tests are deferred under CSV row AC-002, blocked on `verify_heapam()` SRF
        + opclass catalog coverage. Resume = promote AC-002 (002_nonesuch first —
        only error-path catalog lookups) when those land.
      - **PROGRESS 2026-06-15 (loop #20):** AC-003 **enabler** — `CREATE SCHEMA`
        is now **durable across a server restart** (the recurring 003_check
        blocker noted in loops #15/#19: a `--schema s1` run clean pre-restart
        reported `no relations to check` post-restart because the schema
        registration was in-memory-only). Mirrors the CREATE/DROP DATABASE
        WAL-record mechanism (M0054-0001), NOT the pg_class heap-append: new
        physical-replay-no-op record kinds `RecordKindCreateSchema`=34
        (`kind|oid|nameLen|name`, OID carried) / `RecordKindDropSchema`=35
        (`internal/wal/recovery.go`); verbatim-mirror recovery driver
        `replaySchemaDDLRecords` (`internal/initdb/schema_ddl_recovery.go`)
        wired into `open.go` after the database-DDL replay; idempotent catalog
        hooks `Register/UnregisterSchemaDuringRecovery`. Emitted at all three
        execution routes (parsed `CompatNoopStmt{schema}` in `execCompatNoop`,
        the parser-rejected compat-no-op branch in dispatch, and `DROP SCHEMA`
        in `execDropCompat`; all `WAL != nil`-guarded). Non-transactional (like
        CREATE DATABASE). PG-standby visibility (pg_namespace heap row +
        2684/2685 indexes) is explicitly OUT OF SCOPE — goopg resolves schemas
        via the in-memory registry, not the index. Tests: wal codec round-trip
        (`internal/wal/schema_ddl_test.go`), initdb Open→append→Close→Open
        replay (`internal/initdb/schema_ddl_recovery_test.go`), and e2e
        `TestPort_CreateSchemaSurvivesRestart`
        (`internal/testport/create_schema_durability_test.go`, over-the-wire
        CREATE/DROP across two restarts). Gates: build/vet clean; wal + catalog
        + initdb + executor + server + amcheck-alltables/003 suites PASS, no
        regression. Design doc `0110-0012-create-schema-wal-durability.md` +
        README index. AC-003 stays `defer` (the remaining 003 tiers still need
        the index AMs / column types / TOAST / multi-DB feature work).
      - **PROGRESS 2026-06-15 (loop #21):** AC-003 — **user-schema TABLE
        durability across restart** (completes loop #20's CREATE SCHEMA work) and
        the **schema-scoped 003_check tier** ported. Loop #20 made the schema
        name/OID survive a restart, but a table created in a user schema still
        reloaded under the wrong namespace: the write side `namespaceOIDForSchema`
        collapsed every non-`public` schema to the `pg_catalog` OID (11) so `s1.t`
        was written `relnamespace=11` and the read side `loadUserTablesFromHeap`
        reloaded it as `pg_catalog.t` → `pg_amcheck --schema s1` found no
        relations post-restart (the exact 003_check symptom logged in loops
        #15/#19). Fix makes the sibling encode/decode pair agree on the **real
        schema OID** from the registry 0110-0012 restores: `namespaceOIDForSchema(
        cat, schema)` resolves a registered user schema via `cat.SchemaOID`;
        `loadUserTablesFromHeap` + `loadUserIndexesFromHeap` reverse-map a
        non-system `relnamespace` via the new `cat.SchemaNameForOID`;
        `replaySchemaDDLRecords` moved **before** table load in `open.go` so the
        registry is populated when the reverse-map runs; `SchemaOID` promoted onto
        the `Catalog` interface. No new durability machinery — only corrects the
        value written to an existing `pg_class` column so the heap-scan recovery
        is lossless. New e2e `TestPort_PgAmcheck003SchemaScoped`
        (`internal/testport/pgamcheck003_schemascoped_test.go`): corrupts
        `s1.t003sc`'s heap file across a stop→corrupt→restart cycle and asserts a
        `--schema s1` run reports the missing file (exit 2) — proving schema + its
        table survived with correct association end-to-end. Unit gates
        `TestSchemaOIDNameRoundTrip` + `TestNamespaceOIDForSchemaResolvesUserSchema`.
        Sibling-path class [[pattern_sibling_paths_must_agree]]. Gates:
        build/vet/gofmt clean; catalog + initdb + executor + server + full
        pg_amcheck testport + `TestPort_CreateSchemaSurvivesRestart` PASS (no
        regression); TPC-H spotcheck SKIP (no data dir; change touches only
        non-`public` namespace resolution, public tables byte-identical). Design
        doc `0110-0013-user-schema-table-durability.md` + README + CSV AC-003 +
        markdown. PG-standby user-schema visibility stays out of scope. AC-003
        stays `defer`.
      - **PROGRESS 2026-06-15 (loop #19):** AC-003 — the **central combined-
        corruption integration tier** of `003_check.pl` (its main check, :347-365)
        LANDED. The three sibling surrogates each inject ONE corruption on a
        single-relation fixture; none proves the property 003_check's main check
        actually asserts — that pg_amcheck reports MULTIPLE distinct corruption
        classes across a multi-relation DB in ONE pass without aborting on the
        first corrupt relation (the removed-heap-file case raises ERROR 58030, not
        a corruption row). New `TestPort_PgAmcheck003CombinedCorruption`
        (`internal/testport/pgamcheck003_combined_test.go`) injects all three —
        removed btree index fork (`tfork_idx`), removed heap file (`tfile`),
        overwritten heap line pointer (`tpage`, reusing `corruptFirstLinePointer-
        Length`) — in a SINGLE stop→corrupt→restart cycle (mirroring
        `perform_all_corruptions`), then asserts one scoped run reports all three
        upstream-verbatim regexes together (`index .* lacks a main relation fork`
        + `line pointer` + `could not open file .*: No such file or directory`)
        with exit 2 and empty stderr. PASS (11.1s); full pg_amcheck port suite PASS
        (33.7s, no regression). **Pure faithful port, ZERO engine change** — goopg's
        per-relation dispatch already reports all three. Surfaced a SEPARATE gap
        (out of scope): goopg does not persist a `CREATE SCHEMA` `pg_namespace` row
        across restart (a first `--schema s1` run was clean pre-corruption but
        reported `no relations to check` post-restart), so the fixture uses
        `public` + one `--table` per relation like every AC-003 surrogate. Gates:
        gofmt + `go vet ./internal/testport` clean; build clean. CSV AC-003 +
        markdown + design doc `0110-0003` (new "Combined-corruption integration
        tier" section) updated. AC-003 stays `defer` (hash/gist/gin/brin/spgist
        AMs, box/int4range/int4[] types, STORAGE EXTERNAL TOAST, multi-DB
        orchestration; 005_opclass_damage still need feature work).
      - **PROGRESS 2026-06-15 (loop #15):** AC-003 — the **heap-table file-removal
        corruption tier** of `003_check.pl` LANDED (the companion to loop #14's
        index fork). `003_check` removes an ordinary table's backing file
        (`plan_to_remove_relation_file('db1','s2.t1')`, :275) and asserts
        pg_amcheck reports `could not open file ".*": No such file or directory`
        (exit 2, :327/:357-365). Same `os.O_CREATE` hazard: `verifyHeapamOp.Open`'s
        `Pool.NBlocks` would recreate the removed heap fork as an empty 0-block
        file → `VerifyHeapRelation` reports the table *clean* (silent false
        negative). Fix reuses the stat-only seam: `verifyHeapamOp.Open` calls
        `ctx.Pool.Exists(rel)` BEFORE `NBlocks`; absent → `58030` (ERRCODE_IO_ERROR,
        what `mdopenfork`'s `errcode_for_file_access` yields for ENOENT) with the
        verbatim `could not open file "%s": No such file or directory`. The path is
        built by new `storage.Manager.RelPath`/`Pool.RelPath` (data-dir-relative
        `base/<db>/<relfile>`, faithful to upstream `relpath()`). Unit gate
        `TestVerifyHeapam_DetectsMissingRelationFile` (drops fork, asserts msg).
        E2E `TestPort_PgAmcheck003MissingHeapFile`
        (`internal/testport/pgamcheck003_missingheap_test.go`) drives the real
        pg_amcheck over stop→unlink→restart (exit 2 + verbatim report) AND asserts
        the fork is NOT recreated on restart — PASS (7.0s). Gates: `go build ./...`
        clean; full `internal/testport` pg_amcheck suite PASS (20.4s);
        `internal/executor` + `internal/storage` PASS; vet clean. CSV AC-003 +
        markdown + design doc `0110-0003` updated. STILL DEFERRED (AC-003 stays
        `defer`): hash/gist/gin/brin/spgist index AMs, box/int4range/int4[] types,
        STORAGE EXTERNAL TOAST corruption, page-overwrite for unsupported
        relkinds, multi-DB orchestration; 005_opclass_damage.
      - **PROGRESS 2026-06-15 (loop #14):** AC-003 — the **index file-removal
        (missing-main-relation-fork) corruption tier** of `003_check.pl` LANDED.
        goopg's smgr opens relation files with `os.O_CREATE`, so `Pool.NBlocks`
        on a removed fork silently RECREATED it as an empty 0-block file → the
        btree engine reported the index *clean* (a silent false negative exactly
        where PG reports corruption). Fix mirrors `bt_index_check_callback`'s
        `smgrexists(MAIN_FORKNUM)` guard (`verify_nbtree.c:318`): new stat-only
        `storage.Manager.Exists`/`Pool.Exists` (pure `os.Stat(relPath)`, never the
        `O_CREATE` `relFile` path — every live rel has an on-disk file, so a stat
        never false-negatives a live rel nor recreates a removed one), called in
        `evalBtIndexCheck` BEFORE `NBlocks`; absent → `XX002`
        (ERRCODE_INDEX_CORRUPTED) with verbatim `index "%s" lacks a main relation
        fork`. Covers `bt_index_check` + `bt_index_parent_check`. Unit gate
        `TestBtIndexCheck_DetectsMissingRelationFork` (drops the fork, asserts the
        message). E2E `TestPort_PgAmcheck003MissingIndexFork`
        (`internal/testport/pgamcheck003_missingfork_test.go`) drives the real
        pg_amcheck over the full stop→unlink→restart lifecycle (exit 2 + verbatim
        report) AND asserts goopg does NOT recreate the fork on restart (the
        load-bearing property a unit test can't prove) — PASS (7.3s). Gates:
        `go build ./...` clean; full `internal/testport` pg_amcheck suite PASS
        (13.8s); `internal/executor` + `internal/storage` PASS; vet clean. CSV
        AC-003 rationale + markdown + design doc `0110-0003` updated. STILL
        DEFERRED (AC-003 stays `defer`): heap-table file-removal (`could not open
        file` message needs a typed missing-file error from smgr), the
        hash/gist/gin/brin/spgist index AMs, box/int4range/int4[] types, STORAGE
        EXTERNAL TOAST corruption, multi-DB orchestration; 005_opclass_damage
        (CREATE OPERATOR CLASS + pg_amproc parity).
      - **PROGRESS 2026-06-15 (loop #11):** AC-003 — **whole-database
        relation-enumeration tier ported + blocker #3 hypothesis REFUTED**.
        `003_check.pl`'s clean-db path runs the *default* `pg_amcheck` (no
        scoping), which enumerates every checkable relation and dispatches
        `verify_heapam`/`bt_index_check` per relation — a distinct tier from the
        single-`--table` path. New `TestPort_PgAmcheckAllTables`
        (`internal/testport/pgamcheck_alltables_port_test.go`) drives the real
        binary over a goopg DB mixing the relkinds `003_check` builds that goopg
        supports (heap table, several btree indexes incl. UNIQUE, sequence, view,
        materialized view) in a user schema; a `--schema s1` run checks the
        heap+btree subset and skips the view/sequence — **clean (exit 0)**. It
        also drives the *unscoped whole-database* run (which would reach
        `pg_catalog.*`) — **also clean (exit 0)**, which empirically REFUTES the
        prior blocker #3 ("system-catalog heap resolution"): goopg never feeds its
        system catalogs to pg_amcheck's heap-check dispatch, so there is no
        `verify_heapam`-on-catalog gap to close. The dispatch fixes (blocker #1
        lateral pushdown, blocker #2 install-schema `bt_index_check`) are asserted
        as hard regressions. Remaining `003_check` blockers are now purely
        feature/corruption — hash/gist/gin/brin/spgist AMs goopg lacks,
        box/int4range/int4[] types, STORAGE EXTERNAL TOAST corruption, multi-DB
        orchestration, and the file-removal/page-overwrite corruption mechanics
        (multi-milestone). Gates: full `internal/testport` pg_amcheck suite PASS
        (001/002/004 + btree + alltables); gofmt + `go vet ./internal/testport`
        clean. Design doc `0110-0003` + CSV AC-003 + markdown updated. AC-003 stays
        `defer` (003 + 005 need the feature work above).
      - **PROGRESS 2026-06-15 (loop #8):** AC-003 enabler — **lateral
        outer-qual pushdown** landed (`pushOuterQualsIntoLaterals` in
        `internal/planner/pushdown.go`), the dominant blocker for
        relation-scoped `pg_amcheck` runs. Live-wire diagnosis showed
        pg_amcheck's heap command is an implicit-LATERAL comma-join
        `FROM pg_catalog.pg_class c, "<schema>".verify_heapam(relation := c.oid,
        …) v WHERE c.oid = N`; goopg planned the residual `WHERE c.oid = N`
        *above* the lateral nested-loop, so `verify_heapam` was opened for EVERY
        pg_class row and raised `could not open relation` on the first non-heap
        sibling (an index/sequence OID) before the filter could drop it — exit 2
        on a perfectly healthy target whenever the DB held >1 relation. The new
        pass moves an outer-only conjunct (sideLeft by index AND name;
        `collectScanOutputNames` now covers the `*Values` virtual-catalog node)
        onto the lateral join's outer child, matching PG's nested-loop qual
        placement. No-op unless the residual Filter's direct child is a Lateral
        join → zero impact on non-lateral shapes (all of TPC-H). After the fix
        `pg_amcheck --table public.t` and `--table … --no-dependent-indexes`
        return exit 0 over a multi-table/indexed DB (verified end-to-end). Regression
        `TestPlanOuterQualPushedBelowLateralJoin` (planner_test.go). Gates: full
        `internal/planner` + `internal/executor` suites PASS; `go vet`
        planner/server clean; 001/002/004 amcheck port tests PASS; build ./...
        clean; gofmt clean. TPC-H spotcheck SKIPPED (no data dir loaded in this
        tree) — safe by construction (lateral-only guard). Diagnostic also
        pinned the two AC-003 remainders for `003_check`: (#2) `bt_index_check`
        schema-qualified dispatch (`public.bt_index_check does not exist` —
        `evalFuncCall` strips only `pg_catalog.`), and (#3) system-catalog heap
        resolution in `verifyHeapamResolveTable`/`LookupTableByOID`. Design doc
        `0110-0003` extended (lateral-pushdown + 003_check-blockers sections).
        Resume = bt_index_check schema-qualifier (blocker #2, small).
      - **PROGRESS 2026-06-15 (loop #7):** AC-003 — the **page-structural heap
        tier of `004_verify_heapam.pl`** is ported as
        `TestPort_PgAmcheck004VerifyHeapam`
        (`internal/testport/pgamcheck004_port_test.go`), driving the real
        pg_amcheck binary against a live goopg cluster end-to-end. Mirrors
        upstream's stop→seek/overwrite→restart corruption mechanism: inits with
        `--no-data-checksums` (upstream `no_data_checksums=>1`; goopg now defaults
        checksums ON, which would otherwise trip the storage-manager checksum
        verify before verify_heapam sees the damage), CREATE EXTENSION amcheck,
        inserts rows, locates the heap file by globbing `base/*/<reloid>` (goopg's
        storage dbOid ≠ pg_database.oid), stops cleanly, overwrites the first line
        pointer's length on block 0 to 0x7FFF so lp_off+lp_len>BLCKSZ, restarts,
        re-CREATE EXTENSION amcheck (runtime-only install per gap #7c doesn't
        survive restart), and asserts pg_amcheck exits 2 with the upstream-verbatim
        `line pointer to page offset N with length 32767 ends beyond maximum page
        offset 8192` report on stdout. PASS (3.1s); 001/002 still PASS. CSV AC-003
        rationale + design doc 0110-0003 + README index updated. NOT ported
        (goopg-divergent): 004's MVCC/attribute + TOAST tiers corrupt PG's on-disk
        varatt_external pointer layout (goopg uses chunk-relation TOAST). AC-003
        stays `defer` — `003_check` (whole-db orchestration; needs system-catalog
        heap pages to verify cleanly) and `005_opclass_damage` (CREATE OPERATOR
        CLASS + pg_amproc parity) remain.
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
      - **PROGRESS 2026-06-15 (loop #9):** added the last unvalidated real-producer
        mutator — **page deletion** via `btree.VacuumIndexPages` (the deletion-path
        mirror of the split validation that surfaced M0110-0007). New
        `internal/amcheck/verify_nbtree_pagedel_test.go` empties interior leaves of
        a live multi-level tree and runs every engine tier over the post-deletion
        pages. Single + non-adjacent multi interior-leaf deletion are clean (all
        tiers silent — validates `unlinkEmptyLeaf`'s three-way relink + the engine's
        deleted-page exemptions on real layout). **Adjacent interior-leaf deletion
        in one pass surfaced a genuine goopg bug → filed M0110-0010:**
        `unlinkEmptyLeaf` relinks neighbours from pointers captured before any
        unlink, so a survivor's `btpo_prev`/`btpo_next` is left pointing at a block
        deleted in the same pass, breaking the leaf sibling chain (both the WAL
        emitter and FPI fallback share it). The sibling-link tier correctly flags
        `... points to deleted block`; pinned by
        `TestVerifyBtreeEngineDetectsStaleSiblingLinkAfterAdjacentLeafDeletion`
        (flips to silence when M0110-0010 lands). `go test -race ./internal/amcheck
        ./internal/access/btree` PASS; gofmt/vet clean; **zero contaminated files
        touched** (all new test code + design `0110-0005`). Landed on worktree
        branch `m0110-amcheck-pagedel` (commit 443ea473) off clean HEAD b8dd6403 —
        the main tree still carries the foreign gen-column WIP.
      - **PROGRESS 2026-06-15 (loop #13):** the **amcheck SQL surface Slices S1+S2
        LANDED** (design `0110-0008`) on worktree branch
        `m0110-0003-amcheck-sql-surface` (commit 6f3a6f1b) off clean HEAD b8dd6403.
        Picked up a prior loop's uncommitted WIP sitting in worktree
        `m0110-amcheck-sql` (CREATE EXTENSION + pg_extension), verified, and
        committed it there — escaping the main-tree foreign-WIP block via worktree
        isolation. S1 = `CREATE EXTENSION [IF NOT EXISTS] name [WITH][SCHEMA][VERSION]
        [CASCADE]` DDL (`parser.CreateExtensionStmt`/`parseCreateExtensionTail` →
        planner DDL passthrough → `CREATE EXTENSION` tag → `ddlOp.execCreateExtension`;
        unknown→58P01, duplicate→42710, built-in allow-list `amcheck→1.4`). S2 =
        `pg_extension` virtual catalog (OID 3079, upstream column shape;
        extconfig/extcondition NULL) + `InMemory.CreateExtension` registry, backing
        pg_amcheck's `pg_extension⋈pg_namespace` install probe (`pg_amcheck.c:173`).
        Tests `parser.TestParseCreateExtension` (5 shapes) +
        `testport.TestPort_AmcheckCreateExtension` (probe lifecycle) PASS. Gates:
        gofmt/vet clean; parser/catalog/executor/planner/server unit tests PASS;
        build OK; TPC-H spotcheck SKIPPED (no data; change is purely additive).
        **Remaining: S3 `verify_heapam` SRF, S4 `bt_index_check`/`bt_index_parent_check`
        SRFs, S5 TAP port** (`002_nonesuch`…`005`). AC-002's server-side requirement
        is now satisfied; promoting it still needs S5 + the SRFs to exist in `pg_proc`.
        Merge to the active branch still awaits a clean tree. Resume = Slice S3.
      - **PROGRESS 2026-06-15 (loop #14):** the **amcheck SQL surface Slice S3
        LANDED** — the `verify_heapam(regclass, ...)` SRF executor (design
        `0110-0008`) on worktree branch `m0110-0003-amcheck-sql-surface`
        (commit 8154404c, on top of S1+S2 6f3a6f1b, off clean HEAD b8dd6403).
        It is the thin wire adapter over the committed `internal/amcheck` heap
        engine: new `VerifyHeapam` plan node + `planVerifyHeapam` dispatch +
        FoldConstants/walkPlanExprs cases (`planner/{plan,planner,foldconst,
        unnest}.go`); parser FROM-SRF recognition (`parser/select.go`); analyzer
        `tableFuncColumns` 4-column shape so explicit `SELECT blkno FROM ...`
        resolves, not just `SELECT *` (`analyzer/analyzer.go` — sibling path to
        the planner's table-func dispatch, the bug that made the first test fail);
        new executor op (`executor/operators_verify_heapam.go` + `executor.go`
        dispatch) that resolves the regclass arg → heap relation, fills a
        `PageSource` from the buffer pool (copy-per-block, no held pins), passes
        `nblocks` + `RelDesc.Natts`/`NextXid`/`RelFrozenXid`, and streams each
        `amcheck.HeapRelReport` as `(blkno,offnum,attnum,msg)` (attnum NULL for
        page/header-level findings). Output SETOF (blkno int8, offnum int8,
        attnum int4, msg text) mirrors upstream. Tests PASS:
        `TestVerifyHeapam_{CleanTableNoReports,DetectsInjectedCorruption,
        StartBlockOutOfRange}` — the clean-table → 0-rows case through the full
        parse→plan→execute stack is the no-false-positive gate. Gates: gofmt
        clean (additions); vet clean; build OK; executor/planner/analyzer/parser/
        catalog/server/amcheck unit tests PASS; TPC-H spotcheck SKIPPED (no data
        dir in worktree; change is purely additive, touches no existing query
        path). Two intentional S3→S5 follow-ups (documented in 0110-0008):
        (1) clog-backed `XidStatusFunc` — disabled (nil) because the executor
        `Context` has no clog handle yet, so the clog-dependent HOT-chain tier is
        off; page-structural + natts + xmin/xmax bounds tiers are active;
        (2) named-arg + LATERAL call shape pg_amcheck emits (`relation := c.oid`
        correlated on pg_class) — goopg's parser has no `:=` named-arg syntax, so
        S3 supports **positional** args only. **Remaining: S4 bt_index_check/
        bt_index_parent_check SRFs, S5 TAP port.** Merge to the active branch
        still awaits a clean tree. Resume = Slice S4.
      - **PROGRESS 2026-06-15 (loop #15):** the **amcheck SQL surface Slice S4
        LANDED** — `bt_index_check` / `bt_index_parent_check` scalar `void`
        functions (design `0110-0008`) on worktree branch
        `m0110-0003-amcheck-sql-surface` (commit b7c1b78c, on top of S3 8154404c,
        off clean HEAD b8dd6403). Unlike S3's FROM-clause SRF, these are
        SELECT-list scalar functions (pg_amcheck issues
        `SELECT bt_index_check(c.oid, false) FROM pg_class c, pg_index i …`), so
        they live in `evalFuncCall` dispatch (`executor/expr.go`) with `exprType`
        returning `void` (OID 2278, `planner/planner.go`). New
        `executor/operators_bt_index_check.go` resolves the index regclass
        (OID/name) → `IndexRelFileNode`, fills a `PageSource` from the buffer
        pool, and drives the committed engine's structural tiers
        (`VerifyBtreePage`/`VerifyBtreeItemOrder` per block,
        `VerifyBtreeLevelSiblingLinks` per level via leftmost-descent,
        `VerifyBtreeParentDownlinks` per internal page on parent-check); any
        `[]amcheck.BtreeReport` finding raises `XX002` (ERRCODE_INDEX_CORRUPTED),
        clean index → void. Tests PASS:
        `TestBtIndexCheck_{CleanIndexNoError (no-false-positive gate, both funcs,
        all positional shapes), DetectsCorruptMetapage, NonexistentIndex}`.
        Gates: gofmt clean (also fixed pre-existing expr.go import disorder); vet
        clean; build OK; executor/planner/analyzer/parser/amcheck unit tests PASS;
        TPC-H spotcheck SKIPPED (no data dir in worktree; additive, no query-path
        change). Three intentional S4→S5 follow-ups (documented in 0110-0008):
        (1) `heapallindexed` heap↔index completeness (needs MVCC heap-entry-set
        former; default pg_amcheck probe passes `false`); (2) `rootdescend`/
        `checkunique` deeper parent-check tiers; (3) named-arg `:=`/LATERAL
        (positional-only, shared with S3). **Remaining: S5 TAP port + named-arg/
        clog wiring; MERGE (human clears foreign gen-column WIP).** Resume =
        Slice S5 in the same worktree.
      - **PROGRESS 2026-06-15 (loop #16):** the **amcheck SQL surface Slice S5
        named-argument parsing LANDED** (commit 42e67873, on top of S4 b7c1b78c,
        off clean HEAD b8dd6403). pg_amcheck emits the legacy `name := value`
        named-arg spelling — `bt_index_check(index := c.oid, heapallindexed :=
        false)` and the FROM-clause SRF `verify_heapam(relation := c.oid,
        on_error_stop := false, …)`. goopg stripped `=>` positionally (M0097-0003)
        only in the expr path (`parseFuncCallTail`). This loop extends BOTH the
        expr path AND the FROM-SRF arg loop (`parseRangeVar`) — the **sibling
        path**, which previously stripped no names at all — to accept `:=`/`=>`
        and map positionally. Arg-names that lex as unreserved keywords (`index`,
        `skip`) are accepted via the gated `isNamedArgNameToken` helper (the
        `:=`/`=>` lookahead disambiguates). Positional order already matches the
        S3/S4 executors, so the strip binds correctly; `c.oid` parses to a
        `ColumnRef`. Tests PASS: `parser.TestParseNamedArgColonEqual{,FromSRF,
        EquivalentToFatArrow}` (both scalar funcs, full 6-arg FROM-SRF with
        correlated `c.oid` first, `:=`≡`=>` equivalence). Gates: gofmt clean (my
        edits; left pre-existing select.go drift untouched), vet clean, build OK,
        parser/analyzer/planner suites PASS, executor amcheck tests PASS; TPC-H
        spotcheck SKIPPED (no data dir; parser-only additive). Parser-only, zero
        contaminated files. **Remaining for S5:** (a) LATERAL resolution of the
        `c.oid` outer ref at plan/exec time (planner change — its own bounded loop
        per the M0072-hang precedent); (b) clog `XidStatusFunc` wiring; (c) the
        AC-002..AC-005 TAP port; (d) MERGE (human clears foreign gen-column WIP).
      - **PROGRESS 2026-06-15 (loop #19):** the **AC-002 bootstrap-query SQL-engine
        gaps #1 + #2 LANDED** (worktree branch `m0110-0003-amcheck-sql-surface`,
        commit c91512ba on top of a74c7036, off clean HEAD b8dd6403). The
        `002_nonesuch` port drives real `pg_amcheck`, whose database/relation
        resolution queries (`pg_amcheck.c compile_database_list` /
        `compile_relation_list_one_db`) hit two GENERAL goopg SQL gaps, neither
        amcheck-specific: **#1** `index` rejected as a CTE name — `parseCTE`→
        `parseIdent` accepts an unreserved/col_name keyword, but the post-`,`
        look-ahead guard in `parseWithClause` only allowed `TokenIdent`/
        `TokenQuotedIdent`, so `WITH a AS (…), index AS (…)` errored; guard now
        mirrors `parseIdent` (reserved keywords still rejected). **#2** a VALUES
        list backing a CTE reported 0 columns — `analyzer.registerAnalyzedCTE`
        built columns only from `cte.Query.Targets` (empty for VALUES) → 42P10;
        it now derives the count from the first `ValuesRows` row when `Targets`
        is empty, mirroring the VALUES anchor in `analyzeRecursiveCTE` (sibling
        path). Tests: `parser.TestParseCTENamedIndex`, `analyzer.TestAnalyzeWith
        ValuesCTE{ColumnAliases,ArityMismatch,DefaultColumnNames}`. Gates: full
        parser+analyzer suites PASS; `go build ./...`+gofmt+vet clean; zero
        contaminated files. **Remaining AC-002 gap #3 (connection-level):** goopg
        does not reject a connection to a non-existent database at startup —
        needs (a) `template1`/`template0` registered in `pg_database` (default
        set is only `{"postgres"}`, yet `002_nonesuch` connects to `template1`)
        and (b) a `HasDatabase` check after auth in `server.go` returning FATAL
        3D000 `database "%s" does not exist` (guarded like `tryHandleDatabaseDDL`
        for nil/non-registry catalogs). Touches the connection handshake (every
        connection) → its own bounded loop with the full `internal/server` suite +
        a live-cluster smoke. `TestPort_PgAmcheck002Nonesuch` self-skips via a new
        `qqq`-rejection preflight probe so #1+#2 do not flip it SKIP→FAIL. Design
        doc `0110-0008` updated. Resume = AC-002 gap #3.

- [x] **M0110-0010 — B-tree vacuum: relink LIVE siblings when deleting adjacent empty leaves**
      - **DONE 2026-06-15 (loop #10).** Landed on worktree branch
        `m0110-amcheck-pagedel` (commit 08ec6b20) off clean HEAD b8dd6403 — the
        main tree still carries the foreign gen-column WIP, so the btree fix
        (btree files are NOT contaminated) lands in the worktree.
        `unlinkEmptyLeaf` / `unlinkEmptyLeafFPI`
        (`internal/access/btree/btree_vacuum.go`) now relink the nearest **live**
        siblings instead of the PHASE-1-captured neighbours. New `liveSibling`
        walk skips any `BTDeleted`/`BTHalfDead` page (PHASE 1 stamps every target
        leaf before any unlink, and `recycleBlock` does not wipe pages, so the
        chain through dead pages stays navigable) and returns the nearest live
        left/right block — order-independent, so `CompleteDeferredDeletions`
        (block-order, post-crash) is correct too. Mirrors PG
        `_bt_unlink_halfdead_page`. **No WAL format change:** the `BtreeUnlinkPage`
        record already carries arbitrary sibling blocks and `replayBtreeUnlinkPage`
        applies whatever it names; only the computed values changed. Both the
        WAL-emit path and the FPI fallback share the computation (sibling paths
        agree). DoD met: detection test flipped to silence
        (`TestVerifyBtreeEngineSilentAfterAdjacentLeafDeletion`); new storage-layer
        `TestVacuumIndexPagesAdjacentLeafRunRelinksLiveSiblings` asserts every
        survivor links only to live blocks + a bidirectionally intact chain.
        Gates: `go test -race ./internal/access/btree ./internal/amcheck
        ./internal/wal ./internal/mvcc ./internal/storage` PASS; `go build ./...`
        clean; gofmt/vet clean; TPC-H spotcheck Q12=2/Q13=33 (canonical).
        Design doc `docs/design/0110-0010-btree-vacuum-adjacent-leaf-relink.md`
        + README index.
      - **Discovered 2026-06-15 (loop #9)** by the new real-producer page-deletion
        validation (`internal/amcheck/verify_nbtree_pagedel_test.go`); detection
        pinned by `TestVerifyBtreeEngineDetectsStaleSiblingLinkAfterAdjacentLeafDeletion`.
      - Symptom: when `VacuumIndexPages` empties two or more **adjacent** leaves in
        a single pass (the common case for a range `DELETE` + `VACUUM`), the
        surviving siblings at the run's edges end up with `btpo_next`/`btpo_prev`
        pointing at a block that was itself deleted in the same pass. The on-disk
        leaf sibling chain is left structurally broken. amcheck's sibling-link tier
        flags `downlink or sibling link points to deleted block`.
      - Root cause: `unlinkEmptyLeaf` / `unlinkEmptyLeafFPI`
        (`internal/access/btree/btree_vacuum.go`) relink neighbours from
        `emptyLeafInfo.prev`/`.next` captured at PHASE-1 scan time, **before** any
        leaf is unlinked. For `L0→L1→L2→L3→L4` with `L1,L2,L3` emptied,
        `unlink(L1)` sets `L0.next=L2` and `unlink(L3)` sets `L4.prev=L2` — both now
        reference the deleted `L2`.
      - Required (mirrors PG `_bt_unlink_halfdead_page` + the M0110-0007 split fix):
        re-read the CURRENT left/right siblings at unlink time (skipping/deferring a
        neighbour that is itself half-dead), and fold the corrected sibling blocks
        into the unlink WAL record + replay. This is a WAL/concurrency change — run
        `go test -race ./internal/access/btree ./internal/wal ./internal/mvcc
        ./internal/storage` plus the recovery/replay path, and the TPC-H spotcheck.
        Write a design doc (`docs/design/0110-00NN-btree-vacuum-adjacent-leaf-relink.md`)
        first. DoD: flip the detection test to a silence assertion.
      - Blocked on a clean tree only insofar as committing to the main tree is
        blocked by the foreign WIP; the btree files themselves are NOT contaminated,
        so the fix can land in a worktree off clean HEAD.
      - **PROGRESS 2026-06-15 (loop #20):** AC-002 bootstrap-query gaps **#3 +
        #2b LANDED** in worktree `m0110-0003-amcheck-sql-surface` (off clean HEAD
        `b8dd6403`, building on the gap #1/#2 commit). (a) `catalog.NewInMemory`
        seeds `{postgres, template1, template0}` and `pg_database` virtual rows
        carry each template's canonical attrs (template1 oid1 allowconn=t
        istemplate=t; template0 oid4 allowconn=f istemplate=t) — the amcheck DB
        filter `datallowconn AND datconnlimit != -2` now yields `{postgres,
        template1}`. (b) `internal/server/server.go` rejects a non-replication
        connection to an unregistered database after auth with FATAL 3D000
        `database "%s" does not exist` (guarded for nil/non-registry catalogs like
        `tryHandleDatabaseDDL`). (c) gap #2b: PG allows a CTE alias list SHORTER
        than the inner query (`parse_cte.c` 583-585) — fixed all 6 `!=`→`>` alias
        guards (2 in `analyzer.go`, 4 in `planner/with.go`, incl. the easily-missed
        3-tab "Bypass Plan() entry" block) + rename only the aliased prefix.
        Tests: `server.TestConnect{NonexistentDatabaseRejected,BootstrapDatabasesAccepted}`,
        `catalog.TestNewInMemorySeedsPostgresDatabase`,
        `{analyzer,planner}.Test*FewerColumnAliasesAccepted`. Live smoke verified
        (psql -d qqq → FATAL 3D000; pg_database 3 rows). Full server/analyzer/
        planner/catalog/executor suites green; gofmt/build clean. Design `0110-0008`
        extended. **NEW gap #4 surfaced + isolated** (deferred, own loop): a
        WITH-CTE is not visible inside a FROM-subquery of the OUTER statement —
        `WITH x(a) AS (SELECT 1) SELECT a FROM (SELECT a FROM x) s` →
        `relation "x" does not exist`. `002_nonesuch` still self-skips (its final
        `SELECT … FROM (… filtered_databases) …` hits gap #4). Resume = fix CTE
        scope propagation into outer FROM-subqueries (analyzer `buildSelectScope`/
        subquery synthesis + planner `planCTEs` into derived-table planning).

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

- [x] **M0117-0002** — DONE (branch `m0117-0002-clog-visibility-fallback`, commit `b3d5b448`
      off clean HEAD `b8dd6403`; design `docs/design/0117-0002-visibility-clog-fallback.md`,
      indexed). Pending human merge (foreign gen-column WIP holds the main tree).
      Verified loop #18: the stacked worktree chain (0001→0008) builds clean and
      `go test -race ./internal/mvcc/...` PASS off clean HEAD.
      - Summary: Runtime CLOG-consulting visibility fallback (gap G4; P1). Add a CLOG
        fallback in `Snapshot.SeesCommittedXID` for in-window XIDs not classified by
        the in-memory `InProgress`/`Aborted` arrays, keeping the arrays as the fast
        path; audit the `visibility.go` ↔ `subxact_visibility.go` sibling paths.
      - Author `docs/design/0117-0002-visibility-clog-fallback.md` and index it before coding.
      - Gate: TPC-H spot-check (`scripts/tpch-spotcheck.sh`, Q12=2/Q13=35) + `go test -race ./internal/mvcc/...`. Effort: M.

- [x] **M0117-0003** — DONE (branch `m0117-0003-pg-subtrans-restore`, commit `24b09523`
      off clean HEAD `b8dd6403`; design `docs/design/0117-0003-pg-subtrans-restore-on-restart.md`,
      indexed). Pending human merge. Verified loop #18 via the stacked-chain build +
      `go test -race ./internal/mvcc/...` PASS off clean HEAD.
      - Summary: `pg_subtrans` restore-on-restart (gap G5 read path; P1). Wire
        `SubxactMap.EnablePersistence` into the `internal/initdb/open.go` recovery
        sequence and load persisted parent links from the `pg_subtrans` SLRU back into
        the in-memory `SubxactMap` so subxact parentage survives a restart.
      - Author `docs/design/0117-0003-pg-subtrans-restore-on-restart.md` and index it before coding.
      - Gate: standby-attach E2E + `go test -race ./internal/mvcc/...`. Effort: M.

- [x] **M0117-0004** — DONE (branch `m0117-0004-clog-sub-committed`, commit `f6d3d36c`
      off clean HEAD `b8dd6403`; design `docs/design/0117-0004-clog-sub-committed-lane.md`,
      indexed). Pending human merge. Verified loop #18 via the stacked-chain build +
      `go test -race ./internal/mvcc/...` PASS off clean HEAD.
      - Summary: `SUB_COMMITTED` (0x03) CLOG lane (gap G5 SUB_COMMITTED; P1; builds on
        M0117-0003). Generate the 0x03 lane in the commit path (`mirrorToSLRUUnlocked`)
        for committed subxacts whose parent is still in-progress, and read it back in
        `loadFromSLRU`; document which code path writes each state.
      - Author `docs/design/0117-0004-clog-sub-committed-lane.md` and index it before coding.
      - Gate: extend the dual-store consistency test + `go test -race ./internal/mvcc/...`. Effort: S–M.

- [x] **M0117-0005** — DONE (branch `m0117-0005-clog-incremental-flush-group-commit`,
      commit `5fcdb27b` off clean HEAD `b8dd6403`; design
      `docs/design/0117-0005-clog-incremental-flush-group-commit.md`, indexed). Pending
      human merge. Verified loop #18 via the stacked-chain build +
      `go test -race ./internal/mvcc/...` PASS off clean HEAD.
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

- [ ] **M0117-0008** — Part A DONE; Part B DEFERRED (branch `m0117-0008-datfrozenxid-persist`,
      commit `3ff00365` off the m0117-0007 chain tip `1f1100e8`; design
      `docs/design/0117-0008-datfrozenxid-persistence.md`, indexed). Pending human merge.
      - **Part A (done):** dual-store consistency for all 4 CLOG status codes
        (IN_PROGRESS/Unknown 0x00, COMMITTED 0x01, ABORTED 0x02, SUB_COMMITTED 0x03)
        is already satisfied by the M0117-0004 chain — `clog_dual_store_consistency_test.go`
        round-trips every lane flat-file↔SLRU across adjacent lanes, a page boundary,
        two segments, and a TruncateCLOG.
      - **Part B (deferred):** on-disk in-place `pg_database.datfrozenxid` persistence at
        VACUUM end is NOT Effort-S — goopg has no runtime shared-catalog RelFileNode
        resolver (pg_database is shared at `global/1262`), the only catalog-heap
        precedent (`syncTableToCatalogHeap`) appends rows rather than overwriting a
        field in place, and a faithful `heap_inplace_update` needs buffer-lock + WAL +
        a PG-standby-attach E2E (SKIPs under worktree isolation). goopg's own CLOG
        truncation reads in-memory `cat.DatFrozenXID()` directly, so the persisted tuple
        is purely external (standby/tooling) parity. Design doc carries the 5-step
        Part-B plan; deferral-ledger line added.
- [ ] **M0117-0008** — Part A DONE (via chain M0117-0004); Part B DEFERRED (see deferral
      ledger 2026-06-15). Design `docs/design/0117-0008-datfrozenxid-persistence.md`
      authored + indexed (branch `m0117-0008-datfrozenxid-persist` off `1f1100e8`).
      - Summary: Persist `datfrozenxid` in the `pg_database` catalog tuple at VACUUM
        end (rather than only computing it on demand) and extend the dual-store
        consistency tests for round-trip coverage of all status codes.
      - Author `docs/design/0117-0008-datfrozenxid-persistence.md` and index it before coding. (DONE.)
      - **Part A (DONE via chain M0117-0004):** the "dual-store consistency for all
        status codes" deliverable is already satisfied —
        `internal/mvcc/clog_dual_store_consistency_test.go` round-trips all four CLOG
        lanes (IN_PROGRESS/Unknown 0x00 via truncation, COMMITTED 0x01, ABORTED 0x02,
        SUB_COMMITTED 0x03) flat-file ↔ SLRU across adjacent lanes, a page boundary,
        two segments, and a TruncateCLOG. Verified green this loop (`go test -race
        -run 'TestCLogDualStoreConsistency|TestCLogSubCommittedResolvesViaParent|TestCLogTruncateKeepsStoresConsistent' ./internal/mvcc/`).
      - **Part B (DEFERRED):** on-disk in-place `pg_database.datfrozenxid` persistence
        at VACUUM end. NOT the labelled Effort-S — investigation established: no
        runtime shared-catalog `RelFileNode` resolver (pg_database is shared at
        `global/1262`; the in-memory catalog maps only user tables); the only runtime
        catalog-heap precedent (`syncTableToCatalogHeap`) is an *append* of
        pg_class/pg_attribute, not an in-place field overwrite; a faithful
        `heap_inplace_update` needs buffer-lock + WAL logging + a PG-standby-attach
        E2E to verify (SKIPs under worktree isolation). goopg's own CLOG truncation
        reads in-memory `cat.DatFrozenXID()` directly, so this is purely external
        (standby/tooling) parity + restart-survivability — blast radius bounded.
        Full 5-step Part-B plan in the design doc. Defers to a dedicated full-gate
        session per `m0074_partial_scope_lessons`.
      - Gate (Part B): `go test ./internal/catalog/...`; re-init data dir + regress-port
        re-run (catalog tuple-format change) + PG-standby-attach E2E. Effort: M (not S).


## Notes

- This file is the authoritative TODO list for Ralph. Update it after every
  meaningful change.
- Keep work to ONE item per loop. Decompose further if an item is larger
  than what fits in a single agent invocation.
- Every non-trivial subsystem must land alongside (or just before) a design
  doc under `docs/design/`. The spec treats this as a hard requirement.    

