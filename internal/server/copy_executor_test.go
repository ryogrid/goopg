package server

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/protocol"
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
	mvccMgr := mvcc.NewManager()

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
	writeFrontendFrame(t, conn, protocol.MsgCopyData, []byte("1\talpha\n2\tbeta\n"))
	writeFrontendFrame(t, conn, protocol.MsgCopyDone, nil)

	frames := readUntilReady(t, conn)
	want := []byte{protocol.MsgCopyInResponse, protocol.MsgCommandComplete, protocol.MsgReadyForQuery}
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
	writeFrontendFrame(t, conn, protocol.MsgCopyData, []byte("7\thello\n8\tworld\n"))
	writeFrontendFrame(t, conn, protocol.MsgCopyDone, nil)
	_ = readUntilReady(t, conn)

	// Read back via COPY TO.
	writeQuery(t, conn, "COPY items TO STDOUT")
	frames := readUntilReady(t, conn)
	want := []byte{
		protocol.MsgCopyOutResponse,
		protocol.MsgCopyData,
		protocol.MsgCopyData,
		protocol.MsgCopyDone,
		protocol.MsgCommandComplete,
		protocol.MsgReadyForQuery,
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
		protocol.MsgCopyOutResponse,
		protocol.MsgCopyData,
		protocol.MsgCopyDone,
		protocol.MsgCommandComplete,
		protocol.MsgReadyForQuery,
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
		if f.Type == protocol.MsgCopyData {
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
	want := []byte{protocol.MsgErrorResponse, protocol.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d (%v)", len(frames), len(want), frameTypes(frames))
	}
	got := parseErrorFields(t, frames[0].Payload)
	if !strings.Contains(got[protocol.FieldMessage], "RETURNING clause") {
		t.Errorf("message=%q want RETURNING-clause error", got[protocol.FieldMessage])
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
	want := []byte{protocol.MsgErrorResponse, protocol.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d (%v)", len(frames), len(want), frameTypes(frames))
	}
	got := parseErrorFields(t, frames[0].Payload)
	if got[protocol.FieldSQLState] != "42P01" {
		t.Errorf("sqlstate=%q want 42P01", got[protocol.FieldSQLState])
	}
}

func frameTypes(fs []protocol.Frame) []byte {
	out := make([]byte, len(fs))
	for i, f := range fs {
		out[i] = f.Type
	}
	return out
}
