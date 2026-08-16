package postmaster

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

func TestCopyToStdoutSelectOne(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY (SELECT 1) TO STDOUT")
	frames := readUntilReady(t, conn)
	want := []byte{
		libpq.MsgCopyOutResponse,
		libpq.MsgCopyData,
		libpq.MsgCopyDone,
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
	if got := string(frames[1].Payload); got != "1\n" {
		t.Fatalf("CopyData payload=%q want %q", got, "1\\n")
	}
	tag := strings.TrimSuffix(string(frames[3].Payload), "\x00")
	if tag != "COPY 1" {
		t.Fatalf("command tag=%q want COPY 1", tag)
	}
}

func TestCopyFromStdinCountsRows(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY pgbench_history FROM STDIN")
	writeFrontendFrame(t, conn, libpq.MsgCopyData, []byte("1\ta\n2\tb\n"))
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

func TestCopyFromStdinFailReturnsError(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY pgbench_history FROM STDIN")
	writeFrontendFrame(t, conn, libpq.MsgCopyFail, cstring("frontend aborted"))

	frames := readUntilReady(t, conn)
	want := []byte{libpq.MsgCopyInResponse, libpq.MsgErrorResponse, libpq.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%d want=%d", len(frames), len(want))
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}
	got := parseErrorFields(t, frames[1].Payload)
	if got[libpq.FieldSQLState] != string(errcodes.QueryCanceled) {
		t.Fatalf("sqlstate=%q want %q", got[libpq.FieldSQLState], errcodes.QueryCanceled)
	}
}
