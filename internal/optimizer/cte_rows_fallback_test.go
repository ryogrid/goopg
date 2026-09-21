package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestInitialRelRowsCTEFallbackGate pins the M0129-S1 `rows<=1` CTE fallback
// in both directions, and pins that the knob M0145-0012 added actually removes
// it.
//
// Why a test rather than a sweep: the arm engages on three TPC-DS queries and
// the sweep's value channel cannot see it at all (values are identical with
// and without the arm — only the plan shape and the runtime move, Q74 by
// 16.6x). A cardinality substitution that silently stops happening is exactly
// the class of regression the corpus gates do not catch, so the guard needs a
// unit-level witness.
func TestInitialRelRowsCTEFallbackGate(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "crf_t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The production shape, in miniature: a CTE body of 8325 rows (Q74's
	// `year_total` at SF0.25) under four equality conjuncts on columns with
	// no statistics. Each defaults to defaultEqSelectivity, so
	// 0.005^4 x 8325 collapses below 1 and the arm — if enabled — replaces
	// it with the body count.
	body := &SeqScan{Table: statsTable("year_total_body", 8325, 8325), schema: tableSchema(tbl)}
	cte := &CTEScan{Name: "year_total", Alias: "t", Child: body, schema: tableSchema(tbl)}
	leaf := &Filter{Child: cte, Predicate: combineAnd([]Expr{
		numGroupsEq(0, 1), numGroupsEq(0, 2), numGroupsEq(0, 3), numGroupsEq(0, 4),
	})}

	// filteredRows is meaningless for a derived leaf (it comes from a
	// synthetic catalog.Table), which is exactly why initialRelRows reads the
	// subtree instead — pass a value that would be wrong if it were believed.
	info := baseRelInfo{filteredRows: 7}

	restore := cteRowsFallbackEnabled
	defer func() { cteRowsFallbackEnabled = restore }()

	cteRowsFallbackEnabled = true
	if got := initialRelRows(leaf, info); got != 8325 {
		t.Fatalf("with the arm ON, initialRelRows = %v, want the body count 8325", got)
	}

	cteRowsFallbackEnabled = false
	if got := initialRelRows(leaf, info); got != 1 {
		t.Fatalf("with the arm OFF, initialRelRows = %v, want the collapsed estimate 1 "+
			"(PG's clamp_row_est floor) — the knob must actually remove the substitution", got)
	}
}
