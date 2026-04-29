# Milestone 0011 — B-tree NUMERIC Key Support

**Status:** planned
**Depends on:** Milestone 0001 (foundational server with B-tree v0),
Milestone 0002 (concurrent B-tree durability / correctness baseline),
Milestone 0003 (HammerDB TPC-H workload coverage and numeric SQL surface).
**Drives:** Unblocking HammerDB TPC-H index-build completion, expanding index
eligibility for NUMERIC/DECIMAL columns, and reducing planner/executor
fallback to sequential scans for numeric predicates.

## Context

Current B-tree index creation in goopg is restricted to `int4` key columns.
The CREATE INDEX path rejects `numeric` / `decimal` with the error message
"btree v0 only supports int4 keys". This behavior blocks end-to-end TPC-H
runs driven by HammerDB.

A concrete failure has been observed when users manually run
`bench/tpch/run_all.sh`:

```text
Vuser 1:Loading ORDERS/LINEITEM...1490000
Vuser 1:Loading ORDERS/LINEITEM...1500000
Vuser 1:ORDERS and LINEITEM Done Rows 1..1500000
Vuser 1:Loading TPCH TABLES COMPLETE
Vuser 1:End:Wed Apr 29 13:32:59 JST 2026
Vuser 1:CREATING TPCH INDEXES
Vuser 1:message type 0x5a arrived from server while idle
Error in Virtual User 1: ERROR:  btree v0 only supports int4 keys, got "numeric" <- here

Vuser 1:FINISHED FAILED
ALL VIRTUAL USERS COMPLETE
TPROC-H Driver Script
1 = FINISH FAILED
```

The same limitation is visible in the DDL executor path
(`internal/executor/operators_ddl.go`, `createSingleColumnBTreeIndex`) and
must be removed safely. Numeric key ordering semantics must remain stable and
PG-compatible enough for index scans, uniqueness checks, and replay/restart
consistency.

## In Scope

### B-tree NUMERIC Key Eligibility

- Extend CREATE INDEX / ALTER TABLE ADD PRIMARY KEY index-build validation so
  `numeric` and `decimal` key columns are accepted by B-tree v0.
- Preserve existing behavior for `int4` keys and current unsupported key
  types.

### Numeric Key Encoding and Comparison

- Introduce a deterministic B-tree key representation for NUMERIC datums that
  preserves numeric ordering across:
  - Different scales for equal values (for example `1.0` and `1.00`).
  - Negative values, zero, and sign changes around zero.
  - Large integral and fractional magnitudes supported by current numeric
    execution semantics.
- Ensure comparator behavior is consistent between index build, insert,
  uniqueness checks, and index scans.

### Backfill and Uniqueness Semantics

- Extend B-tree backfill used by CREATE INDEX so NUMERIC keys are extracted
  from heap tuples and inserted with correct ordering.
- Enforce UNIQUE/PRIMARY KEY semantics for NUMERIC keys using numeric value
  equality semantics (not textual representation equality).

### Planner / Executor Integration

- Ensure planner/executor paths that currently rely on int4 key encoding can
  generate and use NUMERIC index quals for supported operators.
- Preserve correct fallback behavior when predicates are not indexable.

### HammerDB TPC-H Validation

- Add regression/integration coverage that reproduces the failure class from
  `bench/tpch/run_all.sh` and verifies index creation no longer fails due to
  NUMERIC key type rejection.
- Confirm TPC-H index-build stage reaches completion under the documented
  HammerDB flow.

## Out of Scope

- New index access methods beyond B-tree.
- Full multi-column generalization beyond existing B-tree v0 scope if still
  constrained to single-column indexes.
- Locale/collation features unrelated to numeric ordering.
- Changing NUMERIC SQL arithmetic semantics outside index-key handling.
- Performance tuning for very high precision extremes beyond correctness-first
  coverage.

## Required Design Docs

Place under `docs/design/` with sequential numbering at creation time:

- `0011-0001-btree-numeric-key-ordering.md` — NUMERIC key byte layout,
  ordering invariants, and comparison contract.
- `0011-0002-btree-numeric-build-and-uniqueness.md` — DDL validation,
  backfill flow, UNIQUE/PRIMARY KEY behavior, and error handling.
- `0011-0003-hammerdb-tpch-numeric-index-validation.md` — reproduction of the
  current failure, validation matrix, and expected pass criteria for the
  HammerDB run flow.

These design docs should cross-link to:
`docs/design/0003-0004-hammerdb-tpch-integration.md`,
`docs/design/0003-0012-numeric-arithmetic.md`, and
`docs/design/0002-0002-btree-concurrency.md`.

## Reference

Upstream sources to consult:

- `postgres/src/backend/access/nbtree/nbtcompare.c` — B-tree comparison logic
  and operator-class assumptions.
- `postgres/src/backend/utils/adt/numeric.c` — NUMERIC representation and
  comparison behavior.
- `postgres/src/include/catalog/pg_opclass.dat` and
  `postgres/src/include/catalog/pg_amproc.dat` — numeric operator-class and
  support-function mapping for B-tree.

## Definition of Done

1. CREATE INDEX and ALTER TABLE ADD PRIMARY KEY accept `numeric` / `decimal`
   key columns for B-tree v0 where this milestone declares support.
2. The previous failure mode from manual `bench/tpch/run_all.sh` execution
   ("btree v0 only supports int4 keys, got \"numeric\"") is no longer
   reproducible.
3. B-tree build/backfill over NUMERIC data completes successfully and index
   scans return correct ordering/equality behavior for mixed-scale values.
4. UNIQUE/PRIMARY KEY constraints on NUMERIC columns reject duplicate numeric
   values regardless of textual formatting differences.
5. Restart/crash-recovery tests involving NUMERIC-key B-tree indexes pass with
   no ordering corruption.
6. Planner/executor can use NUMERIC B-tree indexes for representative TPC-H-
   style predicates without correctness regression.
7. All required design docs (`0011-0001` to `0011-0003`) are merged with
   status `accepted`.
