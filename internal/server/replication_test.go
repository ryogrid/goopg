package server

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/wal"
)

// startReplicationTestServer brings up a Server with a Slots registry
// rooted at a tempdir but no storage handles — replication command
// dispatch only needs the slot store + (optionally) a WAL writer.
func startReplicationTestServer(t *testing.T) (string, *wal.Slots, func()) {
	t.Helper()
	dataDir := t.TempDir()
	slots, err := wal.OpenSlots(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Slots:            slots,
		SystemID:         "7300000000000000001",
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	addr := srv.Addr().String()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Server.Run did not return within 2s of cancel")
		}
	}
	return addr, slots, stop
}

// dialReplication completes the startup handshake with replication=true
// and returns a FrameReader/Writer pair positioned at ReadyForQuery.
func dialReplication(t *testing.T, addr string) (net.Conn, *protocol.FrameReader, *protocol.FrameWriter) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	writeStartupPacket(t, conn, map[string]string{
		"user":        "rep",
		"replication": "true",
	})
	r := protocol.NewFrameReader(conn)
	w := protocol.NewFrameWriter(conn)
	// Drain the handshake: AuthOK + N×ParameterStatus + BackendKeyData
	// + ReadyForQuery.
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("handshake read: %v", err)
		}
		if f.Type == protocol.MsgReadyForQuery {
			break
		}
	}
	return conn, r, w
}

// sendQuery emits a Query frame with the SQL string, including the
// trailing NUL the protocol requires.
func sendQuery(t *testing.T, w *protocol.FrameWriter, sql string) {
	t.Helper()
	body := append([]byte(sql), 0)
	if err := w.WriteFrame(protocol.MsgQuery, body); err != nil {
		t.Fatalf("write Query: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// readUntilReadyForQuery drains backend frames until a ReadyForQuery,
// returning every frame in order. Useful for asserting on the entire
// reply tuple of a single Query.
func readUntilReadyForQuery(t *testing.T, r *protocol.FrameReader) []protocol.Frame {
	t.Helper()
	var out []protocol.Frame
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		// Copy payload; the FrameReader reuses its buffer.
		copyPayload := make([]byte, len(f.Payload))
		copy(copyPayload, f.Payload)
		out = append(out, protocol.Frame{Type: f.Type, Payload: copyPayload})
		if f.Type == protocol.MsgReadyForQuery {
			return out
		}
	}
}

// TestReplicationIdentifySystem covers the IDENTIFY_SYSTEM reply
// shape (4-column RowDescription + 1 DataRow + CommandComplete +
// ReadyForQuery) and the SystemID echoing from Config.
func TestReplicationIdentifySystem(t *testing.T) {
	addr, _, stop := startReplicationTestServer(t)
	defer stop()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, "IDENTIFY_SYSTEM")
	frames := readUntilReadyForQuery(t, r)

	// Expected: T (RowDescription), D (DataRow), C (CommandComplete), Z.
	if len(frames) != 4 {
		t.Fatalf("frame count = %d, want 4 (got types %s)", len(frames), replicationFrameTypes(frames))
	}
	if frames[0].Type != protocol.MsgRowDescription {
		t.Errorf("frame[0] = %c, want T", frames[0].Type)
	}
	if frames[1].Type != protocol.MsgDataRow {
		t.Fatalf("frame[1] = %c, want D", frames[1].Type)
	}
	// Validate the DataRow has exactly 4 columns and column 0 is the
	// SystemID we configured.
	cells := decodeDataRow(t, frames[1].Payload)
	if len(cells) != 4 {
		t.Fatalf("DataRow columns = %d, want 4", len(cells))
	}
	if string(cells[0]) != "7300000000000000001" {
		t.Errorf("systemid = %q, want 7300000000000000001", cells[0])
	}
	if string(cells[1]) != "1" {
		t.Errorf("timeline = %q, want 1", cells[1])
	}
	// xlogpos is "0/0" because no WAL writer is wired in this test.
	if string(cells[2]) != "0/0" {
		t.Errorf("xlogpos = %q, want 0/0", cells[2])
	}
	if cells[3] != nil {
		t.Errorf("dbname = %q, want NULL", cells[3])
	}
	if frames[2].Type != protocol.MsgCommandComplete ||
		!hasCommandTag(frames[2].Payload, "IDENTIFY_SYSTEM") {
		t.Errorf("CommandComplete tag mismatch: %q", frames[2].Payload)
	}
	if frames[3].Type != protocol.MsgReadyForQuery {
		t.Errorf("frame[3] = %c, want Z", frames[3].Type)
	}
}

// TestReplicationCreateAndDropSlot covers CREATE_REPLICATION_SLOT
// (writes the slot to disk via the Slots store) and
// DROP_REPLICATION_SLOT (removes it).
func TestReplicationCreateAndDropSlot(t *testing.T) {
	addr, slots, stop := startReplicationTestServer(t)
	defer stop()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, `CREATE_REPLICATION_SLOT primary PHYSICAL`)
	frames := readUntilReadyForQuery(t, r)
	if frames[0].Type != protocol.MsgRowDescription {
		t.Fatalf("CREATE_REPLICATION_SLOT first frame = %c, want T", frames[0].Type)
	}
	if frames[1].Type != protocol.MsgDataRow {
		t.Fatalf("CREATE_REPLICATION_SLOT second frame = %c, want D", frames[1].Type)
	}
	cells := decodeDataRow(t, frames[1].Payload)
	if string(cells[0]) != "primary" {
		t.Errorf("slot_name = %q, want primary", cells[0])
	}
	// Backing store must show the slot.
	if _, err := slots.Get("primary"); err != nil {
		t.Errorf("slots.Get(primary) after CREATE: %v", err)
	}

	sendQuery(t, w, `DROP_REPLICATION_SLOT primary`)
	frames = readUntilReadyForQuery(t, r)
	if frames[0].Type != protocol.MsgCommandComplete ||
		!hasCommandTag(frames[0].Payload, "DROP_REPLICATION_SLOT") {
		t.Errorf("DROP_REPLICATION_SLOT first frame = (%c, %q), want C+tag", frames[0].Type, frames[0].Payload)
	}
	if _, err := slots.Get("primary"); err == nil {
		t.Errorf("slots.Get(primary) after DROP: expected ErrSlotNotFound, got nil")
	}
}

// TestReplicationSlotInvalidName: server must reject CREATE with a
// non-conforming name via ErrorResponse, then continue serving.
func TestReplicationSlotInvalidName(t *testing.T) {
	addr, _, stop := startReplicationTestServer(t)
	defer stop()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, `CREATE_REPLICATION_SLOT "Has-Dash" PHYSICAL`)
	frames := readUntilReadyForQuery(t, r)
	if frames[0].Type != protocol.MsgErrorResponse {
		t.Fatalf("invalid name first frame = %c, want E (ErrorResponse)", frames[0].Type)
	}
	// Connection must still be usable: a follow-up IDENTIFY_SYSTEM
	// works.
	sendQuery(t, w, "IDENTIFY_SYSTEM")
	frames = readUntilReadyForQuery(t, r)
	if frames[0].Type != protocol.MsgRowDescription {
		t.Errorf("post-error IDENTIFY_SYSTEM first frame = %c, want T", frames[0].Type)
	}
}

// decodeDataRow parses a DataRow payload into per-column byte slices.
// nil indicates a NULL column. Format:
//
//	int16 ncolumns | { int32 length | bytes[length] | length=-1 means NULL } * ncolumns
func decodeDataRow(t *testing.T, payload []byte) [][]byte {
	t.Helper()
	if len(payload) < 2 {
		t.Fatalf("DataRow payload too short: %d", len(payload))
	}
	n := binary.BigEndian.Uint16(payload[:2])
	out := make([][]byte, n)
	off := 2
	for i := 0; i < int(n); i++ {
		if off+4 > len(payload) {
			t.Fatalf("DataRow truncated at column %d", i)
		}
		length := int32(binary.BigEndian.Uint32(payload[off : off+4]))
		off += 4
		if length == -1 {
			out[i] = nil
			continue
		}
		if off+int(length) > len(payload) {
			t.Fatalf("DataRow value at column %d truncated", i)
		}
		out[i] = make([]byte, length)
		copy(out[i], payload[off:off+int(length)])
		off += int(length)
	}
	return out
}

// hasCommandTag reports whether the CommandComplete payload (a
// NUL-terminated tag string) starts with want.
func hasCommandTag(payload []byte, want string) bool {
	if len(payload) < len(want)+1 {
		return false
	}
	if string(payload[:len(want)]) != want {
		return false
	}
	return payload[len(want)] == 0 || payload[len(want)] == ' '
}

func replicationFrameTypes(frames []protocol.Frame) string {
	out := make([]byte, len(frames))
	for i, f := range frames {
		out[i] = f.Type
	}
	return string(out)
}
