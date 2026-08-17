package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

// TestNotifyEntryBytes verifies the modelled async-queue entry size used to
// drive notify-SLRU page-zeroing accounting: positive, 8-aligned, and growing
// with the payload. M0118-0009 (`stats`, SLRU rung).
func TestNotifyEntryBytes(t *testing.T) {
	small := notifyEntryBytes("c", "")
	if small <= 0 || small%8 != 0 {
		t.Fatalf("empty-payload entry bytes = %d, want positive 8-aligned", small)
	}
	big := notifyEntryBytes("stats_test_use", string(make([]byte, 4096)))
	if big%8 != 0 {
		t.Fatalf("big entry bytes = %d not 8-aligned", big)
	}
	if big <= small || big < 4096 {
		t.Fatalf("big entry bytes = %d, want > %d and >= 4096", big, small)
	}
}

// TestHasAnyListener checks the listener gate that governs whether a committed
// notification writes to the modelled async queue. M0118-0009.
func TestHasAnyListener(t *testing.T) {
	h := newNotifyHub()
	if h.hasAnyListener() {
		t.Fatal("fresh hub reports a listener")
	}
	sess := misc.NewSessionRegistry(nil)
	h.Listen(sess, "ch")
	if !h.hasAnyListener() {
		t.Fatal("after Listen, hasAnyListener = false")
	}
	h.Unlisten(sess, "ch")
	if h.hasAnyListener() {
		t.Fatal("after Unlisten, hasAnyListener = true")
	}
}
