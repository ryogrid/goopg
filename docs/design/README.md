# goopg Design Documents

Design documents are **part of the deliverable** for this project, not scratch
notes. Every non-trivial subsystem must land alongside (or just before) its
design doc. See `.ralph/specs/GOAL_AND_REQUIREMENTS.md` §9 for the rules.

## Conventions

- Filenames use the form `<milestone-or-spec-id>-NNNN-short-slug.md`.
  `<milestone-or-spec-id>` is `root` for the foundational requirements spec
  and a milestone identifier such as `0002` for milestone-scoped docs.
  `NNNN` is a zero-padded sequence number within that identifier.
- Reserve a concrete filename before implementation work starts; do not
  leave bare `NNNN-*` placeholders in active plans.
- Each doc opens with a short metadata block: status, date, supersedes.
- Status values: `draft`, `accepted`, `superseded`, `historical`.
- When a new doc supersedes an older one, mark the older doc
  `superseded` and add a `superseded by:` link forward. Do not delete it.
- Cite upstream PostgreSQL with repository-relative paths
  (e.g. `postgres/src/backend/storage/buffer/bufmgr.c`).

## Index

| #    | Title                                         | Status   | Summary                                                                     |
| ---- | --------------------------------------------- | -------- | --------------------------------------------------------------------------- |
| root-0001 | [Architecture Overview](root-0001-architecture-overview.md) | accepted | Single-process Go architecture, upstream-reference policy, reported `server_version`. |
| root-0002 | [Wire Protocol (v0)](root-0002-wire-protocol.md) | accepted | Frame reader/writer, startup-packet parsing, ParameterStatus/BackendKeyData/ReadyForQuery emission, graceful shutdown. |
| root-0003 | [Authentication](root-0003-authentication.md) | accepted | pg_hba.conf parser, policy/matcher, server integration. v0 implements `trust` and `reject`. |
| root-0004 | [Configuration and GUC](root-0004-configuration-and-guc.md) | accepted | postgresql.conf parser, GUC registry, SessionRegistry layering, SHOW/SET/RESET in the simple-query path. |
| root-0005 | [Buffer Manager and Storage Manager (v0)](root-0005-buffer-manager.md) | accepted | mmap'd page arena, smgr per-relation file with O_DIRECT, clock-sweep buffer pool, Pin/Unpin/MarkDirty. |
| root-0006 | [On-Disk Page and Tuple Format (v0)](root-0006-storage-format.md) | accepted | PageHeaderData layout, ItemId encoding, RelFileNode triple, tuple-header field set. |
| root-0009 | [B-tree Index Access Method (v0)](root-0009-btree.md) | accepted | Single-column int4 B-tree on the buffer-pool, page split, range scan. |
| root-0007 | [MVCC Tuple Header and Snapshot Manager (v0)](root-0007-mvcc-and-snapshots.md) | accepted | Heap tuple header with xmin/xmax metadata plus a snapshot manager for READ COMMITTED / REPEATABLE READ visibility. |
| root-0008 | [WAL Writer and Recovery Seam (v0)](root-0008-wal-and-recovery.md) | accepted | Segmented WAL writer, FlushUpTo durability contract, and WAL-before-data integration seam for buffer flushes. |
| root-0015 | [Simple Query Path (v0) and SQLSTATE Strategy](root-0015-simple-query-and-sqlstate.md) | accepted | Interim `SELECT 1` shim, RowDescription/DataRow/CommandComplete encoders, generated `internal/sqlstate`. |
| root-0016 | [VACUUM and ANALYZE (v0)](root-0016-vacuum-and-analyze.md) | accepted | Heap page-level prune driven by the MVCC oldest-xmin horizon, full-scan ANALYZE, REINDEX bridge for B-tree cleanup. |
| root-0010 | [SQL Parser and AST (v0)](root-0010-parser.md) | accepted | Hand-written lexer + recursive-descent parser for the pgbench SQL subset; AST node types mirror upstream parsenodes.h names. |
| root-0011 | [Planner and Catalog Seam (v0)](root-0011-planner.md) | accepted | In-memory catalog interface, logical plan nodes, rule-based single-pass planner mapping each pgbench statement shape to a fixed template. |
| root-0012 | [Executor (v0)](root-0012-executor.md) | accepted | Volcano-style Open/Next/Close iterators, expression evaluator, Datum union, Values/Project/Filter/Limit/Sort + heap operators. |
| root-0013 | [Extended Query Protocol (v0)](root-0013-extended-query-protocol.md) | accepted | Parse/Bind/Describe/Execute/Sync state machine, per-connection statement+portal caches, and SQLSTATE/error choreography. |
| root-0014 | [COPY FROM/TO (v0)](root-0014-copy.md) | accepted | COPY wire-mode state machine, text/binary framing seam, and integration points with executor insert/scan paths. |
| root-0017 | [Data Directory and Server Bootstrap (v0)](root-0017-data-directory.md) | accepted | `goopg init` directory layout (PG_VERSION, base/global/pg_wal/pg_xact, sample conf files); `initdb.Open` runtime bundle; deferred items (on-disk catalog, pg_control). |
| 0002-0001 | [Checkpointing (M0002)](0002-0001-checkpointing.md) | accepted | Production-grade checkpointer: GUCs (`checkpoint_timeout`, `checkpoint_completion_target`, `max_wal_size`, `min_wal_size`, `full_page_writes`), max_wal_size volume trigger, spread/paced writeback, full-page-image WAL records on first dirty per epoch, SQL `CHECKPOINT` verb. |
| 0002-0002 | [Concurrent B-tree (M0002)](0002-0002-btree-concurrency.md) | accepted (L1+L2+L3a) | Plan to remove the global B-tree mutex: L1 RWMutex (read parallelism), L2 per-page latches + Lehman-Yao right-link descent (high keys in BTPageOpaque, format v2), L3a atomic split WAL records (RecordKindBtreeSplit), L3b writer-vs-writer concurrency + page deletion (planned). |
| 0002-0003 | [Logical Redo Records (M0002)](0002-0003-redo-records.md) | draft | Recover from the FPI-everything regression. Adds Pool.MarkDirtyChangeRecord (once-per-epoch FPI baseline + logical records for subsequent dirties), RecordKindHeapInsert + RecordKindBtreeInsert + RecordKindHeapDelete + RecordKindHeapVacuum. Migrated paths: writeHeapRow, btree non-split insert, UPDATE/DELETE xmax stamps, VACUUM page prune. Btree metapage updates and btree internal-node insert remain on FPI-every-dirty until subsequent loops migrate them. pgbench -i WAL ~1.6 GB → ~21 MB; pgbench -t 30 default mixed stays flat. |
| 0003-0002 | [Join Executors (M0003)](0003-0002-join-executors.md) | draft | Hash join as the planner's preferred algorithm whenever the predicate is a single disjoint-side equality. New planner.JoinAlgo + LeftKey/RightKey; splitEqualityForHash + exprSide classify predicates. Executor's joinOp dispatches Open into runHashJoin (build right, probe left, NULL keys never match) or runNestedLoop (universal fallback). INNER + LEFT supported by hash; RIGHT/FULL/CROSS stay on nested-loop. |
| 0003-0004 | [HammerDB TPC-H Integration (M0003)](0003-0004-hammerdb-tpch-integration.md) | draft | TPC-H DDL runs end-to-end. Adds NUMERIC/DECIMAL codec (varlen text round-trip; integer→string formatting for INSERTs), decimal / scientific literal lexer (TokenNumericLit + parser.NumericConst), text→numeric assignability for HammerDB-shape multi-row INSERTs, `to_timestamp(text, fmt)` builtin, and `ALTER TABLE … ADD FOREIGN KEY` syntax (enforcement no-op). All eight HammerDB CREATE TABLEs and FK ALTERs verified via psql 18.3. |
| 0003-0005 | [CASE Expressions (M0003)](0003-0005-case-expressions.md) | draft | Both searched (`CASE WHEN cond THEN result …`) and simple (`CASE expr WHEN val THEN result …`) forms. parser.CaseExpr + planner.CaseExpr + executor evalCaseExpr; first-match-wins, NULL-is-not-matched. analyzer's analyzeCaseExpr unifies branch types same-or-compatible-else-unknown. Required for TPC-H Q1, Q12, Q14. |
| 0003-0006 | [Date / Interval Arithmetic (M0003)](0003-0006-date-interval-arithmetic.md) | draft | `date 'YYYY-MM-DD'` / `timestamp 'YYYY-MM-DD HH:MM:SS'` typed-string-literal sugar (parser.TypedStringLit) and `interval 'N' unit` (parser.IntervalLit, units day/month/year). New Datum KindInterval with months+days fields. evalBinary handles `time ± interval` via Go's time.AddDate (upstream-aligned month overflow). EXTRACT(field FROM ts) added with year/month/day/hour/minute/second/dow/doy/quarter/epoch fields. Required for TPC-H Q1/Q4/Q5/Q6/Q7/Q8/Q9/Q12/Q14/Q15/Q20. |
| 0003-0007 | [EXPLAIN (M0003)](0003-0007-explain.md) | draft | Parser KwExplain + parser.ExplainStmt wrap an inner stmt; planner.Explain wraps the planned inner Node; executor's explainOp pre-order walks the tree and emits one row per node as a single-column QUERY PLAN text result. Hash-join vs nested-loop visible in the label. Wire layer reports CommandComplete tag "EXPLAIN". Options/ANALYZE/JSON deferred. |
| 0003-0008 | [Subqueries (M0003)](0003-0008-subqueries.md) | draft | Scalar uncorrelated subqueries — `(SELECT …)` in expression position. Plus `[NOT] IN (subquery \| val_list)` and `[NOT] EXISTS (subquery)`. parser.SubqueryExpr / InExpr / ExistsExpr → planner mirrors → executor evalSubquery / evalInExpr / evalExistsExpr. Three-valued NULL semantics for IN; multi-column / multi-row violations report 42601 / 21000. Correlated subqueries (parameter pull-up) deferred. |
| 0003-0009 | [Views (M0003)](0003-0009-views.md) | draft | `CREATE [OR REPLACE] VIEW name [(col_list)] AS SELECT …` and `DROP VIEW [IF EXISTS]`. Catalog stores the view's parser AST; planScanRangeVar plans the inner SELECT recursively at every reference. Column types flow from the inner plan; column names from explicit aliases or target-list inference. Required for HammerDB TPC-H Q15. DML on views, catalog persistence, recursive views deferred. |

Append new rows in numeric order. Do not reorder.
