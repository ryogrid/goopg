package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestFlattenPulledBodyTreeAdmitsCTELeaf pins M0145-0008ac's promotion of
// M0145-0011 scope (c): the pulled body's flat splice admits a `*CTEScan` leaf
// unconditionally, as PG treats a CTE reference in a sublink body as a base rel
// of the pulled-up jointree. The splice must carry the CTE leaf itself, not a
// substitute: the seam binds and prices it from its plan (M0145-0013).
func TestFlattenPulledBodyTreeAdmitsCTELeaf(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "pcl_t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	scan := &SeqScan{Table: tbl, schema: tableSchema(tbl)}
	cteBody := &SeqScan{Table: tbl, schema: tableSchema(tbl)}
	cte := &CTEScan{Name: "c", Alias: "c", Child: cteBody, schema: tableSchema(tbl)}
	body := &Join{Type: JoinTypeInner, Left: scan, Right: cte}

	leaves, _, why, ok := flattenPulledBodyTree(body, 2)
	if !ok {
		t.Fatalf("flat splice declined a *CTEScan leaf: why=%q", why)
	}
	if len(leaves) != 2 {
		t.Fatalf("leaves = %d, want 2", len(leaves))
	}
	if _, isCTE := leaves[1].(*CTEScan); !isCTE {
		t.Fatalf("leaf 1 is %T, want *CTEScan — the splice must carry the CTE leaf itself, not a substitute", leaves[1])
	}
}

// TestFlattenPulledBodyTreeNonCTEDerivedStillDeclines pins the NARROWNESS of
// the flat splice: it admits `*SeqScan` and `*CTEScan` and nothing else. A
// `*Filter` over a scan (the "needs unwrapping" case the decline comment
// names) must still decline, because the splice's qual rebase assumes the
// leaf emits exactly its own Output() and a Filter changes the row count
// without changing the schema.
func TestFlattenPulledBodyTreeNonCTEDerivedStillDeclines(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "pcl_u"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	scan := &SeqScan{Table: tbl, schema: tableSchema(tbl)}
	filtered := &Filter{Child: &SeqScan{Table: tbl, schema: tableSchema(tbl)}}
	body := &Join{Type: JoinTypeInner, Left: scan, Right: filtered}

	if _, _, why, ok := flattenPulledBodyTree(body, 2); ok {
		t.Fatalf("flat splice admitted a *Filter leaf; only *SeqScan and *CTEScan are leaves")
	} else if why != "body-leaf-(*optimizer.Filter)" {
		t.Fatalf("decline reason = %q, want body-leaf-(*optimizer.Filter)", why)
	}
}
