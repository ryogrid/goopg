# 0097-0032c — `COLLATE` expression postfix + complete `pg_wait_events` types

**Milestone:** M0097-0032 (Port `sysviews` regress test)
**Date:** 2026-05-25
**Status:** Implemented

## Problem

The `sysviews` regress test's wait-event query failed outright:

```sql
select type, count(*) > 0 as ok FROM pg_wait_events
  where type <> 'InjectionPoint' group by type order by type COLLATE "C";
```

Two independent defects:

1. **Parser:** `ORDER BY type COLLATE "C"` raised
   `syntax error at or near "... (got collate)"`. goopg's expression parser
   had no `COLLATE` production; the keyword was only consumed ad-hoc in a
   handful of DDL / `ON CONFLICT` target contexts (`internal/parser/ddl.go`,
   `dml.go`), never in general expression position. Any `a_expr COLLATE name`
   — `ORDER BY`, target list, `WHERE` — errored.

2. **Catalog:** even once parsed, goopg's `pg_wait_events` virtual table listed
   only **6** wait-event types (Activity, Client, IO, Lock, LWLock, Timeout).
   PG 18 emits **9** (adds **BufferPin, Extension, IPC**), so the `GROUP BY type`
   returned 6 rows where the expected output has 9.

## Fix

### 1. `COLLATE` as a high-precedence postfix (`internal/parser/select.go`)

PG's grammar treats `COLLATE` as `a_expr COLLATE any_name`, a postfix binding
tighter than the arithmetic/comparison operators. goopg has no non-default
collation machinery, so the parser now **consumes and discards** the collation
reference, leaving the operand expression unchanged. This is exactly correct for
`"C"` / `"POSIX"` (byte order == Go's native string comparison) and matches the
collation-skipping the codebase already did in narrower contexts.

The handler sits in `parseExprPrec`'s unconditional postfix section, alongside
`::` and `[...]`, so it applies everywhere an expression appears. A new helper
`skipCollationName` consumes a possibly schema-qualified name
(`"C"`, `pg_catalog."C"`, `en_US`).

### 2. Missing wait-event types (`internal/catalog/catalog.go`)

Added canonical PG 18 rows for the three absent types (names/descriptions from
`postgres/src/backend/utils/activity/wait_event_names.txt`):

- `BufferPin` / `BufferPin`
- `Extension` / `Extension`
- `IPC` / `AppendReady`, `BackendTermination`, `BgWorkerShutdown`, `BgWorkerStartup`

The query only checks `count(*) > 0` per type, so one row per type suffices; the
IPC set has a few representative members for realism.

## Verification

- `sysviews` wait-event query now returns the exact 9 expected rows (verified
  end-to-end against a live goopg server on port 5533).
- `COLLATE` confirmed working in `ORDER BY`, target list, and `WHERE` (byte-order
  sort `A < a < b`).
- Tests: `TestParseCollatePostfix` (`internal/parser/select_test.go`),
  `TestPgWaitEventsCoversAllTypes` (`internal/catalog/catalog_test.go`).
- `internal/parser`, `internal/catalog`, `internal/planner` all green;
  `internal/executor` clean except the pre-existing, unrelated
  `TestAnalyzeRespectsStatsTarget` ANALYZE-sampling failure (documented in
  fix_plan; fails identically on clean HEAD).

## Known limitations

- Collation references are discarded, not honored. Non-`C`/`POSIX` collations
  do not change comparison semantics — a project-wide simplification, not new
  to this change.
- `sysviews` still has remaining gaps (`pg_backend_memory_contexts` Go-runtime
  introspection, `pg_hba_file_rules` FILTER) tracked under M0097-0032.
