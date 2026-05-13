package wal

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestClassifyHeapInsertRoutesByXmin: a HeapInsert record with a
// heap-tuple body whose xmin=42 must dispatch a ChangeInsert
// under xid 42.
func TestClassifyHeapInsertRoutesByXmin(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	tuple, err := storage.NewHeapTuple(42, 0, []byte("payload")).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insertPayload := EncodeHeapInsert(rel, 5, 1, tuple)
	if err := Classify(d, Record{Payload: insertPayload, EndLSN: 100}); err != nil {
		t.Fatal(err)
	}
	if d.Active() != 1 {
		t.Fatalf("Active=%d want 1 (xid 42)", d.Active())
	}

	// Commit and verify the plugin saw the right xact frame.
	commitPayload := EncodeXactCommit(42)
	if err := Classify(d, Record{Payload: commitPayload, EndLSN: 200}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Begin", "Change", "Commit"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("plugin calls=%v want %v", p.calls, want)
	}
	if d.Active() != 0 {
		t.Errorf("Active after commit=%d want 0", d.Active())
	}
}

// TestClassifyHeapDeleteRoutesByXmax: a HeapDelete record carries
// xmax in the payload — that's the xact id the change must be
// queued under.
func TestClassifyHeapDeleteRoutesByXmax(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	deletePayload := EncodeHeapDelete(rel, 5, 1, 77, nil)
	if err := Classify(d, Record{Payload: deletePayload, EndLSN: 50}); err != nil {
		t.Fatal(err)
	}
	if d.Active() != 1 {
		t.Errorf("Active=%d want 1 (xid 77)", d.Active())
	}
	if err := Classify(d, Record{Payload: EncodeXactCommit(77), EndLSN: 150}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Begin", "Change", "Commit"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("plugin calls=%v want %v", p.calls, want)
	}
}

// TestClassifyAbortDropsXact: an XactAbort marker drops the
// xact's queued changes; subsequent commit on a different xact
// is unaffected.
func TestClassifyAbortDropsXact(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	tuple, _ := storage.NewHeapTuple(11, 0, []byte("a")).MarshalBinary()
	if err := Classify(d, Record{Payload: EncodeHeapInsert(rel, 0, 1, tuple), EndLSN: 1}); err != nil {
		t.Fatal(err)
	}
	if err := Classify(d, Record{Payload: EncodeXactAbort(11), EndLSN: 2}); err != nil {
		t.Fatal(err)
	}
	if d.Active() != 0 {
		t.Errorf("Active after abort=%d want 0", d.Active())
	}
	if len(p.calls) != 0 {
		t.Errorf("plugin saw aborted xact: %v", p.calls)
	}
}

// TestClassifyIsolatesConcurrentXacts: two xids interleaved must
// stay isolated through the classifier and only commit their own
// changes through the plugin.
func TestClassifyIsolatesConcurrentXacts(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	t1, _ := storage.NewHeapTuple(101, 0, []byte("a")).MarshalBinary()
	t2, _ := storage.NewHeapTuple(102, 0, []byte("b")).MarshalBinary()
	t1b, _ := storage.NewHeapTuple(101, 0, []byte("c")).MarshalBinary()

	must := func(rec Record) {
		t.Helper()
		if err := Classify(d, rec); err != nil {
			t.Fatal(err)
		}
	}
	must(Record{Payload: EncodeHeapInsert(rel, 0, 1, t1), EndLSN: 10})
	must(Record{Payload: EncodeHeapInsert(rel, 0, 2, t2), EndLSN: 20})
	must(Record{Payload: EncodeHeapInsert(rel, 0, 3, t1b), EndLSN: 30})
	if d.Active() != 2 {
		t.Errorf("Active mid-stream=%d want 2", d.Active())
	}

	must(Record{Payload: EncodeXactCommit(101), EndLSN: 40})
	want := []string{"Begin", "Change", "Change", "Commit"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("after committing 101, plugin=%v want %v", p.calls, want)
	}

	must(Record{Payload: EncodeXactCommit(102), EndLSN: 50})
	want = append(want, "Begin", "Change", "Commit")
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("after committing 102, plugin=%v want %v", p.calls, want)
	}
}

// TestClassifySkipsNonTxRecords: vacuum, btree, page-image,
// checkpoint records are all dropped silently — they're not
// user-data transactional events.
func TestClassifySkipsNonTxRecords(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	for _, payload := range [][]byte{
		EncodeCheckpoint(),
		EncodeHeapVacuum(rel, 0, []uint16{1, 2}),
		EncodeBtreeInsert(rel, 0, []byte("item")),
	} {
		if err := Classify(d, Record{Payload: payload, EndLSN: 10}); err != nil {
			t.Errorf("payload kind=%d err=%v", payload[0], err)
		}
	}
	if d.Active() != 0 {
		t.Errorf("non-tx records leaked into the buffer: Active=%d", d.Active())
	}
	if len(p.calls) != 0 {
		t.Errorf("non-tx records reached the plugin: %v", p.calls)
	}
}

// TestEncodeDecodeXactMarker pins the round-trip for the new
// commit / abort markers.
func TestEncodeDecodeXactMarker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		encode func(storage.TransactionID) []byte
		kind   byte
	}{
		{"commit", EncodeXactCommit, RecordKindXactCommit},
		{"abort", EncodeXactAbort, RecordKindXactAbort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.encode(0xABCD1234)
			if p[0] != tc.kind {
				t.Errorf("kind=%d want %d", p[0], tc.kind)
			}
			xid, err := DecodeXactMarker(p)
			if err != nil {
				t.Fatal(err)
			}
			if xid != 0xABCD1234 {
				t.Errorf("xid=%x want 0xABCD1234", xid)
			}
		})
	}
}
