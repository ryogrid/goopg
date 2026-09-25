package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestInitialRelRowsCTEKeepsCollapsedEstimate pins PG's behaviour for a CTE
// leaf whose filtered estimate collapses: `set_cte_size_estimates` keeps it
// and `clamp_row_est` floors it at 1
// (postgres/src/backend/optimizer/path/costsize.c:5356-5363). goopg's M0129-S1
// arm substituted the body's unfiltered row count instead; M0145-0012 retired
// it for plan parity.
//
// Why a unit witness: the substitution is invisible to every value gate
// (values are identical with and without it — only the plan shape and the
// runtime move, Q74 by ~13x), so reintroducing it would pass the corpus gates.
func TestInitialRelRowsCTEKeepsCollapsedEstimate(t *testing.T) {
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
	// 0.005^4 x 8325 collapses below 1.
	body := &SeqScan{Table: statsTable("year_total_body", 8325, 8325), schema: tableSchema(tbl)}
	cte := &CTEScan{Name: "year_total", Alias: "t", Child: body, schema: tableSchema(tbl)}
	leaf := &Filter{Child: cte, Predicate: combineAnd([]Expr{
		numGroupsEq(0, 1), numGroupsEq(0, 2), numGroupsEq(0, 3), numGroupsEq(0, 4),
	})}

	// filteredRows is meaningless for a derived leaf (it comes from a
	// synthetic catalog.Table), which is exactly why initialRelRows reads the
	// subtree instead — pass a value that would be wrong if it were believed.
	info := baseRelInfo{filteredRows: 7}

	if got := initialRelRows(leaf, info); got != 1 {
		t.Fatalf("initialRelRows = %v, want the collapsed estimate floored at 1 "+
			"(PG's clamp_row_est) — no body-count substitution", got)
	}
}
