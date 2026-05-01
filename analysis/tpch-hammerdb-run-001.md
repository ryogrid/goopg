# TPC-H HammerDB End-to-End Run (SF=1)

**Date:** 2026-05-01
**goopg commit:** `6ce4ad2` (HEAD)
**HammerDB version:** 5.0
**Scale factor:** 1

## Configuration

| Parameter | Value |
|-----------|-------|
| `shared_buffers` | 256 MB |
| `checkpoint_timeout` | 15 min |
| `max_wal_size` | 4 GB |
| `GOMEMLIMIT` | 512 MiB |
| `TPCH_BUILD_THREADS` | 1 |
| `TPCH_TOTAL_QUERYSETS` | 1 |
| `TPCH_DEGREE_OF_PARALLEL` | 1 |

## Schema Build

### What Worked
- Server startup with fresh `goopg init`: clean
- `CREATE TABLE` for all 8 TPC-H tables: clean
- Data loading via `COPY FROM STDIN` for all tables:
  - REGION (5 rows): OK
  - NATION (25 rows): OK
  - SUPPLIER (10,000 rows): OK
  - CUSTOMER (150,000 rows): OK
  - PART and PARTSUPP (200,000 parts + corresponding partsupp): OK
  - ORDERS and LINEITEM (1,500,000 orders + ~6,000,000 lineitems): OK

### What Failed
- **Index creation** ("CREATING TPCH INDEXES" → `FINISHED FAILED`):
  - goopg's `CREATE INDEX` does not support all index types required by HammerDB
  - Known limitation (int8 index support was recently added, but other index types remain unsupported)

## Power Test

### Result: **FAILED**

The power test (Q1–Q22) aborted immediately at the first query (Q14) with:

```
ERROR:  DecodeRow: l_extendedprice: decode numeric "2-HIGH": numeric out of int64 range: "2-HIGH"
```

This error occurs in `internal/executor/codec.go:DecodeRowInto` when the `l_extendedprice` column of the `lineitem` table contains a non-numeric string value.

### Identified: Systematic Data Corruption

The error is reproducible across multiple independent fresh data loads. The corrupted value is always a string from the `orders.o_orderpriority` column (e.g. `1-URGENT`, `2-HIGH`, `3-MEDIUM`, `4-NOT SPECIFIED`, `5-LOW`). This indicates a **systematic column-alignment bug** in the COPY FROM path where ORDERS data is written to the LINEITEM table.

Root cause analysis (see `internal/executor/copy.go:PushLine`):
- The `CopyFromExecutor.PushLine` method calls `DecodeCopyTextRow` with columns derived from `plan.ColumnIndex`
- The `writeHeapRow` call uses `c.cols` (= `plan.Table.Columns`) and the assembled row
- The column order in the catalog matches the CREATE TABLE DDL
- The two-phase COPY TEXT parser (`splitCopyTextFields`) correctly handles tab-delimited data

**Hypothesized cause:** HammerDB's data generator produces ORDERS and LINEITEM data within a single virtual-user session. Despite appearing as separate COPY statements, some ORDERS rows may be routed to the LINEITEM COPY handler due to a wire-protocol or framing issue during the transition between the two COPY sessions.

**Status:** Under investigation. The exact trigger has not been isolated.

## Data Verification

- `orders` table: queryable (COUNT query times out without indexes, but `SELECT ... LIMIT N` works)
- `lineitem` table: some rows are clean, others contain column-shifted data from `orders`
- Test table (`test_lineitem`) created separately and populated via manual `COPY FROM STDIN`: data is clean

## Index Creation

All indexes fail with feature-not-supported errors. The specific error messages were not captured in the HammerDB build log (HammerDB suppresses per-query errors in build mode). Known unsupported index types in v0 include:
- Numeric-column indexes that require type-specific B-tree comparison operators
- Composite indexes
- Partial indexes / functional indexes

## Summary

| Step | Status | Notes |
|------|--------|-------|
| Server init & config | PASS | shared_buffers=256MB verified stable |
| Schema creation | PASS | All 8 tables created |
| Data loading | PASS | SF=1 data loaded, no OOM |
| Index creation | FAIL | Feature gap: unsupported index types |
| Power test Q1–Q22 | FAIL | Data corruption in lineitem table |

## Next Steps

1. Fix the column-alignment bug in the COPY FROM path (likely in `PushLine` or the COPY state transition)
2. Implement `CREATE INDEX` for remaining index types (composite, partial)
3. Re-run power test after fixes
