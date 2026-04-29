package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/protocol"
)

// fakePublisherConn returns a ConnPair backed by an in-memory
// pre-recorded publisher response (the bytes that would arrive
// from a publisher's CopyOutResponse / CopyData* / CopyDone /
// CommandComplete / ReadyForQuery sequence) and a discard sink
// for outbound subscriber bytes. Used by the manager tests so
// they don't have to spin up real TCP.
func fakePublisherConn(t *testing.T, rows []string) *ConnPair {
	t.Helper()
	pubBytes := pubRespond(t, rows)
	return &ConnPair{
		Reader: protocol.NewFrameReader(bytes.NewReader(pubBytes)),
		Writer: protocol.NewFrameWriter(&bytes.Buffer{}),
		Close:  func() error { return nil },
	}
}

// fakeWriterPair wraps a recordingLineWriter in a WriterPair so
// the manager's close path runs.
func fakeWriterPair(rec *recordingLineWriter) *WriterPair {
	return &WriterPair{
		Writer: rec,
		Close:  func() error { return nil },
	}
}

// TestRunTableSyncManagerWalksUnsyncedRels: a subscription with
// three rels (one each at i, d, r) drives exactly two sync
// invocations (the i and d rows), producing 's' at the end of
// each. The 'r' row is skipped — the manager doesn't touch
// already-ready rels.
func TestRunTableSyncManagerWalksUnsyncedRels(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", []string{"p"}, "", true); err != nil {
		t.Fatal(err)
	}
	for _, oid := range []uint32{10, 20, 30} {
		if _, err := ps.AddSubscriptionRel("sub1", oid); err != nil {
			t.Fatal(err)
		}
	}
	// 10 stays at 'i', 20 advances to 'd', 30 walks all the way
	// to 'r' so the manager skips it.
	if _, err := ps.AdvanceSubscriptionRel("sub1", 20, catalog.SubRelStateDataCopy, 0); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{
		catalog.SubRelStateDataCopy,
		catalog.SubRelStateSyncDone,
		catalog.SubRelStateReady,
	} {
		if _, err := ps.AdvanceSubscriptionRel("sub1", 30, st, 0); err != nil {
			t.Fatal(err)
		}
	}

	resolveCalls := 0
	connCalls := 0
	writerCalls := 0
	writers := map[uint32]*recordingLineWriter{}

	results, err := RunTableSyncManager(context.Background(), TableSyncManagerConfig{
		PubSub:  ps,
		SubName: "sub1",
		ResolveRel: func(oid uint32) (string, bool) {
			resolveCalls++
			return fmt.Sprintf("public.t%d", oid), true
		},
		OpenConn: func(_ context.Context, oid uint32) (*ConnPair, error) {
			connCalls++
			return fakePublisherConn(t, []string{
				fmt.Sprintf("%d\trow", oid),
			}), nil
		},
		OpenWriter: func(_ context.Context, oid uint32) (*WriterPair, error) {
			writerCalls++
			rec := &recordingLineWriter{}
			writers[oid] = rec
			return fakeWriterPair(rec), nil
		},
	})
	if err != nil {
		t.Fatalf("RunTableSyncManager: %v", err)
	}

	// Two non-'r' rels means two visits.
	if got := len(results); got != 2 {
		t.Fatalf("results=%d want 2", got)
	}
	if resolveCalls != 2 || connCalls != 2 || writerCalls != 2 {
		t.Errorf("resolveCalls=%d connCalls=%d writerCalls=%d, all want 2",
			resolveCalls, connCalls, writerCalls)
	}

	// Both visited rels should now be at 's'.
	for _, oid := range []uint32{10, 20} {
		got, _ := ps.LookupSubscriptionRel("sub1", oid)
		if got.State != catalog.SubRelStateSyncDone {
			t.Errorf("rel %d state=%q want s", oid, got.State)
		}
		if writers[oid].RowsInserted() != 1 {
			t.Errorf("rel %d rows=%d want 1", oid, writers[oid].RowsInserted())
		}
	}
	// The 'r' rel must be untouched.
	got, _ := ps.LookupSubscriptionRel("sub1", 30)
	if got.State != catalog.SubRelStateReady {
		t.Errorf("rel 30 state=%q want r (manager should not touch ready rels)", got.State)
	}
	if _, opened := writers[30]; opened {
		t.Errorf("writer for ready rel 30 should never have been opened")
	}

	// Per-result bookkeeping: InitialState reflects the state
	// BEFORE the visit; FinalState reflects the state AFTER.
	for _, r := range results {
		if r.FinalState != catalog.SubRelStateSyncDone {
			t.Errorf("rel %d FinalState=%q want s", r.RelOID, r.FinalState)
		}
		if r.Err != nil {
			t.Errorf("rel %d unexpected Err=%v", r.RelOID, r.Err)
		}
		if r.Rows != 1 {
			t.Errorf("rel %d Rows=%d want 1", r.RelOID, r.Rows)
		}
	}
}

// TestRunTableSyncManagerOneFailureDoesNotAbortRest: when one
// rel's OpenConn fails, the manager records the per-rel error
// and continues to the next rel. Mirrors upstream's behaviour
// where one stuck table doesn't block the rest.
func TestRunTableSyncManagerOneFailureDoesNotAbortRest(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	for _, oid := range []uint32{1, 2, 3} {
		if _, err := ps.AddSubscriptionRel("sub1", oid); err != nil {
			t.Fatal(err)
		}
	}

	dialErr := errors.New("dial refused")
	results, err := RunTableSyncManager(context.Background(), TableSyncManagerConfig{
		PubSub:     ps,
		SubName:    "sub1",
		ResolveRel: func(oid uint32) (string, bool) { return fmt.Sprintf("t%d", oid), true },
		OpenConn: func(_ context.Context, oid uint32) (*ConnPair, error) {
			if oid == 2 {
				return nil, dialErr
			}
			return fakePublisherConn(t, []string{"x\ty"}), nil
		},
		OpenWriter: func(_ context.Context, _ uint32) (*WriterPair, error) {
			return fakeWriterPair(&recordingLineWriter{}), nil
		},
	})
	if err != nil {
		t.Fatalf("manager-level err=%v want nil", err)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d want 3", len(results))
	}

	// rel 2 should carry the dial error; rels 1 and 3 succeed.
	var failed *TableSyncResult
	for i, r := range results {
		if r.RelOID == 2 {
			failed = &results[i]
		}
	}
	if failed == nil || !errors.Is(failed.Err, dialErr) {
		t.Errorf("failed result for rel 2 missing or wrong err: %+v", failed)
	}
	if failed.FinalState != catalog.SubRelStateInit {
		t.Errorf("failed rel 2 FinalState=%q want i (sync did not run)", failed.FinalState)
	}
	got1, _ := ps.LookupSubscriptionRel("sub1", 1)
	got3, _ := ps.LookupSubscriptionRel("sub1", 3)
	if got1.State != catalog.SubRelStateSyncDone || got3.State != catalog.SubRelStateSyncDone {
		t.Errorf("siblings of failed rel didn't reach s: 1=%q 3=%q", got1.State, got3.State)
	}
	got2, _ := ps.LookupSubscriptionRel("sub1", 2)
	if got2.State != catalog.SubRelStateInit {
		t.Errorf("failed rel 2 state=%q want i (no advance on conn failure)", got2.State)
	}
}

// TestRunTableSyncManagerUnresolvedRelnameSkips: a rel whose
// ResolveRel returns ok=false produces a per-rel error and is
// left in its original state.
func TestRunTableSyncManagerUnresolvedRelnameSkips(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("sub1", 42); err != nil {
		t.Fatal(err)
	}

	connOpened := false
	results, err := RunTableSyncManager(context.Background(), TableSyncManagerConfig{
		PubSub:     ps,
		SubName:    "sub1",
		ResolveRel: func(_ uint32) (string, bool) { return "", false },
		OpenConn: func(_ context.Context, _ uint32) (*ConnPair, error) {
			connOpened = true
			return nil, errors.New("should not dial")
		},
		OpenWriter: func(_ context.Context, _ uint32) (*WriterPair, error) {
			return fakeWriterPair(&recordingLineWriter{}), nil
		},
	})
	if err != nil {
		t.Fatalf("manager err=%v", err)
	}
	if connOpened {
		t.Errorf("OpenConn called for unresolved rel; should bail before dialling")
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Errorf("results=%+v want one with non-nil Err", results)
	}
	got, _ := ps.LookupSubscriptionRel("sub1", 42)
	if got.State != catalog.SubRelStateInit {
		t.Errorf("state=%q want i (no advance on resolve failure)", got.State)
	}
}

// TestRunTableSyncManagerNoUnsyncedRelsReturnsEmpty: an all-`r`
// subscription produces no results and never invokes any
// callback.
func TestRunTableSyncManagerNoUnsyncedRelsReturnsEmpty(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("sub1", 99); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{
		catalog.SubRelStateDataCopy,
		catalog.SubRelStateSyncDone,
		catalog.SubRelStateReady,
	} {
		if _, err := ps.AdvanceSubscriptionRel("sub1", 99, st, 0); err != nil {
			t.Fatal(err)
		}
	}

	results, err := RunTableSyncManager(context.Background(), TableSyncManagerConfig{
		PubSub:     ps,
		SubName:    "sub1",
		ResolveRel: func(_ uint32) (string, bool) { t.Fatal("ResolveRel should not be called"); return "", false },
		OpenConn:   func(_ context.Context, _ uint32) (*ConnPair, error) { t.Fatal("OpenConn should not be called"); return nil, nil },
		OpenWriter: func(_ context.Context, _ uint32) (*WriterPair, error) { t.Fatal("OpenWriter should not be called"); return nil, nil },
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(results) != 0 {
		t.Errorf("results=%v want empty", results)
	}
}
