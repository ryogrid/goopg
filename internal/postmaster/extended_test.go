package postmaster

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

type bindParam struct {
	isNull bool
	value  string
}

func writeFrontendFrame(t *testing.T, conn net.Conn, typ byte, payload []byte) {
	t.Helper()
	hdr := make([]byte, 5)
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)+4))
	msg := append(hdr, payload...)
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write frame %q: %v", typ, err)
	}
}

func cstring(s string) []byte {
	out := make([]byte, 0, len(s)+1)
	out = append(out, s...)
	out = append(out, 0)
	return out
}

func appendUint16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}

func appendUint32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendInt32(b []byte, v int32) []byte {
	return appendUint32(b, uint32(v))
}

func parsePayload(name, query string, paramTypeOIDs []uint32) []byte {
	p := make([]byte, 0, len(name)+len(query)+8+4*len(paramTypeOIDs))
	p = append(p, cstring(name)...)
	p = append(p, cstring(query)...)
	p = appendUint16(p, uint16(len(paramTypeOIDs)))
	for _, oid := range paramTypeOIDs {
		p = appendUint32(p, oid)
	}
	return p
}

func bindPayload(portal, statement string, paramFormats []uint16, params []bindParam, resultFormats []uint16) []byte {
	p := make([]byte, 0, 64)
	p = append(p, cstring(portal)...)
	p = append(p, cstring(statement)...)
	p = appendUint16(p, uint16(len(paramFormats)))
	for _, f := range paramFormats {
		p = appendUint16(p, f)
	}
	p = appendUint16(p, uint16(len(params)))
	for _, bp := range params {
		if bp.isNull {
			p = appendInt32(p, -1)
			continue
		}
		p = appendInt32(p, int32(len(bp.value)))
		p = append(p, bp.value...)
	}
	p = appendUint16(p, uint16(len(resultFormats)))
	for _, f := range resultFormats {
		p = appendUint16(p, f)
	}
	return p
}

func describePayload(kind byte, name string) []byte {
	p := make([]byte, 0, len(name)+2)
	p = append(p, kind)
	p = append(p, cstring(name)...)
	return p
}

func executePayload(portal string, maxRows int32) []byte {
	p := make([]byte, 0, len(portal)+6)
	p = append(p, cstring(portal)...)
	p = appendInt32(p, maxRows)
	return p
}

func TestExtendedParseBindDescribeExecuteSyncSelectOne(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("", "SELECT 1", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "", nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgDescribe, describePayload('P', ""))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)

	frames := readUntilReady(t, conn)
	want := []byte{
		libpq.MsgParseComplete,
		libpq.MsgBindComplete,
		libpq.MsgRowDescription,
		libpq.MsgDataRow,
		libpq.MsgCommandComplete,
		libpq.MsgReadyForQuery,
	}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d", len(frames), len(want))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}
	if nfields := binary.BigEndian.Uint16(frames[2].Payload[:2]); nfields != 1 {
		t.Fatalf("row description nfields=%d want 1", nfields)
	}
	dr := frames[3].Payload
	if ncols := binary.BigEndian.Uint16(dr[:2]); ncols != 1 {
		t.Fatalf("datarow ncols=%d want 1", ncols)
	}
	if got := string(dr[6:7]); got != "1" {
		t.Fatalf("datarow value=%q want 1", got)
	}
}

func TestExtendedBindUnknownStatementRequiresSync(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "missing", nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("", "SELECT 1", nil)) // ignored until Sync
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)

	frames := readUntilReady(t, conn)
	if len(frames) != 2 {
		t.Fatalf("frames=%d want 2", len(frames))
	}
	if frames[0].Type != libpq.MsgErrorResponse || frames[1].Type != libpq.MsgReadyForQuery {
		t.Fatalf("types=%q,%q want E,Z", frames[0].Type, frames[1].Type)
	}
	got := parseErrorFields(t, frames[0].Payload)
	if got[libpq.FieldSQLState] != string(errcodes.InvalidSQLStatementName) {
		t.Fatalf("sqlstate=%q want %q", got[libpq.FieldSQLState], errcodes.InvalidSQLStatementName)
	}

	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("", "SELECT 1", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "", nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)

	frames = readUntilReady(t, conn)
	want := []byte{libpq.MsgParseComplete, libpq.MsgBindComplete, libpq.MsgDataRow, libpq.MsgCommandComplete, libpq.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d", len(frames), len(want))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}
}

func TestExtendedDescribeStatement(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("s1", "SELECT 1", nil))
	writeFrontendFrame(t, conn, libpq.MsgDescribe, describePayload('S', "s1"))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)

	frames := readUntilReady(t, conn)
	want := []byte{libpq.MsgParseComplete, libpq.MsgParameterDesc, libpq.MsgRowDescription, libpq.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d", len(frames), len(want))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}
	if n := binary.BigEndian.Uint16(frames[1].Payload[:2]); n != 0 {
		t.Fatalf("parameter count=%d want 0", n)
	}
}

func TestExtendedBindParameterCountMismatch(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("p1", "SELECT $1", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "p1", nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)

	frames := readUntilReady(t, conn)
	want := []byte{libpq.MsgParseComplete, libpq.MsgErrorResponse, libpq.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d", len(frames), len(want))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}
	got := parseErrorFields(t, frames[1].Payload)
	if got[libpq.FieldSQLState] != string(errcodes.ProtocolViolation) {
		t.Fatalf("sqlstate=%q want %q", got[libpq.FieldSQLState], errcodes.ProtocolViolation)
	}
}
