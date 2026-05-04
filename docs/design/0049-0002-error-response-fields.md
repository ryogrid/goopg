# Full ErrorResponse Fields — M0049-0002

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

goopg's ErrorResponse carried only S/V (severity), C (SQLSTATE), M (message),
and R (routine). psql uses the `P` (Position) field to render a caret-pointer
line under the offending token:

```
ERROR:  syntax error at or near "FOO"
LINE 1: SELECT * FROM FOO BAR
                          ^
```

Without `P`, psql shows no caret. The parser already tracked byte offsets in
`SyntaxError.Pos`; they just weren't forwarded to the ErrorResponse.

## 2. Design

### 2.1 New field codes (`internal/protocol/messages.go`)

```go
FieldPosition byte = 'P'  // 1-based byte offset into original query
FieldWhere    byte = 'W'  // context stack (PL/pgSQL frame, etc.)
FieldSchema   byte = 's'
FieldTable    byte = 't'
FieldColumn   byte = 'c'
```

These join the existing S/V/C/M/D/H/F/L/R constants.

### 2.2 `syntaxErrorMsg` helper (`internal/server/copy.go`)

```go
func syntaxErrorMsg(err error) (msg string, extra []protocol.ErrorField) {
    var se *parser.SyntaxError
    if errors.As(err, &se) {
        msg = se.Error()
        // Strip the internal "(byte N)" annotation from the message.
        if idx := strings.LastIndex(msg, " (byte "); idx >= 0 {
            msg = msg[:idx]
        }
        if se.Pos >= 0 {
            extra = []protocol.ErrorField{
                {Code: protocol.FieldPosition, Value: strconv.Itoa(se.Pos + 1)},
            }
        }
        return msg, extra
    }
    return err.Error(), nil
}
```

`SyntaxError.Pos` is 0-based (byte index); `FieldPosition` is 1-based per the
PostgreSQL protocol spec.

### 2.3 Simple query parse-error path (`internal/server/dispatch.go`)

```go
msg, extra := syntaxErrorMsg(err)
return s.writeQueryError(w, sqlstate.SyntaxError, msg, extra...)
```

`writeQueryError` now accepts variadic extra fields that are appended after the
standard S/V/C/M/R fields.

### 2.4 Extended query parse-error path (`internal/server/dispatch_extended.go`)

`executeExtendedQueryViaExecutor` extracts the position from
`extendedQueryError.Position` and threads it through
`extendedMessageError.Position` → `writeExtendedMessageError` which includes it
when non-zero.

## 3. Correctness

- `FieldPosition` is only emitted when `SyntaxError.Pos >= 0` (all parser
  errors today have valid positions).
- The `(byte N)` suffix is stripped from the client-facing message to match
  upstream's human-readable format.
- Non-SyntaxError errors (planner, executor) continue to emit no FieldPosition
  since they don't have a query byte offset.

## 4. Tests (`internal/server/error_response_test.go`)

| Test | Coverage |
|---|---|
| `TestErrorResponseDoDPositionField` | **DoD**: ErrorResponse for `SELECT 1 FROM` includes FieldPosition='14'; message has no `(byte N)` suffix |
| `TestSyntaxErrorPositionValue` | Position value is in range `[1, len(query)+1]` |
