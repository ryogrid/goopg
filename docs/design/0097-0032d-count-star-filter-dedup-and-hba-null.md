# 0097-0032d — `count(*) FILTER` aggregate dedup + `pg_hba_file_rules.error` NULL

Status: accepted
Milestone: M0097-0032 (Port `sysviews` regress test)
Date: 2026-05-25

## Problem

`sysviews.sql` exercises two `pg_*_file_rules` queries of the shape:

```sql
select count(*) > 0 as ok,
       count(*) FILTER (WHERE error IS NOT NULL) = 0 AS no_err
  from pg_hba_file_rules;
```

goopg returned `t | f` (expected `t | t`). Two independent defects combined to
produce that single wrong row.

### Defect 1 — `count(*) FILTER (...)` silently dropped its FILTER (engine-wide)

The query has **two `count(*)` aggregates**: a bare `count(*)` and a
`count(*) FILTER (WHERE error IS NOT NULL)`. The planner deduplicates aggregate
calls by `aggregateCallKey` (`internal/planner/planner.go`) so repeated
aggregates share one runtime slot. That key folded in name, `*`, `DISTINCT`,
and arguments — **but not the `FILTER` predicate**. So both calls hashed to
`count|*`, collapsed onto one (unfiltered) slot, and the second target resolved
to the first. The filtered count therefore reported the *unfiltered* total.

This was not specific to `pg_hba_file_rules` — any query with a bare aggregate
plus a filtered twin (or two twins differing only by predicate) was affected,
e.g. `count(*), count(*) FILTER (WHERE id > 1)` over an ordinary table returned
equal values. A *single* `count(*) FILTER` worked, masking the bug; the
collision needs ≥2 same-name/same-arg aggregates.

A secondary instance of the same class: `buildAggregateCall`'s early returns
for the `count(*)` (star) and zero-arg branches never resolved `fc.Filter` at
all, so even after the key was fixed those paths would carry `Filter == nil`.

### Defect 2 — `pg_hba_file_rules.error` was `""`, not NULL

The canned row stored `""` for the `error` column. The `no_err` test counts
rows where `error IS NOT NULL`; an empty string is NOT NULL, so it counted 1.
The column must be SQL NULL (a parsed rule with no error).

## Fix

`internal/planner/planner.go`:

- `aggregateCallKey` now appends `filter|<parserExprKey(fc.Filter)>` when a
  FILTER is present. The same function is used on both the build side
  (`buildAggregateStage`, `collectAggregateCalls`) and the reference-resolution
  side (`resolveExprAfterAggregate`), so build and lookup keys stay consistent
  (sibling-paths rule).
- `parserExprKey` gained an `*parser.IsNullExpr` case (`isnull:` / `isnotnull:`)
  so two FILTERs differing only by `IS NULL` vs `IS NOT NULL` get distinct keys
  rather than colliding on the `expr:%T` fallback.
- `buildAggregateCall` resolves `fc.Filter` once up front and threads it through
  every return path, including `count(*)` and zero-arg aggregates.

`internal/catalog/catalog.go`:

- The `pg_hba_file_rules` canned row omits its trailing `error` cell. Both the
  planner (`buildVirtualValues`) and executor (`rematerialiseVirtualRows`)
  materialise a missing trailing cell as `NullConst`, so this yields a true SQL
  NULL. The misleading comment (which claimed a `"NULL"` sentinel while storing
  `""`) was rewritten.

## Tests

- `internal/planner/planner_test.go` — `TestAggregateFilterDistinguishedInDedupKey`:
  plans `count(*)` + three differently-filtered `count(*)` over one table and
  asserts four distinct aggregate slots, three of them filtered.
- `internal/catalog/catalog_test.go` — `TestPgHbaFileRulesErrorIsNull`: asserts
  `error` is the last column and the row stops before it (→ NULL).

Verified end-to-end on a live server (port 5599):
`count(*), count(*) FILTER (WHERE id>1), … IS NULL, … IS NOT NULL` →
`3, 2, 2, 1`; both `pg_hba_file_rules` and `pg_ident_file_mappings` `no_err`
queries → `t | t`.

## Result

`sysviews` regress diff **33 → 11** lines. The entire residual is now
`pg_backend_memory_contexts` introspection (TopMemoryContext
`total_bytes >= free_bytes`, the Bump-context `Caller tuples` rows, and the
`CacheMemoryContext` multi-child path check) — a Go-runtime design constraint,
since goopg has no faithful equivalent of PostgreSQL's C memory-context tree.

## Scope note

The FILTER-dedup fix is a general correctness fix for the aggregate planner,
not a `pg_hba_file_rules`-specific patch; `sysviews` merely surfaced it.
