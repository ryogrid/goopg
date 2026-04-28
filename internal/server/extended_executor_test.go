package server

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/protocol"
)

// TestExtendedQueryExecutorBindParameter pins the new behaviour that
// the extended-query path routes through parser→planner→executor
// when storage is configured, including the bind-parameter datums
// that previously errored with "bind parameters in Execute are not
// supported yet".
//
// The test seeds an `items(id int4, label text)` table via the
// Catalog (the only side-channel a server-level test has), then
// drives Parse/Bind/Execute/Sync for `INSERT INTO items VALUES ($1,
// $2)` and verifies a final `SELECT * FROM items` (executed via
// the simple-query path) sees the row that came from the bind
// values.
func TestExtendedQueryExecutorBindParameter(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// Parse + Bind + Execute + Sync — INSERT with two text-format
	// parameters. The dispatch should route through the executor
	// rather than the legacy "bind parameters not supported" error.
	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload("ins", "INSERT INTO items VALUES ($1, $2)", nil))
	writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", "ins", nil, []bindParam{{value: "42"}, {value: "answer"}}, nil))
	writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)

	frames := readUntilReady(t, conn)
	var sawCC bool
	for _, f := range frames {
		if f.Type == protocol.MsgErrorResponse {
			t.Fatalf("ErrorResponse: payload=%q", f.Payload)
		}
		if f.Type == protocol.MsgCommandComplete {
			sawCC = true
			tag := strings.TrimSuffix(string(f.Payload), "\x00")
			if tag != "INSERT 0 1" {
				t.Errorf("CommandComplete=%q want %q", tag, "INSERT 0 1")
			}
		}
	}
	if !sawCC {
		t.Fatal("no CommandComplete frame in INSERT response")
	}

	// Now read the row back via the simple-query path to prove the
	// extended-path INSERT actually wrote it.
	writeQuery(t, conn, "SELECT * FROM items")
	frames = readUntilReady(t, conn)
	var sawData bool
	for _, f := range frames {
		if f.Type == protocol.MsgErrorResponse {
			t.Fatalf("SELECT ErrorResponse: payload=%q", f.Payload)
		}
		if f.Type == protocol.MsgDataRow {
			sawData = true
			// DataRow payload starts with uint16 ncols then per-col
			// (int32 len, bytes); we only check the bytes contain
			// the expected values.
			if !strings.Contains(string(f.Payload), "42") || !strings.Contains(string(f.Payload), "answer") {
				t.Errorf("DataRow payload=%q missing 42/answer", f.Payload)
			}
		}
	}
	if !sawData {
		t.Fatal("no DataRow returned from SELECT")
	}
}
