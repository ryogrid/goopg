package backup

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestBaseBackupThrottle pins MAX_RATE throttling (bbsink_throttle): the
// streamer sleeps until the bytes sent fit the rate, returns at once when
// they already do, and a cancelled statement wakes it with the context
// error — which ReplyBaseBackup turns into 57014 (M0149-0077l).
func TestBaseBackupThrottle(t *testing.T) {
	s := &baseBackupStreamer{ctx: context.Background(), maxRateBytes: 1 << 20, start: time.Now(), bytesDone: 1 << 17}
	t0 := time.Now()
	if err := s.throttle(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < 80*time.Millisecond {
		t.Fatalf("128 KiB at 1 MiB/s slept %v, want ~125ms", d)
	}
	s.bytesDone = 0
	if err := s.throttle(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s = &baseBackupStreamer{ctx: ctx, maxRateBytes: 32 * 1024, start: time.Now(), bytesDone: 1 << 20}
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	t0 = time.Now()
	if err := s.throttle(); !errors.Is(err, context.Canceled) {
		t.Fatalf("throttle after cancel = %v", err)
	}
	if time.Since(t0) > 5*time.Second {
		t.Fatal("cancel did not wake the throttle")
	}
}

// TestParseMaxRateOption pins MAX_RATE parsing; the range check
// (32 .. 1048576 kB/s) runs in ReplyBaseBackup with 22003.
func TestParseMaxRateOption(t *testing.T) {
	o, err := parseBaseBackupOptions("(CHECKPOINT 'fast', MAX_RATE 32)")
	if err != nil || !o.MaxRateSet || o.MaxRate != 32 {
		t.Fatalf("opts = %+v, err = %v", o, err)
	}
	if _, err := parseBaseBackupOptions("(MAX_RATE 'x')"); err == nil {
		t.Fatal("non-integer MAX_RATE accepted")
	}
}
