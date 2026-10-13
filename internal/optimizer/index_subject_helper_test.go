package optimizer

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// planIndexSubject plans stmt with enable_seqscan and enable_bitmapscan off.
// Tests whose subject is a plain index-scan shape use it: their in-memory
// catalogs carry no storage and no statistics, so the costed search sizes the
// relation as near-empty and keeps a Seq Scan, which PG — sizing a
// never-vacuumed relation at 10 pages — would not always do. The rule-based
// single-table producer used to override that election with a heuristic index
// scan; since M0146-0060 it only supplies a LIKE prefix, so these tests ask
// for their subject the way PG would need to.
func planIndexSubject(stmt parser.Stmt, cat catalog.Catalog) (Node, error) {
	ps := DefaultPlannerSettings()
	ps.EnableSeqScan = false
	ps.EnableBitmapScan = false
	return PlanWithSettings(stmt, cat, ps)
}
