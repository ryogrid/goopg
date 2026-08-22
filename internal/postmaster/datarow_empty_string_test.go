package postmaster

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// TestSimpleQueryEmptyStringNotNull pins the state-dependent manifestation of
// M0134-datarow-empty-string: a still-nil per-connection scratch buffer
// (dispatch.go's `w.DataRowScratch`) sliced at [0:0] for the FIRST non-null
// empty-string cell on a fresh connection previously yielded a nil []byte,
// indistinguishable from the d.IsNull() sentinel — so `SELECT ''` sent a
// DataRow with column length -1 (NULL) instead of length 0 (empty value).
//
// The DataRow wire format is int32 length + bytes; NULL is length -1, an
// empty non-null value is length 0 with zero following bytes. This test
// decodes that length field directly rather than relying on a display tool
// (psql's NULL-vs-empty rendering can itself mask the bug depending on
// \pset null).
func TestSimpleQueryEmptyStringNotNull(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// Fresh connection, fresh scratch buffer: this is the FIRST query, so
	// DataRowScratch's valueBuf starts nil — the exact state the brief
	// calls out as the narrower, state-dependent bug.
	writeQuery(t, conn, "SELECT ''::text")
	frames := readUntilReady(t, conn)

	dataRowLen, ok := firstDataRowColumnLength(t, frames)
	if !ok {
		t.Fatal("no DataRow frame in response")
	}
	if dataRowLen != 0 {
		t.Errorf("SELECT ''::text DataRow column length = %d, want 0 (empty, not NULL/-1)", dataRowLen)
	}
}

// TestSimpleQueryEmptyStringNotNull_SecondQuery is the companion
// non-regression check called out in acceptance criterion 3: a
// previously-populated scratch buffer (from a prior non-empty SELECT on the
// same connection) must not mask a bug in the reverse direction, and the
// already-working "second query empty string" case must keep working.
func TestSimpleQueryEmptyStringNotNull_SecondQuery(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SELECT 'x'")
	_ = readUntilReady(t, conn)

	writeQuery(t, conn, "SELECT ''::text")
	frames := readUntilReady(t, conn)

	dataRowLen, ok := firstDataRowColumnLength(t, frames)
	if !ok {
		t.Fatal("no DataRow frame in response")
	}
	if dataRowLen != 0 {
		t.Errorf("SELECT ''::text (2nd query) DataRow column length = %d, want 0", dataRowLen)
	}
}

// TestExtendedProtocolEmptyStringNotNull pins the unconditional
// manifestation of M0134-datarow-empty-string in the Bind/Execute row
// builder (dispatch_extended.go), which passes literal `nil` as the dest
// buffer to appendTypedCellText/AppendValueText for every cell.
func TestExtendedProtocolEmptyStringNotNull(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("", "SELECT ''::text", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "", nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)

	frames := readUntilReady(t, conn)
	dataRowLen, ok := firstDataRowColumnLength(t, frames)
	if !ok {
		t.Fatal("no DataRow frame in response")
	}
	if dataRowLen != 0 {
		t.Errorf("extended-protocol SELECT ''::text DataRow column length = %d, want 0 (empty, not NULL/-1)", dataRowLen)
	}
}

// TestExtendedProtocolRepeatEmptyNotNull covers the AppendValueText
// (non-schema-formatted) call site at dispatch_extended.go, exercised by an
// argument-index path beyond the known schema slice (the brief's line 525).
// repeat('Pg', 0) is non-null (repeat() IS NULL evaluates false) but
// renders to zero bytes.
func TestExtendedProtocolRepeatEmptyNotNull(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("", "SELECT repeat('Pg', 0)", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "", nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)

	frames := readUntilReady(t, conn)
	dataRowLen, ok := firstDataRowColumnLength(t, frames)
	if !ok {
		t.Fatal("no DataRow frame in response")
	}
	if dataRowLen != 0 {
		t.Errorf("extended-protocol SELECT repeat('Pg',0) DataRow column length = %d, want 0 (empty, not NULL/-1)", dataRowLen)
	}
}

// firstDataRowColumnLength decodes the first column's int32 length field
// from the first DataRow frame found in frames. DataRow payload layout is
// int16 ncols, then per-column (int32 len, bytes...); NULL is encoded as
// length -1.
func firstDataRowColumnLength(t *testing.T, frames []libpq.Frame) (int32, bool) {
	t.Helper()
	for _, f := range frames {
		if f.Type != libpq.MsgErrorResponse && f.Type != libpq.MsgDataRow {
			continue
		}
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("ErrorResponse: payload=%q", f.Payload)
		}
		if len(f.Payload) < 6 {
			t.Fatalf("DataRow payload too short: %v", f.Payload)
		}
		return int32(binary.BigEndian.Uint32(f.Payload[2:6])), true
	}
	return 0, false
}
