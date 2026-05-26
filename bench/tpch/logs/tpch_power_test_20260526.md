# TPC-H Power Test — All 22 Queries Passed

**Date:** 2026-05-26  
**Run log:** `run_goopg_20260526-135117.log`  
**Binary commit:** `26cf58d` (fix: VARATT_IS_1B_E TOAST marker)  
**Branch:** `align-data-structure-with-pg`  
**Scale factor:** SF=1  
**HammerDB:** v5.0  

## Result

**FINISHED SUCCESS** — all 22 queries completed without error.

| Order | Query | Time (s) |
|------:|------:|---------:|
| 1  | Q14 | 20.728 |
| 2  | Q2  | 59.078 |
| 3  | Q9  | 56.059 |
| 4  | Q20 | 19.451 |
| 5  | Q6  | 13.116 |
| 6  | Q17 | 45.209 |
| 7  | Q18 | 36.773 |
| 8  | Q8  | 171.430 |
| 9  | Q21 | 295.057 |
| 10 | Q13 | 84.864 |
| 11 | Q3  | 16.789 |
| 12 | Q22 | 84.918 |
| 13 | Q16 | 2.904 |
| 14 | Q4  | 217.190 |
| 15 | Q11 | 2.409 |
| 16 | Q15 | 36.701 |
| 17 | Q1  | 20.036 |
| 18 | Q10 | 18.524 |
| 19 | Q19 | 24.503 |
| 20 | Q5  | 18.603 |
| 21 | Q7  | 122.899 |
| 22 | Q12 | 100.535 |

**Total elapsed:** 1469 seconds (~24.5 minutes)  
**Geometric mean of query times:** 36.30 seconds  

## Background and Fix History

### Previous failures

The prior power test run (same date, `run_goopg_20260526-120958.log`) failed at
Q11 with:

```
Error in Virtual User 1: Query Error : ERROR:  column "inf" does not exist
```

This was a secondary symptom. The root cause was that `supplier` and `customer`
tables returned **0 rows** despite 1.8 MB and 23 MB of on-disk data respectively.
The Q11 HAVING clause arithmetic produced `inf` (division by zero with an empty
supplier table), which goopg then misidentified as a column reference.

### Root cause: TOAST pointer marker collision (commit `26cf58d`)

The TOAST pointer encoding used `0x1B` as the first byte of the 13-byte
external-varlena header. `0x1B = (13 << 1) | 1` is also the valid PG
short-varlena header for any **12-character string** (`total = 13`, header =
`(total << 1) | 1`).

HammerDB's `gen_phone` procedure always generates phone numbers in the format
`XXX-XXX-XXXX`, which is exactly 12 characters. Therefore:

- Every `s_phone` value in `supplier` (10,000 rows) produced header byte `0x1B`.
- Every `c_phone` value in `customer` (150,000 rows) produced header byte `0x1B`.

The decoder in `decodePhysicalPGValueMctx` checked `data[0] == 0x1B` and
returned a `KindToastPointer` Datum for these values. `DetoastRow` then
attempted to dereference the phone string bytes as a TOAST OID/chunk pointer,
failed, and the seqscan operator silently `continue`d past the tuple
(`operators_storage.go`). All rows in both tables were invisibly dropped.

**Fix:** changed the TOAST marker to `0x01` (`VARATT_IS_1B_E` in PG's varlena
encoding). `0x01 >> 1 = 0` is an impossible data-varlena length, so no
legitimate string can produce this header byte. Changed in both:

- `encodeRowPG` (encoder): `buf[0] = 0x01`
- `decodePhysicalPGValueMctx` (decoder): `data[0] == 0x01`

### Concurrent change: JSON catalog removal (commit `40ed3a3`)

The M0111-0002 S2/S3 heap-tuple format unification (committed earlier on this
branch) changed the on-disk write format. Existing data directories written
before S2 were unreadable after S3 deleted the legacy decoder. This required a
full data-directory reset before the verification run.

Separately, the user requested that catalog persistence be aligned with
PostgreSQL's approach. PG never uses JSON for catalog storage — it reads
`pg_class` and `pg_attribute` heap pages on startup. The JSON save/load path
(`global/pg_catalog.json`, `loadCatalogSnapshot`, `SaveCatalog` JSON write,
`maybeMigrateCatalogToHeap`) was removed. `loadUserTablesFromHeap` is now the
sole catalog recovery path, matching PG's behaviour.

### Fresh data load

Because both the TOAST marker byte (`0x1B` → `0x01`) and the underlying
heap-tuple format (M0111-0002 S2) had changed, existing on-disk data was
incompatible. A full reset was performed:

```bash
./bench/tpch/setup_goopg.sh --reset   # wipe + reinit data dir
./bench/tpch/build_schema_goopg.sh    # load SF=1 data (~10 min)
./bench/tpch/run_power_test_goopg.sh  # 22-query power test
```

All 22 queries passed on the first attempt with the fresh data.
