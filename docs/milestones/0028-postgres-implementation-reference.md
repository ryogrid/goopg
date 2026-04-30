# Milestone 0028 — goopg Implementation Reference: PostgreSQL Logic Documentation

**Status:** planned
**Depends on:** All preceding milestones (M0007–M0027) — every shipped feature is a candidate for documentation.
**Drives:** A browsable collection of English Markdown documents under `docs/reference/` that explain how each goopg component works and how the equivalent logic is implemented in PostgreSQL. The goal is to make the codebase more approachable for new contributors and to surface design differences that may indicate optimisation opportunities or correctness gaps.

## Context

goopg implements a PostgreSQL-compatible subset of SQL and storage
engine features. For each implemented feature, the corresponding
PostgreSQL implementation approach differs in ways that range from
superficial (different data structures) to fundamental (different
execution model). Understanding these differences helps:

- New contributors ramp up faster.
- Reviewers spot correctness issues.
- Performance investigators identify architecture-level improvements.

This milestone produces reference documents — one per component
area — that describe both the goopg approach and the PostgreSQL
approach at the logic/architecture level.

## Document Format

Each document follows a consistent template:

```markdown
# Component Name

## Overview
(What this component does, at a high level.)

## goopg Implementation
(Key types, functions, data flow, concurrency model.)

## PostgreSQL Implementation
(Corresponding types, functions, data flow in upstream PG.
References to source file paths under `postgres/src/`.)

## Key Differences
(Design decisions that differ, and why goopg chose its approach.)

## Potential Optimisations or Corrections
(Gaps or improvements identified during documentation.)
```

## Document List (Alphabetical, No Upper Limit)

The following documents are planned. New documents can be added
as needed — there is no upper bound.

| Doc ID | Title | Covers goopg components |
|--------|-------|------------------------|
| REF-001 | AIO Subsystem | `internal/aio/` |
| REF-002 | B-Tree Index | `internal/access/btree/` |
| REF-003 | Buffer Pool | `internal/storage/bufpool.go`, `internal/storage/smgr.go` |
| REF-004 | Checkpointer | `internal/wal/checkpointer.go` |
| REF-005 | CTE / WITH Clause | `internal/planner/with.go`, `internal/analyzer/` |
| REF-006 | EXPLAIN / ANALYZE | `internal/executor/operators_explain.go`, `internal/planner/` |
| REF-007 | Heap Storage & MVCC | `internal/storage/heap.go`, `internal/mvcc/` |
| REF-008 | Lock Manager | `internal/lockmgr/` |
| REF-009 | Logical Replication | `internal/wal/pgoutput.go`, logical replication paths |
| REF-010 | Parser & AST | `internal/parser/` |
| REF-011 | Planner & Optimiser | `internal/planner/` |
| REF-012 | PL/pgSQL Runtime | `internal/plpgsql/`, `internal/executor/plpgsql_runtime.go` |
| REF-013 | Tuple Format & Codec | `internal/storage/heap.go`, `internal/executor/codec.go` |
| REF-014 | UPSERT (ON CONFLICT) | `internal/executor/operators_upsert.go` |
| REF-015 | WAL Format & I/O | `internal/wal/format.go`, `internal/wal/writer.go` |
| REF-016 | WAL Buffer & Eviction | `internal/wal/wal_buffer.go` |
| REF-017 | WAL Redo / Crash Recovery | `internal/wal/recovery.go`, `internal/wal/stream_replayer.go` |
| REF-018 | Window Functions | `internal/executor/operators_window.go` |
| REF-019 | pg_stat_activity & Wait Events | `internal/activity/`, wait-event hooks |
| REF-020 | Row Locking (FOR UPDATE) | `internal/executor/operators_lockrows.go` |
| REF-021 | Protocol & Wire Format | `internal/protocol/` |
| REF-022 | Session & Transaction Management | `internal/mvcc/`, `internal/server/` |
| REF-023 | Autovacuum | `internal/autovacuum/` |

## Definition of Done

1. The `docs/reference/` directory contains at least one document
   per component area listed above.
2. Every document follows the template (Overview, goopg
   Implementation, PostgreSQL Implementation, Key Differences,
   Potential Optimisations).
3. At least 5 documents are created in the initial pass.
4. `go test ./...` remains green (documentation-only milestone).
5. `docs/reference/README.md` lists all documents with brief
   descriptions.

## Reference

- PostgreSQL source tree: `postgres/src/`
- goopg source tree: `internal/`
- Existing reference docs: `docs/reference/wait-events.md`
