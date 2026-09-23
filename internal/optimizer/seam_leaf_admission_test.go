package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestSeamLeafBindingAdmission pins M0145-0013's admission rule at the seam:
// a `*SeqScan` leaf binds to its catalog relation, a `*CTEScan` leaf is
// admitted with a DELIBERATELY nil table, and every other leaf kind is
// refused.
//
// The nil table is the load-bearing part, not an omission. It is how a CTE
// leaf expresses "no catalog statistics" downstream: the seam prices it via
// `EstimateRows` over the body rather than the catalog path
// (`TestSeamLeafRelInfoRoutesOnTable` below). Until M0145-0018 it also kept
// the `outer-over-derived` firewall in force over the leaf — a future edit
// that "helpfully" synthesises a catalog.Table for the CTE leaf would still
// silently re-route its pricing, and no corpus gate would report it.
func TestSeamLeafBindingAdmission(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "sla_t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("SeqScan binds to its relation", func(t *testing.T) {
		scan := &SeqScan{Table: tbl, Alias: "s", schema: tableSchema(tbl)}
		b, ok := seamLeafBinding(scan, 7, nil, 0)
		if !ok {
			t.Fatalf("a bare *SeqScan leaf must be admitted")
		}
		if b.table != tbl || b.alias != "s" || b.offset != 7 {
			t.Fatalf("binding = {table:%v alias:%q offset:%d}, want {sla_t s 7}", b.table, b.alias, b.offset)
		}
	})

	t.Run("CTEScan is admitted with no relation", func(t *testing.T) {
		cte := &CTEScan{Name: "c", Alias: "c1", Child: &SeqScan{Table: tbl, schema: tableSchema(tbl)}, schema: tableSchema(tbl)}
		b, ok := seamLeafBinding(cte, 3, nil, 0)
		if !ok {
			t.Fatalf("a *CTEScan leaf must be admitted — M0145-0013's whole point")
		}
		if b.table != nil {
			t.Fatalf("binding.table = %v, want nil: a CTE leaf carries no catalog "+
				"statistics, and the pricing path routes on table==nil", b.table)
		}
		if b.alias != "c1" || b.offset != 3 {
			t.Fatalf("binding = {alias:%q offset:%d}, want {c1 3}", b.alias, b.offset)
		}
	})

	t.Run("anything else is refused", func(t *testing.T) {
		other := &Filter{Child: &SeqScan{Table: tbl, schema: tableSchema(tbl)}}
		if _, ok := seamLeafBinding(other, 0, nil, 0); ok {
			t.Fatalf("a *Filter leaf must be refused — the splice's qual rebase assumes "+
				"the leaf emits exactly its own Output()")
		}
	})
}

// TestSeamLeafRelInfoRoutesOnTable pins that the pricing follows the binding:
// a real relation goes through the catalog-statistics path, and a derived leaf
// goes through `EstimateRows`.
//
// Routing on `b.table` rather than on the node type is deliberate — both
// `estimateBaseRelInfo` and `applyRelSizeFallback` read `binding.table`, and
// with it nil `estimateTableRowsFallback` returns 0, which would floor a
// derived leaf at a ZERO row estimate and make every join above it free. That
// is the same failure mode the opaque Semi/Anti RHS arm documents.
func TestSeamLeafRelInfoRoutesOnTable(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "slr_t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	body := &SeqScan{Table: statsTable("slr_body", 8325, 8325)}
	cte := &CTEScan{Name: "c", Alias: "c1", Child: body}

	b, ok := seamLeafBinding(cte, 0, nil, 0)
	if !ok {
		t.Fatalf("CTE leaf not admitted")
	}
	info := seamLeafRelInfo(4, b, cte, nil, cat)
	if info.bindingIdx != 4 {
		t.Fatalf("bindingIdx = %d, want 4", info.bindingIdx)
	}
	if info.baseRows != 8325 {
		t.Fatalf("baseRows = %d, want 8325 — EstimateRows(*CTEScan) recurses the body "+
			"(goopg's set_cte_size_estimates equivalent), it must not fall to the catalog path", info.baseRows)
	}
	if info.filteredRows != 8325 {
		t.Fatalf("filteredRows = %d, want 8325 with no local filter", info.filteredRows)
	}
}
