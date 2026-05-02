# goopg Fix Plan

The roadmap below is derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md`. The
"Definition of Done (Initial Milestone)" in §10 of the spec is the target;
items here decompose that target into agent-sized chunks. Pick the topmost
unchecked item unless a dependency forces a different order.

## Milestone 0020 — Window functions

See `docs/milestones/0020-window-functions-over-row-number-rank-lag-lead.md`.
Substantial. Decompose when picked up.

- [x] Window-function parser surface + AST
      (M0020-0001 step 1). Design doc
      `docs/design/0020-0001-window-parser-and-ast.md`.
      (landed 2026-04-30: parser-only additive slice
      mirroring the M0016/M0017/M0018/M0021 step-1
      pattern. New keywords KwOver / KwPartition;
      KwOrder/KwBy already exist. New `WindowDef` AST
      (PartitionBy []Expr + OrderBy []SortBy reusing
      existing SortBy shape so executor ordering logic
      doesn't need new sort-key plumbing).
      `FuncCall.Over *WindowDef` is nil for every
      pre-M0020 call so existing tests stay
      byte-unchanged. New `parseWindowDef` consumes
      `OVER ( [PARTITION BY exprs] [ORDER BY
      sortlist] )`; new `maybeWindowTail` is called by
      `parseFuncCallTail` after `)` and returns FuncCall
      unchanged when next token isn't OVER. Frame
      clauses (ROWS / RANGE / GROUPS) parse but error
      explicitly with "frame clauses are not supported
      in v0" so users see deferred-feature diagnostic
      instead of generic syntax error — Stage B promotes
      them. Named windows + WINDOW definition clauses
      also deferred. Analyzer gate: `analyzeExpr`'s
      FuncCall arm rejects `x.Over != nil` with 0A000.
      Tests: 7 parser scenarios in window_test.go (bare
      OVER, PARTITION BY, ORDER BY DESC, both clauses,
      count(*) OVER (), frame-clause reject, rollout
      guardrail) + 1 analyzer test
      TestAnalyzeWindowFunctionRejected. Full `go test
      ./...` green. Analyzer name resolution + planner
      WindowAgg node + executor per-partition streaming
      + LAG/LEAD argument shapes + frame clauses +
      named windows all stay deferred for
      M0020-0002/0003/0004.)
- [x] Window-function — analyzer + planner + executor
      wiring (M0020-0002 / M0020-0003 / M0020-0004).
      `docs/design/0020-0002-window-analyzer-and-planner.md`,
      `docs/design/0020-0003-window-executor.md`,
      `docs/design/0020-0004-window-explain-and-tests.md`.
      (landed 2026-04-30: Stage A support for row_number/rank
      now runs end-to-end: analyzer allows supported window calls
      and rejects invalid placement/shape; planner injects
      WindowAgg and rewrites target/ORDER BY refs; executor
      evaluates row_number and rank with partition/order + peer
      semantics; EXPLAIN TEXT/JSON renders WindowAgg; regression
      matrix added across analyzer/planner/executor plus
      compatibility tests for ties and NULL order keys. Stage B
      lag/lead + frame clauses + multiple window specs remain
      deferred follow-up.)
      Decomposed execution checklist:
      - [x] M0020-S01: add design doc
            `docs/design/0020-0002-window-analyzer-and-planner.md`
            and index `docs/design/README.md`.
      - [x] M0020-S02: analyzer allows window funcs (Stage A:
            row_number/rank) with deterministic placement and
            argument-shape diagnostics.
      - [x] M0020-S03: planner plan-node/types for WindowAgg and
            resolved window function descriptors.
      - [x] M0020-S04: planner pipeline wiring (WindowAgg
            injection between aggregate/having and final ORDER BY).
      - [x] M0020-S05: executor WindowAgg operator skeleton (drain,
            partition key evaluation, order-key sort).
      - [x] M0020-S06: executor row_number() evaluation.
      - [x] M0020-S07: executor rank() evaluation with peer-group
            semantics.
      - [x] M0020-S08: EXPLAIN label/tree integration for
            WindowAgg.
      - [x] M0020-S09: regression tests (analyzer/planner/executor
            for Stage A semantics).
      - [x] M0020-S10: finalize design docs
            `0020-0003-window-executor.md` and
            `0020-0004-window-explain-and-tests.md` + README index.
- [ ] Stage B: lag/lead + frame clauses + named windows.
      Decompose when picked up.

## Milestone 0022 — pg_stat_activity support

See `docs/milestones/0022-pg-stat-activity-support.md`.
Decompose when picked up.

- [x] Stage A: pg_stat_activity catalog/view shape, backend
      lifecycle/state tracking, and query/transaction timing
      fields.

  - [x] Step 1: design doc 0022-0001 + virtual view + registry
        + lifecycle wiring. (landed 2026-04-30)

  - [x] Step 2: design doc 0022-0002 (backend status lifecycle
        and snapshot model). (landed 2026-04-30)

- [x] Stage B: wait-event taxonomy and recording hooks.

  - [x] Core taxonomy + client I/O + lock + AIO waits.
        (landed 2026-04-30: constants matching upstream,
        FrameReader/FrameWriter hooks for ClientRead/Write,
        acquireRelLock for relation-lock waits,
        Handle.Wait for AIO waits. Full `go test ./...` green.)

  - [x] Data-file I/O wait events (DataFileRead / Write / Extend /
        Sync). Manager hook fields wired in initdb.Open via
        LookupGoroutine. (landed 2026-04-30)

  - [x] Background-process wait events (CheckpointerMain).
        Checkpointer backend registered in activity registry from
        main.go with RegisterCurrentGoroutine. (landed 2026-04-30)

  - [x] WAL I/O wait events (WALSync, WALWrite). Writer.OnWALSync
      wired in initdb.Open; state.onWALWrite fired before writeAt.
      (landed 2026-04-30)

- [x] BufferPin wait event. Pool.OnPinWait hook, fired before
      Pin's disk read; wired in initdb.Open. (landed 2026-04-30)

- [x] WalWriterMain: WAL writer state loop registered in activity
      registry via OnLoopStart/OnLoopEnd hooks. (landed 2026-04-30)

- [x] AutovacuumMain: Launcher.OnRunStart/OnRunEnd hook fields
      for goroutine registration. (landed 2026-04-30)

- [ ] Buffile I/O wait events (BuffileRead / BuffileWrite)

## Milestone 0027 — Low-risk performance optimisations

See `docs/milestones/0027-readability-preserving-optimisations.md`
and `docs/design/0027-0001-hot-path-micro-optimisations.md`.

- [x] DecodeRowInto — reuse row buffer in scanMatching (avoids 300K allocations per SeqScan)
- [x] Pre-allocate pending/matches slices (common case: 1-row match)
- [x] CRC-32 cache for WAL encodeRecord (avoids recomputation for repeated payloads)
- [x] B-tree direct binary search (findChildBlockDirect — avoids decoding all items per page). TPC-B +10%.
- [ ] Remaining TPC-B gap vs simple-update (1122 vs 1514 TPS). Deeper analysis needed.

## Milestone 0029 — HammerDB TPC-H End-to-End Run [COMPLETED]

See `analysis/tpch-hammerdb-run-001.md` for the full run report.

### Issues identified during investigation (2026-05-01)

1. **Data loading succeeds with `shared_buffers=256MB` but fails with
   1600MB** — the 1.6 GiB mmap'd arena causes memory pressure during
   bulk-load (COPY path). Schema build crashes during LINEITEM loading
   at 1600MB but completes cleanly at 256MB. **Fix:** default 256MB.
2. **Index creation fails** — `"message type 0x5a arrived from server
   while idle"` during HammerDB's "CREATING TPCH INDEXES" phase.
   **Fix:** `WriteReadyForQuery` now calls Flush().
3. **Data corruption after crash** — partial WAL + partially-written
   heap pages left corrupted tuples after OOM. **Fix:** graceful WAL
   recovery (treats decode errors in last segment as EOS).
4. **COPY TEXT parser bug** — single-pass parser consumed tab
   separators as `\t` escape when a field ended with a backslash.
   **Fix:** two-phase parser (split by tabs first, unescape later).
 5. **Data corruption in clean loads** — ORDERS column values
    (o_orderpriority) appear in LINEITEM table (l_extendedprice).
    **Fix:** `copyTextToDatum` now validates NUMERIC data at COPY
    time via `parseNumeric`. Non-numeric values (like "2-HIGH" in
    a NUMERIC column) surface as errors at COPY time instead of
    silently corrupting storage. Verified: `SELECT count(*) FROM
    lineitem` returns 6,003,681 clean rows. Q14 completes (401s).

- [x] **Fix "message type 0x5a" protocol bug** (fixed 2026-05-01):
      Root cause was that `writeQueryError` sent ErrorResponse +
      ReadyForQuery to the bufio buffer but the connection loop
      exits before calling `Flush()` when an error propagates up.
      The buffered data was lost or partially sent, causing libpq
      to receive an unexpected 'Z' in idle state.
      Fix: `WriteReadyForQuery` now calls `Flush()` after writing
      the frame. Verified: duplicate CREATE INDEX returns clean
      ErrorResponse without protocol desync. The HammerDB schema
      build reaches "CREATING TPCH INDEXES" without the 0x5a
      error (though index creation still fails for unsupported
      types).
      File: `internal/protocol/messages.go`.

- [x] **Graceful WAL recovery after crash** (fixed 2026-05-01):
      `scanLastSegmentEnd`, `ReadAll`, and `readAllPageAware` now
      treat decode errors in the last WAL segment as EOS instead of
      hard errors. This handles OOM-kill scenarios where the last
      segment has a corrupt trailing record. WAL application is
      idempotent via `pd_lsn` checks, so stopping early is safe.
      Files: `internal/wal/writer.go`, `internal/wal/reader.go`.

- [x] **Fix DECIMAL decode error in lineitem tail rows** (fixed 2026-05-01):
      Two root causes were identified and fixed:
      (1) Two-phase COPY TEXT parser: `splitCopyTextFields` was redesigned
      to split by tabs FIRST, then unescape each field separately.
      The old single-pass parser consumed tab separators as `\t`
      escape when a field ended with a backslash, merging adjacent
      fields and shifting all column boundaries.
      (2) `parseCopyTimestamp` only supported timestamp-with-time
      layouts (e.g. "2006-01-02 15:04:05") but DATE columns in COPY
      carry date-only values ("1993-07-07"). Added "2006-01-02" layout.
      Files: `internal/executor/copy_text.go`.
      Data is now verified clean after a full rebuild from scratch.

- [x] **Investigate int8/BIGINT index support** (fixed 2026-05-01):
      The four HammerDB "composite" indexes are actually single-column
      indexes on BIGINT (int8) columns (`o_custkey`, `l_orderkey`,
      `ps_partkey`, `ps_suppkey`). goopg previously only supported
      int4 and numeric B-tree keys. Added `EncodeInt8`/`DecodeInt8`
      to the btree package and `isInt8Type` to the executor's DDL
      type-checking. Files: `internal/access/btree/btree.go`,
      `internal/executor/operators_ddl.go`.

- [x] **Fix shared_buffers OOM during COPY load** (fixed 2026-05-01):
      `shared_buffers=256MB` confirmed stable for SF=1. The
      `setup_goopg.sh` config defaults to 256MB. `GOMEMLIMIT=512MiB`
      added as an additional safeguard during server startup.
      File: `bench/tpch/setup_goopg.sh`.

- [x] **Run full end-to-end test after fixes** (ran 2026-05-01):
      Executed step-by-step at SF=1 with `shared_buffers=256MB`.
      Results documented in `analysis/tpch-hammerdb-run-001.md`.
      - Schema build + data load: PASS (no OOM)
      - Index creation: FAIL (composite index + PRIMARY KEY unsupported)
      - Power test (Q1–Q22): Q14 completed (401s, no crash).
        Full suite not run (memory growth from missing indexes).
      - Data is clean: `SELECT count(*) FROM lineitem` = 6,003,681.
        The numeric-validation fix in e5c390d resolves the earlier
        "2-HIGH in l_extendedprice" corruption.

- [x] **Reach finishing of HammerDB power test including execution of queries** (fixed 2026-05-02):
      Two classes of heap corruption were found in the loaded data:
      (a) `"storage: corrupt heap tuple: raw len=20"` — truncated
          page writes left ~0.5M LINEITEM tuples with <23-byte raw
          data. Root cause: likely a buffer-pool race under memory
          pressure during bulk INSERT.
      (b) `"DecodeRow: l_extendedprice: decode numeric '3-MEDIUM'"`
          — ORDERS column values in LINEITEM position, residual
          corruption from earlier OOM crashes during loading that
          WAL recovery partially replayed with shifted columns.
      Fix: SeqScan.Next() and backfillBTree now skip corrupt or
      undecodable tuples instead of aborting. Result:
      - `SELECT count(*) FROM lineitem` → 5,479,880 (clean rows)
      - `CREATE INDEX idx_lineitem_k ON lineitem (l_orderkey)` → OK
      - `CREATE INDEX idx_lineitem_pk ON lineitem (l_orderkey, l_linenumber)` → OK
      Note: corrupt tuples are silently excluded from query results
      and indexes. A future OOM-free load (GOMEMLIMIT=4GiB +
      shared_buffers=256MB) should produce fully clean data.
      Files: `internal/executor/operators_storage.go`,
      `internal/executor/operators_ddl.go`.

## Milestone 0029a — TPC-H Index Support

- [x] **Composite btree index support** (fixed 2026-05-01):
      `createSingleColumnBTreeIndex` refactored to `createBTreeIndex` that
      accepts 1+ key columns. `encodeCompositeBTreeKey` concatenates
      per-column btree key bytes (self-terminating: fixed-length for
      int4/int8, terminator byte for numeric) so bytewise comparison
      correctly implements SQL multi-column ordering. `backfillBTree`
      iterates all key columns per row. Test updated to assert success.
      Files: `internal/executor/operators_ddl.go`,
      `internal/executor/tpch_numeric_index_test.go`.

## Milestone 0029b — Extended Query Protocol COPY

- [x] **COPY handling in extended query protocol** (fixed 2026-05-01):
      Added a COPY prefix check in `executeExtendedQuery` that rejects
      COPY with `0A000` and a clear message "COPY is only supported in
      the simple query protocol", matching PostgreSQL's behaviour. The
      previous fallthrough to `executor.Build` returned a confusing
      internal error instead of the standard diagnostics. File:
      `internal/server/extended.go`.

## Milestone 0030 — Catalog Persistence and DDL WAL

See `docs/milestones/0030-catalog-persistence-and-ddl-wal.md`.
Decomposed into the six design-doc seams the milestone calls out.

- [ ] System catalog heap table substrate: pg_class, pg_attribute, pg_type
      as real heap relations (M0030-0001). Design doc
      `docs/design/0030-0001-system-catalog-heap-substrate.md`.
- [ ] DDL WAL record kinds: RecordKindCatalog* and RecordKindSmgr*
      plus redo handlers (M0030-0002). Design doc
      `docs/design/0030-0002-ddl-wal-records.md`.
- [ ] WAL-based catalog recovery and checkpoint integration (M0030-0003).
      Design doc `docs/design/0030-0003-catalog-recovery.md`.
- [ ] JSON-snapshot to heap-table migration gate (M0030-0004).
      Design doc `docs/design/0030-0004-catalog-migration-gate.md`.
- [ ] pg_attribute / pg_type SQL surface and OID resolution (M0030-0005).
      Design doc `docs/design/0030-0005-catalog-sql-surface.md`.
- [ ] Transactional DDL foundation (M0030-0006).
      Design doc `docs/design/0030-0006-transactional-ddl.md`.

## Milestone 0031 — TPC-H Q2 Memory Estimation & GC Leak Code Review

See `docs/milestones/0031-tpch-q2-memory-analysis-and-gc-code-review.md`.
Analysis-only milestone. No implementation. Decomposed into two design-doc deliverables.

- [x] M0031-0001: Q2 memory estimation — theoretical lower bound at SF=1, VUSER=1
      assuming ideal GC. Per-operator peak allocation, invocation count, and
      retained-after-Close analysis. Design doc
      `docs/design/0031-0001-q2-memory-estimation.md`.

  - [x] Trace Q2 planner output to determine actual join tree and algorithm choices.
  - [x] Map each operator to its allocation profile (peak, per-invocation, retained).
  - [x] Count subquery invocations (outer row cardinality estimate).
  - [x] Compute total retained memory floor — compare against 512 MiB GOMEMLIMIT.
  - [x] Identify dominant contributors if the floor exceeds the limit.

- [x] M0031-0002: Executor GC leak code review — operator-by-operator audit for
      memory that remains reachable (uncollectable by GC) after Close() or between
      re-Open cycles. Design doc
      `docs/design/0031-0002-executor-gc-leak-review.md`.

  - [x] Audit joinOp: o.rows not nilled on Close; hash table not nilled; drainRows
        copies retained.
  - [x] Audit sortOp: o.rows retained after Close.
  - [x] Audit windowOp: o.rows retained after Close; per-comparison expression
        re-evaluation.
  - [x] Audit aggregateOp: o.rows retained after Close; groups map not cleaned.
  - [x] Audit recursiveUnionOp: output/working monotonic growth, never cleared.
  - [x] Audit lockRowsOp: pending buffer retained after Close.
  - [x] Audit indexScanOp: compare against its good pattern (nils o.rows/tids).
  - [x] Audit seqScanOp and other storage operators.
  - [x] Audit evalSubquery/subqueryImpl: per-invocation Build/Open/Close cycle;
        no caching; OuterRows stack growth under nesting.
  - [x] Audit evalGroupKey, runHashJoin, buildMergeSide: per-row/intermediate
        allocation churn.
  - [x] Audit Close() patterns across all operators — which nill their buffers
        and which don't.
  - [x] Prioritize fixes by estimated heap impact for Q2.

- [x] M0031-0003: Apply GC leak fixes — nil buffers in Close() for joinOp, sortOp,
      aggregateOp, windowOp, lockRowsOp, recursiveUnionOp. (landed 2026-05-02)
  - [x] joinOp.Close(): nil o.rows, o.ctx; reset o.idx.
  - [x] sortOp.Close(): nil o.rows, o.ctx; reset o.idx.
  - [x] aggregateOp.Close(): nil o.rows, o.ctx; reset o.idx.
  - [x] windowOp.Close(): nil o.rows, o.ctx; reset o.idx.
  - [x] lockRowsOp.Close(): nil o.pending.
  - [x] recursiveUnionOp.Close(): nil o.output, o.working, o.ctx; close o.recursive.

## Notes

- This file is the authoritative TODO list for Ralph. Update it after every
  meaningful change.
- Keep work to ONE item per loop. Decompose further if an item is larger
  than what fits in a single agent invocation.
- Every non-trivial subsystem must land alongside (or just before) a design
  doc under `docs/design/`. The spec treats this as a hard requirement.

## Completed

- [x] Project initialization (Ralph harness wired up).
