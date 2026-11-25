package replication

import "testing"

// TestLogicalReceiverFeedbackPositions pins send_feedback's position choice
// (worker.c, M0149-0040a): write is the last received position — a 'w'
// frame's start or walEnd, or a keepalive's walEnd — and flush/apply follow
// it while no transaction is in progress, so an idle subscriber confirms up
// to the publisher's WAL end; inside a transaction they stay at the last
// applied commit. No reported position moves backwards.
func TestLogicalReceiverFeedbackPositions(t *testing.T) {
	r := NewLogicalReceiver(LogicalReceiverConfig{StartLSN: 100})
	if w, f := r.feedbackPositions(); w != 100 || f != 100 {
		t.Fatalf("fresh: write %d flush %d, want 100 100", w, f)
	}
	r.noteReceived(500) // keepalive walEnd on a quiet publisher
	if w, f := r.feedbackPositions(); w != 500 || f != 500 {
		t.Fatalf("idle: write %d flush %d, want 500 500", w, f)
	}
	if !r.statusMoved() {
		t.Fatal("a moved position must trigger a reply")
	}
	r.lastSentWrite.Store(500)
	r.lastSentFlush.Store(500)
	if r.statusMoved() {
		t.Fatal("an unmoved position must not trigger a reply")
	}
	r.inXact.Store(true) // Begin seen, Commit not yet
	r.noteReceived(800)
	if w, f := r.feedbackPositions(); w != 800 || f != 500 {
		t.Fatalf("in transaction: write %d flush %d, want 800 500", w, f)
	}
	r.noteReceived(300) // an older position never lowers last_received
	r.inXact.Store(false)
	r.applyLSN.Store(800)
	if w, f := r.feedbackPositions(); w != 800 || f != 800 {
		t.Fatalf("after commit: write %d flush %d, want 800 800", w, f)
	}
}
