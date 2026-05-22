# pgbench Performance Comparison: goopg vs PostgreSQL

**Date:** 2026-05-22
**Scale Factor:** 100
**Duration:** 60 seconds per workload
**Configuration:** shared_buffers=2560MB, wal_buffers=100MB, checkpoint_timeout=24h, max_wal_size=1024GB

## SELECT-ONLY Workload (50 clients, 50 threads)

| System | TPS | Avg Latency (ms) | Conn Time (ms) |
|--------|-----|-----------------|----------------|
| goopg | **125,323** | 0.399 | 6.12 |
| PostgreSQL | **165,482** | 0.302 | 101.05 |

**goopg / PostgreSQL ratio: 75.7%**

## SIMPLE-UPDATE Workload (10 clients, 2 threads)

| System | TPS | Avg Latency (ms) | Conn Time (ms) | Notes |
|--------|-----|-----------------|----------------|-------|
| goopg | 187 | 53.46 | 2.34 | Incomplete — see Known Issues |
| PostgreSQL | **972** | 10.28 | 10.04 | Clean run |

## STANDARD (TPC-B-like) Workload (10 clients, 2 threads)

| System | TPS | Avg Latency (ms) | Conn Time (ms) | Notes |
|--------|-----|-----------------|----------------|-------|
| goopg | 219 | 45.75 | 2.54 | Incomplete — see Known Issues |
| PostgreSQL | **2,159** (c=10) / **6,468** (c=50) | 4.63 (c=10) / 7.73 (c=50) | 11.53 / 36.10 | Clean runs |

## Known Issues Affecting Results

### 1. DecodePhysicalPGRow: truncated varlena (CRITICAL)

All UPDATE-based workloads (SIMPLE-UPDATE, STANDARD) encounter the error:

    ERROR: DecodePhysicalPGRow: filler: truncated varlena

This aborts pgbench clients and makes results incomplete/unreliable.
Root cause is in the PG-binary heap tuple decode path
(`internal/executor/codec.go::DecodeRowIntoMctxPGTuple` /
`decodePhysicalPGValueMctx`).  The varlena field length decoding is
inconsistent with the encoded length for certain column value sizes.

This is related to:
- The TOAST out-of-line storage bug (>2000-byte values silently lost)
- Missing `KindString` → numeric coercion in `encodeValuePG` (partially
  fixed 2026-05-22)
- M0106-0010 PG-native physical format switch that moved INSERT/UPDATE
  from the goopg-format codec to the PG-format codec

### 2. SELECT-ONLY is unaffected

Read-only queries do not hit the varlena truncation edge case because
the decode path for plain reads uses a different code path that handles
the existing on-disk format correctly.

### 3. Historical comparison

Previous benchmark (2026-05-15, commit `ea19bca`):
- goopg STANDARD c=50 SU: **1,428 TPS**
- Current STANDARD c=10: **219 TPS** (but incomplete due to errors)

The regression suggests the varlena decode issue was introduced or
worsened by subsequent commits on the `claude-param-0522` branch.

## Summary

goopg SELECT-ONLY performance is competitive at **75.7% of PostgreSQL**
for read-only workloads at scale factor 100 with 50 concurrent clients.
UPDATE-based workloads cannot be reliably measured until the PG-format
varlena decode issue is fixed.
