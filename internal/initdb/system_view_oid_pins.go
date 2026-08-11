// System-view OID policy (M0131-S8a, 2026-08-11).
//
// DECISION: Option A — goopg PINS its nailed system-view OIDs (and their
// pg_rewrite rule OIDs) to the values PostgreSQL 18.3's own initdb assigns
// while executing `system_views.sql`. goopg no longer chooses its own
// values in the FirstUnpinnedObjectId..FirstNormalObjectId band
// (12000..16383, postgres/src/include/access/transam.h:194-196).
//
// WHY (full argument in docs/design/0131-0008-system-view-oid-policy.md):
// a captured `ev_action` is a serialised Query tree that names relations by
// OID, and `system_views.sql` is full of view-on-view chains (14 edges over
// its 80 views), so a dependent view's blob embeds its base view's
// initdb-assigned OID. Under Option A those embedded OIDs are already
// correct and the capture tool's acceptance test is a byte `cmp` against
// upstream's own output; under the alternative (keep goopg OIDs, rewrite
// relids in every blob) the corpus is permanently derived-not-captured and
// every future view inherits a tokenising rewrite pass over bytes a foreign
// engine casts straight to FormData_* structs.
//
// Pinning does not fight any allocator: goopg has no runtime allocator that
// can reach this band (catalog.FirstUserOID = 16384 floors every dynamically
// minted relation OID), so every in-band value is a hand-written constant in
// this table.
//
// THE COST, STATED: goopg's view OIDs are now a function of upstream's
// initdb execution order. It is deterministic for a fixed PG build (verified
// by two independent throwaway `initdb` runs at pinning time, byte-identical
// assignments), but a PG 18.4/19 `system_views.sql` change that adds or
// reorders an object shifts the whole band. That drift is DETECTED, not
// silent: systemViewOIDOracle* below stamps the oracle this table was
// captured from, and the guard test in system_view_oid_pins_test.go fails on
// a mismatch rather than silently re-pinning.

package initdb

// The oracle this table was captured from. A guard test compares this
// against the PG version the tree pins (config.PGVersionString /
// catalog-version constant); a mismatch is a hard failure whose fix is to
// re-capture, not to relax the check.
const (
	// systemViewOIDOracleVersion is the `SELECT version()` prefix of the
	// PG instance whose initdb produced the OIDs below.
	systemViewOIDOracleVersion = "PostgreSQL 18.3"
	// systemViewOIDOracleCatVersion is that instance's pg_controldata
	// "Catalog version number".
	systemViewOIDOracleCatVersion = 202506291
)

// The initdb-assigned OID band (transam.h:194-196) and goopg's reserved
// sub-band inside it.
const (
	firstUnpinnedObjectID uint32 = 12000
	firstNormalObjectID   uint32 = 16384

	// goopgOnlySystemViewOIDBase is where a goopg-only relation with no
	// upstream counterpart must be assigned from, so it can never collide
	// with an upstream initdb assignment this table has not yet pinned.
	// PG 18.3's initdb tops out at 13626 across every OID-bearing catalog
	// (max over pg_class/pg_type/pg_rewrite/pg_proc/pg_constraint/
	// pg_namespace in a fresh template), so 15000 leaves ~1.4k of headroom
	// for PG minor-version growth below it and ~1.4k of goopg space above.
	goopgOnlySystemViewOIDBase uint32 = 15000
)

// systemViewOIDPin is one row of the pinned table: what PG 18.3's initdb
// assigns to a `system_views.sql` view and to its ON-SELECT `_RETURN` rule.
//
// UpstreamRelType is recorded but NOT yet adopted — goopg's nailed rows carry
// RelType 2249 (RECORDOID) where PG mints a per-view composite pg_type. That
// divergence is M0131-S6.5's probe and is ledgered; this field exists so the
// guard test can assert goopg's deliberate divergence is still the *only* one
// and so S6.5/S9 have the target values already captured.
type systemViewOIDPin struct {
	ViewName        string
	ViewOID         uint32
	RuleOID         uint32
	UpstreamRelType uint32
	RelNatts        int
}

// systemViewOIDPins returns the pinned table for every nailed system view
// goopg hosts, in upstream OID order.
//
// Captured 2026-08-11 from a throwaway `initdb --no-sync` of PG 18.3
// (postgres/local_install/) via:
//
//	SELECT c.relname, c.oid, c.reltype, c.relnatts, r.oid
//	  FROM pg_class c JOIN pg_rewrite r ON r.ev_class = c.oid
//	 WHERE c.relname IN (…) ORDER BY c.oid;
//
// M0131-S7 replaces this hand-captured table with a generated manifest
// (internal/initdb/nailed_view_manifest.tsv) covering every captured view;
// the guard test then checks this table against the manifest. Until then
// this IS the manifest for the six hosted views.
func systemViewOIDPins() []systemViewOIDPin {
	return []systemViewOIDPin{
		{"pg_stat_replication", 12231, 12234, 12233, 20},
		{"pg_stat_wal_receiver", 12240, 12243, 12242, 15},
		{"pg_stat_recovery_prefetch", 12244, 12247, 12246, 10},
		{"pg_stat_subscription", 12248, 12251, 12250, 11},
		{"pg_replication_slots", 12261, 12264, 12263, 21},
		{"pg_stat_replication_slots", 12266, 12269, 12268, 10},
	}
}

// systemViewOIDPinByName looks up a pin by view name.
func systemViewOIDPinByName(name string) (systemViewOIDPin, bool) {
	for _, p := range systemViewOIDPins() {
		if p.ViewName == name {
			return p, true
		}
	}
	return systemViewOIDPin{}, false
}

// pinnedSystemViewOIDs returns the set of every OID this policy pins — view
// OIDs and rule OIDs together. Used by the blob invariant guard: any
// in-band `:relid` inside a committed *_ev_action.dat must be a member.
func pinnedSystemViewOIDs() map[uint32]string {
	out := make(map[uint32]string, 2*len(systemViewOIDPins()))
	for _, p := range systemViewOIDPins() {
		out[p.ViewOID] = p.ViewName
		out[p.RuleOID] = p.ViewName + "._RETURN"
	}
	return out
}
