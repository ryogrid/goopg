package server

// TestRestartAfterRetention is an end-to-end regression test for
// M0045-0001 through M0045-0003 (crash recovery from a non-zero starting
// WAL segment). Prior to M0045-0001, starting the server against a data
// directory whose WAL segment 0 had been removed by retention returned:
//
//	goopg: wal: first segment is 000000000000000000000002, expected 000000000000000000000000
//
// The test reproduces the exact failure scenario deterministically:
// small WAL segments + explicit checkpoints + retention → kill → restart.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/wal"
)

// retTestSegSize is the WAL segment size used for retention tests: 1 MiB
// so that inserting ~5 000 rows fills enough WAL for retention to fire.
const retTestSegSize int64 = 1 << 20 // 1 MiB

// retTestServer bundles all handles for one server instance in the test.
type retTestServer struct {
	rt     *initdb.Runtime
	srv    *Server
	addr   string
	cancel context.CancelFunc
	done   chan error
}

// startRetTestServer opens a Runtime from dir with small WAL segments,
// wires a SlotAwareRetainer so checkpoints prune old segments, then starts
// a Server on an ephemeral port.
func startRetTestServer(t *testing.T, dir string) *retTestServer {
	t.Helper()
	rt, err := initdb.Open(initdb.OpenOptions{
		DataDir:        dir,
		PoolSlots:      64,
		WALSegmentSize: retTestSegSize,
	})
	if err != nil {
		t.Fatalf("initdb.Open: %v", err)
	}

	// Wire slot-aware retention so each CheckpointNow prunes old segments.
	rt.Checkpointer.SetRetainer(&wal.SlotAwareRetainer{
		Writer: rt.WAL,
		Slots:  rt.Slots,
	})

	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Catalog:          rt.Catalog,
		Pool:             rt.Pool,
		TxnMgr:           rt.TxnMgr,
		Checkpointer:     rt.Checkpointer,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()

	return &retTestServer{
		rt:     rt,
		srv:    srv,
		addr:   srv.Addr().String(),
		cancel: cancel,
		done:   done,
	}
}

// close performs a graceful shutdown (used for the second server instance).
func (s *retTestServer) close(t *testing.T) {
	t.Helper()
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Error("server did not stop within 5s")
	}
	if err := s.rt.Close(); err != nil {
		t.Logf("runtime close: %v", err)
	}
}

// hardKill simulates a SIGKILL that arrives after two clean checkpoints.
// The server's accept loop is stopped via context cancellation; the runtime
// is then closed to release all file descriptors and stop background
// goroutines. No additional catalog flush or post-shutdown checkpoint is
// performed — the binary is "dead" at this point.
//
// Data files are already consistent from the two prior CheckpointNow calls
// (each of which flushed all dirty pages to disk). The restart will
// exercise M0045-0001/0002/0003 by replaying an empty post-checkpoint
// window while finding segment 0 missing.
func (s *retTestServer) hardKill(t *testing.T) {
	t.Helper()
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		t.Error("server did not stop within 3s")
	}
	if err := s.rt.Close(); err != nil {
		t.Logf("runtime close during hardKill: %v", err)
	}
}

// retRunQuery sends a simple-query SQL statement and fails the test if
// the server returns an ErrorResponse.
func retRunQuery(t *testing.T, addr, sql string) {
	t.Helper()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	writeQuery(t, conn, sql)
	for _, f := range readUntilReady(t, conn) {
		if f.Type == protocol.MsgErrorResponse {
			t.Fatalf("query %q: %s", sql,
				parseErrorFields(t, f.Payload)[protocol.FieldMessage])
		}
	}
}

// retSeedRows inserts `count` rows via COPY FROM STDIN starting at id=startID.
// Each row contains a 250-byte text value so that 5 000 rows generate more
// than 1 MiB of WAL (heap insert records + full-page images for new pages),
// driving the WAL writer past segment 0 in a single batch.
func retSeedRows(t *testing.T, addr string, startID, count int) {
	t.Helper()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	var buf strings.Builder
	pad := strings.Repeat("x", 250)
	for i := 0; i < count; i++ {
		fmt.Fprintf(&buf, "%d\t%s\n", startID+i, pad)
	}

	writeQuery(t, conn, "COPY retention_items FROM STDIN")
	// Split data into 256 KiB frames to stay within MaxRegularMessageLength.
	data := []byte(buf.String())
	const chunkSize = 256 * 1024
	for len(data) > 0 {
		n := len(data)
		if n > chunkSize {
			n = chunkSize
		}
		writeFrontendFrame(t, conn, protocol.MsgCopyData, data[:n])
		data = data[n:]
	}
	writeFrontendFrame(t, conn, protocol.MsgCopyDone, nil)
	for _, f := range readUntilReady(t, conn) {
		if f.Type == protocol.MsgErrorResponse {
			t.Fatalf("COPY error: %s",
				parseErrorFields(t, f.Payload)[protocol.FieldMessage])
		}
	}
}

// retCountRows returns the number of rows in retention_items by issuing a
// full table scan and counting DataRow frames.
func retCountRows(t *testing.T, addr string) int {
	t.Helper()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	writeQuery(t, conn, "SELECT id FROM retention_items")
	var count int
	for _, f := range readUntilReady(t, conn) {
		if f.Type == protocol.MsgErrorResponse {
			t.Fatalf("SELECT error: %s",
				parseErrorFields(t, f.Payload)[protocol.FieldMessage])
		}
		if f.Type == protocol.MsgDataRow {
			count++
		}
	}
	return count
}

// requireSeg0Missing fails the test when WAL segment 0 still exists. Its
// presence means retention has not fired and the M0045 code path won't be
// exercised.
func requireSeg0Missing(t *testing.T, dir string) {
	t.Helper()
	seg0 := filepath.Join(dir, "pg_wal", "000000000000000000000000")
	if _, err := os.Stat(seg0); err == nil {
		t.Fatal("WAL segment 0 still present; retention has not fired — " +
			"M0045 code path will not be exercised")
	}
}

// TestRestartAfterRetention is the M0045 regression test.
//
// Phase 1 — populate and force retention:
//   - Start server with 1 MiB WAL segments and a slot-aware retainer.
//   - Create table, seed 5 000 rows (generates > 1 MiB WAL via FPIs +
//     insert records), force checkpoint → retention removes segment 0.
//   - Seed another 5 000 rows, force second checkpoint → second retention pass.
//   - Persist catalog (table schema survives restart).
//
// Phase 2 — simulate SIGKILL:
//   - Cancel the server context and close the runtime.
//   - Assert segment 0 is gone (sanity).
//
// Phase 3 — restart:
//   - Open a fresh Runtime against the same data dir with the same
//     segment size. Pre-M0045-0001 this fails with
//     "first segment is …, expected 000000000000000000000000".
//   - Post-fix: the server starts successfully.
//
// Phase 4 — verify data integrity:
//   - All 10 000 rows survive the restart (heap data was flushed by the
//     two prior checkpoints; WAL replay finds nothing to redo).
func TestRestartAfterRetention(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// ── Phase 1: populate and drive retention ──────────────────────────

	srv1 := startRetTestServer(t, dir)

	retRunQuery(t, srv1.addr,
		"CREATE TABLE retention_items (id int4, val text)")

	// Batch 1: 5 000 rows → ~2–3 MiB WAL (FPIs on every new heap page).
	retSeedRows(t, srv1.addr, 0, 5_000)
	// Checkpoint 1: flushes dirty pages + WAL, runs retention.
	// checkpointLSN > 1 MiB → keepSeg ≥ 1 → segment 0 removed.
	if err := srv1.rt.Checkpointer.CheckpointNow(); err != nil {
		t.Fatalf("checkpoint 1: %v", err)
	}

	// Batch 2: 5 000 more rows to confirm retention still works after
	// segment 0 is gone.
	retSeedRows(t, srv1.addr, 5_000, 5_000)
	// Checkpoint 2: second retention pass; all dirty pages now on disk.
	if err := srv1.rt.Checkpointer.CheckpointNow(); err != nil {
		t.Fatalf("checkpoint 2: %v", err)
	}

	// Persist the in-memory catalog so retention_items is visible on restart.
	if err := srv1.rt.SaveCatalog(); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}

	// ── Phase 2: simulate SIGKILL ──────────────────────────────────────

	srv1.hardKill(t)
	requireSeg0Missing(t, dir) // confirm precondition for M0045

	// ── Phase 3: restart ───────────────────────────────────────────────

	// Pre-M0045-0001 this call fatalf'd with:
	//   initdb.Open: goopg: wal: first segment is …, expected 000000000000000000000000
	srv2 := startRetTestServer(t, dir)
	defer srv2.close(t)

	// ── Phase 4: verify data integrity ────────────────────────────────

	got := retCountRows(t, srv2.addr)
	if got != 10_000 {
		t.Fatalf("after restart: got %d rows, want 10000", got)
	}
}
