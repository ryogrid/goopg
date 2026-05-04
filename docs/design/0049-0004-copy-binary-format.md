# 0049-0004 — Binary COPY format

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0049 — Protocol parity
**Supersedes:** —

## Context

`COPY ... WITH (FORMAT BINARY)` today returns 0A000 / `feature_not_
supported`. Bulk-load tools (pg_dump / pg_restore) and `\copy ... binary`
in psql can't use goopg as a target. Text COPY works but parses 5–10×
slower for typical schemas because per-column type-from-text decoders
are universally slower than per-type binary decoders (numerics, dates,
bytea especially).

## Plan

1. **Header.** 19 bytes:
   - 11-byte signature `PGCOPY\n\377\r\n\0`.
   - 4-byte flags (only bit 16 = OID-included is meaningful; goopg
     never includes OID, so always 0).
   - 4-byte extension-area length (always 0 for v0).
2. **Per-row body.** `int16 fieldCount`, then per field:
   - `int32 length` (`-1` for NULL).
   - `length` bytes of binary payload encoded by the column type's
     existing extended-query Bind/Execute binary codec.
3. **Trailer.** `int16 = -1`.
4. **Wire integration.**
   - `COPY ... TO STDOUT WITH (FORMAT BINARY)` — executor walks the
     scan, encodes header + per-row body + trailer into `CopyData`
     frames.
   - `COPY ... FROM STDIN WITH (FORMAT BINARY)` — receive `CopyData`
     until trailer; for each row, decode columns via the binary
     decoders; emit to the existing executor `Insert` path.
5. **Type coverage.** Reuse the binary codecs already in
   `internal/protocol/binary.go` (extended-query path). Audit:
   `int4`, `int8`, `numeric`, `text`, `varchar`, `char`, `date`,
   `timestamp`, `bool`, `bytea`. Anything else errors with
   `feature_not_supported` (matches upstream's behaviour for
   uncommon types).
6. **Error path.** Mid-COPY errors abort the transaction and consume
   the rest of the CopyData stream until `CopyFail` or
   `CopyDone`.

## Definition of Done

- `COPY t TO 'binary'` followed by `COPY t FROM 'binary'` reproduces
  every row for the supported type set.
- pg_dump-style tool against goopg in binary mode loads a 1M-row
  table ≥ 3× faster than the text path.
- Errors mid-stream cleanly abort the transaction and surface SQLSTATE
  + position (M0049-0002 fields).

## Upstream reference

- `postgres/src/backend/commands/copyfromparse.c` — binary parser.
- `postgres/src/backend/commands/copyto.c` — binary writer.
- `postgres/src/include/commands/copy.h` —
  `BinarySignature[]` constant.

## goopg references

- `internal/server/copy.go`, `internal/protocol/copy.go`.
- `internal/protocol/binary.go` — per-type codecs.
- `docs/design/root-0014-copy.md` — current text-COPY scope.
