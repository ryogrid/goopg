# 0100-0005 Loop-14 PG Physical Format Fixes

**Status**: accepted  
**Loop**: 14  
**Date**: 2026-05-20

## Context

M0100-0005 requires all 21 RC isolation tests to pass. Currently 9/21 pass.
Three correctness bugs blocked further progress on `eval-plan-qual` and other
tests. This design doc covers the three fixes landed in loop 14.

---

## Fix A: Numeric encode/decode in PG physical format

### Problem

`encodeValuePG` for `numeric`/`decimal` fell through to the default case:
`varlenaTextBytes(d.StringValue())`. For `KindNumeric` datums, `StringValue()`
returns `""` (empty) because numeric values live in `Int`/`M` fields, not
`Buf`. Result: balance columns stored as empty PG varlena strings.

`decodePhysicalPGValueMctx` had no `numeric` case, so reading those stored
bytes failed with `unsupported PostgreSQL physical type "numeric"` — causing
the seqScan to silently skip all rows with numeric columns (including
`accounts` in eval-plan-qual).

### Fix

- `encodeValuePG`: add `case "numeric", "decimal"` that calls
  `varlenaTextBytes(numericText(d))` to produce a proper PG varlena.
- `decodePhysicalPGValueMctx`: add `case "numeric", "decimal"` that reads
  a PG varlena, extracts the text payload, and parses it via
  `parseNumericFast`/`parseNumeric` (mirroring the goopg-format decode path).

---

## Fix B: CREATE FUNCTION attributes before AS $$

### Problem

`parseCreateFunctionTail` only recognised `LANGUAGE` and `AS` clauses. Any
attribute keyword appearing before `AS $$body$$` (e.g. `IMMUTABLE`,
`VOLATILE`, `SECURITY DEFINER`) caused:
`syntax error: expected AS $$body$$ for CREATE FUNCTION (got immutable)`

This blocked `insert-conflict-specconflict.spec` (defines functions with
`IMMUTABLE LANGUAGE plpgsql AS $$...$$`).

### Fix

Add `isFunctionAttribute()` / `consumeFunctionAttribute()` helpers to
`internal/parser/function.go` that recognise and silently consume:
`IMMUTABLE`, `VOLATILE`, `STABLE`, `STRICT`, `LEAKPROOF`, `NOT LEAKPROOF`,
`SECURITY DEFINER/INVOKER`, `EXTERNAL SECURITY DEFINER/INVOKER`,
`CALLED ON NULL INPUT`, `RETURNS NULL ON NULL INPUT`, `PARALLEL SAFE/UNSAFE/RESTRICTED`,
`COST n`, `ROWS n`, `SUPPORT name`, `SET guc`.

Applied to both `parseCreateFunctionTail` and `parseCreateProcedureTail`.

---

## Fix C: Bitmap + natts aware decoder for ALTER TABLE ADD COLUMN

### Problem

`eval-plan-qual.spec` runs:
```sql
ALTER TABLE accounts_ext ADD COLUMN newcol int DEFAULT 42;
ALTER TABLE accounts_ext ADD COLUMN newcol2 text DEFAULT NULL;
```

Old rows stored before the ALTER have:
- `tuple.Header.Infomask2 & HeapNattsMask = 3` (3 original columns)
- `tuple.Bitmap` = null bitmap covering only those 3 columns (or nil if none null)
- `tuple.Data` = PG physical bytes for 3 columns only

The existing `decodePhysicalPGRowIntoMctx` iterated over all 5 schema columns,
reached `off > len(data)` on the 4th, and returned an error. `seqScanOp`
silently skipped those rows — making the `EXISTS (... FROM accounts_ext ...)` 
subquery in `wnested2` return empty results and suppressing all 5 NOTICEs.

An identical issue existed in `updateViaIndex`'s scan-matching loop (used by
`UPDATE accounts_ext ... WHERE accountid = 'checking'`).

### Fix

New function `DecodeRowIntoMctxPGTuple(dst, cols, data, bitmap, storedNatts, sctx)`:
1. **storedNatts == 0**: legacy goopg row without explicit natts — uses normal
   `decodeRowIntoMctx` (no bitmap, all columns present).
2. **storedNatts >= len(cols)** and no bitmap: fast path via existing
   `decodeRowIntoMctx`.
3. **PG physical format + bitmap + natts**:
   - For `i >= storedNatts`: column absent (ALTER TABLE ADD COLUMN) → NULL.
   - For `bitmap[i/8] >> (i%8) & 1 == 0`: column NULL per bitmap → NULL, skip.
   - Otherwise: align + decode via `decodePhysicalPGValueMctx`.
   - `off >= len(data)`: data exhausted early → NULL for remaining columns.

Applied at two call sites:
- `seqScanOp.Next()` — primary scan path for all table reads.
- `updateViaIndex` scan-matching loop — EPQ + index-driven UPDATE scans.

### Result

`eval-plan-qual` progresses from 1199/1494 matching output lines to 1257/1494.
The first 393 lines (covering permutations 1–15) now match. Still deferred
on permutations involving concurrent waiting (missing `<waiting ...>` markers).
