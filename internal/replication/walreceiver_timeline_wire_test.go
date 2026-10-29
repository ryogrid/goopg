package replication_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/postmaster"
	"github.com/goopg/goopg/internal/replication"
)

const timelineTestSysID = "7300000000000000001"

// startTimelinePrimary runs a walsender whose WAL writer is on timeline tli
// and whose pg_wal holds the given history files. postmaster.Config.Timeline
// is left unset: IDENTIFY_SYSTEM must report the writer's timeline.
func startTimelinePrimary(t *testing.T, tli uint32, history map[uint32]string) (string, func()) {
	t.Helper()
	dataDir := t.TempDir()
	walDir := filepath.Join(dataDir, "pg_wal")
	w, err := xlog.NewWriter(xlog.Config{WALDir: walDir, SegmentSize: 4096, TimelineID: tli})
	if err != nil {
		t.Fatal(err)
	}
	for h, content := range history {
		if err := os.WriteFile(filepath.Join(walDir, xlog.TimelineHistoryFileName(h)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv := postmaster.New(postmaster.Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		WAL:              w,
		WALDirPath:       walDir,
		WALSegmentSize:   4096,
		SystemID:         timelineTestSysID,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	return srv.Addr().String(), func() {
		cancel()
		<-done
		_ = w.Close()
	}
}

func newStandbyWAL(t *testing.T) (*xlog.Writer, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := xlog.NewWriter(xlog.Config{WALDir: dir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, dir
}

// TestWalReceiverFetchesMissingTimelineHistory: before streaming, the
// walreceiver writes the history file of every timeline between its start
// timeline and the primary's that pg_wal lacks, byte-for-byte as the
// primary sent it, and leaves an existing one alone
// (WalRcvFetchTimeLineHistoryFiles). A promotion's findNewestTimeLine then
// sees the primary's timelines.
func TestWalReceiverFetchesMissingTimelineHistory(t *testing.T) {
	h2 := "1\t0/3000098\tno recovery target specified\n"
	h3 := "1\t0/3000098\tno recovery target specified\n2\t0/5000060\tno recovery target specified\n"
	addr, stop := startTimelinePrimary(t, 3, map[uint32]string{2: h2, 3: h3})
	defer stop()

	standbyWAL, walDir := newStandbyWAL(t)
	const kept = "1\t0/3000098\tlocal copy\n"
	if err := os.WriteFile(filepath.Join(walDir, xlog.TimelineHistoryFileName(2)), []byte(kept), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := replication.DialWalReceiver(context.Background(), replication.WalReceiverConfig{
		PrimaryAddr: addr,
		User:        "rep",
		WAL:         standbyWAL,
		DialTimeout: 3 * time.Second,
		WALDir:      walDir,
	})
	if err != nil {
		t.Fatalf("DialWalReceiver: %v", err)
	}
	_ = rec.Close()

	for tli, want := range map[uint32]string{2: kept, 3: h3} {
		got, err := os.ReadFile(filepath.Join(walDir, xlog.TimelineHistoryFileName(tli)))
		if err != nil {
			t.Fatalf("history of timeline %d: %v", tli, err)
		}
		if string(got) != want {
			t.Errorf("history of timeline %d = %q, want %q", tli, got, want)
		}
	}
	if got := xlog.FindNewestTimeline(walDir, 1); got != 3 {
		t.Errorf("FindNewestTimeline after the fetch = %d, want 3 (a promotion takes 4)", got)
	}
}

// TestWalReceiverIdentifySystemChecks: WalReceiverMain refuses to stream
// from a primary with another system identifier, or whose timeline is
// behind the one streaming starts on.
func TestWalReceiverIdentifySystemChecks(t *testing.T) {
	addr, stop := startTimelinePrimary(t, 1, nil)
	defer stop()

	cases := []struct {
		name     string
		sysID    uint64
		startTLI uint32
		wantErr  string
	}{
		{"matching", 7300000000000000001, 1, ""},
		{"other system", 7300000000000000002, 1,
			"database system identifier differs between the primary and standby: The primary's identifier is 7300000000000000001, the standby's identifier is 7300000000000000002."},
		{"primary behind", 7300000000000000001, 2,
			"highest timeline 1 of the primary is behind recovery timeline 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			standbyWAL, _ := newStandbyWAL(t)
			rec, err := replication.DialWalReceiver(context.Background(), replication.WalReceiverConfig{
				PrimaryAddr: addr,
				User:        "rep",
				WAL:         standbyWAL,
				DialTimeout: 3 * time.Second,
				SystemID:    tc.sysID,
				StartTLI:    tc.startTLI,
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("DialWalReceiver: %v", err)
				}
				_ = rec.Close()
				return
			}
			if err == nil {
				_ = rec.Close()
				t.Fatalf("DialWalReceiver succeeded, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
