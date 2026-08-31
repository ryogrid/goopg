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

## Key Files

- `frame.go` (331) — `FrameReader`/`FrameWriter`: the low-level framing
  (4-byte length prefix + type byte), startup packet, and bounded message
  reading/writing, including a `maxPayload` safety limit.
- `messages.go` (559) — the server→client message writers (`Write*` family:
  `WriteAuthenticationOk`, `WriteParameterStatus`, `WriteRowDescription`,
  `WriteDataRow`, `WriteCommandComplete`, `WriteReadyForQuery`,
  `WriteErrorResponse`, …) and the `FieldDescription`/`ErrorField` structs.
- `protocol.go` (122) — protocol constants: `ProtocolVersion3_0`, message type
  bytes (`MsgQuery`, `MsgParse`, `MsgBind`, `MsgExecute`, `MsgDataRow`, …),
  authentication codes, transaction-status enums, `CancelRequestCode`.
- `replication.go` (250) — replication-protocol framing helpers used by the
  walsender/logical receiver (identify system, start replication, timeline
  history, `XLogData`).
- `auth/` — the authentication policy engine and challenge-response methods:
  - `auth.go` (330) — `Policy`, `RuleSet`, `Rule`, `Request`, `Decision`;
    `DefaultPolicy`; `Method*` constants; pg_hba-style rule matching
    (`connTypeMatches`, `nameListMatches`, `addressMatches`).
  - `exchange.go` (320) — the auth exchange driver (decides which method to run
    and drives the challenge/response state machine).
  - `scram.go` (510) — SCRAM-SHA-256 server implementation (RFC 5802) —
    nonce/verifier exchange, `SaltedVerifier`, channel binding.
  - `saslprep.go` / `saslprep_tables.go` (145/862) — SASLprep string
    normalization (RFC 4013) and its Unicode tables.
  - `parser.go` (348) — pg_hba.conf parser.
  - `userstore.go` (316) — password/user store lookup (cleartext, MD5, SCRAM
    verifier) keyed by user/role.

## Public API

```go
// Framing (frame.go)
func NewFrameReader(r io.Reader) *FrameReader
func (fr *FrameReader) ReadStartupPacket() (version uint32, payload []byte, err error)
func (fr *FrameReader) ReadFrame() (Frame, error)          // type byte + payload
func NewFrameWriter(w io.Writer) *FrameWriter
func (fw *FrameWriter) WriteFrame(typ byte, payload []byte) error
func (fw *FrameWriter) DataRowScratch(ncols int) (cells [][]byte, valueBuf []byte)
func ParseStartupParameters(buf []byte) (map[string]string, error)

// Message writers (messages.go)
func (fw *FrameWriter) WriteAuthenticationOk() / WriteAuthenticationSASL(...) / ...
func (fw *FrameWriter) WriteParameterStatus(name, value string)
func (fw *FrameWriter) WriteRowDescription(cols []FieldDescription)
func (fw *FrameWriter) WriteDataRow(cells [][]byte)
func (fw *FrameWriter) WriteCommandComplete(tag string)
func (fw *FrameWriter) WriteReadyForQuery(status byte)
func (fw *FrameWriter) WriteErrorResponse(fields []ErrorField)

// Auth policy (auth/auth.go)
type Policy interface{ ... }
func DefaultPolicy() *RuleSet
type Request struct{ ConnType; User; Database string; Remote net.IP }
type Decision struct{ Method Method; Options ...; Rule Rule }
func (rs *RuleSet) Match(req Request) Decision
```

## Internal structure

- **Framing** — `FrameReader`/`FrameWriter` abstract the 4-byte big-endian
  length + message-type framing of the v3 protocol. The reader enforces
  `maxPayload` so an over-long message is rejected instead of OOM'ing the
  process; the writer buffers and flushes on demand.
- **Message types** — `protocol.go` enumerates every message byte; `messages.go`
  provides the canonical writer for each server→client message. `FieldDescription`
  carries column name, type OID, typmod, format, and the 8-bit field flags.
- **Auth** — `auth/auth.go` implements the policy/RuleSet model: a
  `Request{ConnType, User, Database, Address}` is matched against pg_hba-style
  rules (host/local + address + user/database lists + method), producing a
  `Decision` naming the auth method (`trust`, `password`, `md5`,
  `scram-sha-256`, `reject`, …). `auth/exchange.go` then drives the wire
  exchange; `auth/scram.go` implements SCRAM-SHA-256; `auth/saslprep.go`
  normalizes SASLprep; `auth/userstore.go` looks up stored credentials.
- **Error fields** — `ErrorField` maps to the protocol's `S/ V/ C/ M/ D/ H/
  P/ W/ …` field bytes. `ShouldOutputToClient` (`messages.go`) reproduces PG's
  `should_output_to_client` filtering on `client_min_messages` (elevel ceiling).

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
  than exhausting memory.
- **Auth order matters** — the startup packet is read *before* the connection
  is fully accepted; `ReadStartupPacket` returns the protocol version so
  `postmaster` can reject pre-v3 clients and answer `CancelRequestCode`.
- **Client-min-messages** — `WriteNoticeResponse` consults
  `ShouldOutputToClient` through a per-connection hook, not at every call site;
  ERROR/FATAL/PANIC always send, INFO always sends, and the `error` ceiling
  (21) is cleared unconditionally (the single-emitter pattern from
  `elog.c`).