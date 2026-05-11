# Milestone 0085 — pg_commit_ts (optional commit timestamps)

**Status:** planned (low priority — PG-optional)
**Depends on:** M0080 (persistence audit)
**Drives:** PostgreSQL `track_commit_timestamp = on` parity —
allows queries against `pg_xact_commit_timestamp(xid)` to map
transaction ids to wall-clock commit times. Used by logical
replication conflict resolution and forensic analysis.

## Context

`pg_commit_ts` is an OPTIONAL PG subsystem (default off).
goopg currently has no notion of per-XID commit timestamps;
this milestone introduces the persistence layer, the
`track_commit_timestamp` GUC, and the WAL records that record
commit timestamps when the GUC is on.

## Required design docs

- `docs/design/0085-0001-commit-timestamps-overview.md`
  (PG-aligned slru-style storage, WAL record format, GUC).

## Tasks

Tasks will be detailed when this milestone is picked up. See the
fix_plan.md note at the top of this file.

## Definition of Done (sketch)

- `track_commit_timestamp = on` causes XACT_COMMIT records
  to carry a wall-clock timestamp.
- `pg_xact_commit_timestamp(xid)` function returns the
  recorded timestamp.
- `pg_commit_ts/` directory persists state across restart.
- WAL replay reconstructs the commit-timestamp map.
