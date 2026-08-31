# 0111-0002 — Heap-Tuple Row Format Unification (single PG-physical format)

## Status
Draft (2026-05-26)

## Context

goopg has carried **two** heap-tuple row-body encodings since M0106-0010 introduced
PG-native physical storage alongside the original goopg-internal format:

| | Legacy (goopg v0) | PG-physical |
|---|---|---|
| encoder | `EncodeRow` (`internal/executor/codec.go`) | `EncodeRowPG` |
| decoder | `decodeGoopgRowIntoMctx` / `decodeValue` | `decodePhysicalPGRowIntoMctx` / `decodePhysicalPGValueMctx` |
| per-column framing | inline `[flag byte][value]` (0=present, 1=NULL, 2=TOAST) | none; NULLs via separate bitmap |
| integer byte order | big-endian | little-endian (PG native) |
| alignment | packed | PG `typalign` per type |
| varlena (text/varchar/…) | 4-byte big-endian length prefix | PG varlena header (1-byte short / 4-byte) |
| null handling | inline flag byte | `NullBitmapPG`, stored in tuple header (`HEAP_HASNULL`) |
| `natts` in `Infomask2` | not set (0) | set (= column count) |

PG-physical exists so a real PostgreSQL 18 standby can read goopg's full-page-image WAL via
`heap_deform_tuple`. The legacy format was a v0 stopgap predating the PG-compatible type system.

### The bug class this eliminates

Decode currently **guesses** which format a tuple uses:

- `decodeRowIntoMctx` tries the PG decoder first and falls back to the legacy decoder.
- `DecodeRowIntoMctxPGTuple`, when `storedNatts == 0 && len(bitmap) == 0`, forces `natts = ncols`
  and delegates to that same guessing path.

The two formats are not mutually exclusive at the byte level: a legacy-encoded `int4` whose
big-endian low byte forms a valid PG varlena header can be consumed *exactly* to end-of-tuple by
the PG decoder, which then "succeeds" and returns wrong data. This is precisely the
`TestAnalyzeRespectsStatsTarget` corruption (`int4` 15 → 0, 28 → 0) root-caused and fixed for the
ANALYZE path in commit `ce422ec`. The same latent corruption exists in ~16 other bare-`DecodeRow`
call sites. The inverse hazard (legacy decoder accepting PG bytes) is documented at
`decodeRowIntoMctx` (M0111-0001). **Guessing between two ambiguous formats is the defect; the only
robust discriminator is the tuple header.**

## Decision

Collapse to **one** on-disk heap-tuple row format — **PG-physical** — and make every decode path
**deterministic and header-driven** rather than guessing. Concretely:

1. **Decode invariant:** `natts > 0 || len(bitmap) > 0` ⇒ PG-physical; `natts == 0 && bitmap == nil`
   ⇒ legacy. No format is ever inferred from the column bytes. PostgreSQL always writes
   `natts ≥ 1`, so `natts == 0` is an unambiguous "legacy goopg tuple" marker.
2. **Write:** always `EncodeRowPG` + `NullBitmapPG` + `SetNatts(ncols)` for user-table DML and TOAST
   chunks. Remove the `ctx.LogCanonical != nil` encode branch.
3. **Remove** the legacy encoder/decoder and the guessing path entirely once writes are unified.

A real server already defaults to PG-physical (`internal/initdb/open.go` `PageHeaders: true` ⇒
`LogCanonical != nil`); legacy bodies are produced only in tests / non-PageHeaders mode. **Migration:
re-initialise the data directory.** goopg is a from-scratch reimplementation with no upstream
upgrade guarantee, so dropping the ability to read pre-existing legacy on-disk data is acceptable.

## Scope boundaries (verified)

- **Out of scope — system catalogs (`internal/catalog/codec.go`).** `pg_class`, `pg_attribute`,
  `pg_type`, etc. are `Virtual: true` (`internal/catalog/catalog.go`); the planner short-circuits
  any scan of a virtual table into a materialised `Values` node (`internal/planner/planner.go`,
  `buildVirtualValues`) before it reaches `SeqScan`, so catalog rows are **never** decoded through
  the executor heap codec. The catalog's own big-endian codec is a self-consistent, separate
  subsystem and is untouched here.
- **Out of scope — BTree index keys** (`encodeBTreeKeyForColumn`): per-Datum encoding, independent
  of row encoding.
- **WAL recovery/redo is format-agnostic:** `DecodeHeapInsert` treats the tuple body as opaque bytes
  and replay re-adds them to the page, preserving whatever header/`natts` were written.
- **Type coverage is not a blocker:** `encodeValuePG` / `decodePhysicalPGValueMctx` already cover a
  superset of the legacy codec's types (the legacy decoder is the limited one).

## Risk: pgoutput logical replication

`internal/wal/pgoutput.go` (`encodePgoTuple` / `pgoDecodeValue`) decodes user-table change bodies
for logical replication assuming the **legacy** inline-flag format, with no header/bitmap/`natts`
awareness. Once writes become always-PG-physical, this would mis-decode. `wal` cannot import
`executor` (import cycle), so it needs a `wal`-local PG-physical column reader (mirroring
`decodePhysicalPGValueMctx`, consistent with how `pgoDecodeValue` already duplicates the legacy
decoder). This is handled in stage **S2a**, before the write switch.

WAL recovery/redo, canonical-stream replication, and goopg's own standby replay are unaffected
(they already operate on PG-physical bodies in PageHeaders mode, which is the default; the change
only makes the non-PageHeaders/test path agree).

## Staged plan (one logical change per commit)

### S1 — Deterministic, header-driven decode (correctness; no on-disk change)
Rewrite `DecodeRowIntoMctxPGTuple` as a dispatcher: `natts == 0 && bitmap == nil` ⇒ legacy decoder
directly, else PG-physical loop directly (remove the "try legacy first" attempt and the delegation
to the guessing `decodeRowIntoMctx`). Add a shared helper
`DecodeHeapTupleRow(dst, cols, tuple, sctx)` =
`DecodeRowIntoMctxPGTuple(dst, cols, tuple.Data, tuple.Bitmap, int(tuple.Header.Infomask2 & storage.HeapNattsMask), sctx)`,
and route the 16 bare-`DecodeRow`/`DecodeRowInto` call sites (all have the `storage.HeapTuple` in
scope) through it: `operators_fk.go` (×6), `operators_merge.go`, `operators_upsert.go` (×2),
`operators_indexonly.go`, `operators_index.go`, `operators_storage.go` (×2), `applyworker.go` (×2),
`toast.go`. **No write/`SetNatts` change in S1** — legacy bodies still carry `natts == 0`, which the
dispatcher keys off. Safe on mixed/legacy data.

### S2a — pgoutput physical decode (required before S2)
Make `encodePgoTuple` header-aware and add a `wal`-local PG-physical column reader; keep its legacy
branch until S3.

### S2 — Always write PG-physical (user DML + TOAST)
Drop the `LogCanonical` encode branch in `writeHeapRowReturning` and the HOT-update path; always
`EncodeRowPG` + `NullBitmapPG` + unconditional `SetNatts(ncols)`. Switch TOAST chunk encoding
(`toast.go`) to `EncodeRowPG`. Rewrite tests that asserted legacy round-trips under
`LogCanonical=nil` (notably `codec_pg_format_fallback_test.go`).

### S3 — Remove legacy format + dead projection code (requires re-init)
Delete the legacy arm of `DecodeRowIntoMctxPGTuple`, the guessing in `decodeRowIntoMctx`, `EncodeRow`,
`decodeGoopgRowIntoMctx`, legacy `encodeValue`/`decodeValue`, the pgoutput legacy branch, and the
unused projection decoders (`DecodeRowProjection*`, `decodeRowProjectionMctx`) — each only after
`grep`/reference checks confirm zero remaining callers.

## Other correctness considerations

- **ALTER TABLE ADD COLUMN** (`storedNatts < ncols`): the PG-physical loop fills trailing columns
  with NULL; regression test required.
- **NULL handling** moves entirely to the bitmap (`NullBitmapPG`: bit set = NOT NULL, matching PG's
  `heap_fill_tuple`); test all-NULL, mixed, and trailing-NULL (byte-boundary) rows.
- **TOAST pointer framing** differs (legacy `0x02` + 12 bytes vs PG short-varlena `0x1B` + 12 bytes)
  but both decode to the same `ToastPointerDatum`, and `DetoastValue` is framing-agnostic; covered
  by a TOAST round-trip test.

## Verification

- Per stage: `go test ./internal/executor/... ./internal/wal/... ./internal/storage/... ./internal/catalog/...`.
- After S2: fresh `make` re-init + DML smoke (INSERT / UPDATE HOT+non-HOT / DELETE / SELECT, NULLs,
  a >2 KB TOAST value, ADD COLUMN trailing-NULL) and a WAL crash-recovery smoke.
- After S3: `go build ./... && go vet ./...`; full `go test ./...`; the pg_regress / parity suite
  with diff-line comparison to the M0097 baseline (must not regress).

## References
- `internal/executor/codec.go` (`EncodeRow`, `EncodeRowPG`, `DecodeRowIntoMctxPGTuple`,
  `decodeRowIntoMctx`, `decodeGoopgRowIntoMctx`, `decodePhysicalPGValueMctx`, `NullBitmapPG`)
- `internal/executor/operators_storage.go` (`writeHeapRowReturning`, HOT-update path),
  `internal/executor/toast.go`, `internal/wal/pgoutput.go`
- `internal/storage/heap.go` (`HeapNattsMask`, `SetNatts`, `HeapTuple`)
- Prior art: [0111-0001-pg-format-codec-parity.md](0111-0001-pg-format-codec-parity.md);
  ANALYZE decode fix commit `ce422ec`.
