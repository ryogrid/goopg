package server

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/storage"
	_ "github.com/lib/pq"
)

// TestCopyFromLogCanonicalEncoding verifies that when LogCanonical is set in
// the server config, dispatchCopyViaExecutor wires ectx.LogCanonical so COPY
// FROM writes rows in PG physical format (EncodeRowPG) rather than goopg
// format (EncodeRow).
//
// Before the fix, ectx.LogCanonical was never set in the COPY path, so COPY
// used goopg-format encoding while SELECT/UPDATE used DecodePhysicalPGRow —
// causing a mixed-format decode mismatch (root cause of the c=50 SU TPS gate
// miss in M0107 milestone-close).
func TestCopyFromLogCanonicalEncoding(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	// Close order: server first (via deferred cancel+wait registered later),
	// then pool (flushes dirty pages), then manager (closes file handles).
	// Defers are LIFO so register mgr first (runs last), pool second (runs before mgr).
	defer mgr.Close()  // runs last
	defer pool.Close() // runs before mgr

	cat := catalog.NewInMemory()
	// Pre-create the table so COPY can find it.
	if _, err := cat.CreateTable(parser.ObjectName{Name: "accts"}, []catalog.Column{
		{Name: "aid", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "abalance", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	mvccMgr := mvcc.NewManager()

	// Count LogCanonical invocations; > 0 after COPY means ectx.LogCanonical
	// was wired and COPY used EncodeRowPG.
	var canonCalls atomic.Int64
	mockLogCanonical := catalog.LogCanonicalFunc(func(_ []byte) (uint64, error) {
		canonCalls.Add(1)
		return uint64(canonCalls.Load()), nil
	})

	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Catalog:          cat,
		Pool:             pool,
		TxnMgr:           mvccMgr,
		LogCanonical:     mockLogCanonical,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Server.Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Server.Run did not stop")
		}
	}()

	addr := srv.Addr().String()

	// — COPY phase: use raw wire-protocol helpers (same pattern as
	//   TestCopyFromExecutorEndToEnd) to send COPY FROM STDIN rows.
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "COPY accts FROM STDIN")
	writeFrontendFrame(t, conn, protocol.MsgCopyData, []byte("1\t0\n2\t0\n3\t0\n4\t0\n5\t0\n"))
	writeFrontendFrame(t, conn, protocol.MsgCopyDone, nil)
	frames := readUntilReady(t, conn)

	// Expect: CopyInResponse, CommandComplete, ReadyForQuery.
	var gotTag string
	for _, f := range frames {
		if f.Type == protocol.MsgCommandComplete {
			gotTag = strings.TrimSuffix(string(f.Payload), "\x00")
		}
	}
	if gotTag != "COPY 5" {
		t.Fatalf("COPY command tag = %q, want \"COPY 5\"", gotTag)
	}

	// LogCanonical must have been called at least once — confirming that
	// the COPY path now writes PG-format rows via ectx.LogCanonical.
	if n := canonCalls.Load(); n == 0 {
		t.Fatal("LogCanonical was never called during COPY: ectx.LogCanonical was not wired")
	}

	// — Query phase: open a second SQL connection to SELECT and UPDATE the
	//   COPY'd rows, verifying they are decodable (no format-mismatch OOM).
	colonIdx := strings.LastIndex(addr, ":")
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sqlCtx := context.Background()

	// SELECT all rows back.
	rows, err := db.QueryContext(sqlCtx, "SELECT aid, abalance FROM accts ORDER BY aid")
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var count int
	for rows.Next() {
		var aid, abalance int
		if err := rows.Scan(&aid, &abalance); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if aid != count+1 {
			t.Errorf("row %d: aid=%d want %d", count, aid, count+1)
		}
		if abalance != 0 {
			t.Errorf("row %d: abalance=%d want 0", count, abalance)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 5 {
		t.Fatalf("got %d rows after COPY, want 5", count)
	}

	// UPDATE a row and re-read — simulates the pgbench SU workload pattern.
	if _, err := db.ExecContext(sqlCtx,
		"UPDATE accts SET abalance = abalance + 100 WHERE aid = 3"); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	var got int
	if err := db.QueryRowContext(sqlCtx,
		"SELECT abalance FROM accts WHERE aid = 3").Scan(&got); err != nil {
		t.Fatalf("SELECT after UPDATE: %v", err)
	}
	if got != 100 {
		t.Errorf("abalance after UPDATE = %d, want 100", got)
	}
}
