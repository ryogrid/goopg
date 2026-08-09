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
