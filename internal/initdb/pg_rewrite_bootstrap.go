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

// pgRewriteOIDPgStatWalReceiverReturn is a goopg-private stable OID
// assigned to the pg_stat_wal_receiver._RETURN rule. PG normally
// assigns rule OIDs dynamically at initdb time; this OID lives in
// PG18's FirstUnpinnedObjectId..FirstNormalObjectId range
// (12000..16383) and stays adjacent to the view OID 12100 (Step 3dl).
const pgRewriteOIDPgStatWalReceiverReturn uint32 = 12101

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
// the nailed local views. Currently only pg_stat_wal_receiver — adding
// further system_views.sql views is mechanical (capture ev_action from
// an upstream PG dump, add an entry here).
func pgRewriteInitialEntries() []pgRewriteEntry {
	return []pgRewriteEntry{
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
}

// pgStatWalReceiverViewOID mirrors the OID assigned in Step 3dl's
// nailedLocalRels entry. Keep in sync — pg_rewrite.ev_class is the
// foreign key into pg_class.
const pgStatWalReceiverViewOID uint32 = 12100

// pgRewriteRow builds the 8-column Form_pg_rewrite row in
// pgRewriteColDefs order. All columns are BKI_FORCE_NOT_NULL so the
// null-bitmap is unused (every datum is a real value, including the
// empty-string ev_qual encoded as varlena "<>").
func pgRewriteRow(e pgRewriteEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),             // 1 oid
		executor.NewStringDatum(e.RuleName),            // 2 rulename
		executor.NewIntDatum(int64(e.EvClass)),         // 3 ev_class
		executor.NewIntDatum(int64(e.EvType)),          // 4 ev_type (char → byte via Int path in codec)
		executor.NewIntDatum(int64(e.EvEnabled)),       // 5 ev_enabled
		executor.NewBoolDatum(e.IsInstead),             // 6 is_instead
		executor.NewStringDatum(e.EvQual),              // 7 ev_qual    (varlena pg_node_tree)
		executor.NewStringDatum(e.EvAction),            // 8 ev_action  (varlena pg_node_tree)
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
