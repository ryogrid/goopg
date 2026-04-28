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
