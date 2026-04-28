// Package protocol implements PostgreSQL frontend/backend wire protocol v3
// framing and the small set of backend-emitted messages the v0 server needs.
//
// Scope and growth path are documented in docs/design/0002-wire-protocol.md.
// References to upstream PostgreSQL files use repository-relative paths.
package protocol

// Protocol version constants (mirrors postgres/src/include/libpq/pqcomm.h).
//
// PostgreSQL encodes a protocol version as (major << 16) | minor, big-endian
// on the wire. v0 of goopg accepts exactly 3.0; the wider range is recorded
// here so that future negotiation code (`NegotiateProtocolVersion`) has the
// numbers in one place.
const (
	ProtocolVersion3_0 uint32 = 3 << 16

	ProtocolEarliest = ProtocolVersion3_0
	ProtocolLatest   = ProtocolVersion3_0
)

// Special "version" codes used in the first 4 bytes of a startup packet to
// request something other than a normal session start. Mirrors the
// CANCEL_REQUEST_CODE / NEGOTIATE_SSL_CODE / NEGOTIATE_GSS_CODE constants in
// postgres/src/include/libpq/pqcomm.h.
const (
	CancelRequestCode uint32 = 1234<<16 | 5678
	NegotiateSSLCode  uint32 = 1234<<16 | 5679
	NegotiateGSSCode  uint32 = 1234<<16 | 5680
)

// MaxStartupPacketLength caps the size of a startup packet. Matches
// postgres/src/include/libpq/pqcomm.h:118 (MAX_STARTUP_PACKET_LENGTH).
const MaxStartupPacketLength = 10000

// MaxRegularMessageLength caps every post-startup message's payload. The
// upstream codebase does not impose a single ceiling here; we pick 1 MiB as
// a defensive limit to keep a malicious client from forcing us to allocate
// arbitrarily large buffers. The COPY path (milestone 6) will lift this for
// data rows specifically.
const MaxRegularMessageLength = 1 << 20

// Backend message type bytes. Mirrors PqMsg_* in
// postgres/src/include/libpq/protocol.h.
const (
	MsgAuthentication     byte = 'R'
	MsgParameterStatus    byte = 'S'
	MsgBackendKeyData     byte = 'K'
	MsgReadyForQuery      byte = 'Z'
	MsgParseComplete      byte = '1'
	MsgBindComplete       byte = '2'
	MsgCloseComplete      byte = '3'
	MsgErrorResponse      byte = 'E'
	MsgNoticeResponse     byte = 'N'
	MsgRowDescription     byte = 'T'
	MsgParameterDesc      byte = 't'
	MsgDataRow            byte = 'D'
	MsgCommandComplete    byte = 'C'
	MsgPortalSuspended    byte = 's'
	MsgNoData             byte = 'n'
	MsgEmptyQueryResponse byte = 'I'
)

// Frontend message type bytes recognised so far. The full set is in
// postgres/src/include/libpq/protocol.h; goopg widens the set as milestones
// land.
const (
	MsgTerminate       byte = 'X'
	MsgQuery           byte = 'Q'
	MsgPasswordMessage byte = 'p'
	MsgParse           byte = 'P'
	MsgBind            byte = 'B'
	MsgDescribe        byte = 'D'
	MsgExecute         byte = 'E'
	MsgSync            byte = 'S'
	MsgFlush           byte = 'H'
	MsgClose           byte = 'C'
)

// AuthenticationRequest subcodes (postgres/src/include/libpq/protocol.h:74).
const (
	AuthenticationOK              uint32 = 0
	AuthenticationCleartextPasswd uint32 = 3
	AuthenticationMD5Passwd       uint32 = 5
	AuthenticationGSS             uint32 = 7
	AuthenticationGSSCont         uint32 = 8
	AuthenticationSASL            uint32 = 10
	AuthenticationSASLCont        uint32 = 11
	AuthenticationSASLFinal       uint32 = 12
)

// TransactionStatus byte sent in ReadyForQuery.
type TransactionStatus byte

const (
	TxStatusIdle                TransactionStatus = 'I'
	TxStatusInTransaction       TransactionStatus = 'T'
	TxStatusInFailedTransaction TransactionStatus = 'E'
)
