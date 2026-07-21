package server

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// The client-EOF watcher (eof_watch.go) exists because a client killed
// mid-query sends no CancelRequest, so nothing cancels a CPU-bound
// statement (the csq-S6 spin incident: 227 % CPU held by orphaned cross
// joins). These tests pin the watcher's contract at the socket level;
// the executor side of the unwind is covered by the ctx.Err() checks'
// own tests.

// tcpPair returns a connected (server, client) TCP pair on loopback.
func tcpPair(t *testing.T) (srv net.Conn, cli net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	type accepted struct {
		c   net.Conn
		err error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, aerr := ln.Accept()
		ch <- accepted{c, aerr}
	}()
	cli, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	a := <-ch
	if a.err != nil {
		t.Fatalf("accept: %v", a.err)
	}
	t.Cleanup(func() {
		_ = a.c.Close()
		_ = cli.Close()
	})
	return a.c, cli
}

// waitForEOFWatch polls cond every 10 ms up to timeout.
func waitForEOFWatch(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestEOFWatchCancelsOnClientClose(t *testing.T) {
	srv, cli := tcpPair(t)
	var cancelled atomic.Bool
	w := startClientEOFWatch(srv, func() { cancelled.Store(true) }, nil)
	if w == nil {
		t.Fatal("watcher unexpectedly nil for a TCP conn")
	}
	defer w.Stop()

	_ = cli.Close() // client dies mid-query; no CancelRequest
	if !waitForEOFWatch(3*time.Second, cancelled.Load) {
		t.Fatal("watcher did not cancel within 3s of client close")
	}
}

func TestEOFWatchIgnoresLiveIdleClient(t *testing.T) {
	srv, _ := tcpPair(t)
	var cancelled atomic.Bool
	w := startClientEOFWatch(srv, func() { cancelled.Store(true) }, nil)
	if w == nil {
		t.Fatal("watcher unexpectedly nil for a TCP conn")
	}
	// Let several poll intervals elapse with the client alive but silent.
	time.Sleep(3 * eofWatchInterval)
	if cancelled.Load() {
		t.Fatal("watcher cancelled a live idle client")
	}
	stopped := make(chan struct{})
	go func() { w.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return promptly")
	}
	if cancelled.Load() {
		t.Fatal("cancel fired during Stop of a live connection")
	}
}

// Pending client bytes (a pipelined frame, CopyData) must neither trigger a
// cancel nor be consumed — MSG_PEEK non-interference is the property that
// makes arming the watcher safe even around COPY FROM STDIN.
func TestEOFWatchToleratesPendingDataWithoutConsuming(t *testing.T) {
	srv, cli := tcpPair(t)
	var cancelled atomic.Bool
	w := startClientEOFWatch(srv, func() { cancelled.Store(true) }, nil)
	if w == nil {
		t.Fatal("watcher unexpectedly nil for a TCP conn")
	}
	payload := []byte("pipelined-bytes")
	if _, err := cli.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
	// Several polls with data sitting in the kernel buffer.
	time.Sleep(3 * eofWatchInterval)
	if cancelled.Load() {
		t.Fatal("watcher treated pending data as connection death")
	}
	w.Stop()
	// The bytes must still be fully readable: MSG_PEEK consumed nothing.
	_ = srv.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(payload))
	if _, err := ioReadFull(srv, got); err != nil {
		t.Fatalf("reading pipelined bytes after Stop: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("pipelined bytes corrupted: got %q want %q", got, payload)
	}
}

// ioReadFull is a tiny local io.ReadFull to keep the imports flat.
func ioReadFull(c net.Conn, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := c.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// A connection type without syscall access (net.Pipe) yields a nil watcher,
// and Stop on nil must be a no-op — the dispatch sites call Stop
// unconditionally.
func TestEOFWatchNilOnNonSyscallConn(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	w := startClientEOFWatch(a, func() {}, nil)
	if w != nil {
		t.Fatal("expected nil watcher for net.Pipe")
	}
	w.Stop() // must not panic
}
