package protocol

import (
	"encoding/binary"
)

// WriteAuthenticationOk emits 'R' / int32(0) — the "no further authentication
// required" message. v0 always answers AuthenticationOk after parsing the
// startup packet; real auth lives in milestone 3.
func (fw *FrameWriter) WriteAuthenticationOk() error {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], AuthenticationOK)
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
