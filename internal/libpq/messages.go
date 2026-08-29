package libpq

import (
	"encoding/binary"
	"strings"
)

// WriteStartupMessage emits the v3 protocol startup packet. Unlike
// regular messages this has no leading type byte:
//
//	int32 totalLen | int32 protocolVersion | (key NUL value NUL)* | NUL
//
// Used by client-side code (e.g., the standby walreceiver) that needs
// to drive a connection through the same handshake the test helper
// `writeStartupPacket` performs.
func (fw *FrameWriter) WriteStartupMessage(params map[string]string) error {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, ProtocolVersion3_0)
	for k, v := range params {
		body = append(body, k...)
		body = append(body, 0)
		body = append(body, v...)
		body = append(body, 0)
	}
	body = append(body, 0)
	pkt := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(pkt[:4], uint32(4+len(body)))
	copy(pkt[4:], body)
	if _, err := fw.w.Write(pkt); err != nil {
		return err
	}
	return fw.Flush()
}

// WriteQuery emits a simple-query 'Q' frame. The trailing NUL the
// protocol requires is appended automatically.
func (fw *FrameWriter) WriteQuery(sql string) error {
	body := make([]byte, 0, len(sql)+1)
	body = append(body, sql...)
	body = append(body, 0)
	return fw.WriteFrame(MsgQuery, body)
}

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
	if err := fw.WriteFrame(MsgReadyForQuery, []byte{byte(status)}); err != nil {
		return err
	}
	return fw.Flush()
}

// ReadyForQuery emits 'Z' carrying the CONNECTION's current transaction
// status, as reported by TxStatusFn (see the field comment on FrameWriter).
// Every ordinary end-of-message ReadyForQuery goes through here so the byte is
// derived from live transaction state in one place instead of being hard-coded
// 'I' at ~40 call sites. With no TxStatusFn installed it degrades to 'I',
// which is what a connection with no transaction machinery (walsender-only
// paths, tests) should report anyway.
func (fw *FrameWriter) ReadyForQuery() error {
	return fw.WriteReadyForQuery(fw.txStatus(false))
}

// ReadyForQueryAfterError emits the 'Z' that terminates an ErrorResponse.
// It is a separate entry point because PostgreSQL marks the transaction
// aborted (AbortCurrentTransaction) BEFORE computing the status byte, whereas
// goopg's dispatch loop marks connTxState failed only after the error has been
// written; passing afterError=true lets the status function report 'E' for an
// explicit block that is about to be marked failed.
func (fw *FrameWriter) ReadyForQueryAfterError() error {
	return fw.WriteReadyForQuery(fw.txStatus(true))
}

func (fw *FrameWriter) txStatus(afterError bool) TransactionStatus {
	if fw.TxStatusFn == nil {
		return TxStatusIdle
	}
	return fw.TxStatusFn(afterError)
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

// WriteCopyInResponse emits 'G' / int8 overall-format / int16 ncolumns /
// int16 format[*]. overall-format is 0=text or 1=binary.
func (fw *FrameWriter) WriteCopyInResponse(overallFormat byte, columnFormats []uint16) error {
	payload := make([]byte, 0, 1+2+2*len(columnFormats))
	payload = append(payload, overallFormat)
	payload = appendUint16(payload, uint16(len(columnFormats)))
	for _, f := range columnFormats {
		payload = appendUint16(payload, f)
	}
	return fw.WriteFrame(MsgCopyInResponse, payload)
}

// WriteCopyOutResponse emits 'H' with the same payload layout as
// CopyInResponse.
func (fw *FrameWriter) WriteCopyOutResponse(overallFormat byte, columnFormats []uint16) error {
	payload := make([]byte, 0, 1+2+2*len(columnFormats))
	payload = append(payload, overallFormat)
	payload = appendUint16(payload, uint16(len(columnFormats)))
	for _, f := range columnFormats {
		payload = appendUint16(payload, f)
	}
	return fw.WriteFrame(MsgCopyOutResponse, payload)
}

// WriteCopyData emits one COPY data chunk ('d').
func (fw *FrameWriter) WriteCopyData(data []byte) error {
	return fw.WriteFrame(MsgCopyData, data)
}

// WriteCopyDone emits backend CopyDone ('c').
func (fw *FrameWriter) WriteCopyDone() error {
	return fw.WriteFrame(MsgCopyDone, nil)
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
	// FieldPosition is a decimal 1-based byte offset into the original query
	// string. psql uses it to render the caret-pointer line under the error.
	FieldPosition byte = 'P'
	FieldWhere    byte = 'W'
	FieldSchema   byte = 's'
	FieldTable    byte = 't'
	FieldColumn   byte = 'c'
	FieldFile     byte = 'F'
	FieldLine     byte = 'L'
	FieldRoutine  byte = 'R'
)

// WriteErrorResponse emits 'E' / [code,value\0]+ / \0.
func (fw *FrameWriter) WriteErrorResponse(fields []ErrorField) error {
	return fw.writeFieldedMessage(MsgErrorResponse, fields)
}

// Message elevels, mirroring postgres/src/include/utils/elog.h. The numeric
// values are upstream's so the `>=` comparison in ShouldOutputToClient is
// literally the one PG makes; only the levels goopg can either put on the wire
// or accept as a client_min_messages value are enumerated.
const (
	elevelDebug5  = 10
	elevelDebug4  = 11
	elevelDebug3  = 12
	elevelDebug2  = 13
	elevelDebug1  = 14
	elevelLog     = 15
	elevelInfo    = 17
	elevelNotice  = 18
	elevelWarning = 19
	elevelError   = 21
	elevelFatal   = 22
	elevelPanic   = 23
)

// severityElevel maps the non-localized severity string goopg puts in the 'V'
// field of a NoticeResponse/ErrorResponse back to its elog.h elevel. The
// second result is false for a severity this table does not know, in which
// case callers must not suppress the message.
func severityElevel(sev string) (int, bool) {
	switch sev {
	case "DEBUG", "DEBUG1":
		return elevelDebug1, true
	case "DEBUG2":
		return elevelDebug2, true
	case "DEBUG3":
		return elevelDebug3, true
	case "DEBUG4":
		return elevelDebug4, true
	case "DEBUG5":
		return elevelDebug5, true
	case "LOG":
		return elevelLog, true
	case "INFO":
		return elevelInfo, true
	case "NOTICE":
		return elevelNotice, true
	case "WARNING":
		return elevelWarning, true
	case "ERROR":
		return elevelError, true
	case "FATAL":
		return elevelFatal, true
	case "PANIC":
		return elevelPanic, true
	}
	return 0, false
}

// clientMinMessagesElevel maps a client_min_messages GUC value to its elevel,
// mirroring client_message_level_options in
// postgres/src/backend/utils/misc/guc_tables.c. "debug" and "info" are
// upstream's hidden aliases: accepted as input, not advertised in
// pg_settings.enumvals.
func clientMinMessagesElevel(val string) (int, bool) {
	switch strings.ToLower(val) {
	case "debug5":
		return elevelDebug5, true
	case "debug4":
		return elevelDebug4, true
	case "debug3":
		return elevelDebug3, true
	case "debug2", "debug":
		return elevelDebug2, true
	case "debug1":
		return elevelDebug1, true
	case "log":
		return elevelLog, true
	case "info":
		return elevelInfo, true
	case "notice":
		return elevelNotice, true
	case "warning":
		return elevelWarning, true
	case "error":
		return elevelError, true
	}
	return 0, false
}

// ShouldOutputToClient reports whether a message of the given non-localized
// severity is visible to a client whose client_min_messages GUC holds
// clientMin, mirroring should_output_to_client in
// postgres/src/backend/utils/error/elog.c:
//
//	return (elevel >= client_min_messages || elevel == INFO);
//
// INFO is deliberately unconditional upstream ("always sent to client
// regardless of client_min_messages", elog.h). An unrecognized severity or
// GUC value falls open — goopg never silently drops a message it cannot
// classify.
func ShouldOutputToClient(severity, clientMin string) bool {
	elevel, ok := severityElevel(severity)
	if !ok {
		return true
	}
	if elevel == elevelInfo {
		return true
	}
	min, ok := clientMinMessagesElevel(clientMin)
	if !ok {
		return true
	}
	return elevel >= min
}

// noticeSeverity extracts the severity a NoticeResponse field set carries.
// The non-localized 'V' field is authoritative (it is the one whose spelling
// is fixed by the protocol); 'S' is the fallback for field sets that predate
// it.
func noticeSeverity(fields []ErrorField) string {
	sev := ""
	for _, f := range fields {
		if f.Code == FieldSeverityNonLocal {
			return f.Value
		}
		if f.Code == FieldSeverity && sev == "" {
			sev = f.Value
		}
	}
	return sev
}

// WriteNoticeResponse emits 'N' with the same body shape as ErrorResponse,
// unless the connection's client_min_messages GUC suppresses this severity.
//
// The filter lives here, at the single wire choke point, for the same reason
// PostgreSQL puts should_output_to_client inside elog.c rather than at each
// ereport call site: every one of goopg's NOTICE/WARNING producers reaches the
// client through this function, so no producer can forget to consult the GUC.
// WriteErrorResponse needs no such gate — ERROR/FATAL/PANIC are elevel >= 21
// and client_min_messages caps out at "error" (21), so upstream's comparison
// admits them unconditionally.
func (fw *FrameWriter) WriteNoticeResponse(fields []ErrorField) error {
	if fw.ClientMinMessagesFn != nil {
		if !ShouldOutputToClient(noticeSeverity(fields), fw.ClientMinMessagesFn()) {
			return nil
		}
	}
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

// WriteDataRowReuse writes a DataRow using the caller-provided
// scratch buffer as the payload backing. The returned slice (possibly
// grown via append) should be passed back on the next call so the
// underlying capacity amortises across rows. Saves ~33 B / row vs
// WriteDataRow's fresh make([]byte) per call (M0092-0004).
//
// scratch is reset to length 0 and reused; its previous contents
// are overwritten. After WriteFrame copies the payload to the
// underlying writer, the caller may freely truncate the returned
// slice for the next row.
func (fw *FrameWriter) WriteDataRowReuse(columns [][]byte, scratch []byte) ([]byte, error) {
	payload := scratch[:0]
	payload = appendUint16(payload, uint16(len(columns)))
	for _, c := range columns {
		if c == nil {
			payload = appendUint32(payload, 0xFFFFFFFF) // -1 as int32
			continue
		}
		payload = appendUint32(payload, uint32(len(c)))
		payload = append(payload, c...)
	}
	if err := fw.WriteFrame(MsgDataRow, payload); err != nil {
		return payload, err
	}
	return payload, nil
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

// WriteNotificationResponse emits 'A' (NotificationResponse): the int32 PID of
// the notifying backend followed by the channel name and payload as C-strings.
// Mirrors PostgreSQL's NotifyResponse wire shape (src/backend/commands/async.c
// → pq_putmessage('A', ...)). The empty payload is the common case (NOTIFY with
// no second argument) and is sent as a bare NUL terminator.
func (fw *FrameWriter) WriteNotificationResponse(pid uint32, channel, payload string) error {
	payloadBytes := make([]byte, 0, 4+len(channel)+1+len(payload)+1)
	payloadBytes = appendUint32(payloadBytes, pid)
	payloadBytes = append(payloadBytes, channel...)
	payloadBytes = append(payloadBytes, 0)
	payloadBytes = append(payloadBytes, payload...)
	payloadBytes = append(payloadBytes, 0)
	return fw.WriteFrame(MsgNotificationResponse, payloadBytes)
}

func appendUint16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}
func appendUint32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
