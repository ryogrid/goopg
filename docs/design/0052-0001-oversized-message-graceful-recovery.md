# 0052-0001 — Oversized Client Message: Graceful Recovery and Limit Increase

**Status:** accepted  
**Date:** 2026-05-05  
**Covers:** M0052-0001 (root-cause analysis + logging) and M0052-0002 (limit increase)

---

## 1. Problem

HammerDB TPC-H's ORDERS/LINEITEM load uses batched INSERT statements that
accumulate **~4 000 VALUES rows per 1 000-order batch** (average 4 LINEITEM
rows per order).  Each LINEITEM row in the wire message is approximately
250 bytes:

```
(to_timestamp('1993-JAN-07','YYYY-Mon-DD'),'61001','0.10','16296.40',
 '6507','32','N','47836','O','0.00',
 to_timestamp('1994-FEB-15','YYYY-Mon-DD'),
 to_timestamp('1994-FEB-20','YYYY-Mon-DD'),
 'AIR','2','DELIVER IN PERSON','the hockey players cajole...')
```

Average batch payload: `4 000 × 250 + 225 (header) ≈ 1 000 225 bytes` — just
under the original 1 MiB (`1 << 20 = 1 048 576`) limit.  However, the number
of LINEITEM rows per order is **uniform(1,7)**, so ~8% of 1 000-order batches
exceed 4 243 rows and breach the limit.  In a 1 500-batch SF=1 run, each run
has approximately an 8% chance of hitting the limit at some point; run-009
hit it at batch 62.

### Pre-fix behaviour

`internal/protocol/frame.go:ReadFrame()`:
```
if payloadLen > fr.maxRegul {
    return Frame{}, fmt.Errorf("frame %q: payload %d exceeds %d", ...)
}
```

The function read the 5-byte header, detected the oversize, then returned
**without draining the remaining payload bytes**.  The connection stream was
left desynchronised.  `runPostStartupLoop` received the error, logged it at
**Debug** level (invisible in production logs), and returned — closing the TCP
connection.  libpq on the HammerDB side received a bare TCP FIN/RST mid-response
and reported `server closed the connection unexpectedly`.

**Secondary issue:** M0051-0001 inadvertently left `KwSavepoint`/`KwRelease`
constants and `SavepointStmt`/`ReleaseSavepointStmt`/`RollbackToSavepointStmt`
AST node types out of the committed HEAD (they existed only in the working tree),
causing `go build` to fail for anyone compiling from HEAD.

---

## 2. Fixes

### Fix A — Stream re-synchronisation in `ReadFrame` (M0052-0001)

After detecting an oversized payload, drain the bytes from the bufio.Reader
using `io.CopyN(io.Discard, fr.r, int64(payloadLen))` before returning.  The
stream is then in a clean state (positioned at the next message header), and
the error sentinel `ErrFrameTooLarge` is returned instead of a generic
`fmt.Errorf` so callers can distinguish a recoverable size-limit error from
an unrecoverable EOF.

```go
var ErrFrameTooLarge = errors.New("frame payload exceeds MaxRegularMessageLength")
```

### Fix B — Graceful session continue in `runPostStartupLoop` (M0052-0001)

```go
if errors.Is(err, protocol.ErrFrameTooLarge) {
    logger.Info("oversized client message rejected", "err", err)
    if werr := s.writeQueryError(w, sqlstate.ProtocolViolation, err.Error()); ...
    continue   // keep serving
}
```

Without this, even with the stream re-synchronised, the server would drop the
connection on every oversized message.  With the `continue`, the session stays
alive and libpq receives a proper ErrorResponse — but HammerDB still calls
`error "[pg_result $result -error]"` on any non-OK response, terminating the
TCL virtual user.

### Fix C — Observability uplift (M0052-0001)

- `serveConn` deferred block: added `recover()` with ERROR-level logging for
  any unrecovered panic.
- All silent goroutine-exit paths elevated from Debug to Info.

### Fix D — Increase `MaxRegularMessageLength` to 16 MiB (M0052-0002)

Fix B keeps sessions alive when a message is too large, but HammerDB still
fails because it treats any ErrorResponse as fatal.  The root remedy is to
raise the limit high enough that HammerDB's batches never hit it:

| Scenario                              | Bytes       |
|---------------------------------------|-------------|
| Average LINEITEM batch (4 rows/order) | ~1 000 225  |
| Maximum LINEITEM batch (7 rows/order) | ~1 750 225  |
| **`MaxRegularMessageLength` (new)**   | **16 777 216** (16 MiB) |

16 MiB is 9.6× the maximum possible HammerDB batch.  As a DoS upper bound:
a malicious client would need to send 16 MiB per connection to force that
allocation; at 100 connections this is 1.6 GiB, which is bounded by
`GOMEMLIMIT=20GiB` in the bench environment.

The limit is surfaced through:
- `MaxRegularMessageLength const` in `internal/protocol/protocol.go`
- `NewFrameReaderWithLimit(r io.Reader, maxPayload int)` in `frame.go` for
  tests that need a smaller ceiling without generating multi-MiB messages
- `Config.MaxQueryPayloadBytes int` in `internal/server/server.go` (0 =
  use `MaxRegularMessageLength`); consumed in `serveConn`

### Fix E — Parser compile error (M0052-0001)

Committed the missing `KwSavepoint`, `KwRelease` token constants (token.go)
and `SavepointStmt`, `ReleaseSavepointStmt`, `RollbackToSavepointStmt` AST
nodes (ast.go) that M0050-0004 and M0051-0001 referenced but never committed.

---

## 3. Files changed

| File | Change |
|------|--------|
| `internal/protocol/protocol.go` | `MaxRegularMessageLength` 1 MiB → 16 MiB; `ErrFrameTooLarge` sentinel |
| `internal/protocol/frame.go` | drain oversized payload; return `ErrFrameTooLarge` |
| `internal/protocol/frame_test.go` | update `TestFrameReaderRejectsOversizePayload`; add `TestFrameReaderResynchronisesAfterOversizePayload` |
| `internal/server/server.go` | `Config.MaxQueryPayloadBytes`; `serveConn` uses configurable limit; panic recovery; Info exits |
| `internal/server/oversized_message_test.go` | E2E DoD `TestE2EOversizedMessageDoD` with `MaxQueryPayloadBytes=1024` |
| `internal/parser/ast.go` | add `SavepointStmt`, `ReleaseSavepointStmt`, `RollbackToSavepointStmt` |
| `internal/parser/token.go` | add `KwSavepoint`, `KwRelease` constants + keywords map entries |

---

## 4. Upstream reference

PostgreSQL enforces no hard per-message size limit in the backend; the query
string is read into a dynamically-growing `StringInfo` buffer
(`postgres/src/backend/libpq/pqmq.c`, `pq_getmessage`).  goopg retains a limit
for DoS protection but at 16 MiB it is high enough for all standard workloads.

---

## 5. Tests

- `TestFrameReaderRejectsOversizePayload` — frame above limit → `ErrFrameTooLarge`
- `TestFrameReaderResynchronisesAfterOversizePayload` — after drain, next
  normal frame reads successfully
- `TestE2EOversizedMessageDoD` — full server: oversized message → `ErrorResponse`;
  same connection continues to serve `SELECT 1`
