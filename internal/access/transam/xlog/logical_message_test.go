package xlog

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// msgRecorder is an OutputPlugin + MessagePlugin that logs every callback.
type msgRecorder struct{ events []string }

func (m *msgRecorder) Begin(xid storage.TransactionID, _ uint64) error {
	m.events = append(m.events, fmt.Sprintf("B%d", xid))
	return nil
}
func (m *msgRecorder) Change(c Change) error {
	m.events = append(m.events, fmt.Sprintf("change%d", c.Kind))
	return nil
}
func (m *msgRecorder) Commit(xid storage.TransactionID, _ uint64) error {
	m.events = append(m.events, fmt.Sprintf("C%d", xid))
	return nil
}
func (m *msgRecorder) LogicalMessage(xid storage.TransactionID, lsn uint64, transactional bool, prefix string, content []byte) error {
	m.events = append(m.events, fmt.Sprintf("M%d/%v/%s=%s", xid, transactional, prefix, content))
	return nil
}

// TestLogicalMessageDecoding: pg_logical_emit_message's record survives the
// WAL round trip and reaches the output plugin as logical decoding delivers
// it (logicalmsg_decode, ReorderBufferQueueMessage): a transactional message
// inside its transaction's Begin/Commit, dropped when the transaction aborts;
// a non-transactional one at once, outside any transaction; a message from
// another database not at all.
func TestLogicalMessageDecoding(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: 1 << 16, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	app := func(payload []byte, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	const db = 5
	app(EncodeLogicalMessagePG(7, LogicalMessage{DBOid: db, Transactional: true, Prefix: "p", Content: []byte("in-xact")}))
	app(EncodeLogicalMessagePG(0, LogicalMessage{DBOid: db, Prefix: "p", Content: []byte("no-xact")}))
	app(EncodeLogicalMessagePG(8, LogicalMessage{DBOid: db, Transactional: true, Prefix: "p", Content: []byte("aborted")}))
	app(EncodeLogicalMessagePG(9, LogicalMessage{DBOid: 6, Prefix: "p", Content: []byte("other-db")}))
	app(EncodeXactAbortPG(8))
	app(EncodeXactCommitPG(7, false))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadAll(dir, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	rec := &msgRecorder{}
	d := NewDecoder(rec)
	d.SetDatabase(db)
	for _, r := range recs {
		if err := Classify(d, r); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"M0/false/p=no-xact", "B7", "M7/true/p=in-xact", "C7"}
	if fmt.Sprint(rec.events) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", rec.events, want)
	}
}

// TestLogicalMessageCodec pins xl_logical_message's layout (message.h):
// dbId, transactional, padding, prefix_size (with the NUL), message_size,
// then the prefix and the message.
func TestLogicalMessageCodec(t *testing.T) {
	framed, err := EncodeLogicalMessagePG(3, LogicalMessage{DBOid: 16384, Transactional: true, Prefix: "ab", Content: []byte{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	rmid, info, xid, body, ok := unframePGAssembled(framed)
	if !ok || rmid != RmgrLogicalMessage || info != xlogLogicalMessage || xid != 3 {
		t.Fatalf("envelope = %v %v %v %v", rmid, info, xid, ok)
	}
	want := []byte{0x00, 0x40, 0, 0, 1, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 'a', 'b', 0, 1, 2, 3}
	if !bytes.HasSuffix(body, want) {
		t.Errorf("record body % x does not end with the main data % x", body, want)
	}
	m, err := DecodeLogicalMessage(want)
	if err != nil || m.DBOid != 16384 || !m.Transactional || m.Prefix != "ab" || !bytes.Equal(m.Content, []byte{1, 2, 3}) {
		t.Errorf("decode = %+v, %v", m, err)
	}
	if _, err := DecodeLogicalMessage(want[:26]); err == nil {
		t.Error("a truncated message decoded")
	}
}

// TestPgOutputMessagesOption: pgoutput writes a MESSAGE frame only when the
// subscriber set the "messages" option (pgoutput_message).
func TestPgOutputMessagesOption(t *testing.T) {
	var buf bytes.Buffer
	p := NewPgOutput(nil, &buf)
	if err := p.LogicalMessage(1, 0x10, true, "p", []byte("x")); err != nil || buf.Len() != 0 {
		t.Fatalf("messages off wrote %d bytes (err %v)", buf.Len(), err)
	}
	p.SetMessages(true)
	if err := p.LogicalMessage(1, 0x10, true, "p", []byte("x")); err != nil || buf.Len() == 0 || buf.Bytes()[0] != 'M' {
		t.Fatalf("messages on wrote % x (err %v)", buf.Bytes(), err)
	}
}
