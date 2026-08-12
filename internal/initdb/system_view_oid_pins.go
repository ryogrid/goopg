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
		// M0131-S9.1 (2026-08-11): the SRF-only tranche. Every one of these
		// views is `FROM <set-returning function>` with no catalog relation
		// and no view dependency, so its ev_action carries ZERO in-band
		// `:relid` — verified against the oracle before pinning, which is why
		// the whole tranche could be pinned in one pass without ordering it
		// by view-on-view edges. Same capture run as the six below.
		// M0131-S9.2a (2026-08-12): the head of the "views over real
		// catalogs" tranche — the pg_class family. Unlike everything below,
		// these are NOT SRF-only: each is a join over `pg_class` (1259) with
		// `pg_namespace` (2615) and, for two of them, `pg_tablespace` (1213),
		// so this is the first tranche whose ev_action carries RTE_JOIN.
		// Those base OIDs are all bootstrap constants BELOW the 12000 band,
		// so the blobs still carry zero in-band `:relid` and the tranche
		// needed no view-on-view ordering.
		//
		// NOT pinned: pg_indexes (12043). Its ev_action is 9002 B as a stored
		// varlena against the script's 8000 B inline budget — ceiling #1 of
		// design 0131-0009, and the FIRST view in the corpus to breach it.
		// The prerequisite is DECLARE_TOAST(pg_rewrite, 2838, 2839); ledgered.
		// M0131-S9.2b (2026-08-12): the shared-catalog tranche. pg_roles is
		// `FROM pg_authid` (1260) LEFT JOIN pg_db_role_setting (2964) and
		// pg_stat_activity joins pg_stat_get_activity(NULL) against pg_authid
		// (1260) AND pg_database (1262) — the first captured blobs that read
		// SHARED catalogs, and pg_stat_activity is the first that mixes an SRF
		// with catalog relations in one join tree. Every base OID is again a
		// sub-12000 bootstrap constant, so no in-band `:relid` and no
		// view-on-view ordering (measured, not assumed).
		{"pg_roles", 12000, 12003, 12002, 13},
		// M0131-S9.2c (2026-08-12): the authid family, and with it the corpus's
		// FIRST view-on-view edge. pg_shadow is catalog-direct (pg_authid 1260
		// LEFT JOIN pg_db_role_setting 2964), so it belongs to S9.2. pg_user is
		// `FROM pg_shadow` (system_views.sql:60-71) and its blob carries
		// `:relid 12005` — measured, the first in-band relid in the whole
		// corpus. Under the Option-A identity pinning of 0131-0008 that
		// embedded OID is already correct the moment pg_shadow is pinned above
		// it, which is exactly the property the policy was chosen for; capture
		// guard #4 enforces the base-before-dependent ordering rather than
		// trusting it, and a hosted PG evaluating pg_user is the acceptance
		// measurement.
		//
		// NOT pinned: pg_group (12010, rule 12013, reltype 12012, 3 atts —
		// captured and under every ceiling at 1428 B stored). Its
		// `ARRAY(SELECT member FROM pg_auth_members …)` target entry makes it
		// the corpus's first blob with an ARRAY(SubLink), and a hosted PG
		// rejects it with "could not find array type for data type oid":
		// get_array_type (lsyscache.c) reads pg_type.typarray for OIDOID and
		// goopg seeds that column as a literal 0 for EVERY row
		// (pg_type_bootstrap.go:306), even though the _oid row (1028) itself
		// exists in pg_type_seed_data.go. That is a pg_type bootstrap gap, not
		// a capture gap — the same class as pg_timezone_abbrevs' missing
		// pg_amop row. Ledgered; the resume point is populating typarray (and
		// typelem) from pg_type.dat.
		{"pg_shadow", 12005, 12008, 12007, 9},
		{"pg_user", 12014, 12017, 12016, 9},
		// M0131-S9.2d (2026-08-12): the rest of S9.2's catalog-direct,
		// under-ceiling tranche, captured in one pass. Every S9.2d view is a
		// join over ordinary catalogs (and, for the pg_stat_database pair,
		// over pg_stat_get_db_* SRFs) with NO view-on-view edge — measured
		// before pinning, not assumed: none of their ev_action blobs carries a
		// `:relid` in the 12000..16383 band, so capture guard #4 has nothing
		// to order here. They are interleaved by upstream OID below:
		// pg_rules 12023, pg_sequences 12048, pg_prepared_xacts 12090,
		// pg_stat_database 12270, pg_stat_database_conflicts 12275 and
		// pg_user_mappings 12338.
		//
		// NOT pinned, both captured and both under every size ceiling —
		// ceilings #4 and #5, and like #1..#3 both are gaps in what goopg
		// BOOTSTRAPS, not in the capture tooling:
		//
		//   pg_policies (12018, 12021, reltype 12020, 8 atts). A hosted PG
		//   fails it with "could not open relation with OID 3256" — pg_policy
		//   is not an on-disk relation in a goopg cluster at all, so this is
		//   the first captured blob whose base CATALOG is missing rather than
		//   its type or operator metadata. (Its `roles` column is also the
		//   corpus's first `name[]`, which is why 1003 is now canonical in
		//   pg_type_bootstrap.go.)
		//
		//   pg_publication_tables (12068, 12071, reltype 12070, 5 atts). A
		//   hosted PG fails it with "target type is not an array", raised by
		//   ExecInitExprRec's T_ArrayCoerceExpr arm (execExpr.c:1684-1688)
		//   when get_element_type() finds pg_type.typelem = 0. goopg seeds
		//   typelem as a literal 0 for EVERY row (pg_type_bootstrap.go
		//   pgTypeRow, column 14) — the exact twin of the typarray gap that
		//   blocks pg_group (F11), one column over in the same literal row.
		//   Populating typelem and typarray from pg_type.dat unblocks both.
		{"pg_rules", 12023, 12026, 12025, 4},
		{"pg_views", 12028, 12031, 12030, 4},
		{"pg_tables", 12033, 12036, 12035, 8},
		{"pg_matviews", 12038, 12041, 12040, 7},
		{"pg_sequences", 12048, 12051, 12050, 11},
		{"pg_locks", 12073, 12076, 12075, 16},
		{"pg_cursors", 12077, 12080, 12079, 6},
		{"pg_prepared_xacts", 12090, 12093, 12092, 5},
		{"pg_prepared_statements", 12095, 12098, 12097, 8},
		{"pg_settings", 12104, 12107, 12106, 17},
		{"pg_file_settings", 12110, 12113, 12112, 7},
		{"pg_hba_file_rules", 12114, 12117, 12116, 11},
		{"pg_ident_file_mappings", 12118, 12121, 12120, 7},
		// NOT pinned: pg_timezone_abbrevs (12122). Its ev_action is the only
		// blob in the tranche carrying a SortGroupClause (`ORDER BY abbrev`),
		// and a hosted PG 18.3 rejects it with "operator 664 is not a valid
		// ordering operator" — get_ordering_op_properties (lsyscache.c) scans
		// pg_amop for (664, btree, strategy 1) and goopg's on-disk pg_amop
		// does not carry text_lt's row. That is a catalog gap, not a capture
		// gap; ledgered, and the resume point is bootstrapping pg_amop.
		{"pg_timezone_names", 12126, 12129, 12128, 4},
		{"pg_config", 12130, 12133, 12132, 2},
		{"pg_shmem_allocations", 12134, 12137, 12136, 4},
		{"pg_shmem_allocations_numa", 12138, 12141, 12140, 3},
		{"pg_backend_memory_contexts", 12142, 12145, 12144, 10},
		// M0131-S9.2b, continued (see the pg_roles comment above).
		// M0131-S9.3a (2026-08-12): the FIRST view-on-view tranche at scale —
		// the per-table statistics family. Unlike S9.2c's single pg_shadow →
		// pg_user edge, this tranche pins two BASES together with their four
		// dependents in one capture run, so capture guard #4's
		// base-before-dependent ordering is exercised by four edges at once.
		// Measured against the oracle before pinning (stored ev_action size /
		// in-band :relid set):
		//
		//   pg_stat_all_tables        12146  5473 B  (no in-band relid)
		//   pg_stat_sys_tables        12156  2476 B  :relid 12146
		//   pg_stat_user_tables       12165  2478 B  :relid 12146
		//   pg_stat_xact_all_tables   12151  5057 B  (no in-band relid)
		//   pg_stat_xact_sys_tables   12161  1822 B  :relid 12151
		//   pg_stat_xact_user_tables  12170  1824 B  :relid 12151
		//
		// The dependents are an order of magnitude smaller than their bases
		// because a `FROM pg_stat_all_tables WHERE …` Query stores the base as
		// one RTE_RELATION reference rather than re-expanding its 30-column
		// SRF join — which is precisely the property Option-A pinning buys
		// (0131-0008): the embedded 12146/12151 are already correct.
		//
		// NOT pinned in this tranche: the pg_statio_*_tables triple. Its base
		// pg_statio_all_tables (12174) stores at 10475 B, over the 8000 B
		// inline budget — ceiling #1 (pg_rewrite TOAST 2838/2839) again, and
		// under guard #4 a dependent cannot be pinned before its base, so
		// pg_statio_{sys,user}_tables wait on that same TOAST work. Ledgered.
		{"pg_stat_all_tables", 12146, 12149, 12148, 30},
		{"pg_stat_xact_all_tables", 12151, 12154, 12153, 12},
		{"pg_stat_sys_tables", 12156, 12159, 12158, 30},
		{"pg_stat_xact_sys_tables", 12161, 12164, 12163, 12},
		{"pg_stat_user_tables", 12165, 12168, 12167, 30},
		{"pg_stat_xact_user_tables", 12170, 12173, 12172, 12},
		{"pg_stat_activity", 12226, 12229, 12228, 22},
		// The six M0131-S8a views, interleaved by upstream OID.
		{"pg_stat_replication", 12231, 12234, 12233, 20},
		{"pg_stat_slru", 12236, 12239, 12238, 9},
		{"pg_stat_wal_receiver", 12240, 12243, 12242, 15},
		{"pg_stat_recovery_prefetch", 12244, 12247, 12246, 10},
		{"pg_stat_subscription", 12248, 12251, 12250, 11},
		{"pg_stat_ssl", 12253, 12256, 12255, 8},
		{"pg_stat_gssapi", 12257, 12260, 12259, 5},
		{"pg_replication_slots", 12261, 12264, 12263, 21},
		{"pg_stat_replication_slots", 12266, 12269, 12268, 10},
		// S9.2d. pg_stat_database is the corpus's first blob with a set
		// operation: system_views.sql:1006-1010 selects from
		// `(SELECT 0 AS oid, NULL::name AS datname UNION ALL SELECT oid,
		// datname FROM pg_database)`, so its Query carries an RTE_SUBQUERY
		// whose own Query is a SetOperationStmt — a shape no earlier capture
		// exercised.
		{"pg_stat_database", 12270, 12273, 12272, 30},
		{"pg_stat_database_conflicts", 12275, 12278, 12277, 8},
		{"pg_stat_archiver", 12289, 12292, 12291, 7},
		// M0131-S9.1b (2026-08-11): the RTE_RESULT pair. Both views are a bare
		// `SELECT <srf>() AS …` with NO FROM clause at all (system_views.sql:
		// 1150-1169), so their Query carries a single RTE_RESULT range-table
		// entry — the fifth RTE kind, which no other captured blob exercises
		// (0131-0009 §"Two unmeasured ev_action shapes"). Held back from the
		// S9.1 tranche precisely so that shape lands with a two-view blast
		// radius; captured in the same identity-pinned way as everything else.
		{"pg_stat_bgwriter", 12293, 12296, 12295, 4},
		{"pg_stat_checkpointer", 12297, 12300, 12299, 11},
		{"pg_stat_io", 12301, 12304, 12303, 20},
		{"pg_stat_wal", 12305, 12308, 12307, 5},
		{"pg_stat_progress_basebackup", 12329, 12332, 12331, 6},
		{"pg_user_mappings", 12338, 12341, 12340, 6},
		{"pg_replication_origin_status", 12343, 12346, 12345, 4},
		{"pg_wait_events", 12351, 12354, 12353, 3},
		{"pg_aios", 12355, 12358, 12357, 15},
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
