package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestPubSubDDLRecoveryReplaysCreatePublication confirms the DU-002
// restart-persistence hook (M0119-0004, loop #67 ledger resume point): a
// CREATE PUBLICATION WAL record written by a pre-crash run is replayed into
// catalog.PubSub on the post-crash Open path, so pg_publication (pg_dump's
// getPublications) round-trips after restart even though the goopg process
// never wrote a JSON snapshot. The OID/owner/table list all survive the
// restart.
func TestPubSubDDLRecoveryReplaysCreatePublication(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40600)
	const wantOwnerOID = uint32(16401)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreatePublication("mypub", []string{"public.t1"}, wantOID, wantOwnerOID, false, true, true, true)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-publication: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the publication in PubSub without
	// any JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	pub, ok := rt2.PubSub.LookupPublication("mypub")
	if !ok {
		t.Fatalf("after WAL replay, publication \"mypub\" not found; registry = %+v", rt2.PubSub.Publications())
	}
	if pub.OID != wantOID || pub.Owner != wantOwnerOID || pub.AllTables || !pub.PublishInsert || !pub.PublishUpdate || !pub.PublishDelete {
		t.Errorf("after WAL replay, publication = %+v, want OID=%d Owner=%d AllTables=false Publish{Insert,Update,Delete}=true", pub, wantOID, wantOwnerOID)
	}
	if len(pub.Tables) != 1 || pub.Tables[0] != "public.t1" {
		t.Errorf("after WAL replay, publication.Tables = %v, want [public.t1]", pub.Tables)
	}
}

// TestPubSubDDLRecoveryReplaysDropPublicationAfterCreate confirms a CREATE
// followed by a DROP cancels out — the registry agrees with the most recent
// durable record, not the first one.
func TestPubSubDDLRecoveryReplaysDropPublicationAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreatePublication("mypub", nil, 40610, 10, true, true, true, true)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropPublication("mypub")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	if pub, ok := rt2.PubSub.LookupPublication("mypub"); ok {
		t.Errorf("after CREATE + DROP replay, publication \"mypub\" = %+v, want not found", pub)
	}
}

// TestPubSubDDLRecoveryReplaysAlterPublicationOwnerAfterCreate mirrors the
// collation OWNER TO recovery test for ALTER PUBLICATION ... OWNER TO.
func TestPubSubDDLRecoveryReplaysAlterPublicationOwnerAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40620)
	const wantOwnerOID = uint32(16402)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreatePublication("mypub", nil, wantOID, 10, true, true, true, true)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterPublicationOwner("mypub", wantOwnerOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append owner: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	pub, ok := rt2.PubSub.LookupPublication("mypub")
	if !ok {
		t.Fatalf("after CREATE + OWNER replay, \"mypub\" not found; registry = %+v", rt2.PubSub.Publications())
	}
	if pub.OID != wantOID {
		t.Errorf("after CREATE + OWNER replay, OID = %d, want %d (owner change must not disturb OID)", pub.OID, wantOID)
	}
	if pub.Owner != wantOwnerOID {
		t.Errorf("after CREATE + OWNER replay, Owner = %d, want %d", pub.Owner, wantOwnerOID)
	}
}

// TestPubSubDDLRecoveryReplaysCreateSubscription is the subscription
// analogue of TestPubSubDDLRecoveryReplaysCreatePublication.
func TestPubSubDDLRecoveryReplaysCreateSubscription(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40630)
	const wantOwnerOID = uint32(16403)
	const wantDBOid = uint32(16401)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateSubscription("mysub", "host=localhost dbname=foo", "myslot", []string{"mypub"}, wantOID, wantOwnerOID, true, wantDBOid)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-subscription: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	sub, ok := rt2.PubSub.LookupSubscription("mysub")
	if !ok {
		t.Fatalf("after WAL replay, subscription \"mysub\" not found; registry = %+v", rt2.PubSub.Subscriptions())
	}
	if sub.OID != wantOID || sub.Owner != wantOwnerOID || sub.Conninfo != "host=localhost dbname=foo" || sub.SlotName != "myslot" || !sub.Enabled || sub.DBOid != wantDBOid {
		t.Errorf("after WAL replay, subscription = %+v, want OID=%d Owner=%d Conninfo=... SlotName=myslot Enabled=true DBOid=%d", sub, wantOID, wantOwnerOID, wantDBOid)
	}
	if len(sub.Publications) != 1 || sub.Publications[0] != "mypub" {
		t.Errorf("after WAL replay, subscription.Publications = %v, want [mypub]", sub.Publications)
	}
}

// TestPubSubDDLRecoveryReplaysDropSubscriptionAfterCreate is the
// subscription analogue of TestPubSubDDLRecoveryReplaysDropPublicationAfterCreate.
func TestPubSubDDLRecoveryReplaysDropSubscriptionAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateSubscription("mysub", "host=localhost", "mysub", []string{"mypub"}, 40640, 10, true, 0)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropSubscription("mysub")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	if sub, ok := rt2.PubSub.LookupSubscription("mysub"); ok {
		t.Errorf("after CREATE + DROP replay, subscription \"mysub\" = %+v, want not found", sub)
	}
}

// TestPubSubDDLRecoveryReplaysAlterSubscriptionOwnerAfterCreate is the
// subscription analogue of
// TestPubSubDDLRecoveryReplaysAlterPublicationOwnerAfterCreate.
func TestPubSubDDLRecoveryReplaysAlterSubscriptionOwnerAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40650)
	const wantOwnerOID = uint32(16404)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateSubscription("mysub", "host=localhost", "mysub", nil, wantOID, 10, true, 0)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterSubscriptionOwner("mysub", wantOwnerOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append owner: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	sub, ok := rt2.PubSub.LookupSubscription("mysub")
	if !ok {
		t.Fatalf("after CREATE + OWNER replay, \"mysub\" not found; registry = %+v", rt2.PubSub.Subscriptions())
	}
	if sub.OID != wantOID {
		t.Errorf("after CREATE + OWNER replay, OID = %d, want %d (owner change must not disturb OID)", sub.OID, wantOID)
	}
	if sub.Owner != wantOwnerOID {
		t.Errorf("after CREATE + OWNER replay, Owner = %d, want %d", sub.Owner, wantOwnerOID)
	}
}

// TestReplayPubSubDDLRecordsHandlesMissingWalDir verifies the recovery hook
// is idempotent when invoked against a missing pg_wal directory (brand new
// initdb).
func TestReplayPubSubDDLRecordsHandlesMissingWalDir(t *testing.T) {
	pubsub := catalog.NewPubSub()
	if err := replayPubSubDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), pubsub); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(pubsub.Publications()) != 0 || len(pubsub.Subscriptions()) != 0 {
		t.Errorf("no-op replay should not register anything, got publications=%+v subscriptions=%+v", pubsub.Publications(), pubsub.Subscriptions())
	}
}

// TestReplayPubSubDDLRecordsHandlesNilPubSub verifies the recovery hook
// tolerates a nil PubSub (mirrors the nil-catalog guard the other DDL
// recovery drivers have for embedded test setups).
func TestReplayPubSubDDLRecordsHandlesNilPubSub(t *testing.T) {
	if err := replayPubSubDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), nil); err != nil {
		t.Fatalf("replay with nil pubsub: %v", err)
	}
}
