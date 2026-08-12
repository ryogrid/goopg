package server

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/protocol"
)

// TestCopyFromStdinErrorDrainsTrailingFrames pins the upstream rule at
// postgres/src/backend/tcop/postgres.c:5004-5013: CopyData/CopyDone/CopyFail
// frames that reach the idle main loop are accepted and ignored, because "we
// probably got here because a COPY failed, and the frontend is still sending
// data".
//
// goopg reports a mid-stream COPY FROM failure with ErrorResponse + RFQ as
// soon as the offending line is pushed, but the client does not see that until
// it has finished streaming; its trailing CopyData/CopyDone frames then arrive
// after copyIn was already cleared. Before the fix those frames fell through to
// the main switch's default arm, which answered each one with
// `message type "c" not yet supported` + a second ReadyForQuery — leaving the
// session one RFQ ahead for the rest of its life (libpq then reports
// `message type 0x5a arrived from server while idle`).
//
// The assertion that matters is the one after the drain: the very next
// statement must produce exactly its own frames, with no leftovers.
func TestCopyFromStdinErrorDrainsTrailingFrames(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY items FROM STDIN")
	// A bad integer in the first column fails the decode mid-stream.
	writeFrontendFrame(t, conn, protocol.MsgCopyData, []byte("notanint\tx\n"))

	frames := readUntilReady(t, conn)
	want := []byte{protocol.MsgCopyInResponse, protocol.MsgErrorResponse, protocol.MsgReadyForQuery}
	if len(frames) != len(want) {
		t.Fatalf("frames=%q want %q", frameTypes(frames), want)
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}

	// The frontend had already pipelined the rest of the stream; these land
	// after the server has left COPY mode and must be silently dropped.
	writeFrontendFrame(t, conn, protocol.MsgCopyData, []byte("1\ty\n"))
	writeFrontendFrame(t, conn, protocol.MsgCopyDone, nil)

	// The session must still be in step: this query's frames and nothing else.
	writeQuery(t, conn, "SELECT 1")
	frames = readUntilReady(t, conn)
	want = []byte{
		protocol.MsgRowDescription,
		protocol.MsgDataRow,
		protocol.MsgCommandComplete,
		protocol.MsgReadyForQuery,
	}
	if len(frames) != len(want) {
		t.Fatalf("after drain frames=%q want %q", frameTypes(frames), want)
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Fatalf("after drain frame[%d]=%q want %q", i, frames[i].Type, w)
		}
	}
	if tag := strings.TrimSuffix(string(frames[2].Payload), "\x00"); tag != "SELECT 1" {
		t.Fatalf("command tag=%q want %q", tag, "SELECT 1")
	}
}

// TestCopyFailAfterCopyEndedIsIgnored covers the third frame type in the same
// upstream arm: a CopyFail that races the server's own error report must not
// draw an ErrorResponse of its own.
func TestCopyFailAfterCopyEndedIsIgnored(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY items FROM STDIN")
	writeFrontendFrame(t, conn, protocol.MsgCopyData, []byte("1\tok\n"))
	writeFrontendFrame(t, conn, protocol.MsgCopyDone, nil)
	frames := readUntilReady(t, conn)
	if len(frames) != 3 || frames[1].Type != protocol.MsgCommandComplete {
		t.Fatalf("frames=%q want CopyInResponse/CommandComplete/RFQ", frameTypes(frames))
	}

	writeFrontendFrame(t, conn, protocol.MsgCopyFail, cstring("too late"))

	writeQuery(t, conn, "SELECT 1")
	frames = readUntilReady(t, conn)
	for _, f := range frames {
		if f.Type == protocol.MsgErrorResponse {
			t.Fatalf("stray ErrorResponse after post-COPY CopyFail: frames=%q", frameTypes(frames))
		}
	}
	if len(frames) != 4 {
		t.Fatalf("after stray CopyFail frames=%q want 4", frameTypes(frames))
	}
}
