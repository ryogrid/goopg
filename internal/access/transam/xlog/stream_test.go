package xlog

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// streamRecorder is a recordingPlugin that also implements StreamingPlugin.
type streamRecorder struct{ recordingPlugin }

func (p *streamRecorder) StreamStart(xid storage.TransactionID, first bool) error {
	s := "StreamStart"
	if first {
		s += "(first)"
	}
	p.calls = append(p.calls, s)
	return nil
}
func (p *streamRecorder) StreamStop() error {
	p.calls = append(p.calls, "StreamStop")
	return nil
}
func (p *streamRecorder) StreamCommit(xid storage.TransactionID, lsn uint64) error {
	p.calls = append(p.calls, "StreamCommit")
	return nil
}
func (p *streamRecorder) StreamAbort(xid, subxid storage.TransactionID, lsn uint64) error {
	p.calls = append(p.calls, "StreamAbort")
	return nil
}

// TestDecoderStreamsLargeTransactions pins M0149-0063a against PG 18.3's
// ReorderBufferCheckMemoryLimit / ReorderBufferStreamTXN: once the queued
// changes exceed logical_decoding_work_mem the transaction is streamed
// (stream start, its changes, stream stop) instead of waiting for its
// commit; changes queued after that go out at commit in a final segment
// followed by a stream commit, and an aborted streamed transaction gets a
// stream abort. A transaction under the limit still decodes at commit.
// debug_logical_replication_streaming = immediate streams each change.
func TestDecoderStreamsLargeTransactions(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 16500}
	ins := func(lsn uint64) Change {
		return Change{Kind: ChangeInsert, LSN: lsn, Rel: rel, NewTuple: bytes.Repeat([]byte{1}, 100)}
	}
	limit := 2 * changeSize(ins(0)) // the second change reaches the limit

	p := &streamRecorder{}
	d := NewDecoder(p)
	d.SetStreaming(limit, false)
	d.ApplyChange(10, ins(1))
	d.ApplyChange(10, ins(2)) // over the limit: streamed
	d.ApplyChange(10, ins(3))
	if err := d.takeStreamErr(); err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyCommitAt(10, 90, 100); err != nil {
		t.Fatal(err)
	}
	want := []string{"StreamStart(first)", "Change", "Change", "StreamStop", "StreamStart", "Change", "StreamStop", "StreamCommit"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("streamed commit: calls = %v\nwant %v", p.calls, want)
	}

	p.calls = nil
	d.ApplyChange(11, ins(4))
	d.ApplyChange(11, ins(5))
	if err := d.ApplyAbortAt(11, 120); err != nil {
		t.Fatal(err)
	}
	want = []string{"StreamStart(first)", "Change", "Change", "StreamStop", "StreamAbort"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("streamed abort: calls = %v\nwant %v", p.calls, want)
	}

	p.calls = nil
	d.ApplyChange(12, ins(6))
	if err := d.ApplyCommitAt(12, 130, 140); err != nil {
		t.Fatal(err)
	}
	if want = []string{"Begin", "Change", "Commit"}; !reflect.DeepEqual(p.calls, want) {
		t.Errorf("small transaction: calls = %v, want %v", p.calls, want)
	}
	if d.buf.size != 0 {
		t.Errorf("reorder buffer size = %d after every transaction ended, want 0", d.buf.size)
	}

	p.calls = nil
	d.SetStreaming(1<<30, true) // immediate
	d.ApplyChange(13, ins(7))
	d.ApplyChange(13, ins(8))
	if err := d.ApplyCommitAt(13, 150, 160); err != nil {
		t.Fatal(err)
	}
	want = []string{"StreamStart(first)", "Change", "StreamStop", "StreamStart", "Change", "StreamStop", "StreamCommit"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("immediate: calls = %v\nwant %v", p.calls, want)
	}
}

// TestPgOutputStreamMessages pins the stream wire format (proto.c
// logicalrep_write_stream_start / _stop / _commit / _abort): inside a
// segment each change message carries the xid after its type byte, and
// the stream abort carries abort_lsn / abort_time only under streaming =
// parallel.
func TestPgOutputStreamMessages(t *testing.T) {
	var buf bytes.Buffer
	p := NewPgOutput(&CatalogSnapshot{}, &buf)
	p.SetStreaming(true, false)
	if err := p.StreamStart(77, true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.writeChange([]byte{pgoInsert, 0xAA}); err != nil {
		t.Fatal(err)
	}
	if err := p.StreamStop(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.writeChange([]byte{pgoInsert, 0xBB}); err != nil {
		t.Fatal(err)
	}
	if err := p.StreamAbort(77, 77, 5); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	want := []byte{
		'S', 0, 0, 0, 77, 1, // stream start, first segment
		'I', 0, 0, 0, 77, 0xAA, // change with xid
		'E',       // stream stop
		'I', 0xBB, // outside a stream: no xid
		'A', 0, 0, 0, 77, 0, 0, 0, 77, // abort, no abort info
	}
	if !bytes.Equal(got, want) {
		t.Errorf("wire = %v\nwant %v", got, want)
	}

	buf.Reset()
	p.SetStreaming(true, true)
	if err := p.StreamAbort(78, 79, 0x10); err != nil {
		t.Fatal(err)
	}
	if n := buf.Len(); n != 1+4+4+8+8 {
		t.Errorf("parallel stream abort is %d bytes, want 25", n)
	}
	if !strings.HasPrefix(buf.String(), "A") {
		t.Errorf("parallel stream abort type byte = %q", buf.String()[:1])
	}
}
