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
		// pg_indexes (12043) was ceiling #1 of design 0131-0009 — the FIRST
		// view in the corpus whose stored ev_action (9002 B) breached the
		// 8000 B inline-heap-tuple budget. M0131-S20.1 bootstrapped
		// DECLARE_TOAST(pg_rewrite, 2838, 2839), S20.2a wrote the chunk
		// writer, and S20.2b (2026-08-12) relaxed capture guard #5 to
		// "inline OR toastable" and captured it: its value is now five
		// 1996-byte chunks in base/{1,5}/2838 under chunk_id 12047, behind an
		// 18-byte VARTAG_ONDISK pointer in the pg_rewrite tuple. Pinned below
		// at its upstream OID position.
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
		// pg_group (12010, rule 12013, reltype 12012, 3 atts) was captured but
		// withheld by S9.2c as ceiling #3: its `ARRAY(SELECT member FROM
		// pg_auth_members …)` target entry is the corpus's first ARRAY(SubLink),
		// and a hosted PG rejected it with "could not find array type for data
		// type oid" because get_array_type (lsyscache.c) reads pg_type.typarray
		// for OIDOID and goopg seeded that column as a literal 0 for EVERY row.
		// M0131-S9.3c populates typelem/typarray from the PG 18.3 catalog
		// (pg_type_bootstrap.go pgTypeElemArray), so oid (26) now reports
		// typarray = 1028 and the ceiling is gone.
		{"pg_shadow", 12005, 12008, 12007, 9},
		{"pg_group", 12010, 12013, 12012, 3},
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
		// ceiling #4 is GONE. pg_policies (12018) was captured by S9.2d and
		// withheld because a hosted PG failed it with "could not open
		// relation with OID 3256" — pg_policy was not an on-disk relation in
		// a goopg cluster at all, the corpus's only blob blocked by a missing
		// base CATALOG rather than by its type or operator metadata.
		// M0131-S9.3e bootstraps pg_policy as an empty nailed heap
		// (relcache_init.go, mappedLocalCatalogPlaceholderOIDs) and pins the
		// view below. (Its `roles` column is also the corpus's first
		// `name[]`, which is why 1003 is canonical in pg_type_bootstrap.go.)
		//
		// (pg_publication_tables, ceiling #5, was the twin of pg_group's #3 —
		// "target type is not an array" from ExecInitExprRec's
		// T_ArrayCoerceExpr arm (execExpr.c:1684-1688) when get_element_type()
		// found pg_type.typelem = 0, one column over from the typarray literal.
		// M0131-S9.3c closed both with one population and pins it below.)
		{"pg_policies", 12018, 12021, 12020, 8},
		{"pg_rules", 12023, 12026, 12025, 4},
		{"pg_views", 12028, 12031, 12030, 4},
		{"pg_tables", 12033, 12036, 12035, 8},
		{"pg_matviews", 12038, 12041, 12040, 7},
		{"pg_indexes", 12043, 12046, 12045, 5},
		{"pg_sequences", 12048, 12051, 12050, 11},
		// M0131-S20.2b (2026-08-12): the OUT-OF-LINE tranche — every view
		// ceiling #1 (pg_rewrite TOAST 2838/2839) had been withholding since
		// S9.2a. Their ev_action does not fit an inline heap tuple, so each is
		// seeded as an 18-byte VARTAG_ONDISK pointer plus 1996-byte chunks in
		// base/{1,5}/2838 (chunk_id = rule OID + 1, F22). Re-measured against
		// the oracle in this capture run (stored ev_action / chunks):
		//
		//   pg_indexes            12043   9002 B   5   (pinned above)
		//   pg_stats              12053   9316 B   5
		//   pg_statio_all_tables  12174  10475 B   6   (+ 2 dependents)
		//   pg_stats_ext          12058  12196 B   7
		//
		// (Those are UPSTREAM's chunk counts. goopg's own pglz compresses each
		// value 3-4% smaller, so its heap holds 5/5/6/6 chunks — a byte
		// divergence in the TOAST heap that the detoasted value does not
		// carry. Ledgered, with the measured numbers in
		// assertHostedPGSeesPgRewriteToastRelation.)
		//
		// TWO of the six over-budget values still stay out, and NEITHER is
		// blocked by size any more — both toast cleanly and both were measured
		// against a hosted PG in this slice rather than assumed:
		//
		//   pg_seclabels (12099, 35379 B, 18 chunks) reads pg_seclabel (3596)
		//   and pg_largeobject_metadata (2995), neither of which is an on-disk
		//   relation in a goopg cluster: "could not open relation with OID
		//   3596" — the same class of blocker as pg_policies' pg_policy
		//   (3256), i.e. ceiling #4.
		//
		//   pg_stats_ext_exprs (12063, 11481 B) needs pg_type 10029, the
		//   COMPOSITE rowtype of pg_statistic. goopg seeds the array type
		//   10028 (_pg_statistic) and points its typelem at 10029, but no
		//   pg_type row for 10029 exists, so a hosted PG fails with "type with
		//   OID 10029 does not exist" and then trips
		//   Assert("OidIsValid(typentry->typrelid)") (typcache.c:3082) during
		//   transaction abort. This is a NEW ceiling — #6 — and the first one
		//   that is about a catalog's own rowtype rather than a missing
		//   relation or an unpopulated column. Ledgered.
		{"pg_stats", 12053, 12056, 12055, 17},
		{"pg_stats_ext", 12058, 12061, 12060, 15},
		{"pg_publication_tables", 12068, 12071, 12070, 5},
		{"pg_locks", 12073, 12076, 12075, 16},
		{"pg_cursors", 12077, 12080, 12079, 6},
		// M0131-S9.3d (2026-08-12): the REMAINDER tranche. After S9.3b closed
		// the statistics families the corpus was 60 views, and re-asking the
		// oracle for every pg_catalog view NOT in this table (oid, rule oid,
		// stored ev_action size, in-band :relid set) showed twenty left, of
		// which eleven are under the 8000 B inline budget with no in-band
		// relid and no known blocker — they were never withheld for a measured
		// reason, they simply were not in an earlier tranche's subject family.
		// This tranche takes all eleven in one capture run; the four that stay
		// out are named at their OID positions below.
		//
		// pg_available_extensions / _versions read the pg_available_extension*
		// SRFs (system_views.sql:672-693), i.e. ordinary S9.1-shaped SRF-only
		// views that S9.1's "pg_stat_get_* family" subject line skipped over.
		{"pg_available_extensions", 12081, 12084, 12083, 4},
		{"pg_available_extension_versions", 12085, 12088, 12087, 9},
		{"pg_prepared_xacts", 12090, 12093, 12092, 5},
		{"pg_prepared_statements", 12095, 12098, 12097, 8},
		{"pg_settings", 12104, 12107, 12106, 17},
		{"pg_file_settings", 12110, 12113, 12112, 7},
		{"pg_hba_file_rules", 12114, 12117, 12116, 11},
		{"pg_ident_file_mappings", 12118, 12121, 12120, 7},
		// M0131-S9.3d: pg_timezone_abbrevs (12122) was S9.1's ceiling #2 — the
		// only blob in the corpus carrying a SortGroupClause (`ORDER BY
		// abbrev`), rejected by a hosted PG with "operator 664 is not a valid
		// ordering operator" because get_ordering_op_properties (lsyscache.c)
		// looks (664 = text_lt, btree, strategy 1) up through the EMPTY
		// pg_amop_fam_strat_index (2653). M0131-S12 bulk-loaded 2653 and 2654
		// (bootstrapPgAmopFamStratIndex / bootstrapPgAmopOprFamIndex) while
		// fixing the sort blocker one catalog over, and its own note records
		// that the two were the SAME bug — so this pin is the re-measurement
		// that ceiling #2 is gone, carried by the hosted-PG probe set.
		{"pg_timezone_abbrevs", 12122, 12125, 12124, 3},
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
		// NOT pinned in THIS tranche: the pg_statio_*_tables triple. Its base
		// pg_statio_all_tables (12174) stores at 10475 B, over the 8000 B
		// inline budget — ceiling #1 (pg_rewrite TOAST 2838/2839) again, and
		// under guard #4 a dependent cannot be pinned before its base, so
		// pg_statio_{sys,user}_tables waited on that same TOAST work.
		// M0131-S20.2b pins all three below: the base goes out of line in six
		// chunks while its two dependents (1756 B / 1759 B) stay INLINE — F14
		// again, and the reason the S20.1 filing's "eight oversize values" was
		// an overcount. It is also the first place the corpus mixes an
		// external base with inline dependents across one view-on-view edge.
		{"pg_stat_all_tables", 12146, 12149, 12148, 30},
		{"pg_stat_xact_all_tables", 12151, 12154, 12153, 12},
		{"pg_stat_sys_tables", 12156, 12159, 12158, 30},
		{"pg_stat_xact_sys_tables", 12161, 12164, 12163, 12},
		{"pg_stat_user_tables", 12165, 12168, 12167, 30},
		{"pg_stat_xact_user_tables", 12170, 12173, 12172, 12},
		{"pg_statio_all_tables", 12174, 12177, 12176, 11},
		{"pg_statio_sys_tables", 12179, 12182, 12181, 11},
		{"pg_statio_user_tables", 12183, 12186, 12185, 11},
		// M0131-S9.3b (2026-08-12): the rest of S9.3's reachable population —
		// the per-index and per-sequence statistics families. Three more
		// bases with two dependents each, so this tranche adds SIX view-on-
		// view edges in one capture run (the corpus goes 4 -> 10 edges over
		// 2 -> 5 bases). Measured against the oracle before pinning (stored
		// ev_action size / in-band :relid set):
		//
		//   pg_stat_all_indexes      12187  6826 B  (no in-band relid)
		//   pg_stat_sys_indexes      12192  1714 B  :relid 12187
		//   pg_stat_user_indexes     12196  1716 B  :relid 12187
		//   pg_statio_all_indexes    12200  6799 B  (no in-band relid)
		//   pg_statio_sys_indexes    12205  1625 B  :relid 12200
		//   pg_statio_user_indexes   12209  1628 B  :relid 12200
		//   pg_statio_all_sequences  12213  2431 B  (no in-band relid)
		//   pg_statio_sys_sequences  12218  1559 B  :relid 12213
		//   pg_statio_user_sequences 12222  1561 B  :relid 12213
		//
		// F14 (0131-0009) holds again at 3-for-3: every dependent stores at
		// roughly a quarter of its base, because the rewritten Query names
		// the base as ONE RTE_RELATION instead of re-expanding its SRF join.
		// The two index bases sit at 6.8 kB — the closest any captured blob
		// has come to the 8000 B inline budget without breaching it — while
		// their dependents are nowhere near it, which is why ceiling #1 can
		// only ever bite a base.
		//
		// Guard #4 (base before dependent) orders each triple; the bases are
		// listed first here for the same reason.
		{"pg_stat_all_indexes", 12187, 12190, 12189, 9},
		{"pg_statio_all_indexes", 12200, 12203, 12202, 7},
		{"pg_statio_all_sequences", 12213, 12216, 12215, 5},
		{"pg_stat_sys_indexes", 12192, 12195, 12194, 9},
		{"pg_stat_user_indexes", 12196, 12199, 12198, 9},
		{"pg_statio_sys_indexes", 12205, 12208, 12207, 7},
		{"pg_statio_user_indexes", 12209, 12212, 12211, 7},
		{"pg_statio_sys_sequences", 12218, 12221, 12220, 5},
		{"pg_statio_user_sequences", 12222, 12225, 12224, 5},
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
		// M0131-S9.3d, continued: the per-function statistics pair
		// (system_views.sql:1049-1070), pg_stat_get_function_* SRFs joined
		// against pg_proc (1255) and pg_namespace (2615) — catalog-direct,
		// no view-on-view edge, both under 2.5 kB.
		{"pg_stat_user_functions", 12279, 12282, 12281, 6},
		{"pg_stat_xact_user_functions", 12284, 12287, 12286, 6},
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
		// M0131-S9.3d, continued: the rest of the command-progress family.
		// pg_stat_progress_basebackup below was pinned by S9.1 because it is
		// the one member with no catalog join; the other five each join
		// pg_stat_get_progress_info('<cmd>') against pg_database (1262) and,
		// for four of them, pg_class (1259) (system_views.sql:1085-1148), so
		// they belong to the S9.2 shape and were simply never captured.
		{"pg_stat_progress_analyze", 12309, 12312, 12311, 13},
		{"pg_stat_progress_vacuum", 12314, 12317, 12316, 15},
		{"pg_stat_progress_cluster", 12319, 12322, 12321, 12},
		{"pg_stat_progress_create_index", 12324, 12327, 12326, 16},
		{"pg_stat_progress_basebackup", 12329, 12332, 12331, 6},
		{"pg_stat_progress_copy", 12333, 12336, 12335, 11},
		{"pg_user_mappings", 12338, 12341, 12340, 6},
		{"pg_replication_origin_status", 12343, 12346, 12345, 4},
		{"pg_stat_subscription_stats", 12347, 12350, 12349, 12},
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
