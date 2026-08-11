package initdb

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/config"
)

// Guards for the M0131-S8a system-view OID policy
// (docs/design/0131-0008-system-view-oid-policy.md §Guards). The policy is
// Option A: goopg pins its nailed system-view OIDs and their pg_rewrite rule
// OIDs to PG 18.3's own initdb assignment. These tests are the enforcement —
// they hold the pinned table, the production constants, and the committed
// ev_action blobs to one another so a future capture cannot silently
// reintroduce a goopg-private assignment.

// Guard 1: every relkind='v' nailed relation's OID equals the pinned value
// for that view name, and its RelNatts agrees with upstream's relnatts.
//
// Also asserts the converse — every pinned view has a nailed row — so
// deleting a nailed entry cannot make this guard vacuous.
func TestNailedViewOIDsMatchUpstreamPins(t *testing.T) {
	byName := map[string]nailedRel{}
	for _, group := range [][]nailedRel{nailedSharedRels, nailedLocalRels} {
		for _, r := range group {
			if r.RelKind != 'v' {
				continue
			}
			byName[r.RelName] = r
		}
	}
	if len(byName) == 0 {
		t.Fatal("no relkind='v' nailed relations found — guard is vacuous")
	}

	for _, pin := range systemViewOIDPins() {
		got, ok := byName[pin.ViewName]
		if !ok {
			t.Errorf("pinned view %q has no nailed pg_class row", pin.ViewName)
			continue
		}
		if got.OID != pin.ViewOID {
			t.Errorf("%s: nailed OID %d, pinned (upstream PG 18.3 initdb) %d",
				pin.ViewName, got.OID, pin.ViewOID)
		}
		if int(got.RelNatts) != pin.RelNatts {
			t.Errorf("%s: nailed RelNatts %d, upstream relnatts %d",
				pin.ViewName, got.RelNatts, pin.RelNatts)
		}
		delete(byName, pin.ViewName)
	}
	// A nailed view with no pin is an unpinned in-band assignment: exactly
	// what the policy forbids (its OID may collide with an upstream object
	// whose OID a future captured blob embeds).
	for name, r := range byName {
		t.Errorf("nailed view %q (OID %d) has no entry in systemViewOIDPins() — "+
			"pin it against a real PG 18.3 initdb or move it above "+
			"goopgOnlySystemViewOIDBase (%d)", name, r.OID, goopgOnlySystemViewOIDBase)
	}
}

// Guard 2: the pg_rewrite _RETURN rule OIDs the bootstrap seeds equal
// upstream's rule OIDs. A hosted PG resolves rules through
// RewriteOidIndexId (2692), so a goopg-private rule OID would make the
// index's keys disagree with upstream's notion of the same rule.
func TestPgRewriteRuleOIDsMatchUpstreamPins(t *testing.T) {
	// ev_class → rule OID, as actually seeded.
	seeded := map[uint32]uint32{}
	for _, e := range pgRewriteInitialEntries() {
		if e.RuleName != "_RETURN" {
			continue
		}
		seeded[e.EvClass] = e.OID
	}
	if len(seeded) != len(systemViewOIDPins()) {
		t.Fatalf("seeded %d _RETURN rules, pinned table has %d views",
			len(seeded), len(systemViewOIDPins()))
	}
	for _, pin := range systemViewOIDPins() {
		got, ok := seeded[pin.ViewOID]
		if !ok {
			t.Errorf("%s: no seeded _RETURN rule with ev_class=%d",
				pin.ViewName, pin.ViewOID)
			continue
		}
		if got != pin.RuleOID {
			t.Errorf("%s._RETURN: seeded rule OID %d, pinned (upstream) %d",
				pin.ViewName, got, pin.RuleOID)
		}
	}
}

var evActionRelidRE = regexp.MustCompile(`:relid (\d+)`)

// Guard 3: no unmapped in-band relid survives in any committed ev_action
// blob. A captured `ev_action` names relations by OID; an embedded value in
// 12000..16383 that is not one of goopg's pinned view OIDs means a hosted PG
// would try to open a relation this cluster does not have.
//
// This is the guard that was FAILING before S8a landed:
// pg_stat_replication_slots_ev_action.dat embeds `:relid 12261` twice (its
// base view pg_replication_slots, which goopg used to call 12105). Pinning
// pg_replication_slots to 12261 fixed it without touching the blob.
func TestEvActionBlobsCarryNoUnmappedInBandRelid(t *testing.T) {
	dats, err := filepath.Glob("*_ev_action.dat")
	if err != nil {
		t.Fatal(err)
	}
	if len(dats) == 0 {
		t.Fatal("no *_ev_action.dat files found — guard is vacuous")
	}
	pinned := pinnedSystemViewOIDs()
	checked := 0
	for _, dat := range dats {
		body, err := os.ReadFile(dat)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range evActionRelidRE.FindAllStringSubmatch(string(body), -1) {
			relid, err := strconv.ParseUint(m[1], 10, 32)
			if err != nil {
				t.Fatalf("%s: unparsable relid %q: %v", dat, m[1], err)
			}
			checked++
			oid := uint32(relid)
			if oid < firstUnpinnedObjectID {
				continue // a pinned catalog OID (e.g. 1260 pg_authid) — fine
			}
			if oid >= firstNormalObjectID {
				t.Errorf("%s: :relid %d is a user-OID-range value in a "+
					"bootstrap blob", dat, oid)
				continue
			}
			if name, ok := pinned[oid]; !ok {
				t.Errorf("%s: :relid %d is in the initdb-assigned band "+
					"(%d..%d) but is not a pinned goopg system-view OID — a "+
					"hosted PG would open a relation that does not exist",
					dat, oid, firstUnpinnedObjectID, firstNormalObjectID-1)
			} else if name == "" {
				t.Errorf("%s: :relid %d maps to an empty name", dat, oid)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no :relid found in any blob — guard is vacuous")
	}
}

// Guard 4: disjointness. No two pins share an OID (view-vs-view,
// rule-vs-rule, or view-vs-rule), every pin lies inside the initdb-assigned
// band, and goopg's reserved sub-band starts below FirstUserOID so a pinned
// OID can never be mistaken for a dynamically minted one.
func TestSystemViewOIDPinsAreDisjointAndInBand(t *testing.T) {
	seen := map[uint32]string{}
	for _, pin := range systemViewOIDPins() {
		for _, e := range []struct {
			oid   uint32
			label string
		}{
			{pin.ViewOID, pin.ViewName},
			{pin.RuleOID, pin.ViewName + "._RETURN"},
		} {
			if prev, dup := seen[e.oid]; dup {
				t.Errorf("OID %d assigned to both %q and %q", e.oid, prev, e.label)
			}
			seen[e.oid] = e.label
			if e.oid < firstUnpinnedObjectID || e.oid >= firstNormalObjectID {
				t.Errorf("%s: OID %d outside the initdb-assigned band %d..%d",
					e.label, e.oid, firstUnpinnedObjectID, firstNormalObjectID-1)
			}
		}
	}
	if goopgOnlySystemViewOIDBase <= firstUnpinnedObjectID ||
		goopgOnlySystemViewOIDBase >= firstNormalObjectID {
		t.Errorf("goopgOnlySystemViewOIDBase=%d must lie inside %d..%d",
			goopgOnlySystemViewOIDBase, firstUnpinnedObjectID, firstNormalObjectID-1)
	}
	if uint32(catalog.FirstUserOID) != firstNormalObjectID {
		t.Errorf("catalog.FirstUserOID=%d but PG FirstNormalObjectId=%d — the "+
			"policy's premise (no runtime allocator reaches the pinned band) "+
			"no longer holds", catalog.FirstUserOID, firstNormalObjectID)
	}
	// Every pin must sit BELOW the goopg-only sub-band: these are
	// upstream-derived, not goopg inventions.
	for _, pin := range systemViewOIDPins() {
		if pin.ViewOID >= goopgOnlySystemViewOIDBase {
			t.Errorf("%s: upstream-pinned OID %d is inside goopg's reserved "+
				"sub-band (>=%d)", pin.ViewName, pin.ViewOID, goopgOnlySystemViewOIDBase)
		}
	}
}

// Guard 5: version stamp. The pinned table is a function of the oracle's
// initdb execution order, so it is only valid for the PG version this tree
// pins. A PG bump must re-capture the table, not inherit it silently.
func TestSystemViewOIDPinsOracleStampMatchesTree(t *testing.T) {
	// The tree's pinned PG version is the server_version GUC's BootVal —
	// the same string a client sees in ParameterStatus.
	sv, ok := config.BuildDefaultRegistry().Get("server_version")
	if !ok {
		t.Fatal("server_version GUC missing from the default registry")
	}
	if want := "PostgreSQL " + sv.BootVal; systemViewOIDOracleVersion != want {
		t.Errorf("pinned table captured from %q but the tree pins %q — "+
			"re-capture systemViewOIDPins() against the new oracle "+
			"(docs/design/0131-0008-system-view-oid-policy.md §Guards 5)",
			systemViewOIDOracleVersion, want)
	}
	if systemViewOIDOracleCatVersion != config.CatalogVersionNo {
		t.Errorf("pinned table captured from catversion %d but the tree pins "+
			"%d — re-capture systemViewOIDPins()",
			systemViewOIDOracleCatVersion, config.CatalogVersionNo)
	}
}

// The pinned table must agree with the production constants the bootstrap
// actually uses. Without this, the table could be correct while the
// constants drifted.
func TestSystemViewOIDPinConstantsAgree(t *testing.T) {
	cases := []struct {
		name    string
		viewOID uint32
		ruleOID uint32
	}{
		{"pg_stat_wal_receiver", pgStatWalReceiverViewOID, pgRewriteOIDPgStatWalReceiverReturn},
		{"pg_stat_replication", pgStatReplicationViewOID, pgRewriteOIDPgStatReplicationReturn},
		{"pg_stat_recovery_prefetch", pgStatRecoveryPrefetchViewOID, pgRewriteOIDPgStatRecoveryPrefetchReturn},
		{"pg_stat_subscription", pgStatSubscriptionViewOID, pgRewriteOIDPgStatSubscriptionReturn},
		{"pg_replication_slots", pgReplicationSlotsViewOID, pgRewriteOIDPgReplicationSlotsReturn},
		{"pg_stat_replication_slots", pgStatReplicationSlotsViewOID, pgRewriteOIDPgStatReplicationSlotsReturn},
	}
	if len(cases) != len(systemViewOIDPins()) {
		t.Fatalf("%d constant pairs vs %d pins — a view was added to one side only",
			len(cases), len(systemViewOIDPins()))
	}
	for _, tc := range cases {
		pin, ok := systemViewOIDPinByName(tc.name)
		if !ok {
			t.Errorf("%s: no pin", tc.name)
			continue
		}
		if tc.viewOID != pin.ViewOID {
			t.Errorf("%s: view-OID constant %d, pin %d", tc.name, tc.viewOID, pin.ViewOID)
		}
		if tc.ruleOID != pin.RuleOID {
			t.Errorf("%s: rule-OID constant %d, pin %d", tc.name, tc.ruleOID, pin.RuleOID)
		}
	}
}

// The deliberate divergence, asserted as such: goopg's nailed views carry
// RelType 2249 (RECORDOID) where upstream mints a per-view composite
// pg_type. This test pins that it is still the ONLY divergence from the
// captured upstream shape, and fails loudly if a nailed view starts
// claiming upstream's composite OID without the pg_type row to back it
// (M0131-S6.5 / ledger).
func TestNailedViewRelTypeDivergenceIsDeliberate(t *testing.T) {
	const recordOID = 2249
	for _, pin := range systemViewOIDPins() {
		var got *nailedRel
		for i := range nailedLocalRels {
			if nailedLocalRels[i].OID == pin.ViewOID {
				got = &nailedLocalRels[i]
				break
			}
		}
		if got == nil {
			t.Errorf("%s: no nailed row for pinned OID %d", pin.ViewName, pin.ViewOID)
			continue
		}
		if got.RelType == pin.UpstreamRelType {
			t.Errorf("%s: RelType now claims upstream's composite type %d — "+
				"that requires a real pg_type row (M0131-S6.5); if it landed, "+
				"update this guard", pin.ViewName, pin.UpstreamRelType)
			continue
		}
		if got.RelType != recordOID {
			t.Errorf("%s: RelType=%d, want RECORDOID %d (goopg's deliberate "+
				"divergence) or upstream's %d with a backing pg_type row",
				pin.ViewName, got.RelType, recordOID, pin.UpstreamRelType)
		}
	}
}
