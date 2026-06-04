# Practice card — catalog / DDL / pg_* views

**Load when** the task touches `internal/catalog/`, DDL (CREATE/ALTER/DROP),
`pg_class`/`pg_attribute`/other system catalogs, views, functions, or
constraints.

**Why:** catalog shape and DDL semantics are PG-compatibility surfaces; subtle
mismatches break client tools and dependency tracking silently.

## Must-run gate

- Run the catalog/parser package tests plus any regress tests that exercise the
  DDL (`internal/catalog/...`, relevant `internal/testport` regress entries).
- After **catalog tuple-format** changes, re-init the data dir and re-run the
  full regress suite — format drift surfaces in unrelated tests
  (see [[codec-storage-change]]).
- Decode catalog rows **header-driven**, not via bare `DecodeRow` assumptions
  ([[analyze_stats_target_test_failing_at_head]]).

## Known traps

- **View → constraint dependency tracking:** CREATE VIEW validation needs the
  view→underlying-object dependencies recorded, or functional-dependency / GROUP
  BY validation (42803) misbehaves; the 42803-only fix is partial — full
  dependency tracking is the remaining work
  ([[m0097_functional_deps_create_view_validation]]).
- **Error codes must match PG exactly** (SQLSTATE). Clients gate on them; an
  almost-right code is a compatibility bug.
- **System-catalog visibility** under MVCC: DDL must make new catalog rows
  visible to the right snapshots (interacts with [[wal-replication-change]]).

## Oracle

Mirror upstream catalog layout and DDL semantics from `./postgres/` (cite the
file). Validate against vanilla PG 18.3 — `psql \d`, `\df`, `\dv`, and
`information_schema` queries are cheap parity checks.

## If you must defer

A partial fix (e.g. error-code-only) is still incomplete — record the remaining
dependency-tracking work in the deferral ledger with a resume point.
