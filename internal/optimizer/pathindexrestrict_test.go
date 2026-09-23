package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// restrictionEqualityPrefix binds a GAPLESS leading prefix of the index to the
// leaf's `col = const` conjuncts (btree amoptionalkey, build_index_paths): a
// bound second column behind an unbound first one is not an index qual, and a
// non-equality or boolean-constant conjunct never binds. M0145-0029 slice 1.
func TestRestrictionEqualityPrefix(t *testing.T) {
	cat := saopFixture(t)
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "item"})
	if !ok {
		t.Fatal("item missing")
	}
	var composite *catalog.Index
	for _, idx := range cat.IndexesOnTable(tbl) {
		if idx.Name == "idx_item_sk_flag" {
			composite = idx
		}
	}
	if composite == nil {
		t.Fatal("idx_item_sk_flag missing")
	}
	col := func(i int) *ColumnRef { return &ColumnRef{Index: i, Name: tbl.Columns[i].Name} }
	eqSK := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqFlag := &BinaryOp{Op: parser.OpEq, Left: &IntegerConst{Value: 7}, Right: col(2)} // const on the left
	gtSK := &BinaryOp{Op: parser.OpGt, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqBool := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &BooleanConst{Value: true}}

	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{eqFlag, eqSK}); len(got) != 2 ||
		got[0].indexCol != 0 || got[0].local != Expr(eqSK) || got[1].indexCol != 1 || got[1].local != Expr(eqFlag) {
		t.Fatalf("both columns bound: want [sk, flag] in index order, got %+v", got)
	}
	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{eqFlag}); len(got) != 0 {
		t.Fatalf("second column alone must not bind (gapless prefix), got %d clauses", len(got))
	}
	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{eqSK}); len(got) != 1 {
		t.Fatalf("leading column alone is a 1-column prefix, got %d clauses", len(got))
	}
	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{gtSK, eqBool}); len(got) != 0 {
		t.Fatalf("range / boolean-constant conjuncts must not bind as equality keys, got %d", len(got))
	}
}

// The lowering reinstates the leaf's Filter WITHOUT the conjuncts the probe
// already applies (PG's qpqual excludes quals redundant with the index quals),
// omitting a wrapper that is left empty and keeping LeafLocal.
func TestRewrapLeafDroppingRemovesConsumedConjuncts(t *testing.T) {
	a := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0}, Right: &IntegerConst{Value: 1}}
	b := &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Index: 1}, Right: &IntegerConst{Value: 5}}
	inner := &Filter{Child: &SeqScan{}, Predicate: &BinaryOp{Op: parser.OpAnd, Left: a, Right: b}, LeafLocal: true}
	outer := &Filter{Child: inner, Predicate: a, LeafLocal: true}
	scan := &IndexScan{}

	got := rewrapLeafDropping(outer, scan, map[Expr]bool{a: true})
	f, ok := got.(*Filter)
	if !ok || f.Child != Node(scan) || f.Predicate != Expr(b) || !f.LeafLocal {
		t.Fatalf("want one LeafLocal Filter{b} directly over the scan, got %T", got)
	}
	if got := rewrapLeafDropping(&Filter{Child: &SeqScan{}, Predicate: a}, scan, map[Expr]bool{a: true}); got != Node(scan) {
		t.Fatalf("a wrapper left empty must be omitted, got %T", got)
	}
}

// An index-only path over a FILTERED leaf is admitted only when its
// equality-prefix index clauses consume every local conjunct: a residual qual
// would have to be re-evaluated over the narrowed schema, which the lowering
// cannot express. M0145-0029 slice 3.
func TestConsumingIndexClausesRequiresEveryConjunct(t *testing.T) {
	cat := saopFixture(t)
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "item"})
	if !ok {
		t.Fatal("item missing")
	}
	byName := map[string]*catalog.Index{}
	for _, idx := range cat.IndexesOnTable(tbl) {
		byName[idx.Name] = idx
	}
	composite, pkey := byName["idx_item_sk_flag"], byName["item_pkey"]
	if composite == nil || pkey == nil {
		t.Fatal("fixture indexes missing")
	}
	col := func(i int) *ColumnRef { return &ColumnRef{Index: i, Name: tbl.Columns[i].Name} }
	eqSK := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqFlag := &BinaryOp{Op: parser.OpEq, Left: col(2), Right: &IntegerConst{Value: 7}}
	gtFlag := &BinaryOp{Op: parser.OpGt, Left: col(2), Right: &IntegerConst{Value: 7}}

	if got := consumingIndexClauses(cat, tbl, composite, []Expr{eqFlag, eqSK}); len(got) != 2 {
		t.Fatalf("both conjuncts bound by the composite: want 2 clauses, got %d", len(got))
	}
	if got := consumingIndexClauses(cat, tbl, pkey, []Expr{eqSK}); len(got) != 1 {
		t.Fatalf("the single conjunct bound by the pkey: want 1 clause, got %d", len(got))
	}
	if got := consumingIndexClauses(cat, tbl, pkey, []Expr{eqSK, eqFlag}); got != nil {
		t.Fatalf("i_flag = 7 is a residual on item_pkey; want nil, got %d clauses", len(got))
	}
	if got := consumingIndexClauses(cat, tbl, composite, []Expr{eqSK, gtFlag}); got != nil {
		t.Fatalf("a range conjunct is not consumed by the equality prefix; want nil, got %d clauses", len(got))
	}
}

// The index-only lowering turns the consumed clauses into the probe (Key for
// one, Keys for several — the executor pads a short prefix) and leaves no
// leaf Filter behind: every conjunct was an index qual, as PG's Index Only
// Scan with `Index Cond` and no Filter.
func TestIndexOnlyScanPlanCarriesIndexQuals(t *testing.T) {
	cat := saopFixture(t)
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "item"})
	if !ok {
		t.Fatal("item missing")
	}
	var composite *catalog.Index
	for _, idx := range cat.IndexesOnTable(tbl) {
		if idx.Name == "idx_item_sk_flag" {
			composite = idx
		}
	}
	schema := make(Schema, len(tbl.Columns))
	for i, c := range tbl.Columns {
		schema[i] = SchemaColumn{Name: c.Name}
	}
	col := func(i int) *ColumnRef { return &ColumnRef{Index: i, Name: tbl.Columns[i].Name} }
	eqSK := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqFlag := &BinaryOp{Op: parser.OpEq, Left: col(2), Right: &IntegerConst{Value: 7}}
	leaf := &Filter{Child: &SeqScan{Table: tbl, schema: schema},
		Predicate: &BinaryOp{Op: parser.OpAnd, Left: eqSK, Right: eqFlag}, LeafLocal: true}
	rel := newRelOptInfo(1, 1000, 32)
	rel.baseLeaf = leaf

	clauses := consumingIndexClauses(cat, tbl, composite, extractFilterConjuncts(leaf))
	if len(clauses) != 2 {
		t.Fatalf("fixture: want 2 consuming clauses, got %d", len(clauses))
	}
	covered, _ := indexCoversColumns(composite, []catalog.Column{tbl.Columns[0], tbl.Columns[2]})
	p := &Path{Kind: PathIndexScan, Rel: rel, IndexInfo: composite, IndexScanDir: ForwardScanDirection,
		IndexOnly: true, IndexOnlyCovered: covered, IndexClauses: clauses}
	ios, ok := createIndexScanPlan(p).(*IndexOnlyScan)
	if !ok {
		t.Fatalf("want a bare *IndexOnlyScan (no residual Filter), got %T", createIndexScanPlan(p))
	}
	if len(ios.Keys) != 2 || ios.Keys[0] != clauses[0].key || ios.Keys[1] != clauses[1].key || ios.Key != nil {
		t.Fatalf("want Keys = [sk key, flag key] in index order, got Key=%v Keys=%v", ios.Key, ios.Keys)
	}

	// A one-column prefix probe uses Key.
	leaf1 := &Filter{Child: &SeqScan{Table: tbl, schema: schema}, Predicate: eqSK, LeafLocal: true}
	rel.baseLeaf = leaf1
	p.IndexClauses = consumingIndexClauses(cat, tbl, composite, extractFilterConjuncts(leaf1))
	if ios, ok := createIndexScanPlan(p).(*IndexOnlyScan); !ok || ios.Key == nil || ios.Keys != nil {
		t.Fatalf("one-column prefix: want Key set and Keys nil, got %#v", createIndexScanPlan(p))
	}
}
