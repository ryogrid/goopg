package postmaster

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/storage"
)

// startCopyExecServer launches a Server whose Config is wired with a
// real catalog/pool/mvcc, plus a 2-column `items(id int4, label text)`
// test table — enough to exercise the parser→planner→executor path
// for both COPY FROM and COPY TO.
func startCopyExecServer(t *testing.T) (addr string, cat catalog.Catalog, stop func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat = catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	mvccMgr := transam.NewManager()

	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Catalog:          cat,
		Pool:             pool,
		TxnMgr:           mvccMgr,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()

	stop = func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Server.Run returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Server.Run did not return within 2s of cancel")
		}
		_ = pool.Close()
		_ = mgr.Close()
	}
	return srv.Addr().String(), cat, stop
}

// TestCopyFromExecutorEndToEnd: COPY items FROM STDIN with two rows
// reaches the heap via the executor path; the resulting CommandComplete
// reports the inserted-row count.
func TestCopyFromExecutorEndToEnd(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY items FROM STDIN")
	writeFrontendFrame(t, conn, libpq.MsgCopyData, []byte("1\talpha\n2\tbeta\n"))
	writeFrontendFrame(t, conn, libpq.MsgCopyDone, nil)

	frames := readUntilReady(t, conn)
	want := []byte{libpq.MsgCopyInResponse, libpq.MsgCommandComplete, libpq.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d", len(frames), len(want))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}
	tag := strings.TrimSuffix(string(frames[1].Payload), "\x00")
	if tag != "COPY 2" {
		t.Fatalf("command tag=%q want COPY 2", tag)
	}
}

// TestCopyStandaloneResolvesNonDefaultDatabase pins M0122-0015a: the
// standalone COPY path (dispatchCopyViaExecutor) built its executor.Context
// by hand and planned the statement against the raw catalog, so relation
// lookup keyed off DefaultDBOid's namespace. Inside a CREATE DATABASE–created
// database, `COPY t1 TO STDOUT` raised 42P01 `relation "t1" does not exist`
// while `SELECT count(*) FROM t1` in the SAME session returned rows — so
// pg_dump of every user database died at its first `COPY public.<t> FROM
// stdin` and no user database was dumpable. FAIL-pre: ErrorResponse on the
// COPY TO (both unqualified and public.-qualified). The fix plans through
// sessionPlanCatalog + resolveConnDBOid (the dispatch PlanCatalog site,
// M0122-0007 slice 4c) and stamps the context through wireExtensionRows,
// the single per-database site dispatch uses for every statement.
func TestCopyStandaloneResolvesNonDefaultDatabase(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	setup := dialAndComplete(t, addr)
	defer setup.Close()
	writeQuery(t, setup, "CREATE DATABASE userdb")
	readUntilReady(t, setup)

	conn := dialAndCompleteDB(t, addr, "userdb")
	defer conn.Close()
	for _, q := range []string{
		"CREATE TABLE t1(a int, b text)",
		"INSERT INTO t1 VALUES (1, 'x'), (2, 'y')",
	} {
		writeQuery(t, conn, q)
		for _, f := range readUntilReady(t, conn) {
			if f.Type == libpq.MsgErrorResponse {
				t.Fatalf("%s: ErrorResponse %s", q, f.Payload)
			}
		}
	}

	readCopy := func(q string) (tag, payload string) {
		t.Helper()
		writeQuery(t, conn, q)
		for _, f := range readUntilReady(t, conn) {
			switch f.Type {
			case libpq.MsgErrorResponse:
				t.Fatalf("%s: ErrorResponse %s — 42P01 regression: standalone COPY must resolve relations in the connecting database (M0122-0015a)", q, f.Payload)
			case libpq.MsgCopyData:
				payload += string(f.Payload)
			case libpq.MsgCommandComplete:
				tag = strings.TrimSuffix(string(f.Payload), "\x00")
			}
		}
		return tag, payload
	}

	// COPY TO, unqualified and qualified: both failed pre-fix.
	if tag, payload := readCopy("COPY t1 TO STDOUT"); tag != "COPY 2" || payload != "1\tx\n2\ty\n" {
		t.Fatalf("COPY t1 TO STDOUT: tag=%q payload=%q, want COPY 2 / \"1\\tx\\n2\\ty\\n\"", tag, payload)
	}
	if tag, payload := readCopy("COPY public.t1 TO STDOUT"); tag != "COPY 2" || payload != "1\tx\n2\ty\n" {
		t.Fatalf("COPY public.t1 TO STDOUT: tag=%q payload=%q, want COPY 2 / \"1\\tx\\n2\\ty\\n\"", tag, payload)
	}

	// COPY FROM, the twin direction (Rule #2 sibling): the rows must land
	// in userdb — proven by re-reading them through COPY TO.
	writeQuery(t, conn, "COPY t1 FROM STDIN")
	writeFrontendFrame(t, conn, libpq.MsgCopyData, []byte("3\tz\n"))
	writeFrontendFrame(t, conn, libpq.MsgCopyDone, nil)
	for _, f := range readUntilReady(t, conn) {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("COPY t1 FROM STDIN: ErrorResponse %s", f.Payload)
		}
	}
	if _, payload := readCopy("COPY t1 TO STDOUT"); payload != "1\tx\n2\ty\n3\tz\n" {
		t.Fatalf("after COPY FROM: payload=%q, want \"1\\tx\\n2\\ty\\n3\\tz\\n\" (COPY FROM must write the connecting database's table)", payload)
	}

	// The Rule-#2 twin: the same COPY inside a multi-statement simple-query
	// batch (runInlineCopy, borrowing the batch context). It planned against
	// the raw catalog too and raised 42P01 in the user database while the
	// standalone path of this very test passed — fixed by planning through
	// ctxPlanCatalog/ctxPlannerSettings, the executeOneSimpleStmt shape.
	writeQuery(t, conn, "BEGIN; COPY t1 TO STDOUT; COMMIT")
	{
		var tag, payload string
		for _, f := range readUntilReady(t, conn) {
			switch f.Type {
			case libpq.MsgErrorResponse:
				t.Fatalf("inline batch COPY t1 TO STDOUT: ErrorResponse %s — runInlineCopy must scope to the connecting database (M0122-0015a twin)", f.Payload)
			case libpq.MsgCopyData:
				payload += string(f.Payload)
			case libpq.MsgCommandComplete:
				tag = strings.TrimSuffix(string(f.Payload), "\x00")
			}
		}
		if payload != "1\tx\n2\ty\n3\tz\n" || tag == "" {
			t.Fatalf("inline batch COPY: tag=%q payload=%q, want rows 1..3", tag, payload)
		}
	}

	// Control sibling: the DEFAULT database keeps resolving its own
	// relations through the DefaultDBOid fallback (the harness's `items`).
	defConn := dialAndComplete(t, addr)
	defer defConn.Close()
	writeQuery(t, defConn, "COPY items TO STDOUT")
	for _, f := range readUntilReady(t, defConn) {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("default-database COPY items TO STDOUT: ErrorResponse %s", f.Payload)
		}
	}
}

// TestCopyToExecutorEndToEnd: after a COPY FROM populates `items`,
// a follow-up COPY items TO STDOUT emits the rows back as CopyData
// frames in declared order.
func TestCopyToExecutorEndToEnd(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// Load via COPY FROM.
	writeQuery(t, conn, "COPY items FROM STDIN")
	writeFrontendFrame(t, conn, libpq.MsgCopyData, []byte("7\thello\n8\tworld\n"))
	writeFrontendFrame(t, conn, libpq.MsgCopyDone, nil)
	_ = readUntilReady(t, conn)

	// Read back via COPY TO.
	writeQuery(t, conn, "COPY items TO STDOUT")
	frames := readUntilReady(t, conn)
	want := []byte{
		libpq.MsgCopyOutResponse,
		libpq.MsgCopyData,
		libpq.MsgCopyData,
		libpq.MsgCopyDone,
		libpq.MsgCommandComplete,
		libpq.MsgReadyForQuery,
	}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d (%v)", len(frames), len(want), frameTypes(frames))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}
	if got := string(frames[1].Payload); got != "7\thello\n" {
		t.Errorf("data[0]=%q want %q", got, "7\thello\n")
	}
	if got := string(frames[2].Payload); got != "8\tworld\n" {
		t.Errorf("data[1]=%q want %q", got, "8\tworld\n")
	}
	tag := strings.TrimSuffix(string(frames[4].Payload), "\x00")
	if tag != "COPY 2" {
		t.Fatalf("command tag=%q want COPY 2", tag)
	}
}

// TestCopyDMLReturningExecutorEndToEnd: COPY (INSERT … RETURNING) TO
// STDOUT runs the INSERT, streams the RETURNING row as CopyData, and —
// critically — commits BEFORE CommandComplete so the row is visible to
// the very next command. A follow-up COPY items TO STDOUT must see it.
// Regression for the commit-ordering bug where the client raced ahead
// of the COPY transaction's commit. M0097-0009.
func TestCopyDMLReturningExecutorEndToEnd(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY (INSERT INTO items (id, label) VALUES (1, 'x') RETURNING id) TO STDOUT")
	frames := readUntilReady(t, conn)
	want := []byte{
		libpq.MsgCopyOutResponse,
		libpq.MsgCopyData,
		libpq.MsgCopyDone,
		libpq.MsgCommandComplete,
		libpq.MsgReadyForQuery,
	}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d (%v)", len(frames), len(want), frameTypes(frames))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}
	if got := string(frames[1].Payload); got != "1\n" {
		t.Errorf("RETURNING data=%q want %q", got, "1\n")
	}
	if tag := strings.TrimSuffix(string(frames[3].Payload), "\x00"); tag != "COPY 1" {
		t.Errorf("command tag=%q want COPY 1", tag)
	}

	// The committed row must be visible to the next command.
	writeQuery(t, conn, "COPY items TO STDOUT")
	frames = readUntilReady(t, conn)
	var dataRows []string
	for _, f := range frames {
		if f.Type == libpq.MsgCopyData {
			dataRows = append(dataRows, string(f.Payload))
		}
	}
	if len(dataRows) != 1 || dataRows[0] != "1\tx\n" {
		t.Fatalf("after COPY(INSERT), COPY TO sees %v want [\"1\\tx\\n\"] (commit-ordering regression)", dataRows)
	}
}

// TestCopyDMLNoReturningRejected: COPY (DML) without RETURNING has no
// rows to copy; the planner rejects it before any CopyOutResponse.
func TestCopyDMLNoReturningRejected(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY (INSERT INTO items (id, label) VALUES (1, 'x')) TO STDOUT")
	frames := readUntilReady(t, conn)
	want := []byte{libpq.MsgErrorResponse, libpq.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d (%v)", len(frames), len(want), frameTypes(frames))
	}
	got := parseErrorFields(t, frames[0].Payload)
	if !strings.Contains(got[libpq.FieldMessage], "RETURNING clause") {
		t.Errorf("message=%q want RETURNING-clause error", got[libpq.FieldMessage])
	}
}

// TestCopyExecutorUnknownTable: planner-stage 42P01 propagates as an
// ErrorResponse without arming a CopyIn loop.
func TestCopyExecutorUnknownTable(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY no_such_table FROM STDIN")
	frames := readUntilReady(t, conn)
	want := []byte{libpq.MsgErrorResponse, libpq.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d (%v)", len(frames), len(want), frameTypes(frames))
	}
	got := parseErrorFields(t, frames[0].Payload)
	if got[libpq.FieldSQLState] != "42P01" {
		t.Errorf("sqlstate=%q want 42P01", got[libpq.FieldSQLState])
	}
}

func frameTypes(fs []libpq.Frame) []byte {
	out := make([]byte, len(fs))
	for i, f := range fs {
		out[i] = f.Type
	}
	return out
}

// TestCopyToInMultiStatementBatch: a psql `\;`-joined batch that mixes
// COPY (query) TO STDOUT statements with a regular SELECT executes each
// statement in order, streaming the COPY rows inline and emitting one
// CommandComplete per statement plus a single trailing ReadyForQuery for
// the whole Query message. Before M0097-0024 this hit the single-COPY
// guard "expected exactly one COPY statement". This is the copyselect
// `copy (select 1) to stdout\; copy (select 2) to stdout\; select 3` shape.
func TestCopyToInMultiStatementBatch(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "copy (select 1) to stdout; copy (select 2) to stdout; select 3;")
	frames := readUntilReady(t, conn)
	want := []byte{
		libpq.MsgCopyOutResponse, libpq.MsgCopyData, libpq.MsgCopyDone, libpq.MsgCommandComplete,
		libpq.MsgCopyOutResponse, libpq.MsgCopyData, libpq.MsgCopyDone, libpq.MsgCommandComplete,
		libpq.MsgRowDescription, libpq.MsgDataRow, libpq.MsgCommandComplete,
		libpq.MsgReadyForQuery,
	}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d (%v)", len(frames), len(want), frameTypes(frames))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q (%v)", i, frames[i].Type, w, frameTypes(frames))
		}
	}
	if got := string(frames[1].Payload); got != "1\n" {
		t.Errorf("first COPY data=%q want %q", got, "1\n")
	}
	if got := string(frames[5].Payload); got != "2\n" {
		t.Errorf("second COPY data=%q want %q", got, "2\n")
	}
	if tag := strings.TrimSuffix(string(frames[3].Payload), "\x00"); tag != "COPY 1" {
		t.Errorf("first tag=%q want COPY 1", tag)
	}
}

// TestCopyToBatchStopsOnError: when a statement after an inline COPY TO
// fails (`copy (select 1) to stdout\; select 1/0`), the COPY rows stream
// out first, then the error aborts the rest of the batch with a single
// ReadyForQuery — the copyselect "row, then error" case.
func TestCopyToBatchStopsOnError(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "copy (select 1) to stdout; select 1/0;")
	frames := readUntilReady(t, conn)
	// select 1/0 is folded at plan time (PostgreSQL's eval_const_expressions
	// evaluates constant-arithmetic errors at plan time), so no RowDescription
	// is emitted — the error fires before the query begins executing. M0097-0047.
	want := []byte{
		libpq.MsgCopyOutResponse, libpq.MsgCopyData, libpq.MsgCopyDone, libpq.MsgCommandComplete,
		libpq.MsgErrorResponse, libpq.MsgReadyForQuery,
	}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d (%v)", len(frames), len(want), frameTypes(frames))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q (%v)", i, frames[i].Type, w, frameTypes(frames))
		}
	}
	got := parseErrorFields(t, frames[4].Payload) // frame 4 = ErrorResponse (no RowDescription)
	if !strings.Contains(got[libpq.FieldMessage], "division by zero") {
		t.Errorf("message=%q want division-by-zero error", got[libpq.FieldMessage])
	}
}

// TestCopyFromStdinInBatchDeferred guards the documented deferral: COPY
// FROM STDIN inside a multi-statement batch is not yet supported, but it
// must surface a clean FeatureNotSupported ERROR — never the internal
// "planner.Copy has no executor path yet" leak that the multi-statement
// path produced before M0097-0024. Statements before the COPY still run.
// TestCopyFromStdinInMultiStatementBatch: COPY FROM STDIN inside a psql
// `\;`-joined batch now streams its CopyData/CopyDone frames synchronously
// mid-batch. This is the copyselect
// `select 0\; copy test3 from stdin\; copy test3 from stdin\; select 1`
// shape (two STDIN data blocks, each `\.`-terminated). The server emits one
// CopyInResponse per COPY, consumes the client's data, writes one
// CommandComplete per statement, and a single trailing ReadyForQuery for the
// whole Query message. M0097-0024.
func TestCopyFromStdinInMultiStatementBatch(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	r := libpq.NewFrameReader(conn)
	next := func() libpq.Frame {
		t.Helper()
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		return f
	}
	expect := func(want byte) libpq.Frame {
		t.Helper()
		f := next()
		if f.Type != want {
			t.Fatalf("frame=%q want %q", f.Type, want)
		}
		return f
	}

	writeQuery(t, conn, "select 0; copy items from stdin; copy items from stdin; select 1;")

	// select 0 → T, D, C.
	expect(libpq.MsgRowDescription)
	expect(libpq.MsgDataRow)
	expect(libpq.MsgCommandComplete)

	// First COPY FROM STDIN: CopyInResponse, then we stream one row plus the
	// deprecated `\.` end-of-data marker, then CopyDone.
	expect(libpq.MsgCopyInResponse)
	writeFrontendFrame(t, conn, libpq.MsgCopyData, []byte("1\talpha\n\\.\n"))
	writeFrontendFrame(t, conn, libpq.MsgCopyDone, nil)
	if cc := expect(libpq.MsgCommandComplete); strings.TrimSuffix(string(cc.Payload), "\x00") != "COPY 1" {
		t.Errorf("first COPY tag=%q want COPY 1", strings.TrimSuffix(string(cc.Payload), "\x00"))
	}

	// Second COPY FROM STDIN.
	expect(libpq.MsgCopyInResponse)
	writeFrontendFrame(t, conn, libpq.MsgCopyData, []byte("2\tbeta\n"))
	writeFrontendFrame(t, conn, libpq.MsgCopyDone, nil)
	if cc := expect(libpq.MsgCommandComplete); strings.TrimSuffix(string(cc.Payload), "\x00") != "COPY 1" {
		t.Errorf("second COPY tag=%q want COPY 1", strings.TrimSuffix(string(cc.Payload), "\x00"))
	}

	// select 1 → T, D, C; then the single trailing RFQ.
	expect(libpq.MsgRowDescription)
	expect(libpq.MsgDataRow)
	expect(libpq.MsgCommandComplete)
	expect(libpq.MsgReadyForQuery)

	// Both rows must be committed and visible to the next command.
	writeQuery(t, conn, "COPY items TO STDOUT")
	var rows []string
	for {
		f := next()
		if f.Type == libpq.MsgCopyData {
			rows = append(rows, string(f.Payload))
		}
		if f.Type == libpq.MsgReadyForQuery {
			break
		}
	}
	if len(rows) != 2 || rows[0] != "1\talpha\n" || rows[1] != "2\tbeta\n" {
		t.Fatalf("after STDIN-in-batch, COPY TO sees %v want [\"1\\talpha\\n\" \"2\\tbeta\\n\"]", rows)
	}
}

// TestCopyFromStdinInBatchAbortsOnFail: a CopyFail mid-batch surfaces a clean
// ERROR (57014) + ReadyForQuery and aborts the rest of the batch — no second
// RFQ, no internal leak. M0097-0024.
func TestCopyFromStdinInBatchAbortsOnFail(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	r := libpq.NewFrameReader(conn)
	next := func() libpq.Frame {
		t.Helper()
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		return f
	}

	writeQuery(t, conn, "select 0; copy items from stdin; select 1;")
	// select 0 → T, D, C.
	if f := next(); f.Type != libpq.MsgRowDescription {
		t.Fatalf("frame=%q want T", f.Type)
	}
	if f := next(); f.Type != libpq.MsgDataRow {
		t.Fatalf("frame=%q want D", f.Type)
	}
	if f := next(); f.Type != libpq.MsgCommandComplete {
		t.Fatalf("frame=%q want C", f.Type)
	}
	if f := next(); f.Type != libpq.MsgCopyInResponse {
		t.Fatalf("frame=%q want G", f.Type)
	}
	writeFrontendFrame(t, conn, libpq.MsgCopyFail, append([]byte("client gave up"), 0))

	ef := next()
	if ef.Type != libpq.MsgErrorResponse {
		t.Fatalf("frame=%q want E", ef.Type)
	}
	got := parseErrorFields(t, ef.Payload)
	if !strings.Contains(got[libpq.FieldMessage], "client gave up") {
		t.Errorf("message=%q want CopyFail message", got[libpq.FieldMessage])
	}
	// The batch aborts: a single RFQ closes the message, and select 1 never runs.
	if f := next(); f.Type != libpq.MsgReadyForQuery {
		t.Fatalf("frame=%q want Z (single trailing RFQ, batch aborted)", f.Type)
	}
}
