# 0100-0005g — decodeValueSize handles `serial` / `bigserial`

**Status:** accepted
**Milestone:** M0100-0005 (drop-index-concurrently-1 setup unblock)
**Date:** 2026-05-15

## Problem

`TestPort_IsolationDropIndexConcurrently1` failed during global setup with
`ERROR: column "data" is null and cannot be indexed (42804)` on

    CREATE TABLE test_dc(id serial primary key, data int);
    INSERT INTO test_dc(data) SELECT * FROM generate_series(1, 100);
    CREATE INDEX test_dc_data ON test_dc(data);

The same shape reproduces outside the isolation suite with as few as five rows
— any time a non-leading column is indexed on a table whose leading column has
type `serial` (or `bigserial`), `CREATE INDEX` rejects the rows as if `data`
were `NULL`. Plain `int primary key` is unaffected.

## Root cause

`internal/executor/operators_ddl.go::collectBTreeEntries` builds a "keep" mask
that limits column materialisation to the index's key columns (the rest are
size-scanned to advance the offset but not decoded into Datums; this is the
M0054-0005c-followup optimisation). The size-only path goes through
`decodeValueSize` in `internal/executor/codec.go`.

`decodeValueSize` had cases for `int2 / int4 / int8 / bool / timestamp / …`
but **not** for `serial`, `bigserial`. Encoding however *does* — `encodeValue`
lumps `serial` into the `int4` arm and `bigserial` into the `int8` arm and
emits the corresponding fixed-width binary form. So a row whose first column
is `serial` encodes as `[flag=0][4-byte int]` for column 0, and the
size-decoder, missing the `serial` case, falls through to the varlen default,
reads the int4 bytes as a big-endian uint32 length prefix, and advances by
`4 + N` bytes — `N` being the actual stored id. For id=1 that overshoots by
1 byte; for id=5 by 5 bytes; etc.

The size-decoder thus lands the offset inside or past the next column's
encoded value. The next column's flag byte is misread, the column is decoded
as `NULL` (when the misread byte happens to be `1`) or its 4-byte int4
payload is read from misaligned bytes (when the misread byte is `0`).
`encodeCompositeBTreeKey` then rejects the NULL key and the whole `CREATE
INDEX` fails. The same projection-skip is reached by every index-maintenance
path that needs to decode only key columns, so the bug surface is wider
than `CREATE INDEX`.

The encode side wasn't symmetrically broken because `encodeValue`'s `int4`
and `int8` arms already include the `serial` / `bigserial` aliases — only
the size-scan path on the decode side was missing them. `decodeValue` and
`decodeValueArena` also already include the `serial` / `bigserial` aliases
on their `int4` / `int8` arms; they were never exercised on this column
because `keep[id]=false` short-circuited them to `decodeValueSize`.

## Fix

`internal/executor/codec.go::decodeValueSize` — add `"serial"` to the
`int4 / integer / int` arm (returns 4 bytes) and `"bigserial"` to the
`int8 / bigint` arm (returns 8 bytes). The `int2 / smallint` arm
intentionally does **not** include `"smallserial"`: `encodeValue` has no
`int2`/`smallserial` *aliasing* arm, so `smallserial` falls through to the
varlen text default on both sides. Encoder and decoder are symmetric for
`smallserial` via the varlen path, so the size-scan default case is
correct for it.

## Regression pin

`internal/executor/codec_projection_serial_test.go::TestDecodeRowProjectionSkipsSerialColumn`
encodes a two-column row `[serial id=5, int4 data=5]`, decodes it with
`keep=[false, true]`, and asserts `dst[1].Int == 5`. Two table-driven
sub-tests cover `serial` and `bigserial`. Verified to FAIL without the fix
(reads `data` as NULL or as 20250624 garbage for bigserial), PASS with it.

`TestPort_IsolationDropIndexConcurrently1` no longer fails global setup
post-fix; the spec now advances through `CREATE INDEX` and defers on a
separate `EXPLAIN (COSTS OFF) EXECUTE` parity issue (utility-statement
plan rendering — out of this fix's scope).

`go test -race ./internal/executor/ ./internal/storage/ ./internal/server/
./internal/mvcc/ ./internal/wal/ ./internal/initdb/ ./internal/parser/
./internal/planner/ ./internal/analyzer/` PASS.
