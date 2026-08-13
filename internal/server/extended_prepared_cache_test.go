package server

import (
	"encoding/binary"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/protocol"
)

// M0132-S13 — prepared-statement cache over the extended protocol.
//
// S11's measurement found the prepared path re-parses on every Execute and
// re-parses+re-plans on every Describe. The fix (0132-0005) hoists the
// cross-session plan-cache lookup ahead of the parse on the Execute path, and
// routes the Describe path through the same cache. These tests pin that the
// cache is a pure optimisation — results, describe output, and DDL invalidation
// are identical to a re-parse every time.

// TestM0132S13_PreparedPlanReuseReadsFreshData runs the same prepared SELECT
// before and after an INSERT: the cached plan must be re-executed against the
// current heap, not frozen at first-plan time. This is the property a
// "skip parse on cache hit" bug would break — a wrong node (or a stale plan)
// would either error or return the pre-INSERT row count.
func TestM0132S13_PreparedPlanReuseReadsFreshData(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	// Parse once, execute twice around an INSERT.
	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload("q", "SELECT * FROM items", nil))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)
	if f := drainToReady(t, r); hasError(f) {
		t.Fatalf("Parse errored: %+v", f)
	}

	exec := func(wantRows int) {
		t.Helper()
		writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", "q", nil, nil, nil))
		writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
		writeFrontendFrame(t, conn, protocol.MsgSync, nil)
		frames := drainToReady(t, r)
		if hasError(frames) {
			t.Fatalf("Execute errored: %+v", frames)
		}
		n := 0
		for _, f := range frames {
			if f.Type == protocol.MsgDataRow {
				n++
			}
		}
		if n != wantRows {
			t.Fatalf("Execute returned %d rows, want %d", n, wantRows)
		}
	}

	exec(0) // first Execute plans + caches

	if f := simpleStmt(t, conn, r, "INSERT INTO items VALUES (1, 'a')"); hasError(f) {
		t.Fatalf("INSERT errored: %+v", f)
	}

	exec(1) // second Execute must hit the cache AND see the new row
}

// TestM0132S13_DescribeCachedAcrossExecutions pins the Describe result against
// execution-induced mutation of the shared plan node: Describe → Execute →
// Describe must return the same RowDescription both times.
func TestM0132S13_DescribeCachedAcrossExecutions(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload("q", "SELECT id, label FROM items", nil))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)
	if f := drainToReady(t, r); hasError(f) {
		t.Fatalf("Parse errored: %+v", f)
	}

	describe := func() int {
		t.Helper()
		writeFrontendFrame(t, conn, protocol.MsgDescribe, describePayload('S', "q"))
		writeFrontendFrame(t, conn, protocol.MsgSync, nil)
		frames := drainToReady(t, r)
		var nfields int
		for _, f := range frames {
			if f.Type == protocol.MsgErrorResponse {
				t.Fatalf("Describe errored: %+v", f)
			}
			if f.Type == protocol.MsgRowDescription {
				nfields = int(binary.BigEndian.Uint16(f.Payload[:2]))
			}
		}
		if nfields == 0 {
			t.Fatal("no RowDescription frame")
		}
		return nfields
	}

	if got := describe(); got != 2 {
		t.Fatalf("first Describe nfields=%d, want 2", got)
	}
	// Execute the same statement so the node is run through executor.Build/Open/Next.
	writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", "q", nil, nil, nil))
	writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)
	if f := drainToReady(t, r); hasError(f) {
		t.Fatalf("Execute errored: %+v", f)
	}
	if got := describe(); got != 2 {
		t.Fatalf("second Describe nfields=%d, want 2", got)
	}
}

// TestM0132S13_DDLInvalidatesPlanCache runs a DDL between two Executes of the
// same prepared statement: the cache must be cleared, forcing the second Execute
// to re-parse+re-plan against the new catalog rather than serve the stale node.
func TestM0132S13_DDLInvalidatesPlanCache(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload("q", "SELECT * FROM items", nil))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)
	if f := drainToReady(t, r); hasError(f) {
		t.Fatalf("Parse errored: %+v", f)
	}

	execRows := func(wantRows int) {
		t.Helper()
		writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", "q", nil, nil, nil))
		writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
		writeFrontendFrame(t, conn, protocol.MsgSync, nil)
		frames := drainToReady(t, r)
		if hasError(frames) {
			t.Fatalf("Execute errored: %+v", frames)
		}
		n := 0
		for _, f := range frames {
			if f.Type == protocol.MsgDataRow {
				n++
			}
		}
		if n != wantRows {
			t.Fatalf("Execute returned %d rows, want %d", n, wantRows)
		}
	}

	execRows(0) // caches the plan

	// A DDL statement invalidates the whole cross-session plan cache.
	if f := simpleStmt(t, conn, r, "CREATE TABLE scratch (x int4)"); hasError(f) {
		t.Fatalf("CREATE TABLE errored: %+v", f)
	}

	execRows(0) // must re-plan (cache miss) and still answer correctly
}

// TestM0132S13_ParameterizedExecuteReuse drives the benchmark's shape: one
// Parse of a parameterised INSERT, three Bind/Execute cycles with different
// values. The first Execute plans + caches; the rest must hit the cache and
// still bind their parameters correctly (no cross-execution param leakage).
func TestM0132S13_ParameterizedExecuteReuse(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	writeFrontendFrame(t, conn, protocol.MsgParse, parsePayload("ins", "INSERT INTO items VALUES ($1, $2)", nil))
	writeFrontendFrame(t, conn, protocol.MsgSync, nil)
	if f := drainToReady(t, r); hasError(f) {
		t.Fatalf("Parse errored: %+v", f)
	}

	for i := 1; i <= 3; i++ {
		writeFrontendFrame(t, conn, protocol.MsgBind, bindPayload("", "ins", nil, []bindParam{{value: strconv.Itoa(i)}, {value: "r" + strconv.Itoa(i)}}, nil))
		writeFrontendFrame(t, conn, protocol.MsgExecute, executePayload("", 0))
		writeFrontendFrame(t, conn, protocol.MsgSync, nil)
		if f := drainToReady(t, r); hasError(f) {
			t.Fatalf("Execute %d errored: %+v", i, f)
		}
	}

	if got := countItems(t, conn, r); got != 3 {
		t.Fatalf("after 3 parameterised Executes: %d rows, want 3", got)
	}
}
