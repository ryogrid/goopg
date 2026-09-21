package optimizer

// M0145-0005 slice 3 — jtScope parity pins. `extractScopeLeaves` must
// produce the IDENTICAL (scans, widths, onQuals, outer, semiAnti) tuple
// `extractSearchLeaves` derives by walking the node chain — the table is
// a recording of the same traversal, not a re-derivation. These tests
// run both paths over the shape matrix the walk's arms cover: comma
// items, inner/outer/reduced-right chains, multi-item mixes, the FULL
// fold, the `!preserved` decline, and the demoted-ANTI synthetic leaf.

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func scopeTestCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	for _, name := range []string{"a", "b", "c", "d"} {
		cols := []catalog.Column{
			{Name: name + "x", Type: catalog.Type{Name: "int8"}},
			{Name: name + "y", Type: catalog.Type{Name: "int8"}},
		}
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	return cat
}

// sameTuple compares the two extraction paths' outputs field by field.
// scans must be the same NODE POINTERS (both paths read the nodes
// planFromItem attached); rebased preds compare by reflect.DeepEqual —
// cloneExprRefs produces structurally identical trees when both paths
// shift by the same delta, and a divergence there is exactly the bug
// this pin exists for.
func sameTuple(t *testing.T, wScans []Node, wWidths []int, wOn []chainOnQual, wOuter []outerChainLink, wSemi []semiAntiChainLink, wOK bool, sScans []Node, sWidths []int, sOn []chainOnQual, sOuter []outerChainLink, sSemi []semiAntiChainLink, sOK bool) {
	t.Helper()
	if wOK != sOK {
		t.Fatalf("extraction ok mismatch: walk=%v scope=%v (walk scans=%d widths=%v)",
			wOK, sOK, len(wScans), wWidths)
	}
	if !wOK {
		return
	}
	if len(wScans) != len(sScans) {
		t.Fatalf("scan count: walk=%d scope=%d", len(wScans), len(sScans))
	}
	for i := range wScans {
		if wScans[i] != sScans[i] {
			t.Fatalf("scan[%d] differs: walk=%T scope=%T", i, wScans[i], sScans[i])
		}
	}
	if !reflect.DeepEqual(wWidths, sWidths) {
		t.Fatalf("widths: walk=%v scope=%v", wWidths, sWidths)
	}
	if len(wOn) != len(sOn) {
		t.Fatalf("onQuals: walk=%d scope=%d", len(wOn), len(sOn))
	}
	for i := range wOn {
		if wOn[i].belowNullable != sOn[i].belowNullable {
			t.Fatalf("onQuals[%d].belowNullable: walk=%v scope=%v", i, wOn[i].belowNullable, sOn[i].belowNullable)
		}
		if !reflect.DeepEqual(wOn[i].pred, sOn[i].pred) {
			t.Fatalf("onQuals[%d].pred differs:\nwalk=%#v\nscope=%#v", i, wOn[i].pred, sOn[i].pred)
		}
	}
	if len(wOuter) != len(sOuter) {
		t.Fatalf("outerLinks: walk=%d scope=%d", len(wOuter), len(sOuter))
	}
	for i := range wOuter {
		if wOuter[i].jointype != sOuter[i].jointype ||
			wOuter[i].preserved != sOuter[i].preserved ||
			wOuter[i].nullable != sOuter[i].nullable {
			t.Fatalf("outer[%d]: walk=(%v,%v,%v) scope=(%v,%v,%v)", i,
				wOuter[i].jointype, wOuter[i].preserved, wOuter[i].nullable,
				sOuter[i].jointype, sOuter[i].preserved, sOuter[i].nullable)
		}
		if !reflect.DeepEqual(wOuter[i].pred, sOuter[i].pred) {
			t.Fatalf("outer[%d].pred differs:\nwalk=%#v\nscope=%#v", i, wOuter[i].pred, sOuter[i].pred)
		}
	}
	if len(wSemi) != len(sSemi) {
		t.Fatalf("semiAnti: walk=%d scope=%d", len(wSemi), len(sSemi))
	}
	for i := range wSemi {
		w, s := wSemi[i], sSemi[i]
		if w.jointype != s.jointype || w.lhs != s.lhs || w.rhs != s.rhs || w.flattened != s.flattened {
			t.Fatalf("semiAnti[%d]: walk=(%v,%v,%v,f=%v) scope=(%v,%v,%v,f=%v)", i,
				w.jointype, w.lhs, w.rhs, w.flattened, s.jointype, s.lhs, s.rhs, s.flattened)
		}
		if w.sjinfo != s.sjinfo {
			t.Fatalf("semiAnti[%d].sjinfo: walk=%p scope=%p", i, w.sjinfo, s.sjinfo)
		}
		if !reflect.DeepEqual(w.pred, s.pred) {
			t.Fatalf("semiAnti[%d].pred differs:\nwalk=%#v\nscope=%#v", i, w.pred, s.pred)
		}
		if len(w.bodyQuals) != len(s.bodyQuals) {
			t.Fatalf("semiAnti[%d].bodyQuals: walk=%d scope=%d", i, len(w.bodyQuals), len(s.bodyQuals))
		}
		for k := range w.bodyQuals {
			if !reflect.DeepEqual(w.bodyQuals[k], s.bodyQuals[k]) {
				t.Fatalf("semiAnti[%d].bodyQuals[%d] differs", i, k)
			}
		}
	}
}

func TestScopeExtractionMatchesWalk(t *testing.T) {
	cat := scopeTestCatalog(t)
	for _, q := range []string{
		"SELECT * FROM a",
		"SELECT * FROM a, b",
		"SELECT * FROM a, b, c, d",
		"SELECT * FROM a JOIN b ON a.ax = b.bx",
		"SELECT * FROM a JOIN b ON a.ax = b.bx JOIN c ON b.bx = c.cx",
		"SELECT * FROM a LEFT JOIN b ON a.ax = b.bx",
		"SELECT * FROM a LEFT JOIN b ON a.ax = b.bx LEFT JOIN c ON a.ax = c.cx",
		// LEFT inner-only conjunct pushdown: `b.by > 5` wraps the right
		// leaf in Filter{LeafLocal} — the leaf record is the wrapped node.
		"SELECT * FROM a LEFT JOIN b ON a.ax = b.bx AND b.by > 5",
		"SELECT * FROM a LEFT JOIN b ON a.ax = b.bx JOIN c ON a.ay = c.cy",
		"SELECT * FROM a RIGHT JOIN b ON a.ax = b.bx",
		"SELECT * FROM a JOIN b ON TRUE, c LEFT JOIN d ON c.cx = d.dx",
		"SELECT * FROM a, b JOIN c ON b.bx = c.cx",
		"SELECT * FROM a FULL JOIN b ON a.ax = b.bx",
		"SELECT * FROM a JOIN b ON TRUE FULL JOIN c ON TRUE",
		"SELECT * FROM a JOIN (SELECT * FROM b) s ON TRUE",
		"SELECT * FROM a JOIN (b LEFT JOIN c ON b.bx = c.cx) ON TRUE",
		// the !preserved decline: an outer link inside another's
		// nullable side — both paths must return !ok.
		"SELECT * FROM a LEFT JOIN b ON a.ax = b.bx RIGHT JOIN c ON TRUE",
		// demoted ANTI (leading-collapsed): the WHERE conjunct forces
		// LEFT->ANTI — the chain carries a Join{Anti} and the walk's
		// semiAnti arm fires.
		"SELECT * FROM a LEFT JOIN b ON a.ax = b.bx WHERE b.bx IS NULL",
		// demoted ANTI mid-chain (non-leading): `c`'s link demotes.
		"SELECT * FROM a JOIN b ON TRUE LEFT JOIN c ON b.bx = c.cx WHERE c.cx IS NULL",
		"SELECT * FROM a, b LEFT JOIN c ON b.bx = c.cx WHERE c.cx IS NULL",
	} {
		t.Run(q, func(t *testing.T) {
			stmts, err := parser.Parse(q)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			node, rctx, err := planFromClause(stmts[0].(*parser.SelectStmt), cat, DefaultPlannerSettings(), nil)
			if err != nil {
				t.Fatalf("planFromClause: %v", err)
			}
			if rctx.jtScope == nil {
				t.Fatal("planFromClause left resolveContext.jtScope nil")
			}
			if rctx.jtScope.root != node {
				t.Fatalf("jtScope.root (%T) != chain root (%T)", rctx.jtScope.root, node)
			}
			wScans, wWidths, wOn, wOuter, wSemi, wOK := extractSearchLeaves(node)
			sScans, sWidths, sOn, sOuter, sSemi, sOK := extractScopeLeaves(rctx.jtScope)
			sameTuple(t, wScans, wWidths, wOn, wOuter, wSemi, wOK,
				sScans, sWidths, sOn, sOuter, sSemi, sOK)
		})
	}
}

// The scope table must reproduce the walk's DECLINE when a link sits
// inside another outer link's nullable side — the `preserved` gate
// restated by containment — and when a leaf would be a join the walk
// descends.
func TestScopeExtractionDeclinesLikeWalk(t *testing.T) {
	// Hand-poisoned table: a leaf that is a descendable Join.
	bad := &jtScopeTable{}
	bad.addLeaf(&Join{Type: JoinTypeInner}, false)
	if _, _, _, _, _, ok := extractScopeLeaves(bad); ok {
		t.Fatal("extractScopeLeaves accepted a descendable-Join leaf")
	}
	// Nil leaf.
	bad2 := &jtScopeTable{}
	bad2.addLeaf(nil, false)
	if _, _, _, _, _, ok := extractScopeLeaves(bad2); ok {
		t.Fatal("extractScopeLeaves accepted a nil leaf")
	}
}
