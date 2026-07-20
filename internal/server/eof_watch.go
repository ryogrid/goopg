package server

import (
	"context"
	"log/slog"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// clientEOFWatch detects a client that vanished mid-query and cancels the
// query's context so the executor's ctx.Err() checks can unwind it.
//
// Why it exists (csq-S6 deferral, 2026-07-21): a client killed mid-query
// sends no CancelRequest, and during a long CPU-bound statement the backend
// neither reads nor writes the socket — so nothing notices the dead peer.
// A cancelled 280 s cross join left its backend at >100 % CPU for what
// would have been hours (measured live: two such orphans held 227 % CPU and
// 11.7 GB RSS, starving every later query into timeout). The TCP keepalive
// set in serveConn only surfaces on an ACTIVE read/write, which is exactly
// what a compute-bound query never does. This is the root fix promised by
// analysis/tpch-runner-measurement-report-2026-05-06.md §2.4 and design doc
// 0058-0001 §6 (Gap 5), implemented with a kernel-level peek instead of the
// read-deadline scheme sketched there.
//
// How: every eofWatchInterval the watcher issues
// recvfrom(fd, 1, MSG_PEEK|MSG_DONTWAIT) on the client socket.
//   - EAGAIN            → no bytes, peer alive: keep polling.
//   - n > 0             → bytes pending (pipelined frames, CopyData): peer
//     alive, keep polling. MSG_PEEK never consumes, so the connection's
//     FrameReader — including a COPY FROM STDIN drain running concurrently
//     in the backend goroutine — is completely unaffected. That
//     non-interference is the reason for MSG_PEEK rather than a
//     SetReadDeadline+Peek loop on the shared bufio.Reader, which would
//     race with in-query reads and could desync a frame mid-io.ReadFull.
//   - n == 0            → orderly FIN: peer closed. Cancel the query.
//     (A client that half-closes with shutdown(SHUT_WR) while still
//     expecting results would be misjudged dead; libpq/psql never do that,
//     and PostgreSQL likewise treats client EOF as connection death.)
//   - other errno / fd gone → connection reset or closed: cancel.
//
// The watcher touches only the raw fd, never the FrameReader and never the
// connection deadlines, so it cannot interfere with protocol reads. It is
// armed per query by runPostStartupLoop and stopped as soon as the handler
// returns; replication-mode connections never arm it (the walsender manages
// its own socket lifecycle, replication.go).
//
// This project targets x86_64 Linux only (see .ralph/AGENT.md), so the raw
// recvfrom is not a portability concern. Connections whose net.Conn does not
// expose a syscall.RawConn (e.g. net.Pipe in tests) simply do not get a
// watcher.
type clientEOFWatch struct {
	stop chan struct{}
	done chan struct{}
}

const eofWatchInterval = 500 * time.Millisecond

// startClientEOFWatch arms a watcher on conn. It returns nil (a valid,
// Stop-safe result) when the connection cannot be watched. cancel must be
// safe to call concurrently with — and after — normal query completion; the
// per-query context.CancelFunc satisfies both.
func startClientEOFWatch(conn net.Conn, cancel context.CancelFunc, logger *slog.Logger) *clientEOFWatch {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return nil
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return nil
	}
	w := &clientEOFWatch{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(eofWatchInterval)
		defer ticker.Stop()
		var peek [1]byte
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
			}
			dead := false
			ctrlErr := rc.Control(func(fd uintptr) {
				for {
					n, _, rerr := unix.Recvfrom(int(fd), peek[:], unix.MSG_PEEK|unix.MSG_DONTWAIT)
					if rerr == unix.EINTR {
						continue
					}
					if rerr == unix.EAGAIN || rerr == unix.EWOULDBLOCK {
						return // no pending bytes; peer alive
					}
					if rerr != nil || n == 0 {
						dead = true // reset/error, or n==0: orderly EOF
					}
					return // n > 0: pending bytes; peer alive
				}
			})
			if ctrlErr != nil {
				// The fd was closed underneath us (connection teardown
				// racing the watcher): treat as gone. Cancelling here is
				// a no-op if the query already finished.
				dead = true
			}
			if dead {
				if logger != nil {
					logger.Info("client connection lost mid-query; cancelling statement")
				}
				cancel()
				return
			}
		}
	}()
	return w
}

// Stop tears the watcher down and waits for its goroutine to exit. It is
// safe on a nil receiver (unwatchable connection) and must be called from
// the connection goroutine after the query handler returns, so no watcher
// outlives its query.
func (w *clientEOFWatch) Stop() {
	if w == nil {
		return
	}
	close(w.stop)
	<-w.done
}
