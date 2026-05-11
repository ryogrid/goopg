# Milestone 0083 — pg_multixact + multi-row locking metadata

**Status:** planned
**Depends on:** M0021 (tuple-level locking), M0080 (persistence
audit)
**Drives:** PostgreSQL `pg_multixact` parity — required for
SELECT FOR UPDATE / FOR SHARE under concurrent multi-row
locking, FK row-level lock coordination, and the
`XLOG_HEAP2_LOCK_UPDATED` WAL record kind.

## Context

goopg currently supports single-row SELECT FOR UPDATE via
`RecordKindHeapLock`, but multi-row lock chains (multiple
transactions locking overlapping row sets, FK enforcement
in a multi-row UPDATE) require PostgreSQL's MultiXactId
concept: a "transaction id" that represents a set of
transactions sharing a lock on the same tuple. PG persists
these in `pg_multixact/offsets` and `pg_multixact/members`.

## Required design docs

- `docs/design/0083-0001-multixact-overview.md`
  (multixact id allocation, member set encoding, persistence
  format, wraparound prevention).
- `docs/design/0083-0002-multixact-wal-records.md`
  (`XLOG_HEAP2_LOCK_UPDATED`, `XLOG_MULTIXACT_CREATE_ID`,
  `XLOG_MULTIXACT_ZERO_OFF_PAGE`,
  `XLOG_MULTIXACT_ZERO_MEM_PAGE`,
  `XLOG_MULTIXACT_TRUNCATE_ID`).

## Tasks

Tasks will be detailed when this milestone is picked up. See the
fix_plan.md note at the top of this file.

## Definition of Done (sketch)

- Concurrent SELECT FOR SHARE on the same row from two
  transactions produces a multixact-stamped xmax.
- `pg_multixact/offsets` and `pg_multixact/members` persisted
  across restart.
- Lock release after commit/abort updates the multixact state
  correctly.
- WAL records cover every state transition; replay reproduces
  the multixact tables.
