package replication_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/replication"
)

// TestWalReceiverEndsHistoricTimeline: against a primary on timeline 3, a
// walreceiver that starts on timeline 1 receives timeline 1's WAL up to its
// switch point, answers the server's CopyDone (libpqrcv_endstreaming), and
// Run returns *EndOfTimeline naming the next timeline. Once the standby's
// writer has moved onto timeline 2 at that point, the next connection
// streams timeline 2 to its own end, and after the move to timeline 3 the
// current timeline streams normally. A connection that starts exactly at a
// switch point gets the end of timeline from DialWalReceiver (no COPY).
func TestWalReceiverEndsHistoricTimeline(t *testing.T) {
	addr, primary, walDir, stop := startTimelinePrimary(t, 3, nil)
	defer stop()
	var ends []uint64
	for i := 0; i < 6; i++ {
		_, end, err := primary.Append([]byte(strings.Repeat(fmt.Sprintf("rec%d-", i), 40)))
		if err != nil {
			t.Fatal(err)
		}
		ends = append(ends, end)
	}
	sw1, sw2 := ends[1], ends[3]
	writeHistory(t, walDir, 3, fmt.Sprintf("1\t%X/%X\tx\n2\t%X/%X\tx\n", uint32(sw1>>32), uint32(sw1), uint32(sw2>>32), uint32(sw2)))
	writeHistory(t, walDir, 2, fmt.Sprintf("1\t%X/%X\tx\n", uint32(sw1>>32), uint32(sw1)))

	standby, standbyDir := newStandbyWAL(t)
	dial := func(startLSN uint64) (*replication.WalReceiver, error) {
		return replication.DialWalReceiver(context.Background(), replication.WalReceiverConfig{
			PrimaryAddr:    addr,
			User:           "rep",
			WAL:            standby,
			StartLSN:       startLSN,
			StatusInterval: 200 * time.Millisecond,
			DialTimeout:    3 * time.Second,
			WALDir:         standbyDir,
		})
	}
	streamToEnd := func(wantTLI, wantNext uint32, wantEnd uint64) {
		t.Helper()
		rec, err := dial(standby.WrittenLSN())
		if err != nil {
			t.Fatalf("dial on timeline %d: %v", wantTLI, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = rec.Run(ctx)
		var eot *replication.EndOfTimeline
		if !errors.As(err, &eot) {
			t.Fatalf("Run on timeline %d = %v, want *EndOfTimeline", wantTLI, err)
		}
		if eot.TLI != wantTLI || eot.NextTLI != wantNext || eot.StartLSN != wantEnd {
			t.Errorf("end of timeline = %+v, want timeline %d → %d at %X", *eot, wantTLI, wantNext, wantEnd)
		}
		if got := standby.WrittenLSN(); got != wantEnd {
			t.Errorf("standby WAL ends at %X after timeline %d, want the switch point %X", got, wantTLI, wantEnd)
		}
	}

	streamToEnd(1, 2, sw1)

	// Already at the switch point: no COPY, the end comes from the dial.
	rec, err := dial(sw1)
	var eot *replication.EndOfTimeline
	if !errors.As(err, &eot) || eot.NextTLI != 2 {
		if rec != nil {
			_ = rec.Close()
		}
		t.Fatalf("dial at the switch point = %v, want *EndOfTimeline with next timeline 2", err)
	}

	if err := standby.SwitchTimeline(2, sw1, 0); err != nil {
		t.Fatal(err)
	}
	streamToEnd(2, 3, sw2)

	if err := standby.SwitchTimeline(3, sw2, 0); err != nil {
		t.Fatal(err)
	}
	rec, err = dial(standby.WrittenLSN())
	if err != nil {
		t.Fatalf("dial on the current timeline: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rec.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for standby.WrittenLSN() < primary.WrittenLSN() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run on the current timeline = %v, want nil on cancel", err)
	}
	if standby.WrittenLSN() != primary.WrittenLSN() {
		t.Errorf("standby WAL ends at %X, want the primary's %X", standby.WrittenLSN(), primary.WrittenLSN())
	}
}
