package server

import (
	"testing"

	"github.com/goopg/goopg/internal/protocol"
)

// TestExtendedQueryTypedColumnsMatchSimpleQuery pins the M0119-0004
// dispatch_extended.go vs dispatch.go type-formatting divergence fix:
// executeExtendedQueryViaExecutor previously special-cased only
// float4/float8 and fell back to the generic Datum.AppendValueText for
// every other column type, so regclass/date/time cells rendered
// differently (a raw OID number, or the full
// "2006-01-02 15:04:05.000000" KindTime fallback) than the simple-query
// path's PG-shaped output. Both paths now share appendTypedCellText, so
// driving the SAME query through Parse/Bind/Execute/Sync (the extended
// protocol) must produce the same cell text the simple-query path does.
func TestExtendedQueryTypedColumnsMatchSimpleQuery(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "INSERT INTO items VALUES (1, 'alpha')")
	readUntilReady(t, conn)

	const query = "SELECT tableoid::regclass, '2024-03-04'::date, '13:05:09'::time FROM items WHERE id = 1"

	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload("sel", query, nil))
	writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", "sel", nil, nil, nil))
	writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)

	frames := readUntilReady(t, conn)
	var dataRows [][]byte
	for _, f := range frames {
		switch f.Type {
		case protocol.MsgErrorResponse:
			t.Fatalf("unexpected error: %s", string(f.Payload))
		case protocol.MsgDataRow:
			dataRows = append(dataRows, f.Payload)
		}
	}
	if len(dataRows) != 1 {
		t.Fatalf("DataRow count=%d, want 1; frames=%+v", len(dataRows), frames)
	}
	row := decodeDataRow(t, dataRows[0])
	if len(row) != 3 {
		t.Fatalf("cell count=%d, want 3; row=%+v", len(row), row)
	}
	if got := string(row[0]); got != "items" {
		t.Errorf("tableoid::regclass=%q, want %q (relation name, not raw OID)", got, "items")
	}
	if got := string(row[1]); got != "2024-03-04" {
		t.Errorf("date=%q, want %q", got, "2024-03-04")
	}
	if got := string(row[2]); got != "13:05:09" {
		t.Errorf("time=%q, want %q", got, "13:05:09")
	}
}
