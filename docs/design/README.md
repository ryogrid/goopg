# goopg Design Documents

Design documents are **part of the deliverable** for this project, not scratch
notes. Every non-trivial subsystem must land alongside (or just before) its
design doc. See `.ralph/specs/GOAL_AND_REQUIREMENTS.md` §9 for the rules.

## Conventions

- Filenames use the form `NNNN-short-slug.md`, where `NNNN` is a zero-padded
  sequence number assigned at creation time and never reused.
- Each doc opens with a short metadata block: status, date, supersedes.
- Status values: `draft`, `accepted`, `superseded`, `historical`.
- When a new doc supersedes an older one, mark the older doc
  `superseded` and add a `superseded by:` link forward. Do not delete it.
- Cite upstream PostgreSQL with repository-relative paths
  (e.g. `postgres/src/backend/storage/buffer/bufmgr.c`).

## Index

| #    | Title                                         | Status   | Summary                                                                     |
| ---- | --------------------------------------------- | -------- | --------------------------------------------------------------------------- |
| 0001 | [Architecture Overview](0001-architecture-overview.md) | accepted | Single-process Go architecture, upstream-reference policy, reported `server_version`. |
| 0002 | [Wire Protocol (v0)](0002-wire-protocol.md) | accepted | Frame reader/writer, startup-packet parsing, ParameterStatus/BackendKeyData/ReadyForQuery emission, graceful shutdown. |
| 0003 | [Authentication](0003-authentication.md) | accepted | pg_hba.conf parser, policy/matcher, server integration. v0 implements `trust` and `reject`. |
| 0004 | [Configuration and GUC](0004-configuration-and-guc.md) | accepted | postgresql.conf parser, GUC registry, SessionRegistry layering, SHOW/SET/RESET in the simple-query path. |
| 0005 | [Buffer Manager and Storage Manager (v0)](0005-buffer-manager.md) | accepted | mmap'd page arena, smgr per-relation file with O_DIRECT, clock-sweep buffer pool, Pin/Unpin/MarkDirty. |
| 0006 | [On-Disk Page and Tuple Format (v0)](0006-storage-format.md) | accepted | PageHeaderData layout, ItemId encoding, RelFileNode triple, tuple-header field set. |
| 0009 | [B-tree Index Access Method (v0)](0009-btree.md) | accepted | Single-column int4 B-tree on the buffer-pool, page split, range scan. |
| 0007 | [MVCC Tuple Header and Snapshot Manager (v0)](0007-mvcc-and-snapshots.md) | accepted | Heap tuple header with xmin/xmax metadata plus a snapshot manager for READ COMMITTED / REPEATABLE READ visibility. |
| 0008 | [WAL Writer and Recovery Seam (v0)](0008-wal-and-recovery.md) | accepted | Segmented WAL writer, FlushUpTo durability contract, and WAL-before-data integration seam for buffer flushes. |
| 0015 | [Simple Query Path (v0) and SQLSTATE Strategy](0015-simple-query-and-sqlstate.md) | accepted | Interim `SELECT 1` shim, RowDescription/DataRow/CommandComplete encoders, generated `internal/sqlstate`. |
| 0016 | [VACUUM and ANALYZE (v0)](0016-vacuum-and-analyze.md) | accepted | Heap page-level prune driven by the MVCC oldest-xmin horizon, full-scan ANALYZE, REINDEX bridge for B-tree cleanup. |
| 0010 | [SQL Parser and AST (v0)](0010-parser.md) | accepted | Hand-written lexer + recursive-descent parser for the pgbench SQL subset; AST node types mirror upstream parsenodes.h names. |

Append new rows in numeric order. Do not reorder.
