# Milestone 0116 — Multi-Column Index-Only Scan Key Decoding

**Status:** planned
**Filed:** 2026-05-26
**Depends on:** M0046 (Index-Only Scan foundation, accepted)
**Reference design:** `docs/design/mvcc-optimize/0116-0001-multi-column-ios.md`

## Problem

PostgreSQL's Index-Only Scan (IOS) avoids heap fetches entirely when the
Visibility Map confirms all tuples on a page are visible, and all projected
columns are available in the index key. This is the second half of the
Visibility Map optimization: the VM makes it safe to skip the heap; the index
provides the column values directly.

goopg implements IOS (`internal/executor/operators_indexonly.go`) but
`decodeRowFromKey` currently only handles a **single-column** index key:

```go
// operators_indexonly.go:145
if len(key) != 1 {
    return nil, fmt.Errorf("index-only scan: multi-column key decode not supported yet")
}
```

Tables with composite primary keys or multi-column unique constraints cannot
use IOS. Examples from the TPC-H schema: `orders (o_orderkey)` is
single-column and works; `lineitem (l_orderkey, l_linenumber)` has a composite
key and falls back to heap fetches today, negating the Visibility Map benefit.

## Goal

Extend `indexOnlyScanOp.decodeRowFromKey` to decode B-tree keys that carry two
or more column values, making the Visibility Map optimization available to
composite-key tables.

## Scope

- Extend or add decoders for all key type families used in practice:
  `int4`, `int8` (already supported); `varchar`/`char`, `timestamp` (already
  supported); `float8`, `date`, `bool`, `name` (to be added in M0116-0001,
  as they are not currently decoded even for single-column IOS).
- Multi-column decode follows the same alignment and encoding rules used by
  the B-tree key encoder (`internal/storage/btree_key.go` or equivalent).
- Planner: when the IOS is chosen for a relation, verify that all output
  columns referenced by the query are present in the index key in declaration
  order. Emit `IndexOnlyScan` only if the key covers all projected columns.
- Fallback: if any projected column is missing from the index key, emit
  `IndexScan` (heap fetch) as today.

## Out of Scope

- Covering index expressions (e.g., `CREATE INDEX ON t ((a + b))`).
- Partial indexes.
- `INCLUDE` columns (non-key columns stored in the index leaf); tracked as
  a separate enhancement once the core multi-column path works.
- Changes to the B-tree page format or key encoding.

## Sub-Milestones

- **M0116-0001** — Extend `decodeRowFromKey` to iterate over all columns in
  the key, dispatching each via the same per-type decoder used for the
  single-column path.
- **M0116-0002** — Planner: `planIndexOnlyScan` checks that every column in
  the `SELECT` target list is available in the composite index key; falls back
  to `IndexScan` if any column is missing.
- **M0116-0003** — Integration tests: 2-column and 3-column composite PK
  tables, mixed types (int + text), IOS chosen for covering queries, heap
  fallback for non-covering queries.
- **M0116-0004** — Regression check: existing single-column IOS tests
  (`TestIndexOnlyScan*`) still pass without modification.

## Definition of Done

- [ ] `decodeRowFromKey` handles keys with two or more columns without
  returning an error.
- [ ] The planner emits `IndexOnlyScan` for composite-key tables when the
  query projects only index-covered columns.
- [ ] The planner falls back to `IndexScan` when any projected column is absent
  from the index key.
- [ ] `go test ./internal/executor/... -run TestIndexOnly` passes for
  single-column and multi-column cases.
- [ ] A dedicated integration test covering `lineitem`-style (int4, int4)
  composite key IOS passes end-to-end.
- [ ] No regression in pgbench select-only TPS vs. pre-milestone baseline.
