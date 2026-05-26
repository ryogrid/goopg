# 0111-0004 — PG-Format Decode Gap for Fixed-Width Types (int8 / name / temporal)

## Status
Accepted (2026-05-24)

## Context

M0106-0010 switched goopg's heap-tuple storage from a private fixed-format
encoding to PG-native physical format (`EncodeRowPG` /
`decodePhysicalPGRowIntoMctx`) so a PG18 standby attaching via
`pg_basebackup` can read goopg's pages through WAL FPIs.

The *encoder* (`encodeValuePG`) was given a complete per-type switch:
`bool`, `int2`, `int4`, `int8/bigint/bigserial`, `oid/regproc`,
`timestamp/timestamptz`, `date`, `time`, `timetz`, `name`, `float4/float8`,
the array types, etc. The *decoder* (`decodePhysicalPGValueMctx`) was only
given a subset: `bool`, `int2`, `int4`, `oid`, `time`, `timetz`, the
varlena/text family, `bytea`, `numeric`. Everything else fell through to a
`default` branch that returns `"unsupported PostgreSQL physical type"`.

The two switches drifted out of sync. The most damaging omission was
**`int8`/`bigint`/`bigserial`**: `count(*)`, `sum()`, and every plain
`bigint` column produce int8 values, encode fine, but failed to decode.

### Failure mechanism (silent row loss)

`decodeRowIntoMctx` tries `decodePhysicalPGRowIntoMctx` first, then falls
back to the legacy goopg decoder. For a row with an int8 column, both fail
(physical hits the `default` error; legacy mis-reads the PG bytes), so the
whole row decode errors. The seqscan treats an undecodable tuple as "no
visible row" and silently skips it — no error surfaces to the client.

Observable symptoms on a fresh cluster (PG18 psql client):

```
INSERT INTO t(bigint_col) VALUES (5);   -- reports "INSERT 0 1"
SELECT * FROM t;                          -- (0 rows)   <-- row vanished
CREATE TABLE c AS SELECT count(*) FROM s GROUP BY k;  -- stores 0 rows
```

This is the root cause of the M0097-0003 regression flagged in M0111's
audit: `select_implicit` and `portals_p2` both materialise aggregate
(`count(*)`) results, hit the int8 decode gap, and lost every row. `name`
columns lost rows the same way (64-byte `NameData` had no decode case).

## Change

Close the encode/decode asymmetry in `decodePhysicalPGValueMctx`
(`internal/executor/codec.go`) by adding the missing fixed-width cases,
each mirroring exactly what `encodeValuePG` writes:

| type | on-disk form (encode) | decode added |
|------|-----------------------|--------------|
| `int8` / `bigint` / `bigserial` | 8-byte LE int64 | `int64(LE.Uint64)` |
| `regproc` | 4-byte LE (shared with `oid`) | added to the `oid` arm |
| `xid` / `xid8` | 4-byte LE TransactionId | `int64(LE.Uint32)` |
| `name` | fixed 64 bytes, `\0`-padded | trim at first NUL → string |
| `timestamp` / `timestamptz` | 8-byte LE µs since PG epoch | `time.UnixMicro(µs + pgEpochUnixMicros)` |
| `date` | 4-byte LE days since PG epoch | `days*86400e6 + pgEpoch` |

`name` uses the arena allocator when an `sctx` is supplied, matching the
text path. `decodeValueSize` already handled int8/bigserial (added in
[0100-0005g](0100-0005g-decode-value-size-serial-types.md)) for the
*legacy* projection-skip path, which is why the gap was specific to the
*physical* value decoder.

Also removed leftover `GOOPG_DIAG_OVF` debug instrumentation in
`internal/executor/expr.go` (and its now-unused `os` import) that was
committed in a WIP commit and broke the build.

## Scope / non-goals

- **Not fixed (separate bugs):** `timestamp`/`date`/`xid` columns still
  reject direct string-literal INSERTs at *encode* time (`"expected time,
  got kind 3"` / type-mismatch) — an INSERT-side coercion gap, not a decode
  gap. The decode cases are nonetheless correct and verified via a value
  path that produces the right Datum kind (e.g. `now()::timestamp`).
- **Not fixed:** `bigint` columns reject literals that the lexer parses as
  `KindNumeric` (values requiring numeric fallback). `encodeValuePG`'s int8
  arm accepts only `KindInt`/`KindString`. Pre-existing; tracked separately.
- `xid8` is encoded as 4 bytes (matching the existing encoder), not PG's
  8-byte FullTransactionId. The decode mirrors the encoder for round-trip;
  PG-on-the-wire xid8 width is a separate follow-up.

## Verification

- New unit tests in `internal/executor/codec_int8_name_pg_test.go`:
  `TestDecodePhysicalPGInt8RoundTrip` (incl. int64 min/max),
  `TestDecodePhysicalPGNameRoundTrip`,
  `TestDecodePhysicalPGRowInt8DoesNotDropRow` (full-row heap-path decode).
- Live psql round-trip on a fresh cluster: bigint INSERT + SELECT, CTAS
  `count(*) … GROUP BY`, `sum()`, `name`, `now()::timestamp` all return
  their rows.
- Regress parity: `select_implicit` and `portals_p2` flip
  `failed → pass`; `name` improves 97 → 77 normalized diff lines (remaining
  diffs are unrelated). `int2`/`int4`/`numerology` unchanged (their diffs
  are not int8/name-related).
- `go test ./internal/executor/... ./internal/planner/... ./internal/server/...`
  green except the pre-existing `TestAnalyzeRespectsStatsTarget`
  (NDistinct sampling estimate 398 vs 400 on an **int4** column — confirmed
  failing without this change; unrelated).
