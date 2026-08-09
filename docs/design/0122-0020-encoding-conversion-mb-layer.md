# 0122-0020 — Character-encoding conversion: `internal/mb` layer + LATIN1↔UTF8 first slice

Status: draft (in progress). Source: `.ralph/fix_plan.md` M0122-0008
("Auth / roles / multi-DB isolation / encoding").

## Problem

goopg validates encoding names at every tier (initdb `--encoding`, GUC
`client_encoding` check, `CREATE DATABASE ENCODING`, catalog name tables) but
performs **zero actual byte-level transcoding**. When a client sets
`client_encoding` to a non-UTF8 value, the server continues to send raw UTF8
bytes and read client bytes as raw UTF8. This is wire-incompatible with real
clients that honour `client_encoding` — a `psql` connected with
`client_encoding=LATIN1` expects LATIN1-encoded result cells and sends
LATIN1-encoded query text, not UTF8.

The gap was explicitly documented in `unimplemented_feat.json` (M0122-0008,
status `open`, code_audit `PARTIAL`), the CREATE CONVERSION DDL path
(`internal/executor/operators_ddl.go` L16029: "no encoding-conversion engine"),
and the catalog `UserConversion` struct doc comment ("goopg performs no actual
encoding conversion").

## Fix

A new `internal/mb/` package (mirrors `postgres/src/backend/utils/mb/`) with
three layers:

1. **Conversion function table** — a proc-OID → Go function dispatch that
   mirrors PG's `OidFunctionCall6(proc, ...)` path.
2. **Core dispatch** — `DoEncodingConversion(src, srcEnc, destEnc)` with the
   same fast-path semantics as `pg_do_encoding_conversion` (empty string,
   same-encoding, SQL_ASCII passthrough + verify).
3. **Wire-in** — hook the server I/O paths (output DataRow cells, input Bind
   parameters and Parse query text) through the conversion layer when
   `client_encoding != server_encoding`.

### Layer 1: Conversion function table (`internal/mb/`)

A `ConvProc` is a Go function with the PG signature:

```go
type ConvProc func(src []byte, noError bool) (bytesConsumed int, dest []byte, err error)
```

A global `BuiltinConversions map[uint32]ConvProc` maps proc OID → function.
First slice populates two entries:

| proc OID | function | PG source |
|----------|----------|-----------|
| 4374 | `iso8859_1_to_utf8` | `conversion_procs/utf8_and_iso8859_1/utf8_and_iso8859_1.c:41` |
| 4375 | `utf8_to_iso8859_1` | same file L77 |

The proc OIDs come from `initdb/pg_proc_seed_data.go` (already seeded at L2989).

Both functions are ported byte-for-byte from the PG C source:

- **`iso8859_1_to_utf8`**: walks the source; ASCII bytes pass through unchanged;
  high-bit-set bytes (0x80–0xFF) expand to the 2-byte UTF8 encoding
  `(0xC0 | (c>>6)), (0x80 | (c&0x3F))`. Returns bytes consumed. Stops on
  embedded NUL.

- **`utf8_to_iso8859_1`**: walks the source; ASCII bytes pass through; for
  high-bit-set bytes, validates the UTF8 sequence with `pg_utf8_islegal`,
  rejects sequences longer than 2 bytes with `report_untranslatable_char`,
  decodes the 2-byte sequence into a codepoint, and rejects codepoints outside
  the LATIN1 range (0x80–0xFF). Stops on embedded NUL.

Validation helpers ported from `postgres/src/backend/utils/mb/conv.c` and
`postgres/src/common/wchar.c`:
- `pg_utf_mblen` — byte-length of a UTF8 multi-byte leading byte.
- `pg_utf8_islegal` — validate a UTF8 sequence.
- `reportInvalidEncoding` / `reportUntranslatableChar` → Go errors carrying
  SQLSTATE `22021` / `22028`.

### Layer 2: Core dispatch (`DoEncodingConversion`)

```go
func DoEncodingConversion(src []byte, srcEnc, destEnc int32) ([]byte, error)
```

Fast paths (matches PG `pg_do_encoding_conversion` exactly):
1. `len(src) == 0` → return `src` (empty string always valid).
2. `srcEnc == destEnc` → return `src` (no conversion needed, assume valid).
3. `destEnc == PG_SQL_ASCII` → return `src` (any string valid in SQL_ASCII).
4. `srcEnc == PG_SQL_ASCII` → verify `src` is valid `destEnc` via
   `pg_verifymbstr`, then return `src`.

Then: lookup the proc OID from the `pg_conversion` catalog (a new
`FindDefaultConversionProc` method on the catalog that searches
`UserConversion` entries by `(ForEncoding, ToEncoding)`). The 128 bootstrap
rows in `initdb/pg_conversion_bootstrap.go` already carry ConProc OIDs, so the
defaults resolve without extra seeding.

If no proc is found, raise `42883` (undefined function — same as PG).

Otherwise: allocate a destination buffer sized `len(src) * MAX_CONVERSION_GROWTH + 1`
(default 4 for UTF8), call the proc, and return a trimmed slice of the used
portion.

Also: `ServerToClient` / `ClientToServer` convenience wrappers that read
`client_encoding` from the session GUCs.

### Layer 3: Wire-in (server I/O paths)

**Output direction** (server → client, should transcode when
`client_encoding != server_encoding`):

1. `internal/server/dispatch.go` — simple-query DataRow cell serialization
   (`PutDataRowScratch` path). After the existing `AppendValueText` produces
   UTF8 bytes, if `client_encoding != UTF8`, transcode through
   `ServerToClient`.

2. `internal/server/extended.go` — extended-query portal Execute
   (`WriteDataRow`). Same treatment.

3. `internal/server/dispatch.go` — cursor FETCH DataRow loop (~L3784). Same
   treatment.

**Input direction** (client → server):

4. `internal/server/extended.go` Bind parameters (~L196): after reading the
   raw parameter bytes, if `client_encoding != UTF8`, transcode through
   `ClientToServer`.

5. Simple-query and Parse message query strings: transcode the query text
   before it reaches the parser. (The exact hook point is TBD during
   implementation — it may be in `handleSimpleQuery` or `handleParse`.)

All hook points already have access to the session GUCs through the
`ectx.GetSetting` bridge (`dispatch.go:392`).

**Performance note:** the fast path (same encoding or ASCII-only content)
must be allocation-free for the dominant `client_encoding=UTF8` case.
`DoEncodingConversion` returns `src` unchanged when `srcEnc == destEnc`.

## pg_conversion catalog lookup

goopg's `catalog.InMemory` already has `UserConversion` entries and
`CreateConversion`/`FindConversion(name,schema)` methods. What's missing is
**lookup by encoding pair** — the equivalent of PG's `FindDefaultConversion`
(`namespace.c:4083` → `pg_conversion.c:152`, CONDEFAULT syscache scan).

Add:

```go
func (im *InMemory) FindDefaultConversionProc(forEnc, toEnc int32) (uint32, bool)
```

This walks `im.userConversions` for a match on `ForEncoding`/`ToEncoding`,
returning the `FuncOID`. The 128 bootstrap rows in
`initdb/pg_conversion_bootstrap.go` seed the builtin pairs; user-created
conversions (CREATE CONVERSION) are also visible.

## First slice scope

- Package `internal/mb/` with:
  - `conv.go` — `ConvProc` type, `BuiltinConversions` table, `DoEncodingConversion`,
    `ServerToClient`, `ClientToServer`, validation helpers.
  - `latin1.go` — `iso8859_1_to_utf8`, `utf8_to_iso8859_1`.
  - `wchar.go` — `pg_utf_mblen`, `pg_utf8_islegal`, `pg_verifymbstr`.
  - `latin1_test.go` — byte-for-byte round-trip tests against PG expected output.
- `internal/catalog/catalog.go` — `FindDefaultConversionProc` method.
- `internal/server/dispatch.go` — output DataRow transcoding.
- `internal/server/extended.go` — input Bind parameter transcoding + output DataRow.

**Out of scope for this slice:**
- Other encoding pairs (EUC_JP, SJIS, BIG5, GBK, etc.) — these follow the same
  pattern and are added in subsequent slices.
- `SET client_encoding` to trigger re-transcoding of in-flight portal state
  (PG's `PerformDefaultEncodingConversion` caches the last-used proc; goopg
  v0 always resolves fresh).
- `pg_encoding_max_length` SQL function.
- Query-text transcoding on the simple-query and Parse paths (these are
  ASCII-safe for practical SQL; the Bind parameter path is the critical one
  because parameter values can contain arbitrary byte sequences).

## Test plan

1. `internal/mb/latin1_test.go`: byte-for-byte round-trip: every 0x00–0xFF
   byte → iso8859_1_to_utf8 → utf8_to_iso8859_1 → original byte. Validate
   error cases (embedded NUL, invalid UTF8, untranslatable codepoints).
2. `internal/server/encoding_conversion_test.go`: E2E test — start goopg,
   connect with `client_encoding=LATIN1`, INSERT a row with LATIN1
   high-bit characters, SELECT it back, verify the bytes round-trip.
3. `internal/mb/conv_test.go`: `DoEncodingConversion` fast-path tests
   (empty, same-enc, SQL_ASCII).

## Deferral ledger

Append one row for each follow-up encoding pair not yet implemented, and
one for the query-text transcoding path (simple-query/Parse).

## Gates

- `go build ./...` clean
- `go test ./internal/mb/...` PASS
- `go test ./internal/server/... ./internal/executor/... ./internal/catalog/...` PASS
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
- `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed)
