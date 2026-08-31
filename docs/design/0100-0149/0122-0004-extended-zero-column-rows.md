# 0122-0004 — Zero-column result sets over the extended query protocol

**Status:** accepted
**Milestone/bucket:** M0122-0004 (SQL language / executor features); closes
`unimplemented_feat.json` "goopg does not emit a blank data row for 0-column
SELECT results".

## Problem

PostgreSQL treats a *zero-column* read as a real result set: `SELECT FROM t`
returns one zero-column `DataRow` per source row, `SELECT;` returns exactly one
zero-column row, and `Describe` on such a portal replies with a
`RowDescription` carrying **0 fields** (not `NoData`). Verified live against
PostgreSQL 18.3:

```
SELECT FROM t         -- 3 source rows -> (3 rows)   [simple and \bind \g]
SELECT FROM t WHERE id>=2                 -> (2 rows)
SELECT;                                    -> (1 row)
```

goopg's **simple-query** path (`internal/server/dispatch.go`) already handled
this correctly — it gates both the `RowDescription` emission and the per-row
`DataRow` emission on `schema != nil` (writers such as
INSERT/UPDATE/DELETE/DDL/transaction report a *nil* `Output()` schema; a
zero-column read reports a *non-nil, zero-length* schema).

The **extended-query** path (`internal/server/dispatch_extended.go`,
`internal/server/extended.go`) diverged — a classic sibling-path bug
(`pattern_sibling_paths_must_agree`), the same class as
`0119-0004-extended-protocol-type-format-parity.md`:

1. `executeExtendedQueryViaExecutor` built `res.Fields` and appended per-row
   `res.Rows` only under `if len(schema) > 0`. For a zero-column read
   (`len(schema) == 0` but non-nil) it therefore appended **no rows at all**,
   so `Execute` sent zero `DataRow`s and the command tag read `SELECT 0`.
2. `describeViaPlanner` collapsed `len(schema) == 0` to the write-only case and
   returned `NoData`, and the two `handleDescribeFrame` callers keyed
   `NoData` off `len(fields) == 0`, so a zero-column read got `NoData` instead
   of a 0-field `RowDescription`.

Most client libraries only take the extended path for parameterized/prepared
queries, which is why no existing wire test caught it.

## Fix

Make the extended path use the same `schema != nil` / `schema == nil`
discriminator the simple path uses (nil ⇒ no result set ⇒ `NoData`; non-nil ⇒
result set, possibly zero-width ⇒ `RowDescription` + one `DataRow` per row):

- `dispatch_extended.go`: gate `res.Fields` construction and per-row
  `res.Rows` population on `schema != nil` (was `len(schema) > 0`). A
  zero-length row yields a non-nil empty `[][]byte`; `WriteDataRow` encodes
  it as a `DataRow` with column-count 0.
- `extended.go` `describeViaPlanner`: branch on `schema == nil` (write-only ⇒
  `nil, true`); a non-nil zero-length schema builds a non-nil **empty**
  `[]FieldDescription`, signalling "zero-column result set".
- `extended.go` `handleDescribeFrame` (both `'S'` and `'P'`): key `NoData`
  off `fields == nil` rather than `len(fields) == 0`, so the non-nil empty
  slice produces `WriteRowDescription` with 0 fields.

`WriteRowDescription`/`WriteDataRow` (`internal/protocol/messages.go`) already
encode empty slices as count-0 frames, so no protocol-layer change was needed.
`res.Fields` is not consumed by the `Execute` handler (extended-protocol
`RowDescription` comes only from `Describe`); it is populated for consistency.

## Tests

`internal/server/extended_zero_column_test.go`:

- `TestExtendedZeroColumnSelectEmitsRows` — `SELECT FROM items` over 3 rows via
  Parse/Bind/Describe/Execute/Sync asserts a 0-field `RowDescription` (not
  `NoData`), three zero-column `DataRow`s, and a `SELECT 3` command tag.
- `TestExtendedZeroColumnSelectWithFilter` — `SELECT FROM items WHERE id >= 2`
  returns exactly 2 zero-column rows / `SELECT 2` (row count tracks the WHERE
  clause, not a blanket "emit one row").

Non-vacuousness confirmed by reverting the `dispatch_extended.go` row-emission
guard to `len(schema) > 0` and observing `DataRow count=0, want 2`.

## Gates

`go build ./...` clean; full `internal/server` + `internal/protocol` suites
PASS; TPC-H spotcheck Q12=2/Q13=33 PASS (wire-protocol change only, no
planner/executor touch); pgbench smoke = pre-commit hook.

## Out of scope / not deferred

Both wire protocols now emit zero-column result sets faithfully; the simple
path was already correct and the extended path is fixed. No PG behavior remains
unimplemented for this feature, so no deferral-ledger row is opened.
