package server

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/wal"
)

// TestParseStartReplicationArgsLogical pins the publisher-side
// parser for the LOGICAL form: a real subscriber emits something
// like
//
//	START_REPLICATION SLOT logical1 LOGICAL 0/0
//	    ("proto_version" '1', "publication_names" 'p1,p2')
//
// and the parser must recognise the mode keyword, slot name, LSN,
// and the option block.
func TestParseStartReplicationArgsLogical(t *testing.T) {
	args, err := parseStartReplicationArgs(
		`START_REPLICATION SLOT logical1 LOGICAL 0/CAFE ("proto_version" '1', "publication_names" 'p1,p2')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if args.Mode != "LOGICAL" {
		t.Errorf("Mode=%q want LOGICAL", args.Mode)
	}
	if args.SlotName != "logical1" {
		t.Errorf("SlotName=%q want logical1", args.SlotName)
	}
	if args.StartLSN != 0xCAFE {
		t.Errorf("StartLSN=%x want 0xCAFE", args.StartLSN)
	}
	if args.Options["proto_version"] != "1" {
		t.Errorf("proto_version=%q want 1", args.Options["proto_version"])
	}
	if args.Options["publication_names"] != "p1,p2" {
		t.Errorf("publication_names=%q want p1,p2", args.Options["publication_names"])
	}
}

// TestParseStartReplicationArgsLogicalRequiresSlot pins the
// upstream rule that LOGICAL mode demands a SLOT clause.
func TestParseStartReplicationArgsLogicalRequiresSlot(t *testing.T) {
	if _, err := parseStartReplicationArgs(`START_REPLICATION LOGICAL 0/0`); err == nil {
		t.Errorf("LOGICAL without SLOT was accepted")
	}
}

// TestParseStartReplicationArgsPhysicalStillWorks: the
// existing PHYSICAL grammar continues to parse, including
// the slot+timeline shape.
func TestParseStartReplicationArgsPhysicalStillWorks(t *testing.T) {
	args, err := parseStartReplicationArgs(`START_REPLICATION SLOT primary PHYSICAL 1/2 TIMELINE 1`)
	if err != nil {
		t.Fatal(err)
	}
	if args.Mode != "PHYSICAL" {
		t.Errorf("Mode=%q", args.Mode)
	}
	if args.SlotName != "primary" {
		t.Errorf("SlotName=%q", args.SlotName)
	}
	if args.StartLSN != (uint64(1)<<32)|2 {
		t.Errorf("StartLSN=%x", args.StartLSN)
	}
	if args.Timeline != 1 {
		t.Errorf("Timeline=%d", args.Timeline)
	}
}

// TestWalsenderPgoutputAdapterWrapsAsCopyData pins the wire-
// format invariant: every Write call from a PgOutput plugin
// becomes one `'w'` CopyData frame on the wire, with monotonic
// startLSN/endLSN. The subscriber's LogicalReceiver expects
// exactly this shape.
func TestWalsenderPgoutputAdapterWrapsAsCopyData(t *testing.T) {
	var buf bytes.Buffer
	fw := protocol.NewFrameWriter(&buf)

	a := &walsenderPgoutputAdapter{w: fw, nextLSN: 100}
	if _, err := a.Write([]byte("first-message")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}

	// Read the framed bytes back.
	fr := protocol.NewFrameReader(&buf)
	f1, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if f1.Type != protocol.MsgCopyData {
		t.Errorf("frame[0].Type=%q want CopyData", f1.Type)
	}
	parsed, kind, err := protocol.DecodeReplicationMessage(f1.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if kind != protocol.ReplMsgWALData {
		t.Errorf("frame[0] inner kind=%q want w", kind)
	}
	m1 := parsed.(*protocol.WALDataMessage)
	if string(m1.WALBytes) != "first-message" {
		t.Errorf("frame[0] body=%q", m1.WALBytes)
	}
	if m1.StartLSN != 100 {
		t.Errorf("frame[0] StartLSN=%d want 100", m1.StartLSN)
	}
	if m1.EndLSN != 100+uint64(len("first-message"))-1 {
		t.Errorf("frame[0] EndLSN=%d", m1.EndLSN)
	}

	f2, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	parsed2, _, _ := protocol.DecodeReplicationMessage(f2.Payload)
	m2 := parsed2.(*protocol.WALDataMessage)
	if m2.StartLSN != m1.EndLSN+1 {
		t.Errorf("frame[1] StartLSN=%d want %d (monotonic)", m2.StartLSN, m1.EndLSN+1)
	}
	if string(m2.WALBytes) != "second" {
		t.Errorf("frame[1] body=%q", m2.WALBytes)
	}
}

// TestPublicationFilterAllowsByTable: a publication that names
// "items" admits every change for items (subject to publish
// flags) and rejects every change for other tables.
func TestPublicationFilterAllowsByTable(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreatePublication("p", []string{"public.items"}, catalog.DefaultPublicationOptions()); err != nil {
		t.Fatal(err)
	}
	f := buildPublicationFilter(ps, []string{"p"})

	items := &wal.RelationDef{Schema: "public", Name: "items"}
	events := &wal.RelationDef{Schema: "public", Name: "events"}

	if !f.Allows(items, wal.ChangeInsert) {
		t.Errorf("items insert allowed=false want true")
	}
	if !f.Allows(items, wal.ChangeDelete) {
		t.Errorf("items delete allowed=false want true")
	}
	if f.Allows(events, wal.ChangeInsert) {
		t.Errorf("events insert allowed=true want false (not in publication)")
	}
}

// TestPublicationFilterAllTables: FOR ALL TABLES admits every
// relation regardless of the per-table list.
func TestPublicationFilterAllTables(t *testing.T) {
	ps := catalog.NewPubSub()
	opts := catalog.DefaultPublicationOptions()
	opts.AllTables = true
	if _, err := ps.CreatePublication("pall", nil, opts); err != nil {
		t.Fatal(err)
	}
	f := buildPublicationFilter(ps, []string{"pall"})

	if !f.Allows(&wal.RelationDef{Name: "anything"}, wal.ChangeInsert) {
		t.Errorf("FOR ALL TABLES rejects an arbitrary relation")
	}
}

// TestPublicationFilterRespectsPublishFlags: a publication with
// publish=insert,delete (no update) admits inserts and deletes
// but not updates for its tables.
func TestPublicationFilterRespectsPublishFlags(t *testing.T) {
	ps := catalog.NewPubSub()
	opts := catalog.DefaultPublicationOptions()
	opts.PublishUpdate = false
	if _, err := ps.CreatePublication("p", []string{"public.items"}, opts); err != nil {
		t.Fatal(err)
	}
	f := buildPublicationFilter(ps, []string{"p"})

	items := &wal.RelationDef{Schema: "public", Name: "items"}
	if !f.Allows(items, wal.ChangeInsert) {
		t.Errorf("insert blocked")
	}
	if f.Allows(items, wal.ChangeUpdate) {
		t.Errorf("update allowed despite publish=insert,delete")
	}
	if !f.Allows(items, wal.ChangeDelete) {
		t.Errorf("delete blocked")
	}
}

// TestPublicationFilterUnionAcrossPublications: when the slot
// names two publications, the relation is allowed if any
// publication grants the action. Mirrors upstream's
// "any publication grants the change" rule.
func TestPublicationFilterUnionAcrossPublications(t *testing.T) {
	ps := catalog.NewPubSub()
	insertOnly := catalog.DefaultPublicationOptions()
	insertOnly.PublishUpdate = false
	insertOnly.PublishDelete = false
	deleteOnly := catalog.DefaultPublicationOptions()
	deleteOnly.PublishInsert = false
	deleteOnly.PublishUpdate = false
	if _, err := ps.CreatePublication("p_ins", []string{"public.items"}, insertOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.CreatePublication("p_del", []string{"public.items"}, deleteOnly); err != nil {
		t.Fatal(err)
	}

	f := buildPublicationFilter(ps, []string{"p_ins", "p_del"})
	items := &wal.RelationDef{Schema: "public", Name: "items"}
	if !f.Allows(items, wal.ChangeInsert) {
		t.Errorf("insert blocked despite p_ins covering it")
	}
	if !f.Allows(items, wal.ChangeDelete) {
		t.Errorf("delete blocked despite p_del covering it")
	}
	if f.Allows(items, wal.ChangeUpdate) {
		t.Errorf("update allowed despite neither publication granting it")
	}
}

// TestPublicationFilterUnknownPublicationSkipped: a non-existent
// publication name is silently skipped — the slot still works
// against any publications that do exist.
func TestPublicationFilterUnknownPublicationSkipped(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreatePublication("real", []string{"public.items"}, catalog.DefaultPublicationOptions()); err != nil {
		t.Fatal(err)
	}
	f := buildPublicationFilter(ps, []string{"missing", "real"})
	items := &wal.RelationDef{Schema: "public", Name: "items"}
	if !f.Allows(items, wal.ChangeInsert) {
		t.Errorf("insert blocked despite real publication being among the list")
	}
}

// TestSplitPublicationNamesTrimsAndDropsEmpty.
func TestSplitPublicationNamesTrimsAndDropsEmpty(t *testing.T) {
	got := splitPublicationNames(" p1 , p2,, p3 ,")
	want := []string{"p1", "p2", "p3"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}


// TestLogicalSyncRepDispatchUnblocksOnApplyCatchup is the M0103-0005
// integration test for the logical walsender → SyncRep wait queue.
//
// It pins the round-trip the real publisher takes: the subscriber emits
// a Standby Status Update CopyData frame on the START_REPLICATION
// LOGICAL stream, the walsender's receive-side goroutine calls
// handleStandbyCopyData, that dispatcher decodes the 'r' message and
// feeds SyncRep.UpdateStandbyProgress keyed on the application_name
// from the START_REPLICATION handshake. A publisher COMMIT blocked on
// remote_apply must release as soon as the subscriber's apply_lsn
// crosses the commit target.
//
// Race-tested: run with `-race`. The test fans out two goroutines
// (waiter + feeder) that share the SyncRep instance.
func TestLogicalSyncRepDispatchUnblocksOnApplyCatchup(t *testing.T) {
	t.Parallel()

	syncRep := wal.NewSyncRep()
	if err := syncRep.SetStandbyNames("goopg_sub"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}

	s := &Server{cfg: Config{SyncRep: syncRep}}

	const commitLSN uint64 = 0x1000

	// Initial subscriber report: write/flush ahead, apply still behind.
	// This is what an apply worker that has buffered the txn but not yet
	// committed locally looks like.
	lagPayload := protocol.EncodeStandbyStatusUpdate(
		commitLSN+0x100, commitLSN+0x100, commitLSN-1,
		time.Unix(0, 0).UTC(), false,
	)
	if err := s.handleStandbyCopyData("", lagPayload, nil, syncRep, "goopg_sub"); err != nil {
		t.Fatalf("dispatch lag report: %v", err)
	}
	write, flush, apply := syncRep.StandbyProgress("goopg_sub")
	if write != commitLSN+0x100 || flush != commitLSN+0x100 || apply != commitLSN-1 {
		t.Fatalf("lag progress: write=%x flush=%x apply=%x want %x/%x/%x",
			write, flush, apply,
			commitLSN+0x100, commitLSN+0x100, commitLSN-1)
	}

	// Start a publisher-side COMMIT waiter on remote_apply.
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- syncRep.WaitForLSN(waitCtx, commitLSN, wal.SyncRepRemoteApply)
	}()

	// The waiter must still be blocked: apply < commitLSN.
	select {
	case err := <-done:
		t.Fatalf("WaitForLSN released early (apply=%x < target=%x): %v", apply, commitLSN, err)
	case <-time.After(50 * time.Millisecond):
	}

	// Subscriber now reports apply at the commit target — this is the
	// post-apply ack and should release the COMMIT.
	catchupPayload := protocol.EncodeStandbyStatusUpdate(
		commitLSN+0x100, commitLSN+0x100, commitLSN,
		time.Unix(0, 0).UTC(), false,
	)
	if err := s.handleStandbyCopyData("", catchupPayload, nil, syncRep, "goopg_sub"); err != nil {
		t.Fatalf("dispatch catchup report: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForLSN: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForLSN did not release after apply_lsn caught up via logical-walsender dispatch")
	}
}

// TestLogicalSyncRepDispatchEmptyAppNameIsNoop pins the safety
// invariant that a START_REPLICATION LOGICAL connection without an
// `application_name` startup parameter does NOT pollute the SyncRep
// registry with an empty-string entry. Otherwise a stray
// SetStandbyNames('"".*') or a default rule could accidentally match.
func TestLogicalSyncRepDispatchEmptyAppNameIsNoop(t *testing.T) {
	t.Parallel()

	syncRep := wal.NewSyncRep()
	s := &Server{cfg: Config{SyncRep: syncRep}}

	payload := protocol.EncodeStandbyStatusUpdate(0x500, 0x500, 0x500,
		time.Unix(0, 0).UTC(), false)
	if err := s.handleStandbyCopyData("", payload, nil, syncRep, ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	write, flush, apply := syncRep.StandbyProgress("")
	if write != 0 || flush != 0 || apply != 0 {
		t.Fatalf("empty appName progress was recorded: write=%x flush=%x apply=%x",
			write, flush, apply)
	}
}
