package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestFlattenPulledBodyTreeCTELeafGate pins M0145-0011 scope (c): the pulled
// body's flat splice declines a `*CTEScan` leaf by default and admits it only
// under `GOOPG_PULLUP_CTE_LEAF=on`.
//
// The DEFAULT arm is what this test mainly protects. The scope-(c) relaxation
// is knob-arm measurement apparatus — M0145-0011 lands no relaxation — so a
// future edit that makes the admission unconditional has to fail a test rather
// than merely change a plan somewhere in a sweep. The decline REASON is pinned
// too, because the census counts the reason string: the loop-41 baseline read
// `any-body-leaf-(*optimizer.CTEScan)` 30 times over TPC-DS, and a silent
// rename of that string would report the relaxation as a no-op.
func TestFlattenPulledBodyTreeCTELeafGate(t *testing.T) {
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

	restore := pullupCTELeafEnabled
	defer func() { pullupCTELeafEnabled = restore }()

	pullupCTELeafEnabled = false
	if _, _, why, ok := flattenPulledBodyTree(body, 2); ok {
		t.Fatalf("default arm admitted a *CTEScan leaf; the firewall's pairing constraint says it must decline")
	} else if why != "body-leaf-(*optimizer.CTEScan)" {
		t.Fatalf("decline reason = %q, want body-leaf-(*optimizer.CTEScan) — the census counts this string", why)
	}

	pullupCTELeafEnabled = true
	leaves, _, why, ok := flattenPulledBodyTree(body, 2)
	if !ok {
		t.Fatalf("knob arm declined a *CTEScan leaf: why=%q", why)
	}
	if len(leaves) != 2 {
		t.Fatalf("leaves = %d, want 2", len(leaves))
	}
	if _, isCTE := leaves[1].(*CTEScan); !isCTE {
		t.Fatalf("leaf 1 is %T, want *CTEScan — the splice must carry the CTE leaf itself, not a substitute", leaves[1])
	}
}

// TestFlattenPulledBodyTreeNonCTEDerivedStillDeclines pins the NARROWNESS of
// scope (c): the flag admits `*CTEScan` and nothing else. A `*Filter` over a
// scan (the "needs unwrapping" case the decline comment names) must still
// decline even with the knob on, because the splice's qual rebase assumes the
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

	restore := pullupCTELeafEnabled
	defer func() { pullupCTELeafEnabled = restore }()
	pullupCTELeafEnabled = true

	if _, _, why, ok := flattenPulledBodyTree(body, 2); ok {
		t.Fatalf("knob arm admitted a *Filter leaf; scope (c) covers *CTEScan only")
	} else if why != "body-leaf-(*optimizer.Filter)" {
		t.Fatalf("decline reason = %q, want body-leaf-(*optimizer.Filter)", why)
	}
}
