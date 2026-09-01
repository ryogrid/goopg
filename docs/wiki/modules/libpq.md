# Module: `internal/libpq`

The PostgreSQL **v3 wire protocol** implementation — the framing layer, message
types, and connection startup/authentication handshake. It is a faithful Go
port of PG's `src/backend/libpq` plus the `src/interfaces/libpq` message
constants, and it is what makes goopg a drop-in wire-compatible server for any
`psql`/JDBC/`pgx` client.

It is deliberately a **leaf protocol layer**: it knows nothing about SQL,
catalogs, or execution. Higher layers (`internal/postmaster`) drive the
connection lifecycle; the executor writes result rows through
`FrameWriter.DataRow*`; the replication streams use `replication.go`.
Package total: **4,093 LOC** across 11 production files (4 parent + 7 `auth/`).

## Key Files

| File | LOC | Role |
|------|-----|------|
| `messages.go` | 559 | Server→client message writers (`Write*` family: `WriteAuthenticationOk`, `WriteParameterStatus`, `WriteRowDescription`, `WriteDataRow`, `WriteCommandComplete`, `WriteReadyForQuery`, `WriteErrorResponse`, …). `FieldDescription`/`ErrorField` structs, `ShouldOutputToClient` filter. |
| `frame.go` | 331 | `FrameReader`/`FrameWriter`: the low-level framing (4-byte length prefix + type byte), startup packet, bounded message reading/writing, `DataRowScratch` pre-sizing. |
| `replication.go` | 250 | Replication-protocol framing helpers: `EncodeWALData`, `EncodeKeepalive`, `EncodeStandbyStatusUpdate`, `DecodeReplicationMessage`, `WALDataMessage`/`KeepaliveMessage`/`StandbyStatusUpdate` structs, pgTimestamp conversion. |
| `protocol.go` | 122 | Protocol constants: `ProtocolVersion3_0`, message type bytes (`MsgQuery`, `MsgParse`, `MsgBind`, `MsgExecute`, `MsgDataRow`, …), authentication codes (`AuthenticationOK`, `AuthenticationSASL`, …), transaction-status enums, `CancelRequestCode`, `MaxStartupPacketLength`/`MaxRegularMessageLength`. |
| `auth/auth.go` | 330 | `Policy` interface, `RuleSet`/`Rule`/`Request`/`Decision` structs; `DefaultPolicy`; pg_hba-style rule matching (`connTypeMatches`, `nameListMatches`, `addressMatches`); `Method*` constants. |
| `auth/exchange.go` | 320 | The auth exchange driver — decides which method to run and drives the challenge/response state machine. |
| `auth/scram.go` | 510 | SCRAM-SHA-256 server implementation (RFC 5802) — nonce/verifier exchange, `SaltedVerifier`, channel binding. |
| `auth/saslprep.go` | 145 | SASLprep string normalization (RFC 4013) — high-level API. |
| `auth/saslprep_tables.go` | 862 | SASLprep Unicode tables (prohibited characters, mapping categories). |
| `auth/parser.go` | 348 | pg_hba.conf parser — reads rules from file, builds `RuleSet`. |
| `auth/userstore.go` | 316 | Password/user store lookup (cleartext, MD5, SCRAM verifier) keyed by user/role. |

## Public API

### Framing (`frame.go`)

```go
func NewFrameReader(r io.Reader) *FrameReader
func NewFrameReaderWithLimit(r io.Reader, maxPayload int) *FrameReader
func (fr *FrameReader) ReadStartupPacket() (version uint32, payload []byte, err error)
func (fr *FrameReader) ReadFrame() (Frame, error)          // type byte + payload
func (fw *FrameWriter) WriteFrame(typ byte, payload []byte) error
func (fw *FrameWriter) WriteRaw(buf []byte) error
func (fw *FrameWriter) Flush() error
func (fw *FrameWriter) DataRowScratch(ncols int) (cells [][]byte, valueBuf []byte)
func (fw *FrameWriter) PutDataRowScratch(cells [][]byte)
func ParseStartupParameters(buf []byte) (map[string]string, error)
```

`FrameReader` exposes `OnBeforeRead`/`OnAfterRead` hooks wired by the postmaster
for `activity.ClientRead` wait-event tracking. `Frame` carries a `Payload` slice
borrowed from the reader's internal buffer — valid only until the next
`ReadFrame` call; callers that need it to outlive the next read must copy.

### Message writers (`messages.go`)

```go
func (fw *FrameWriter) WriteStartupMessage(params map[string]string) error
func (fw *FrameWriter) WriteQuery(sql string) error
func (fw *FrameWriter) WriteAuthenticationOk() error
func (fw *FrameWriter) WriteAuthenticationCleartextPassword() error
func (fw *FrameWriter) WriteAuthenticationMD5Password(salt [4]byte) error
func (fw *FrameWriter) WriteAuthenticationSASL(mechanisms []string) error
func (fw *FrameWriter) WriteAuthenticationSASLContinue(serverFirstMsg string) error
func (fw *FrameWriter) WriteAuthenticationSASLFinal(serverFinalMsg string) error
func (fw *FrameWriter) WriteParameterStatus(name, value string) error
func (fw *FrameWriter) WriteBackendKeyData(pid int32, secretKey int32) error
func (fw *FrameWriter) WriteReadyForQuery(status byte) error
func (fw *FrameWriter) ReadyForQuery() error
func (fw *FrameWriter) ReadyForQueryAfterError() error
func (fw *FrameWriter) WriteParseComplete()
func (fw *FrameWriter) WriteBindComplete()
func (fw *FrameWriter) WriteCloseComplete()
func (fw *FrameWriter) WritePortalSuspended()
func (fw *FrameWriter) WriteParameterDescription(paramOIDs []uint32)
func (fw *FrameWriter) WriteNoData()
func (fw *FrameWriter) WriteCopyInResponse(cols []FieldDescription, format byte)
func (fw *FrameWriter) WriteCopyOutResponse(cols []FieldDescription, format byte)
func (fw *FrameWriter) WriteCopyData(data []byte)
func (fw *FrameWriter) WriteCopyDone()
func (fw *FrameWriter) WriteErrorResponse(fields []ErrorField) error
func (fw *FrameWriter) WriteNoticeResponse(fields []ErrorField) error
func (fw *FrameWriter) WriteRowDescription(cols []FieldDescription) error
func (fw *FrameWriter) WriteDataRow(cells [][]byte) error
func (fw *FrameWriter) WriteDataRowReuse(cells [][]byte) error
func (fw *FrameWriter) WriteCommandComplete(tag string) error
func (fw *FrameWriter) WriteEmptyQueryResponse() error
func (fw *FrameWriter) WriteNotificationResponse(pid uint32, channel, payload string) error
func (fw *FrameWriter) WriteCopyBothResponse(cols []FieldDescription, format byte) error
func ShouldOutputToClient(severity, clientMin string) bool
```

### Auth policy (`auth/auth.go`)

```go
type Method int  // MethodTrust, MethodPassword, MethodMD5, MethodSCRAMSHA256, MethodReject, ...
type ConnType int  // ConnLocal, ConnHost, ConnHostSSL, ConnHostNoSSL, ConnHostGSSEnc, ...
type Policy interface { MatchRequest(Request) Decision }
type Request struct{ ConnType; User, Database string; Remote net.IP }
type Decision struct{ Method Method; Options map[string]string; Rule Rule }
type RuleSet struct { rules []Rule }
func DefaultPolicy() *RuleSet
func (rs *RuleSet) Match(req Request) Decision
type Rule struct{ ConnType ConnType; Database []string; User []string; Addr *net.IPNet; Method Method; Options map[string]string }
type ErrRejected struct / ErrMethodUnsupported struct / ParseError struct
```

### Replication (`replication.go`)

```go
const ReplMsgWALData byte = 'w' / ReplMsgKeepalive = 'k' / ReplMsgStandbyStatus = 'r' / ReplMsgHotStandbyFeedback = 'h'
func PgTimestampMicros(t time.Time) int64
func PgTimestampToTime(micros int64) time.Time
func EncodeWALData(startLSN, endLSN uint64, sendTime time.Time, walBytes []byte) []byte
func EncodeKeepalive(walEnd uint64, sendTime time.Time, replyRequested bool) []byte
func EncodeStandbyStatusUpdate(writeLSN, flushLSN, applyLSN uint64, sendTime time.Time, replyRequested bool) []byte
func DecodeReplicationMessage(payload []byte) (typ byte, msg any, err error)
type WALDataMessage struct{ StartLSN, EndLSN uint64; SendTime time.Time; Payload []byte }
type KeepaliveMessage struct{ WalEnd uint64; SendTime time.Time; ReplyRequested bool }
type StandbyStatusUpdate struct{ WriteLSN, FlushLSN, ApplyLSN uint64; SendTime time.Time; ReplyRequested bool }
```

### SCRAM (`auth/scram.go`)

```go
type SCRAMSecret struct{ ... }
func NewSCRAMSecret(password string) (*SCRAMSecret, error)
func NewSCRAMSecretWithIterations(password string, iterations int) (*SCRAMSecret, error)
func ParseSCRAMSecret(secret string) (*SCRAMSecret, error)
func (s *SCRAMSecret) EncodedSalt() string / VerifySCRAMSecretFromPassword(pw) bool
type SCRAMServer struct{ ... }
func NewSCRAMServer(username string, secret *SCRAMSecret) (*SCRAMServer, error)
func (s *SCRAMServer) Step(input []byte) ([]byte, bool, error)
func pbkdf2HMACSHA256(password, salt []byte, iter, keyLen int) []byte
```

## Internal structure

### Framing (`frame.go`)

```mermaid
flowchart TD
    subgraph Client
        CQ[WriteQuery / WriteStartupMessage]
    end
    subgraph Server
        FR[FrameReader]
        FR --> RSP[ReadStartupPacket → version + payload]
        FR --> RF[ReadFrame → Frame{Type, Payload}]
        FW[FrameWriter]
        FW --> WF[WriteFrame(type, payload)]
        FW --> DRS[DataRowScratch → pre-sized cells + valueBuf]
        FW --> F[Flush]
    end
```

Every v3 message is `[type byte][4-byte big-endian length including type][body]`.
The startup packet has NO leading type byte: `[4-byte length][4-byte
version][key-NUL-value-NUL]*[NUL]`. `FrameReader` enforces `maxStart`
(10,000 — `MaxStartupPacketLength`) and `maxRegul` (16 MiB —
`MaxRegularMessageLength`) so an over-long message is rejected with
`ErrFrameTooLarge` instead of OOM'ing the process. `FrameWriter` buffers
through `bufio.Writer` and flushes on demand.

`DataRowScratch(ncols)` pre-sizes the row buffer so the common `SELECT` path
does not re-append per cell — returns `cells [][]byte` (pointers into
`valueBuf`) and `valueBuf []byte` (the contiguous scratch space). Callers
write column values into `valueBuf` and set `cells[i]` slices; `PutDataRowScratch`
reclaims the scratch for reuse.

### Message types (`protocol.go`)

Client→Server:

| Byte | Message | Payload |
|------|---------|---------|
| `Q` | MsgQuery | SQL string + NUL |
| `P` | MsgParse | prepared stmt name + SQL + param OIDs |
| `B` | MsgBind | portal name + stmt name + param formats + params + result formats |
| `D` | MsgDescribe | 'S'/'P' + name |
| `E` | MsgExecute | portal name + maxRows |
| `H` | MsgFlush | — |
| `S` | MsgSync | — |
| `C` | MsgClose | 'S'/'P' + name |
| `X` | MsgTerminate | — |
| `d` | MsgCopyData | raw bytes |
| `c` | MsgCopyDone | — |
| `f` | MsgCopyFail | error message + NUL |
| `p` | MsgPasswordMessage | password + NUL |

Server→Client:

| Byte | Message | Payload |
|------|---------|---------|
| `R` | MsgAuthentication | int32 method code + method-specific bytes |
| `S` | MsgParameterStatus | name NUL value NUL |
| `K` | MsgBackendKeyData | int32 PID + int32 secret key |
| `Z` | MsgReadyForQuery | byte txStatus (I/T/E) |
| `1` | MsgParseComplete | — |
| `2` | MsgBindComplete | — |
| `3` | MsgCloseComplete | — |
| `E` | MsgErrorResponse | field-set (S/V/C/M/D/H/P/W/…) |
| `N` | MsgNoticeResponse | field-set (same format as ErrorResponse) |
| `G` | MsgCopyInResponse | format + col count + per-col formats |
| `H` | MsgCopyOutResponse | format + col count + per-col formats |
| `W` | MsgCopyBothResponse | format + col count + per-col formats |
| `T` | MsgRowDescription | col count + per-col FieldDescriptions |
| `t` | MsgParameterDescription | param OID count + OIDs |
| `D` | MsgDataRow | col count + per-col length+value pairs |
| `d` | MsgCopyData | raw bytes (in CopyBoth) |
| `c` | MsgCopyDone | — |
| `C` | MsgCommandComplete | tag string + NUL |
| `s` | MsgPortalSuspended | — |
| `n` | MsgNoData | — |
| `I` | MsgEmptyQueryResponse | — |
| `A` | MsgNotificationResponse | int32 PID + channel NUL + payload NUL |

### Error fields (`messages.go`)

`ErrorField` maps to the protocol's field bytes: `FieldSeverity` (`S`),
`FieldSeverityNonLocal` (`V`), `FieldSQLState` (`C`), `FieldMessage` (`M`),
`FieldDetail` (`D`), `FieldHint` (`H`), `FieldPosition` (`P`),
`FieldWhere` (`W`), `FieldSchema` (`s`), `FieldTable` (`t`),
`FieldColumn` (`c`), `FieldFile` (`F`), `FieldLine` (`L`),
`FieldRoutine` (`R`). `ShouldOutputToClient` reproduces PG's
`should_output_to_client` filtering on `client_min_messages` (elevel ceiling):
ERROR/FATAL/PANIC always send, INFO always sends, and the `error` ceiling (21)
is cleared unconditionally (the single-emitter pattern from `elog.c`).

### Replication framing (`replication.go`)

Inside CopyData frames during streaming replication:

```
Server→Client: 'w' | startLSN(8) | endLSN(8) | sendTime(8) | walBytes...
Server→Client: 'k' | walEnd(8) | sendTime(8) | replyRequested(1)
Client→Server: 'r' | writeLSN(8) | flushLSN(8) | applyLSN(8) | sendTime(8) | replyRequested(1)
Client→Server: 'h' | applyLSN(8) | applyTime(8) | catalogXmin(4) | epoch(4)   (deferred)
```

`sendTime` is microseconds since 2000-01-01 UTC (`pgEpoch`), matching
upstream's `TimestampTz`. `DecodeReplicationMessage` dispatches on the inner
type byte and returns the decoded struct. `EncodeWALData`/`EncodeKeepalive`/
`EncodeStandbyStatusUpdate` build the raw payload bytes.

### Auth subsystem (`auth/`)

```mermaid
flowchart TD
    subgraph Client
        C[StartupMessage]
    end
    subgraph Server
        P[Policy.Match Request]
        P --> D{Decision}
        D -->|trust| OK[AuthenticationOk]
        D -->|password| PW[AuthenticationCleartextPassword]
        D -->|md5 + salt| MD5[AuthenticationMD5Password]
        D -->|scram-sha-256| SASL[AuthenticationSASL mechanisms]
        D -->|reject| REJ[ErrorResponse FATAL]
        SASL --> E[auth/exchange.go state machine]
        E --> SCRAM[scram.go: SaltedVerifier challenge]
        SCRAM --> OK
    end
```

`auth/auth.go` implements the policy/RuleSet model: a `Request{ConnType, User,
Database, Address}` is matched against pg_hba-style rules (host/local + address +
user/database lists + method), producing a `Decision` naming the auth method.
`auth/exchange.go` drives the wire exchange; `auth/scram.go` implements
SCRAM-SHA-256 (RFC 5802) with `SaltedVerifier`, nonce, and channel binding;
`auth/saslprep.go` normalizes SASLprep (RFC 4013); `auth/parser.go` parses
`pg_hba.conf`; `auth/userstore.go` looks up stored credentials (cleartext, MD5
hash, SCRAM verifier) keyed by user/role.

The auth method constants in `auth.go` are: `MethodTrust`, `MethodPassword`,
`MethodMD5`, `MethodSCRAMSHA256`, `MethodReject` (plus `MethodPeer`/
`MethodCert`/`MethodGSS` stubs that return `ErrMethodUnsupported`).

### pg_hba.conf parsing (`auth/parser.go`)

`ParseHBAFile`/`ParseHBAReader` tokenize each line (`tokenize`), skip
comments/blank lines (`looksLikeRule`), expand `include`/`include_dir`/
`include_if_exists` directives (`includeDirective`, `resolveIncludePath`), and
build a `Rule` per entry (`parseRule`). `parseAddressField` handles IPv4/IPv6
CIDR and the `samehost`/`samenet` keywords; `parseMethodAndOptions` parses the
method plus `password_encryption`-style options into the `Options` map.

### SCRAM state machine (`auth/scram.go`)

```mermaid
sequenceDiagram
    participant C as Client
    participant S as SCRAMServer
    C->>S: client-first-message (n,,n=user,r=nonce)
    S->>S: handleClientFirst: verify user, load SCRAMSecret
    S->>S: generate server nonce, salt, iterations
    S-->>C: server-first-message (r=nonce,s=salt,i=iter)
    C->>S: client-final-message (c=cbind,r=nonce,p=proof)
    S->>S: handleClientFinal: verify channel binding, verify proof
    alt proof valid
        S-->>C: server-final-message (v=signature) → AuthenticationOk
    else proof invalid
        S-->>C: ErrorResponse 28P01
    end
```

The `SCRAMSecret` holds `salt []byte`, `iterations int`, and the stored
`StoredKey`/`ServerKey` derived from the password. `VerifySCRAMSecretFromPassword`
re-derives and compares. The `Step` method drives the state machine
(`scramState` constants: initial → client-first → client-final → done).

## Dependencies

- **Used by** — `internal/postmaster` (connection accept + protocol loop),
  `internal/replication` (walsender framing), `internal/executor`
  (`client_min_messages` filtering hook).
- **Uses** — `internal/utils/misc` (GUC values), `internal/utils/errcodes`
  (SQLSTATE constants). `auth/` uses `internal/crypto`-style primitives only if
  needed for SCRAM/MD5; it avoids importing higher layers.

## Notable patterns / gotchas

- **Length-prefix discipline** — every v3 message is `[type][4-byte len][body]`.
  `WriteFrame` computes the length from the payload; `DataRowScratch` pre-sizes
  the row buffer so the common `SELECT` path does not re-append per cell.

- **`maxPayload` guard** — `NewFrameReaderWithLimit` bounds message size; a
  client sending a multi-GB message is rejected with `ErrFrameTooLarge` rather
  than exhausting memory. Startup packets are limited to 10,000 bytes; regular
  messages to 16 MiB.

- **Auth order matters** — the startup packet is read *before* the connection
  is fully accepted; `ReadStartupPacket` returns the protocol version so
  `postmaster` can reject pre-v3 clients and answer `CancelRequestCode`.

- **`Frame.Payload` is borrowed** — the payload slice points into the reader's
  internal buffer and is only valid until the next `ReadFrame`. Callers must
  copy if they need it to outlive the next read — this is the #1 source of
  data-corruption bugs in new protocol handlers.

- **Client-min-messages** — `WriteNoticeResponse` consults
  `ShouldOutputToClient` through a per-connection hook, not at every call site;
  ERROR/FATAL/PANIC always send, INFO always sends, and the `error` ceiling
  (21) is cleared unconditionally (the single-emitter pattern from `elog.c`).

- **`WriteQuery` appends trailing NUL** — the simple-query 'Q' frame requires
  the SQL string to be NUL-terminated. `WriteQuery` adds it automatically,
  matching upstream's protocol.

- **`WriteStartupMessage` vs `WriteFrame`** — startup packets have no leading
  type byte and use a different framing (length includes the 4-byte version,
  not a type byte). `WriteStartupMessage` handles this separately from the
  regular `WriteFrame` path.

- **pgEpoch is 2000-01-01 UTC** — replication timestamps are microseconds since
  this epoch, not Go's standard epoch (1970). `PgTimestampMicros`/`PgTimestampToTime`
  provide the conversion.

- **Replication inner-message type bytes** — the first byte of a CopyData
  payload during streaming replication is NOT a protocol message type byte but
  an inner replication type (`'w'`/`'k'`/`'r'`/`'h'`). `DecodeReplicationMessage`
  dispatches on these.

- **`WriteDataRowReuse`** — reuses the `[numCols][4-byte-len][value]…` format
  but with caller-supplied slices; used by the executor's batch path to avoid
  allocating per-row metadata while the row data is already in the value buffer.

- **`ErrFrameTooLarge` is a sentinel** — returned by `ReadFrame` when the
  payload exceeds the configured limit. The postmaster drains the oversized
  frame to resync the stream, then emits `08P01`.

- **SCRAM-SHA-256 requires the stored verifier** — `Password` auth with a
  cleartext password cannot be upgraded to SCRAM on the fly unless the store
  holds a `SCRAMSecret`; `NewSCRAMSecret` exists so role DDL can persist a
  verifier at `CREATE ROLE … PASSWORD` time. A mismatch here (storing only the
  cleartext, then trying SCRAM) yields a spurious 28P01.

- **`should_output_to_client` is not a simple `<` comparison** — the elevel
  ceiling check reproduces PG's `errstart` logic: INFO and below always pass,
  ERROR and above always pass, and the `error` level is special-cased. Porting
  it as a plain threshold mis-filters NOTICE in `client_min_messages=error`.

- **Rule matching order is first-match-wins** — `RuleSet.Match` iterates rules
  in file order; a catch-all `host all all all md5` placed before a restrictive
  rule shadows it, exactly like upstream pg_hba.conf. Reordering rules changes
  the auth outcome with no error.

## Protocol version and startup handling (`protocol.go` / `frame.go`)

The `ProtocolVersion3_0` constant is `196608` (0x00030000). `ReadStartupPacket`
checks the version word:
- `>= 3.0` → accepts; version is returned so the postmaster can branch.
- `< 3.0` → rejected (pre-v3 clients).
- `CancelRequestCode` (80877102, 0x04D2162E) → the postmaster answers with a
  `CancelRequest` response (no auth, just `K` with the backend key) rather than
  starting a normal session.
- `SSLRequestCode` (80877103) / `GSSENCRequestCode` (80877104) → negotiation
  requests handled by the accept loop before the normal handshake.

The startup packet body is a sequence of `key\0value\0` pairs terminated by a
final `\0`. `ParseStartupParameters` splits it into a map, lowercasing keys
(`user`, `database`, `application_name`, `client_encoding`, `options`,
`replication`, etc.). Unknown parameters are ignored (matching libpq).

`MaxStartupPacketLength` (10,000) bounds the entire startup packet; a longer
one is `ErrFrameTooLarge`. `MaxRegularMessageLength` (16 MiB) bounds every
non-startup message.

## Authentication method constants (`auth/auth.go`)

```go
const (
    MethodTrust       Method = iota
    MethodPassword
    MethodMD5
    MethodSCRAMSHA256
    MethodReject
    MethodPeer   // unsupported
    MethodCert   // unsupported
    MethodGSS    // unsupported
)
```

`ConnType` mirrors the pg_hba.conf first field: `ConnLocal`, `ConnHost`,
`ConnHostSSL`, `ConnHostNoSSL`, `ConnHostGSSEnc`, `ConnHostGSSEncNo`.

The `Decision` struct returned by `Policy.MatchRequest` names the method plus
any method-specific options (e.g. `password_encryption`, `scram_iterations`).
`ErrRejected` carries the reject reason (usually "no pg_hba.conf entry matches");
the postmaster renders it as FATAL 28000.

## SASLprep normalization (`auth/saslprep.go`)

`SASLprep` implements RFC 4013 over the user-provided password: it maps
non-ASCII space characters to ASCII space (C.1.2), removes commonly mapped
characters (B.1), normalizes to NFKC, and rejects prohibited characters
(C.1.2/C.2/C.3/C.4/C.5/C.6/C.7/C.8/C.9). The table data lives in
`saslprep_tables.go` (862 LOC of generated category ranges).

The SCRAM path applies SASLprep to the client-supplied password before
deriving the SaltedPassword — PG does the same, so a password with a
prohibited character (e.g. a non-breaking space) fails both sides identically.

## MD5 vs SCRAM verifier storage (`auth/userstore.go`)

The `UserStore` interface:

```go
type UserStore interface {
    LookupSCRAM(user string) (*SCRAMSecret, bool)
    LookupMD5(user string) ([16]byte, bool)  // MD5 hash of "password<user>"
    LookupPassword(user string) (string, bool) // cleartext fallback
}
```

`Exchange` selects the method and pulls the matching credential:
- SCRAM-SHA-256 → `LookupSCRAM` → `SCRAMServer.Step`.
- MD5 → `LookupMD5` → verify `md5(password+user)` against the stored hash.
- Password (cleartext) → `LookupPassword` → constant-time compare.

A role's `Password` field in the catalog stores either `SCRAM-SHA-256$...`
(the verifier string) or `md5<hex>`. `ParseSCRAMSecret` parses the former;
the catalog's `RoleAttrs.Password` carries whichever form was set at
`CREATE ROLE ... PASSWORD`.

## Key flow: a complete client connection

```mermaid
sequenceDiagram
    participant C as Client (psql)
    participant FR as FrameReader
    participant PM as postmaster
    participant AU as auth.Exchange
    participant SS as SCRAMServer
    participant FW as FrameWriter
    C->>FR: startup packet (v3.0, user, database)
    FR->>PM: ReadStartupPacket → version + params
    PM->>PM: checkAuth policy match
    PM->>AU: Exchange(Decision{scram}, store, user)
    AU->>SS: NewSCRAMServer(user, secret)
    AU->>FW: WriteAuthenticationSASL(["SCRAM-SHA-256"])
    C->>FR: SASLInitialResponse (client-first)
    AU->>SS: Step(client-first) → server-first
    AU->>FW: WriteAuthenticationSASLContinue(server-first)
    C->>FR: SASLResponse (client-final)
    AU->>SS: Step(client-final) → server-final or error
    alt valid proof
        AU->>FW: WriteAuthenticationSASLFinal(server-final)
    else invalid
        AU->>FW: WriteErrorResponse(28P01)
    end
    PM->>FW: WriteAuthenticationOk
    PM->>FW: WriteParameterStatus x N
    PM->>FW: WriteBackendKeyData(pid, secret)
    PM->>FW: WriteReadyForQuery
```

## Message size and framing internals

`FrameWriter` wraps a `bufio.Writer`. `WriteFrame` computes the 4-byte
big-endian length as `len(payload) + 4` (the length includes the length field
itself but NOT the type byte), writes the type byte, the length, then the
payload. `WriteRaw` writes pre-assembled bytes without framing — used by the
replication walsender path where the caller has already built the full
`CopyData` frame. `Flush` pushes the buffer to the underlying writer.

`FrameReader` reads the 1-byte type + 4-byte length, then `io.ReadFull` for
the payload. The `maxPayload` bound is enforced against the length field
before allocation. `OnBeforeRead`/`OnAfterRead` are invoked around each
`ReadFrame` so the postmaster can mark `ClientRead` wait events.

## FieldDescription and RowDescription

`FieldDescription` is the per-column metadata written in a RowDescription
(`T`) frame:

```go
type FieldDescription struct {
    Name      string
    TableOID  uint32
    AttrNum   int16
    TypeOID   uint32
    TypeLen   int16
    TypeMod   int32
    Format    int16 // 0 = text, 1 = binary
}
```

`WriteRowDescription` emits each field as:
`name\0` + `tableOID(4)` + `attnum(2)` + `typeOID(4)` + `typlen(2)` +
`typmod(4)` + `format(2)`.

The `TypeLen` comes from the catalog `Type` struct (4 for int4, -1 for
varlena). `TypeMod` carries typmod (e.g. `varchar(10)` → 14, encoded as
`10 + VARHDRSZ`).

## ErrorResponse field-set

`writeFieldedMessage` is the shared writer for `E` (ErrorResponse) and `N`
(NoticeResponse) frames. It emits each `ErrorField` as:
`fieldType(1)` + `value\0`. The message ends with a NUL terminator byte
(`\0`).

`severityElevel`/`clientMinMessagesElevel` convert severity strings
(`"ERROR"`, `"WARNING"`, `"NOTICE"`, …) to elevel integers for the
`ShouldOutputToClient` comparison. The severity strings used in the frame
are `"ERROR"`, `"FATAL"`, `"PANIC"`, `"WARNING"`, `"NOTICE"`, `"INFO"`,
`"DEBUG1"`…`"DEBUG5"`, `"LOG"`, `"HINT"`, `"DETAIL"`.

## SCRAM protocol details

### SaltedVerifier

`SCRAMSecret` (scram.go:53) stores the salt, iterations, and the server key:

```go
type SCRAMSecret struct {
    Salt        []byte
    Iterations  int
    StoredKey   []byte
    ServerKey   []byte
}
```

`NewSCRAMSecret` derives the verifier from a password using PBKDF2-HMAC-SHA256
(10,000 iterations by default, matching PG's `scram_iterations` default).
`NewSCRAMSecretWithIterations` allows the iterations to be customized.
`ParseSCRAMSecret` parses the `SCRAM-SHA-256$<salt>:<storedkey>:<serverkey>`
catalog format.

### The exchange state machine

`Exchange` (exchange.go:35) is the top-level driver. It switches on the
`Decision.Method` and runs the matching sub-routine:

- `runTrust` — sends `AuthenticationOk` immediately.
- `runCleartext` — sends `AuthenticationCleartextPassword`, reads the
  password message, compares with the store's cleartext.
- `runMD5` — sends `AuthenticationMD5Password` with a random 4-byte salt,
  reads the salted hash, compares with the store's MD5.
- `runSCRAM` — drives `SCRAMServer.Step` through the SASL challenge.

`readSASLInitial`/`readSASLResponse`/`readPasswordMessage` are the frame
readers for the password/auth messages.

### Channel binding

`validNoBindingChannelAttr` checks the `c=` attribute in the client-final
message. The server accepts `c=biws` (base64 of "n,," — no channel binding)
and `c=,,` (the actual channel-binding data). The server echoes the client's
choice in its proof verification.

## `Frame` and `FrameReader` fields

```go
type Frame struct {
    Type    byte
    Payload []byte  // borrowed from the reader buffer
}

type FrameReader struct {
    r         *bufio.Reader
    maxStart  int   // 10,000
    maxRegul  int   // 16 MiB
    buf       []byte  // internal read buffer (payload alias)
    OnBeforeRead func()
    OnAfterRead  func()
}
```

`ReadStartupPacket` reads the 4-byte length, validates it against `maxStart`,
then reads the version + parameter block. `ParseStartupParameters` parses the
`key\0value\0` sequence into a map, rejecting a malformed packet (unterminated
key or value) with an error.

`ReadFrame` reads the type byte + 4-byte length (validated against
`maxRegul`), then `io.ReadFull` into `buf` (grown as needed). The returned
`Frame.Payload` aliases `buf` — valid only until the next `ReadFrame`.

## Gotcha: SSL/negotiation frame handling

The accept loop must distinguish a startup packet from an SSL request before
calling `ReadStartupPacket` — the SSLRequestCode (80877103) and GSSENC
(80877104) are 8-byte packets (length + code) with NO parameter block. The
postmaster's accept loop peeks the first 8 bytes, handles negotiation, and
then calls `ReadStartupPacket` only for genuine v3 startups.