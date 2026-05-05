# Binary COPY Format — M0049-0004

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

`COPY ... WITH (FORMAT BINARY)` or bare `BINARY` keyword returned 0A000.
Binary COPY is used by pg_dump/pg_restore and `\copy ... binary` in psql,
and is 3–10× faster than text COPY for numeric-heavy tables.

## 2. Design

### 2.1 Binary COPY wire format

```
Header  (19 bytes): "PGCOPY\n\377\r\n\0" + int32 flags (0) + int32 ext-area-len (0)
Per row (variable): int16 fieldCount | for each field: int32 len (-1=NULL) + bytes
Trailer (2 bytes):  int16 = -1
```

### 2.2 New file: `internal/executor/copy_binary.go`

| Function | Description |
|---|---|
| `CopyBinaryHeader() []byte` | 19-byte PGCOPY header |
| `AppendCopyBinaryRow(dst, row, cols)` | Encodes one row |
| `AppendCopyBinaryTrailer(dst)` | Appends int16(-1) |
| `ParseCopyBinaryHeader(data)` | Validates 11-byte signature |
| `ParseCopyBinaryRows(data, cols)` | Parses all complete rows; returns `(rows, trailerFound, consumed, err)` |

**Supported types:** int4, int8, bool, text/varchar/char, timestamp, timestamptz, date, numeric, bytea.

Binary encoding per PostgreSQL wire protocol:
- int4: 4 bytes big-endian
- int8: 8 bytes big-endian
- bool: 1 byte (0/1)
- text/varchar: raw UTF-8 bytes
- timestamp/timestamptz: int64 microseconds from 2000-01-01T00:00:00 UTC
- date: int32 days from 2000-01-01
- numeric: `int16 ndigits, weight, sign, dscale` + `ndigits × int16` base-10000 digits

### 2.3 Format detection (`internal/executor/copy.go`)

`IsBinaryFormat(opts []parser.CopyOption) bool` checks for `FORMAT binary` (new-style `WITH (FORMAT BINARY)`) or bare `BINARY` keyword (legacy syntax).

### 2.4 `RunCopyTo` updated signature

```go
func RunCopyTo(ctx *Context, plan *planner.Copy, emit func([]byte) error) (count int64, binary bool, err error)
```

When binary, `RunCopyTo` emits the header, binary rows, and trailer. The `binary` return value lets the wire layer set the correct format code.

### 2.5 `CopyFromExecutor.PushBinaryData`

For binary COPY FROM, the wire layer accumulates CopyData payloads and calls `PushBinaryData(chunk)` which:
1. Validates the 19-byte header on the first call
2. Calls `ParseCopyBinaryRows` for all complete rows in the buffer
3. Inserts each row via `writeHeapRow`
4. Returns `(done=true, nil)` when the int16(-1) trailer is found

`IsBinary() bool` on `CopyFromExecutor` lets the wire layer select the text vs binary path.

### 2.6 Wire layer (`internal/server/copy.go`)

- `runCopyTo`: `WriteCopyOutResponse(1, nil)` when binary, `(0, nil)` for text
- `dispatchCopyViaExecutor → CopyFrom arm`: `WriteCopyInResponse(1, nil)` when binary
- `handleCopyInFrame`: binary path calls `PushBinaryData`; text path unchanged

## 3. Correctness

- `ParseCopyBinaryRows` handles partial rows (incomplete fields) by returning
  the byte offset of the last complete row; the buffer is compacted before
  the next chunk.
- The numeric encoder/decoder handles the base-10000 digit representation via
  a string round-trip for correctness on complex values.
- `IsBinary()` ensures that a single `CopyFromExecutor` never mixes text and
  binary paths.

## 4. Tests (`internal/executor/copy_binary_test.go`)

| Test | Coverage |
|---|---|
| `TestCopyBinaryRoundTrip` | **DoD**: round-trip int4/int8/bool/text/timestamp/date/numeric + NULL |
| `TestCopyBinaryHeaderSignature` | 19-byte header signature bytes correct |
| `TestCopyBinaryRoundTripViaExecutor` | Full `RunCopyTo` → `ParseCopyBinaryRows` against real storage-backed table |
