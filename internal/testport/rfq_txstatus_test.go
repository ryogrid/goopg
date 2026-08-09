package testport

// rfq_txstatus_test.go — the ReadyForQuery transaction-status byte.
//
// M-NIGHTLY AI-20260810-011258-006. goopg answered EVERY ReadyForQuery with
// 'I' (idle); the 'T' / 'E' codes existed in internal/protocol but no code path
// could emit them. libpq exposes this byte as PQtransactionStatus, and pgbench
// reads it after a failed command (CSTATE_ERROR in pgbench.c) to decide whether
// the failed block still needs a ROLLBACK:
//
//	tstatus = getTransactionStatus(st->con);
//	if (tstatus == TSTATUS_IN_BLOCK)   -> send "ROLLBACK"
//	else if (tstatus == TSTATUS_IDLE)  -> assume the block is already closed
//
// A permanent 'I' put pgbench on the second branch, so the failed block was
// never rolled back, and the NEXT script iteration's BEGIN (command index 4 of
// the tpcb-like / simple-update scripts) came back 25P02 "current transaction
// is aborted" — a non-retriable error, which aborts the client. That is the
// nightly's "79 aborted clients whose originating error is absent from the
// log": the originating error was a retriable one (pgbench only prints those
// under --verbose-errors), and the visible abort was this protocol-state
// mismatch one transaction later.
//
// The test drives that exact sequence at the wire level, since psql does not
// print the status byte in non-interactive mode.
//
// Upstream reference: TransactionBlockStatusCode(),
// postgres/src/backend/access/transam/xact.c.

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

// rfqConn is a minimal simple-query wire client: enough to send Query and read
// back the terminating ReadyForQuery status byte.
type rfqConn struct {
	conn net.Conn
	r    *protocol.FrameReader
	w    *protocol.FrameWriter
}

func dialRFQ(t *testing.T, addr string) *rfqConn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, protocol.ProtocolVersion3_0)
	for _, kv := range [][2]string{{"user", "postgres"}, {"database", "postgres"}} {
		body = append(body, kv[0]...)
		body = append(body, 0)
		body = append(body, kv[1]...)
		body = append(body, 0)
	}
	body = append(body, 0)
	pkt := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(pkt[:4], uint32(4+len(body)))
	copy(pkt[4:], body)
	if _, err := conn.Write(pkt); err != nil {
		t.Fatalf("write startup packet: %v", err)
	}

	c := &rfqConn{conn: conn, r: protocol.NewFrameReader(conn), w: protocol.NewFrameWriter(conn)}
	if got := c.readUntilReady(t); got != protocol.TxStatusIdle {
		t.Fatalf("handshake ReadyForQuery = %q, want 'I'", got)
	}
	return c
}

// readUntilReady drains frames up to and including ReadyForQuery and returns
// its status byte.
func (c *rfqConn) readUntilReady(t *testing.T) protocol.TransactionStatus {
	t.Helper()
	for {
		f, err := c.r.ReadFrame()
		if err != nil {
			if err == io.EOF {
				t.Fatal("connection closed before ReadyForQuery")
			}
			t.Fatalf("read frame: %v", err)
		}
		if f.Type == protocol.MsgReadyForQuery {
			if len(f.Payload) != 1 {
				t.Fatalf("ReadyForQuery payload = %d bytes, want 1", len(f.Payload))
			}
			return protocol.TransactionStatus(f.Payload[0])
		}
	}
}

// exec sends one simple Query and returns the status byte of its
// ReadyForQuery.
func (c *rfqConn) exec(t *testing.T, sql string) protocol.TransactionStatus {
	t.Helper()
	if err := c.w.WriteQuery(sql); err != nil {
		t.Fatalf("write Query %q: %v", sql, err)
	}
	if err := c.w.Flush(); err != nil {
		t.Fatalf("flush Query %q: %v", sql, err)
	}
	return c.readUntilReady(t)
}

// TestPort_ReadyForQueryTransactionStatus pins the 'I'/'T'/'E' contract of the
// ReadyForQuery status byte, including the pgbench recovery sequence that
// AI-20260810-011258-006 turned into a client abort.
func TestPort_ReadyForQueryTransactionStatus(t *testing.T) {
	c := newCluster(t, "rfq-txstatus")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownFast) }()

	conn := dialRFQ(t, c.ListenAddr())

	steps := []struct {
		sql  string
		want protocol.TransactionStatus
		why  string
	}{
		{"SELECT 1", protocol.TxStatusIdle, "autocommit statement outside any block"},
		{"BEGIN", protocol.TxStatusInTransaction, "explicit block open and valid"},
		{"CREATE TEMP TABLE rfq_t (a int)", protocol.TxStatusInTransaction, "successful statement inside the block"},
		{"INSERT INTO rfq_t VALUES (0)", protocol.TxStatusInTransaction, "successful write inside the block"},
		// A RUNTIME error (division by a column value, so the planner cannot
		// constant-fold it) — this is the path that reaches
		// dispatchSimpleQueryViaExecutor's connTxState.Fail().
		{"SELECT 1/a FROM rfq_t", protocol.TxStatusInFailedTransaction, "the erroring statement's own ReadyForQuery"},
		{"SELECT 1", protocol.TxStatusInFailedTransaction, "25P02 while the block stays aborted"},
		{"ROLLBACK", protocol.TxStatusIdle, "ROLLBACK closes the failed block"},
		{"SELECT 1", protocol.TxStatusIdle, "back to autocommit"},
		// An autocommit statement that errors does NOT open a block: PG
		// reports 'I', not 'E'.
		{"SELECT 1/0", protocol.TxStatusIdle, "autocommit error leaves no open block"},
		// COMMIT path.
		{"BEGIN", protocol.TxStatusInTransaction, "second block"},
		{"COMMIT", protocol.TxStatusIdle, "COMMIT closes the block"},
	}
	for i, st := range steps {
		got := conn.exec(t, st.sql)
		if got != st.want {
			t.Errorf("step %d %q: ReadyForQuery = %q, want %q (%s)",
				i, st.sql, string(got), string(st.want), st.why)
		}
	}

	// The pgbench loop itself: after the failed block is rolled back, the next
	// BEGIN must succeed. Before the fix this returned 25P02 because the client
	// had been told (via 'I') that there was no block left to roll back.
	if got := conn.exec(t, "BEGIN"); got != protocol.TxStatusInTransaction {
		t.Fatalf("BEGIN after recovered block = %q, want 'T'", string(got))
	}
	if got := conn.exec(t, "END"); got != protocol.TxStatusIdle {
		t.Fatalf("END = %q, want 'I'", string(got))
	}

	// A PLAN-time error (unknown column) must report 'E' too, so the client
	// still knows a ROLLBACK is owed — this is the branch pgbench takes.
	//
	// NOTE: only the status byte is asserted here. goopg's plan-time error
	// paths in dispatchSimpleQueryViaExecutor return writeQueryError directly
	// and never reach connTxState.Fail(), so the block is not actually marked
	// aborted and a FOLLOWING statement is accepted instead of raising 25P02 —
	// a separate, pre-existing divergence from upstream (every error inside a
	// transaction block aborts it: AbortCurrentTransaction, xact.c). Recorded
	// in .ralph/deferral_ledger.md; do not widen this test until it is fixed.
	if got := conn.exec(t, "BEGIN"); got != protocol.TxStatusInTransaction {
		t.Fatalf("BEGIN = %q, want 'T'", string(got))
	}
	if got := conn.exec(t, "SELECT undefined_column_xyz"); got != protocol.TxStatusInFailedTransaction {
		t.Fatalf("plan-time failure in block = %q, want 'E'", string(got))
	}
	if got := conn.exec(t, "ROLLBACK"); got != protocol.TxStatusIdle {
		t.Fatalf("ROLLBACK = %q, want 'I'", string(got))
	}
}
