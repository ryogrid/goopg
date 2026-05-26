# Milestone 0114 — pg_internal.init Relcache Fast-Start Cache for goopg

**Status:** planned
**Filed:** 2026-05-26
**Depends on:** M0106 (pg_internal.init PG-standby compat, accepted), M0111 (heap-based catalog recovery, accepted)
**Reference plan:** `.ralph/fix_plan.md` (M0114 section)

## Problem

On every startup goopg scans all pages of `pg_class` and `pg_attribute` to
rebuild its in-memory catalog (`catalog.InMemory`).  For TPC-H SF=1 the
catalog is small so the scan is fast, but as the number of user tables and
columns grows the scan cost grows linearly.

PostgreSQL avoids this cost with `pg_internal.init` — a binary file written
at the end of every successful startup (or on DDL commit via relcache
invalidation) that caches the fully-decoded relcache descriptors.  On the next
startup PG reads `pg_internal.init` instead of scanning `pg_class` /
`pg_attribute`; if the file is absent or stale (e.g. after a crash), it falls
back to the heap scan.

goopg already writes `pg_internal.init` for PG standby compatibility (M0106)
but does not read it for its own startup.

## Goal

Implement reading of `pg_internal.init` during goopg startup as a fast-path
alternative to the `pg_class` / `pg_attribute` heap scan.  If the file is
present and valid (version tag + checksum match), load the in-memory catalog
from it and skip the heap scan.  If the file is absent, stale, or corrupt,
fall back to the existing heap scan and regenerate the file at end-of-startup.

## Motivation

- **Startup latency**: avoids O(N-pages) I/O for large schemas; important as
  goopg clusters grow beyond TPC-H SF=1 scale.
- **PG alignment**: PG's startup fast-path relies on `pg_internal.init`; goopg
  should use the same mechanism rather than always paying the full heap scan.
- **Cache coherence**: because goopg already manages `pg_internal.init`
  invalidation on DDL commits (M0106), adding the read path completes the
  full cache lifecycle without introducing new invalidation machinery.

## Key design areas

- Parsing the binary `pg_internal.init` format that goopg currently writes
  (M0106 defined the write format; the read path must be symmetric).
- Validating the file: magic number, version, checksum; detecting staleness
  relative to `pg_control` LSN or checkpoint counter.
- Populating `catalog.InMemory` from the decoded relcache entries, including
  column types, OIDs, and schema names.
- Fallback handling: any parse/validation error silently falls through to the
  heap scan; the file is deleted and rewritten after the heap scan succeeds.
- Testing: verify that a file written by one startup is correctly read by the
  next; verify that a corrupted/truncated file triggers the fallback without
  error.

## Out of scope

- Per-backend `pg_internal.init` (PG creates one per database OID subdirectory;
  goopg can start with a single global file).
- Encoding extended catalog fields not currently in `catalog.InMemory`
  (e.g. per-column collation, storage parameters) — add only what the
  in-memory catalog already represents.
