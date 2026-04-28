package protocol

import (
	"encoding/binary"
)

// WriteAuthenticationOk emits 'R' / int32(0) — "no further authentication
// required".
func (fw *FrameWriter) WriteAuthenticationOk() error {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], AuthenticationOK)
	return fw.WriteFrame(MsgAuthentication, buf[:])
}

// WriteAuthenticationCleartextPassword emits 'R' / int32(3). The client
// answers with a PasswordMessage carrying the cleartext password.
func (fw *FrameWriter) WriteAuthenticationCleartextPassword() error {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], AuthenticationCleartextPasswd)
	return fw.WriteFrame(MsgAuthentication, buf[:])
}

// WriteAuthenticationMD5Password emits 'R' / int32(5) / 4-byte salt. The
// client answers with a PasswordMessage of the form
// "md5" + md5_hex(md5_hex(password+username) + salt).
func (fw *FrameWriter) WriteAuthenticationMD5Password(salt [4]byte) error {
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[0:4], AuthenticationMD5Passwd)
	copy(buf[4:8], salt[:])
	return fw.WriteFrame(MsgAuthentication, buf[:])
}

// WriteAuthenticationSASL emits 'R' / int32(10) / [mechanism\0]+ / \0 —
// the list of SASL mechanisms the server is willing to negotiate. The
// client picks one and replies with a SASLInitialResponse. See
// postgres/src/backend/libpq/auth.c:CheckSASLAuth.
func (fw *FrameWriter) WriteAuthenticationSASL(mechanisms []string) error {
	size := 4 + 1
	for _, m := range mechanisms {
		size += len(m) + 1
	}
	payload := make([]byte, 0, size)
	payload = append(payload,
		byte(AuthenticationSASL>>24), byte(AuthenticationSASL>>16),
		byte(AuthenticationSASL>>8), byte(AuthenticationSASL))
	for _, m := range mechanisms {
		payload = append(payload, m...)
		payload = append(payload, 0)
	}
	payload = append(payload, 0)
	return fw.WriteFrame(MsgAuthentication, payload)
}

// WriteAuthenticationSASLContinue emits 'R' / int32(11) / SASL-data.
// The body is mechanism-specific (for SCRAM-SHA-256, the
// server-first-message).
func (fw *FrameWriter) WriteAuthenticationSASLContinue(data []byte) error {
	payload := make([]byte, 0, 4+len(data))
	payload = append(payload,
		byte(AuthenticationSASLCont>>24), byte(AuthenticationSASLCont>>16),
		byte(AuthenticationSASLCont>>8), byte(AuthenticationSASLCont))
	payload = append(payload, data...)
	return fw.WriteFrame(MsgAuthentication, payload)
}

// WriteAuthenticationSASLFinal emits 'R' / int32(12) / SASL-data. The
// body is mechanism-specific (for SCRAM-SHA-256, the
// server-final-message containing the ServerSignature `v=...`).
func (fw *FrameWriter) WriteAuthenticationSASLFinal(data []byte) error {
	payload := make([]byte, 0, 4+len(data))
	payload = append(payload,
		byte(AuthenticationSASLFinal>>24), byte(AuthenticationSASLFinal>>16),
		byte(AuthenticationSASLFinal>>8), byte(AuthenticationSASLFinal))
	payload = append(payload, data...)
	return fw.WriteFrame(MsgAuthentication, payload)
}

// WriteParameterStatus emits 'S' / key\0value\0.
//
// Both key and value must be valid PostgreSQL identifiers / values, i.e.
// not contain NUL bytes. Callers control the values; we don't sanitise.
func (fw *FrameWriter) WriteParameterStatus(key, value string) error {
	payload := make([]byte, 0, len(key)+len(value)+2)
	payload = append(payload, key...)
	payload = append(payload, 0)
	payload = append(payload, value...)
	payload = append(payload, 0)
	return fw.WriteFrame(MsgParameterStatus, payload)
}

// WriteBackendKeyData emits 'K' / int32 pid / int32 secret. Protocol 3.0
// uses a 4-byte secret; 3.2 widens this and would call a different helper
// when we add support.
func (fw *FrameWriter) WriteBackendKeyData(pid, secret uint32) error {
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[0:4], pid)
	binary.BigEndian.PutUint32(buf[4:8], secret)
	return fw.WriteFrame(MsgBackendKeyData, buf[:])
}

// WriteReadyForQuery emits 'Z' / 1-byte transaction status.
func (fw *FrameWriter) WriteReadyForQuery(status TransactionStatus) error {
	return fw.WriteFrame(MsgReadyForQuery, []byte{byte(status)})
}

// WriteParseComplete emits '1' with no payload.
func (fw *FrameWriter) WriteParseComplete() error {
	return fw.WriteFrame(MsgParseComplete, nil)
}

// WriteBindComplete emits '2' with no payload.
func (fw *FrameWriter) WriteBindComplete() error {
	return fw.WriteFrame(MsgBindComplete, nil)
}

// WriteCloseComplete emits '3' with no payload.
func (fw *FrameWriter) WriteCloseComplete() error {
	return fw.WriteFrame(MsgCloseComplete, nil)
}

// WritePortalSuspended emits 's' with no payload.
func (fw *FrameWriter) WritePortalSuspended() error {
	return fw.WriteFrame(MsgPortalSuspended, nil)
}

// WriteParameterDescription emits 't' / int16 count / int32 typeOID[*].
func (fw *FrameWriter) WriteParameterDescription(typeOIDs []uint32) error {
	payload := make([]byte, 0, 2+4*len(typeOIDs))
	payload = appendUint16(payload, uint16(len(typeOIDs)))
	for _, oid := range typeOIDs {
		payload = appendUint32(payload, oid)
	}
	return fw.WriteFrame(MsgParameterDesc, payload)
}

// WriteNoData emits 'n' with no payload.
func (fw *FrameWriter) WriteNoData() error {
	return fw.WriteFrame(MsgNoData, nil)
}

// ErrorField is one (code, value) pair inside an ErrorResponse or
// NoticeResponse body. See docs/design/0002-wire-protocol.md for the list
// of codes the v0 server emits.
type ErrorField struct {
	Code  byte
	Value string
}

// Common ErrorField codes (postgres/src/include/utils/elog.h).
const (
	FieldSeverity         byte = 'S'
	FieldSeverityNonLocal byte = 'V'
	FieldSQLState         byte = 'C'
	FieldMessage          byte = 'M'
	FieldDetail           byte = 'D'
	FieldHint             byte = 'H'
	FieldFile             byte = 'F'
	FieldLine             byte = 'L'
	FieldRoutine          byte = 'R'
)

// WriteErrorResponse emits 'E' / [code,value\0]+ / \0.
func (fw *FrameWriter) WriteErrorResponse(fields []ErrorField) error {
	return fw.writeFieldedMessage(MsgErrorResponse, fields)
}

// WriteNoticeResponse emits 'N' with the same body shape as ErrorResponse.
func (fw *FrameWriter) WriteNoticeResponse(fields []ErrorField) error {
	return fw.writeFieldedMessage(MsgNoticeResponse, fields)
}

func (fw *FrameWriter) writeFieldedMessage(typ byte, fields []ErrorField) error {
	size := 1 // trailing NUL
	for _, f := range fields {
		size += 1 + len(f.Value) + 1
	}
	payload := make([]byte, 0, size)
	for _, f := range fields {
		payload = append(payload, f.Code)
		payload = append(payload, f.Value...)
		payload = append(payload, 0)
	}
	payload = append(payload, 0)
	return fw.WriteFrame(typ, payload)
}

// FieldDescription describes one column of a query result, matching the
// per-attribute layout written by SendRowDescriptionMessage in
// postgres/src/backend/access/common/printtup.c.
type FieldDescription struct {
	Name         string
	TableOID     uint32 // 0 when not from a base table
	ColumnAttNum uint16 // 0 when not from a base table
	TypeOID      uint32
	TypeSize     int16 // pg_type.typlen; -1 for variable-width
	TypeModifier int32 // pg_type.atttypmod; -1 when none
	Format       int16 // 0 = text, 1 = binary
}

// WriteRowDescription emits 'T' / int16 fieldCount / [name\0 tableOID(int32)
// columnAttNum(int16) typeOID(int32) typeSize(int16) typeMod(int32)
// format(int16)]+.
func (fw *FrameWriter) WriteRowDescription(fields []FieldDescription) error {
	size := 2
	for _, f := range fields {
		size += len(f.Name) + 1 + 4 + 2 + 4 + 2 + 4 + 2
	}
	payload := make([]byte, 0, size)
	payload = appendUint16(payload, uint16(len(fields)))
	for _, f := range fields {
		payload = append(payload, f.Name...)
		payload = append(payload, 0)
		payload = appendUint32(payload, f.TableOID)
		payload = appendUint16(payload, f.ColumnAttNum)
		payload = appendUint32(payload, f.TypeOID)
		payload = appendUint16(payload, uint16(f.TypeSize))
		payload = appendUint32(payload, uint32(f.TypeModifier))
		payload = appendUint16(payload, uint16(f.Format))
	}
	return fw.WriteFrame(MsgRowDescription, payload)
}

// WriteDataRow emits 'D' / int16 columnCount / [int32 length / value]+.
// A nil column slice encodes the SQL NULL (length = -1, no bytes).
func (fw *FrameWriter) WriteDataRow(columns [][]byte) error {
	size := 2
	for _, c := range columns {
		size += 4
		if c != nil {
			size += len(c)
		}
	}
	payload := make([]byte, 0, size)
	payload = appendUint16(payload, uint16(len(columns)))
	for _, c := range columns {
		if c == nil {
			payload = appendUint32(payload, 0xFFFFFFFF) // -1 as int32
			continue
		}
		payload = appendUint32(payload, uint32(len(c)))
		payload = append(payload, c...)
	}
	return fw.WriteFrame(MsgDataRow, payload)
}

// WriteCommandComplete emits 'C' with a NUL-terminated tag like "SELECT 1".
// See postgres/src/backend/tcop/cmdtag.c:BuildQueryCompletionString.
func (fw *FrameWriter) WriteCommandComplete(tag string) error {
	payload := make([]byte, 0, len(tag)+1)
	payload = append(payload, tag...)
	payload = append(payload, 0)
	return fw.WriteFrame(MsgCommandComplete, payload)
}

// WriteEmptyQueryResponse emits 'I' with no payload. PostgreSQL sends this
// when the simple Query message body is empty or whitespace-only.
func (fw *FrameWriter) WriteEmptyQueryResponse() error {
	return fw.WriteFrame(MsgEmptyQueryResponse, nil)
}

func appendUint16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}
func appendUint32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
