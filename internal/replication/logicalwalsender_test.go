package replication

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/access/transam/xlog"
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

// TestWalsenderPgoutputAdapterWrapsAsCopyData pins the wire format
// (WalSndPrepareWrite, M0149-0040b): every Write call from a PgOutput
// plugin becomes one 'w' CopyData frame whose dataStart and walEnd are
// both the write location — the LSN of the WAL record being decoded — so
// the subscriber's replies name real WAL positions.
func TestWalsenderPgoutputAdapterWrapsAsCopyData(t *testing.T) {
	var buf bytes.Buffer
	fw := libpq.NewFrameWriter(&buf)

	a := &walsenderPgoutputAdapter{w: fw}
	a.setWriteLocation(0x1000)
	if _, err := a.Write([]byte("first-message")); err != nil {
		t.Fatal(err)
	}
	a.setWriteLocation(0x2000)
	if _, err := a.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}

	fr := libpq.NewFrameReader(&buf)
	for _, want := range []struct {
		lsn  uint64
		body string
	}{{0x1000, "first-message"}, {0x2000, "second"}} {
		f, err := fr.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if f.Type != libpq.MsgCopyData {
			t.Fatalf("frame Type=%q want CopyData", f.Type)
		}
		parsed, kind, err := libpq.DecodeReplicationMessage(f.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if kind != libpq.ReplMsgWALData {
			t.Fatalf("inner kind=%q want w", kind)
		}
		m := parsed.(*libpq.WALDataMessage)
		if string(m.WALBytes) != want.body || m.StartLSN != want.lsn || m.EndLSN != want.lsn {
			t.Errorf("frame %q start %#x walEnd %#x, want %q at %#x/%#x", m.WALBytes, m.StartLSN, m.EndLSN, want.body, want.lsn, want.lsn)
		}
	}
}

// TestWalsenderPgoutputAdapterKeepalive pins WalSndKeepalive for the
// LOGICAL walsender (M0103-0008, M0149-0040b): the keepalive's walEnd is
// sentPtr, the end of the last WAL record read, so an idle subscriber
// confirms up to the publisher's WAL end. A keepalive is due early while
// the subscriber's reported write and flush positions are behind sentPtr
// and none is awaiting a reply (WalSndWaitForWal).
func TestWalsenderPgoutputAdapterKeepalive(t *testing.T) {
	var buf bytes.Buffer
	fw := libpq.NewFrameWriter(&buf)

	a := &walsenderPgoutputAdapter{w: fw, sentPtr: 0x100}
	a.advanceSent(0x500)
	a.advanceSent(0x300) // never moves back
	if !a.subscriberBehind() {
		t.Fatal("subscriber that reported nothing must be behind sentPtr")
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := a.WriteKeepalive(now); err != nil {
		t.Fatal(err)
	}
	if a.subscriberBehind() {
		t.Fatal("no second early keepalive while one awaits its reply")
	}
	reply := libpq.EncodeStandbyStatusUpdate(0x500, 0x500, 0x500, now, false)
	a.noteReply(reply)
	if a.subscriberBehind() {
		t.Fatal("a subscriber that confirmed sentPtr is not behind")
	}

	fr := libpq.NewFrameReader(&buf)
	kf, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	parsed, kind, err := libpq.DecodeReplicationMessage(kf.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if kind != libpq.ReplMsgKeepalive {
		t.Fatalf("inner kind=%q want k", kind)
	}
	if k := parsed.(*libpq.KeepaliveMessage); k.WALEnd != 0x500 {
		t.Errorf("WALEnd=%#x want 0x500 (sentPtr)", k.WALEnd)
	}
}

// TestWalsenderPgoutputAdapterKeepaliveBeforeFirstWrite pins the
// no-messages-yet behaviour: an adapter that has read nothing must still
// emit a well-formed keepalive (walEnd = its start position, 0 here).
func TestWalsenderPgoutputAdapterKeepaliveBeforeFirstWrite(t *testing.T) {
	var buf bytes.Buffer
	fw := libpq.NewFrameWriter(&buf)
	a := &walsenderPgoutputAdapter{w: fw}

	if err := a.WriteKeepalive(time.Unix(1_700_000_000, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	fr := libpq.NewFrameReader(&buf)
	kf, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	parsed, kind, err := libpq.DecodeReplicationMessage(kf.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if kind != libpq.ReplMsgKeepalive {
		t.Fatalf("inner kind=%q want k", kind)
	}
	k := parsed.(*libpq.KeepaliveMessage)
	if k.WALEnd != 0 {
		t.Errorf("WALEnd=%d want 0 (no underflow on empty adapter)", k.WALEnd)
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

	items := &xlog.RelationDef{Schema: "public", Name: "items"}
	events := &xlog.RelationDef{Schema: "public", Name: "events"}

	if !f.Allows(items, xlog.ChangeInsert) {
		t.Errorf("items insert allowed=false want true")
	}
	if !f.Allows(items, xlog.ChangeDelete) {
		t.Errorf("items delete allowed=false want true")
	}
	if f.Allows(events, xlog.ChangeInsert) {
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

	if !f.Allows(&xlog.RelationDef{Name: "anything"}, xlog.ChangeInsert) {
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

	items := &xlog.RelationDef{Schema: "public", Name: "items"}
	if !f.Allows(items, xlog.ChangeInsert) {
		t.Errorf("insert blocked")
	}
	if f.Allows(items, xlog.ChangeUpdate) {
		t.Errorf("update allowed despite publish=insert,delete")
	}
	if !f.Allows(items, xlog.ChangeDelete) {
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
	items := &xlog.RelationDef{Schema: "public", Name: "items"}
	if !f.Allows(items, xlog.ChangeInsert) {
		t.Errorf("insert blocked despite p_ins covering it")
	}
	if !f.Allows(items, xlog.ChangeDelete) {
		t.Errorf("delete blocked despite p_del covering it")
	}
	if f.Allows(items, xlog.ChangeUpdate) {
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
	items := &xlog.RelationDef{Schema: "public", Name: "items"}
	if !f.Allows(items, xlog.ChangeInsert) {
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

// TestSplitPublicationNamesQuotedIdentifiers pins the
// `SplitIdentifierString`-equivalent semantics 0103-0016 adds for
// the libpq logical-replication wire shape `publication_names '"p"'`.
// Each name inside the option value is double-quote-wrapped so
// names containing commas remain safe to split; without quoted-
// identifier unquoting the lookup keys never matched the stored
// publication names and every decoded change was silently
// rejected (rung 11 of M0103-0008).
func TestSplitPublicationNamesQuotedIdentifiers(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "single quoted", raw: `"p"`, want: []string{"p"}},
		{name: "multiple quoted", raw: `"p","q"`, want: []string{"p", "q"}},
		{name: "doubled-quote escape", raw: `"a""b"`, want: []string{`a"b`}},
		{name: "unquoted lowercased", raw: `Foo`, want: []string{"foo"}},
		{name: "unquoted preserves case after lowering", raw: `FOO,BAR`, want: []string{"foo", "bar"}},
		{name: "quoted preserves case", raw: `"Foo"`, want: []string{"Foo"}},
		{name: "whitespace tolerance",
			raw:  `  "p"  ,  "q"  `,
			want: []string{"p", "q"}},
		{name: "empty input", raw: ``, want: nil},
		{name: "all whitespace", raw: `   `, want: nil},
		{name: "trailing comma allowed", raw: `"p",`, want: []string{"p"}},
		{name: "mixed quoted and unquoted", raw: `"p",q`, want: []string{"p", "q"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitPublicationNames(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("len(splitPublicationNames(%q)) = %d, want %d (%v)",
					tc.raw, len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("splitPublicationNames(%q)[%d] = %q, want %q",
						tc.raw, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestSplitPublicationNamesSyntaxErrorsReturnNil pins the explicit
// "return nil on syntax error" contract callers rely on to fall back
// to the v0 "no filter ⇒ pass everything" behaviour (see
// 0103-0016). Upstream rejects malformed `publication_names` at
// SUBSCRIPTION DDL time, well before the wire-level slot is started.
func TestSplitPublicationNamesSyntaxErrorsReturnNil(t *testing.T) {
	cases := []string{
		`"unterminated`,
		`"p" junk`,
	}
	for _, raw := range cases {
		if got := splitPublicationNames(raw); got != nil {
			t.Errorf("splitPublicationNames(%q) = %v, want nil", raw, got)
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

	syncRep := xlog.NewSyncRep()
	if err := syncRep.SetStandbyNames("goopg_sub"); err != nil {
		t.Fatalf("SetStandbyNames: %v", err)
	}

	s := &Handler{cfg: Config{SyncRep: syncRep}}

	const commitLSN uint64 = 0x1000

	// Initial subscriber report: write/flush ahead, apply still behind.
	// This is what an apply worker that has buffered the txn but not yet
	// committed locally looks like.
	lagPayload := libpq.EncodeStandbyStatusUpdate(
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
		done <- syncRep.WaitForLSN(waitCtx, commitLSN, xlog.SyncRepRemoteApply)
	}()

	// The waiter must still be blocked: apply < commitLSN.
	select {
	case err := <-done:
		t.Fatalf("WaitForLSN released early (apply=%x < target=%x): %v", apply, commitLSN, err)
	case <-time.After(50 * time.Millisecond):
	}

	// Subscriber now reports apply at the commit target — this is the
	// post-apply ack and should release the COMMIT.
	catchupPayload := libpq.EncodeStandbyStatusUpdate(
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

	syncRep := xlog.NewSyncRep()
	s := &Handler{cfg: Config{SyncRep: syncRep}}

	payload := libpq.EncodeStandbyStatusUpdate(0x500, 0x500, 0x500,
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

// TestBuildPublicationFilterResolvesCrossDB pins M0119-0006 (deferral row 1354
// claim 2): the PubSub registry is keyed per-database
// (pubMapKey(dbOid, name), catalog/pubsub.go), so buildPublicationFilter must
// resolve a publication under the slot's database. The pre-fix hardcode
// (LookupPublication(name, DefaultDBOid)) missed a DB-2 publication entirely,
// leaving the filter non-nil but empty — publicationFilter.Allows then
// returned false for every change and the stream silently dropped all of it.
func TestBuildPublicationFilterResolvesCrossDB(t *testing.T) {
	im := catalog.NewInMemory()
	db2Oid, err := im.CreateDatabase("db2", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase(db2): %v", err)
	}
	ps := catalog.NewPubSub()
	if _, err := ps.CreatePublication("p_db2", []string{"public.items"}, catalog.DefaultPublicationOptions(), db2Oid); err != nil {
		t.Fatal(err)
	}

	items := &xlog.RelationDef{Schema: "public", Name: "items"}

	// With DB-2 oid: the DB-2 publication resolves → the filter admits items.
	f2 := buildPublicationFilter(ps, []string{"p_db2"}, db2Oid)
	if !f2.Allows(items, xlog.ChangeInsert) {
		t.Errorf("DB-2 publication not resolved with dbOid=%d: items insert blocked", db2Oid)
	}

	// With empty dbOid (default DB-1): the DB-2 publication is not found and
	// silently skipped — same behaviour as the pre-fix hardcode.
	fDefault := buildPublicationFilter(ps, []string{"p_db2"})
	if fDefault.Allows(items, xlog.ChangeInsert) {
		t.Errorf("DB-2 publication leaked into the default (DB-1) filter")
	}
}

// TestWalsenderCatalogDBOidVar pins the M-NIGHTLY
// AI-20260815-011722-003 fix (regression of 92d99a25): the walsender's
// catalog-snapshot and publication-filter dbOid must be the namespace oid
// (DefaultDBOid for "postgres" — goopg persists postgres-routed
// tables/publications under DefaultDBOid, not the raw physical PostgresDBOid
// 5), or PgOutput.Change silently drops every DML. The fixture mimics the live
// server, where detectCatalogDBOID reads PostgresDBOid (5) back from the
// physical global/1262 heap and stamps it on the catalog.
func TestWalsenderCatalogDBOidVar(t *testing.T) {
	im := catalog.NewInMemory()
	im.SetDBOID(catalog.PostgresDBOid)
	if got, want := walsenderCatalogDBOidVar(im, "postgres"), []uint32{catalog.DefaultDBOid}; !slices.Equal(got, want) {
		t.Fatalf("postgres → %v, want %v", got, want)
	}
	if got, want := walsenderCatalogDBOidVar(im, ""), []uint32{catalog.DefaultDBOid}; !slices.Equal(got, want) {
		t.Fatalf("empty dbName → %v, want %v", got, want)
	}
}

// TestWalsenderPgoutputAdapterEmptyWriteIsDropped pins review/260831-2 CP-4:
// an empty payload carries nothing and must put no frame on the wire; the
// next real write still goes out at the write location.
func TestWalsenderPgoutputAdapterEmptyWriteIsDropped(t *testing.T) {
	var buf bytes.Buffer
	fw := libpq.NewFrameWriter(&buf)

	a := &walsenderPgoutputAdapter{w: fw}
	a.setWriteLocation(100)
	n, err := a.Write(nil)
	if err != nil {
		t.Fatalf("Write(nil): %v", err)
	}
	if n != 0 {
		t.Errorf("Write(nil) = %d, want 0", n)
	}
	if buf.Len() != 0 {
		t.Errorf("empty write put %d bytes on the wire, want none", buf.Len())
	}

	// The next real write still starts where it should.
	if _, err := a.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	fr := libpq.NewFrameReader(&buf)
	f, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := libpq.DecodeReplicationMessage(f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	m := parsed.(*libpq.WALDataMessage)
	if m.StartLSN != 100 || m.EndLSN != 100 {
		t.Errorf("after the empty write: StartLSN=%d EndLSN=%d, want 100/100", m.StartLSN, m.EndLSN)
	}
}
