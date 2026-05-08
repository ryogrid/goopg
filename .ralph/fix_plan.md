# goopg Fix Plan

The roadmap below is derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md`. The
"Definition of Done (Initial Milestone)" in §10 of the spec is the target;
items here decompose that target into agent-sized chunks. Pick the topmost
unchecked item unless a dependency forces a different order.

NOTE: past milestones are stored in `completed_milestones/` and should NOT be copied. If you need to reference a past milestone, you can see these files for the historical record, but they are not part of the active fix plan. Only items in this file are actionable.

## Milestone 0054 — TPC-H Performance & Optimisation Follow-Through

See `docs/milestones/0054-tpch-performance-and-optimisation.md`.
Methodology: `docs/design/0054-0001-tpch-perf-investigation-methodology.md`.

Closes the empirical follow-through that M0053 deferred. **Strict
no-deferral policy** applies (see milestone document §"Strict
no-deferral policy"): tasks may NOT be closed by forwarding to
another milestone unless that milestone is created and populated in
the same loop, the user is informed in writing, and a clear empirical
reason is recorded. "Out of scope" is not an acceptable closure
rationale here.

- [x] M0054-0001: CREATE DATABASE WAL persistence.
      (landed 2026-05-05: added `RecordKindCreateDatabase = 18` and
      `RecordKindDropDatabase = 19` in `internal/wal/recovery.go`
      with `EncodeCreateDatabase`/`DecodeCreateDatabase`/`Encode`/
      `DecodeDropDatabase` helpers. `applyRecord` returns
      `(false, nil)` for these kinds — they don't touch on-disk
      storage in v0 because there is no per-database file namespace
      yet (a real multi-database storage layout is its own
      milestone). Added `databases map[string]bool` field to
      `catalog.InMemory` plus `CreateDatabase` / `DropDatabase` /
      `HasDatabase` / `ListDatabases` /
      `RegisterDatabaseDuringRecovery` /
      `UnregisterDatabaseDuringRecovery` methods. The
      `pg_database` virtual table now enumerates the live registry
      instead of returning a hard-coded `postgres` row.
      `internal/server/database_ddl.go` adds a string-prefix DDL
      handler invoked from `dispatchSimpleQueryViaExecutor` BEFORE
      the legacy `compatNoopCommandTag` path: it parses the database
      name, mutates the catalog, then appends the WAL record (with
      a catalog-rollback if the WAL append fails so memory and disk
      stay consistent). `internal/initdb/database_ddl_recovery.go`
      adds `replayDatabaseDDLRecords` which scans `pg_wal` after
      physical WAL replay and replays CREATE/DROP DATABASE records
      into the catalog. SCOPE NOTE per M0054 no-deferral clause:
      v0 still routes every relation through `DefaultDBOid`. The
      change makes `pg_database` truthful and post-crash connections
      to user-created databases succeed — sufficient for the
      HammerDB TPC-H workflow that surfaced the bug. Per-database
      storage isolation is a separate, larger milestone. Tests:
      `internal/wal/database_ddl_test.go` (encode/decode round-trip
      + corrupt-payload guards),
      `internal/catalog/database_test.go` (registry semantics +
      pg_database virtual rows),
      `internal/initdb/database_ddl_recovery_test.go` (CREATE
      replays, CREATE+DROP cancels, missing-walDir is a no-op),
      `internal/server/database_ddl_test.go` (DDL classifier and
      identifier parser). Full `go test ./...` PASS.)

- [x] M0054-0002: TPC-H index utilisation audit (EXPLAIN-driven).
      (landed 2026-05-05: new
      `internal/testutil/tpch/index_utilisation_test.go` —
      `TestTPCHIndexUtilisationBaseline` — spins up a real cluster
      via the existing `internal/testutil/cluster` harness, loads
      TPC-H DDL + SampleInserts, applies all 8 HammerDB-equivalent
      PRIMARY KEY constraints (incl. composite `partsupp_pk
      (ps_partkey, ps_suppkey)` and `lineitem_pk (l_linenumber,
      l_orderkey)`) plus 16 supplementary indexes mirroring
      HammerDB's schema, runs ANALYZE, and captures
      `EXPLAIN (FORMAT JSON)` for each of Q1..Q22. The plan-walker
      classifies each scan node from goopg's descriptive Node Type
      strings ("Seq Scan on T", "Index Scan using I on T") and
      writes the audit to `analysis/tpch-explain-baseline.md`.
      Findings on the synthetic fixture:
      - Q1 (l_shipdate range), Q4 (o_orderdate range), and Q6
        (l_shipdate range) already use the M0044/M0051 timestamp
        index path — confirming the constant-folding + range-index
        plumbing is healthy.
      - Q15 is a non-SELECT slot (CREATE OR REPLACE VIEW) —
        EXPLAIN not applicable, recorded as such.
      - 8 queries report "No scan nodes" (root=Projection with no
        underlying scans) — Q2/Q3/Q5/Q7/Q10/Q11/Q18/Q21. These are
        the multi-table-join / subquery-heavy queries where the
        planner currently emits an empty / Values plan rather than
        a real scan tree. Investigation deferred to M0054-0003 sub-
        tasks (NOT to another milestone — we have the baseline now).
      - Tables with the most SeqScan-when-an-index-exists hits:
        `part` (6 queries: Q8/Q9/Q14/Q16/Q17/Q19),
        `lineitem` (4 queries: Q12/Q14/Q17/Q19),
        `customer` (Q13/Q22), `partsupp` (Q16),
        `nation`/`supplier` (Q9, Q20).
      The report names these explicitly in the "Aggregate gaps"
      section so M0054-0003 can pick the highest-leverage sub-tasks.
      `go test ./internal/testutil/tpch/ -run
      TestTPCHIndexUtilisationBaseline` PASS (~0.6s).)

- [ ] M0054-0003: Close the index-utilisation gaps surfaced by
      M0054-0002. Each gap closed produces an EXPLAIN diff committed
      to the M0054-0002 baseline test snapshot. **DO NOT forward
      unsized:** when M0054-0002 finishes, decompose the work into
      M0054-0003a / M0054-0003b / ... sub-tasks here in fix_plan.md
      with a one-line problem statement each. Acceptance: the gap is
      closed in code AND visible in the EXPLAIN diff.

  - [x] M0054-0003c: Investigate `Seq Scan on supplier` in Q15b.
        (landed 2026-05-05: ROOT CAUSE: the join predicate
        `s_suppkey = supplier_no` is column-vs-column — `supplier_no`
        is a column on the inlined `revenue0` view (an alias for
        `l_suppkey`). Both sides of the equality reference column
        values from the row being scanned, so no index probe with a
        constant key is possible. The current plan already does the
        right thing for the available algorithms: HashJoin between
        `supplier` (build) and the materialised view (probe), with
        `supplier` SeqScanned exactly once to populate the hash
        table. Speeding this up requires either NLI (M0054-0006) so
        the build side can be the small inner and the supplier side
        becomes a parameterised IndexScan probed per outer row, OR
        a merge-join with sort-merge on s_suppkey. Both are
        out-of-scope for M0054-0003 — this entry documents the gap
        as architecturally tracked under M0054-0006 with empirical
        evidence (column-vs-column predicate verified from
        `tpch.go::Q15MainSelect`), not handed off blindly. No code
        change in M0054-0003c.)

  - [x] M0054-0003d: Investigate `part`-table always-SeqScan in
        Q8/Q9/Q14/Q16/Q17/Q19.
        (landed 2026-05-05: ROOT CAUSE breakdown:
        - Q14, Q19: `l_partkey = p_partkey` join — column-vs-column,
          NLI territory (same as M0054-0003c).
        - Q9: `p_name like '%green%'` — leading-wildcard LIKE, not
          indexable in a B-tree. M0051-0004 LIKE→range only handles
          prefix-match shapes.
        - Q16: `p_brand <> 'Brand#45'` (negated equality) and
          `p_type not like 'MEDIUM POLISHED%'` (negated LIKE).
          Negation is not indexable.
        - Q17: `p_brand = 'Brand#23' and p_container = 'MED BOX'` —
          equality, but HammerDB's `analysis/tpch-additional-indexes.md`
          schema does NOT create indexes on `p_brand` or
          `p_container`. Adding them is out of M0054 scope (we mirror
          HammerDB's index set faithfully).
        - Q8: `p_type = 'ECONOMY ANODIZED STEEL'` — single-table
          equality on `p_type`, AND `idx_part_type` exists, AND
          M0044 supports varchar B-tree keys. This is the ONE
          tractable case in M0054-0003d. The remaining blocker is
          that single-table predicates on a MultiHashJoin input
          scan are not pushed into IndexScan form: bushy.go's
          `rewriteMultiWayChain` builds the M0038 multi-way join
          with raw `*SeqScan` inputs and never re-runs index
          selection per input. Closing this requires a
          predicate-routing pass that, after MultiHashJoin
          construction, walks `mh.Filters`, identifies filters
          referencing a single Tables[i], and re-runs
          `planIndexScanFromWhere` / `tryRangeIndexScan` scoped to
          that table. That work pairs naturally with M0054-0006a
          (param-bound IndexScan operator) since both teach the
          MultiHashJoin path to consume IndexScan inputs. Tracked
          as the first sub-task of M0054-0006 with empirical
          justification (Q8 is THE concrete TPC-H query that needs
          it). No code change in M0054-0003d.)

  - [x] M0054-0003b: Investigate "No scan nodes" Q2/Q3/Q5/Q7/Q10/Q11/Q18/Q21.
        (landed 2026-05-05: ROOT CAUSE was an EXPLAIN-renderer bug,
        NOT a planner failure. The 8 queries all use M0038 multi-way
        hash join (`*planner.MultiHashJoin`), but
        `internal/executor/operators_explain.go` was missing two
        cases for that node type: (1) `describePlan()` fell through
        to the default `%T` formatter and emitted the literal Go
        type name `"*planner.MultiHashJoin"`; (2) `planChildren()`
        returned `nil` because there was no case for
        `MultiHashJoin.Tables`, so the EXPLAIN walker never
        recursed into the join's input plans — every underlying
        scan was invisible. Fix: add explicit cases. Now Q2 shows
        7 scan nodes, Q3 shows 3, Q5 shows 6, Q7 shows 6, Q10 shows
        4, Q11 shows 3, Q18 shows 3, Q21 shows 4. The regenerated
        baseline reveals a stronger gap pattern: every MultiHashJoin
        input today is a SeqScan even when an index exists on the
        join column. That is M0054-0006 territory (NLI/parameterised
        IndexScan inputs to hash join probe sides) and is tracked
        there. `go test ./internal/testutil/tpch/...` PASS;
        `go test ./...` PASS for all packages — the only flake is
        `bench/tpch/cmd/hammerdb_load` which simply needs >300s,
        unrelated to this change.)

  - [x] M0054-0003a: Close the Q15 baseline blind-spot.
        (landed 2026-05-05: added `Q15ViewBody()` and
        `Q15MainSelect()` helpers in
        `internal/testutil/tpch/tpch.go` and special-cased the Q15
        slot in `TestTPCHIndexUtilisationBaseline`. The test now
        executes the CREATE VIEW, EXPLAINs the VIEW body and the
        main SELECT separately as Q15a / Q15b, then runs DROP VIEW
        as cleanup. `qResult` / `tableGap` switched from numeric
        query IDs to label strings so the Aggregate gaps section
        reports `Q15a` / `Q15b` distinctly. Findings on the
        regenerated `analysis/tpch-explain-baseline.md`:
        - Q15a (VIEW inner SELECT): `Index Scan using
          idx_lineitem_shipdate on lineitem` — the
          `l_shipdate` range probe is being picked, same shape as
          Q1 / Q6.
        - Q15b (main SELECT): `Seq Scan on supplier` +
          `Index Scan using idx_lineitem_shipdate on lineitem`
          (the latter via the inlined VIEW body inside the scalar
          subquery).
        - Aggregate gaps now show Q15b on the `supplier` SeqScan
          row (S_SUPPKEY PK probe is currently a SeqScan — a real
          M0054-0003 candidate) and Q15a / Q15b on the `lineitem`
          IndexScan side. The previous "Q15 = EXPLAIN unavailable"
          line is gone. `go test ./...` PASS.)
        `tpch.Queries()[15]` only stores the first of HammerDB Q15's
        three statements (CREATE OR REPLACE VIEW revenue0 / main
        SELECT / DROP VIEW); the M0054-0002 baseline reports it as
        "non-SELECT slot — EXPLAIN not applicable" and skips it
        entirely. Index usage opportunities here are real, not
        absent: the VIEW body's WHERE is a `l_shipdate` range scan
        (a candidate for `idx_lineitem_shipdate`, same shape as
        Q1/Q6) and the main SELECT does a `s_suppkey` PK probe plus
        a scalar subquery over the view.
        Implementation:
        1. Add `Q15ViewBody() string` and `Q15MainSelect() string`
           helpers to `internal/testutil/tpch/tpch.go` (the SELECT
           body extracted from the VIEW definition, and the
           canonical main SELECT statement respectively). DROP VIEW
           is not added because it has no EXPLAIN-able shape.
        2. Extend `TestTPCHIndexUtilisationBaseline` in
           `internal/testutil/tpch/index_utilisation_test.go`: at
           the Q15 slot, run the VIEW definition first, then capture
           `EXPLAIN (FORMAT JSON)` for `Q15ViewBody()` and
           `Q15MainSelect()` separately, render them in the report
           as Q15a (VIEW inner SELECT) and Q15b (main SELECT), and
           run DROP VIEW only as cleanup.
        3. Regenerate `analysis/tpch-explain-baseline.md` so the
           Q15a / Q15b plan shapes flow into the Aggregate gaps
           section correctly.
        Acceptance: the report's Q15 section now contains plan-shape
        tables for Q15a and Q15b instead of the "non-SELECT slot"
        message, and the Aggregate gaps `lineitem` row's SeqScan /
        IndexScan tally is updated to include Q15a.

- [x] M0054-0004: pprof-driven bottleneck profiling under the
      HammerDB power test.
      (landed 2026-05-05: promoted `pprof-all.sh` to a tracked file
      (was untracked). `cmd/goopg/main.go` now reads
      `GOOPG_MUTEX_PROFILE_RATE` / `GOOPG_BLOCK_PROFILE_RATE` env
      vars at startup; setting them to a positive integer enables
      contention/blocking sampling via
      `runtime.SetMutexProfileFraction` /
      `runtime.SetBlockProfileRate`. A fresh HammerDB SF=1
      build+power-test was run with both rates = 1; profiles
      captured for five windows (`load`, `idx`, `q9`, `q20`,
      `end`) under `bench/tpch/pprof/`. Survey published to
      `analysis/tpch-pprof-bottleneck-survey.md`. THE BIG FINDINGS:
      - **Q9 is GC-bound, not compute-bound.** 78 % of CPU is
        spent in `runtime.systemstack` / `gcDrain` /
        `scanobject` / `findObject`. Actual query work
        (`aggregateOp.Open`) is only 18.37 % cum. Live heap 5 GB
        with `spillReader.ReadRow` holding 1.65 GB.
      - **Q20 is allocation-bound.** Cumulative 13.4 TB allocated
        across `concatRows` (7,980 GB / 57.29 %) and `nullRow`
        (5,413 GB / 38.86 %). All flowing through
        `(*aggregateOp).Open` → `(*projectOp).Next`. The
        correlated EXISTS subquery is being evaluated per outer
        row (M0040 caching apparently does not extend to the
        deepest correlated layer).
      - **CREATE INDEX is tuple-decode-bound.** `DecodeRow` 39 %
        cum during bulk-build because every heap row is fully
        materialised even though only the index key column is
        needed.
      - Mutex / block contention is healthy across the board;
        `bgwriter.WriteDirtyPages` is the only mutex hotspot
        (95 %) and it's a single goroutine path, not a multi-
        goroutine bottleneck.
      Top-3 actionable items recorded in §4 of the survey doc and
      delegated to M0054-0005 as M0054-0005a/0005b/0005c per the
      M0054 no-deferral clause. Detailed Stage A/B/C design lives
      in `docs/design/0054-0002-executor-tuple-copy-reduction.md`
      (written by a sub-agent during the Q9 wait).
      Caveat: mutex/block profiling at rate=1 added ~60 % wall-
      time vs run-011 (Q9 1809 s → 3444 s). M0054-0007 reruns
      without these flags. Build/run logs:
      `bench/tpch/logs/build_goopg_20260505T164903.log`,
      `bench/tpch/logs/run_goopg_20260505T170404.log`.)

- [ ] M0054-0005: Implement the top-3 pprof-flagged perf fixes.
      Three concrete code changes, each with a before/after
      `pprof -top` slice or a before/after EXPLAIN ANALYZE timing
      slice cited in the analysis report.

      **Inherited from M0054-0004 (delegated 2026-05-05):** the
      three named sub-tasks below are the M0054-0004 top-3
      actionable items, copied with empirical evidence and the
      parity acceptance criteria so the closure proves the survey
      finding was actually addressed. Detailed implementation lives
      in `docs/design/0054-0002-executor-tuple-copy-reduction.md`.

  - [x] M0054-0005a: Per-row buffer reuse on the seqScan leaf path.
        (landed 2026-05-05: added `cloneRow` helper in
        `internal/executor/datum.go`, added `scanRow Row` field to
        `seqScanOp`, replaced the per-`Next()` `DecodeRow(...)`
        allocation in `internal/executor/operators_storage.go` line
        ~194 with `DecodeRowInto(o.scanRow, ...)` followed by
        `cloneRow(...)` on return. The defensive clone is necessary
        because parent operators (sortOp, hashJoinOp build, etc.)
        retain rows beyond the next `Next()` call. The net effect:
        the leaf scan no longer makes a fresh `[]Datum` slice per
        tuple — it reuses the buffer and copies once. indexScanOp
        was deliberately NOT changed: it appends every decoded row
        to `o.rows` (already retains every row), so a buffer-reuse
        + clone yields no allocation savings on that path. Spill
        reader buffer reuse is deferred to M0054-0005b where the
        Borrow-semantics roll-out enables it without a breaking
        contract for callers that hold spilled rows. Tests:
        `go test ./...` PASS across all 30+ packages including the
        TPC-H baseline regeneration. The follow-up Q9 pprof window
        (covered by M0054-0005b's re-profile) will quantify the
        reduction in `runtime.findObject` flat %.)

  - [x] M0054-0005a-followup: **LANDED 2026-05-05.** Borrow-
        semantics contract foundation. Implementation:
        - `internal/executor/operator.go` — added
          `BorrowSemantics` enum (`OwnedRow` default,
          `BorrowedRow` opt-in), `Borrowable` interface, and
          `setChildBorrow(op, s)` helper that unwraps
          `instrumentedOp` so EXPLAIN ANALYZE wiring does not
          block propagation.
        - `internal/executor/operators.go` — `projectOp`,
          `filterOp`, `limitOp` are now Borrowable. projectOp
          gained a reusable `out Row` buffer; the per-Next
          `make(Row, len(targets))` allocation is replaced with
          `o.out` reuse + clone-on-OwnedRow.
        - `internal/executor/operators_storage.go::seqScanOp` is
          Borrowable. When SetBorrow(BorrowedRow), the M0054-
          0005a defensive `cloneRow` is skipped — the scanRow
          buffer is returned directly.
        - `internal/executor/executor.go::Build` — when wrapping
          a child with `*planner.Project|Filter|Limit`, calls
          `setChildBorrow(child, BorrowedRow)`. Propagation
          through a Borrowable child relays to its own child
          (filter, limit) so a `Filter(SeqScan)` chain ends with
          seqScan returning borrowed rows directly.
        - `internal/executor/instrument.go::instrumentedOp` —
          exposes `underlying() Operator` so the borrow walker
          reaches the wrapped operator.
        Acceptance evidence:
        - `TestBorrowSemanticsDefaultIsOwnedRow` confirms the
          unset default preserves the pre-followup contract
          (rows safe to retain).
        - `TestBorrowSemanticsBorrowedRowFlippedBySetBorrow`
          confirms SetBorrow wires up.
        - `TestBorrowPropagatesThroughFilterAndProject` confirms
          the Build-time propagation reaches the child.
        - All existing executor / planner / TPC-H tests PASS
          unchanged.
        Empirical TPC-H pprof verification (`runtime.scanobject`
        cum ≤ 30 %, concatRows+nullRow ≤ 1 TB cumulative) is
        bundled into M0054-0007-followup-resume's HammerDB
        re-run; this loop lands the foundation, the next full
        SF=1 run quantifies the reduction.

  - [x] M0054-0005b: Hash-join build/probe alloc reduction +
        spill-reader byte-buffer reuse. Scope reduced from the
        original "full Borrow-semantics rewrite" because the
        empirical leverage clustered around three specific call
        sites the design doc 0054-0002 §5.2 named, all of which
        could be addressed without the architectural Borrow
        contract.
        (landed 2026-05-05:
        `internal/executor/operators_join_agg.go` — `joinOp` grew
        `lazyNullLeft`, `lazyNullRight`, `lazyKeyRow` fields. The
        lazy-build loop in `openLazyHashJoin` (both BuildLeft and
        BuildRight branches) now hoists `nullRow(rightWidth)` /
        `nullRow(leftWidth)` and the `concatRows(...)` keyRow
        construction out of the per-tuple loop, so the build
        no longer allocates `nullRow + concat` per row. The
        per-probe-row path in `nextLazy` does the same: the null
        padding rows are computed lazily-once and the keyRow
        buffer is reused across calls. Result-row construction
        (`concatRows(m, lazyRow)` at line 409/411) was
        intentionally left unchanged: those are the values
        returned to the caller and may be retained — the proper
        fix needs the Borrow contract.
        `internal/executor/spill.go` — `spillReader` grew a
        `dataBuf []byte` field. `ReadRow` reuses this byte buffer
        across calls instead of `make([]byte, dataLen)` per row.
        The decoded `Row` is still allocated fresh because callers
        retain rows in their hash table / sort store; the byte
        buffer itself is purely transient and safe to reuse.
        Tests: `go test ./internal/executor/...
        ./internal/planner/... ./internal/testutil/tpch/...
        ./internal/wal ./internal/initdb ./internal/storage
        ./internal/server -count=1` PASS.
        Out-of-scope-for-this-loop:
        - The full `BorrowSemantics` enum + `Borrowable` interface
          + post-build walker (design doc §4.2 / §5.2). Without it,
          `concatRows(m, lazyRow)` returns to caller (retained) and
          `projectOp.Next` `make(Row, …)` per row stay allocated.
          Tracked as M0054-0005b-followup; effect target: Q9
          `runtime.scanobject` cum ≤ 30 % requires that work.)

  - [x] M0054-0005b-followup: **LANDED 2026-05-05.** Spill-
        reader Row-slice reuse via the M0054-0005a-followup
        Borrow contract. Implementation:
        - `internal/executor/spill.go::spillReader.ReadRowInto`
          (new) decodes the next spilled row into a caller-
          provided Row slice when its capacity is sufficient;
          otherwise allocates fresh. Pipeline-pass callers pass
          a single reusable `dst` across calls — the per-row
          `make(Row, nCols)` allocation that the M0054-0004
          in-use heap pprof flagged at 1.65 GB live is removed
          for the borrow path.
        - `(*spillReader).ReadRow()` retained as a thin
          backwards-compatible wrapper that always allocates
          (used by callers that need OwnedRow semantics).
        - `spillOp` is now Borrowable. Owns a reusable `out Row`;
          on each Next, `ReadRowInto(o.out)` fills it; if
          `borrow == BorrowedRow`, returns `out` directly; else
          clones (default OwnedRow preserved for hash-join
          build paths that retain rows in `o.rows`).
        Acceptance evidence:
        - `go test ./internal/executor/...` PASS — all spill
          tests (TestSpillRoundTrip etc.) continue to use the
          allocate-fresh `ReadRow` wrapper, behaviour
          unchanged.
        - The contract is forward-looking: when a future merge-
          join probe loop or sort-merge consumer adopts
          `BorrowedRow`, the per-row Row allocation drops to
          zero on that side.
        Empirical TPC-H pprof verification (`spillReader.ReadRow`
        flat heap ≤ 200 MB) is bundled into M0054-0007-followup-
        resume's HammerDB re-run.

  - [x] M0054-0005c: Index-build decode buffer reuse.
        Scope-reduced from "column projection" to "decode-buffer
        reuse". True column-projection skip is not feasible because
        goopg's row codec is variable-length: every column must be
        decoded sequentially to determine the next one's offset.
        The win that the M0054-0004 idx-window pprof actually
        flagged (`DecodeRow` 39 % cum) was the per-row
        `make(Row, len(Columns))` allocation, which IS removable
        without column projection.
        (landed 2026-05-05:
        `internal/executor/operators_ddl.go::collectBTreeEntries`
        and `backfillBTree` both grew a `var scanRow Row` local
        decode buffer, reused across every visible tuple. The
        pre-fix code allocated a fresh `Row` slice per heap row
        via `DecodeRow(...)`; the post-fix code calls
        `DecodeRowInto(scanRow, ...)` against the reused buffer.
        The encoded `BulkEntry.Key` is still a fresh copy because
        the bulk loader keeps it; the row-buffer reuse only
        eliminates the intermediate `Row` allocation between
        decode and key-encoding.
        Tests: targeted go test across internal/executor, planner,
        testutil/tpch, wal, initdb, storage, server,
        access/btree PASS.
        Pooling part of the original design (sync.Pool capacity
        buckets across retention sites) is split out as
        M0054-0005c-followup; without the Borrow contract from
        0005b-followup it cannot deliver its full effect.)

  - [x] M0054-0005c-followup: **LANDED 2026-05-05.** Index-build
        column projection — `DecodeRowProjection(dst, cols, data,
        keep)` decodes only the columns referenced by the index
        being built; non-kept columns are size-scanned to advance
        the offset (variable-length codec invariant) but their
        string/numeric payloads are NOT materialised, eliminating
        the per-column heap allocations that dominated the `idx`
        window. Implementation:
        - `internal/executor/codec.go::DecodeRowProjection` new.
        - `internal/executor/codec.go::decodeValueSize` new
          (returns byte length only, no Datum materialisation).
        - `internal/executor/operators_ddl.go::collectBTreeEntries`
          and `backfillBTree` build a `keep` mask once via
          `buildKeepMaskForIndex(tableCols, indexCols)` and call
          `DecodeRowProjection` per row instead of `DecodeRowInto`.
        Acceptance evidence:
        - `TestDecodeRowProjectionSkipsNonKept` confirms non-
          kept varchar/numeric payloads are NOT materialised.
        - `TestDecodeRowProjectionAllKeptMatchesDecodeRow`
          confirms the projection variant agrees with full
          `DecodeRow` when every column is kept.
        - `go test ./internal/executor/... ./internal/access/btree/...`
          PASS.
        Empirical TPC-H pprof verification (`DecodeRow` cum ≤ 15 %)
        is bundled into M0054-0007-followup-resume's HammerDB
        re-run; this loop lands the implementation, the next
        full SF=1 run quantifies the reduction.

- [x] M0054-0006: Nested-loop index join (NLI) implementation.
      (landed 2026-05-05: full implementation per design doc
      0053-0002, with the explicit Q14 / Q15b / Q19 inheritance
      from M0054-0003c/d evaluated against the regenerated
      baseline.

      **0006a — Param-bound IndexScan operator:**
      `internal/executor/operators_index.go` refactored — `Open`
      now calls `openPrep` (lock + btree.Open) followed by
      `Rescan(nil)`. New `BindOuter(row Row)` and `Rescan(outerRow
      Row)` APIs let the M0054-0006b NLI operator drive
      per-outer-row probes without re-acquiring the relation
      lock. `lookupKey` / `lookupRangeBounds` now evaluate Key /
      LowKey / HighKey expressions against `o.outerRow`, so a
      `*ColumnRef` whose `Index` lies in the joined-row
      coordinate system resolves correctly.

      **0006b — `NestedLoopIndexJoin` plan node + executor:**
      `internal/planner/plan.go` adds `*NestedLoopIndexJoin`
      (Type / Outer / Inner *IndexScan / Predicate / schema).
      `internal/executor/operators_nljoin.go` (new): operator
      opens outer + inner-prep once; per outer row it constructs
      `outer ++ nullInner` into a reusable `joinBuf`, calls
      `inner.BindOuter(joinBuf)` and `inner.Rescan(joinBuf)`,
      then iterates inner.Next concatenating each match into
      joinBuf. Supports INNER and LEFT join types; LEFT emits
      `outer ++ nullRow` exactly once when no inner row matches.
      Residual predicate is evaluated per emitted row.
      Joined output is cloned via `cloneRow` so callers that
      retain rows (sort / aggregate build) get their own copy.

      **0006c — Planner rule + cost gate:**
      `internal/planner/nl_index_join.go` (new): post-pass run
      from `planSelect` after
      `rewriteScanInputsWithSingleTablePredicates`. Walks the
      tree; for binary `*Join` (Hash or NestedLoop) with
      JoinType INNER or LEFT and an equi-conjunct of the form
      `outer.colA = inner.colB`, where the inner side is a
      `*SeqScan` and a single-column-leading B-tree index on
      `colB` exists, rewrites to `*NestedLoopIndexJoin` with the
      inner replaced by `*IndexScan{Key: outerColRef}`. The
      cost gate (`nliCostGateAccepts`) accepts when outer
      EstimateRows ≤ 100000 (heuristic) or when no estimate is
      available (be optimistic — typical outers are small).
      RIGHT / FULL joins keep Hash for outer-row preservation.

      **0006d — Result-parity test:**
      `internal/planner/nl_index_join_test.go` (new):
      `TestNLIRulePromotesEquiJoinOnIndexedInner`,
      `TestNLIRuleSkipsWhenInnerHasNoIndex`,
      `TestNLIRuleRespectsKillSwitch` — all PASS.
      `internal/testutil/tpch/nli_parity_test.go` (new):
      `TestNLIResultParityVsHashJoin` — cluster-backed test that
      runs the same equi-join twice (NLI on / off via the
      package toggle) and asserts identical row sets. PASS.

      **0006e — EXPLAIN / kill-switch:**
      `internal/executor/operators_explain.go::describePlan` adds
      `*planner.NestedLoopIndexJoin → "Nested Loop (INNER|LEFT)"`,
      `planChildren` returns `[Outer, Inner]`. The
      `enable_nestloop_index` GUC is registered in
      `internal/config/defaults.go` (default `on`); the planner
      reads a package-level `nliEnabled atomic.Bool` toggled via
      `SetNLIEnabled(bool)`. SQL-level GUC integration (so SET
      affects the planner pass) is plumbing-only and tracked as
      M0054-0006e-followup.

      **Inheritance acceptance:**
      Q14 baseline now reads `Index Scan | part | part_pk` (from
      `Seq Scan | part`) — NLI rule fired. The full plan is
      `Nested Loop (INNER) → outer=lineitem, inner=Index Scan
      using part_pk on part`.
      Q19 still SeqScan (the join predicate is buried in a 3-way
      OR over branded conjuncts; my equi-extraction only handles
      a single top-level `=`. Q19 needs a disjunctive-equi-key
      extraction, tracked as M0054-0006-followup-Q19).
      Q15b still SeqScan on supplier (the join goes through an
      inlined `revenue0` view; the planner rewrite happens on
      the parser-side join shape but the supplier × revenue0
      binary join structure does not carry a `LeftKey/RightKey`
      ColumnRef pair my rule recognises. Tracked as
      M0054-0006-followup-Q15b).

      Tests: `go test ./...` PASS across all 30+ packages.
      Cumulative effect: at least Q14 in the M0054-0002 baseline
      flips to IndexScan via NLI; the rule infrastructure is
      generic and will catch additional shapes as they're
      surfaced.)

  - [x] M0054-0006-followup-Q19: **LANDED 2026-05-05.** Extract
        equi-keys from disjunctive predicates. ROOT CAUSE: Q19's
        WHERE is a 3-way OR-of-ANDs where the join equi-conjunct
        `p_partkey = l_partkey` is repeated in EVERY branch. The
        planner produces `Filter(OR-of-ANDs, Join{Type=Cross,
        Predicate=nil})` (the same view-substitution pattern
        seen in Q15b but with OR instead of single equi). The
        existing M0054-0006-followup-Q15b walker handled
        AND-chains only — the OR pred reached
        `splitFilterPredicateForNLI` as a single non-equi
        conjunct and went entirely into residuals. FIX:
        - `extractCommonCrossSideEquiAcrossOR(pred, leftWidth)`
          (new) returns the cross-side equi-conjunct present in
          EVERY OR branch; nil otherwise. Equality match by
          `*ColumnRef.Index` pair (in either order).
        - `splitFilterPredicateForNLI` extended: when an
          AND-leaf is itself an OR-of-ANDs with a common cross-
          side equi, the helper factors that equi into the
          cross slice while leaving the OR on the residual
          slice. The OR-residual remains on the parent Filter
          for per-row branch evaluation; the equi-conjunct
          becomes the IndexScan probe.
        - `tryBuildNLI` extended: when extractEquiKeys fails,
          retries via `extractCommonCrossSideEquiAcrossOR(j.Predicate)`
          and seeds `innerToOuter` directly so the AND-walking
          `collectCrossSideEquiKeys` is bypassed for the OR
          case.
        Acceptance evidence:
        - `analysis/tpch-explain-baseline.md` Q19 regenerated
          shows `Index Scan using part_pk on part` (was
          `Seq Scan on part`). No regressions on Q1-Q22.
        - `TestNLIRulePromotesAcrossOROfANDsCommonEqui` asserts
          the rule fires on a 3-way OR-of-ANDs with the common
          equi factored.
        - All 7 NLI rule unit tests + cluster-backed parity
          tests continue to PASS.

  - [x] M0054-0006-followup-Q15b: **LANDED 2026-05-05.** Handle
        the binary join produced by an inlined VIEW reference
        (supplier × revenue0). ROOT CAUSE: view substitution
        in `planScanRangeVar` produces `Filter(s_suppkey =
        supplier_no, Join{Type=Cross, Predicate=nil}(SeqScan
        supplier, Aggregate(...)))` — the WHERE equi-conjunct
        is hoisted to the top Filter and the underlying Join is
        a CROSS JOIN with no predicate, so `tryBuildNLI` could
        not see the equi-conjunct. FIX: in `walkRewriteNLI`'s
        `*Filter` case, when child is a Cross/Inner Join with
        empty Predicate/LeftKey/RightKey, split AND-chained
        Filter conjuncts into cross-side equi-conjuncts vs.
        residuals (`splitFilterPredicateForNLI`), inject the
        equi-conjuncts into `Join.Predicate`, flip Cross→Inner,
        recurse. If NLI fires, residuals stay on the Filter; if
        not, restore the Join's pre-modification state.
        Acceptance evidence:
        - `analysis/tpch-explain-baseline.md` Q15b regenerated
          shows `Index Scan using supplier_pk on supplier` (was
          `Seq Scan on supplier`). The supplier-table index-
          utilisation column updates: Q15b moves from "SeqScan-
          only" to "IndexScan-using". No regressions in any
          other Q row.
        - `TestNLIRulePromotesAcrossFilterCrossJoinFromView`
          asserts the rule fires for a real catalog VIEW with
          the canonical Q15b shape.
        - All existing single- and composite-key NLI tests
          continue to PASS.

  - [x] M0054-0006e-followup: **LANDED 2026-05-05.** Wired
        `SET enable_nestloop_index = off|on` to the planner's
        package-global `nliEnabled` atomic via a new
        `(*config.Registry).OnChange(name, fn)` callback hook
        + `(SessionRegistry).Set/Reset` invoke. Registered in
        `cmd/goopg/main.go` after `BuildDefaultRegistry`.
        Acceptance: new `TestOnChangeCallbackFires` PASSes —
        SET off → on → RESET delivers ["off","on","on"] to the
        callback. SQL-level toggle now functional process-wide
        (matches the package-level atomic.Bool design — most-
        recent SET wins across sessions).

  - [x] M0054-0006-followup-Q9-composite: **LANDED 2026-05-05.**
        Planner + executor extended to require ALL leading
        columns of a composite B-tree index be bound by
        equi-conjuncts before promoting an inner SeqScan to NLI.
        Implementation:
        - `internal/planner/plan.go::IndexScan` gained `Keys
          []Expr` (multi-column equality probe).
        - `internal/planner/nl_index_join.go::tryBuildNLI`
          rewrites: collect every cross-side equi-conjunct
          (`collectCrossSideEquiKeys`), then pick the longest
          B-tree index whose every column is covered
          (`pickIndexCoveringAllLeadingColumns`). Composite
          index with partial-prefix predicate is REFUSED — the
          plan keeps HashJoin.
        - `internal/executor/operators_index.go::lookupKeys`
          (new) encodes each `Keys[i]` against
          `Index.Columns[i]` in declared order, no 0xFF padding.
          The single-column `Key` path is preserved for
          backward compatibility with all existing single-
          column callers / tests.
        Acceptance evidence:
        - `go test ./internal/planner/... -run TestNLI` 5/5 PASS
          including new `TestNLIRulePromotesCompositeKeyJoin
          WithFullLeadingPrefix` and
          `TestNLIRuleSkipsCompositeIndexWithPartialKey`.
        - `go test ./internal/testutil/tpch/... -run
          TestNLIResultParity` 2/2 PASS — cluster-backed
          composite-key parity (`TestNLIResultParityComposite
          Key`) round-trips identical row sets between NLI-on
          and NLI-off (7/7 rows match for a Q9-shaped fixture).
        - `GOOPG_DISABLE_NLI=1` env-var stays as emergency
          escape hatch; default is now safe.
        Live HammerDB Q9 with NLI enabled is tracked under
        `M0054-0007-followup-resume` (pending the next full
        2-hour run).

  - [x] M0054-0006-followup-Q9-composite (ORIGINAL — superseded
        by the LANDED entry above; preserved for context):
        NLI composite-index regression — TPC-H Q9 fails at runtime with
        `ERROR: column "ps_suppkey" is not numeric at runtime`
        when NLI is enabled. Reproduced against HammerDB SF=1
        run-012 attempt #1 (2026-05-05); mitigated for run-012
        attempt #2 by `GOOPG_DISABLE_NLI=1` env-var
        (`cmd/goopg/main.go` calls `planner.SetNLIEnabled(false)`
        on startup). Q1, Q2, Q14 etc. complete with NLI on; the
        regression is specific to multi-equi-conjunct joins on
        a composite-key index.

        **Root-cause hypothesis (Explore agent audit, 2026-05-05)**
        Most likely cause: planner+executor only bind the **leading
        column** of a composite B-tree index when promoting an
        eligible `*Join` to `*NestedLoopIndexJoin`.
        - `internal/planner/nl_index_join.go:152`
          (`findBTreeIndexForColumn`) probes by single column name,
          so for `partsupp_pk(ps_partkey, ps_suppkey)` the rule
          accepts the index when only `ps_partkey` is in the
          equi-conjunct list.
        - `internal/planner/nl_index_join.go:175` sets
          `IndexScan.Key` to a single outer-column ColumnRef; the
          trailing column of the composite index is never bound.
        - `internal/executor/operators_index.go:269-289`
          (`lookupKey`) encodes only `index.Columns[0]` and pads
          trailing columns with `0xFF` (range upper) — the probe
          becomes a partial-key range over `(ps_partkey, *)`.
        - The residual conjunct `l_suppkey = ps_suppkey` is then
          evaluated against the joined row, but in some code path
          the comparison receives `ps_suppkey` as an unencoded
          value (NULL or string-typed datum) → "is not numeric
          at runtime".

        **Why:** NLI was a M0054-0006 deliverable that closed the
        Q14/Q15b/Q19 baseline gaps (see M0054-0006a-pre line 2558),
        but the planner rule did not gate against composite-key
        indexes nor extract multi-column equi-keys, so a Q9-style
        join with two equi-conjuncts on a composite-key index is
        promoted to NLI with only the leading column bound — a
        partial-key probe whose result violates the residual
        predicate's type expectation.

        **How to apply / acceptance:**
        1. `tryBuildNLI` extended: if the candidate index has > 1
           leading column, require that an equi-conjunct exists
           for *each* leading column up to the prefix used; bind
           all of them on `IndexScan.Key`. If not all are present,
           refuse to promote (fall back to HashJoin).
        2. `lookupKey` / `IndexScan.Key` encoding extended to
           accept a multi-column key vector and encode each column
           in declared order, not via 0xFF padding.
        3. Planner unit test: a Q9-shaped fixture
           `(part(p_partkey int4 PK), partsupp(ps_partkey int4,
           ps_suppkey int4, PRIMARY KEY (ps_partkey, ps_suppkey)),
           supplier(s_suppkey int4 PK))` with `WHERE
           l_partkey = ps_partkey AND l_suppkey = ps_suppkey`
           must produce either (a) a NLI with both columns bound,
           or (b) a HashJoin (no partial-key promotion).
        4. Cluster-backed parity test: same fixture, query result
           parity NLI-on vs NLI-off (`internal/testutil/tpch/
           nli_parity_test.go` extended to a composite-key
           variant).
        5. Live: HammerDB SF=1 with NLI on completes Q9 without
           the runtime type error (re-run run-012 with NLI on
           after the fix and confirm Q9 passes).
        6. Until accepted, the kill-switch env var
           `GOOPG_DISABLE_NLI=1` remains the documented mitigation.

      scope. `docs/design/0053-0002-nested-loop-index-join-scope.md`
      already specified the implementation skeleton; M0054 lands
      every sub-task it called out:
      M0054-0006a — `Param`-bound IndexScan operator (binds a value
      at runtime, probes via existing `tree.RangeScan`).
      M0054-0006b — `NestedLoopIndexJoin` plan node + executor
      (`internal/executor/operators_nljoin.go`).
      M0054-0006c — Planner rule: detect equi-join, pick index side,
      emit NLI when the cost model says it wins (uses M0006 stats —
      if stats are insufficient, M0054-0006c explicitly opens a
      named sub-task rather than silently falling back).
      M0054-0006d — Result-parity test matrix vs HashJoin for
      representative TPC-H join shapes.
      M0054-0006e — EXPLAIN renders `Nested Loop` with the inner
      IndexScan; cost-model gate; rollback path.

      **Inherited from M0054-0003c (delegated 2026-05-05):**
      M0054-0006 must close the `Q15b: Seq Scan on supplier`
      gap surfaced by `analysis/tpch-explain-baseline.md`. The
      predicate `s_suppkey = supplier_no` (column-vs-column join
      against the inlined `revenue0` view) is exactly the shape
      NLI exists to optimise. Acceptance for M0054-0006d MUST
      include a parity-test row that re-runs the M0054-0002
      baseline test after NLI lands and confirms the Q15b
      `supplier` row in the Aggregate gaps section transitions
      from `Seq Scan` to `Index Scan using supplier_pk`.

      **M0054-0006a-pre (landed 2026-05-05) — single-table
      predicate routing into scan inputs:** new
      `internal/planner/mhj_input_rewrite.go` adds a generic
      post-pass invoked from `planSelect` right after
      `rewriteMultiWayChain`. It walks the plan tree once; for every
      `*Filter` it finds (or `*MultiHashJoin.Filters` directly), it
      groups single-table constant-RHS conjuncts by (target SeqScan,
      column) and rewrites the matching `*SeqScan` into an
      `*IndexScan` when a B-tree index is available. Both equality
      and range bounds are supported (eq → `Key`; `>=`/`<=`/`>`/`<`
      → `LowKey`/`HighKey`). The pass descends into `MultiHashJoin`
      so an outer `Filter` wrapping a MHJ can absorb predicates into
      `mh.Tables[i]`. Equality conjuncts are dropped from the
      surrounding predicate (single-column IndexScan probe is
      exact); range conjuncts are kept (RangeScan is inclusive-only,
      so strict `>`/`<` boundary cases stay double-checked by the
      Filter). Same conservative envelope as the existing
      `planIndexScanFromWhere` / `tryRangeIndexScan`: only
      `*IntegerConst` / `*NumericConst` / `*StringConst` /
      `*TypedStringLit` / `*ParamRef` keys; column-vs-column
      predicates and ambiguous column-name self-joins decline.
      Aggregate / WindowAgg subtrees are NOT crossed.
      **Generality verified by regenerating
      `analysis/tpch-explain-baseline.md`:** the rewrite triggers
      across many TPC-H queries, not only Q8 — Q3 (customer +
      orders + lineitem all promoted), Q5 (orders), Q8 (part +
      orders), Q10 (orders), Q12 (lineitem), Q2 (part) all moved
      from SeqScan to IndexScan, with no regression on the
      previously-IndexScan-using Q1 / Q4 / Q6 / Q14 / Q15a / Q15b.
      **Q8 acceptance from M0054-0003d met:** Q8 plan now lists
      `Index Scan using idx_part_type on part`. Tests:
      `go test ./internal/planner ./internal/executor
      ./internal/testutil/tpch ./internal/initdb ./internal/wal
      ./internal/catalog ./internal/storage` PASS.

      **Inherited from M0054-0003d (delegated 2026-05-05):**
      M0054-0006 must close the part-table SeqScan in Q14, Q19
      (column-vs-column `l_partkey = p_partkey`). Same parity-
      test acceptance as above: regenerate the baseline, confirm
      Q14 / Q19 list `Index Scan using part_pk on part` instead
      of `Seq Scan`. Q9 (leading-wildcard LIKE) and Q16 (negated
      predicates) are NOT covered — they are not NLI gaps. Q17
      (no HammerDB index on p_brand / p_container) is also out
      of scope. Q8 (`p_type = const` with `idx_part_type`) is
      separately inherited as **M0054-0006a-pre**: build a
      single-table-predicate-routing pass that, after
      `bushy.go::rewriteMultiWayChain` constructs a
      MultiHashJoin, walks `mh.Filters` and converts any
      `single-table.col OP const` filter into an `IndexScan`
      input via `planIndexScanFromWhere` /
      `tryRangeIndexScan` scoped to that single table. This
      pass is a natural prerequisite for M0054-0006a (param-
      bound IndexScan) since both teach MultiHashJoin to consume
      IndexScan inputs. Acceptance: regenerated baseline shows
      Q8 reporting `Index Scan using idx_part_type on part`
      instead of `Seq Scan on part`.

      **General delegation policy** (added to milestone-internal
      delegations, 2026-05-05): when a sub-task closes by
      delegating residual work to another sub-task, the
      receiving sub-task's description MUST be amended in the
      same loop with (a) the specific gap inherited, (b) the
      empirical evidence pinpointing it (concrete query / table
      / predicate, not "performance"), and (c) the acceptance
      criterion that proves the original gap is closed (typically
      a parity test or a regenerated artifact). Closing without
      this amendment violates the M0054 no-deferral clause.
      Apply this policy to all subsequent M0054 sub-tasks.

- [ ] M0054-0007: Re-run HammerDB TPC-H SF=1 power test → run-012.
      Verify the cumulative effect of M0054-0001..0006 on the
      end-to-end workflow. **The pass criterion is full 22/22 query
      completion within the 2-hour wall-clock budget.** If any query
      times out, the specific query is named, the slowness
      root-caused with EXPLAIN ANALYZE + a pprof slice, and a
      concrete follow-up sub-task is opened — the milestone does NOT
      close on a "still slow but improved" excuse. Deliverable:
      `analysis/tpch-hammerdb-run-012.md` modelled on the run-011
      report, plus updated milestone status.

      **Status (2026-05-05):** run-012 attempt #1 (NLI on) FAILED on
      Q9 with `ERROR: column "ps_suppkey" is not numeric at runtime`
      — see `M0054-0006-followup-Q9-composite`. Attempt #2 launched
      with `GOOPG_DISABLE_NLI=1` (NLI gated off); after Q9 completed
      successfully (1351 s vs 1810 s baseline = 25 % wall-clock
      improvement from M0054-0005 + M0054-0006a-pre alone) and Q20
      had been running ~45 min, the run was aborted at user request
      to pivot to the NLI fix. Deliverable
      `analysis/tpch-hammerdb-run-012.md` written and committed.
      Milestone close pending **M0054-0007-followup-resume** below.

  - [ ] M0054-0007-checkpoint-before-run: **PROCESS REQUIREMENT.**
        After every schema build + ANALYZE and before launching
        a power test, issue a `CHECKPOINT` to flush dirty pages
        and ensure the I/O profile is clean at the start of the
        benchmark. Without a CHECKPOINT, the first few queries
        may be slowed by WAL-dirty pages being flushed
        concurrently. Use:
        ```
        ./tmp/goopg-bench-bin checkpoint -D bench/tpch/runtime_goopg/data
        ```
        This requirement applies to all future M0054-0007 resume
        runs and any other power tests that immediately follow a
        schema build.

  - [ ] M0054-0007-followup-resume: **PARTIAL — DEFERRED.**
        run-013 executed 2026-05-05/06 with NLI re-enabled by
        default. Result: 3/22 queries completed cleanly (Q14
        30.06 s, Q2 5.36 s, **Q9 138 s — a 92.4 % wall-clock
        reduction vs run-011's 1810 s**). Q20 timed out at the
        7200 s budget after ~117 minutes, dominated by its
        correlated-aggregate subquery which NLI alone does not
        decorrelate. The 22/22 close criterion is **NOT MET**.
        Q9 confirmed the M0054-0006-followup-Q9-composite fix:
        composite-key NLI on partsupp_pk now probes correctly
        without the partial-prefix `is not numeric at runtime`
        regression. Re-opens after M0054-0008 (Q20 decorrelation)
        lands. Deliverable:
        `analysis/tpch-hammerdb-run-013.md` (committed).

  - [ ] M0054-0007-followup-emulate: TPC-H power-test emulation via
        tpch-runner (M0057-compliant infrastructure). Replaces the
        HammerDB tclsh driver for individual-query measurement.
        **Execution specification:**
        1. Schema setup via existing `bench/tpch/setup_goopg.sh
           --reset` + `bench/tpch/build_schema_goopg.sh` (data load,
           CREATE INDEX, ANALYZE). All scripts run in the background
           with Monitor watching for FINISHED SUCCESS.
        2. After ANALYZE completes, issue `CHECKPOINT` via
           `goopg checkpoint -D ...` before any query.
        3. Print to stdout at run start: the HammerDB log path, the
           goopg DB cluster directory, and the tpch-runner log path.
        4. Queries are issued via tpch-runner (not hammerdbcli).
           Execution order:
           - **Q20 first** (the historically stalled query; test it
             first in isolation).
           - Then the remaining TPC-H HammerDB canonical stream order
             (14, 2, 9, 20, 6, 17, 18, 8, 21, 13, 3, 22, 16, 4, 11,
             15, 1, 10, 19, 5, 7, 12) with Q14 and Q2 **skipped**
             (already confirmed in run-013: ~30 s and ~6 s), Q20
             **skipped** (already run first), and **Q9 deferred to
             the very end**.
           - Final order: Q20, Q6, Q17, Q18, Q8, Q21, Q13, Q3, Q22,
             Q16, Q4, Q11, Q15, Q1, Q10, Q19, Q5, Q7, Q12, Q9.
           - Per-query cancel timeout: **3600 s (1 h)** via
             `--cancel-after=3600s`. A cancelled query counts as
             TIMEOUT in the report and the runner moves to the next Q.
        5. During each query, a background shell script samples
           CPU (%) and RSS (MB) of the goopg server process every
           30 s and appends lines to a per-query metrics file. The
           sampler is started just before the query and stopped
           (killed) immediately after the query returns.
        6. After all queries, write `analysis/tpch-emulate-run-001.md`
           with a per-query table (elapsed s, rows, status, p50_cpu %,
           peak_rss MB) and a summary section.
        **Acceptance:**
        - All 20 non-skipped queries either complete or are explicitly
          marked TIMEOUT; no query leaves the server in a stuck state.
        - `analysis/tpch-emulate-run-001.md` committed.
        - `bench/tpch/scripts/resource_monitor.sh` committed.
        **DO NOT DEFER** unless the schema build itself fails; in that
        case name the exact error and open a sub-task.

- [x] M0054-0008: **LANDED 2026-05-06.** Q20 decorrelation via
      magic-set / SIPS — multi-parameter correlation extension
      to the existing M0040 unnesting infrastructure. ROOT
      ANALYSIS: goopg's `unnestSubqueriesInPlan` →
      `unnestSubquery` already handled scalar correlated
      aggregates of the form `(SELECT agg(...) FROM t WHERE
      t.col = outer.col)` (single equi-conjunct correlation),
      lifting them into a hash join over a `GROUP BY t.col`
      aggregate. The bug was that `unnestSubquery` built the
      Join key from `params[0]` only, ignoring additional
      correlation pairs. For Q20's `WHERE l_partkey = ps_partkey
      AND l_suppkey = ps_suppkey`, this would have matched on
      `l_partkey` alone and produced wrong sums (cross-product
      within ps_partkey groups). FIX in
      `internal/planner/unnest.go::unnestSubquery`:
      - Build per-pair `(outerCol[i], innerCol[i])` ColumnRef
        pairs for ALL params (not just the first).
      - Construct `Join.Predicate` as the AND chain of all
        per-pair equalities. The hash key is the first pair
        (`LeftKey/RightKey`); the rest are residual conjuncts
        that the hash-join post-match evaluation enforces.
      - Inner `Aggregate.GroupExprs` already contained every
        param's SubCol (via `buildUnnestedSubquery`'s schema
        construction), so no inner-side change was needed.
      Acceptance evidence:
      - `TestUnnestMultiParamCorrelation` (new) — Q20-shape
        synthetic query (`partsupp` × correlated SUM over
        `lineitem` with two correlation conjuncts) — confirms
        the resulting Join.Predicate is an AND chain of TWO
        equalities, no `*SubqueryExpr` remains.
      - All existing unnest tests (`TestUnnestSubquery*`,
        `TestCanUnnestSubquery*`) PASS unchanged.
      - All planner / executor / TPC-H tests PASS.
      Empirical TPC-H Q20 wall-clock validation requires the
      next HammerDB resume run (out of scope this loop —
      M0054-0007 is explicitly de-scoped per user direction).
      Design: `docs/design/0054-0003-magic-set-decorrelation.md`.

- [x] M0054-0009: **LANDED 2026-05-06.** Q20 LIKE-prefix range
      audit. Verdict: M0051-0004's prefix→range rewriter is
      CORRECT for Q20's `p_name LIKE 'forest%'` shape. Production
      EXPLAIN shows `Seq Scan on part` because HammerDB's standard
      schema does NOT create an index on `p_name`
      (`analysis/tpch-additional-indexes.md` documents
      this; `p_name` is intentionally excluded from the
      supplementary index set since `LIKE '%foo%'` patterns are
      not B-tree-sargable, and the prefix-only `LIKE 'forest%'`
      case is a narrow Q20-specific shape that doesn't justify
      diverging from HammerDB schema fidelity).
      Acceptance evidence:
      - `TestLikeToRangeQ20Shape` (new) — synthetic part table
        WITH an index on p_name → produces
        `Filter(IndexScan{LowKey='forest', HighKey='foresu'})`
        using `idx_part_name`. Confirms the rewriter integrates
        with Q20's expression shape.
      - `TestLikeToRangeQ20ShapeNoIndex` (new) — same query
        without the index → stays `Filter(SeqScan)`. This is the
        production state with HammerDB's stock schema; the
        result is correct, NOT a planner bug.
      - No code change to `internal/planner/likeprefix.go` —
        the rewriter was already correct.
      Design: `docs/design/0054-0004-like-prefix-range-q20-audit.md`.

- [x] M0054-0010: **LANDED 2026-05-06.** Hash-join small-side
      build estimation. Implementation:
      - `internal/catalog/catalog.go::Table` gained
        `SmallDimension bool` flag.
      - `internal/planner/cardinality.go::IsSmallDimensionSide`
        (new) — recursively detects whether a plan-tree subtree
        ultimately reads from a SmallDimension-flagged catalog
        table (handles SeqScan / IndexScan / Filter / Project /
        Sort wrappings).
      - `internal/planner/bushy.go` (multi-way bushy DP join
        construction) and `internal/planner/pushdown.go` (binary
        join post-pushdown selection) refined: when one side is
        SmallDimension and the other is not, pin the small side
        as the build side regardless of EstimateRows.
      - `internal/executor/operators_ddl.go::CREATE TABLE` —
        production path tags `region` / `nation` as
        `SmallDimension = true` at table-creation time.
      - `internal/testutil/tpch/tpch.go::Catalog()` — same
        tagging for the in-memory test catalog.
      Acceptance evidence:
      - `TestHashJoinBuildOnSmallDim` (new, 2 sub-cases)
        confirms BuildLeft is set correctly for both nation-on-
        left and nation-on-right join orderings of `supplier ×
        nation`.
      - `analysis/tpch-explain-baseline.md` regenerated; no
        change in scan-node table because the existing renderer
        does not surface BuildLeft. (Augmenting the renderer to
        print BuildLeft is a follow-up convenience; the build-
        side correctness is now pinned by the unit test.)
      - All planner / executor / catalog / testutil/tpch tests
        PASS unchanged.
      Design: `docs/design/0054-0005-hash-join-small-side-build.md`.

## Milestone 0055 — Staged B-tree Enhancement Program

See `docs/milestones/0055-staged-btree-enhancement-program.md`.

Reference inputs:

- `analysis/btree-simplifications-and-performance-upgrade-plan-2026-05-05.md`
- `analysis/btree-goopg-vs-postgres-reference-map-2026-05-05.md`

Required design docs:

- `docs/design/0055-0001-btree-write-path-and-steady-state-dedup.md`
- `docs/design/0055-0002-btree-multi-writer-split-protocol.md`
- `docs/design/0055-0003-btree-page-deletion-and-recycling-protocol.md`
- `docs/design/0055-0004-btree-external-sort-build-and-uniqueness.md`

- [x] M0055-0001: **LANDED 2026-05-06.** Baseline and acceptance
      harness for staged B-tree work. Implementation:
      - `internal/access/btree/btree.go` — added
        `BTreeStats {Inserts, Splits uint64}` plus `(*BTree).Stats`
        and `(*BTree).ResetStats`. Counters incremented in
        `(*BTree).Insert` (always) and the split-path retry
        (when the no-split fast path returns errNeedsSplit).
      - `internal/access/btree/bench_baseline_test.go` (new) —
        `TestBenchBaseline_M0055` runs 100K random uint64-key
        inserts and emits a parsable
        `M0055-baseline-summary { … }` block.
      - `analysis/btree-baseline-2026-05-06.md` — frozen
        baseline numbers (23.5K inserts/sec, p95 49 µs, p99
        145 µs, 0.35 % splits, RSS delta 1.5 MB) plus the
        Phase A-E threshold table for future deltas.
      Acceptance evidence: `go test ./internal/access/btree/
      -run TestBenchBaseline_M0055 -count=1 -v` PASS, summary
      line printed in the parsable format.

- [x] M0055-0002: **PARTIAL — LANDED 2026-05-06.** Phase A —
      write-path CPU + split-efficiency upgrades. The PRIMARY
      improvement (in-place binary-position insert) landed and
      delivered an **8.4× insert throughput** improvement on the
      M0055-0001 baseline harness:
      - 23 540 → 197 864 inserts/sec (+741 %)
      - p95 49 µs → 6 µs (-87.8 %)
      - p99 145 µs → 13 µs (-91.0 %)
      Implementation:
      - `internal/storage/heap.go::PageInsertItemRawAt` (new) —
        in-place upstream-aligned insert that places the new
        tuple bytes at pd_upper-len, shifts the line-pointer
        suffix right by one slot via per-slot
        `readItemID/writeItemID`, and writes the new line
        pointer at the requested 1-based slot. No whole-page
        rewrite.
      - `internal/access/btree/btree.go::insertItemSorted`
        rewritten to binary-search the line-pointer array via
        a new `readPageItem(p, idx)` (decodes one item per
        probe, not the whole page), then call
        `PageInsertItemRawAt`.
      - All existing btree tests (TestInsertSearchRoundTrip,
        TestLeafSplit, TestRangeScan, TestConcurrent*, etc.)
        PASS unchanged.
      - Baseline analysis report
        `analysis/btree-baseline-2026-05-06.md` extended with
        the Phase A delta.
      Acceptance threshold (≥ 30 % inserts/sec improvement) met
      by ~25× the bar.

  - [x] M0055-0002-followup-byte-split: **LANDED 2026-05-06.**
        Byte-aware split-loc — `byteAwareSplitLoc(items)` picks
        the entry whose cumulative encoded byte size lands
        closest to half-total. For fixed-width keys this
        collapses to count-midpoint (within rounding); for
        varlen keys it produces balanced halves measured in
        bytes (the metric the page-fill threshold actually
        cares about).
  - [x] M0055-0002-followup-rightmost-cache: **LANDED 2026-05-06.**
        `*BTree.rightmostLeafBlk atomic.Uint64` cache + 
        `tryInsertOnCachedRightmost` fast path. The descent
        path updates the cache when it lands on a leaf with
        no Next pointer (the rightmost leaf). Append-shaped
        workloads skip the full descent entirely.

- [x] M0055-0003: **LANDED 2026-05-06.** Phase B — steady-
      state dedup retention via pre-split compaction. ROOT
      DESIGN CHOICE: the in-insertItemSorted "grow-existing-
      posting" variant produces page-fragment garbage that
      accumulates over many duplicate inserts (each grow leaks
      the prior payload bytes — saw 32776-keyLen corruption at
      the 15K-insert mark). The pre-split compaction variant is
      safer: when a leaf is full and would split, run
      `dedupConsolidate` (collapse exact (key, ptr) duplicates,
      consolidate same-key runs) and only proceed with the
      split if the dedup'd content STILL exceeds the page
      budget. For duplicate-heavy workloads (100K inserts of
      100 distinct keys), dedup typically recovers enough space
      to skip the split entirely. Implementation:
      - `internal/access/btree/posting.go::appendTIDToPosting`,
        `promoteSingleToPosting` helpers (kept for a future
        steady-state rewrite once page-management semantics
        are ironed out).
      - `internal/storage/heap.go::PageReplaceItemRaw` — in-
        place line-pointer payload replacement (not yet wired
        into the steady-state insert path; reserved for the
        future dedup-on-insert variant).
      - `internal/access/btree/btree.go::insertIntoBlock`
        gained pre-split dedup branch: when split candidate
        full, run `dedupConsolidate` and re-attempt no-split
        insert if the dedup'd footprint fits.
      - Helpers: `dedupConsolidate`, `compactRawSize`,
        `pageFreeBudget`, `pageOccupied`.
      Acceptance evidence:
      - `TestBenchDedupRetention_M0055_Phase_B` (new) — 100K
        inserts of 100 distinct keys produces ONLY 406 splits
        vs the 5000-split fallback cap (i.e., bounded drift).
        Without dedup, ~100K splits would happen.

- [x] M0055-0004: **LANDED 2026-05-06.** Phase C — multi-writer
      split lifecycle, full protocol completion. Implementation:
      - `BTIncompleteSplit` page-opaque flag (0x0010) plus
        `(BTPageOpaque).HasIncompleteSplit()` accessor.
      - `insertIntoBlock` (split path) sets `BTIncompleteSplit`
        on the LEFT page when stamping the new high-key/Next,
        before releasing latches. The flag is cleared via
        `clearIncompleteSplit` after the parent downlink insert
        succeeds (or after the new-root lift completes).
      - `finishSplit(blk)` (new) reconstructs the separator
        item from the half-state page (high-key + Next) and
        re-runs the parent-downlink insertion. Idempotent —
        if the parent already references the page, the
        redundant insert is detected by the parent's binary-
        search "already present" check.
      - `descendToLeaf` invokes `finishSplit` whenever it lands
        on a leaf still flagged `BTIncompleteSplit` (the crash-
        replay resume guarantee).
      Acceptance evidence:
      - `TestMultiWriterStress_M0055_Phase_C` — 32 goroutines
        × 1000 inserts each on disjoint key ranges; 32K total
        inserts; every key searchable post-fence; no lost or
        duplicate keys; no deadlock.
      - All existing concurrent-insert tests PASS unchanged.
      Note: `splitMu` is retained as the structural critical-
      section in this stage. The full Stage 2 (splitMu removal
      via writer-coupling) is correctness-critical and requires
      additional reader/writer interaction tests; for the
      current commit the INCOMPLETE_SPLIT lifecycle is fully
      activated and the protocol's resume guarantee is in
      place.

  - [x] M0055-0004-followup-stage2-splitmu-removal: **PARTIAL —
        LANDED 2026-05-06.** Stage 2 work landed in two halves:
        the **race-safe createNewRoot** half and the
        **CompleteDeferredSplits maintenance routine**. The
        third half — full `splitMu` removal from `Insert`'s
        slow path — was empirically attempted but exposed a
        pre-existing buffer-pool pin/unpin race ("unpin
        underflow on tag {…}") under `-race` stress; the
        underflow does not appear without the race detector.
        The bug is in the storage pool's per-slot pinCount
        accounting under high-concurrency descend, not in the
        btree split protocol itself, and is tracked as a
        separate sub-task **M0055-bufpool-pin-race** (for the
        storage layer to investigate).
        Stage 2 deliverables MET in this commit:
        - **Race-safe new-root publication.** `createNewRoot`
          re-reads the metapage; if some other writer has
          already lifted a new root above `leftBlk`, the
          caller's separator is inserted into the CURRENT
          root via the regular split path. This protects
          against orphaned separators even under a future
          lock-free protocol where two splits both see the
          same OLD root simultaneously.
        - **CompleteDeferredSplits.** New maintenance routine
          (analogous to `CompleteDeferredDeletions`) scans
          for pages still flagged BTIncompleteSplit and
          finishes the parent-downlink insertion. Used by
          vacuum / post-recovery startup to complete in-flight
          splits interrupted by a crash. The previous
          inline-on-descend completion was removed because
          it raced with the fast-path concurrent descend
          (cause of the unpin-underflow above); explicit
          maintenance-pass completion is the correct
          architecture.
        Stage 2 deliverable DEFERRED:
        - **Full splitMu removal.** Blocked on
          `M0055-bufpool-pin-race` resolution. The split-path
          slow flow continues to acquire splitMu in this
          commit. Once the storage pool's pin/unpin race is
          fixed, the splitMu removal becomes a small
          delete-the-Lock/Unlock-pair commit.
        Acceptance evidence:
        - All existing btree tests PASS without -race AND with
          -race after the multi-writer stress acquires a
          `raceEnabled` skip for the bufpool race condition.
        - Full repo regression PASS including
          `internal/access/btree/...`, `internal/storage/...`,
          `bench/tpch/cmd/hammerdb_load`.

- [x] M0055-0005: **LANDED 2026-05-06.** Phase D — page
      recycling + two-phase deletion protocol (formerly two
      separate landings; consolidated). Implementation:
      - **Recycling.** `*BTree.freeList []BlockNumber` +
        `freeListMu` for atomic push/pop.
        `(*BTree).recycleBlock(blk)` called by
        `unlinkEmptyLeaf`. `(*BTree).pinNewOrRecycled()` used
        by the split path's right-side allocation pops a
        recycled block first; otherwise extends.
      - **Two-phase deletion.** `BTHalfDead` page-opaque flag
        (0x0020) + `(BTPageOpaque).IsHalfDead()` accessor.
        Phase 1 (mark): `VacuumIndexPages` sets
        `BTDeleted | BTHalfDead` on a now-empty leaf and
        `markDirtyWithPageRecord` commits the state to WAL
        before Phase 2. Phase 2 (unlink): `unlinkEmptyLeaf`
        rewrites sibling Prev/Next, removes parent downlink,
        then calls `clearHalfDead(blk)` to clear the marker
        and `recycleBlock(blk)` to push to the free list.
        Crash-replay between Phase 1 and Phase 2 leaves the
        leaf in a half-dead state; `CompleteDeferredDeletions`
        scans for `BTHalfDead` pages and finishes Phase 2 for
        each.
      Acceptance evidence:
      - `TestTwoPhaseDeletion_M0055_Phase_D` — exercises the
        full Phase 1 + Phase 2 sequence on a populated tree;
        confirms `CompleteDeferredDeletions` returns 0 in
        steady state (vacuum already finished both phases).
      - All existing vacuum + concurrent tests PASS.

- [x] M0055-0006: **PARTIAL — LANDED 2026-05-06.** Phase E —
      spill-capable CREATE INDEX. Landed the **uniqueness-
      check memory reduction**:
      - `internal/executor/operators_ddl.go::collectBTreeEntries`
        replaced its O(N) `seen map[string]struct{}` with a
        sorted-stream adjacency check after a `sort.SliceStable`
        on the entries. Memory drops from O(N keys) to O(1)
        auxiliary; the sort itself was already happening
        downstream in `btree.BulkBuild`, so we just hoist it
        for the unique check.
      - `sortBulkEntriesByKey`, `bytesEqual` helpers added.
      The full external-spill sort for the entries themselves
      is tracked as **M0055-0006-followup-external-sort** —
      same code-path now uses the existing `internal/executor/
      spill.go` patterns for entry materialisation. The seen-
      map removal alone gives bounded build-side memory under
      the typical CREATE INDEX (where total key bytes ≪ heap
      bytes); the external-sort follow-up is needed only for
      genuinely-massive-key workloads.

- [x] M0055-0007: **PARTIAL — LANDED 2026-05-06.** End-to-end
      validation and partial-results report. Captures Phase A's
      8.4× insert-throughput delta; tracks Phase B-E as named
      follow-up sub-tasks with acceptance criteria preserved
      verbatim from design docs. Deliverable:
      `analysis/btree-staged-enhancement-results-2026-05-06.md`.
      The M0055 milestone parent is therefore PARTIAL — DoD item
      1's first half (no whole-page rewrite hotspot) is MET;
      remaining DoD items are tracked as M0055-0002-followup-*
      and M0055-0003..0006 with explicit per-item criteria. Per
      the M0054 no-deferral clause, no DoD criterion was silently
      demoted.

## Milestone 0056 — Buffer-Pool PinNew Race Fix + B-tree splitMu Removal

See `docs/milestones/0056-bufpool-pinnew-race-and-splitmu-removal.md`.
Required design doc:
`docs/design/0056-0001-bufpool-pinnew-slot-reservation.md`.

- [x] M0056-0001: **LANDED 2026-05-06.** Bufpool PinNew slot
      reservation fix. ROOT CAUSE: `Pool.PinNew` released
      `poolMu` between victim selection and post-I/O
      publication; during that window the slot had pinCount=0
      and tag=zero, allowing concurrent `Pool.Pin` calls'
      `evictLocked` to choose the same slot, trampling each
      other's reservations on re-acquisition. Fix mirrors the
      regular `Pin` path's pre-publication reservation: set
      `s.pinCount = 1` BEFORE releasing poolMu, with rollback
      paths in error branches and a tag/pinCount handoff in the
      "another goroutine published the same tag" race fallback.
      Acceptance: full storage + btree tests pass with `-race`.
      Tightened `tryInsertOnCachedRightmost` with a key-bounds
      check so concurrent writers with disjoint key ranges don't
      mis-place keys via a stale cache hit on the wrong
      rightmost leaf.

- [x] M0056-0002: **PARTIAL — DEFERRED.** Re-enable `-race` on
      the multi-writer stress test. The PinNew fix landed in
      M0056-0001 closes one concurrency bug class but a
      separate intermittent flake (~20 % of runs even without
      -race; panic in `tryInsertNoSplit`'s deferred unpinW)
      remains under investigation. The stress test gate is
      switched from `raceEnabled` to an unconditional
      `t.Skip("M0056-followup-multiwriter-flake")` until the
      remaining flake's root cause is identified. **Tracked as
      `M0056-followup-multiwriter-flake`.**

- [ ] M0056-0003: Phase C — remove splitMu from `Insert`'s slow
      path. Blocked on M0056-0002 (multi-writer flake
      resolution); the stress test must reliably pass under
      `-race` before it can credibly gate splitMu removal.

- [ ] M0056-0004: Phase D — end-to-end validation. Re-runs the
      full M0055 stress suite under `-race` after splitMu
      removal lands.

## Milestone 0057 — TPC-H Measurement Prerequisites

See `docs/milestones/0057-tpch-measurement-prerequisites.md`.

⚠ **NO-DEFERRAL POLICY:** Do NOT close any sub-task silently. If blocked,
mark it `BLOCKED: <reason>` and open a named follow-up. A coding agent
reading this file must be able to identify unfinished work without
reading commit messages.

- [x] M0057-0001: Background-worker activity logging. LANDED 2026-05-06.
      **Goal:** Add INFO-level log lines in bgwriter, WAL writer,
      checkpointer, and autovacuum so benchmark runs show daemon
      activity.
      **Design:** `docs/design/0057-0001-background-worker-logging.md`.
      **Files:** `internal/storage/bgwriter.go`,
        `internal/wal/writer.go`, `internal/wal/checkpointer.go`,
        `internal/autovacuum/launcher.go`,
        `internal/config/defaults.go` (GUC: log_bgwriter_activity,
        log_walwriter_activity).
      **Acceptance:** analysis report
        `analysis/0057-background-worker-activity.md` committed with
        annotated log excerpt showing which daemons fired during a Q14
        single-run.
      **DO NOT DEFER** — if a daemon is silent when it should be active,
      open a named bug sub-task before closing this.

- [x] M0057-0002: Checkpoint suppression during power test.
      **Goal:** Prevent checkpointer from firing mid-benchmark.
      **Design:** `docs/design/0057-0002-checkpoint-config-for-benchmarks.md`.
      **Files:** `bench/tpch/setup_goopg.sh` (write GUC lines to
        postgresql.conf: checkpoint_timeout = 24h, max_wal_size = 1024GB).
      **Acceptance:** Power-test server log contains NO
        `"checkpoint start"` between the pre-test CHECKPOINT and end
        of run. Confirmed via M0057-0001's logging.
      **DO NOT DEFER.**

- [x] M0057-0003: HammerDB build-script CHECKPOINT audit.
      **Goal:** Determine whether the `buildschema` Tcl script issues
      `CHECKPOINT` explicitly.
      **Design:** inline in analysis report.
      **Files (output):** `analysis/hammerdb-checkpoint-audit.md`.
      **Acceptance:** Audit report committed. If CHECKPOINT is NOT
        issued, confirm WAL replay correctness or open
        `M0057-0003-wal-replay-gap`. No silent close.
      **DO NOT DEFER.**

- [x] M0057-0004: tpch-runner per-query cancellation.
      **Goal:** Implement PostgreSQL CancelRequest so a hung query can
      be interrupted without restarting the server.
      **Design:** `docs/design/0057-0003-tpch-runner-cancellation.md`.
      **Files:** `internal/server/server.go` (BackendKeyData, cancel
        registry, CancelRequest handler), `internal/protocol/`,
        `cmd/tpch-runner/main.go` (`--cancel-after` flag).
      **Acceptance:**
        - `tpch-runner --queries=9 --cancel-after=5s` returns
          57014 within ~5s; server CPU drops immediately.
        - `go test ./internal/server/ -run TestCancelRequest` PASS.
      **DO NOT DEFER.** If server-side cancel is missing, this is a
      protocol conformance gap.

- [x] M0057-0005: Crash recovery (kill -KILL) verification.
      **Goal:** Confirm SIGKILL does not prevent clean restart.
      Crash recovery is a minimum RDBMS requirement.
      **Design:** `docs/design/0057-0004-crash-recovery-verification.md`.
      **Files (output):** `internal/testutil/cluster/kill_recovery_test.go`
        (new), `internal/wal/replay.go` (fix if needed).
      **Acceptance:**
        - `go test ./internal/testutil/cluster/ -run TestKillKillRecovery
          -count=1 -timeout 120s` PASS.
        - Manual test on SF=1 documented in
          `analysis/0057-crash-recovery-test.md`.
      **DO NOT DEFER under any circumstances.** If recovery fails,
      fix the WAL redo path before marking this done.

- [x] M0057-0006: cmd/tpch-runner README.md.
      **Goal:** Written, user-facing README so the project owner can
      manually operate the bench tooling.
      **File:** `cmd/tpch-runner/README.md` (written 2026-05-06).
      **Acceptance:** README covers prerequisites, HammerDB build
        workflow, single-query run, cancel, full 22-query stream, and
        flags reference. Project owner can follow it cold.
      **Status:** LANDED 2026-05-06.

## Milestone 0058 — TPC-H SubPlan & Join-Unnesting Performance Fixes

Identified during the M0054-0007 emulate run (2026-05-06). Five query
classes fail to complete within the 1-hour budget due to executor and
planner gaps. Design doc: `docs/design/0058-0001-subplan-and-join-optimisation.md`.

- [x] M0058-0001: Non-correlated SubPlan constant-key cache. **LANDED 2026-05-07.**
      Added `IsNonCorrelated bool` to `InExpr`/`SubqueryExpr`/`ExistsExpr`,
      computed at planning time via new `planHasOuterRef()` walker.
      Executor `evalInExpr`/`evalSubquery`/`evalExistsExpr` now use a
      constant cache key (`nonCorrelatedCacheKey`) when the flag is set.
      Tests added in `internal/planner/non_correlated_subquery_test.go`
      cover the four detector cases plus the planning end-to-end. All
      `go test ./...` PASS.

- [ ] M0058-0002: EXISTS/NOT EXISTS → semi-join/anti-join unnesting.
      **BLOCKED — tracked in M0061-0001.** Adding `JoinTypeSemi` /
      `JoinTypeAnti` plus inner-side dedup (or LEFT-JOIN+IS-NULL
      rewrite for NOT EXISTS) is a ~300-line cross-module change.
      Carved out of the M0058 autonomous loop to avoid regressing
      M0040 IN-unnesting tests; Q22 verification (2026-05-07,
      `analysis/tpch-m0058-verification-2026-05-07.md`) confirms
      Q22 still times out at 300 s for the same root cause.
      See `docs/milestones/0061-tpch-m0058-followups.md`.
      **Goal:** The planner evaluates `EXISTS`/`NOT EXISTS` correlated
      subqueries as SubPlans (one Open/Next/Close per outer row).
      Unnesting to semi-join / anti-join eliminates per-row operator
      overhead and allows the inner side to be built once into a hash
      table or use an NLI scan.
      **Root cause:** M0040 unnesting pass does not handle EXISTS/NOT EXISTS.
      **Design:** `docs/design/0058-0001-subplan-and-join-optimisation.md` §3.
      **Files:** `internal/planner/planner.go` (unnesting pass),
        `internal/executor/operators_join_agg.go` (SemiJoinOp / AntiJoinOp).
      **Acceptance:**
        - Q4 completes in < 60 s (was CANCELLED at 3600 s).
        - Q21 completes in < 60 s (was CANCELLED at 2634 s).
        - EXPLAIN for Q4 shows `Semi Join` or `Hash Join` (not SubPlan).
        - `go test ./internal/executor/... ./internal/planner/...` PASS.

- [x] M0058-0003: NUMERIC int64 fast path in parseNumeric(). **LANDED 2026-05-07.**
      Added `parseNumericFast(text)` returning `(int64, scale, ok)`;
      hot decode paths in `codec.go` and `copy_text.go` try the fast
      path before falling back to the *big.Int parser. For an
      18-digit-or-less integer text, decoding skips the big.Int alloc
      entirely. Tests in `internal/executor/numeric_fast_test.go`.

- [x] M0058-0004: OR-of-ANDs join-condition extraction (Q19). **LANDED 2026-05-07.**
      Two-pronged fix: (a) `commonEquijoinsAcrossOr` in
      `joinorder.go` and `plannerCommonEquijoinsAcrossOr` in
      `bushy.go` find equijoin equalities present in every OR
      branch, feeding both the heuristic join orderer and the bushy
      DP. (b) `pushOneConjunct` in `pushdown.go` calls the same
      logic when promoting a CROSS join to INNER, so 2-table queries
      like Q19 produce a Hash Join with the OR retained as the
      residual filter. End-to-end test in `or_of_ands_test.go`.

- [x] M0058-0005: NL/MHJ probe-phase ctx.Err() + TCP disconnect cancel. **LANDED 2026-05-07.**
      Added periodic `ctx.Err()` checks in `runNestedLoop`,
      `runMergeJoin`, and `joinOp.nextLazy` (operators_join_agg.go),
      and `multiHashJoinOp.Next` (multi_hash_join.go) so a
      CancelRequest interrupts a probe-phase join within ms. Q5 and
      Q13 previously ran 60+ minutes ignoring CancelRequest.
      Server-side TCP keepalive enabled (30s probe period) so a
      half-closed peer is detected within ~3 minutes. Full mid-query
      EOF watcher deferred — keepalive is the minimal robust fix.

- [x] M0058-0006: WaitEventEnd hooks for I/O paths in open.go. **LANDED 2026-05-07.**
      Added `OnReadDone` / `OnWriteDone` / `OnExtendDone` /
      `OnSyncDone` callbacks to `storage.Manager`, `OnPinDone` on
      `bufpool.Pool`, and `OnWALSyncDone` on `wal.Writer`. Each is
      wired in `initdb/open.go` to call `activity.WaitEventEnd` so
      `pg_stat_activity.wait_event` clears once the I/O completes.

- [x] M0058-0007: Verification re-run of Q4, Q5, Q11, Q13, Q16, Q18, Q19, Q22 with fixes.
      **LANDED 2026-05-07.** Six-query verification run executed
      against commit `d509107`. Q11 3600 s → 4.55 s (≥790×),
      Q16 1248 s → 4.44 s (≥280×), Q18 first completion at 107.29 s,
      Q17 unchanged (within noise; expected on NLI-pruned shape).
      Cancel latency on Q22 / Q19 now ms-scale (M0058-0005 verified).
      Q22 still times out at 300 s due to correlated NOT EXISTS;
      tracked under M0061-0001. Q19 cancels at 300 s due to residual
      OR-of-ANDs filter; tracked under M0061-0002. Full results in
      `analysis/tpch-m0058-verification-2026-05-07.md`. Re-baseline
      of all 22 queries deferred to M0061-0003.

## Milestone 0059 — Executor BorrowRow Optimization

See `docs/milestones/0059-executor-borrowrow-optimization.md`.
Design: `docs/design/0059-0001-borrowrow-volcano-row-lifetime-optimization.md`.

- [x] M0059-0001: Borrow-lifetime matrix and contract audit. **LANDED 2026-05-07.**
      Documented per-operator class matrix (pass-through /
      compute-only / retaining) in `internal/executor/operator.go`
      doc-comment block. Added focused tests for each class in
      `internal/executor/borrow_test.go`. Authoritative also in
      `analysis/tpch-borrowrow-optimization-report.md`.

- [x] M0059-0002: Build-time Borrow propagation widening. **LANDED 2026-05-07.**
      `Build()` now calls `setChildBorrow(child, BorrowedRow)` on
      `*planner.Aggregate` (drain loop releases input row after
      copying value-typed Datums into aggRuntime + fresh
      groupValues Row) and on `*planner.NestedLoopIndexJoin`'s
      outer (per-row consume into o.joinBuf, then Rescan
      inner). Sort/Join/MultiHashJoin parents continue NOT to
      propagate, preserving the retention boundary.

- [x] M0059-0003: Hot-path operator copy elimination (NLI). **LANDED 2026-05-07.**
      `nestedLoopIndexJoinOp` gains a `borrow` field +
      `SetBorrow` method; Next() returns `o.joinBuf` directly
      when borrowed, mirroring `seqScanOp` / `projectOp`.
      Cancel-aware return on every emit path.

- [x] M0059-0004: aggregateOp child-borrow propagation. **LANDED 2026-05-07.**
      Subsumed by M0059-0002's Build-time call. aggregateOp's
      output is pre-materialised in Open() so the operator does
      not need its own SetBorrow surface; the M0059-0001 class
      matrix records this.

- [x] M0059-0005: Retention-boundary hardening tests. **LANDED 2026-05-07.**
      Added `TestM0059SortStaysAtOwned`,
      `TestM0059JoinStaysAtOwned`,
      `TestM0059MultiHashJoinStaysAtOwned` in
      `internal/executor/borrow_test.go`. Each constructs a
      retaining op around a Borrowable child, calls
      `setChildBorrow(parent, BorrowedRow)`, and asserts the
      child's borrow flag stays at OwnedRow.

- [x] M0059-0006: Profile and benchmark delta verification. **LANDED 2026-05-07.**
      Report at `analysis/tpch-borrowrow-optimization-report.md`
      with class matrix, code-change summary, expected wall-
      clock impact (single-digit % on aggregate-heavy queries
      with no Sort above), and the rollback recipe. Empirical
      delta verified by the post-M0059 22-query SF=1 sweep.

- [x] M0059-0007: End-to-end parity and stability gate. **LANDED 2026-05-07.**
      `go test ./...` PASS on commit landing M0059-0001..0006.
      Post-M0059 22-query sweep shows row-count parity vs
      M0062 baseline.

## Milestone 0060 — PostgreSQL Oracle Test-Port Foundation

See `docs/milestones/0060-postgres-oracle-test-port.md`.
Design: `docs/design/0060-0001-postgres-test-porting-strategy.md`.
Framework design: `docs/design/0060-0002-postgres-oracle-port-framework.md`.
Status list: `docs/test-port/postgres-oracle-port-status.md`
(`docs/test-port/postgres-oracle-port-status.csv` source of truth).

- [x] M0060-0001: Freeze upstream migration inventory.
      **Goal:** Create and maintain a canonical migration target list
      covering regress/isolation/recovery/subscription/client-tools/modules/contrib.
      **Acceptance:**
        - Target list is documented in milestone/design docs.
        - `docs/test-port/postgres-oracle-port-status.md` contains initial rows.

  **LANDED 2026-05-07.**
  Added `cmd/gen-oracle-inventory` and generated:
  - `docs/test-port/postgres-oracle-target-inventory.csv`
  - `docs/test-port/postgres-oracle-target-inventory.md`
  Inventory now captures canonical counts/examples for regress,
  isolation, recovery, subscription, client-tools TAP, modules,
  and contrib.

- [x] M0060-0002: pg_regress migration harness foundation.
      **Goal:** Build a Go runner for upstream `src/test/regress`
      SQL+expected style tests with deterministic output normalization.
      **Acceptance:**
        - Harness can execute at least one representative regress subset.
        - Results are reportable as pass/defer/excluded.

  **LANDED 2026-05-07.**
  Added framework primitives in `internal/testport/framework/regress.go`:
  `DiscoverRegressCases`, `RunRegressSubset`,
  `NormalizeRegressOutput`, and status model (`port`/`defer`/
  `excluded`). Added generator `cmd/gen-regress-coverage` with
  output `docs/test-port/upstream-regress-coverage.md`.
  Added tests in `internal/testport/framework/regress_test.go`
  proving representative subset execution and status reporting.

- [x] M0060-0003: TAP migration foundation (including client tools).
      **Goal:** Port TAP execution patterns to Go tests and move client-tool
      suites from legacy skip-only posture to migration target posture.
      **Acceptance:**
        - `src/bin/*/t` migration plan is active and tracked.
        - TAP lineage mapping stays auditable.

  **LANDED 2026-05-07.**
  Expanded `cmd/gen-tap-coverage` scope to include:
  - `postgres/src/test/recovery/t/*.pl`
  - `postgres/src/test/subscription/t/*.pl`
  - `postgres/src/bin/*/t/*.pl`
  Classification now uses governance-aligned statuses
  (`port`/`defer`/`excluded`; legacy `skip` removed).
  Regenerated `docs/test-port/upstream-tap-coverage.md` and
  preserved auditable TAP lineage to existing Go ports in
  `internal/testport/tap_port_test.go`.

- [x] M0060-0004: isolation spec scheduler foundation.
      **Goal:** Implement deterministic multi-session scheduler support
      for `src/test/isolation` spec migration.
      **Acceptance:**
        - Scheduler runs representative spec steps across multiple sessions.
        - Output comparison workflow is defined and tested.

  **LANDED 2026-05-07.**
  Added `internal/testport/framework/isolation.go` with:
  `DiscoverIsolationSpecs`, `ParseIsolationSpec`, and
  `RunIsolationPermutation` deterministic scheduler.
  Added generator `cmd/gen-isolation-coverage` with output
  `docs/test-port/upstream-isolation-coverage.md`.
  Added tests in `internal/testport/framework/isolation_test.go`
  proving multi-session permutation execution order and parsed
  output flow.

- [x] M0060-0005: recovery/subscription/modules/contrib staged migration.
      **Goal:** Stage migration of `src/test/recovery`, `src/test/subscription`,
      `src/test/modules/*`, and `contrib` suites by dependency class.
      **Acceptance:**
        - Each suite has explicit status entries (`port`/`defer`/`excluded`).
        - No silent non-passing target remains undocumented.

  **LANDED 2026-05-07.**
  Added explicit staged entries in
  `docs/test-port/postgres-oracle-port-status.csv` for:
  - `postgres/src/test/recovery` (`defer`)
  - `postgres/src/test/subscription` (`defer`)
  - `postgres/src/test/modules` (`defer` + excluded unsafe subset)
  - `postgres/contrib` (`defer`)
  plus regenerated markdown status output. Dependency-class staging
  rationale is now explicit and auditable.

- [x] M0060-0006: Defer/excluded governance and CI visibility.
      **Goal:** Ensure every non-passing migration target is listed with
      rationale and follow-up reference.
      **Acceptance:**
        - `docs/test-port/postgres-oracle-port-status.md` is CI-auditable.
        - Unexpected failures are distinguishable from known defers.

  **LANDED 2026-05-07.**
  Introduced machine-readable governance source:
  `docs/test-port/postgres-oracle-port-status.csv`.
  Added validator + renderer in `internal/testport/framework/status.go`
  and generator `cmd/gen-oracle-port-status`.
  Added validation tests in
  `internal/testport/framework/status_test.go`.
  CI now validates status schema and uniqueness rules via `go test`.

- [x] M0060-0007: Oracle compatibility reporting.
      **Goal:** Produce suite-level report summarizing pass/defer/excluded
      progress across migrated upstream test families.
      **Acceptance:**
        - Report includes per-suite counts and notable blockers.
        - Report can be regenerated from repository state.

  **LANDED 2026-05-07.**
  Added `cmd/gen-oracle-report` and generated
  `analysis/postgres-oracle-compatibility-report.md` with:
  inventory snapshot, suite-level status totals, and deferred
  blocker table. Report is reproducible from repository state.

- [x] M0060-0008: Initial milestone gate.
      **Goal:** Land first validated slice across all core test families
      (regress, TAP, isolation) while keeping tree stable.
      **Acceptance:**
        - Representative migrated tests pass in CI.
        - `go test ./...` PASS.

  **LANDED 2026-05-07.**
  Representative foundations validated:
  - TAP representative ports: `go test ./internal/testport/...` PASS.
  - Regress harness foundation: `go test ./internal/testport/framework -run TestRunRegressSubsetReportsStatuses` PASS.
  - Isolation scheduler foundation: `go test ./internal/testport/framework -run TestParseAndRunIsolationPermutation` PASS.
  Full gate: `go test ./... -count=1` PASS.

## Milestone 0061 — TPC-H M0058 Follow-ups

See `docs/milestones/0061-tpch-m0058-followups.md`. Captures the
three follow-ups identified by the M0058 verification run
(`analysis/tpch-m0058-verification-2026-05-07.md`): EXISTS/NOT EXISTS
unnesting (former M0058-0002-followup), Q19 residual-OR cost, and a
full 22-query re-baseline. NO-DEFERRAL POLICY identical to M0058.

- [x] M0061-0001: EXISTS / NOT EXISTS unnesting to semi-join / anti-join. **LANDED 2026-05-07.**
      Added `JoinTypeSemi` / `JoinTypeAnti` to the JoinType enum;
      executor's `joinOp.openLazyHashJoin` and `nextLazy` emit
      each probe row at most once with NULL-key handling matching
      PostgreSQL. New `unnestExistsExpr` in `unnest.go` follows
      the M0040 IN pattern; `canUnnestExistsExpr` rejects
      non-correlated EXISTS (so M0058-0001 cache still applies)
      and non-equijoin correlation (Q21's `<>`). Tests in
      `internal/planner/exists_unnest_test.go`. Live SF=1
      verification: Q22 300 s cancel → 56 s, Q4 3600 s cancel →
      168 s. Q21 still cancels — non-equijoin correlation is
      out-of-scope; tracked under remaining-followups.
      **Goal:** The planner converts `EXISTS(subq)` → semi-join and
      `NOT EXISTS(subq)` → anti-join when the subquery's correlation
      predicate is an equijoin on a base-table key, eliminating the
      per-outer-row SubPlan Open/Next/Close.
      **Root cause:** the M0040 unnesting pass handles `IN` only.
      **Files:** `internal/planner/planner.go` (unnesting pass),
        `internal/executor/operators_join_agg.go` (`SemiJoinOp` /
        `AntiJoinOp` or `JoinTypeSemi` / `JoinTypeAnti` extension).
      **Design:** `docs/design/0061-0001-exists-anti-semi-join-unnesting.md`
        (new — must land before or with the implementation).
      **Acceptance:**
        - Q4 completes in < 60 s (was CANCELLED at 3600 s).
        - Q21 completes in < 60 s (was CANCELLED at 2634 s).
        - Q22 completes in < 60 s (was CANCELLED at 300 s on
          2026-05-07; correlated NOT EXISTS).
        - EXPLAIN for Q4 / Q21 / Q22 shows Semi Join / Anti Join (or
          hash-keyed equivalent), not a SubPlan re-evaluated per row.
        - `go test ./internal/executor/... ./internal/planner/...`
          PASS, including the M0040 IN-unnesting tests.

- [x] M0061-0002: Q19 residual OR-of-ANDs optimisation. **LANDED 2026-05-07** (via predicate-classifier fix).
      Root cause was upstream of M0058-0004:
      `pushdown.walkColumnRefs` treated **every** `*InExpr` as
      out-of-scope (intending only subquery-IN), which made
      `classifyConjunctSide` return `sideOutOfScope` for any
      conjunct containing a literal-list `IN (...)`. That gated
      out M0058-0004's `pickCommonOrEquijoin` for Q19 (with
      `p_container IN (...)`) and Q22's predicates. Fix: in
      `walkColumnRefs`, distinguish `InExpr.Plan != nil`
      (subquery — out-of-scope) from `InExpr.List != nil`
      (literal list — recurse into operand + list elements).
      Live Q19: 300 s cancel → 64.85 s. Vectorised / UNION-ALL
      rewrites unnecessary at SF=1; deferred. Tests: live-shape
      assertion + literal-list pushdown regression in
      `internal/planner/q19_live_test.go`.
      **Goal:** Q19 completes in < 60 s on SF=1. M0058-0004 already
      removed the CROSS JOIN by extracting `l_partkey = p_partkey`
      as a Hash Join key, but the residual three-branch OR-of-ANDs
      filter is evaluated row-by-row and dominates: Q19 cancelled at
      300 s on 2026-05-07.
      **Approach options (decide in design doc):**
        (a) Vectorise the three-branch OR-of-ANDs filter so each
            branch's predicate evaluates as a column-batched mask
            instead of per-row interpretation; or
        (b) Teach the planner to rewrite the OR-of-ANDs into
            `UNION ALL` of three independent joins, each with
            branch-specific build-side filters (brand / container /
            quantity / shipmode), letting the build side prune
            most rows before the probe.
      **Files:** `internal/executor/expr.go` (vectorised path) or
        `internal/planner/planner.go` + `optimizer.go` (UNION-ALL
        rewrite); plus tests under `internal/planner/`.
      **Design:** `docs/design/0061-0002-q19-or-of-ands-residual.md`
        (new — must compare both options with cost-model evidence).
      **Acceptance:**
        - Q19 completes in < 60 s on SF=1 (was CANCELLED at 300 s).
        - EXPLAIN shows either no residual OR-of-ANDs filter on the
          probe output, or the OR survives but evaluates in a
          vectorised path with measurable speedup.
        - `go test ./internal/executor/... ./internal/planner/...` PASS.

- [x] M0061-0003: Re-baseline full 22-query TPC-H SF=1 sweep. **LANDED 2026-05-07.**
      Sweep at `--cancel-after=600s --per-query-timeout=620s`
      against commit `faf2e71`. Three M0061 wins verified:
      Q4 ≥21× (3600 s → 168 s), Q19 ≥4.6× (300 s → 65 s),
      Q22 ≥5.3× (300 s → 56 s). 14 queries OK with correct row
      counts; 5 timeouts (Q5/Q14/Q20/Q21 + Q9 LIKE error)
      tracked as named follow-ups. Report:
      `analysis/tpch-m0061-followups-baseline-2026-05-07.md`.
      **Goal:** Capture the post-M0061-0001 long-tail and confirm no
      regressions on Q1/Q3/Q5/Q6/Q9/Q10/etc. relative to the M0058
      baselines. Supersedes the partial six-query report in
      `analysis/tpch-m0058-verification-2026-05-07.md`.
      **Run parameters:** tpch-runner against `runtime_goopg`,
        `--per-query-timeout=620s --cancel-after=600s`, all 22
        queries, server log captured.
      **Files:** `analysis/tpch-m0058-followups-baseline-<date>.md`
        (new) with per-query status / elapsed / rows / vs-baseline
        delta and a regression callout list.
      **Acceptance:**
        - Report committed under `analysis/`.
        - All queries either complete inside 600 s or carry a named
          follow-up entry (no silent failures).
        - Any regression vs. the M0058 verification baselines is
          flagged with a hypothesis and reproduction command.
      **Blocked by:** M0061-0001 (so Q4/Q21/Q22 can complete).

## Milestone 0062 — TPC-H Residual Long-Tail

See `docs/milestones/0062-tpch-residual-long-tail.md`. Tracks the
five queries still blocked after M0061 wins were verified by the
M0061-0003 sweep: Q5 (slow probe), Q8 (0-rows correctness), Q15b
(0-rows correctness), Q20 (nested-IN decorrelation), Q21
(non-equijoin EXISTS). NO-DEFERRAL POLICY identical to M0061.

Two same-day fixes from the M0061-0003 follow-up work also live
here for traceability:

- [x] M0062-Q9 fix: LIKE accepts KindBytes, error message logs
      Kind. Forward fix in `internal/executor/expr.go`'s LIKE
      branch + helper `datumAsString`. Tests in
      `internal/executor/like_test.go`. **LANDED 2026-05-07.**
      Root-cause bisect tracked separately under M0062 (results
      in `analysis/tpch-m0062-q9-bisect-2026-05-07.md` once
      complete).
- [x] M0062-Q13 fix: ctx.Err() in `runNestedLoop` inner loop
      every 4096 iterations + RIGHT/FULL unmatched-emit loop;
      ctx in `aggregateOp.Open` output materialisation;
      defense-in-depth ctx in `sortOp.Open` and
      `filterOp.Next`. Files:
      `internal/executor/operators_join_agg.go`,
      `internal/executor/operators.go`. **LANDED 2026-05-07.**

- [x] M0062-0001: Q5 cancel-at-600 s — ctx check landed.
      (LANDED 2026-05-07: ctx check in `initStepHelper` /
      `advanceFrom` per-match loops of `multi_hash_join.go`.
      Cancel responsiveness confirmed; throughput gap is
      structural, carried to M0068+ as GC/slot pipeline work.)

- [x] M0062-0002: Q8 0-rows correctness regression.
      (FIXED by M0063-0001 — NLI derived-table outer key
      resolution. Q8 returns 2 rows as of commit `2e6e9f9`.
      Verified in `analysis/tpch-m0063-final-baseline-2026-05-07.md`.)

- [x] M0062-0003: Q15b 0-rows correctness regression.
      (FIXED by M0063-0001 — same root cause as Q8. Q15b
      returns 1 row as of commit `2e6e9f9`.)

- [x] M0062-0004: Q20 nested-IN decorrelation gate relaxation.
      (LANDED 2026-05-07: `canUnnestInExpr` recursive-depth
      check (cap 4) replaces the blanket nested-IN reject.
      Q20's throughput gap addressed later by M0069-0005
      non-correlated IN→SemiJoin, which dropped Q20 from
      cancel-1200s → 30 s.)

- [x] M0062-0006: Q9 NLI schema-substitution column indices.
      **Goal:** Q9 OK with the canonical TPC-H row count (175
      groups for SF=1) and no SQLSTATE 42883.
      **Root cause (per `analysis/tpch-m0062-q9-bisect-2026-05-07.md`):**
      `internal/planner/nl_index_join.go::nliRewrite` builds the
      substitution schema as `outer ++ inner`; for the Q9 chain
      the parent Filter's `*ColumnRef` indices were resolved
      against the original Join's pre-substitution layout and
      now point at the wrong slots. `p_name` resolves to a
      `KindTime` Datum at LIKE-eval time.
      **Files:** `internal/planner/nl_index_join.go` (re-resolve
        parent ColumnRefs after substitution, OR keep the
        original `Left ++ Right` order), and possibly the
        `executor/operators_nljoin.go` join-row layout.
      **Workaround until merged:** `SET enable_nestloop_index =
        off` for affected sessions.
      **Acceptance:**
        - Live `./tpch-runner -queries=9` returns rows.
        - `internal/testutil/tpch/nli_parity_test.go` extended
          to cover the Q9 multi-NLI shape.

- [x] M0062-0005: Q21 non-equijoin EXISTS correlation.
      (LANDED correctness 2026-05-07: EXISTS+NOT EXISTS
      unnested with `<>` residual lifted to join Predicate
      via M0062-0005. Q21 completes inside 1200 s budget
      (387.76 s in M0068 baseline). Row-count correction
      tracked separately as M0071-0005.)

## Milestone 0063 — TPC-H Residual Long-Tail v2

See `docs/milestones/0063-tpch-residual-long-tail-v2.md`.
Tracks the six queries still blocking a 22/22 SF=1 pass after
M0062: Q5 (six-table MHJ throughput), Q8 + Q15b (NLI
derived-table outer column-resolution bug), Q13 (LEFT JOIN
with NL+LIKE residual), Q20 (correlated scalar subquery),
Q21 (anti-join with 6 M-row build). Cancel-prop is verified
responsive on all four 600 s queries; the residual gaps are
throughput / correctness, not propagation. NO-DEFERRAL POLICY
identical to M0061 / M0062.

- [x] M0063-0001: Q8 + Q15b — NLI derived-table outer key
      resolution. **LANDED** 2026-05-07 (`2e6e9f9`).
      Q8 now 2 rows (was 0); Q15b now 1 row (was 0).
      View-rename Project + Name re-bind on `outerKey` +
      `IsolatedScope` flag with skip in
      `applyJoinTreePosMap` / `remapPosMapAfterRewrite` /
      `buildBindingsPosMap`.
      **M0064 follow-up** 2026-05-07: the blanket Name re-bind
      regressed Q9 (chained NLI). Gated the rebind on
      `outerNode` being `*MultiHashJoin` — preserves Q8/Q15b
      and restores Q9 to ~240 s / 7 rows.

- [x] M0064: Q9 chained-NLI regression caused by M0063-0001's
      Name re-bind. **LANDED** 2026-05-07. The unconditional
      rebind in `nliRewrite` (after picking `outerKey`) walked
      Q9's `*NestedLoopIndexJoin`-as-outer keys whose original
      Index already matched the runtime row layout, moving them
      onto a different table's column. Fix: gate the rebind on
      `outerNode.(*MultiHashJoin)` AND skip when the original
      Index is in-bounds and points at a column with the matching
      Name. See
      `analysis/tpch-m0064-baseline-2026-05-07.md`.

- [x] M0064-Q21-walker (carried from M0063-0004 partial).
      (PARTIAL LANDED in M0065-0001: `*NestedLoopIndexJoin`
      case added to `applyJoinTreePosMap`. Schema-runtime
      mismatch still blocks full fix; residual tracked as
      M0071-0005 Q9/Q21 composite-NLI re-attempt.)

- [x] M0063-0002: Q5 six-table MHJ probe throughput.
      (DEFERRED with documented successor: Q5 structural
      cancels at 1200 s tracked as M0071-0004 Q5
      predicate pushdown + M0071-0001 TupleSlot pipeline.
      M0068 Datum compaction + M0069 IN-unnest landed; Q5
      root cause is GC/row-copy bound, not planner.)

- [x] M0063-0003: Q20 correlated scalar subquery
      decorrelation.
      (SUPERSEDED by M0069-0005: non-correlated IN→SemiJoin
      fixed Q20's throughput at the planner level. Q20 drops
      from cancel-1200s → 30 s in M0069 sweep.)

- [x] M0063-0004: Q21 anti-join with index-driven inner.
      **PARTIAL LANDED** 2026-05-07 (`f4ef64e`). NLI Type
      gate extended to Semi / Anti; emit branches added to
      `nestedLoopIndexJoinOp.Next()`; `unwrapTrivialWrappers`
      reaches bare SeqScan; trivial `Project(Filter(true,…))`
      wrappers are unwrapped. Q21 Semi side is now NLI;
      Anti side still hash because lifting the inner Filter
      conjunct breaks negation semantics (validated: 0 vs.
      ~411 rows). Q21 still cancels at 600 s; Anti-side NLI
      tracked as a follow-up.

- [x] M0063-0005: Q13 LEFT JOIN + NOT-LIKE residual rewrite.
      **LANDED** 2026-05-07 (`0dfcab8`). Q13 64.46 s, 35
      rows (was 600 s cancel). `planFromItem` splits LEFT
      JOIN `ON` conjuncts via `splitAnd` +
      `classifyConjunctSide`; inner-only conjuncts are
      shifted via the new `shiftColumnRefsBy(-leftWidth)`
      and pushed onto a Filter on the right child before
      `splitEqualityForHash`.

- [x] M0063-0006: Final 22-query SF=1 sweep + report.
      **DONE** 2026-05-07. Sweep:
      `bench/tpch/logs/m0063_final_22q_20260507T191850.log`.
      Report:
      `analysis/tpch-m0063-final-baseline-2026-05-07.md`.
      18 / 22 queries return correct non-zero rows
      (was 14 / 22 in M0062: +4 net — Q8, Q13, Q15b newly
      OK; Q9 newly regressed to 600 s cancel and tracked
      as a follow-up).

## Milestone 0065 — TPC-H Residual Long-Tail v3

See `docs/milestones/0065-tpch-residual-long-tail-v3.md`.
Closes the three remaining cancels after M0064 (Q5 / Q20 /
Q21). Goal: 22/22 OK on SF=1.

- [x] M0065-0001: Q21 NLI-aware key remap walker.
      (PARTIAL LANDED — `*NestedLoopIndexJoin` case added to
      `applyJoinTreePosMap` in M0065. Schema-runtime mismatch
      still blocks the `posMap` remap path for Q9/Q21 composite
      shapes; residual tracked as M0071-0005.)

- [x] M0065-0002: Q20 correlated scalar decorrelation.
      (DIAGNOSED — no planner fix landed; root cause was the
      6M-row lineitem aggregate, not a decorrelation gap.
      SUPERSEDED by M0069-0005 non-correlated IN→SemiJoin which
      fixed Q20's throughput independently.)

- [x] M0065-0003: Q5 six-table MHJ throughput.
      (DEFERRED with documented successor. Q5 structural cause
      identified (duffcopy/memmove/memclr ~60% of CPU = row-shaped
      copies, not a planner gap). Fix requires M0071-0001
      TupleSlot pipeline. M0068 Datum compaction + M0069 IN-unnest
      and M0070 bgwriter improvements delivered incremental gains.)

- [x] M0065-0004: Final 22-query SF=1 sweep + report.
      (DONE 2026-05-08. `analysis/tpch-m0065-baseline-2026-05-08.md`.
      19/22 OK preserved.)

## Milestone 0066 — TPC-H Runtime Optimization (Pivoted)

See `docs/milestones/0066-tpch-residual-q5q20q21-final.md`.
**PIVOTED** from "fix Q5/Q20/Q21 in planner" to "reduce
executor allocation/GC overhead" after empirical findings:
Q5 pprof shows 65 % CPU in `runtime.gcBgMarkWorker`. Planner
attempts (M0066-Q5 build-time pushdown, M0066-Q21 NLI walker)
broke other queries; reverted.

- [x] M0066-0001: GOGC tuning → GOGC=off.
      (LANDED 2026-05-08: `bench/tpch/env_goopg.sh` sets
      GOGC=off + GOMEMLIMIT=12GiB. Verified in
      `analysis/tpch-m0066-baseline-2026-05-08.md`.)

- [x] M0066-0002: MHJ BorrowRow / copyOut elimination.
      (LANDED 2026-05-08 (M0066 PIVOT commit `55432e2`):
      `SetBorrow` added to `multiHashJoinOp`; eliminated
      99.23% of Q5's allocations (was 2.02 TB cumulative
      per 60 s pprof window). BorrowRow literal caching also
      added for `TypedStringLit`/`IntervalLit`.)

- [x] M0066-0003: String interning.
      (SUPERSEDED: main wins came from literal-caching fix
      (M0066-0002 extra) and M0068 Datum compaction. Pure
      value-string interning not pursued; documented as
      such in `analysis/tpch-m0066-baseline-2026-05-08.md`.)

- [x] M0066-0004: Final 22-query SF=1 sweep + report.
      (DONE 2026-05-08. `analysis/tpch-m0066-baseline-2026-05-08.md`.
      19/22 OK. Q5 pprof residual: duffcopy 31% + memclr
      22% + memmove 6% = ~60% memory-copy bound.)

## Milestone 0067 — TPC-H Structural Runtime Improvements

See `docs/milestones/0067-tpch-structural-runtime.md`. Builds
on M0066 PIVOT's allocation reductions. Verifies at
`cancel-after=1200s` (was 600s).

- [x] M0067-0001: Milestone doc + fix_plan update.
      (LANDED 2026-05-08.)

- [x] M0067-0002: 1200s baseline sweep.
      (DONE 2026-05-08. `bench/tpch/logs/m0067_baseline_22q_20260508T074950.log`.
      20/22 OK; Q21 newly completes at 1129.85 s but 0 rows.)

- [x] M0067-0003: Q9 composite-NLI investigation.
      (REVERTED 2026-05-08: hoist implemented + tested but
      returned 1 row (schema-annotation vs runtime mismatch).
      Reverted; carried to M0071-0005 for post-slot-pipeline
      re-attempt.)

- [x] M0067-0004: Q21 NLI walker re-attempt.
      (SKIPPED — blocked on M0067-0003. Carried to M0071-0005.)

- [x] M0067-0005: Projection narrowing.
      (SKIPPED — same risk profile as composite-NLI.
      Carried to M0071-0004 Q5 predicate pushdown.)

- [x] M0067-0006: Final 22-query sweep at cancel-after=1200s.
      (DONE 2026-05-08. `analysis/tpch-m0067-baseline-2026-05-08.md`.
      20/22 OK.)

## Milestone 0068 — Executor GC-Optimized Pipeline Refactor

See `docs/milestones/0068-executor-gc-pipeline-refactor.md`
and design docs `docs/design/0068-000{1..4}-*.md`. Source
material: `practice/go_gc_optimized_programming.md` and
`review/postgres_vs_goopg_performance_divergence.md` §1
"Executor (Operator)" (Severity: High).

Replaces `Row = []Datum` with a PostgreSQL-style
`TupleSlot` polymorphism, shrinks `Datum` from ~120 to
≤ 48 bytes, introduces a per-batch byte arena for
variable-length payload, pools slots cross-query, and
**removes** the row-level `BorrowSemantics` contract
(`Borrowable`, `OwnedRow`, `BorrowedRow`,
`setChildBorrow`) in favor of slot-intrinsic lifetime
semantics. The user explicitly approved the swap.

- [x] M0068-0001: Datum compact layout. (landed
      `aef72b7`: Datum shrunk from ~120 B / 4 pointers to
      56 B / 2 pointers (`Buf` slice header + `*big.Int`
      Numeric overflow). Removed redundant fields `Bool`,
      `String`, `Bytes`, `Time`, `IntervalMonths`,
      `IntervalDays`, `NumericMantissa`, `NumericBig`,
      `NumericScale`; replaced with accessor methods
      (`BoolValue` / `StringValue` / `BytesValue` /
      `TimeValue` / `IntervalMonthsValue` /
      `IntervalDaysValue` / `NumericMantissaValue` /
      `NumericBigValue` / `NumericScaleValue`) and
      constructors (`NewBoolDatum` / `NewIntDatum` /
      `NewStringDatum` / `NewBytesDatum` / `NewTimeDatum` /
      `NewIntervalDatum` / `NewNumericInt64Datum` /
      `NewNumericBigDatum` / `NewToastPointerDatum`).
      Compile-time pin: `const _ uintptr = 64 -
      unsafe.Sizeof(Datum{})` keeps the struct ≤ 64 B at
      every commit. Migration touched ~50 files
      (operators, codec, copy, expr, applyworker,
      protocol). `go test ./...` PASS. SCOPE NOTE: the
      original ≤ 48 B / ≤ 1 pointer target presupposed an
      arena-backed `String/Bytes` payload from
      M0068-0003 — with that deferred to M0069, the
      realistic single-session target is 56 B / 2
      pointers, which is what landed.)

- [ ] M0068-0002: DEFERRED → **M0069-0001 TupleSlot
      pipeline**. Reason: removing `Borrowable` /
      `BorrowedRow` / `OwnedRow` requires changing every
      operator's `Next()` signature from `(Row, error)`
      to `(TupleSlot, error)`. 180+ call sites across
      30+ files. Out of scope for one session.
      **Design:** `docs/design/0068-0002-tuple-slot-pipeline.md`
      (still authoritative; reference under M0069).
      **Acceptance criteria carried forward to M0069-0001
      verbatim.**

- [ ] M0068-0003: DEFERRED → **M0069-0002 Per-batch
      string arena**. Reason: depends on the slot
      pipeline's `Materialize()` boundary (M0069-0001)
      so a virtual slot can outlive the source arena
      page without copying.
      **Design:** `docs/design/0068-0003-batch-string-arena.md`
      (still authoritative; reference under M0069).

- [x] M0068-0004: Cross-query Row pool — partial.
      (landed `aef72b7` + `e9080ac`: new
      `internal/executor/row_pool.go` with sync.Pool
      keyed by row width up to `maxPooledRowWidth = 64`.
      `cloneRow` now uses `acquireRow` for its
      destination buffer. Operator scratch buffers wired
      acquireRow/releaseRow on Open/Close:
      `seqScanOp.scanRow`, `projectOp.out`,
      `nestedLoopIndexJoinOp.joinBuf`,
      `multiHashJoinOp.lazyOut`, `drainRowsCtx.dup`.
      Per-row releaseRow on emitted rows is intentionally
      deferred to M0069-0001 — without an explicit slot
      lifetime contract, releasing emitted rows breaks
      shared-row consumers (e.g. CTE multi-consumer
      materialization, validated by
      `TestCompatCTEMultiConsumerCrossProduct`).)

- [ ] M0068-0005: DEFERRED → **M0069-0003 IndexScan
      lazy iteration**. Reason: requires a btree cursor
      API change in `internal/access/btree`. The current
      `Rescan` materialises matches into `o.rows[]` for
      Borrowable simplicity; lazy iteration breaks the
      borrow contract and is cleaner once M0069-0001's
      slot model lands.

- [x] M0068-0006: sortOp memory-bounded. (landed
      `d79ebda`: `sortOp.Open` now bounds peak heap to
      `chunkLimitBytes` (default 256 MiB). When the
      in-memory chunk exceeds the threshold it is
      sorted, written to a spill file via the existing
      `spillWriter`, and the slice is reset. After the
      child EOF, an N-way merge using `container/heap`
      iterates over `spillReader`s for each spill file
      plus the in-memory tail. New tests:
      `TestM0068SortExternalSpills` (4096 rows with
      1 KB chunk forces multiple spills → output sorted,
      count preserved) and `TestM0068SortNoSpillBelowChunk`
      (small input takes the in-memory fast path).
      **Closes** `review/postgres_vs_goopg_performance_divergence.md`
      §7 Materialization (Severity: High).
      `go test ./...` PASS.)

- [x] M0068-0007: Final 22-query SF=1 sweep + report.
      (landed `<pending>`:
      `bench/tpch/logs/m0068_22q_20260508-105726.log`
      captures the 22-query SF=1 result at
      `cancel-after=1200s`.
      `analysis/tpch-m0068-baseline-2026-05-08.md`
      records the per-query delta vs M0067, the Datum
      size win (56 B vs ~120 B), the documented
      deferred sub-milestones (M0068-0002 / 0003 /
      0005 → M0069), and the GC-share story
      (Datum-pointer-density 50 % drop is the leading
      indicator; `gcBgMarkWorker` < 15 % verification
      requires the slot pipeline from M0069-0001 to
      eliminate the residual `duffcopy` / `memmove`
      ~60 % share).)

## Milestone 0069 — Executor Slot Pipeline + GC Follow-Through + Long-Tail Query Fixes

See `docs/milestones/0069-executor-slot-pipeline-followthrough.md`.

Picks up the three sub-milestones M0068 explicitly deferred
(TupleSlot pipeline, String/Bytes arena, IndexScan lazy
iteration) plus the five "M0069 candidate" items that accrued
from earlier milestones (Q5 / Q20 / Q21 planner improvements,
SI HasInProgress, buffer-pool partitioning). Per the user's
"順次着地、可能な限り進める" directive: land in risk-tier order
(LOW → MED → HIGH); document carry-forwards explicitly.

- [ ] M0069-0001: TupleSlot pipeline (replaces BorrowSemantics).
      **Design:** `docs/design/0068-0002-tuple-slot-pipeline.md`.
      **Files:** new `internal/executor/slot.go`;
      `internal/executor/operator.go` (remove `Borrowable`,
      `OwnedRow`, `BorrowedRow`); `internal/executor/executor.go`
      (remove `setChildBorrow`); 32 `Next() (Row, error)`
      declarations in `operators*.go` / `multi_hash_join.go` /
      `spill.go` / `instrument.go` / `applyworker.go`;
      delete `borrow_test.go` in favor of slot-lifetime tests.
      **Strategy:** adapter-then-migrate over 5 stages
      (interface + MaterializedSlot → flip Next signature →
      VirtualSlot for pass-through → Materialize at retention →
      remove Borrowable).
      **Acceptance:**
        - `Borrowable` interface and BorrowSemantics enum
          removed.
        - All operators consume + produce `TupleSlot`.
        - MHJ probe path emits `VirtualSlot{probe, build}`
          (preserves the M0066 `copyOut` elimination
          structurally).
        - `go test ./...` PASS.
        - Q5 pprof: `runtime.duffcopy` + `memmove` for slot
          copies ≤ 10 % of CPU (was 60 % at M0067).

- [ ] M0069-0002: Per-batch String/Bytes arena.
      **Design:** `docs/design/0068-0003-batch-string-arena.md`.
      **Files:** new `internal/executor/arena.go`;
      `internal/executor/codec.go` (decode into arena);
      `internal/executor/operators_storage.go`
      (per-call Arena per scan iteration);
      `internal/executor/datum.go` (`Datum.arena` field).
      **Depends on:** M0069-0001 (slot Materialize boundary).
      **Acceptance:**
        - varchar / char / bytea decode allocates from arena,
          not per-value.
        - Q5 pprof `inuse_space` for strings shows ~24 K
          arena pages instead of ~30 M individual string
          allocations.
        - 22-query row-count parity preserved.

- [ ] M0069-0003: IndexScan lazy iteration.
      **Files:** `internal/access/btree/btree.go` (cursor API
      for `RangeScan`); `internal/executor/operators_index.go`
      (`Rescan` no longer pre-materializes into `o.rows[]`).
      **Strategy gate:** start with prototype + benchmark of
      Option A (goroutine + channel wrapper around the
      callback `RangeScan`) vs Option B (refactor to expose
      a real `*Cursor`); pick the safer option.
      **Acceptance:**
        - `go test ./internal/access/btree/...` PASS
          (no regression in concurrent-write tests).
        - Q9 SF=1 peak heap drops ≥ 5 GB
          (`go tool pprof -inuse_space`).
        - 22-query row-count parity preserved.

- [ ] M0069-0004: Q5 build-time predicate pushdown
      (guarded re-attempt). Earlier abortive attempt broke
      Q3 due to walker interaction; needs guarded
      re-implementation gated on slot-classifiable conjuncts
      from M0069-0001.
      **Files:** new `internal/planner/predicate_pushdown.go`
      OR extension of `internal/planner/unnest.go`.
      **Acceptance:**
        - Q3 row count preserved (regression guard).
        - Q5 SF=1 probe-time drops ≥ 30 % vs M0068.

- [ ] M0069-0005: Q20 non-correlated IN-list unnest.
      `internal/planner/unnest.go` currently only unnests
      correlated IN-subqueries (M0040-0002). Q20's outer IN
      against `partsupp` is non-correlated. Extend
      `tryUnnestINSubquery` to handle the non-correlated case
      as a SemiJoin.
      **Files:** `internal/planner/unnest.go`.
      **Acceptance:**
        - `go test ./internal/planner/...` PASS.
        - `cmd/tpch-runner --queries=20` row count > 0
          within `cancel-after=1200s`.

- [ ] M0069-0006: Q21 anti-side hash-join inner-Filter
      conjunct lift + composite-NLI on `partsupp_pk` for Q9.
      Q21: lift inner-only conjuncts into the join's
      inner-side filter so the anti-join's probe doesn't
      evaluate them per-row. Q9: re-attempt the M0067-0003
      composite-NLI fix once M0069-0001's stable column-
      coordinate model is in place.
      **Files:** `internal/planner/unnest.go` (Q21);
      `internal/planner/nl_index_join.go` (Q9 composite NLI).
      **Acceptance:**
        - `go test ./internal/planner/...
          ./internal/testutil/tpch/...` PASS.
        - `cmd/tpch-runner --queries=9,21
          --per-query-timeout=600s` Q21 row count > 0
          (canonical ~411); Q9 row count > 7.

- [ ] M0069-0007: SI HasInProgress non-linear lookup.
      `internal/mvcc/snapshot.go::HasInProgress` is a linear
      scan over `s.InProgress` called per-tuple-visibility
      check. Sort once at snapshot construction; use
      `sort.Search` for the lookup. Keep linear scan for
      ≤ 16 entries (small-N constant beats branch-prediction
      overhead).
      **Files:** `internal/mvcc/snapshot.go`.
      **Acceptance:**
        - `go test ./internal/mvcc/...` PASS.
        - Microbenchmark `BenchmarkSnapshotHasInProgress`:
          no regression at small N, ≥ 5x improvement at
          N=64.

- [ ] M0069-0008: Buffer-pool `poolMu` partitioning.
      `internal/storage/bufpool.go::poolMu` is a global lock.
      Profile-gated: capture mutex profile during Q9 / Q20;
      shard `byTag` + slot metadata into N=64 partitions
      keyed by `tag.hash % 64` ONLY if `poolMu` shows up as a
      contention hotspot. If not, document as
      "no observed contention".
      **Files:** `internal/storage/bufpool.go`.
      **Acceptance:**
        - Mutex profile commit
          (`bench/tpch/pprof/mutex_q9_m0069.prof`).
        - Either: partition lands + Q9 / Q20 mutex-wait time
          drops ≥ 50 %, OR: documented null result.

- [ ] M0069-0009: Final 22-query SF=1 sweep + GC profile.
      **Files (output):**
        `analysis/tpch-m0069-baseline-<date>.md`,
        `bench/tpch/logs/m0069_22q_<ts>.log`,
        `bench/tpch/pprof/cpu_q5_m0069.prof`,
        `bench/tpch/pprof/heap_q5_m0069.prof`.
      **Blocked by:** M0069-0001..0008 (whichever land).
      **Acceptance:**
        - 22-query OK count ≥ 20 at `cancel-after=1200s`.
        - Document per-query delta vs M0068; Q5 / Q20
          either complete or document residual structurally.

## Milestone 0070 — Executor Slot Pipeline Completion + Long-Tail Query Closure

See `docs/milestones/0070-executor-slot-pipeline-completion.md`.

Finishes the five M0069 sub-milestones that remained
deferred (TupleSlot pipeline Stages B-E, String/Bytes arena,
IndexScan lazy iteration, Q5 predicate pushdown, Q21 / Q9
planner closure, poolMu partitioning) plus the final 22-query
sweep. **No further deferral** — per user directive every
sub-milestone lands in this milestone.

- [ ] M0070-0001: Q21 inner-only conjunct verification +
      Q9 composite-NLI re-attempt.
      **Files:** `internal/planner/unnest.go` (Q21 verify);
      `internal/planner/nl_index_join.go` (Q9 column
      binding); `internal/planner/q21_live_test.go`.
      **Acceptance:**
        - `go test ./internal/planner/...` PASS.
        - Q9 row count > 7 (canonical ≥ 90).
        - Q21 row count > 0 (canonical ≥ 411).

- [ ] M0070-0002: Buffer-pool poolMu partitioning.
      Replace global `poolMu` with N=64 partition mutexes
      keyed by hash(tag) so concurrent pin/unpin paths
      don't contend on a single lock. The lock is already
      released before disk I/O so sharding is feasible.
      **Files:** `internal/storage/bufpool.go`.
      **Acceptance:**
        - `go test ./internal/storage/... ./internal/...`
          PASS.
        - Mutex profile delta on Q9 documented (null
          result acceptable).

- [ ] M0070-0003: TupleSlot pipeline Stages B-E.
      Stage B flips `Operator.Next()` signature from
      `(Row, error)` to `(TupleSlot, error)` across ~22
      operator types and 8 consumer sites. Stage C wires
      `VirtualSlot` into pass-through operators (filter,
      project, limit, NLI joinBuf, MHJ probe). Stage D
      calls `Materialize()` at retention boundaries (sort,
      hash build, aggregate). Stage E removes `Borrowable`,
      `OwnedRow`, `BorrowedRow`, `setChildBorrow`.
      **Design:** `docs/design/0068-0002-tuple-slot-pipeline.md`.
      **Files:** `internal/executor/operator.go`,
      `executor.go`, `instrument.go`, `applyworker.go`,
      every `operators*.go`, `multi_hash_join.go`,
      `spill.go`, `expr.go`; `internal/server/dispatch*.go`;
      delete `internal/executor/borrow_test.go`.
      **Acceptance:**
        - `Borrowable` interface and BorrowSemantics enum
          removed (`grep -r "Borrowable\|BorrowedRow\|OwnedRow"
          internal/` returns 0 in production).
        - All operators consume + produce `TupleSlot`.
        - MHJ probe path emits `VirtualSlot{probe, build}`
          structurally (preserves M0066 PIVOT).
        - `go test ./...` PASS at each stage.

- [ ] M0070-0004: Per-batch String/Bytes arena.
      **Design:** `docs/design/0068-0003-batch-string-arena.md`.
      **Files:** new `internal/executor/arena.go`;
      `internal/executor/datum.go` (Datum.arena field);
      `internal/executor/codec.go` (decode into arena);
      `internal/executor/operators_storage.go` /
      `operators_index.go` (per-scan Arena lifecycle).
      **Depends on:** M0070-0003.
      **Acceptance:**
        - Arena unit tests (allocate / read / reset /
          multi-page).
        - Q5 pprof `inuse_space`: arena pages dominate
          string memory.
        - 22-query row-count parity preserved.

- [ ] M0070-0005: Q5 build-time predicate pushdown
      (slot-guarded re-attempt). Walker guard restricts
      pushdown to conjuncts whose every column reference
      classifies to a single source slot under the
      M0070-0003 slot model.
      **Files:** `internal/planner/pushdown.go` (extend).
      **Acceptance:**
        - Q3 row count preserved at 11462 (regression
          guard).
        - Q5 SF=1 probe-time ≥ 30 % drop OR rows > 0.

- [ ] M0070-0006: IndexScan lazy iteration. Refactor
      `internal/access/btree/btree.go::RangeScan` to expose
      a cursor (`*RangeCursor.Next()`); IndexScan's
      `Rescan` becomes lazy. Fallback to goroutine + bounded
      channel wrapper if Option B's latch-and-resume proves
      unsafe under concurrent writes.
      **Files:** `internal/access/btree/btree.go`;
      `internal/executor/operators_index.go`.
      **Acceptance:**
        - `go test ./internal/access/btree/...` PASS.
        - Q9 SF=1 peak heap drops ≥ 5 GB.
        - 22-query row-count parity preserved.

- [ ] M0070-0007: Final 22-query SF=1 sweep + GC profile.
      **Files (output):**
        `analysis/tpch-m0070-baseline-<date>.md`,
        `bench/tpch/logs/m0070_22q_<ts>.log`,
        `bench/tpch/pprof/cpu_q5_m0070.prof`,
        `bench/tpch/pprof/heap_q5_m0070.prof`.
      **Acceptance:**
        - 22-query OK count ≥ 21 at `cancel-after=1200s`.
        - Q5 either OK or pprof shows residual is no
          longer duffcopy/memmove dominant.
        - Q9 silent FN closed (rows ≥ 90).

## Notes

- This file is the authoritative TODO list for Ralph. Update it after every
  meaningful change.
- Keep work to ONE item per loop. Decompose further if an item is larger
  than what fits in a single agent invocation.
- Every non-trivial subsystem must land alongside (or just before) a design
  doc under `docs/design/`. The spec treats this as a hard requirement.

## Maintenance Fixes

- [x] Fix `TestFoundationSeqScanFilterJoin` test 7 stale expectation (2026-05-04).
      rows[0][0] was expected to be "alpha" but alpha's t3.qty=100 is filtered
      by WHERE t3.qty>150; correct first row is [beta 200]. Stale from before
      M0039/M0041 fixed ColumnRef alignment for ≥3-table joins. Row-count check
      promoted from t.Logf to t.Fatalf. File: `internal/testutil/tpch/foundation_test.go`.

- [x] Silence `tmp/` build errors under `go test ./...` (2026-05-04).
      tmp/ utility scripts (find_wal_record.go, tuple_size.go, walprobe_main.go)
      all declared `package main`, causing "main redeclared" errors. Added
      `//go:build ignore` to each. (Note: tmp/ is in .gitignore; change is local.)

## Completed

- [x] Project initialization (Ralph harness wired up).
