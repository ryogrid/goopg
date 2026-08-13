package initdb

import "testing"

// Guards for the M0133-S4 information_schema view OID policy. They mirror the
// M0131-S8a guards in system_view_oid_pins_test.go, adapted for the separate
// informationSchemaViewSeedRels list: the information_schema views are seeded
// to the on-disk catalogs but NOT into pg_internal.init, so they never appear
// in nailedSharedRels/nailedLocalRels.

// Every information_schema pinned view has a seeded pg_class row with the
// pinned OID and upstream's relnatts; and conversely every seeded
// information_schema view has a pin (a seeded row with no pin is an unowned
// in-band assignment).
func TestInformationSchemaViewOIDsMatchUpstreamPins(t *testing.T) {
	byName := map[string]nailedRel{}
	for _, r := range informationSchemaViewSeedRels() {
		if r.RelKind != 'v' {
			t.Errorf("%s: information_schema view seeded with relkind %q, want 'v'", r.RelName, r.RelKind)
		}
		byName[r.RelName] = r
	}
	if len(byName) == 0 {
		t.Fatal("no information_schema views seeded — guard is vacuous")
	}

	for _, pin := range informationSchemaViewOIDPins() {
		got, ok := byName[pin.ViewName]
		if !ok {
			t.Errorf("pinned view %q has no information_schema pg_class row", pin.ViewName)
			continue
		}
		if got.OID != pin.ViewOID {
			t.Errorf("%s: seeded OID %d, pinned (upstream PG 18.3 initdb) %d",
				pin.ViewName, got.OID, pin.ViewOID)
		}
		if int(got.RelNatts) != pin.RelNatts {
			t.Errorf("%s: seeded RelNatts %d, upstream relnatts %d",
				pin.ViewName, got.RelNatts, pin.RelNatts)
		}
		delete(byName, pin.ViewName)
	}
	for name, r := range byName {
		t.Errorf("seeded information_schema view %q (OID %d) has no entry in "+
			"informationSchemaViewOIDPins() — pin it against a real PG 18.3 initdb",
			name, r.OID)
	}
}

// The seeded _RETURN rule OIDs for the information_schema views equal upstream's
// rule OIDs, so a hosted PG's RewriteOidIndexId (2692) lookup agrees.
func TestInformationSchemaViewRewriteRulesMatchUpstreamPins(t *testing.T) {
	seeded := map[uint32]uint32{}
	for _, e := range informationSchemaViewRewriteEntries() {
		if e.RuleName != "_RETURN" {
			continue
		}
		seeded[e.EvClass] = e.OID
	}
	if len(seeded) != len(informationSchemaViewOIDPins()) {
		t.Fatalf("seeded %d information_schema _RETURN rules, pinned table has %d views",
			len(seeded), len(informationSchemaViewOIDPins()))
	}
	for _, pin := range informationSchemaViewOIDPins() {
		got, ok := seeded[pin.ViewOID]
		if !ok {
			t.Errorf("%s: no seeded _RETURN rule with ev_class=%d", pin.ViewName, pin.ViewOID)
			continue
		}
		if got != pin.RuleOID {
			t.Errorf("%s._RETURN: seeded rule OID %d, pinned (upstream) %d",
				pin.ViewName, got, pin.RuleOID)
		}
	}
}

// The information_schema pins share the initdb-assigned band with the pg_catalog
// pins; no OID may be claimed twice (within information_schema, or against
// systemViewOIDPins), or a hosted PG's syscache would resolve one of the two
// objects arbitrarily.
func TestInformationSchemaViewPinsAreDisjointAndInBand(t *testing.T) {
	seen := map[uint32]string{}
	claim := func(oid uint32, label string) {
		if prev, dup := seen[oid]; dup {
			t.Errorf("OID %d assigned to both %q and %q", oid, prev, label)
		}
		seen[oid] = label
		if oid < firstUnpinnedObjectID || oid >= firstNormalObjectID {
			t.Errorf("%s: OID %d outside the initdb-assigned band %d..%d",
				label, oid, firstUnpinnedObjectID, firstNormalObjectID-1)
		}
	}
	for _, pin := range systemViewOIDPins() {
		claim(pin.ViewOID, pin.ViewName)
		claim(pin.RuleOID, pin.ViewName+"._RETURN")
	}
	for _, pin := range informationSchemaViewOIDPins() {
		claim(pin.ViewOID, "information_schema."+pin.ViewName)
		claim(pin.RuleOID, "information_schema."+pin.ViewName+"._RETURN")
	}
}

// The information_schema views keep the same deliberate RelType divergence as
// the pg_catalog corpus: 2249 (RECORDOID), not upstream's per-view composite.
func TestInformationSchemaViewRelTypeDivergenceIsDeliberate(t *testing.T) {
	const recordOID = 2249
	for _, pin := range informationSchemaViewOIDPins() {
		var got *nailedRel
		for i := range informationSchemaViewSeedRels() {
			if informationSchemaViewSeedRels()[i].OID == pin.ViewOID {
				got = &informationSchemaViewSeedRels()[i]
				break
			}
		}
		if got == nil {
			t.Errorf("%s: no seeded row for pinned OID %d", pin.ViewName, pin.ViewOID)
			continue
		}
		if got.RelType != recordOID {
			t.Errorf("%s: RelType=%d, want RECORDOID %d (goopg's deliberate "+
				"divergence) or upstream's %d with a backing pg_type row",
				pin.ViewName, got.RelType, recordOID, pin.UpstreamRelType)
		}
	}
}
