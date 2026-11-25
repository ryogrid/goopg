package xlog

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestArchiverOrderAndPlaceholders pins pgarch_readyXlog's order (timeline
// history files first, then segments oldest first) and archive_command's
// %p / %f / %% expansion (M0149-0018a).
func TestArchiverOrderAndPlaceholders(t *testing.T) {
	wal := t.TempDir()
	for _, n := range []string{"000000010000000000000003", "000000010000000000000002", "00000002.history"} {
		if err := writeArchiveStatus(wal, n, ".ready"); err != nil {
			t.Fatal(err)
		}
	}
	if n, ok := readyXlog(wal); !ok || n != "00000002.history" {
		t.Fatalf("first ready = %q, %v; want the history file", n, ok)
	}
	_ = os.Remove(filepath.Join(wal, archiveStatusDir, "00000002.history.ready"))
	if n, _ := readyXlog(wal); n != "000000010000000000000002" {
		t.Fatalf("next ready = %q; want the oldest segment", n)
	}
	got := replaceArchivePlaceholders(`cp "%p" "/a/%f" 100%% %x`, "pg_wal/F", "F")
	if want := `cp "pg_wal/F" "/a/F" 100% %x`; got != want {
		t.Fatalf("placeholders = %q, want %q", got, want)
	}
}

// TestArchiverCopyLoop pins the archive cycle: a .ready segment is copied
// by archive_command (run in the data directory, %p relative to it) and
// becomes .done; a failing command leaves .ready after its retries; a
// standby with archive_mode = on marks received segments .done; and
// XLogArchiveCheckDone's removability rules.
func TestArchiverCopyLoop(t *testing.T) {
	data := t.TempDir()
	wal := filepath.Join(data, "pg_wal")
	arch := t.TempDir()
	seg := "000000010000000000000001"
	if err := os.MkdirAll(wal, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wal, seg), []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	mode, command, inRecovery := "on", `cp "%p" "`+arch+`/%f"`, false
	a := NewArchiver(ArchiverConfig{
		DataDir: data, WALDir: wal,
		Mode:       func() string { return mode },
		Command:    func() string { return command },
		InRecovery: func() bool { return inRecovery },
	})
	if a.segmentRemovable(wal, seg) {
		t.Fatal("unarchived segment removable")
	}
	if _, err := os.Stat(filepath.Join(wal, archiveStatusDir, seg+".ready")); err != nil {
		t.Fatalf("removal check did not recreate .ready: %v", err)
	}
	a.copyLoop(context.Background())
	if b, err := os.ReadFile(filepath.Join(arch, seg)); err != nil || string(b) != "wal" {
		t.Fatalf("archived copy = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(wal, archiveStatusDir, seg+".done")); err != nil {
		t.Fatalf("no .done after archiving: %v", err)
	}
	if !a.segmentRemovable(wal, seg) {
		t.Fatal("archived segment not removable")
	}

	seg2 := "000000010000000000000002"
	if err := os.WriteFile(filepath.Join(wal, seg2), []byte("wal2"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = "false"
	a.Notify(seg2)
	a.copyLoop(context.Background()) // three failures, one second apart
	if _, err := os.Stat(filepath.Join(wal, archiveStatusDir, seg2+".ready")); err != nil {
		t.Fatalf("failed segment lost its .ready: %v", err)
	}

	inRecovery = true
	a.NotifySegment(1, 3, 16<<20)
	if _, err := os.Stat(filepath.Join(wal, archiveStatusDir, "000000010000000000000003.done")); err != nil {
		t.Fatalf("standby segment not marked .done: %v", err)
	}
	if !a.segmentRemovable(wal, seg2) {
		t.Fatal("archive_mode = on in recovery must not hold segments")
	}
	mode = "off"
	if a.segmentRemovable(wal, seg2) != true || a.Enabled() {
		t.Fatal("archive_mode = off still archives")
	}
}

// TestArchiverRunStartsOnceAllowed pins the archiver "process" lifetime the
// pg_stat_activity row follows (PgArchStartupAllowed): with archive_mode =
// on it does not start during recovery, starts once a wakeup finds the
// server out of recovery, and stops at shutdown (M0149-0027).
func TestArchiverRunStartsOnceAllowed(t *testing.T) {
	data := t.TempDir()
	wal := filepath.Join(data, "pg_wal")
	if err := os.MkdirAll(wal, 0o700); err != nil {
		t.Fatal(err)
	}
	var inRecovery atomic.Bool
	inRecovery.Store(true)
	started, stopped := make(chan struct{}, 2), make(chan struct{}, 2)
	a := NewArchiver(ArchiverConfig{
		DataDir: data, WALDir: wal,
		Mode:       func() string { return "on" },
		Command:    func() string { return "true" },
		InRecovery: inRecovery.Load,
		OnStart:    func() { started <- struct{}{} },
		OnStop:     func() { stopped <- struct{}{} },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.Run(ctx) }()
	a.Wakeup()
	select {
	case <-started:
		t.Fatal("archiver started during recovery with archive_mode = on")
	case <-time.After(200 * time.Millisecond):
	}
	inRecovery.Store(false)
	a.Wakeup()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("archiver did not start after recovery ended")
	}
	a.Wakeup()
	cancel()
	<-done
	if len(started) != 0 || len(stopped) != 1 {
		t.Fatalf("starts after the first = %d, stops = %d; want 0, 1", len(started), len(stopped))
	}
}
