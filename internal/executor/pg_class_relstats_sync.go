package executor

import (
	"math"

	"github.com/goopg/goopg/internal/catalog"
)

// M0146-0064 — `UPDATE pg_class SET reltuples` reaches the planner.
//
// PG plans from the relcache's rd_rel->reltuples and relpages
// (estimate_rel_size, plancat.c), and an UPDATE of the pg_class row
// invalidates that relcache entry, so the next plan in the same session sees
// the new values. Regress groupingsets and bug_16784 rely on it: `update
// pg_class set reltuples = 10 where relname = 'gs_data_1'` makes PG hash every
// grouping set of the CUBE that follows.
//
// goopg's pg_class UPDATE rewrites the physical pg_class heap row (the table's
// columns mirror that tuple descriptor, M0100-0010). But the planner and the
// virtual pg_class view both read catalog.Table.Stats, and startup reloads
// those from the goopg_relstats sidecar heap. The UPDATE reported `UPDATE 1`
// and changed nothing either reads. syncPgClassRelStats applies the updated
// values to Table.Stats and appends them to the sidecar in the same
// transaction (an aborted UPDATE's sidecar row is dead to the reload). A
// ROLLBACK of an explicit transaction restores the previous Stats.

// syncPgClassRelStats applies one updated pg_class row's reltuples to the
// relation's planner statistics, when the UPDATE's SET clause assigns it: the
// heap row's value can be older than Stats (ANALYZE writes the sidecar, not
// the pg_class heap), so an UPDATE of an unrelated pg_class column must not
// drag it back.
//
// relpages is deliberately not synced. PG reads it only as the denominator of
// the tuple density it scales by the live block count (estimate_rel_size);
// costs use the live count. goopg costs a scan from Stats.Pages and does not
// scale an analyzed relation's reltuples, so a written relpages would become a
// fake page count in every cost — further from PG than ignoring it. Ledgered
// with the density scaling it needs.
func (o *updateOp) syncPgClassRelStats(newRow Row) {
	tbl := o.plan.Table
	if tbl == nil || tbl.Schema != "pg_catalog" || tbl.Name != "pg_class" {
		return
	}
	oidCol, tuplesCol := -1, -1
	for i, c := range tbl.Columns {
		switch c.Name {
		case "oid":
			oidCol = i
		case "reltuples":
			tuplesCol = i
		}
	}
	if tuplesCol < 0 || tuplesCol >= len(o.plan.Set) || o.plan.Set[tuplesCol] == nil ||
		tuplesCol >= len(newRow) || oidCol < 0 || oidCol >= len(newRow) {
		return
	}
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return
	}
	oid, ok := datumToFloat64(newRow[oidCol])
	if !ok || oid <= 0 {
		return
	}
	rel, found := im.LookupTableByOID(uint32(oid), catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid))
	if !found || rel == nil || rel.Virtual {
		return
	}

	var merged catalog.TableStats
	if rel.Stats != nil {
		merged = *rel.Stats
	}
	v, ok := datumToFloat64(newRow[tuplesCol])
	if !ok {
		return
	}
	if v < 0 {
		// reltuples = -1 is PG's "never vacuumed or analyzed".
		merged.RowCount = 0
		merged.Analyzed = false
	} else {
		merged.RowCount = int64(math.Round(v))
		merged.Analyzed = true
	}

	if sess, ok := o.ctx.Session.(*BasicSession); ok && sess.InExplicitTransaction() {
		sess.RecordRelStatsUndo(RelStatsUndoEntry{Table: rel, Stats: rel.Stats})
	}
	// Pointer-replace, as UpdateRelStats does: a concurrent planner never
	// sees a torn struct.
	rel.Stats = &merged
	if merged.Analyzed {
		_ = persistRelSize(o.ctx, rel, merged.RowCount, merged.Pages)
	}
	// PG's relcache invalidation for the updated pg_class row also resets
	// every cached plan over the relation (plancache.c PlanCacheRelCallback).
	// goopg's shared plan cache is invalidated through the hook DDL uses,
	// otherwise a cached plan keeps the old row count.
	if o.ctx.OnCommitDDL != nil {
		o.ctx.OnCommitDDL()
	}
}

// RelStatsUndoEntry is one relation's Stats before a pg_class UPDATE replaced
// them inside an explicit transaction; ROLLBACK puts them back (M0146-0064).
type RelStatsUndoEntry struct {
	Table *catalog.Table
	Stats *catalog.TableStats
}
