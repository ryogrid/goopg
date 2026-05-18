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
// No view-side relid appears in the tree (the RTE references the
// underlying function, not the view's pg_class OID), so no OID
// rewriting is needed when porting the dump across PG/goopg.

package initdb

import (
	_ "embed"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

//go:embed pg_stat_wal_receiver_ev_action.dat
var pgStatWalReceiverEvAction string

//go:embed pg_stat_replication_ev_action.dat
var pgStatReplicationEvAction string

//go:embed pg_stat_recovery_prefetch_ev_action.dat
var pgStatRecoveryPrefetchEvAction string

//go:embed pg_stat_subscription_ev_action.dat
var pgStatSubscriptionEvAction string

//go:embed pg_replication_slots_ev_action.dat
var pgReplicationSlotsEvAction string

//go:embed pg_stat_replication_slots_ev_action.dat
var pgStatReplicationSlotsEvAction string

// Rule OIDs for the six replication-view _RETURN rules. Assigned from
// PG18's FirstUnpinnedObjectId..FirstNormalObjectId range (12000..16383).
// View OIDs 12100..12106 are reserved in relcache_init.go; rule OIDs
// start at 12101 (adjacent to wal_receiver view 12100) and continue at
// 12107..12111 for the five batched-28 views (12102..12106).
const (
	pgRewriteOIDPgStatWalReceiverReturn      uint32 = 12101
	pgRewriteOIDPgStatReplicationReturn      uint32 = 12107
	pgRewriteOIDPgStatRecoveryPrefetchReturn uint32 = 12108
	pgRewriteOIDPgStatSubscriptionReturn     uint32 = 12109
	pgRewriteOIDPgReplicationSlotsReturn     uint32 = 12110
	pgRewriteOIDPgStatReplicationSlotsReturn uint32 = 12111
)

// View OIDs for the five batched-28 replication views (mirrors of the
// nailedLocalRels entries in relcache_init.go; kept here for
// pg_rewrite.ev_class cross-reference).
const (
	pgStatReplicationViewOID      uint32 = 12102
	pgStatRecoveryPrefetchViewOID uint32 = 12103
	pgStatSubscriptionViewOID     uint32 = 12104
	pgReplicationSlotsViewOID     uint32 = 12105
	pgStatReplicationSlotsViewOID uint32 = 12106
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
// Matches pgRewriteAttrs in relcache_init.go column-for-column.
func pgRewriteColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "rulename", Type: catalog.Type{Name: "name"}},
		{Name: "ev_class", Type: catalog.Type{Name: "oid"}},
		{Name: "ev_type", Type: catalog.Type{Name: "char"}},
		{Name: "ev_enabled", Type: catalog.Type{Name: "char"}},
		{Name: "is_instead", Type: catalog.Type{Name: "bool"}},
		{Name: "ev_qual", Type: catalog.Type{Name: "pg_node_tree"}},
		{Name: "ev_action", Type: catalog.Type{Name: "pg_node_tree"}},
	}
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
// the nailed local replication views (all six in the 12100..12106 range).
// Each ev_action is a verbatim nodeToString(Query) dump captured from an
// upstream PG18 instance running system_views.sql; no OID rewriting is
// needed because the RTEs reference the backing SRF funcid, not the view's
// pg_class OID.
func pgRewriteInitialEntries() []pgRewriteEntry {
	base := []pgRewriteEntry{
		{
			OID:       pgRewriteOIDPgStatWalReceiverReturn,
			RuleName:  "_RETURN",
			EvClass:   pgStatWalReceiverViewOID,
			EvType:    '1', // CMD_SELECT
			EvEnabled: 'O', // ALWAYS
			IsInstead: true,
			EvQual:    "<>", // empty node tree — no WHERE clause on the rule
			EvAction:  pgStatWalReceiverEvAction,
		},
	}
	return append(base, replicationViewRewriteEntries()...)
}

// replicationViewRewriteEntries returns the five _RETURN rule entries for
// the remaining replication views seeded in batched-28
// (pg_stat_replication, pg_stat_recovery_prefetch, pg_stat_subscription,
// pg_replication_slots, pg_stat_replication_slots). These follow the same
// ev_action capture pattern as the wal_receiver entry above.
func replicationViewRewriteEntries() []pgRewriteEntry {
	return []pgRewriteEntry{
		{
			OID:       pgRewriteOIDPgStatReplicationReturn,
			RuleName:  "_RETURN",
			EvClass:   pgStatReplicationViewOID,
			EvType:    '1',
			EvEnabled: 'O',
			IsInstead: true,
			EvQual:    "<>",
			EvAction:  pgStatReplicationEvAction,
		},
		{
			OID:       pgRewriteOIDPgStatRecoveryPrefetchReturn,
			RuleName:  "_RETURN",
			EvClass:   pgStatRecoveryPrefetchViewOID,
			EvType:    '1',
			EvEnabled: 'O',
			IsInstead: true,
			EvQual:    "<>",
			EvAction:  pgStatRecoveryPrefetchEvAction,
		},
		{
			OID:       pgRewriteOIDPgStatSubscriptionReturn,
			RuleName:  "_RETURN",
			EvClass:   pgStatSubscriptionViewOID,
			EvType:    '1',
			EvEnabled: 'O',
			IsInstead: true,
			EvQual:    "<>",
			EvAction:  pgStatSubscriptionEvAction,
		},
		{
			OID:       pgRewriteOIDPgReplicationSlotsReturn,
			RuleName:  "_RETURN",
			EvClass:   pgReplicationSlotsViewOID,
			EvType:    '1',
			EvEnabled: 'O',
			IsInstead: true,
			EvQual:    "<>",
			EvAction:  pgReplicationSlotsEvAction,
		},
		{
			OID:       pgRewriteOIDPgStatReplicationSlotsReturn,
			RuleName:  "_RETURN",
			EvClass:   pgStatReplicationSlotsViewOID,
			EvType:    '1',
			EvEnabled: 'O',
			IsInstead: true,
			EvQual:    "<>",
			EvAction:  pgStatReplicationSlotsEvAction,
		},
	}
}

// pgStatWalReceiverViewOID mirrors the OID assigned in Step 3dl's
// nailedLocalRels entry. Keep in sync — pg_rewrite.ev_class is the
// foreign key into pg_class.
const pgStatWalReceiverViewOID uint32 = 12100

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
func pgRewriteRow(e pgRewriteEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),       // 1 oid
		executor.NewStringDatum(e.RuleName),      // 2 rulename
		executor.NewIntDatum(int64(e.EvClass)),   // 3 ev_class
		executor.NewIntDatum(int64(e.EvType)),    // 4 ev_type (char → byte via Int path in codec)
		executor.NewIntDatum(int64(e.EvEnabled)), // 5 ev_enabled
		executor.NewBoolDatum(e.IsInstead),       // 6 is_instead
		pglzVarlenaDatum(e.EvQual),               // 7 ev_qual    (varlena pg_node_tree)
		pglzVarlenaDatum(e.EvAction),             // 8 ev_action  (varlena pg_node_tree)
	}
}

// bootstrapPgRewriteTuples writes the seeded Form_pg_rewrite heap
// rows to base/{1,5}/2618. Returns a map keyed by rule OID so the
// follow-on btree leaf bootstrappers (2692 oid index, 2693
// (ev_class, rulename) index) can stamp each IndexTuple's t_tid
// at the (block, offset) of its heap row.
func bootstrapPgRewriteTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgRewriteColDefs()
	entries := pgRewriteInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgRewriteRow(e))
	}
	tids, err := writeMultiPageHeapRows(dataDir, "2618", cols, rows)
	if err != nil {
		return nil, fmt.Errorf("pg_rewrite heap: %w", err)
	}
	m := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		m[e.OID] = tids[i]
	}
	return m, nil
}
