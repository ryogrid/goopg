# B-tree External Sort Build and Streaming Uniqueness (M0055)

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| supersedes | — |

## 1. Problem

Current CREATE INDEX flow collects all entries in memory and applies uniqueness checks with large in-memory structures, limiting scalability on bigger datasets.

## 2. Design

### 2.1 Spill-capable spool

- Read heap tuples and emit key/TID records into sorted runs with bounded memory.
- Merge runs into a single ordered stream for bottom-up page builder.

### 2.2 Streaming uniqueness verification

- For unique indexes, check adjacent keys in merge stream.
- Avoid global hash-set state for full-table uniqueness validation.

### 2.3 Builder integration

- Reuse existing bottom-up page construction contract.
- Keep dedup integration for duplicate-key non-unique cases.

### 2.4 Failure and recovery behavior

- Define temporary file lifecycle and cleanup.
- Ensure deterministic rollback on build failure.

## 3. Configuration

- Introduce bounded memory budget for index build sort/spool.
- Keep defaults conservative and safe for current deployment profile.

## 4. Tests

- Large synthetic build with constrained memory budget.
- Unique and non-unique correctness across spill/no-spill modes.
- Deterministic behavior under interruption and restart of build process.

## 5. Acceptance

- Peak CREATE INDEX memory bounded by configured budget.
- Comparable or improved build throughput on large datasets.
- No regressions on existing bulk build correctness matrix.