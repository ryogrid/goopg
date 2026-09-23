package control

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A data directory whose `<dir>/.goopg.ctl.sock` fits sun_path keeps the
// socket inside itself — the layout every existing cluster already has.
func TestSocketPathForShortDirStaysInDataDir(t *testing.T) {
	dir := "/tmp/goopg-short"
	if got, want := SocketPathFor(dir), filepath.Join(dir, SocketName); got != want {
		t.Fatalf("SocketPathFor(%q) = %q, want %q", dir, got, want)
	}
}

// The nightly witness: a 136-char socket path under a worktree data directory
// failed `goopg start` with "listen unix …: invalid argument". The fallback
// must fit sun_path, be deterministic per data directory, and differ between
// data directories.
func TestSocketPathForLongDirFallsBackDeterministically(t *testing.T) {
	long := "/home/ryo/work/goopg/goopg/tmp/nightly-src-20260923-001346/tmp/pgoutput-interop-pg2g-batchdml/pgoutput_pg2g_batchdml-sub"
	if len(filepath.Join(long, SocketName)) <= maxSocketPathLen {
		t.Fatal("fixture no longer exceeds the limit; lengthen it")
	}
	got := SocketPathFor(long)
	if len(got) > maxSocketPathLen {
		t.Fatalf("fallback %q is %d bytes, over the %d-byte limit", got, len(got), maxSocketPathLen)
	}
	if !strings.HasSuffix(got, ".ctl.sock") || strings.HasPrefix(got, long) {
		t.Fatalf("fallback %q should be a short temp-dir socket, not under the data directory", got)
	}
	if again := SocketPathFor(long); again != got {
		t.Fatalf("fallback not deterministic: %q then %q", got, again)
	}
	if other := SocketPathFor(long + "2"); other == got {
		t.Fatalf("two data directories share fallback %q", got)
	}
}

// End to end: a listener bound at the fallback for an over-long data
// directory answers PING, i.e. the chosen path is actually bindable.
func TestSocketPathForLongDirIsBindable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	path := SocketPathFor(dir)
	ln, err := NewListener(path)
	if err != nil {
		t.Fatalf("NewListener(%q): %v", path, err)
	}
	served := make(chan error, 1)
	go func() { served <- ln.Serve() }()
	reply, err := Send(path, "PING", time.Second)
	if err != nil || reply != "OK" {
		t.Fatalf("PING over fallback socket: reply=%q err=%v", reply, err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}
