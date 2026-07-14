package server

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/protocol"
)

// TestExtendedZeroColumnSelectEmitsRows pins the fix for the
// dispatch_extended.go vs dispatch.go sibling-path divergence on
// zero-column result sets (`SELECT FROM t`, `SELECT;`). PostgreSQL returns
// one zero-column DataRow per source row for these, and Describe replies
// with a RowDescription carrying 0 fields (NOT NoData). The extended path
// previously gated row emission and field-list construction on
// `len(schema) > 0`, so every zero-column row was silently dropped and the
// command tag read `SELECT 0`; the simple-query path (gated on
// `schema != nil`) always emitted them. Verified against PostgreSQL 18.3:
// `SELECT FROM t` over 3 rows returns 3 rows via `\bind \g` (extended proto).
func TestExtendedZeroColumnSelectEmitsRows(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "INSERT INTO items VALUES (1,'a'),(2,'b'),(3,'c')")
	readUntilReady(t, conn)

	// Drive `SELECT FROM items` through Parse/Bind/Describe/Execute/Sync.
	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload("z", "SELECT FROM items", nil))
	writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", "z", nil, nil, nil))
	writeFrontendFrame(t, conn, protocol.MsgDescribe, describePayload('P', ""))
	writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)

	frames := readUntilReady(t, conn)

	var (
		sawRowDesc  bool
		rowDescN    uint16
		dataRows    int
		zeroColRows int
		commandTag  string
	)
	for _, f := range frames {
		switch f.Type {
		case protocol.MsgErrorResponse:
			t.Fatalf("unexpected error: %s", string(f.Payload))
		case protocol.MsgNoData:
			t.Fatalf("got NoData for a zero-column read; want RowDescription with 0 fields")
		case protocol.MsgRowDescription:
			sawRowDesc = true
			rowDescN = binary.BigEndian.Uint16(f.Payload[:2])
		case protocol.MsgDataRow:
			dataRows++
			if binary.BigEndian.Uint16(f.Payload[:2]) == 0 {
				zeroColRows++
			}
		case protocol.MsgCommandComplete:
			// Payload is a NUL-terminated command tag string.
			commandTag = string(f.Payload[:len(f.Payload)-1])
		}
	}

	if !sawRowDesc {
		t.Fatalf("no RowDescription emitted; frames=%+v", frames)
	}
	if rowDescN != 0 {
		t.Fatalf("RowDescription field count=%d, want 0", rowDescN)
	}
	if dataRows != 3 {
		t.Fatalf("DataRow count=%d, want 3 (one zero-column row per source row)", dataRows)
	}
	if zeroColRows != 3 {
		t.Fatalf("zero-column DataRow count=%d, want 3", zeroColRows)
	}
	if commandTag != "SELECT 3" {
		t.Fatalf("command tag=%q, want %q", commandTag, "SELECT 3")
	}
}

// TestExtendedZeroColumnSelectWithFilter confirms the row count tracks the
// WHERE clause (not a blanket "emit one row"): `SELECT FROM items WHERE id>=2`
// returns exactly the 2 matching zero-column rows through the extended path.
func TestExtendedZeroColumnSelectWithFilter(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "INSERT INTO items VALUES (1,'a'),(2,'b'),(3,'c')")
	readUntilReady(t, conn)

	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload("z2", "SELECT FROM items WHERE id >= 2", nil))
	writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", "z2", nil, nil, nil))
	writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)

	frames := readUntilReady(t, conn)
	dataRows := 0
	commandTag := ""
	for _, f := range frames {
		switch f.Type {
		case protocol.MsgErrorResponse:
			t.Fatalf("unexpected error: %s", string(f.Payload))
		case protocol.MsgDataRow:
			dataRows++
			if n := binary.BigEndian.Uint16(f.Payload[:2]); n != 0 {
				t.Fatalf("DataRow ncols=%d, want 0", n)
			}
		case protocol.MsgCommandComplete:
			commandTag = string(f.Payload[:len(f.Payload)-1])
		}
	}
	if dataRows != 2 {
		t.Fatalf("DataRow count=%d, want 2", dataRows)
	}
	if commandTag != "SELECT 2" {
		t.Fatalf("command tag=%q, want %q", commandTag, "SELECT 2")
	}
}
