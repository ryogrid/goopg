package server

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// dialAndComplete handshakes a fresh connection up to the first
// ReadyForQuery so the test can issue Query messages directly.
func dialAndComplete(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	// Connect as the always-seeded "postgres" role: real-catalog test servers
	// now reject connections from a role that does not exist (AC-002 gap #7b,
	// PG's InitializeSessionUserId 28000). The username value is not asserted by
	// any caller of this helper.
	writeStartupPacket(t, conn, map[string]string{"user": "postgres"})
	r := protocol.NewFrameReader(conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("drain startup: %v", err)
		}
		if f.Type == protocol.MsgReadyForQuery {
			return conn
		}
	}
}

func writeQuery(t *testing.T, conn net.Conn, sql string) {
	t.Helper()
	body := append([]byte(sql), 0)
	hdr := make([]byte, 5)
	hdr[0] = protocol.MsgQuery
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(body)+4))
	if _, err := conn.Write(append(hdr, body...)); err != nil {
		t.Fatalf("write query: %v", err)
	}
}

// readUntilReady drains frames until ReadyForQuery, returning the frames
// in order. It copies payloads so callers can hold onto them past the
// next read.
func readUntilReady(t *testing.T, conn net.Conn) []protocol.Frame {
	t.Helper()
	r := protocol.NewFrameReader(conn)
	var frames []protocol.Frame
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		cp := make([]byte, len(f.Payload))
		copy(cp, f.Payload)
		frames = append(frames, protocol.Frame{Type: f.Type, Payload: cp})
		if f.Type == protocol.MsgReadyForQuery {
			return frames
		}
	}
}

// TestSelectOne is the v0 contract from
// docs/design/0015-simple-query-and-sqlstate.md: a `SELECT 1` Query produces
// RowDescription / DataRow / CommandComplete / ReadyForQuery with the right
// column metadata, value, and command tag.
func TestSelectOne(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SELECT 1")
	frames := readUntilReady(t, conn)

	if len(frames) != 4 {
		t.Fatalf("got %d frames, want 4 (T,D,C,Z): %+v", len(frames), frames)
	}
	if frames[0].Type != protocol.MsgRowDescription {
		t.Fatalf("frame[0] = %c, want T", frames[0].Type)
	}
	// RowDescription: int16 nfields=1 + name "?column?\0" (9) + 18 trailing fixed bytes.
	rd := frames[0].Payload
	if len(rd) < 2 {
		t.Fatalf("RowDescription too short: %v", rd)
	}
	if nfields := binary.BigEndian.Uint16(rd[:2]); nfields != 1 {
		t.Fatalf("RowDescription nfields = %d, want 1", nfields)
	}
	rest := rd[2:]
	nul := -1
	for i, b := range rest {
		if b == 0 {
			nul = i
			break
		}
	}
	if nul < 0 || string(rest[:nul]) != "?column?" {
		t.Fatalf("RowDescription column name = %q, want ?column?", rest[:nul])
	}
	tail := rest[nul+1:]
	if len(tail) != 18 {
		t.Fatalf("RowDescription tail = %d bytes, want 18", len(tail))
	}
	tableOID := binary.BigEndian.Uint32(tail[0:4])
	colAttNum := binary.BigEndian.Uint16(tail[4:6])
	typeOID := binary.BigEndian.Uint32(tail[6:10])
	typeSize := int16(binary.BigEndian.Uint16(tail[10:12]))
	typeMod := int32(binary.BigEndian.Uint32(tail[12:16]))
	format := int16(binary.BigEndian.Uint16(tail[16:18]))
	if tableOID != 0 || colAttNum != 0 {
		t.Errorf("table-origin = (%d,%d), want (0,0)", tableOID, colAttNum)
	}
	if typeOID != 23 || typeSize != 4 || typeMod != -1 || format != 0 {
		t.Errorf("type metadata = (oid=%d, size=%d, mod=%d, fmt=%d); want (23, 4, -1, 0)",
			typeOID, typeSize, typeMod, format)
	}

	if frames[1].Type != protocol.MsgDataRow {
		t.Fatalf("frame[1] = %c, want D", frames[1].Type)
	}
	dr := frames[1].Payload
	if ncols := binary.BigEndian.Uint16(dr[:2]); ncols != 1 {
		t.Fatalf("DataRow ncols = %d, want 1", ncols)
	}
	colLen := int32(binary.BigEndian.Uint32(dr[2:6]))
	if colLen != 1 {
		t.Fatalf("DataRow col0 len = %d, want 1", colLen)
	}
	if string(dr[6:7]) != "1" {
		t.Fatalf("DataRow col0 value = %q, want %q", dr[6:7], "1")
	}

	if frames[2].Type != protocol.MsgCommandComplete {
		t.Fatalf("frame[2] = %c, want C", frames[2].Type)
	}
	tag := strings.TrimSuffix(string(frames[2].Payload), "\x00")
	if tag != "SELECT 1" {
		t.Fatalf("command tag = %q, want %q", tag, "SELECT 1")
	}

	if frames[3].Type != protocol.MsgReadyForQuery || frames[3].Payload[0] != 'I' {
		t.Fatalf("frame[3] = (%c, %v), want (Z, [I])", frames[3].Type, frames[3].Payload)
	}
}

// TestSelectOneCaseAndSemicolon checks the v0 matcher tolerates the same
// minor variations a libpq client commonly sends.
func TestSelectOneCaseAndSemicolon(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	// Per docs/design/0015-simple-query-and-sqlstate.md the matcher is
	// case-insensitive and tolerates surrounding whitespace and a trailing
	// semicolon. It does not normalise internal whitespace; the parser will
	// handle that in milestone 6.
	for _, q := range []string{"select 1", "SELECT 1;", "  select 1 ;  "} {
		conn := dialAndComplete(t, addr)
		writeQuery(t, conn, q)
		frames := readUntilReady(t, conn)
		conn.Close()
		if len(frames) != 4 || frames[0].Type != protocol.MsgRowDescription {
			t.Errorf("%q: got %d frames, first=%c", q, len(frames), frames[0].Type)
		}
	}
}

// TestUnsupportedQueryProducesErrorResponse verifies any non-SELECT-1
// statement yields ErrorResponse with a real upstream SQLSTATE plus a
// trailing ReadyForQuery, so the connection stays usable.
func TestUnsupportedQueryProducesErrorResponse(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SELECT now()")
	frames := readUntilReady(t, conn)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (E,Z): %+v", len(frames), frames)
	}
	if frames[0].Type != protocol.MsgErrorResponse {
		t.Fatalf("frame[0] = %c, want E", frames[0].Type)
	}
	got := parseErrorFields(t, frames[0].Payload)
	if got[protocol.FieldSQLState] != string(sqlstate.FeatureNotSupported) {
		t.Errorf("SQLSTATE = %q, want %q", got[protocol.FieldSQLState], sqlstate.FeatureNotSupported)
	}
	if got[protocol.FieldSeverity] != "ERROR" {
		t.Errorf("severity = %q, want ERROR", got[protocol.FieldSeverity])
	}
}

// TestEmptyQueryReturnsEmptyQueryResponse confirms the protocol-conformant
// handling of whitespace-only queries.
func TestEmptyQueryReturnsEmptyQueryResponse(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "   ")
	frames := readUntilReady(t, conn)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (I,Z)", len(frames))
	}
	if frames[0].Type != protocol.MsgEmptyQueryResponse {
		t.Fatalf("frame[0] = %c, want I", frames[0].Type)
	}
}

// TestConnectionStaysAliveAfterError is the explicit milestone-2 contract:
// "any command can return an error message cleanly without crashing the
// server" (.ralph/fix_plan.md). After a failing query, a follow-up
// SELECT 1 must still work on the same connection.
func TestConnectionStaysAliveAfterError(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "frobnicate the gizmo")
	first := readUntilReady(t, conn)
	if first[0].Type != protocol.MsgErrorResponse {
		t.Fatalf("first frame = %c, want E", first[0].Type)
	}
	writeQuery(t, conn, "SELECT 1")
	second := readUntilReady(t, conn)
	if second[0].Type != protocol.MsgRowDescription {
		t.Fatalf("after error, SELECT 1 first frame = %c, want T", second[0].Type)
	}
}

func parseErrorFields(t *testing.T, payload []byte) map[byte]string {
	t.Helper()
	out := map[byte]string{}
	for len(payload) > 0 {
		if payload[0] == 0 {
			return out
		}
		code := payload[0]
		payload = payload[1:]
		end := -1
		for i, b := range payload {
			if b == 0 {
				end = i
				break
			}
		}
		if end < 0 {
			t.Fatalf("ErrorResponse field %c missing terminator", code)
		}
		out[code] = string(payload[:end])
		payload = payload[end+1:]
	}
	return out
}

// Compile-time assurance that the test file actually exercises the slog
// and io packages used by other test files via the shared helpers.
var _ = io.Discard
var _ = slog.LevelInfo
var _ = context.Background
