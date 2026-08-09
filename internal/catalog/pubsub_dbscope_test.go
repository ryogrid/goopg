package catalog

import "testing"

// TestCreatePublicationCrossDatabaseIsolation verifies that CREATE PUBLICATION
// under one database does not collide with a same-named publication already
// created under a different database — the specific gap the DU-002 dump+restore
// round-trip probe (TestPort_PgDumpConnectionSetup) hit: restoring a dump into
// a fresh database re-issues `CREATE PUBLICATION pub`, which previously leaked
// across databases because every Publication shared one flat, dbOid-less
// registry. Mirrors TestCreateTSConfigCrossDatabaseIsolation.
// M0119-0004 (DU-002 per-DB publication scoping).
func TestCreatePublicationCrossDatabaseIsolation(t *testing.T) {
	ps := NewPubSub()
	const otherDB = uint32(99999)
	opts := DefaultPublicationOptions()

	// Create under DefaultDBOid.
	first, err := ps.CreatePublication("pub1", []string{"public.t1"}, opts, DefaultDBOid)
	if err != nil {
		t.Fatalf("CreatePublication under DefaultDBOid: %v", err)
	}

	// The exact same name under a distinct database must NOT collide.
	second, err := ps.CreatePublication("pub1", []string{"public.t2"}, opts, otherDB)
	if err != nil {
		t.Fatalf("CreatePublication under a distinct dbOid falsely collided: %v", err)
	}
	if first.OID == second.OID {
		t.Errorf("cross-database publications share OID %d, want distinct OIDs", first.OID)
	}

	// A genuine same-database duplicate still errors (regression guard).
	if _, err := ps.CreatePublication("pub1", nil, opts, DefaultDBOid); err == nil {
		t.Error("same-database duplicate CreatePublication should still error")
	}

	// LookupPublication is dbOid-scoped.
	if pub, ok := ps.LookupPublication("pub1", DefaultDBOid); !ok || pub.OID != first.OID {
		t.Errorf("LookupPublication under DefaultDBOid = (%v, %v), want (pub, ok)", pub, ok)
	}
	if pub, ok := ps.LookupPublication("pub1", otherDB); !ok || pub.OID != second.OID {
		t.Errorf("LookupPublication under otherDB = (%v, %v), want (pub, ok)", pub, ok)
	}

	// PublicationsForDBOid filters per-database.
	defaultPubs := ps.PublicationsForDBOid(DefaultDBOid)
	otherPubs := ps.PublicationsForDBOid(otherDB)
	if len(defaultPubs) != 1 {
		t.Errorf("PublicationsForDBOid(DefaultDBOid) = %d, want 1", len(defaultPubs))
	}
	if len(otherPubs) != 1 {
		t.Errorf("PublicationsForDBOid(otherDB) = %d, want 1", len(otherPubs))
	}

	// The legacy Publications() returns ALL pubs (backward compat).
	allPubs := ps.Publications()
	if len(allPubs) != 2 {
		t.Errorf("Publications() = %d, want 2 (both databases)", len(allPubs))
	}

	// Dropping under one database must not remove the other's row.
	if err := ps.DropPublication("pub1", DefaultDBOid); err != nil {
		t.Fatalf("DropPublication under DefaultDBOid: %v", err)
	}
	if pubs := ps.PublicationsForDBOid(otherDB); len(pubs) != 1 {
		t.Error("otherDB's pub1 vanished after dropping DefaultDBOid's own copy")
	}
	if pubs := ps.PublicationsForDBOid(DefaultDBOid); len(pubs) != 0 {
		t.Error("DefaultDBOid's pub1 still resolvable after DropPublication")
	}

	// Verify the remaining publication is the correct one.
	if pub, ok := ps.LookupPublication("pub1", otherDB); !ok || pub.OID != second.OID {
		t.Errorf("otherDB's pub1: got (%v, %v), want OID %d", pub, ok, second.OID)
	}
	if _, ok := ps.LookupPublication("pub1", DefaultDBOid); ok {
		t.Error("DefaultDBOid's pub1 still findable after drop")
	}
}

// TestCreateSubscriptionCrossDatabaseIsolation verifies that CREATE SUBSCRIPTION
// under one database does not collide with a same-named subscription already
// created under a different database — the specific gap the DU-002 dump+restore
// round-trip probe (TestPort_PgDumpConnectionSetup) hits after publication scoping:
// restoring a dump into a fresh database re-issues `CREATE SUBSCRIPTION sub`, which
// previously leaked across databases because every Subscription shared one flat,
// dbOid-less registry. Mirrors TestCreatePublicationCrossDatabaseIsolation.
// M0119-0004 (DU-002 per-DB subscription scoping).
func TestCreateSubscriptionCrossDatabaseIsolation(t *testing.T) {
	ps := NewPubSub()
	const otherDB = uint32(99999)

	// Create under DefaultDBOid.
	first, err := ps.CreateSubscriptionAsOwner("sub1", "host=db1", []string{"pub1"}, "", true, 10, DefaultDBOid)
	if err != nil {
		t.Fatalf("CreateSubscriptionAsOwner under DefaultDBOid: %v", err)
	}

	// The exact same name under a distinct database must NOT collide.
	second, err := ps.CreateSubscriptionAsOwner("sub1", "host=db2", []string{"pub2"}, "", false, 10, otherDB)
	if err != nil {
		t.Fatalf("CreateSubscriptionAsOwner under a distinct dbOid falsely collided: %v", err)
	}
	if first.OID == second.OID {
		t.Errorf("cross-database subscriptions share OID %d, want distinct OIDs", first.OID)
	}

	// A genuine same-database duplicate still errors (regression guard).
	if _, err := ps.CreateSubscriptionAsOwner("sub1", "host=db3", nil, "", true, 10, DefaultDBOid); err == nil {
		t.Error("same-database duplicate CreateSubscriptionAsOwner should still error")
	}

	// LookupSubscription is dbOid-scoped.
	if sub, ok := ps.LookupSubscription("sub1", DefaultDBOid); !ok || sub.OID != first.OID {
		t.Errorf("LookupSubscription under DefaultDBOid = (%v, %v), want (sub, ok)", sub, ok)
	}
	if sub, ok := ps.LookupSubscription("sub1", otherDB); !ok || sub.OID != second.OID {
		t.Errorf("LookupSubscription under otherDB = (%v, %v), want (sub, ok)", sub, ok)
	}

	// SubscriptionsForDBOid filters per-database.
	defaultSubs := ps.SubscriptionsForDBOid(DefaultDBOid)
	otherSubs := ps.SubscriptionsForDBOid(otherDB)
	if len(defaultSubs) != 1 {
		t.Errorf("SubscriptionsForDBOid(DefaultDBOid) = %d, want 1", len(defaultSubs))
	}
	if len(otherSubs) != 1 {
		t.Errorf("SubscriptionsForDBOid(otherDB) = %d, want 1", len(otherSubs))
	}

	// The legacy Subscriptions() returns ALL subs (backward compat).
	allSubs := ps.Subscriptions()
	if len(allSubs) != 2 {
		t.Errorf("Subscriptions() = %d, want 2 (both databases)", len(allSubs))
	}

	// Dropping under one database must not remove the other's row.
	if err := ps.DropSubscription("sub1", DefaultDBOid); err != nil {
		t.Fatalf("DropSubscription under DefaultDBOid: %v", err)
	}
	if subs := ps.SubscriptionsForDBOid(otherDB); len(subs) != 1 {
		t.Error("otherDB's sub1 vanished after dropping DefaultDBOid's own copy")
	}
	if subs := ps.SubscriptionsForDBOid(DefaultDBOid); len(subs) != 0 {
		t.Error("DefaultDBOid's sub1 still resolvable after DropSubscription")
	}

	// Verify the remaining subscription is the correct one.
	if sub, ok := ps.LookupSubscription("sub1", otherDB); !ok || sub.OID != second.OID {
		t.Errorf("otherDB's sub1: got (%v, %v), want OID %d", sub, ok, second.OID)
	}
	if _, ok := ps.LookupSubscription("sub1", DefaultDBOid); ok {
		t.Error("DefaultDBOid's sub1 still findable after drop")
	}

	// SetSubscriptionOwner is dbOid-scoped.
	if err := ps.SetSubscriptionOwner("sub1", 20, otherDB); err != nil {
		t.Fatalf("SetSubscriptionOwner under otherDB: %v", err)
	}
	if sub, _ := ps.LookupSubscription("sub1", otherDB); sub.Owner != 20 {
		t.Errorf("otherDB sub1 owner = %d, want 20", sub.Owner)
	}
}
