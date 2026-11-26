package xlog

import (
	"reflect"
	"testing"
)

// TestDecoderWriteLocationsAndSkip pins the M0149-0057c positions: Begin
// goes out at the transaction's first change, each change at its own LSN,
// Commit at the commit end (logical decoding's write_location), the sent
// hook reports the commit end and the restart point, and a transaction
// committed at or before the skip point is not sent again.
func TestDecoderWriteLocationsAndSkip(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)
	var locs []uint64
	d.SetWriteLocationHook(func(lsn uint64) { locs = append(locs, lsn) })
	var sent [][2]uint64
	d.SetSentHook(func(c, r uint64) { sent = append(sent, [2]uint64{c, r}) })

	d.SetRecordStart(100)
	d.ApplyChange(5, Change{Kind: ChangeInsert, LSN: 120, NewTuple: []byte("a")})
	d.SetRecordStart(120)
	d.ApplyChange(5, Change{Kind: ChangeInsert, LSN: 140, NewTuple: []byte("b")})
	if got := d.SafeRestartLSN(140); got != 100 {
		t.Fatalf("SafeRestartLSN with xid 5 in flight = %d, want its first record's start 100", got)
	}
	if err := d.ApplyCommit(5, 160); err != nil {
		t.Fatal(err)
	}
	if want := []uint64{120, 120, 140, 160}; !reflect.DeepEqual(locs, want) {
		t.Fatalf("write locations = %v, want %v", locs, want)
	}
	if want := [][2]uint64{{160, 100}}; !reflect.DeepEqual(sent, want) {
		t.Fatalf("sent = %v, want %v", sent, want)
	}
	if got := d.SafeRestartLSN(160); got != 160 {
		t.Fatalf("SafeRestartLSN with nothing in flight = %d, want 160", got)
	}

	d.SetSkipThrough(200)
	d.ApplyChange(6, Change{Kind: ChangeInsert, LSN: 180, NewTuple: []byte("c")})
	if err := d.ApplyCommit(6, 200); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 4 {
		t.Fatalf("a transaction committed at the skip point was sent: %v", p.calls)
	}
}

// TestRestartCapFor: restart_lsn may follow the subscriber's flush only up
// to the first record of a transaction it has not finished — one sent but
// committing after flush — and never past the decoder's safe point.
func TestRestartCapFor(t *testing.T) {
	s := &SlotDecoder{}
	if got := s.RestartCapFor(500); got != 0 {
		t.Fatalf("cap before any record decoded = %d, want 0", got)
	}
	s.safeRestart.Store(900)
	s.noteSent(300, 200) // committed at 300, restart at 200
	s.noteSent(600, 450) // committed at 600, restart at 450
	// The subscriber confirmed a position inside the second transaction:
	// restart may not pass that transaction's first record.
	if got := s.RestartCapFor(500); got != 450 {
		t.Fatalf("RestartCapFor(500) = %d, want 450", got)
	}
	if len(s.sent) != 1 {
		t.Fatalf("the flushed transaction was not pruned: %v", s.sent)
	}
	if got := s.RestartCapFor(700); got != 700 {
		t.Fatalf("RestartCapFor(700) = %d, want 700", got)
	}
	s.safeRestart.Store(650)
	if got := s.RestartCapFor(800); got != 650 {
		t.Fatalf("RestartCapFor(800) with an in-flight transaction at 650 = %d, want 650", got)
	}
}
