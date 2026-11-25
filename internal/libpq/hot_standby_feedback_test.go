package libpq

import (
	"testing"
	"time"
)

// TestHotStandbyFeedbackRoundTrip pins the 'h' message layout
// (XLogWalRcvSendHSFeedback: type, sendTime, xmin, xmin epoch,
// catalog_xmin, catalog_xmin epoch — 25 bytes, big-endian). M0149-0077i.
func TestHotStandbyFeedbackRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	frame := EncodeHotStandbyFeedback(now, 742, 1, 0, 0)
	if len(frame) != 25 || frame[0] != 'h' {
		t.Fatalf("frame = %x", frame)
	}
	if frame[9] != 0 || frame[12] != 0xe6 { // xmin 742 = 0x000002e6 big-endian
		t.Fatalf("xmin bytes = %x", frame[9:13])
	}
	m, kind, err := DecodeReplicationMessage(frame)
	if err != nil || kind != ReplMsgHotStandbyFeedback {
		t.Fatalf("decode: kind=%q err=%v", kind, err)
	}
	hs := m.(*HotStandbyFeedback)
	if hs.Xmin != 742 || hs.XminEpoch != 1 || hs.CatalogXmin != 0 || !hs.SendTime.Equal(now) {
		t.Fatalf("decoded %+v", hs)
	}
	if _, _, err := DecodeReplicationMessage(frame[:24]); err == nil {
		t.Fatal("short 'h' message decoded")
	}
}
