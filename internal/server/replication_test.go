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
	addr, slots, _, stop := startReplicationTestServerFull(t)
	return addr, slots, stop
}

// startReplicationTestServerFull is the variant used by tests that
// need the WAL writer too (e.g., START_REPLICATION). Returns the
// listen address, the slot registry, the live writer, and a stop
// func.
func startReplicationTestServerFull(t *testing.T) (string, *wal.Slots, *wal.Writer, func()) {
	t.Helper()
	addr, slots, writer, _, stop := startReplicationTestServerWithDir(t)
	return addr, slots, writer, stop
}

// startReplicationTestServerWithDir is the M0102-0003 variant that
// also exposes the walDir so TIMELINE_HISTORY tests can seed a
// `<NN>.history` file in the spot the server reads from.
func startReplicationTestServerWithDir(t *testing.T) (string, *wal.Slots, *wal.Writer, string, func()) {
	t.Helper()
	dataDir := t.TempDir()
	slots, err := wal.OpenSlots(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	walDir := dataDir + "/pg_wal"
	walWriter, err := wal.NewWriter(wal.Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Slots:            slots,
		WAL:              walWriter,
		WALDirPath:       walDir,
		WALSegmentSize:   4096,
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
		_ = walWriter.Close()
	}
	return addr, slots, walWriter, walDir, stop
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


// TestReplicationCreateLogicalSlot covers CREATE_REPLICATION_SLOT
// name LOGICAL pgoutput (M0103-0004): a libpq subscriber sends this
// immediately after the startup handshake when CREATE SUBSCRIPTION
// runs against goopg-as-publisher. The reply must include the
// `output_plugin` column populated with "pgoutput" (PHYSICAL returns
// NULL there); `snapshot_name` is NULL in v0.
func TestReplicationCreateLogicalSlot(t *testing.T) {
	addr, slots, stop := startReplicationTestServer(t)
	defer stop()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, `CREATE_REPLICATION_SLOT sub_a LOGICAL pgoutput NOEXPORT_SNAPSHOT`)
	frames := readUntilReadyForQuery(t, r)
	if frames[0].Type != protocol.MsgRowDescription {
		t.Fatalf("CREATE_REPLICATION_SLOT first frame = %c, want T", frames[0].Type)
	}
	if frames[1].Type != protocol.MsgDataRow {
		t.Fatalf("CREATE_REPLICATION_SLOT second frame = %c, want D", frames[1].Type)
	}
	cells := decodeDataRow(t, frames[1].Payload)
	if len(cells) != 4 {
		t.Fatalf("LOGICAL CREATE row col count = %d, want 4", len(cells))
	}
	if string(cells[0]) != "sub_a" {
		t.Errorf("slot_name = %q, want sub_a", cells[0])
	}
	if cells[2] != nil {
		t.Errorf("snapshot_name = %q, want NULL (v0 has no snapshot exporter)", cells[2])
	}
	if string(cells[3]) != "pgoutput" {
		t.Errorf("output_plugin = %q, want pgoutput", cells[3])
	}
	// Backing store must show a LOGICAL slot.
	slot, err := slots.Get("sub_a")
	if err != nil {
		t.Fatalf("slots.Get(sub_a): %v", err)
	}
	if slot.Kind != wal.SlotLogical {
		t.Errorf("slot.Kind = %v, want SlotLogical", slot.Kind)
	}
}

// TestReplicationCreateLogicalSlotRejectsUnknownPlugin: only `pgoutput`
// is accepted; other plugin names land with feature_not_supported so
// the libpq client gets a deterministic error rather than a hang.
func TestReplicationCreateLogicalSlotRejectsUnknownPlugin(t *testing.T) {
	addr, _, stop := startReplicationTestServer(t)
	defer stop()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, `CREATE_REPLICATION_SLOT sub_b LOGICAL test_decoding`)
	frames := readUntilReadyForQuery(t, r)
	if frames[0].Type != protocol.MsgErrorResponse {
		t.Fatalf("unknown plugin first frame = %c, want E", frames[0].Type)
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

// TestReplicationStartReplicationStreamsRecord exercises the full
// walsender path: a client connects in replication mode, creates a
// slot, issues START_REPLICATION, and reads a CopyBoth + WAL-data
// frame after the primary appends a WAL record. Pins the wire shape
// of the streamed payload (decode via DecodeReplicationMessage).
func TestReplicationStartReplicationStreamsRecord(t *testing.T) {
	addr, _, walWriter, stop := startReplicationTestServerFull(t)
	defer stop()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, `CREATE_REPLICATION_SLOT primary PHYSICAL`)
	_ = readUntilReadyForQuery(t, r)

	sendQuery(t, w, `START_REPLICATION SLOT primary PHYSICAL 0/0`)

	// First backend frame is CopyBothResponse.
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read CopyBothResponse: %v", err)
	}
	if f.Type != protocol.MsgCopyBothResponse {
		t.Fatalf("first frame = %c, want W (CopyBothResponse)", f.Type)
	}

	// Append a WAL record from the primary side.
	want := []byte("hello replication world")
	if _, _, err := walWriter.Append(want); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Read CopyData frames until we see the WAL-data payload our
	// record produced. Keepalives may come first if the timer
	// races us.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for streamed WAL record")
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read CopyData: %v", err)
		}
		if f.Type != protocol.MsgCopyData {
			continue
		}
		parsed, kind, err := protocol.DecodeReplicationMessage(f.Payload)
		if err != nil {
			t.Fatalf("decode replication message: %v", err)
		}
		if kind != protocol.ReplMsgWALData {
			continue
		}
		m := parsed.(*protocol.WALDataMessage)
		if string(m.WALBytes) != string(want) {
			t.Fatalf("WAL-data payload = %q, want %q", m.WALBytes, want)
		}
		// StartLSN / EndLSN must match what the writer reported.
		if m.StartLSN == 0 || m.EndLSN <= m.StartLSN {
			t.Errorf("LSN range = (%d, %d), want non-trivial", m.StartLSN, m.EndLSN)
		}
		return
	}
}

// TestReplicationStartReplicationRejectsLogical: the v0 spec covers
// PHYSICAL only. LOGICAL or unsupported keywords yield a SyntaxError
// or feature-not-supported, and the connection survives.
func TestReplicationStartReplicationRejectsLogical(t *testing.T) {
	addr, _, _, stop := startReplicationTestServerFull(t)
	defer stop()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, `START_REPLICATION LOGICAL 0/0`)
	frames := readUntilReadyForQuery(t, r)
	if frames[0].Type != protocol.MsgErrorResponse {
		t.Fatalf("LOGICAL first frame = %c, want E (ErrorResponse)", frames[0].Type)
	}
	// IDENTIFY_SYSTEM still works after the rejection.
	sendQuery(t, w, "IDENTIFY_SYSTEM")
	frames = readUntilReadyForQuery(t, r)
	if frames[0].Type != protocol.MsgRowDescription {
		t.Errorf("post-error IDENTIFY_SYSTEM = %c, want T", frames[0].Type)
	}
}


// TestReplicationTimelineHistoryReturnsFile verifies the
// M0102-0003 TIMELINE_HISTORY <tli> wire path: the server reads
// `<WALDirPath>/<tli>.history` and returns a single (filename text,
// content bytea) row. A request for a TLI without a history file
// (e.g. TLI=1 on a fresh primary) returns the empty content bytes
// rather than an error — matching upstream's walreceiver contract.
func TestReplicationTimelineHistoryReturnsFile(t *testing.T) {
	addr, _, _, walDir, stop := startReplicationTestServerWithDir(t)
	defer stop()

	// Seed a TLI=2 history file in the test server's WAL dir.
	want := []wal.TimelineHistoryEntry{
		{TLI: 1, SwitchLSN: 0x0000000016000000, Reason: "no recovery target specified"},
	}
	if err := wal.WriteHistory(walDir, 2, want); err != nil {
		t.Fatalf("seed history: %v", err)
	}

	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, "TIMELINE_HISTORY 2")
	frames := readUntilReadyForQuery(t, r)
	if len(frames) != 4 {
		t.Fatalf("frame count = %d, want 4 (got types %s)", len(frames), replicationFrameTypes(frames))
	}
	if frames[0].Type != protocol.MsgRowDescription {
		t.Errorf("frame[0] = %c, want T", frames[0].Type)
	}
	if frames[1].Type != protocol.MsgDataRow {
		t.Fatalf("frame[1] = %c, want D", frames[1].Type)
	}
	cells := decodeDataRow(t, frames[1].Payload)
	if len(cells) != 2 {
		t.Fatalf("DataRow columns = %d, want 2", len(cells))
	}
	if string(cells[0]) != "00000002.history" {
		t.Errorf("filename = %q, want 00000002.history", cells[0])
	}
	wantBody := "1\t0/16000000\tno recovery target specified\n"
	if string(cells[1]) != wantBody {
		t.Errorf("content = %q, want %q", cells[1], wantBody)
	}
	if frames[2].Type != protocol.MsgCommandComplete ||
		!hasCommandTag(frames[2].Payload, "TIMELINE_HISTORY") {
		t.Errorf("CommandComplete tag mismatch: %q", frames[2].Payload)
	}
	if frames[3].Type != protocol.MsgReadyForQuery {
		t.Errorf("frame[3] = %c, want Z", frames[3].Type)
	}
}

// TestReplicationTimelineHistoryMissingReturnsEmptyContent: per the
// upstream walreceiver contract, requesting a TLI whose .history
// file does not exist (typically TLI=1 on a primary that has never
// promoted) returns a row with the synthesised filename and a NULL
// content bytea, not an error frame.
func TestReplicationTimelineHistoryMissingReturnsEmptyContent(t *testing.T) {
	addr, _, _, stop := startReplicationTestServerFull(t)
	defer stop()

	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, "TIMELINE_HISTORY 1")
	frames := readUntilReadyForQuery(t, r)
	if len(frames) != 4 {
		t.Fatalf("frame count = %d, want 4 (got types %s)", len(frames), replicationFrameTypes(frames))
	}
	cells := decodeDataRow(t, frames[1].Payload)
	if len(cells) != 2 {
		t.Fatalf("DataRow columns = %d, want 2", len(cells))
	}
	if string(cells[0]) != "00000001.history" {
		t.Errorf("filename = %q, want 00000001.history", cells[0])
	}
	if cells[1] != nil {
		t.Errorf("content = %q, want NULL for missing TLI", cells[1])
	}
}

func replicationFrameTypes(frames []protocol.Frame) string {
	out := make([]byte, len(frames))
	for i, f := range frames {
		out[i] = f.Type
	}
	return string(out)
}
