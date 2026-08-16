package postmaster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// TestLogicalReceiverReconnect pins the M0103-0003 contract:
// when the publisher closes the connection mid-stream, the
// receiver's Run loop redials with bounded backoff and resumes
// streaming at applyLSN (== publisher slot's confirmed_flush_lsn).
// The fake publisher scripts two back-to-back sessions; each
// emits one pgoutput B/I/C transaction with a higher commit LSN
// then closes its socket. ApplyLSN must advance through both
// commits without the loop returning.
func TestLogicalReceiverReconnect(t *testing.T) {
	t.Parallel()

	subDir := t.TempDir()
	subMgr := storage.NewManager(storage.ManagerConfig{DataDir: subDir})
	defer subMgr.Close()
	subPool, err := storage.NewPool(subMgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer subPool.Close()
	subCat := catalog.NewInMemory()
	if _, err := subCat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	subTxnMgr := transam.NewManager()
	apply := executor.NewApplyWorker(subCat, subPool, subTxnMgr)
	defer apply.SafeRollback()

	// Publisher-side catalog mirror used only to drive
	// PgOutput's relation-cache so the wire bytes match what a
	// real upstream would emit.
	pubCat := catalog.NewInMemory()
	pubTbl, _ := pubCat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	})
	snap := xlog.BuildCatalogSnapshot(pubCat, nil)
	pubRel := pubCat.RelFileNode(pubTbl)

	sessionLSNs := []uint64{0x1000, 0x2000, 0x3000}
	var sessionsAccepted atomic.Int32

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			idx := sessionsAccepted.Add(1) - 1
			go func(c net.Conn, idx int32) {
				defer c.Close()
				if int(idx) >= len(sessionLSNs) {
					return
				}
				if err := runFakePublisherSession(c, snap, pubRel, sessionLSNs[idx], idx); err != nil {
					t.Logf("fake publisher session %d: %v", idx, err)
				}
			}(c, idx)
		}
	}()

	rec := NewLogicalReceiver(LogicalReceiverConfig{
		PrimaryAddr:    ln.Addr().String(),
		User:           "test",
		SlotName:       "s1",
		Apply:          apply,
		ProtoVersion:   1,
		StatusInterval: time.Hour, // suppress periodic status frames in test
		DialTimeout:    2 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- rec.Run(ctx) }()

	// Wait for applyLSN to reach the second session's commit
	// (0x2000) — that proves a reconnect happened and the
	// second session's commit was actually applied.
	if !waitFor(t, 5*time.Second, func() bool {
		return rec.ApplyLSN() >= 0x2000
	}) {
		t.Fatalf("ApplyLSN=%x after 5s want >=0x2000 (sessions accepted: %d)",
			rec.ApplyLSN(), sessionsAccepted.Load())
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned err: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit within 3s of ctx cancel")
	}
}

// TestLogicalReceiverReconnectRespectsCtxDuringBackoff pins that
// ctx cancellation during the sleep-and-retry window short-
// circuits the loop rather than waiting out the backoff. Drives
// this by pointing the receiver at a dead address so the dial
// fails repeatedly; before the first backoff expires we cancel
// the ctx and assert Run returns context.Canceled.
func TestLogicalReceiverReconnectRespectsCtxDuringBackoff(t *testing.T) {
	t.Parallel()

	subDir := t.TempDir()
	subMgr := storage.NewManager(storage.ManagerConfig{DataDir: subDir})
	defer subMgr.Close()
	subPool, err := storage.NewPool(subMgr, storage.PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer subPool.Close()
	subCat := catalog.NewInMemory()
	apply := executor.NewApplyWorker(subCat, subPool, transam.NewManager())
	defer apply.SafeRollback()

	// Listen briefly to grab a free port, then close. Subsequent
	// dials to this address will fail with ECONNREFUSED.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	rec := NewLogicalReceiver(LogicalReceiverConfig{
		PrimaryAddr:    addr,
		SlotName:       "s1",
		Apply:          apply,
		StatusInterval: time.Hour,
		DialTimeout:    200 * time.Millisecond,
		InitialBackoff: 2 * time.Second, // long enough that ctx cancel beats it
		MaxBackoff:     2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- rec.Run(ctx) }()

	// Give Run a moment to fail its first dial and enter the
	// backoff sleep, then cancel.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run err=%v want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not honour ctx cancel within 2s")
	}
}

// TestIsPermanentClassifier pins the rules used by Run to decide
// retry vs. abort.
func TestIsPermanentClassifier(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"closed", errLogicalReceiverClosed, true},
		{"ctx-canceled", context.Canceled, true},
		{"ctx-deadline", context.DeadlineExceeded, true},
		{"start-replication-rejected",
			fmt.Errorf("logicalreceiver: START_REPLICATION rejected: slot does not exist"), true},
		{"startup-rejected",
			fmt.Errorf("logicalreceiver: server rejected startup: invalid user"), true},
		{"pgoutput-decode",
			fmt.Errorf("logicalreceiver: pgoutput decode: bad message"), true},
		{"apply-pgoutput",
			fmt.Errorf("logicalreceiver: apply pgoutput kind=\"I\": conflict"), true},
		{"random-net-error",
			fmt.Errorf("read tcp: connection reset by peer"), false},
		{"dial-failure",
			fmt.Errorf("logicalreceiver: dial 127.0.0.1: connect: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermanent(tc.err); got != tc.want {
				t.Errorf("isPermanent(%v)=%v want %v", tc.err, got, tc.want)
			}
		})
	}
}

// runFakePublisherSession scripts one publisher-side session: read
// startup → reply AuthenticationOk + ReadyForQuery → read query
// → reply CopyBothResponse → send one pgoutput B/I/C transaction
// at commitLSN → close. Used by TestLogicalReceiverReconnect.
func runFakePublisherSession(
	c net.Conn,
	snap *xlog.CatalogSnapshot,
	rel storage.RelFileNode,
	commitLSN uint64,
	sessionIdx int32,
) error {
	fr := libpq.NewFrameReader(c)
	fw := libpq.NewFrameWriter(c)

	if _, _, err := fr.ReadStartupPacket(); err != nil {
		return fmt.Errorf("read startup: %w", err)
	}
	if err := fw.WriteAuthenticationOk(); err != nil {
		return fmt.Errorf("auth-ok: %w", err)
	}
	if err := fw.WriteReadyForQuery(libpq.TxStatusIdle); err != nil {
		return fmt.Errorf("ready-for-query: %w", err)
	}
	if err := fw.Flush(); err != nil {
		return fmt.Errorf("flush rfq: %w", err)
	}

	f, err := fr.ReadFrame()
	if err != nil {
		return fmt.Errorf("read query: %w", err)
	}
	if f.Type != libpq.MsgQuery {
		return fmt.Errorf("expected Query (%c), got %c", libpq.MsgQuery, f.Type)
	}

	if err := fw.WriteCopyBothResponse(0, nil); err != nil {
		return fmt.Errorf("copy-both: %w", err)
	}
	if err := fw.Flush(); err != nil {
		return fmt.Errorf("flush copy-both: %w", err)
	}

	xid := storage.TransactionID(uint32(100 + sessionIdx))
	body := encodeBodyV0([]any{int(sessionIdx + 1), fmt.Sprintf("row-%d", sessionIdx)}, []string{"int4", "text"})
	tup, err := storage.NewHeapTuple(xid, 0, body).MarshalBinary()
	if err != nil {
		return fmt.Errorf("heap-tuple: %w", err)
	}

	emit := func(fn func(po *xlog.PgOutput) error) error {
		var buf bytes.Buffer
		po := xlog.NewPgOutput(snap, &buf)
		if err := fn(po); err != nil {
			return err
		}
		payload := libpq.EncodeWALData(commitLSN, commitLSN, time.Now().UTC(), buf.Bytes())
		if err := fw.WriteCopyData(payload); err != nil {
			return err
		}
		return fw.Flush()
	}

	if err := emit(func(po *xlog.PgOutput) error { return po.Begin(xid, commitLSN) }); err != nil {
		return fmt.Errorf("emit B: %w", err)
	}
	if err := emit(func(po *xlog.PgOutput) error {
		return po.Change(xlog.Change{Kind: xlog.ChangeInsert, Rel: rel, NewTuple: tup})
	}); err != nil {
		return fmt.Errorf("emit I: %w", err)
	}
	if err := emit(func(po *xlog.PgOutput) error { return po.Commit(xid, commitLSN) }); err != nil {
		return fmt.Errorf("emit C: %w", err)
	}

	// Brief pause so the receiver actually parses the frames
	// before our close gives it io.EOF.
	time.Sleep(50 * time.Millisecond)
	return nil
}

// waitFor polls cond every ~10ms until it returns true or the
// timeout elapses. Returns true iff cond became true in time.
func waitForReconnectTest(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// Reference unused helper to keep the import valid across go
// linting passes — waitFor already exists in applylauncher_test.
var _ = waitForReconnectTest
