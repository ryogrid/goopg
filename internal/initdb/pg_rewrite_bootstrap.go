// pg_rewrite bootstrap: seed the one Form_pg_rewrite heap tuple for
// pg_stat_wal_receiver._RETURN so PG's relcache, on opening the view
// (relhasrules=true, seeded by Step 3dl), finds the ON-SELECT rewrite
// rule via SearchSysCache2(RULERELNAME, view_oid, "_RETURN") instead
// of FATAL'ing with "cache lookup failed for rule …".
//
// M0106-0010 Step 3dm phase B (2026-05-18).
//
// The 8-column TupleDesc was nailed in Phase A; Phase B fills in the
// row. The ev_action pg_node_tree was dumped from an upstream PG18
// instance running system_views.sql (the canonical ON-SELECT rule for
// pg_stat_wal_receiver) — it parses to a SELECT FROM
// pg_stat_get_wal_receiver() RANGETBLENTRY with rtekind=3 (function),
// funcid=3317, and 15 TargetEntries matching the view column order.
// No view-side relid appears in THIS view's tree (the RTE references the
// underlying function, not the view's pg_class OID). That was over-claimed
// for the corpus as a whole: pg_stat_replication_slots is a view ON a view
// (system_views.sql:1045 → :1019) and its blob embeds `:relid 12261` twice.
// M0131-S8a settles it by policy rather than by rewriting — goopg's
// system-view OIDs are pinned to upstream's initdb assignment, so an
// embedded relid is already correct. See system_view_oid_pins.go.

package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// Rule OIDs for the six replication-view _RETURN rules.
//
// M0131-S8a (2026-08-11): these were goopg-private assignments (12101,
// 12107..12111) chosen adjacent to the view OIDs. They are now PINNED to
// what PG 18.3's own initdb assigns to each view's _RETURN rule, per the
// Option-A policy in system_view_oid_pins.go — a hosted PG's
// RewriteOidIndexId (2692) lookups must agree with the tree's own
// pg_rewrite rows, so rule OIDs are pinned alongside the view OIDs rather
// than left goopg-private. Values come from systemViewOIDPins(); the guard
// test asserts each constant equals its table row.
const (
	pgRewriteOIDPgStatWalReceiverReturn      uint32 = 12243
	pgRewriteOIDPgStatReplicationReturn      uint32 = 12234
	pgRewriteOIDPgStatRecoveryPrefetchReturn uint32 = 12247
	pgRewriteOIDPgStatSubscriptionReturn     uint32 = 12251
	pgRewriteOIDPgReplicationSlotsReturn     uint32 = 12264
	pgRewriteOIDPgStatReplicationSlotsReturn uint32 = 12269
)

// View OIDs for the five batched-28 replication views (mirrors of the
// nailedLocalRels entries in relcache_init.go; kept here for
// pg_rewrite.ev_class cross-reference). PINNED to upstream by M0131-S8a —
// see systemViewOIDPins(). The pg_replication_slots repin (12105 → 12261)
// also disarms the M0131-S6 landmine for free: the verbatim
// pg_stat_replication_slots ev_action blob embeds `:relid 12261` twice (its
// base view — system_views.sql:1045 selects from :1019), which now names
// the right relation with no blob edit at all.
const (
	pgStatReplicationViewOID      uint32 = 12231
	pgStatRecoveryPrefetchViewOID uint32 = 12244
	pgStatSubscriptionViewOID     uint32 = 12248
	pgReplicationSlotsViewOID     uint32 = 12261
	pgStatReplicationSlotsViewOID uint32 = 12266
)

// pgRewriteColDefs returns the canonical PG18 8-column Form_pg_rewrite
// layout (postgres/src/include/catalog/pg_rewrite.h:32-44):
//
//	oid          oid          (BKI_FORCE_NOT_NULL)
//	rulename     name         (BKI_FORCE_NOT_NULL)
//	ev_class     oid          (BKI_FORCE_NOT_NULL)
//	ev_type      char         (BKI_FORCE_NOT_NULL)
//	ev_enabled   char         (BKI_FORCE_NOT_NULL)
//	is_instead   bool         (BKI_FORCE_NOT_NULL)
//	ev_qual      pg_node_tree (BKI_FORCE_NOT_NULL)
//	ev_action    pg_node_tree (BKI_FORCE_NOT_NULL)
//
// Matches pgRewriteAttrs in relcache_init.go column-for-column. Delegates to
// executor.PGRewriteColumnsPG18 so the bootstrap seed and the B5 Slice C runtime
// writer (writeViewRewriteRow) / reload (loadViewsFromHeap) cannot drift.
func pgRewriteColDefs() []catalog.Column {
	return executor.PGRewriteColumnsPG18()
}

// pgRewriteEntry describes one Form_pg_rewrite row to seed into the
// heap. EvQual and EvAction are nodeToString-formatted pg_node_tree
// strings; both are required to be non-empty (BKI_FORCE_NOT_NULL).
// The empty pg_node_tree convention is "<>".
type pgRewriteEntry struct {
	OID       uint32
	RuleName  string
	EvClass   uint32
	EvType    byte // '1' = CMD_SELECT, '2' = CMD_UPDATE, '3' = CMD_INSERT, '4' = CMD_DELETE
	EvEnabled byte // 'O' = ALWAYS (origin/local), 'D' = DISABLED, 'R' = REPLICA
	IsInstead bool
	EvQual    string
	EvAction  string
}

// pgRewriteInitialEntries returns one row per ON-SELECT rule needed for
// the nailed local replication views (all six in the 12231..12266 range).
// Each ev_action is a verbatim nodeToString(Query) dump captured from an
// upstream PG18 instance running system_views.sql; no OID rewriting is
// needed — for five of the six because their RTEs reference the backing SRF
// funcid rather than a view's pg_class OID, and for
// pg_stat_replication_slots (whose blob DOES embed its base view's relid
// 12261 twice) because M0131-S8a pinned goopg's pg_replication_slots to that
// same upstream OID.
//
// M0131-S9.0: the rows are no longer written out here. They are generated from
// internal/initdb/nailed_view_manifest.tsv into nailedViewRewriteEntries()
// alongside the pg_class/pg_attribute tables the same capture produced, so a
// view cannot reach disk with a seeded relation but no rule (the
// "cache lookup failed for rule …" FATAL) or vice versa. Adding a view to the
// on-disk corpus is now: capture, regenerate, done.
func pgRewriteInitialEntries() []pgRewriteEntry {
	entries := nailedViewRewriteEntries()
	// M0133-S4: the information_schema views' _RETURN rules ride the same
	// pg_rewrite heap. Appended here so a hosted PG's RULERELNAME syscache probe
	// for information_schema.<view> resolves, exactly like the pg_catalog corpus.
	entries = append(entries, informationSchemaViewRewriteEntries()...)
	return entries
}

// nailedViewRewriteEntry returns the seeded _RETURN rule for the named system
// view. Used by guards that assert one view's row rather than the whole set;
// it fails the lookup loudly because a missing view here means the manifest and
// the caller disagree about what is on disk.
func nailedViewRewriteEntry(view string) (pgRewriteEntry, bool) {
	for _, e := range nailedViewRewriteEntries() {
		if e.EvClass == nailedViewSeedOID(view) {
			return e, true
		}
	}
	return pgRewriteEntry{}, false
}

// nailedViewSeedOID returns the generated pg_class OID for the named system
// view, or 0 if it is not part of the on-disk corpus.
func nailedViewSeedOID(view string) uint32 {
	for _, r := range nailedViewSeedRels() {
		if r.RelName == view {
			return r.OID
		}
	}
	return 0
}

// pgStatWalReceiverViewOID mirrors the OID assigned in Step 3dl's
// nailedLocalRels entry. Keep in sync — pg_rewrite.ev_class is the
// foreign key into pg_class. PINNED to upstream by M0131-S8a (was the
// goopg-private 12100) — see systemViewOIDPins().
const pgStatWalReceiverViewOID uint32 = 12240

// pgRewriteRow builds the 8-column Form_pg_rewrite row in
// pgRewriteColDefs order. All columns are BKI_FORCE_NOT_NULL so the
// null-bitmap is unused (every datum is a real value, including the
// empty-string ev_qual encoded as varlena "<>").
// pgRewriteRow builds the 8-column Form_pg_rewrite row in
// pgRewriteColDefs order. ev_qual and ev_action use pglzVarlenaDatum so
// large payloads (e.g. pg_stat_replication's 27 KB ev_action) are
// stored as PGLZ-compressed varlena bytes, which is the same format PG
// produces during system_views.sql processing. Small payloads fall back
// to uncompressed varlena.
// M0131-S20.2: ev_action is stored out of line when its inline representation
// would not fit the heap tuple. pgRewriteRowToasted is the full form —
// pgRewriteRow keeps the inline-only signature for the guards that build a row
// in isolation, and asserts the entry is one that stays inline.
func pgRewriteRow(e pgRewriteEntry) executor.Row {
	row, chunks := pgRewriteRowToasted(e)
	if len(chunks) > 0 {
		// Callers of this form have nowhere to put the chunks; the row alone
		// would carry a pointer into an unwritten TOAST heap.
		panic(fmt.Sprintf("pg_rewrite rule %d (%s) needs out-of-line ev_action: "+
			"build it with pgRewriteRowToasted", e.OID, e.RuleName))
	}
	return row
}

// pgRewriteRowToasted builds the 8-column row and any TOAST chunks its
// ev_action requires. See pg_rewrite_toast_writer.go for the representation
// rules; a nil chunk slice means the value stayed inline, which is the case
// for all 71 views in the corpus as of S20.2's landing.
func pgRewriteRowToasted(e pgRewriteEntry) (executor.Row, []toastChunk) {
	evAction, chunks := pgRewriteEvActionDatum(e)
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),       // 1 oid
		executor.NewStringDatum(e.RuleName),      // 2 rulename
		executor.NewIntDatum(int64(e.EvClass)),   // 3 ev_class
		executor.NewIntDatum(int64(e.EvType)),    // 4 ev_type (char → byte via Int path in codec)
		executor.NewIntDatum(int64(e.EvEnabled)), // 5 ev_enabled
		executor.NewBoolDatum(e.IsInstead),       // 6 is_instead
		pglzVarlenaDatum(e.EvQual),               // 7 ev_qual    (varlena pg_node_tree)
		evAction,                                 // 8 ev_action  (inline varlena OR 18-byte varatt_external)
	}, chunks
}

// bootstrapPgRewriteTuples writes the seeded Form_pg_rewrite heap
// rows to base/{1,5}/2618. Returns a map keyed by rule OID so the
// follow-on btree leaf bootstrappers (2692 oid index, 2693
// (ev_class, rulename) index) can stamp each IndexTuple's t_tid
// at the (block, offset) of its heap row.
// M0131-S20.2: it also returns the TOAST chunks the seeded ev_action values
// pushed out of line, in (chunk_id, chunk_seq) order. The caller hands them to
// bootstrapToastRelationFiles, which is the single writer of base/{1,5}/2838 —
// keeping the chunk bytes and the pointers that name them in one Init step so
// they cannot be produced without each other.
func bootstrapPgRewriteTuples(dataDir string) (map[uint32]heapTID, []toastChunk, error) {
	cols := pgRewriteColDefs()
	entries := pgRewriteInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	external := make([]bool, 0, len(entries))
	var chunks []toastChunk
	for _, e := range entries {
		row, rowChunks := pgRewriteRowToasted(e)
		rows = append(rows, row)
		external = append(external, len(rowChunks) > 0)
		chunks = append(chunks, rowChunks...)
	}
	tids, err := writeMultiPageHeapRowsExternal(dataDir, "2618", cols, rows, external)
	if err != nil {
		return nil, nil, fmt.Errorf("pg_rewrite heap: %w", err)
	}
	m := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		m[e.OID] = tids[i]
	}
	return m, chunks, nil
}
