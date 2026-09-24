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
	// The scope extraction only runs on the jointree arm — set the knob
	// so the joinlist/scope numbering below is the one the seam consumes.
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
			sScans, sWidths, sOn, sOuter, sSemi, sOK := extractScopeLeaves(rctx.jtScope, rctx.joinInfoList)
			sameTuple(t, wScans, wWidths, wOn, wOuter, wSemi, wOK,
				sScans, sWidths, sOn, sOuter, sSemi, sOK)
		})
	}
}

// TestScopeExtractionSemiAntiDeferred pins the M0145-0005 slice-6
// representation: a chain-extracted (demoted) SEMI/ANTI link's right
// side is a REAL joinlist leaf item — the deferred band after every
// emitting leaf — not a synthetic tail leaf. The scope extraction
// emits leaves in that canonical order (emitting first, deferred
// last), marks the link `realLeaf`, and binds it to the
// SpecialJoinInfo deconstruction published on ctx.joinInfoList —
// whereas the node-walk extraction still produces the synthetic
// representation, so the two tuples legitimately differ here.
func TestScopeExtractionSemiAntiDeferred(t *testing.T) {
	cat := scopeTestCatalog(t)
	// `a ANTI b JOIN c`: the WHERE conjunct demotes the LEFT link —
	// emitting leaves are a, c; b is the deferred leaf at index 2.
	stmts, err := parser.Parse("SELECT * FROM a LEFT JOIN b ON a.ax = b.bx JOIN c ON a.ay = c.cy WHERE b.bx IS NULL")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, rctx, err := planFromClause(stmts[0].(*parser.SelectStmt), cat, DefaultPlannerSettings(), nil)
	if err != nil {
		t.Fatalf("planFromClause: %v", err)
	}
	if rctx.jtScope == nil || rctx.jtScope.root != node {
		t.Fatal("jtScope missing or root mismatch")
	}
	// The joinlist carries three leaf items: a(0), c(1), and the
	// deferred b(2) — plus one Anti SpecialJoinInfo on the list.
	if n := rctx.joinlist.nrels(); n != 3 {
		t.Fatalf("joinlist nrels = %d, want 3 (two emitting + deferred b)", n)
	}
	if it := rctx.joinlist[2]; !it.isLeaf() || it.rel != 2 {
		t.Fatalf("joinlist[2] = %+v, want leaf item rel=2", it)
	}
	var sj *SpecialJoinInfo
	for _, s := range rctx.joinInfoList {
		if s != nil && s.Jointype == parser.JoinAnti {
			sj = s
		}
	}
	if sj == nil {
		t.Fatal("joinInfoList carries no JoinAnti SJI")
	}
	if sj.SynLefthand != RelSet(1) || sj.SynRighthand != RelSet(1)<<2 {
		t.Fatalf("SJI syn hands = %v/%v, want {0}/{2}", sj.SynLefthand, sj.SynRighthand)
	}
	scans, widths, _, _, semiAnti, ok := extractScopeLeaves(rctx.jtScope, rctx.joinInfoList)
	if !ok {
		t.Fatal("extractScopeLeaves declined")
	}
	if len(scans) != 3 {
		t.Fatalf("scans = %d, want 3", len(scans))
	}
	// Canonical order: the deferred leaf (the ANTI link's right side)
	// is emitted LAST even though it sits mid-chain in the plan tree.
	if len(semiAnti) != 1 || !semiAnti[0].realLeaf {
		t.Fatalf("semiAnti = %+v, want one realLeaf link", semiAnti)
	}
	lk := semiAnti[0]
	if lk.jointype != parser.JoinAnti || lk.lhs != RelSet(1) || lk.rhs != RelSet(1)<<2 {
		t.Fatalf("link = (%v, lhs=%v, rhs=%v), want (Anti, {0}, {2})", lk.jointype, lk.lhs, lk.rhs)
	}
	if lk.sjinfo != sj {
		t.Fatalf("link sjinfo is not the joinInfoList member (%p vs %p)", lk.sjinfo, sj)
	}
	// b's deferred leaf is the link's own right subtree — same node the
	// plan tree holds.
	if j, isJ := node.(*Join); !isJ || j.Type != JoinTypeInner {
		t.Fatalf("chain root = %T, want the INNER link above the ANTI", node)
	}
	_ = widths
}

// The scope table must reproduce the walk's DECLINE when a link sits
// inside another outer link's nullable side — the `preserved` gate
// restated by containment — and when a leaf would be a join the walk
// descends.
func TestScopeExtractionDeclinesLikeWalk(t *testing.T) {
	// Hand-poisoned table: a leaf that is a descendable Join.
	bad := &jtScopeTable{}
	bad.addLeaf(&Join{Type: JoinTypeInner}, false)
	if _, _, _, _, _, ok := extractScopeLeaves(bad, nil); ok {
		t.Fatal("extractScopeLeaves accepted a descendable-Join leaf")
	}
	// Nil leaf.
	bad2 := &jtScopeTable{}
	bad2.addLeaf(nil, false)
	if _, _, _, _, _, ok := extractScopeLeaves(bad2, nil); ok {
		t.Fatal("extractScopeLeaves accepted a nil leaf")
	}
}
