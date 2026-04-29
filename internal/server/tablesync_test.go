package server

import (
	"bytes"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/protocol"
)

// recordingLineWriter is the unit-test fake for LineWriter. It
// captures every line PushLine sees so the test can assert the
// exact byte-stream the publisher emitted reached the subscriber
// intact, and lets a test inject a row-level apply error.
type recordingLineWriter struct {
	lines     [][]byte
	failOnRow int
	failErr   error
}

func (r *recordingLineWriter) PushLine(line []byte) error {
	if r.failOnRow > 0 && len(r.lines) == r.failOnRow-1 && r.failErr != nil {
		return r.failErr
	}
	cp := make([]byte, len(line))
	copy(cp, line)
	r.lines = append(r.lines, cp)
	return nil
}

func (r *recordingLineWriter) RowsInserted() int64 { return int64(len(r.lines)) }

// pubRespond builds a publisher → subscriber byte stream that
// mirrors what runCopyTo emits: CopyOutResponse, one CopyData per
// '\n'-terminated row, CopyDone, CommandComplete, ReadyForQuery.
func pubRespond(t *testing.T, rows []string) []byte {
	t.Helper()
	var pubOut bytes.Buffer
	w := protocol.NewFrameWriter(&pubOut)
	if err := w.WriteCopyOutResponse(0, nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := w.WriteCopyData([]byte(r + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.WriteCopyDone(); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCommandComplete("COPY 0"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return pubOut.Bytes()
}

// TestRunTableSyncHappyPath: a clean exchange with three rows
// drives the state machine i → d → s, hands every row to the
// LineWriter as the exact COPY-TEXT line bytes the publisher
// emitted (minus the trailing '\n'), and writes a Query frame
// upstream containing the COPY SQL.
func TestRunTableSyncHappyPath(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", []string{"p"}, "", true); err != nil {
		t.Fatal(err)
	}

	pubBytes := pubRespond(t, []string{"1\thello", "2\tworld", "3\tfoo"})

	var subOut bytes.Buffer
	from := &recordingLineWriter{}
	rows, err := RunTableSync(TableSyncConfig{
		PubSub:  ps,
		SubName: "sub1",
		RelOID:  16400,
		Relname: "public.items",
		From:    from,
		Reader:  protocol.NewFrameReader(bytes.NewReader(pubBytes)),
		Writer:  protocol.NewFrameWriter(&subOut),
	})
	if err != nil {
		t.Fatalf("RunTableSync: %v", err)
	}
	if rows != 3 {
		t.Errorf("rows=%d want 3", rows)
	}

	// Verify the bytes the subscriber sent to the publisher: a
	// single Query frame whose body is `COPY public.items TO STDOUT\0`.
	subFR := protocol.NewFrameReader(bytes.NewReader(subOut.Bytes()))
	f, err := subFR.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != protocol.MsgQuery {
		t.Errorf("subscriber sent type=%q want Q", f.Type)
	}
	if !strings.HasPrefix(string(f.Payload), "COPY public.items TO STDOUT") {
		t.Errorf("subscriber sent body=%q", f.Payload)
	}

	// Verify line-for-line round-trip.
	want := [][]byte{[]byte("1\thello"), []byte("2\tworld"), []byte("3\tfoo")}
	if !reflect.DeepEqual(from.lines, want) {
		t.Errorf("lines=%q want %q", from.lines, want)
	}

	// Catalog state should be 's' now.
	got, ok := ps.LookupSubscriptionRel("sub1", 16400)
	if !ok {
		t.Fatal("subscription_rel row missing")
	}
	if got.State != catalog.SubRelStateSyncDone {
		t.Errorf("state=%q want s", got.State)
	}
}

// TestRunTableSyncMultiRowFrame: a publisher that batches several
// rows into one CopyData frame still produces one PushLine call
// per row.
func TestRunTableSyncMultiRowFrame(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}

	var pubOut bytes.Buffer
	w := protocol.NewFrameWriter(&pubOut)
	w.WriteCopyOutResponse(0, nil)
	w.WriteCopyData([]byte("1\ta\n2\tb\n3\tc\n"))
	w.WriteCopyDone()
	w.WriteCommandComplete("COPY 3")
	w.WriteReadyForQuery(protocol.TxStatusIdle)
	w.Flush()

	from := &recordingLineWriter{}
	var subOut bytes.Buffer
	rows, err := RunTableSync(TableSyncConfig{
		PubSub:  ps,
		SubName: "sub1",
		RelOID:  1,
		Relname: "t",
		From:    from,
		Reader:  protocol.NewFrameReader(bytes.NewReader(pubOut.Bytes())),
		Writer:  protocol.NewFrameWriter(&subOut),
	})
	if err != nil {
		t.Fatalf("RunTableSync: %v", err)
	}
	if rows != 3 {
		t.Errorf("rows=%d want 3", rows)
	}
	if len(from.lines) != 3 ||
		string(from.lines[0]) != "1\ta" ||
		string(from.lines[1]) != "2\tb" ||
		string(from.lines[2]) != "3\tc" {
		t.Errorf("lines=%q", from.lines)
	}
}

// TestRunTableSyncAdvancesIToDOnCopyOutResponse: even before any
// rows arrive, seeing CopyOutResponse should bump the state to
// 'd' so that an interrupted sync can be observed in catalog.
func TestRunTableSyncAdvancesIToDOnCopyOutResponse(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}

	// Publisher returns CopyOutResponse then errors mid-stream.
	var pubOut bytes.Buffer
	w := protocol.NewFrameWriter(&pubOut)
	w.WriteCopyOutResponse(0, nil)
	w.WriteErrorResponse([]protocol.ErrorField{
		{Code: 'C', Value: "XX000"},
		{Code: 'M', Value: "publisher exploded"},
	})
	w.Flush()

	from := &recordingLineWriter{}
	var subOut bytes.Buffer
	_, err := RunTableSync(TableSyncConfig{
		PubSub:  ps,
		SubName: "sub1",
		RelOID:  7,
		Relname: "t",
		From:    from,
		Reader:  protocol.NewFrameReader(bytes.NewReader(pubOut.Bytes())),
		Writer:  protocol.NewFrameWriter(&subOut),
	})
	if err == nil {
		t.Fatal("RunTableSync returned nil error after publisher error")
	}
	if !strings.Contains(err.Error(), "publisher exploded") {
		t.Errorf("err=%v should surface the publisher M field", err)
	}
	got, ok := ps.LookupSubscriptionRel("sub1", 7)
	if !ok || got.State != catalog.SubRelStateDataCopy {
		t.Errorf("state=%+v want d (i→d advance happened before the error)", got)
	}
}

// TestRunTableSyncRowApplyErrorLeavesStateAtD: when PushLine fails
// (e.g. constraint violation locally), the sync stops, the state
// stays at 'd' (not 's'), and the publisher error is wrapped.
func TestRunTableSyncRowApplyErrorLeavesStateAtD(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	pubBytes := pubRespond(t, []string{"1\ta", "2\tb"})

	from := &recordingLineWriter{
		failOnRow: 2,
		failErr:   errors.New("decode failure"),
	}
	var subOut bytes.Buffer
	_, err := RunTableSync(TableSyncConfig{
		PubSub:  ps,
		SubName: "sub1",
		RelOID:  9,
		Relname: "t",
		From:    from,
		Reader:  protocol.NewFrameReader(bytes.NewReader(pubBytes)),
		Writer:  protocol.NewFrameWriter(&subOut),
	})
	if err == nil || !strings.Contains(err.Error(), "decode failure") {
		t.Errorf("err=%v want wrap of decode failure", err)
	}
	got, _ := ps.LookupSubscriptionRel("sub1", 9)
	if got.State != catalog.SubRelStateDataCopy {
		t.Errorf("state=%q want d (apply failure should not advance to s)", got.State)
	}
}

// TestRunTableSyncResumesFromExistingRow: a row left at 'd' from a
// previous attempt is not reset to 'i' (no monotonic violation),
// and the second attempt drives it through to 's'.
func TestRunTableSyncResumesFromExistingRow(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("sub1", 11); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AdvanceSubscriptionRel("sub1", 11, catalog.SubRelStateDataCopy, 0); err != nil {
		t.Fatal(err)
	}
	pubBytes := pubRespond(t, []string{"42\tx"})

	from := &recordingLineWriter{}
	var subOut bytes.Buffer
	if _, err := RunTableSync(TableSyncConfig{
		PubSub:  ps,
		SubName: "sub1",
		RelOID:  11,
		Relname: "t",
		From:    from,
		Reader:  protocol.NewFrameReader(bytes.NewReader(pubBytes)),
		Writer:  protocol.NewFrameWriter(&subOut),
	}); err != nil {
		t.Fatalf("RunTableSync: %v", err)
	}
	got, _ := ps.LookupSubscriptionRel("sub1", 11)
	if got.State != catalog.SubRelStateSyncDone {
		t.Errorf("state=%q want s", got.State)
	}
}

// TestRunTableSyncRejectsUnexpectedFrame: an out-of-shape response
// (e.g. RowDescription instead of CopyOutResponse) yields a clear
// error and leaves the catalog at 'i'.
func TestRunTableSyncRejectsUnexpectedFrame(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	var pubOut bytes.Buffer
	w := protocol.NewFrameWriter(&pubOut)
	w.WriteRowDescription(nil)
	w.Flush()

	from := &recordingLineWriter{}
	var subOut bytes.Buffer
	_, err := RunTableSync(TableSyncConfig{
		PubSub:  ps,
		SubName: "sub1",
		RelOID:  13,
		Relname: "t",
		From:    from,
		Reader:  protocol.NewFrameReader(bytes.NewReader(pubOut.Bytes())),
		Writer:  protocol.NewFrameWriter(&subOut),
	})
	if err == nil || !strings.Contains(err.Error(), "expected CopyOutResponse") {
		t.Errorf("err=%v want 'expected CopyOutResponse'", err)
	}
	got, _ := ps.LookupSubscriptionRel("sub1", 13)
	if got.State != catalog.SubRelStateInit {
		t.Errorf("state=%q want i (no advance on shape violation)", got.State)
	}
}

// TestRunTableSyncLogsLifecycle pins the structured-log
// contract: a happy-path RunTableSync emits started, the
// state-change line for d→s, and completed with the row count.
// (i→d state-change only logs when the prior state was actually
// 'i'; here the row is fresh so we expect both transitions.)
func TestRunTableSyncLogsLifecycle(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub_log", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	pubBytes := pubRespond(t, []string{"1\ta", "2\tb"})
	from := &recordingLineWriter{}
	var subOut bytes.Buffer
	rows, err := RunTableSync(TableSyncConfig{
		PubSub:  ps,
		SubName: "sub_log",
		RelOID:  16400,
		Relname: "public.t",
		From:    from,
		Reader:  protocol.NewFrameReader(bytes.NewReader(pubBytes)),
		Writer:  protocol.NewFrameWriter(&subOut),
		Logger:  logger,
	})
	if err != nil || rows != 2 {
		t.Fatalf("rows=%d err=%v want (2, nil)", rows, err)
	}

	want := []string{
		`"event":"tablesync_started"`,
		`"event":"tablesync_state_change"`,
		`"from":"i"`,
		`"to":"d"`,
		`"from":"d"`,
		`"to":"s"`,
		`"event":"tablesync_completed"`,
		`"rows":2`,
	}
	out := buf.String()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("log missing fragment %q; full output:\n%s", w, out)
		}
	}
}
